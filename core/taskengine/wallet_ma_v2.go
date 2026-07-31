package taskengine

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// Modular Account v2 wallet creation.
//
// MA v2 wallets reuse the existing record and index schema unchanged, because
// `Factory` is already part of both the stored record and the `wsalt:` index
// key. A v0.6 SimpleAccount at salt 0 and an MA v2 account at salt 0 for the
// same owner therefore occupy different index slots and coexist without
// collision — no migration, no separate keyspace, and ListWallets keeps
// working on both.
//
// Registration is not bookkeeping. The Gas Manager sponsorship webhook
// (aggregator/gas_manager_webhook.go) resolves a UserOperation's sender back
// to an owner by scanning `w:<chain>:` and REFUSES senders it cannot find —
// that refusal is the only thing stopping the policy from funding wallets
// created outside this gateway, since policy access control is set to None.
// An MA v2 wallet that is derived but never stored is therefore invisible to
// sponsorship: every operation from it is denied, with a message about an
// unknown sender rather than anything pointing at the missing record.

// MAv2WalletFactory returns the factory address MA v2 wallets are recorded
// under. Distinct from config.DefaultFactoryProxyAddressHex, which is the
// v0.6 SimpleAccountFactory.
func MAv2WalletFactory() common.Address { return aa.MAv2FactoryAddress() }

// IsMAv2Wallet reports whether a stored record was created by the MA v2
// factory, as opposed to the v0.6 SimpleAccountFactory.
//
// Records predating the factory field (Factory == nil) are v0.6 by
// definition — MA v2 records have always carried it.
func IsMAv2Wallet(wallet *model.SmartWallet) bool {
	if wallet == nil || wallet.Factory == nil {
		return false
	}
	return *wallet.Factory == MAv2WalletFactory()
}

// GetOrCreateMAv2Wallet derives the MA v2 account for (owner, salt) on the
// given chain and persists its record, returning the existing one if it has
// already been registered.
//
// Idempotent by address rather than by "did we just derive it": the address is
// a pure function of (factory, owner, salt), so a second call for the same
// tuple must return the same record instead of writing a duplicate or
// resetting user-set flags like IsHidden.
//
// The address comes from the factory on-chain (see aa.GetSenderAddressMAv2)
// rather than a local CREATE2 computation, so a derivation that drifts from
// the contract fails loudly here instead of registering an address that will
// never hold anything.
func GetOrCreateMAv2Wallet(db storage.Storage, rpcConn *ethclient.Client, chainID int64, owner common.Address, salt *big.Int) (*model.SmartWallet, error) {
	if db == nil {
		return nil, fmt.Errorf("nil storage")
	}
	if salt == nil {
		return nil, fmt.Errorf("salt is nil")
	}
	if salt.Sign() < 0 {
		return nil, fmt.Errorf("salt must be non-negative, got %s", salt)
	}
	if owner == (common.Address{}) {
		return nil, fmt.Errorf("owner is the zero address")
	}

	factory := MAv2WalletFactory()

	// Prefer the (chain, owner, factory, salt) index over deriving again — it
	// answers "has this been registered?" without an RPC round trip, and it is
	// the same index StoreWallet maintains.
	//
	// Only a genuine "not recorded yet" may fall through to derive-and-store.
	// Treating any read error as absence would re-register on a transient
	// storage fault, and re-registration writes IsHidden:false — silently
	// un-hiding a wallet the user had hidden. Idempotency here is only as good
	// as the lookup it rests on.
	existingAddr, err := LookupCanonicalWalletAddress(db, chainID, owner, factory, salt)
	switch {
	case err == nil:
		wallet, getErr := GetWallet(db, chainID, owner, existingAddr.Hex())
		if getErr == nil {
			return wallet, nil
		}
		if !errors.Is(getErr, badger.ErrKeyNotFound) {
			return nil, fmt.Errorf("reading registered MA v2 wallet %s for owner %s on chain %d: %w",
				existingAddr.Hex(), owner.Hex(), chainID, getErr)
		}
		// Index points at a record that is gone. Rewrite it rather than
		// returning a dangling reference.
	case errors.Is(err, badger.ErrKeyNotFound):
		// Not registered yet — the expected path for a new wallet.
	default:
		return nil, fmt.Errorf("looking up MA v2 wallet index for owner %s salt %s on chain %d: %w",
			owner.Hex(), salt, chainID, err)
	}

	derived, err := aa.GetSenderAddressMAv2ForFactory(rpcConn, owner, factory, salt)
	if err != nil {
		return nil, fmt.Errorf("deriving MA v2 wallet for owner %s salt %s on chain %d: %w",
			owner.Hex(), salt, chainID, err)
	}
	if derived == nil || *derived == (common.Address{}) {
		return nil, fmt.Errorf("factory %s returned the zero address for owner %s salt %s",
			factory.Hex(), owner.Hex(), salt)
	}

	wallet := &model.SmartWallet{
		Owner:    &owner,
		Address:  derived,
		Factory:  &factory,
		Salt:     salt,
		IsHidden: false,
	}
	if err := StoreWallet(db, chainID, owner, wallet); err != nil {
		return nil, fmt.Errorf("storing MA v2 wallet %s for owner %s on chain %d: %w",
			derived.Hex(), owner.Hex(), chainID, err)
	}
	return wallet, nil
}
