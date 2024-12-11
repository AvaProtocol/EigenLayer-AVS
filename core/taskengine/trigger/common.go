package trigger

import (
	"math/big"
	"sync"
	"time"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/ethclient"
)

var (
	zero = big.NewInt(0)
)

type RpcOption struct {
	RpcURL   string
	WsRpcURL string
}

type CommonTrigger struct {
	wsEthClient *ethclient.Client
	ethClient   *ethclient.Client
	rpcOption   *RpcOption

	logger sdklogging.Logger

	// channel to track shutdown
	done     chan bool
	shutdown bool
	mu       sync.Mutex
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
