package trigger

import (
	"context"
	"sync"
	"time"

	"math/big"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

var (
	zero = big.NewInt(0)
)

type RpcOption struct {
	RpcURL   string
	WsRpcURL string
}

type TriggerMark[T any] struct {
	TaskID string

	Marker T
}

type CommonTrigger struct {
	wsEthClient *ethclient.Client
	ethClient   *ethclient.Client
	rpcOption   *RpcOption

	logger sdklogging.Logger

	// channel to track shutdown
	done     chan bool
	shutdown bool
	mu       *sync.Mutex
}

type BlockTrigger struct {
	*CommonTrigger

	schedule map[int][]string

	// channel that we will push the trigger information back
	triggerCh chan TriggerMark[string]
}

func NewBlockTrigger(o *RpcOption, triggerCh chan TriggerMark[string]) *BlockTrigger {
	var err error

	logger, err := sdklogging.NewZapLogger(sdklogging.Production)
	b := BlockTrigger{
		CommonTrigger: &CommonTrigger{
			done:      make(chan bool),
			shutdown:  false,
			rpcOption: o,

			logger: logger,
			mu:     &sync.Mutex{},
		},
		schedule:  make(map[int][]string),
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

func (b *CommonTrigger) retryConnectToRpc() error {
	for {
		if b.shutdown {
			return nil
		}

		conn, err := ethclient.Dial(b.rpcOption.WsRpcURL)
		if err == nil {
			b.wsEthClient = conn
			return nil
		}
		b.logger.Errorf("cannot establish websocket client for RPC, retry in 15 seconds", "err", err)
		time.Sleep(15 * time.Second)
	}

	return nil
}

func (b *CommonTrigger) Shutdown() {
	b.shutdown = true
	b.done <- true
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
				b.logger.Info("detect new block, evaluate checks", "component", "blocktrigger", "block", header.Hash().Hex(), "number", header.Number)

				toRemove := []int{}
				for interval, tasks := range b.schedule {
					z := new(big.Int)
					if z.Mod(header.Number, big.NewInt(int64(interval))).Cmp(zero) == 0 {
						for _, taskID := range tasks {
							b.triggerCh <- TriggerMark[string]{
								TaskID: taskID,
								Marker: header.Number.String(),
							}

						}
						// Remove the task from the queue
						toRemove = append(toRemove, interval)
					}
				}

				for _, v := range toRemove {
					delete(b.schedule, v)
				}
			}
		}
	}()
	return err
}
