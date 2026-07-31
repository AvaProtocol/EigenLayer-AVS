package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
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
