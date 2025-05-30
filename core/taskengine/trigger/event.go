package trigger

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/samber/lo"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

var (
	// To reduce api call we listen to these topics only
	// a better idea is to only subscribe to what we need and re-load when new trigger is added
	whitelistTopics = [][]common.Hash{
		{
			common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"), // erc20 transfer
			//common.HexToHash("0x49628fd1471006c1482da88028e9ce4dbb080b815c9b0344d39e5a8e6ec1419f"), // UserOp
			//common.HexToHash("0x8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925"), // approve
		},
	}
)

type EventMark struct {
	BlockNumber uint64
	LogIndex    uint
	TxHash      string
}

type Check struct {
	TaskMetadata *avsproto.SyncMessagesResp_TaskMetadata

	Program string
	Matcher []*avsproto.EventTrigger_Matcher
}

type EventTrigger struct {
	*CommonTrigger

	checks sync.Map

	// channel that we will push the trigger information back
	triggerCh chan TriggerMetadata[EventMark]
}

func NewEventTrigger(o *RpcOption, triggerCh chan TriggerMetadata[EventMark], logger sdklogging.Logger) *EventTrigger {
	var err error

	b := EventTrigger{
		CommonTrigger: &CommonTrigger{
			done:      make(chan bool),
			shutdown:  false,
			rpcOption: o,
			logger:    logger,
		},

		triggerCh: triggerCh,
		checks:    sync.Map{},
	}

	b.ethClient, err = ethclient.Dial(o.RpcURL)
	if err != nil {
		panic(err)
	}

	b.wsEthClient, err = ethclient.Dial(o.WsRpcURL)

	if err != nil {
		panic(err)
	}

	return &b
}

// TODO: track remainExecution and expriedAt before merge
func (t *EventTrigger) AddCheck(check *avsproto.SyncMessagesResp_TaskMetadata) error {
	evt := check.GetTrigger().GetEvent()

	t.checks.Store(check.TaskId, &Check{
		Program:      evt.GetConfig().GetExpression(),
		Matcher:      evt.GetConfig().GetMatcher(),
		TaskMetadata: check,
	})

	return nil
}

func (t *EventTrigger) RemoveCheck(id string) error {
	t.checks.Delete(id)

	return nil
}

func (evtTrigger *EventTrigger) Run(ctx context.Context) error {
	logs := make(chan types.Log)
	query := ethereum.FilterQuery{
		Topics: whitelistTopics,
	}

	sub, err := evtTrigger.wsEthClient.SubscribeFilterLogs(context.Background(), query, logs)
	evtTrigger.logger.Info("subscribing with filter", "topics", whitelistTopics)

	if err != nil {
		return err
	}

	go func() {
		defer sub.Unsubscribe()
		for {
			select {
			case <-ctx.Done():
				err = nil
			case <-evtTrigger.done:
				err = nil
			case err := <-sub.Err():
				if err == nil {
					continue
				}
				evtTrigger.logger.Error("error when subscribing to websocket rpc, retrying", "rpc", evtTrigger.rpcOption.WsRpcURL, "error", err)
				if sub != nil {
					sub.Unsubscribe()
				}

				if evtTrigger.wsEthClient != nil {
					evtTrigger.wsEthClient.Close()
				}

				if err := evtTrigger.retryConnectToRpc(); err != nil {
					evtTrigger.logger.Error("failed to reconnect to RPC", "error", err)
				}
				sub, err = evtTrigger.wsEthClient.SubscribeFilterLogs(context.Background(), query, logs)
			case event := <-logs:
				evtTrigger.logger.Debug("detect new event, evaluate checks", "event", event.Topics, "contract", event.Address, "tx", event.TxHash)
				// TODO: implement hint to avoid scan all checks
				toRemove := []string{}
				evtTrigger.progress += 1

				startTime := time.Now()
				checksCount := 0
				evtTrigger.checks.Range(func(key any, value any) bool {
					checksCount++
					if evtTrigger.shutdown {
						return false
					}

					check := value.(*Check)
					if hit, err := evtTrigger.Evaluate(&event, check); err == nil && hit {
						evtTrigger.logger.Info("check hit, notify aggregator", "task_id", key)
						evtTrigger.triggerCh <- TriggerMetadata[EventMark]{
							TaskID: key.(string),
							Marker: EventMark{
								BlockNumber: event.BlockNumber,
								LogIndex:    event.Index,
								TxHash:      event.TxHash.String(),
							},
						}

						evtTrigger.logger.Debug("check hit",
							"check", key,
							"tx_hash", event.TxHash,
						)

						// if check.metadata.Remain >= 0 {
						// 	if check.metadata.Remain == 1 {
						// 		toRemove = append(toRemove, key.(string))
						// 		check.metadata.Remain = -1
						// 	}
						// }
					}

					// We do want to continue other check no matter what outcome of previous one
					return true
				})

				duration := time.Since(startTime)
				evtTrigger.logger.Info("completed check evaluations",
					"checks_count", checksCount,
					"duration_ms", duration.Milliseconds(),
					"checks_per_second", float64(checksCount)/(float64(duration.Nanoseconds())/1e9))

				if len(toRemove) > 0 {
					for _, v := range toRemove {
						evtTrigger.checks.Delete(v)
					}
				}
			}
		}
	}()

	return err
}

