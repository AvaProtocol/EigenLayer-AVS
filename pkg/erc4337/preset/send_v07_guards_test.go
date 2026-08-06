package preset

import (
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

func TestCheckDeferredCarrierNonce(t *testing.T) {
	sender := common.HexToAddress("0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd")
	carrier, err := userop.EncodeNonceMAv2(1,
		userop.ValidationOptionGlobal|userop.ValidationOptionDeferredAction, 0)
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 65)

	t.Run("nil auth is fine", func(t *testing.T) {
		if err := checkDeferredCarrierNonce(nil, carrier, sender); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("non-deferred skips the check", func(t *testing.T) {
		auth := &SessionAuthorization{EntityID: 1, SignerKey: testKey(t), CarrierNonce: big.NewInt(99)}
		if err := checkDeferredCarrierNonce(auth, carrier, sender); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("deferred without stored carrier skips", func(t *testing.T) {
		auth := &SessionAuthorization{
			EntityID: 1, SignerKey: testKey(t),
			DeferredData: []byte{0x01}, OwnerSignature: sig,
		}
		if err := checkDeferredCarrierNonce(auth, carrier, sender); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("matching carrier passes", func(t *testing.T) {
		auth := &SessionAuthorization{
			EntityID: 1, SignerKey: testKey(t),
			DeferredData: []byte{0x01}, OwnerSignature: sig,
			CarrierNonce: new(big.Int).Set(carrier),
		}
		if err := checkDeferredCarrierNonce(auth, carrier, sender); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("mismatch fails with both values", func(t *testing.T) {
		auth := &SessionAuthorization{
			EntityID: 1, SignerKey: testKey(t),
			DeferredData: []byte{0x01}, OwnerSignature: sig,
			CarrierNonce: new(big.Int).Set(carrier),
		}
		wrong := big.NewInt(0)
		err := checkDeferredCarrierNonce(auth, wrong, sender)
		if err == nil {
			t.Fatal("expected mismatch error")
		}
		msg := err.Error()
		if !strings.Contains(msg, carrier.String()) || !strings.Contains(msg, wrong.String()) {
			t.Errorf("error should name both nonces: %v", err)
		}
		if !strings.Contains(msg, sender.Hex()) {
			t.Errorf("error should name the sender: %v", err)
		}
	})
}

// EffectiveFactory must ignore the raw v0.6 smart_wallet.factory_address when
// account_provider is modular_account_v2 — the false diagnosis in the AA23
// factory-mismatch findings was rooted in logging the raw field instead.
func TestEffectiveFactoryIgnoresV06ConfigUnderMAv2(t *testing.T) {
	v06 := common.HexToAddress(config.DefaultFactoryProxyAddressHex)
	swCfg := &config.SmartWalletConfig{
		AccountProvider: config.AccountProviderModularAccountV2,
		FactoryAddress:  v06,
	}
	got, err := aa.EffectiveFactory(swCfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != aa.MAv2FactoryAddress() {
		t.Errorf("EffectiveFactory = %s, want MA v2 factory %s (raw config was %s)",
			got.Hex(), aa.MAv2FactoryAddress().Hex(), v06.Hex())
	}
	if got == v06 {
		t.Error("EffectiveFactory must not return the configured v0.6 factory under modular_account_v2")
	}
}

// SessionAuthorization.PolicyID / CarrierNonce are optional metadata; Validate
// must still accept a grant that only has entity + key (applied grants).
func TestSessionAuthOptionalMetadata(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	auth := &SessionAuthorization{
		EntityID:     1,
		SignerKey:    key,
		PolicyID:     "01kz8ebkzgv2th9r32w9qrcw79",
		CarrierNonce: big.NewInt(1),
	}
	if err := auth.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAnnotatePrefundError(t *testing.T) {
	sender := common.HexToAddress("0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd")
	raw := fmt.Errorf("eth_sendUserOperation: precheck failed: sender balance and deposit together is 0 but must be at least 1")
	got := annotatePrefundError(raw, sender)
	if got == nil {
		t.Fatal("expected annotated error")
	}
	msg := got.Error()
	if !strings.Contains(msg, sender.Hex()) {
		t.Errorf("should name sender: %v", got)
	}
	if !strings.Contains(msg, "ALCHEMY_PAYMASTER_POLICY_ID") &&
		!strings.Contains(msg, "alchemy_paymaster_policy_id") {
		t.Errorf("should point at Alchemy paymaster policy: %v", got)
	}
	// Non-prefund errors pass through unchanged (same error value).
	other := fmt.Errorf("AA25 invalid account nonce")
	if annotatePrefundError(other, sender) != other {
		t.Error("non-prefund errors must not be rewrapped into a different identity check beyond annotation")
	}
	// Actually annotatePrefundError always wraps only prefund; for non-match returns err as-is
	if annotatePrefundError(other, sender).Error() != other.Error() {
		t.Errorf("got %v", annotatePrefundError(other, sender))
	}
}
