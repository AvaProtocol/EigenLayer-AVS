package taskengine

import (
	"context"
	"math/big"
	"testing"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

// stubCreationChainReader satisfies ChainStateReader by embedding the interface
// (nil) and overriding only the two methods estimateWalletCreationGas calls —
// so the test needs no live RPC. EstimateGas records the CallMsg it was handed,
// which is where the init code the estimate prices lands.
type stubCreationChainReader struct {
	ChainStateReader
	gasPrice    *big.Int
	estimateGas uint64
	captured    ethereum.CallMsg
}

func (s *stubCreationChainReader) SuggestGasPrice(context.Context) (*big.Int, error) {
	return s.gasPrice, nil
}

func (s *stubCreationChainReader) EstimateGas(_ context.Context, msg ethereum.CallMsg) (uint64, error) {
	s.captured = msg
	return s.estimateGas, nil
}

// The estimate must build init code from a placeholder owner, not from the
// wallet address. Passing the wallet as its own owner (the fixed bug) prices
// creation of a different, wallet-owned account; the gas happens to match,
// which is why it was latent, so only the calldata reveals it.
func TestEstimateWalletCreationGas_BuildsInitCodeFromDummyOwnerNotWallet(t *testing.T) {
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err)

	stub := &stubCreationChainReader{gasPrice: big.NewInt(1_000_000_000), estimateGas: 300_000}
	fe := &FeeEstimator{
		logger: logger,
		chain:  stub,
		// MA v2 so EffectiveFactory resolves from config alone (no RPC).
		smartWalletConfig: &config.SmartWalletConfig{AccountProvider: config.AccountProviderModularAccountV2},
	}
	wallet := common.HexToAddress("0x00000000000000000000000000000000000000a1")

	_, _, err = fe.estimateWalletCreationGas(context.Background(), wallet)
	require.NoError(t, err)

	factory := aa.MAv2FactoryAddress()
	_, dummyOwnerData, err := aa.DeriveInitCodeAuto(feeEstimationDummyOwner, factory, big.NewInt(0))
	require.NoError(t, err)
	_, walletOwnedData, err := aa.DeriveInitCodeAuto(wallet, factory, big.NewInt(0))
	require.NoError(t, err)

	require.NotNil(t, stub.captured.To)
	require.Equal(t, factory, *stub.captured.To, "estimate must target the effective factory")
	require.Equal(t, dummyOwnerData, stub.captured.Data,
		"init code must be built from feeEstimationDummyOwner")
	require.NotEqual(t, walletOwnedData, stub.captured.Data,
		"must not price an account owned by the wallet itself — the bug this fix removes")
}
