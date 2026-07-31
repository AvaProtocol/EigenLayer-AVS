package aa

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// AccountProvider selects which smart-account implementation an address is
// derived from. The string values match core/config's account_provider so a
// caller can pass either the raw yaml value or config's normalised
// AccountProviderName() straight through — both are trimmed and lowercased
// here.
//
// The type is redeclared rather than imported from config. That was originally
// justified as keeping chainio free of a config dependency, which is not true:
// aa.go already imports config. The redeclaration survives because it keeps
// this file's derivation logic expressible in its own terms, but it does mean
// two independent sets of string constants — see the cross-package drift guard
// in core/taskengine, which is what actually holds them together.
type AccountProvider string

const (
	ProviderSimpleAccount    AccountProvider = "simple_account"
	ProviderModularAccountV2 AccountProvider = "modular_account_v2"
)

// DeriveSenderAddress returns the counterfactual account address for
// (owner, salt) under the given provider.
//
// The two providers do not merely use different factory addresses — they use
// different derivation ABIs (`getAddress(owner,salt)` vs
// `getAddressSemiModular(owner,salt)`). Swapping only the factory address
// would call the wrong method and either revert or, worse, return an address
// nothing will ever deploy to. Routing both through one function keeps that
// pairing in a single place.
//
// An empty provider is Modular Account v2, matching config's default. Note
// that this means MA v2 is what an unconfigured caller gets, and MA v2 derives
// different addresses for the same (owner, salt) than SimpleAccount does.
func DeriveSenderAddress(conn *ethclient.Client, owner common.Address, salt *big.Int, provider AccountProvider) (*common.Address, error) {
	switch normaliseProvider(provider) {
	case ProviderSimpleAccount:
		return GetSenderAddress(conn, owner, salt)
	case ProviderModularAccountV2:
		return GetSenderAddressMAv2(conn, owner, salt)
	default:
		return nil, fmt.Errorf("unknown account provider %q; expected %q or %q",
			provider, ProviderSimpleAccount, ProviderModularAccountV2)
	}
}

// DeriveSenderAddressForFactory is DeriveSenderAddress against an explicit
// factory rather than the package-global one. Most callers want this: the
// factory is per-chain config, and deriving against the gateway's default
// factory on a multi-chain task yields an address nothing will deploy to.
//
// The factory is honoured for BOTH providers, including MA v2 — whose factory
// is the same constant on every chain today, but whose caller may be replaying
// a wallet record written under a different one. Passing the recorded factory
// back in is what lets an existing wallet re-derive to itself.
func DeriveSenderAddressForFactory(conn *ethclient.Client, owner, factory common.Address, salt *big.Int, provider AccountProvider) (*common.Address, error) {
	switch normaliseProvider(provider) {
	case ProviderSimpleAccount:
		return GetSenderAddressForFactory(conn, owner, factory, salt)
	case ProviderModularAccountV2:
		return GetSenderAddressMAv2ForFactory(conn, owner, factory, salt)
	default:
		return nil, fmt.Errorf("unknown account provider %q; expected %q or %q",
			provider, ProviderSimpleAccount, ProviderModularAccountV2)
	}
}

// DeriveSenderAddressAuto derives against a factory, inferring the provider
// from that factory. Use it when re-deriving an address that already exists —
// wallet-ownership checks, salt scans, anything replaying a stored record —
// where the question is "what does THIS factory produce", not "what does this
// chain create now".
//
// Because the factory carries the provider, this needs no extra parameter,
// which is what keeps the ChainStateReader gRPC interface unchanged across the
// v0.7 cutover: the worker is already given the factory.
func DeriveSenderAddressAuto(conn *ethclient.Client, owner, factory common.Address, salt *big.Int) (*common.Address, error) {
	return DeriveSenderAddressForFactory(conn, owner, factory, salt, ProviderForFactory(factory))
}

// DeriveInitCodeAuto returns the (factory, factoryData) pair that deploys the
// account for (owner, salt), inferring the account implementation from the
// factory — the init-code counterpart to DeriveSenderAddressAuto.
//
// The two providers build deployment calldata with different factory methods,
// so this must route in lockstep with derivation. Building SimpleAccount
// init code for an address derived as MA v2 produces a UserOperation whose
// initCode deploys to a different address than its sender field, which the
// EntryPoint rejects as AA14.
func DeriveInitCodeAuto(owner, factory common.Address, salt *big.Int) (common.Address, []byte, error) {
	switch ProviderForFactory(factory) {
	case ProviderModularAccountV2:
		return GetInitCodeMAv2ForFactory(owner, factory, salt)
	default:
		hexCode, err := GetInitCodeForFactory(owner.Hex(), factory, salt)
		if err != nil {
			return common.Address{}, nil, err
		}
		raw := common.FromHex(hexCode)
		// initCode is factory(20 bytes) ++ calldata. Anything shorter means the
		// builder returned something that is not init code at all.
		if len(raw) <= common.AddressLength {
			return common.Address{}, nil, fmt.Errorf("init code is %d bytes, too short to contain a factory and calldata", len(raw))
		}
		return factory, raw[common.AddressLength:], nil
	}
}

