package aggregator

import (
	"context"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type chainReadWorker struct {
	avsproto.ChainWorkerClient
	chainID int64
}

func (w *chainReadWorker) GetBalance(context.Context, *avsproto.WorkerGetBalanceReq, ...grpc.CallOption) (*avsproto.WorkerGetBalanceResp, error) {
	return &avsproto.WorkerGetBalanceResp{BalanceWei: big.NewInt(w.chainID).String()}, nil
}

func TestGatewayReadersPreservedAfterLegacyDefaultInitialization(t *testing.T) {
	taskengine.ClearChainStateReaderRegistry()
	t.Cleanup(taskengine.ClearChainStateReaderRegistry)
	chains := []*config.ChainConfig{
		{ChainID: 11155111, Name: "sepolia"},
		{ChainID: 8453, Name: "base"},
		{ChainID: 84532, Name: "base-sepolia"},
		{ChainID: 1, Name: "ethereum"},
	}
	registry := NewChainRegistry(chains, chains[0].ChainID, testutil.GetLogger())
	agg := &Aggregator{config: &config.Config{IsGateway: true, Chains: chains}, chainRegistry: registry, logger: testutil.GetLogger()}
	for _, chain := range chains {
		registry.chains[chain.ChainID].Client = &chainReadWorker{chainID: chain.ChainID}
		agg.registerGatewayChainStateReader(chain, nil)
	}
	// This later single-chain default used to replace chains[0]'s worker with
	// a direct RPC reader. Check routing behavior for every configured chain.
	agg.registerLegacyChainStateReader(uint64(chains[0].ChainID), nil)
	for _, chain := range chains {
		reader := taskengine.GetChainStateReaderForChain(uint64(chain.ChainID))
		require.IsType(t, taskengine.NewWorkerChainStateReader(nil, chain.ChainID, 0), reader)
		balance, err := reader.GetBalance(t.Context(), common.Address{})
		require.NoError(t, err)
		require.Equal(t, chain.ChainID, balance.Int64(), "chain reads must reach their own worker")
	}
}

func TestLegacyReaderAndGatewayStartupFallbackRemainDirect(t *testing.T) {
	taskengine.ClearChainStateReaderRegistry()
	t.Cleanup(taskengine.ClearChainStateReaderRegistry)
	agg := &Aggregator{config: &config.Config{}, logger: testutil.GetLogger()}
	agg.registerLegacyChainStateReader(11155111, nil)
	require.IsType(t, taskengine.NewDirectChainStateReader(nil, 11155111), taskengine.GetChainStateReaderForChain(11155111))
	agg.config.IsGateway = true
	agg.registerGatewayChainStateReader(&config.ChainConfig{ChainID: 8453, Name: "base"}, nil)
	require.IsType(t, taskengine.NewDirectChainStateReader(nil, 8453), taskengine.GetChainStateReaderForChain(8453))
}
