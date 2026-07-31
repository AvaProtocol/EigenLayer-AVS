package aa

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// Account derivation for Alchemy Modular Account v2.
//
// The factory is the same address on every chain Alchemy supports
// (verified across eth/base/arb/bnb/hyperliquid/robinhood/sepolia/base-sepolia
// — see avs-infra Alchemy_Stack_Migration_And_Chain_Expansion.md §1.2), so
// unlike the v0.6 SimpleAccountFactory there is nothing to deploy per chain.
//
// It exposes two account variants with DIFFERENT addresses for the same
// (owner, salt), and the choice is unrevisable once users are onboarded:
//
//	getAddressSemiModular(owner, salt)      -> SemiModularAccountBytecode
//	getAddress(owner, salt, entityId)       -> full ModularAccount
//
// We use the semi-modular one. Alchemy documents its `mode: "default"` as
// "the cheapest, most flexible smart wallet", and the concern that a *semi*
// modular account might not support the permission modules the spend-policy
// design depends on was checked against deployed bytecode: both
// implementations carry installValidation (0x1bbf564c). The cheap variant
// gives up nothing we need.
const MAv2FactoryAddressHex = "0x00000000000017c61b5bEe81050EC8eFc9c6fecd"

// MAv2FactoryAddress is the deterministic AccountFactory address.
func MAv2FactoryAddress() common.Address {
	return common.HexToAddress(MAv2FactoryAddressHex)
}

const maV2FactoryABIJSON = `[
  {"type":"function","name":"getAddressSemiModular","stateMutability":"view",
   "inputs":[{"name":"owner","type":"address"},{"name":"salt","type":"uint256"}],
   "outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"createSemiModularAccount","stateMutability":"payable",
   "inputs":[{"name":"owner","type":"address"},{"name":"salt","type":"uint256"}],
   "outputs":[{"name":"","type":"address"}]}
]`

var (
	maV2FactoryABIOnce sync.Once
	maV2FactoryABI     abi.ABI
	maV2FactoryABIErr  error
)

func ensureMAv2FactoryABI() (abi.ABI, error) {
	maV2FactoryABIOnce.Do(func() {
		maV2FactoryABI, maV2FactoryABIErr = abi.JSON(strings.NewReader(maV2FactoryABIJSON))
	})
	return maV2FactoryABI, maV2FactoryABIErr
}

// GetSenderAddressMAv2 asks the factory for the counterfactual account address.
//
// Deliberately an on-chain call rather than a local CREATE2 computation, for
// the same reason the v0.6 path does it: the factory's salt derivation mixes
// in more than the caller-supplied salt, and a local reimplementation that
// drifts from the contract produces an address that looks right and holds
// nothing.
func GetSenderAddressMAv2(conn *ethclient.Client, owner common.Address, salt *big.Int) (*common.Address, error) {
	return GetSenderAddressMAv2ForFactory(conn, owner, MAv2FactoryAddress(), salt)
}

// GetSenderAddressMAv2ForFactory is GetSenderAddressMAv2 against an explicit
// factory — useful for tests and for pinning a specific audited deployment.
func GetSenderAddressMAv2ForFactory(conn *ethclient.Client, owner common.Address, factory common.Address, salt *big.Int) (*common.Address, error) {
	if conn == nil {
		return nil, fmt.Errorf("nil eth client")
	}
	if salt == nil {
		return nil, fmt.Errorf("salt is nil")
	}
	parsed, err := ensureMAv2FactoryABI()
	if err != nil {
		return nil, err
	}
	callData, err := parsed.Pack("getAddressSemiModular", owner, salt)
	if err != nil {
		return nil, fmt.Errorf("packing getAddressSemiModular: %w", err)
	}
	out, err := conn.CallContract(context.Background(), ethereum.CallMsg{To: &factory, Data: callData}, nil)
	if err != nil {
		return nil, fmt.Errorf("getAddressSemiModular on factory %s for owner %s salt %s: %w",
			factory.Hex(), owner.Hex(), salt.String(), err)
	}
	vals, err := parsed.Unpack("getAddressSemiModular", out)
	if err != nil || len(vals) != 1 {
		return nil, fmt.Errorf("decoding getAddressSemiModular result (%d bytes): %w", len(out), err)
	}
	addr, ok := vals[0].(common.Address)
	if !ok {
		return nil, fmt.Errorf("getAddressSemiModular returned %T, want address", vals[0])
	}
	return &addr, nil
}

// GetInitCodeMAv2 returns the v0.7 factory/factoryData pair that deploys the
// account on first use.
//
// v0.6 concatenated these into a single initCode blob; v0.7 carries them as
// separate UserOperation fields, so they are returned separately rather than
// joined and re-split by the caller.
func GetInitCodeMAv2(owner common.Address, salt *big.Int) (common.Address, []byte, error) {
	return GetInitCodeMAv2ForFactory(owner, MAv2FactoryAddress(), salt)
}

// GetInitCodeMAv2ForFactory is GetInitCodeMAv2 against an explicit factory.
func GetInitCodeMAv2ForFactory(owner common.Address, factory common.Address, salt *big.Int) (common.Address, []byte, error) {
	if salt == nil {
		return common.Address{}, nil, fmt.Errorf("salt is nil")
	}
	parsed, err := ensureMAv2FactoryABI()
	if err != nil {
		return common.Address{}, nil, err
	}
	data, err := parsed.Pack("createSemiModularAccount", owner, salt)
	if err != nil {
		return common.Address{}, nil, fmt.Errorf("packing createSemiModularAccount: %w", err)
	}
	return factory, data, nil
}
