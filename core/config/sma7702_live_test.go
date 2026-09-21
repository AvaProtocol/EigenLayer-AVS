//go:build integration
// +build integration

package config

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

// Live eth_getCode of the SMA-7702 delegate vs the B0 pin. On-demand, not
// per-PR CI. Missing RPC is a fail, not a skip. Does not import testutil
// (that package imports config — an import cycle in this test).
//
//	SEPOLIA_RPC_URL or ETH_RPC_URL, BASE_RPC_URL
//	go test -tags=integration ./core/config -run TestSMA7702PinMatchesLiveBytecode -v -count=1
func TestSMA7702PinMatchesLiveBytecode_SepoliaAndBase(t *testing.T) {
	sepoliaURL := firstLiveRPC("SEPOLIA_RPC_URL", "ETH_RPC_URL")
	if sepoliaURL == "" {
		if k := strings.TrimSpace(os.Getenv("ALCHEMY_API_KEY")); k != "" {
			sepoliaURL = "https://eth-sepolia.g.alchemy.com/v2/" + k
		}
	}
	require.NotEmpty(t, sepoliaURL, "SEPOLIA_RPC_URL or ETH_RPC_URL must be set: a live pin check with no chain proves nothing")

	sepolia, err := ethclient.Dial(sepoliaURL)
	require.NoError(t, err, "cannot reach Sepolia RPC")
	t.Cleanup(func() { sepolia.Close() })
	sepoliaID, err := sepolia.ChainID(context.Background())
	require.NoError(t, err)
	require.Equal(t, SMA7702ChainSepolia, sepoliaID.Int64(), "SEPOLIA_RPC_URL/ETH_RPC_URL must be Ethereum Sepolia")
	requireLiveSMA7702Pin(t, sepolia, SMA7702ChainSepolia, "Sepolia")

	baseURL := strings.TrimSpace(os.Getenv("BASE_RPC_URL"))
	require.NotEmpty(t, baseURL, "BASE_RPC_URL must be set: the pin is byte-identical on Base")
	base, err := ethclient.Dial(baseURL)
	require.NoError(t, err, "cannot reach BASE_RPC_URL")
	t.Cleanup(func() { base.Close() })
	id, err := base.ChainID(context.Background())
	require.NoError(t, err)
	require.Equal(t, SMA7702ChainBase, id.Int64(), "BASE_RPC_URL must be Base mainnet")
	requireLiveSMA7702Pin(t, base, SMA7702ChainBase, "Base")
}

func requireLiveSMA7702Pin(t *testing.T, client *ethclient.Client, chainID int64, label string) {
	t.Helper()
	impl, err := client.CodeAt(context.Background(), SMA7702Delegate(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, impl, "%s: no code at SMA-7702 delegate %s", label, SMA7702DelegateAddressHex)
	require.Equal(t, 23741, len(impl), "%s: impl bytecode length drifted from B0", label)

	c := &SmartWalletConfig{
		ChainID:         chainID,
		SMA7702Delegate: SMA7702Delegate(),
		SMA7702ImplHash: SMA7702ImplHash(),
	}
	// Designation is on the EOA; the pin's hash half is keccak256(impl).
	// Feed a synthetic designation so AssertSMA7702Designation checks the
	// live impl against the stored hash.
	designation := make([]byte, 23)
	designation[0], designation[1], designation[2] = 0xef, 0x01, 0x00
	copy(designation[3:], SMA7702Delegate().Bytes())
	require.NoError(t, c.AssertSMA7702Designation(designation, impl),
		"%s: live keccak256(impl) does not match sma_7702_impl_hash", label)
	t.Logf("%s SMA-7702 %s code=%d bytes hash=%s", label, SMA7702Delegate().Hex(), len(impl), SMA7702ImplHash().Hex())
}

func firstLiveRPC(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}
