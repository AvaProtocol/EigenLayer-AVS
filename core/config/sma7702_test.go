package config

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

func TestSMA7702PinConstantsWellFormed(t *testing.T) {
	require.True(t, common.IsHexAddress(SMA7702DelegateAddressHex))
	require.Equal(t, common.HexToAddress(SMA7702DelegateAddressHex), SMA7702Delegate())
	require.Equal(t, 32, len(SMA7702ImplHash().Bytes()))
	_, err := parseHash32(SMA7702ImplHashHex)
	require.NoError(t, err)
}

func TestSMA7702FirstChains(t *testing.T) {
	require.True(t, SMA7702FirstChain(SMA7702ChainSepolia))
	require.True(t, SMA7702FirstChain(SMA7702ChainBase))
	require.False(t, SMA7702FirstChain(84532), "base-sepolia is not a first production chain")
	require.False(t, SMA7702FirstChain(1))
	require.False(t, SMA7702FirstChain(0))
}

func TestEOA7702ExecuteDefaultsFalse(t *testing.T) {
	var c SmartWalletConfig
	require.False(t, c.EOA7702Execute)
	require.False(t, c.HasSMA7702Pin())
	require.NoError(t, c.ValidateSMA7702())
}

func TestValidateSMA7702PartialPin(t *testing.T) {
	onlyDelegate := &SmartWalletConfig{ChainID: 11155111, SMA7702Delegate: SMA7702Delegate()}
	require.Error(t, onlyDelegate.ValidateSMA7702())
	onlyHash := &SmartWalletConfig{ChainID: 11155111, SMA7702ImplHash: SMA7702ImplHash()}
	require.Error(t, onlyHash.ValidateSMA7702())
}

func TestValidateSMA7702Canonical(t *testing.T) {
	ok := &SmartWalletConfig{
		ChainID:         11155111,
		SMA7702Delegate: SMA7702Delegate(),
		SMA7702ImplHash: SMA7702ImplHash(),
	}
	require.NoError(t, ok.ValidateSMA7702())

	wrongDelegate := *ok
	wrongDelegate.SMA7702Delegate = common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	require.ErrorContains(t, wrongDelegate.ValidateSMA7702(), "canonical SMA-7702")

	wrongHash := *ok
	wrongHash.SMA7702ImplHash = common.HexToHash("0xdeadbeef")
	require.ErrorContains(t, wrongHash.ValidateSMA7702(), "B0 pin")
}

func TestValidateSMA7702Execute(t *testing.T) {
	t.Run("true is refused until B5 even with pin on Sepolia", func(t *testing.T) {
		c := &SmartWalletConfig{
			ChainID:         SMA7702ChainSepolia,
			EOA7702Execute:  true,
			SMA7702Delegate: SMA7702Delegate(),
			SMA7702ImplHash: SMA7702ImplHash(),
		}
		err := c.ValidateSMA7702()
		require.ErrorContains(t, err, "no send path honors it yet (B5)")
		require.ErrorContains(t, err, "derived smart wallet")
	})
	t.Run("true without pin is refused", func(t *testing.T) {
		c := &SmartWalletConfig{ChainID: 11155111, EOA7702Execute: true}
		require.ErrorContains(t, c.ValidateSMA7702(), "B5")
	})
	t.Run("false with pin is the B1 default shape", func(t *testing.T) {
		c := &SmartWalletConfig{
			ChainID:         SMA7702ChainSepolia,
			SMA7702Delegate: SMA7702Delegate(),
			SMA7702ImplHash: SMA7702ImplHash(),
		}
		require.False(t, c.EOA7702Execute)
		require.NoError(t, c.ValidateSMA7702())
	})
	var nilConfig *SmartWalletConfig
	require.NoError(t, nilConfig.ValidateSMA7702())
}

