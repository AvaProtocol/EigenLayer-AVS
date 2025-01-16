package trigger

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/AvaProtocol/ap-avs/core/taskengine/macros"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/dop251/goja"
	"github.com/samber/lo"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

type EventMark struct {
	BlockNumber uint64
	LogIndex    uint
	TxHash      string
}

type Check struct {
	TaskMetadata *avsproto.SyncMessagesResp_TaskMetadata

	Program string
	Matcher []*avsproto.EventCondition_Matcher
}

type EventTrigger struct {
	*CommonTrigger

	checks sync.Map

	// channel that we will push the trigger information back
	triggerCh chan TriggerMetadata[EventMark]
}

func NewEventTrigger(o *RpcOption, triggerCh chan TriggerMetadata[EventMark]) *EventTrigger {
	var err error

	logger, err := sdklogging.NewZapLogger(sdklogging.Production)
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
		Program:      evt.GetExpression(),
		Matcher:      evt.GetMatcher(),
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
	query := ethereum.FilterQuery{}
	sub, err := evtTrigger.wsEthClient.SubscribeFilterLogs(context.Background(), ethereum.FilterQuery{}, logs)
	if err != nil {
		return err
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				err = nil
			case <-evtTrigger.done:
				err = nil
			case err := <-sub.Err():
				evtTrigger.logger.Errorf("getting error when subscribe to websocket rpc. start reconnecting", "errror", err)
				evtTrigger.retryConnectToRpc()
				sub, err = evtTrigger.wsEthClient.SubscribeFilterLogs(context.Background(), query, logs)
			case event := <-logs:
				evtTrigger.logger.Debug("detect new event, evaluate checks", "event", event.Topics[0], "contract", event.Address)
				// TODO: implement hint to avoid scan all checks
				toRemove := []string{}

				evtTrigger.checks.Range(func(key any, value any) bool {
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
	if check.Program != "" {
		// This is the advance trigger with js evaluation based on trigger data
		jsvm := goja.New()
		envs := macros.GetEnvs(map[string]interface{}{
			"trigger1": map[string]interface{}{
				"data": map[string]interface{}{
					"address": strings.ToLower(event.Address.Hex()),
					"topics": lo.Map[common.Hash, string](event.Topics, func(topic common.Hash, _ int) string {
						return "0x" + strings.ToLower(strings.TrimLeft(topic.String(), "0x0"))
					}),
					"data":    "0x" + common.Bytes2Hex(event.Data),
					"tx_hash": event.TxHash,
				},
			},
		})
		for k, v := range envs {
			jsvm.Set(k, v)
		}

		result, err := jsvm.RunString(check.Program)

		if err != nil {
			return false, err
		}

		return result.Export().(bool), err

	}

	var err error = nil
	if len(check.Matcher) > 0 {
		// This is the simpler trigger. It's essentially an anyof
		return lo.SomeBy(check.Matcher, func(x *avsproto.EventCondition_Matcher) bool {
			if len(x.Value) == 0 {
				err = fmt.Errorf("matcher value is empty")
				return false
			}

			switch x.Type {
			case "topics":
				// Matching based on topic of transaction
				topics := lo.Map[common.Hash, string](event.Topics, func(topic common.Hash, _ int) string {
					return "0x" + strings.ToLower(strings.TrimLeft(topic.String(), "0x0"))
				})
				match := true
				for i, v := range x.Value {
					match = match && (v == "" || strings.EqualFold(topics[i], v))
					if !match {
						return false
					}
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

	err = fmt.Errorf("invalid check data: both matcher or trigger is missing")
	return false, err
}
