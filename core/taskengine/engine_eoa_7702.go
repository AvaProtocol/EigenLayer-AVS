package taskengine

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// ErrEOADelegationMissing: the EOA is not designated to the pinned SMA-7702
// (K13). Mapped to problem code EOA_DELEGATION_MISSING. Never inferred from
// type-4 tx status.
var ErrEOADelegationMissing = errors.New("EOA_DELEGATION_MISSING")

// StoreEOA7702Wallet upserts the B2 record: Address==Owner, no factory/salt,
// Delegate = canonical SMA-7702. Does not write the salt secondary index
// (StoreWallet skips it when Salt is nil).
func StoreEOA7702Wallet(db storage.Storage, chainID int64, owner, delegate common.Address) error {
	if db == nil {
		return fmt.Errorf("storage unavailable")
	}
	if owner == (common.Address{}) {
		return fmt.Errorf("eoa_7702 owner is the zero address")
	}
	if delegate != config.SMA7702Delegate() {
		return fmt.Errorf("eoa_7702 delegate %s is not the canonical SMA-7702 %s",
			delegate.Hex(), config.SMA7702DelegateAddressHex)
	}
	existing, err := GetWallet(db, chainID, owner, owner.Hex())
	if err == nil && existing.IsEOA7702() && existing.Delegate != nil && *existing.Delegate == delegate {
		// Already the row. Do not rewrite — a GET poll would otherwise
		// reset IsHidden to false.
		return nil
	}
	ownerCopy, addrCopy, delCopy := owner, owner, delegate
	rec := &model.SmartWallet{
		Kind:     model.WalletKindEOA7702,
		Owner:    &ownerCopy,
		Address:  &addrCopy,
		Delegate: &delCopy,
	}
	if existing != nil && existing.IsEOA7702() {
		rec.IsHidden = existing.IsHidden
	}
	if err := rec.ValidateEOA7702Shape(); err != nil {
		return err
	}
	return StoreWallet(db, chainID, owner, rec)
}

// assertEOA7702OnChain is K13 against the chain's pin. Fail-closed with
// ErrEOADelegationMissing when the pin is missing, the reader is missing,
// or the code does not match.
func (n *Engine) assertEOA7702OnChain(ctx context.Context, chainID int64, eoa common.Address) error {
	sw := n.ResolveSmartWalletConfig(chainID)
	if sw == nil || !sw.HasSMA7702Pin() {
		return fmt.Errorf("%w: SMA-7702 pin is not configured for chain_id=%d", ErrEOADelegationMissing, chainID)
	}
	reader := GetChainStateReaderForChain(uint64(chainID))
	if reader == nil {
		return fmt.Errorf("%w: no chain reader for chain_id=%d", ErrEOADelegationMissing, chainID)
	}
	eoaCode, err := reader.CodeAt(ctx, eoa)
	if err != nil {
		return fmt.Errorf("%w: reading code(%s): %v", ErrEOADelegationMissing, eoa.Hex(), err)
	}
	implCode, err := reader.CodeAt(ctx, sw.SMA7702Delegate)
	if err != nil {
		return fmt.Errorf("%w: reading SMA-7702 impl: %v", ErrEOADelegationMissing, err)
	}
	if err := sw.AssertSMA7702Designation(eoaCode, implCode); err != nil {
		return fmt.Errorf("%w: %v", ErrEOADelegationMissing, err)
	}
	return nil
}

// AssertEOA7702Delegation is the REST/engine entry for K13.
func (n *Engine) AssertEOA7702Delegation(ctx context.Context, chainID int64, eoa common.Address) error {
	return n.assertEOA7702OnChain(ctx, chainID, eoa)
}

// UpsertEOA7702Wallet persists the B2 record after a successful K13 assert.
func (n *Engine) UpsertEOA7702Wallet(chainID int64, owner common.Address) error {
	if n == nil || n.db == nil {
		return fmt.Errorf("storage unavailable")
	}
	return StoreEOA7702Wallet(n.db, chainID, owner, config.SMA7702Delegate())
}

// StoredWallet loads the wallet row on a specific chain.
func (n *Engine) StoredWallet(chainID int64, owner common.Address, addr string) (*model.SmartWallet, error) {
	if n == nil || n.db == nil {
		return nil, fmt.Errorf("storage unavailable")
	}
	return GetWallet(n.db, chainID, owner, addr)
}

// SetStoredWalletHidden toggles IsHidden on the stored row at (chain, owner, addr).
// Does not re-derive a CREATE2 address — that is what made PATCH /wallets/{eoa}
// hide the salt-0 derived runner instead of the EOA.
func (n *Engine) SetStoredWalletHidden(chainID int64, owner common.Address, addr string, hidden bool) (*model.SmartWallet, error) {
	if n == nil || n.db == nil {
		return nil, fmt.Errorf("storage unavailable")
	}
	if err := SetWalletHiddenStatus(n.db, chainID, owner, addr, hidden); err != nil {
		return nil, err
	}
	return GetWallet(n.db, chainID, owner, addr)
}

// RunWithSessionAuthorityLock serializes writers for one (chain, owner, runner).
// Delegation submit uses it for stale-nonce / already-delegated / send, not
// the 30s mine wait. Type-4 controller nonce is a separate per-controller lock.
func (n *Engine) RunWithSessionAuthorityLock(chainID int64, owner, runner common.Address, fn func() error) error {
	lock := sessionAuthorityLock(chainID, owner, runner)
	lock.Lock()
	defer lock.Unlock()
	return fn()
}
