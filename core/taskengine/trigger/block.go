package trigger

import (
	"context"
	"fmt"
	"sync"

	"math/big"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

type TriggerMetadata[T any] struct {
	TaskID string

	Marker T
}

type BlockTrigger struct {
	*CommonTrigger

	schedule map[int64]map[string]bool

	// channel that we will push the trigger information back
	triggerCh chan TriggerMetadata[int64]
}

func NewBlockTrigger(o *RpcOption, triggerCh chan TriggerMetadata[int64], logger sdklogging.Logger) *BlockTrigger {
	var err error

	//logger, err := sdklogging.NewZapLogger(sdklogging.Production)
	b := BlockTrigger{
		CommonTrigger: &CommonTrigger{
			done:      make(chan bool),
			shutdown:  false,
			rpcOption: o,

			logger: logger,
			mu:     sync.Mutex{},
		},
		schedule:  make(map[int64]map[string]bool),
		triggerCh: triggerCh,
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

func (b *BlockTrigger) AddCheck(check *avsproto.SyncMessagesResp_TaskMetadata) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	interval := check.GetTrigger().GetBlock().GetInterval()
	if _, ok := b.schedule[interval]; !ok {
		b.schedule[interval] = map[string]bool{
			check.TaskId: true,
		}
	} else {
		b.schedule[interval][check.TaskId] = true
	}

	return nil
}

func (b *BlockTrigger) Remove(check *avsproto.SyncMessagesResp_TaskMetadata) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	interval := check.GetTrigger().GetBlock().GetInterval()
	if _, ok := b.schedule[interval]; ok {
		delete(b.schedule[interval], check.TaskId)
	}

	return nil
}

func (b *BlockTrigger) Run(ctx context.Context) error {
	headers := make(chan *types.Header)
	sub, err := b.wsEthClient.SubscribeNewHead(ctx, headers)
	if err != nil {
		return fmt.Errorf("failed to subscribe to new headers: %w", err)
	}
	b.logger.Info("subscribed for new blocks", "rpc", b.rpcOption.WsRpcURL)

	go func() {
		//defer sub.Unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case <-b.done:
				return
			case err := <-sub.Err():
				if err != nil {
					b.logger.Error("error when subscribing to websocket RPC, retrying",
						"rpc", b.rpcOption.WsRpcURL,
						"error", err,
						"component", "block")
					if sub != nil {
						sub.Unsubscribe()
					}

					if b.wsEthClient != nil {
						b.wsEthClient.Close()
					}

					if err := b.retryConnectToRpc(); err != nil {
						b.logger.Error("failed to reconnect to RPC", "error", err)
					}
					sub, err = b.wsEthClient.SubscribeNewHead(ctx, headers)
				}
			case header := <-headers:
				b.logger.Debug("detected new block, evaluating checks", "component", "blocktrigger", "block", header.Hash().Hex(), "number", header.Number)
				b.progress = header.Number.Int64()

				toRemove := []int{}
				for interval, tasks := range b.schedule {
					z := new(big.Int)
					if z.Mod(header.Number, big.NewInt(int64(interval))).Cmp(zero) == 0 {
						for taskID := range tasks {
							b.triggerCh <- TriggerMetadata[int64]{
								TaskID: taskID,
								Marker: header.Number.Int64(),
							}

						}
					}
				}

				for _, v := range toRemove {
					delete(b.schedule, int64(v))
				}
			}
		}
	}()

	return err
}
