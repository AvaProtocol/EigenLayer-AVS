package taskengine

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// Live-chain tests execute through the gateway, and the gateway cannot sign as
// a wallet's owner — a stock MA v2 account trusts only its fallback signer.
// Every such test therefore needs a session grant in place before it runs, the
// same one the grant screen will create in production.
//
// grantControllerAuthority builds that grant and installs a resolver over the
// test's own database, so the test exercises the real resolution path rather
// than injecting an authorization directly.
//
// It is idempotent against the CHAIN, which matters because these wallets are
// long-lived Sepolia fixtures: if the controller is already installed at the
// entity, the stored grant is marked applied so the deferred install is not
// replayed onto an entity that already exists. Otherwise it stays pending and
// rides the test's first operation.
func grantControllerAuthority(
	t *testing.T,
	db storage.Storage,
	swCfg *config.SmartWalletConfig,
	owner common.Address,
	wallet common.Address,
) {
	t.Helper()

	ownerKey := requireOwnerKey(t)
	if got := crypto.PubkeyToAddress(ownerKey.PublicKey); got != owner {
		t.Fatalf("TEST_PRIVATE_KEY is %s but the test's owner is %s; they must match to authorize a grant",
			got.Hex(), owner.Hex())
	}
	if swCfg.ControllerPrivateKey == nil {
		t.Fatal("no controller key configured; the gateway cannot be granted anything")
	}
	controller := crypto.PubkeyToAddress(swCfg.ControllerPrivateKey.PublicKey)

	prepared, err := PrepareSessionGrant(db, swCfg.ChainID, controller, "test-grant", SessionGrantRequest{
		Owner:  owner,
		Wallet: wallet,
		// Global with no hooks: a fixture grant, and the API makes that
		// deliberate rather than accidental. Production grants carry hooks.
		Selectors: nil,
		Hooks:     nil,
		// A bare global grant is refused unless acknowledged. Fixtures may be
		// self-administering; production grants carry hooks or scoping.
		AllowSelfAdministration: true,
	})
	if err != nil {
		t.Fatalf("preparing the test grant: %v", err)
	}

	sig, err := crypto.Sign(prepared.Digest.Bytes(), ownerKey) // raw digest
	if err != nil {
		t.Fatalf("signing the grant: %v", err)
	}
	sig[64] += 27

	policy, err := SubmitSessionGrant(db, prepared, sig)
	if err != nil {
		t.Fatalf("submitting the grant: %v", err)
	}

	// If the entity already holds the controller on chain, the install has
	// happened in a previous run. Replaying it would re-run installValidation
	// on an existing entity.
	if installedOnChain(t, swCfg, wallet, policy.EntityID, controller) {
		if err := MarkSessionGrantApplied(db, policy, "pre-existing"); err != nil {
			t.Fatalf("marking the grant applied: %v", err)
		}
		t.Logf("grant: entity %d already installed on %s; not replaying the install",
			policy.EntityID, wallet.Hex())
	} else {
		t.Logf("grant: entity %d pending on %s; the install rides the first operation",
			policy.EntityID, wallet.Hex())
	}

	installSessionResolver(t, db, swCfg, controller)
}

