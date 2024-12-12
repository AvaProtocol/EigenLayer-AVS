package trigger

import (
	"context"
	"sync"

	"math/big"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

type TriggerMark[T any] struct {
	TaskID string

	Marker T
}

type BlockTrigger struct {
	*CommonTrigger

	schedule map[int64]map[string]bool

	// channel that we will push the trigger information back
	triggerCh chan TriggerMark[int64]
}

func NewBlockTrigger(o *RpcOption, triggerCh chan TriggerMark[int64]) *BlockTrigger {
	var err error

	logger, err := sdklogging.NewZapLogger(sdklogging.Production)
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
	//func RegisterBlockListener(ctx context.Context, fn OnblockFunc) error {
	headers := make(chan *types.Header)
	sub, err := b.wsEthClient.SubscribeNewHead(ctx, headers)
	if err != nil {
		return err
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				err = nil
			case <-b.done:
				err = nil
			case err := <-sub.Err():
				b.logger.Errorf("getting error when subscribe to websocket rpc. start reconnecting", "errror", err)
				b.retryConnectToRpc()
				b.wsEthClient.SubscribeNewHead(ctx, headers)
			case header := <-headers:
				b.logger.Debug("detect new block, evaluate checks", "component", "blocktrigger", "block", header.Hash().Hex(), "number", header.Number)
				toRemove := []int{}
				for interval, tasks := range b.schedule {
					z := new(big.Int)
					if z.Mod(header.Number, big.NewInt(int64(interval))).Cmp(zero) == 0 {
						for taskID, _ := range tasks {
							b.triggerCh <- TriggerMark[int64]{
								TaskID: taskID,
								Marker: header.Number.Int64(),
							}

						}
						// Remove the task from the queue
						// toRemove = append(toRemove, interval)
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
