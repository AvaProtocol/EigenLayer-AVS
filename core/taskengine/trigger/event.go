package trigger

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/AvaProtocol/ap-avs/core/taskengine/macros"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/ginkgoch/godash/v2"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	avspb "github.com/AvaProtocol/ap-avs/protobuf"
)

type EventMark struct {
	BlockNumber uint64
	LogIndex    uint
	TxHash      string
}

type EventTrigger struct {
	*CommonTrigger

	checks sync.Map

	// channel that we will push the trigger information back
	triggerCh chan TriggerMark[EventMark]
}

func NewEventTrigger(o *RpcOption, triggerCh chan TriggerMark[EventMark]) *EventTrigger {
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
func (t *EventTrigger) AddCheck(check *avspb.SyncMessagesResp_TaskMetadata) error {
	envs := macros.GetEnvs(map[string]interface{}{
		"trigger1": map[string]interface{}{
			"data": map[string]interface{}{
				"address": "dummy",
				"topics": godash.Map([]common.Hash{}, func(topic common.Hash) string {
					return "0x"
				}),
				"data":    "0x",
				"tx_hash": "dummy",
			},
		},
	})
	program, err := expr.Compile(check.GetTrigger().GetEvent().GetExpression(), expr.Env(envs), expr.AsBool())
	if err != nil {
		return err
	}

	t.checks.Store(check.TaskId, program)

	return nil
}

func (t *EventTrigger) RemoveCheck(id string) error {
	t.checks.Delete(id)

	return nil
}

func (evt *EventTrigger) Run(ctx context.Context) error {
	logs := make(chan types.Log)
	query := ethereum.FilterQuery{}
	sub, err := evt.wsEthClient.SubscribeFilterLogs(context.Background(), ethereum.FilterQuery{}, logs)
	if err != nil {
		return err
	}

	// hardcode to test quick
	// TODO: rever
	//event2, err := testutil.GetEventForTx("0x8f7c1f698f03d6d32c996b679ea1ebad45bbcdd9aa95d250dda74763cc0f508d", 1)

	go func() {
		for {
			select {
			case <-ctx.Done():
				err = nil
			case <-evt.done:
				err = nil
			case err := <-sub.Err():
				evt.logger.Errorf("getting error when subscribe to websocket rpc. start reconnecting", "errror", err)
				evt.retryConnectToRpc()
				sub, err = evt.wsEthClient.SubscribeFilterLogs(context.Background(), query, logs)
			case event := <-logs:
				evt.logger.Info("detect new event, evaluate checks", "component", "eventrigger", "event", event)
				// TODO: implement hint to avoid scan all checks
				toRemove := []string{}

				//event = *event2

				evt.checks.Range(func(key any, value any) bool {
					evt.logger.Info("evaluate with event", event)
					if evt.shutdown {
						return false
					}
					evt.logger.Info("call evt.Evaluate", event)

					if hit, err := evt.Evaluate(&event, value.(*vm.Program)); err == nil && hit {
						evt.logger.Infof("check hit, flush to channel", "taskid", key)
						evt.triggerCh <- TriggerMark[EventMark]{
							TaskID: key.(string),
							Marker: EventMark{
								BlockNumber: event.BlockNumber,
								LogIndex:    event.Index,
								TxHash:      event.TxHash.String(),
							},
						}

						toRemove = append(toRemove, key.(string))
					} else {
						fmt.Println("error evaluate", hit, err)
					}

					return true
				})

				if len(toRemove) > 0 {
					for _, v := range toRemove {
						evt.checks.Delete(v)
					}
				}
			}
		}
	}()

	return err
}

func (evt *EventTrigger) Evaluate(event *types.Log, program *vm.Program) (bool, error) {
	envs := macros.GetEnvs(map[string]interface{}{
		"trigger1": map[string]interface{}{
			"data": map[string]interface{}{
				"address": strings.ToLower(event.Address.Hex()),
				"topics": godash.Map(event.Topics, func(topic common.Hash) string {
					return "0x" + strings.ToLower(strings.TrimLeft(topic.String(), "0x0"))
				}),
				"data":    "0x" + common.Bytes2Hex(event.Data),
				"tx_hash": event.TxHash,
			},
		},
	})

	fmt.Println("Evaluate", program.Source().String(), "envs", envs, event)
	//program, err := expr.Compile(expression, expr.Env(envs), expr.AsBool())

	result, err := expr.Run(program, envs)

	if err != nil {
		return false, err
	}

	return result.(bool), err
}
