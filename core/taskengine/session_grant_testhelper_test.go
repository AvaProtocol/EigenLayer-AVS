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

	// A bare grant is only usable on an entity that is itself bare. An entity
	// carrying hooks from an earlier Studio-shaped grant names the same
	// controller, so a signer-only check adopts it and then builds operations
	// the account rejects — see session_fixture_permissions_test.go.
	resetInitialPermissions(t, db, swCfg, wallet,
		func(entity uint32) expectedPermissions { return timeBoxedGlobalGrant(controller, entity) },
		func(policyID string) (*PreparedSessionGrant, error) {
			return PrepareSessionGrant(db, swCfg.ChainID, controller, policyID, SessionGrantRequest{
				Owner:  owner,
				Wallet: wallet,
				// Globally authorized, so a fixture can move ETH or call
				// anything — but NOT hookless. A global validation with an
				// empty hooks array is refused as a deferred action (#734), so
				// a hookless fixture only ever worked by adopting an entity a
				// previous run had installed. TimeRange is the hook that adds
				// no restriction on what may be called.
				Selectors: nil,
				HooksFor: func(entity uint32) ([][]byte, error) {
					hook, err := aa.TimeRangeValidationHook(entity, fixtureGrantValidUntil, 0)
					if err != nil {
						return nil, fmt.Errorf("time-range hook for entity %d: %w", entity, err)
					}
					return [][]byte{hook}, nil
				},
				ValidUntil: int64(fixtureGrantValidUntil) * 1000,
				// Global with no selector scoping still counts as
				// self-administering, and for a fixture that is deliberate.
				AllowSelfAdministration: true,
			})
		})

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

	// Unlike the bare fixture, this one can REUSE a matching entity: an entity
	// already carrying the allowlist + time-range hook set at its own id is the
	// grant this test wants, so a second run adopts it rather than burning a
	// fresh entity every time.
	policy := resetInitialPermissions(t, db, swCfg, wallet,
		func(entity uint32) expectedPermissions { return hookedGlobalGrant(controller, entity) },
		func(policyID string) (*PreparedSessionGrant, error) {
			prepared, err := PrepareSessionGrant(db, swCfg.ChainID, controller, policyID, SessionGrantRequest{
				Owner:      owner,
				Wallet:     wallet,
				AgentLabel: "test-erc20-approves",
				ValidUntil: perms.ValidUntilMs,
				HooksFor:   perms.HooksFor,
			})
			if err != nil {
				return nil, err
			}
			attachDeclaredPermissions(prepared.Policy, perms)
			return prepared, nil
		})

	if policy.Grant == nil || !policy.Grant.RequiresExecuteUserOp {
		t.Fatal("expected RequiresExecuteUserOp=true (allowlist execution hook); grant shape is wrong")
	}
	t.Logf("grant: entity %d on %s (USDC+WETH-style approves, RequiresExecuteUserOp=%v)",
		policy.EntityID, wallet.Hex(), policy.Grant.RequiresExecuteUserOp)

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
	}, nil))
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
// (entity, wallet). A zero address means the entity is genuinely unclaimed.
//
// Read failures are returned rather than folded into the zero address: callers
// use zero to mean "free to install on", so a transient RPC error that reads as
// zero would install a second validation over an existing one.
func entitySignerOnChain(t *testing.T, swCfg *config.SmartWalletConfig, wallet common.Address, entity uint32) (common.Address, error) {
	t.Helper()
	client, err := ethclient.Dial(swCfg.EthRpcUrl)
	if err != nil {
		return common.Address{}, fmt.Errorf("dialing the chain to check the grant: %w", err)
	}
	defer client.Close()

	data := append(common.FromHex(selectorSSVMSigners), padWord(entity)...)
	data = append(data, common.LeftPadBytes(wallet.Bytes(), 32)...)
	module := aa.SingleSignerValidationModuleAddress()
	out, err := client.CallContract(context.Background(), ethereum.CallMsg{To: &module, Data: data}, nil)
	if err != nil {
		return common.Address{}, fmt.Errorf("calling signers(%d, %s): %w", entity, wallet.Hex(), err)
	}
	if len(out) < 32 {
		return common.Address{}, fmt.Errorf("signers(%d, %s) returned %d bytes, want at least 32",
			entity, wallet.Hex(), len(out))
	}
	return common.BytesToAddress(out[12:32]), nil
}

// installedOnChain is deliberately gone. It answered "does this entity name my
// signer", which fixtures used as "is this entity the grant I need" — and the
// two differ exactly when a stale grant on a shared runner carries the same
// controller with different hooks. Compare the whole validation state instead:
// readValidationState + expectedPermissions in session_fixture_permissions_test.go.

// selectorSSVMSigners is SingleSignerValidationModule.signers(uint32,address).
const selectorSSVMSigners = "0x217178fb"

func padWord(v uint32) []byte {
	return common.LeftPadBytes(big.NewInt(int64(v)).Bytes(), 32)
}
