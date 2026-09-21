package preset

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

func designation(delegate common.Address) []byte {
	out := make([]byte, 23)
	out[0], out[1], out[2] = 0xef, 0x01, 0x00
	copy(out[3:], delegate.Bytes())
	return out
}

func sepoliaPin(execute bool) *config.SmartWalletConfig {
	return &config.SmartWalletConfig{
		ChainID:         config.SMA7702ChainSepolia,
		EOA7702Execute:  execute,
		SMA7702Delegate: config.SMA7702Delegate(),
		SMA7702ImplHash: config.SMA7702ImplHash(),
	}
}

func TestEOA7702SendAllowed(t *testing.T) {
	owner := common.HexToAddress("0x00000000000000000000000000000000000000e0")
	derived := common.HexToAddress("0x00000000000000000000000000000000000000d1")
	impl := []byte("not-the-pin")
	// AssertSMA7702Designation hashes implCode; use a fixture whose hash we set on cfg.
	cfg := sepoliaPin(true)
	goodImpl := []byte("sma-7702-impl-fixture")
	cfg.SMA7702ImplHash = crypto.Keccak256Hash(goodImpl)

	t.Run("derived sender is not an EOA attempt", func(t *testing.T) {
		ok, err := eoa7702SendAllowed(cfg, owner, derived, true, designation(cfg.SMA7702Delegate), goodImpl)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("owner as sender with flag off is refused, not a factory mismatch", func(t *testing.T) {
		off := sepoliaPin(false)
		ok, err := eoa7702SendAllowed(off, owner, owner, true, designation(off.SMA7702Delegate), goodImpl)
		require.False(t, ok)
		require.ErrorContains(t, err, "eoa_7702_execute is false")
	})
	t.Run("flag on without stored record does not fall back", func(t *testing.T) {
		ok, err := eoa7702SendAllowed(cfg, owner, owner, false, designation(cfg.SMA7702Delegate), goodImpl)
		require.False(t, ok)
		require.ErrorContains(t, err, "no eoa_7702 wallet record")
		require.ErrorContains(t, err, "do not fall back")
	})
	t.Run("flag on, stored, bad designation", func(t *testing.T) {
		ok, err := eoa7702SendAllowed(cfg, owner, owner, true, []byte{0x00}, impl)
		require.False(t, ok)
		require.ErrorContains(t, err, "EOA_DELEGATION_MISSING")
	})
	t.Run("flag on, stored, K13 passes", func(t *testing.T) {
		ok, err := eoa7702SendAllowed(cfg, owner, owner, true, designation(cfg.SMA7702Delegate), goodImpl)
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("flag on, base-sepolia refused", func(t *testing.T) {
		c := sepoliaPin(true)
		c.ChainID = 84532
		c.SMA7702ImplHash = crypto.Keccak256Hash(goodImpl)
		ok, err := eoa7702SendAllowed(c, owner, owner, true, designation(c.SMA7702Delegate), goodImpl)
		require.False(t, ok)
		require.ErrorContains(t, err, "Sepolia")
	})
}
