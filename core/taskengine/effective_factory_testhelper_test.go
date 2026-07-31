package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/ethereum/go-ethereum/common"
)

// effectiveFactoryHex is the factory a chain actually creates wallets under.
//
// Tests used to assert against smart_wallet.factory_address directly. Since
// the EntryPoint v0.7 cutover that is only the SimpleAccount factory, while an
// MA v2 chain records wallets under the MA v2 constant — so asserting the
// configured value tests a wallet the engine no longer creates.
func effectiveFactoryHex(t *testing.T, swCfg *config.SmartWalletConfig) string {
	t.Helper()
	factory, err := aa.EffectiveFactory(swCfg)
	if err != nil {
		t.Fatalf("resolving effective factory: %v", err)
	}
	return factory.Hex()
}

// setGlobalFactory points aa's package-global factory at the one this chain
// actually creates accounts under.
//
// Tests used to call setGlobalFactory(t, cfg.SmartWallet)
// directly. That field is the v0.6 SimpleAccountFactory, and since
// aa.GetSenderAddress infers the account implementation from the global, those
// tests forced themselves onto v0.6 derivation while the engine they were
// exercising derived MA v2 — so the runner never matched any derived address.
func setGlobalFactory(t *testing.T, swCfg *config.SmartWalletConfig) {
	t.Helper()
	if err := aa.SetFactoryAddressForConfig(swCfg); err != nil {
		t.Fatalf("setting global factory: %v", err)
	}
}

// effectiveFactoryAddr is effectiveFactoryHex as an address.
func effectiveFactoryAddr(t *testing.T, swCfg *config.SmartWalletConfig) common.Address {
	t.Helper()
	factory, err := aa.EffectiveFactory(swCfg)
	if err != nil {
		t.Fatalf("resolving effective factory: %v", err)
	}
	return factory
}