// grantControllerAuthorityWithERC20Approves installs a production-shaped
// session grant: allowlist approve() on each token, an ERC-20 spend cap on
// capToken (must be one of the approve targets), TimeRange + AllowlistExecHook
// so RequiresExecuteUserOp / wrapExecuteUserOp match production Uniswap grants.
//
// Long-lived Sepolia fixtures often already have the controller on entity 1
// with a different hook set (bare global or USDC-only Studio grant). Reusing
// that entity without reinstall leaves stale hooks and AA23s on WETH approve.
// This helper skips occupied on-chain entities and allocates a free one so the
// deferred install rides the test's first operation with the right hooks.
func grantControllerAuthorityWithERC20Approves(
	t *testing.T,
	db storage.Storage,
	swCfg *config.SmartWalletConfig,
	owner, wallet common.Address,
	approveTokens []common.Address,
	capToken common.Address,
	capAmount string, // decimal smallest-unit string
) {
	t.Helper()

	if len(approveTokens) == 0 {
		t.Fatal("grantControllerAuthorityWithERC20Approves: need at least one approve token")
	}
	ownerKey := requireOwnerKey(t)
	if got := crypto.PubkeyToAddress(ownerKey.PublicKey); got != owner {
		t.Fatalf("TEST_PRIVATE_KEY is %s but the test's owner is %s; they must match to authorize a grant",
			got.Hex(), owner.Hex())
	}
	if swCfg.ControllerPrivateKey == nil {
		t.Fatal("no controller key configured; the gateway cannot be granted anything")
	}
	controller := crypto.PubkeyToAddress(swCfg.ControllerPrivateKey.PublicKey)

	actions := make([]model.AllowedAction, 0, len(approveTokens))
	for _, tok := range approveTokens {
		a := tok
		actions = append(actions, model.AllowedAction{
			Target:    &a,
			Selectors: []string{"0x095ea7b3"}, // approve(address,uint256)
		})
	}
	cap := capToken
	perms := SessionPermissions{
		AllowedActions: actions,
		SpendCap: &model.ERC20SpendCap{
			Token:      &cap,
			Amount:     capAmount,
			GrantedCap: capAmount,
		},
		ValidUntilMs: time.Now().Add(7 * 24 * time.Hour).UnixMilli(),
	}
	if err := perms.Validate(); err != nil {
		t.Fatalf("test grant permissions: %v", err)
	}

	var policy *model.SessionPolicy
	for attempt := 0; attempt < 32; attempt++ {
		policyID := fmt.Sprintf("test-approve-grant-%d", attempt)
		prepared, err := PrepareSessionGrant(db, swCfg.ChainID, controller, policyID, SessionGrantRequest{
			Owner:      owner,
			Wallet:     wallet,
			AgentLabel: "test-erc20-approves",
			ValidUntil: perms.ValidUntilMs,
			HooksFor:   perms.HooksFor,
		})
		if err != nil {
			t.Fatalf("preparing production-shaped test grant (attempt %d): %v", attempt, err)
		}

		sig, err := crypto.Sign(prepared.Digest.Bytes(), ownerKey)
		if err != nil {
			t.Fatalf("signing the grant: %v", err)
		}
		sig[64] += 27

		pol, err := SubmitSessionGrant(db, prepared, sig)
		if err != nil {
			t.Fatalf("submitting the grant (attempt %d): %v", attempt, err)
		}
		attachDeclaredPermissions(pol, perms)
		if err := StoreSessionPolicy(db, pol); err != nil {
			t.Fatalf("storing declared permissions on grant: %v", err)
		}

		// Any non-zero on-chain signer occupies the entity (controller or not).
		// Reusing a foreign or stale-hook entity breaks MA v2 uniqueness / install.
		if existing := entitySignerOnChain(t, swCfg, wallet, pol.EntityID); existing != (common.Address{}) {
			pol.Status = model.SessionPolicyRevoked
			if err := StoreSessionPolicy(db, pol); err != nil {
				t.Fatalf("reserving occupied entity %d: %v", pol.EntityID, err)
			}
			t.Logf("grant: entity %d already occupied on %s by %s; trying next entity for fresh hooks install",
				pol.EntityID, wallet.Hex(), existing.Hex())
			continue
		}

		// Free entity: leave pending so the install rides the first UserOp.
		policy = pol
		t.Logf("grant: entity %d pending on %s (USDC+WETH-style approves, RequiresExecuteUserOp=%v); install rides first operation",
			pol.EntityID, wallet.Hex(), pol.Grant != nil && pol.Grant.RequiresExecuteUserOp)
		break
	}
	if policy == nil {
		t.Fatal("could not allocate a free session entity for a production-shaped grant after 32 attempts")
	}
	if policy.Grant == nil || !policy.Grant.RequiresExecuteUserOp {
		t.Fatal("expected RequiresExecuteUserOp=true (allowlist execution hook); grant shape is wrong")
	}

	installSessionResolver(t, db, swCfg, controller)
}

func installSessionResolver(
	t *testing.T,
	db storage.Storage,
	swCfg *config.SmartWalletConfig,
	controller common.Address,
) {
	t.Helper()
	preset.SetSessionResolver(NewSessionResolver(db, func(a common.Address) (*ecdsa.PrivateKey, error) {
		if a != controller {
			return nil, fmt.Errorf("no key for session signer %s", a.Hex())
		}
		return swCfg.ControllerPrivateKey, nil
	}))
	t.Cleanup(func() { preset.SetSessionResolver(nil) })
}

// requireOwnerKey loads the owner key these tests cannot authorize without.
func requireOwnerKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	raw := os.Getenv("TEST_PRIVATE_KEY")
	if raw == "" {
		t.Fatal("TEST_PRIVATE_KEY must be set: the gateway cannot sign for a wallet without a grant, " +
			"and only the owner can authorize one")
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(raw, "0x"))
	if err != nil {
		t.Fatalf("TEST_PRIVATE_KEY is not a valid hex private key: %v", err)
	}
	return key
}

// entitySignerOnChain returns the SingleSignerValidationModule signer for
// (entity, wallet), or the zero address if unset / unreadable.
func entitySignerOnChain(t *testing.T, swCfg *config.SmartWalletConfig, wallet common.Address, entity uint32) common.Address {
	t.Helper()
	client, err := ethclient.Dial(swCfg.EthRpcUrl)
	if err != nil {
		t.Fatalf("dialing the chain to check the grant: %v", err)
	}
	defer client.Close()

	data := append(common.FromHex(selectorSSVMSigners), padWord(entity)...)
	data = append(data, common.LeftPadBytes(wallet.Bytes(), 32)...)
	module := aa.SingleSignerValidationModuleAddress()
	out, err := client.CallContract(context.Background(), ethereum.CallMsg{To: &module, Data: data}, nil)
	if err != nil || len(out) < 32 {
		return common.Address{}
	}
	return common.BytesToAddress(out[12:32])
}

// installedOnChain reports whether the entity already names this signer.
func installedOnChain(t *testing.T, swCfg *config.SmartWalletConfig, wallet common.Address, entity uint32, signer common.Address) bool {
	t.Helper()
	return entitySignerOnChain(t, swCfg, wallet, entity) == signer
}

// selectorSSVMSigners is SingleSignerValidationModule.signers(uint32,address).
const selectorSSVMSigners = "0x217178fb"

func padWord(v uint32) []byte {
	return common.LeftPadBytes(big.NewInt(int64(v)).Bytes(), 32)
}
