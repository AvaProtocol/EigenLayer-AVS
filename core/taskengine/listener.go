package taskengine

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
)

type OnblockFunc func(*types.Block) error

func RegisterBlockListener(ctx context.Context, fn OnblockFunc) error {
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
			sub, err = wsEthClient.SubscribeNewHead(ctx, headers)
			if err != nil {
				logger.Errorf("fail to subscribe to rpc for new block", "err", err)
			}
		case header := <-headers:
			logger.Info("detect new block, evaluate checks", "component", "taskengine", "block", header.Hash().Hex())

			// ideally we can just use wsEthClient but some particular websocket such as minato doesn't support that
			//block, err := wsEthClient.BlockByHash(context.Background(), header.Hash())
			block, err := rpcConn.BlockByHash(context.Background(), header.Hash())
			if err != nil {
				logger.Errorf("error when fetching new block from websocket", "err", err)
				// TODO: report error in metric
				// The operator will skip run this time
			} else {
				fn(block)
			}
		}
	}
}