func (evt *EventTrigger) Evaluate(event *types.Log, check *Check) (bool, error) {
	if event == nil {
		return false, fmt.Errorf("event is nil")
	}

	var err error = nil

	if len(check.Matcher) > 0 {
		// This is the simpler trigger. It's essentially an anyof
		return lo.SomeBy(check.Matcher, func(x *avsproto.EventTrigger_Matcher) bool {
			if len(x.Value) == 0 {
				err = fmt.Errorf("matcher value is empty")
				return false
			}

			switch x.Type {
			case "topics":
				// Matching based on topic of transaction
				topics := lo.Map[common.Hash, string](event.Topics, func(topic common.Hash, _ int) string {
					return "0x" + strings.ToLower(strings.TrimLeft(topic.String(), "0x"))
				})

				match := true
				// In Topics matching, this will be the array of topics. an element that is empty is skip
				for i, v := range x.Value {
					if v == "" || v == "0x" {
						continue
					}

					match = match && strings.EqualFold(topics[i], v)
				}
				return match
			case "address":
				// Matching base on token contract that emit the event
				return strings.EqualFold(event.Address.String(), x.Value[0])
			}

			// Unsupport type
			err = fmt.Errorf("unsupport matcher type: %s", x.Type)
			return false
		}), err
	}

	if check.Program != "" {
		// This is the advance trigger with js evaluation based on trigger data
		triggerVarName := check.TaskMetadata.GetTrigger().GetName()

		jsvm := taskengine.NewGojaVM()

		envs := macros.GetEnvs(map[string]interface{}{})
		for k, v := range envs {
			if err := jsvm.Set(k, v); err != nil {
				return false, fmt.Errorf("failed to set macro env in JS VM: %w", err)
			}
		}

		triggerData := map[string]interface{}{
			"data": map[string]interface{}{
				"address": strings.ToLower(event.Address.Hex()),
				"topics": lo.Map[common.Hash, string](event.Topics, func(topic common.Hash, _ int) string {
					return "0x" + strings.ToLower(strings.TrimLeft(topic.String(), "0x"))
				}),
				"data":    "0x" + common.Bytes2Hex(event.Data),
				"tx_hash": event.TxHash,
			},
		}
		if err := jsvm.Set(triggerVarName, triggerData); err != nil {
			return false, fmt.Errorf("failed to set trigger data in JS VM: %w", err)
		}

		result, err := jsvm.RunString(check.Program)

		if err != nil {
			return false, err
		}

		evalutationResult, ok := result.Export().(bool)
		if !ok {
			return false, fmt.Errorf("the expression `%s` didn't return a boolean but %v", check.Program, result.Export())
		}

		return evalutationResult, err
	}

	err = fmt.Errorf("invalid event trigger check: both matcher or expression are missing or empty")
	return false, err
}