func TestApplySMA7702FromYAML(t *testing.T) {
	t.Run("omitted keys leave execute false and no pin", func(t *testing.T) {
		dst := &SmartWalletConfig{ChainID: 84532}
		require.NoError(t, applySMA7702(dst, SmartWalletConfigRaw{}))
		require.False(t, dst.EOA7702Execute)
		require.False(t, dst.HasSMA7702Pin())
	})
	t.Run("execute true is refused at apply even with pin", func(t *testing.T) {
		dst := &SmartWalletConfig{ChainID: 11155111}
		err := applySMA7702(dst, SmartWalletConfigRaw{
			EOA7702Execute:  true,
			SMA7702Delegate: SMA7702DelegateAddressHex,
			SMA7702ImplHash: SMA7702ImplHashHex,
		})
		require.ErrorContains(t, err, "B5")
	})
	t.Run("canonical pin parses", func(t *testing.T) {
		dst := &SmartWalletConfig{ChainID: 11155111}
		require.NoError(t, applySMA7702(dst, SmartWalletConfigRaw{
			SMA7702Delegate: SMA7702DelegateAddressHex,
			SMA7702ImplHash: SMA7702ImplHashHex,
		}))
		require.True(t, dst.HasSMA7702Pin())
		require.False(t, dst.EOA7702Execute)
		require.Equal(t, SMA7702Delegate(), dst.SMA7702Delegate)
		require.Equal(t, SMA7702ImplHash(), dst.SMA7702ImplHash)
	})
	t.Run("short hash is refused, not padded", func(t *testing.T) {
		dst := &SmartWalletConfig{ChainID: 11155111}
		err := applySMA7702(dst, SmartWalletConfigRaw{
			SMA7702Delegate: SMA7702DelegateAddressHex,
			SMA7702ImplHash: "0xecfc0328",
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "64")
	})
	t.Run("malformed hex is not reported as truncation", func(t *testing.T) {
		bad := SMA7702ImplHashHex
		bad = bad[:len(bad)-1] + "z"
		dst := &SmartWalletConfig{ChainID: 11155111}
		err := applySMA7702(dst, SmartWalletConfigRaw{
			SMA7702Delegate: SMA7702DelegateAddressHex,
			SMA7702ImplHash: bad,
		})
		require.ErrorContains(t, err, "is not hex")
		require.NotContains(t, err.Error(), "want 32")
	})
	t.Run("one-sided pin is refused", func(t *testing.T) {
		dst := &SmartWalletConfig{ChainID: 11155111}
		require.Error(t, applySMA7702(dst, SmartWalletConfigRaw{SMA7702Delegate: SMA7702DelegateAddressHex}))
	})
}

func TestYAMLUnmarshalReadsSMA7702Keys(t *testing.T) {
	const src = `
eoa_7702_execute: false
sma_7702_delegate: 0x69007702764179f14F51cdce752f4f775d74E139
sma_7702_impl_hash: 0xecfc0328ce4d953a4d452e11f54b578fa2d7f2f8126246d1120ca24d11845081
`
	var raw SmartWalletConfigRaw
	require.NoError(t, yaml.Unmarshal([]byte(src), &raw))
	require.False(t, raw.EOA7702Execute)
	require.Equal(t, SMA7702DelegateAddressHex, raw.SMA7702Delegate)
	require.Equal(t, SMA7702ImplHashHex, raw.SMA7702ImplHash)
}

func designation(delegate common.Address) []byte {
	out := make([]byte, 23)
	out[0], out[1], out[2] = 0xef, 0x01, 0x00
	copy(out[3:], delegate.Bytes())
	return out
}

func TestAssertSMA7702Designation(t *testing.T) {
	c := &SmartWalletConfig{
		ChainID:         11155111,
		SMA7702Delegate: SMA7702Delegate(),
		SMA7702ImplHash: SMA7702ImplHash(),
	}
	impl := []byte("sma-7702-impl-fixture")
	c.SMA7702ImplHash = crypto.Keccak256Hash(impl)

	t.Run("happy path is K13, not tx status", func(t *testing.T) {
		require.NoError(t, c.AssertSMA7702Designation(designation(c.SMA7702Delegate), impl))
	})
	t.Run("short code fails", func(t *testing.T) {
		err := c.AssertSMA7702Designation([]byte{0xef, 0x01, 0x00}, impl)
		require.ErrorContains(t, err, "EOA_DELEGATION_MISSING")
	})
	t.Run("wrong prefix fails", func(t *testing.T) {
		code := designation(c.SMA7702Delegate)
		code[0] = 0x00
		require.ErrorContains(t, c.AssertSMA7702Designation(code, impl), "EOA_DELEGATION_MISSING")
	})
	t.Run("wrong delegate fails", func(t *testing.T) {
		other := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
		require.ErrorContains(t, c.AssertSMA7702Designation(designation(other), impl), "EOA_DELEGATION_MISSING")
	})
	t.Run("wrong impl hash fails", func(t *testing.T) {
		require.ErrorContains(t, c.AssertSMA7702Designation(designation(c.SMA7702Delegate), []byte("other")), "impl hash")
	})
	t.Run("missing pin fails closed", func(t *testing.T) {
		empty := &SmartWalletConfig{ChainID: 1}
		require.ErrorContains(t, empty.AssertSMA7702Designation(designation(SMA7702Delegate()), impl), "not configured")
	})
}

func TestCheck7702AuthorizationChainID(t *testing.T) {
	require.Error(t, Check7702AuthorizationChainID(nil))
	require.Error(t, Check7702AuthorizationChainID(big.NewInt(0)))
	require.Error(t, Check7702AuthorizationChainID(big.NewInt(-1)))
	require.NoError(t, Check7702AuthorizationChainID(big.NewInt(11155111)))
	require.NoError(t, Check7702AuthorizationChainID(big.NewInt(8453)))
}

func TestIsSMA7702DesignationExactLength(t *testing.T) {
	d := SMA7702Delegate()
	require.True(t, IsSMA7702Designation(designation(d), d))
	require.False(t, IsSMA7702Designation(append(designation(d), 0x00), d), "trailing bytes are not a designation")
	require.False(t, IsSMA7702Designation(nil, d))
	require.False(t, IsSMA7702Designation(designation(d)[:22], d))
}

func TestParseHash32RejectsUnprefixed(t *testing.T) {
	_, err := parseHash32(strings.TrimPrefix(SMA7702ImplHashHex, "0x"))
	require.Error(t, err)
}
