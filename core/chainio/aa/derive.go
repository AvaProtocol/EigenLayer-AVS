package aa

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// AccountProvider selects which smart-account implementation an address is
// derived from. The string values match core/config's account_provider so a
// caller can pass either the raw yaml value or config's normalised
// AccountProviderName() straight through — both are trimmed and lowercased
// here. The type is redeclared rather than imported to keep chainio free of a
// config dependency.
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
// An empty provider is SimpleAccount, matching config's default. That default
// is deliberate: MA v2 derives different addresses for the same (owner, salt),
// so silently defaulting to it would move every existing user's wallet.
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
		return ProviderSimpleAccount
	}
	return n
}
