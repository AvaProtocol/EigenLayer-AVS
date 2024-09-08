package taskengine

import (
	"context"
	"time"

	"github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/core/types"
)

type OnblockFunc func(*types.Block) error

var (
	logger logging.Logger
)

func SetLogger(mylogger logging.Logger) {
	logger = mylogger
}

func RegisterBlockListener(ctx context.Context,
	fn OnblockFunc,
) error {
	headers := make(chan *types.Header)
	sub, err := wsEthClient.SubscribeNewHead(ctx, headers)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-sub.Err():
			// TODO: look into error and consider re-connect or wait
			logger.Errorf("error when fetching new block from websocket, retry in 15 seconds", "err", err)
			time.Sleep(15 * time.Second)
			retryWsRpc()
		case header := <-headers:
			logger.Info("detect new block, evaluate checks", "component", "taskengine", "block", header.Hash().Hex())

			block, err := wsEthClient.BlockByHash(context.Background(), header.Hash())
			if err != nil {
				logger.Errorf("error when fetching new block from websocket", "err", err)
				// TODO: report error in metric
				// The operator will skip run this time
				fn(nil)
			} else {
				fn(block)
			}
		}
	}
}
