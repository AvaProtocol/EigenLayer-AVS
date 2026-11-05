package config

import (
	"encoding/hex"
	"io"
	"testing"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

func TestBudgetLogBlockRangeLoadsPerChain(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	var raw ConfigRaw
	require.NoError(t, yaml.Unmarshal([]byte(`
smart_wallet:
  budget_log_block_range: 9999
chains:
  - chain_id: 11155111
    name: sepolia
    worker_addr: localhost:50051
    smart_wallet:
      budget_log_block_range: 1000
  - chain_id: 8453
    name: base
    worker_addr: localhost:50052
    smart_wallet:
      budget_log_block_range: 500
`), &raw))
	require.EqualValues(t, 9999, raw.SmartWallet.BudgetLogBlockRange)
	for i, want := range []uint64{1000, 500} {
		raw.Chains[i].SmartWallet.ControllerPrivateKey = hex.EncodeToString(crypto.FromECDSA(key))
		chain, err := parseChainConfig(raw.Chains[i], sdklogging.NewTextSLogger(io.Discard, nil))
		require.NoError(t, err)
		require.Equal(t, want, chain.SmartWallet.BudgetLogBlockRange)
	}
	require.Error(t, yaml.Unmarshal([]byte("budget_log_block_range: -1"), &SmartWalletConfigRaw{}))
}
