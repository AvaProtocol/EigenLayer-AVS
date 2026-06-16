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

	// a counter to track progress of the trigger. the counter will increase everytime a processing happen
	progress int64
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
}

func (b *CommonTrigger) Shutdown() {
	b.shutdown = true
	b.done <- true
}

// Close releases the trigger's RPC/WS clients without going through the
// Run/Shutdown lifecycle. Unlike Shutdown it does NOT signal the done
// channel, so it's safe to call on a trigger that was constructed but never
// Run — e.g. when multi-chain operator setup soft-skips a sibling chain after
// this trigger's constructor already dialed its clients.
func (b *CommonTrigger) Close() {
	if b.ethClient != nil {
		b.ethClient.Close()
	}
	if b.wsEthClient != nil {
		b.wsEthClient.Close()
	}
}

func (b *CommonTrigger) GetProgress() int64 {
	return b.progress
}
