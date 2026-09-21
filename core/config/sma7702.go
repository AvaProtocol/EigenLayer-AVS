package config

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Canonical SMA-7702 pin from Track B spike B0 (PR #798). Byte-identical on
// Ethereum Sepolia and Base mainnet: 23,741 bytes at the delegate address.
// Comparison is K13: eoa code[0:3]==0xef0100 && code[3:23]==delegate &&
// keccak256(impl) == this hash. Never assert 7702 delegation from type-4 tx
// status — EIP-7702 applies the authorization list before execution, so a
// reverting call can still leave the designation in place.
const (
	SMA7702DelegateAddressHex = "0x69007702764179f14F51cdce752f4f775d74E139" // alchemy.sma-7702.1.0.0
	SMA7702ImplHashHex        = "0xecfc0328ce4d953a4d452e11f54b578fa2d7f2f8126246d1120ca24d11845081"
)

// First production chains for eoa_7702_execute. Other mainnets wait (spec K8).
const (
	SMA7702ChainSepolia = int64(11155111)
	SMA7702ChainBase    = int64(8453)
)

// SMA7702Delegate is the canonical SemiModularAccount7702 address.
func SMA7702Delegate() common.Address {
	return common.HexToAddress(SMA7702DelegateAddressHex)
}

// SMA7702ImplHash is keccak256 of the bytecode at SMA7702Delegate.
func SMA7702ImplHash() common.Hash {
	return common.HexToHash(SMA7702ImplHashHex)
}

// SMA7702FirstChain reports whether chainID is Sepolia or Base — the only
// chains that may turn eoa_7702_execute on.
func SMA7702FirstChain(chainID int64) bool {
	return chainID == SMA7702ChainSepolia || chainID == SMA7702ChainBase
}

// HasSMA7702Pin is true when both the delegate and impl hash are set.
func (c *SmartWalletConfig) HasSMA7702Pin() bool {
	return c != nil &&
		c.SMA7702Delegate != (common.Address{}) &&
		c.SMA7702ImplHash != (common.Hash{})
}

// ValidateSMA7702 checks the pin and execute flag. Empty pin + execute false
// is fine (7702 not configured on this chain). A partial pin or a pin that
// is not the B0 canonical values is refused at load. eoa_7702_execute true
// is refused until B5 adds the send-path consumer — a true value today would
// look honored while UserOps still go through the derived smart wallet.
func (c *SmartWalletConfig) ValidateSMA7702() error {
	if c == nil {
		return nil
	}
	if c.EOA7702Execute {
		return fmt.Errorf(
			"chain_id=%d eoa_7702_execute is true, but no send path honors it yet (B5); leave it false — UserOps still go through the derived smart wallet",
			c.ChainID)
	}
	pinned := c.SMA7702Delegate != (common.Address{})
	hashed := c.SMA7702ImplHash != (common.Hash{})
	if pinned != hashed {
		return fmt.Errorf(
			"chain_id=%d sma_7702_delegate and sma_7702_impl_hash must be set together",
			c.ChainID)
	}
	if pinned {
		if c.SMA7702Delegate != SMA7702Delegate() {
			return fmt.Errorf(
				"chain_id=%d sma_7702_delegate %s is not the canonical SMA-7702 %s (Calibur is not used)",
				c.ChainID, c.SMA7702Delegate.Hex(), SMA7702DelegateAddressHex)
		}
		if c.SMA7702ImplHash != SMA7702ImplHash() {
			return fmt.Errorf(
				"chain_id=%d sma_7702_impl_hash %s does not match the B0 pin %s (Sepolia and Base, 23741 bytes)",
				c.ChainID, c.SMA7702ImplHash.Hex(), SMA7702ImplHashHex)
		}
	}
	return nil
}

// IsSMA7702Designation reports whether code is an EIP-7702 designation for
// delegate: exactly 23 bytes, prefix 0xef0100, bytes 3:23 == delegate.
// This is not a comparison of the whole slice to 0xef0100||delegate as a
// string; EIP-3541 forbids deploying code that starts with 0xEF, so no
// contract can wear this prefix.
func IsSMA7702Designation(code []byte, delegate common.Address) bool {
	if len(code) != 23 {
		return false
	}
	return code[0] == 0xef && code[1] == 0x01 && code[2] == 0x00 &&
		common.BytesToAddress(code[3:23]) == delegate
}

// AssertSMA7702Designation is K13: eoa designation + impl bytecode hash.
// Never takes a tx status. Fail-closed when the pin is missing.
func (c *SmartWalletConfig) AssertSMA7702Designation(eoaCode, implCode []byte) error {
	if c == nil || !c.HasSMA7702Pin() {
		chainID := int64(0)
		if c != nil {
			chainID = c.ChainID
		}
		return fmt.Errorf("EOA_DELEGATION_MISSING: SMA-7702 pin is not configured for chain_id=%d", chainID)
	}
	if !IsSMA7702Designation(eoaCode, c.SMA7702Delegate) {
		return fmt.Errorf(
			"EOA_DELEGATION_MISSING: eoa code is not ef0100||%s (len=%d)",
			c.SMA7702Delegate.Hex(), len(eoaCode))
	}
	got := crypto.Keccak256Hash(implCode)
	if got != c.SMA7702ImplHash {
		return fmt.Errorf(
			"EOA_DELEGATION_MISSING: impl hash %s want %s (delegate %s)",
			got.Hex(), c.SMA7702ImplHash.Hex(), c.SMA7702Delegate.Hex())
	}
	return nil
}

// Check7702AuthorizationChainID refuses chain_id=0. One 7702 authorization
// must not cover every chain. The later delegation API (B3) calls this;
// B1 exposes it so the rule exists before that API lands.
func Check7702AuthorizationChainID(chainID *big.Int) error {
	if chainID == nil || chainID.Sign() <= 0 {
		return fmt.Errorf("7702 authorization chain_id=0 is refused; authorizations are per-chain")
	}
	return nil
}

func applySMA7702(dst *SmartWalletConfig, raw SmartWalletConfigRaw) error {
	if dst == nil {
		return fmt.Errorf("nil smart wallet config")
	}
	delegate, hash, execute, err := parseSMA7702Raw(raw)
	if err != nil {
		return fmt.Errorf("chain_id=%d: %w", dst.ChainID, err)
	}
	dst.SMA7702Delegate = delegate
	dst.SMA7702ImplHash = hash
	dst.EOA7702Execute = execute
	return dst.ValidateSMA7702()
}

func parseSMA7702Raw(raw SmartWalletConfigRaw) (common.Address, common.Hash, bool, error) {
	delegateStr := strings.TrimSpace(raw.SMA7702Delegate)
	hashStr := strings.TrimSpace(raw.SMA7702ImplHash)
	if delegateStr == "" && hashStr == "" {
		return common.Address{}, common.Hash{}, raw.EOA7702Execute, nil
	}
	if delegateStr == "" || hashStr == "" {
		return common.Address{}, common.Hash{}, false, fmt.Errorf(
			"sma_7702_delegate and sma_7702_impl_hash must be set together")
	}
	if !common.IsHexAddress(delegateStr) {
		return common.Address{}, common.Hash{}, false, fmt.Errorf(
			"sma_7702_delegate %q is not an address", delegateStr)
	}
	delegate := common.HexToAddress(delegateStr)
	if delegate == (common.Address{}) {
		return common.Address{}, common.Hash{}, false, fmt.Errorf("sma_7702_delegate is the zero address")
	}
	hash, err := parseHash32(hashStr)
	if err != nil {
		return common.Address{}, common.Hash{}, false, err
	}
	return delegate, hash, raw.EOA7702Execute, nil
}

func parseHash32(s string) (common.Hash, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '0' || (s[1] != 'x' && s[1] != 'X') {
		return common.Hash{}, fmt.Errorf("sma_7702_impl_hash must be 0x-prefixed 32-byte hex")
	}
	body := s[2:]
	if len(body) != 64 {
		return common.Hash{}, fmt.Errorf("sma_7702_impl_hash is %d hex chars, want 64", len(body))
	}
	b, err := hex.DecodeString(body)
	if err != nil {
		return common.Hash{}, fmt.Errorf("sma_7702_impl_hash is not hex: %w", err)
	}
	return common.BytesToHash(b), nil
}
