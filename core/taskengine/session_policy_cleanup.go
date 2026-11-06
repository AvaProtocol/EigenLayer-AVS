package taskengine

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// PolicyCleanupReadError classifies read-reconciliation failures for logging.
// Kind is internal diagnostic metadata, not a response code.
type PolicyCleanupReadError struct {
	Kind  string
	Cause error
}

func (e *PolicyCleanupReadError) Error() string { return e.Cause.Error() }
func (e *PolicyCleanupReadError) Unwrap() error { return e.Cause }

// RefreshSessionPolicyCleanup reconciles a revoked policy read with the
// account's current registration. It is for authorized presentation reads,
// not internal policy lookups that may already hold the authority lock.
// Storage-only engines, unapplied grants and recorded teardowns do no RPCs.
// client is borrowed from the REST server's per-chain clients; never close it.
// A failed read or storage update leaves the caller's snapshot unchanged.
func (n *Engine) RefreshSessionPolicyCleanup(ctx context.Context, policy *model.SessionPolicy, client *ethclient.Client) error {
	if policy == nil || policy.Status != model.SessionPolicyRevoked || !policy.Grant.NeedsOnChainCleanup() || !n.sessionChainReads {
		return nil
	}
	// Historical records may name a chain no longer served here. Never use
	// ResolveSmartWalletConfig's default-chain fallback as teardown evidence.
	if !n.isChainConfigured(policy.ChainID) {
		return &PolicyCleanupReadError{Kind: "configuration", Cause: fmt.Errorf("%w: cleanup on chain %d", ErrSessionChainNotServed, policy.ChainID)}
	}
	if client == nil {
		return &PolicyCleanupReadError{Kind: "configuration", Cause: fmt.Errorf("no cleanup RPC client for chain %d", policy.ChainID)}
	}
	verify := func(ctx context.Context, _ int64, account common.Address, entity uint32) (bool, error) {
		registered, err := aa.EntityRegisteredOnChain(ctx, client, account, entity)
		return !registered, err
	}
	return n.refreshSessionPolicyCleanup(ctx, policy, verify)
}

func (n *Engine) refreshSessionPolicyCleanup(ctx context.Context, policy *model.SessionPolicy, verify TeardownVerifier) error {
	if identityErr := policy.Validate(); identityErr != nil {
		return &PolicyCleanupReadError{Kind: "identity", Cause: fmt.Errorf("cleanup identity: %w", identityErr)}
	}
	ctx, cancel := context.WithTimeout(ctx, teardownVerifyTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	cleared, err := verify(ctx, policy.ChainID, *policy.Runner, policy.EntityID)
	if err != nil {
		return &PolicyCleanupReadError{Kind: "rpc", Cause: fmt.Errorf("checking on-chain cleanup for policy %s: %w", policy.ID, err)}
	}
	if !cleared {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	// Never hold the authority lock for the RPC, or wait behind a slow grant
	// prepare from a read. A busy wallet retains the requirement until next read.
	lock := sessionAuthorityLock(policy.ChainID, *policy.Owner, *policy.Runner)
	if !lock.TryLock() {
		return &PolicyCleanupReadError{Kind: "busy", Cause: fmt.Errorf("recording cleanup for policy %s: wallet permission update in progress", policy.ID)}
	}
	defer lock.Unlock()
	// The marker reloads the stored row rather than writing this snapshot, so
	// concurrent applied-operation hashes or repaired install data survive.
	if markErr := markEntityTornDownLocked(n.db, policy.ChainID, *policy.Owner, *policy.Runner, policy.EntityID); markErr != nil {
		return &PolicyCleanupReadError{Kind: "storage", Cause: fmt.Errorf("recording on-chain cleanup for policy %s: %w", policy.ID, markErr)}
	}
	stored, err := n.GetSessionPolicyByID(&model.User{Address: *policy.Owner}, policy.ChainID, *policy.Runner, policy.ID)
	if err != nil {
		return &PolicyCleanupReadError{Kind: "storage", Cause: fmt.Errorf("reloading policy %s after on-chain cleanup: %w", policy.ID, err)}
	}
	*policy = *stored
	return nil
}