// PackExecuteBatchAuto builds atomic-batch calldata for the account
// implementation this factory belongs to.
//
// Unlike execute(), batching did NOT carry over. MA v2 has a single
// executeBatch((address,uint256,bytes)[]) that always takes per-call values;
// the v0.6 account has two entry points, executeBatch(address[],bytes[]) and
// executeBatchWithValues(...), with different selectors. Sending v0.6 batch
// calldata to an MA v2 account reverts in validation as AA23, which names
// neither the batch nor the account type.
//
// Callers pass values regardless; the v0.6 branch drops them when every value
// is zero, because executeBatchWithValues (0xc3ff72fc) is not simulatable by
// the bundler while plain executeBatch is.
func PackExecuteBatchAuto(factory common.Address, targets []common.Address, values []*big.Int, datas [][]byte) ([]byte, error) {
	if len(targets) != len(datas) || len(targets) != len(values) {
		return nil, fmt.Errorf("batch arity mismatch: %d targets, %d values, %d calldatas",
			len(targets), len(values), len(datas))
	}
	if ProviderForFactory(factory) == ProviderModularAccountV2 {
		calls := make([]Call, len(targets))
		for i := range targets {
			calls[i] = Call{Target: targets[i], Value: values[i], Data: datas[i]}
		}
		return PackExecuteBatchMAv2(calls)
	}
	anyValue := false
	for _, v := range values {
		if v != nil && v.Sign() != 0 {
			anyValue = true
			break
		}
	}
	if anyValue {
		return PackExecuteBatchWithValues(targets, values, datas)
	}
	return PackExecuteBatch(targets, datas)
}

// EffectiveFactory returns the factory this chain creates accounts at NOW,
// which is the MA v2 constant on an MA v2 chain and the configured
// smart_wallet.factory_address otherwise.
//
// Reach for this anywhere the old code passed swCfg.FactoryAddress into a
// derivation. Passing the raw configured address after the v0.7 cutover
// derives a SimpleAccount address on a chain that creates MA v2 accounts —
// silently, since both calls succeed and return a well-formed address that
// simply holds nothing.
func EffectiveFactory(swCfg *config.SmartWalletConfig) (common.Address, error) {
	if swCfg == nil {
		return common.Address{}, fmt.Errorf("nil smart wallet config")
	}
	return FactoryAddressForProvider(AccountProvider(swCfg.AccountProviderName()), swCfg.FactoryAddress)
}

// ProviderForFactory reports which provider created an account at this
// factory. This is the per-WALLET counterpart to config's per-chain setting,
// and the two are not interchangeable: after the v0.7 cutover a chain's config
// says modular_account_v2 while wallets created before it still carry the
// SimpleAccount factory on their stored record. Execution has to follow the
// wallet, not the chain, or it sends a v0.7 operation to a v0.6 account.
func ProviderForFactory(factory common.Address) AccountProvider {
	if factory == MAv2FactoryAddress() {
		return ProviderModularAccountV2
	}
	return ProviderSimpleAccount
}

// FactoryAddressForProvider returns the factory an account of this provider is
// created by — the value recorded on the wallet record, and what keeps the
// `wsalt:` index from conflating a v0.6 and an MA v2 wallet at the same salt.
//
// SimpleAccount has no single answer: its factory is per-deployment and comes
// from config (smart_wallet.factory_address), so callers must supply it. Only
// the MA v2 factory is a constant, being the same address on every chain.
func FactoryAddressForProvider(provider AccountProvider, simpleAccountFactory common.Address) (common.Address, error) {
	switch normaliseProvider(provider) {
	case ProviderSimpleAccount:
		if simpleAccountFactory == (common.Address{}) {
			return common.Address{}, fmt.Errorf("simple_account requires a configured factory address")
		}
		return simpleAccountFactory, nil
	case ProviderModularAccountV2:
		return MAv2FactoryAddress(), nil
	default:
		return common.Address{}, fmt.Errorf("unknown account provider %q", provider)
	}
}

// normaliseProvider trims and lowercases so this package accepts exactly what
// config accepts. Config's AccountProviderName() already does both, but
// callers reasonably pass the raw yaml value through — and if this were
// stricter, a config the gateway loaded happily ("  Modular_Account_V2 ")
// would fail here as an unknown provider, which reads as a code bug rather
// than a whitespace one.
func normaliseProvider(p AccountProvider) AccountProvider {
	n := AccountProvider(strings.ToLower(strings.TrimSpace(string(p))))
	if n == "" {
		return ProviderModularAccountV2
	}
	return n
}
