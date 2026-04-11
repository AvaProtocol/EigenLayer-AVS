package taskengine

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
)

// WalletSaltIndexAddressDeriver returns the address that the given factory
// currently derives for (owner, salt). The migration uses this instead of
// taking an *ethclient.Client directly so that unit tests can inject a
// deterministic fake without standing up a real RPC.
type WalletSaltIndexAddressDeriver func(owner common.Address, factory common.Address, salt *big.Int) (common.Address, error)

// WalletSaltIndexBackfillOptions controls the migration's side effects.
type WalletSaltIndexBackfillOptions struct {
	// DryRun reports what would change without writing anything to BadgerDB.
	DryRun bool
	// Verbose emits one line per processed wallet (canonical or skipped),
	// not just per state change.
	Verbose bool
	// Logf receives formatted progress lines. Pass nil to discard them.
	Logf func(format string, args ...any)
}

// WalletSaltIndexBackfillStats summarizes a migration run.
type WalletSaltIndexBackfillStats struct {
	Total                  int
	CanonicalConfirmed     int
	SecondaryIndexWritten  int
	SecondaryIndexExisting int
	NewlyMarkedStale       int
	AlreadyStale           int
	SkippedMissingFactory  int
	SkippedMissingSalt     int
	SkippedDeriveError     int
}

// BackfillWalletSaltIndex walks every persisted wallet record and either
// writes a (owner, factory, salt) → address secondary index entry (when
// the live derivation still matches the stored address), or marks the row
// stale (when the derivation differs — i.e. the factory's account
// implementation was upgraded since the row was stored).
//
// The function is the shared core for both the `/ava backfill-wallet-salt-index`
// CLI subcommand and the standalone scripts/migration/... wrapper. Tests
// inject a fake deriver to exercise the matrix of canonical / stale /
// missing / error rows without an RPC.
func BackfillWalletSaltIndex(db storage.Storage, derive WalletSaltIndexAddressDeriver, opts WalletSaltIndexBackfillOptions) (*WalletSaltIndexBackfillStats, error) {
	if db == nil {
		return nil, fmt.Errorf("BackfillWalletSaltIndex: db is required")
	}
	if derive == nil {
		return nil, fmt.Errorf("BackfillWalletSaltIndex: derive callback is required")
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(format string, args ...any) {}
	}

	items, err := db.GetByPrefix([]byte("w:"))
	if err != nil {
		return nil, fmt.Errorf("scan wallet records: %w", err)
	}

	stats := &WalletSaltIndexBackfillStats{}

	for _, item := range items {
		stats.Total++

		wallet := &model.SmartWallet{}
		if parseErr := wallet.FromStorageData(item.Value); parseErr != nil {
			logf("  [skip] failed to parse %s: %v", string(item.Key), parseErr)
			continue
		}
		if wallet.Owner == nil || wallet.Address == nil {
			logf("  [skip] %s has nil owner or address", string(item.Key))
			continue
		}
		if wallet.Factory == nil {
			stats.SkippedMissingFactory++
			if opts.Verbose {
				logf("  [skip] %s missing factory", wallet.Address.Hex())
			}
			continue
		}
		if wallet.Salt == nil {
			stats.SkippedMissingSalt++
			if opts.Verbose {
				logf("  [skip] %s missing salt", wallet.Address.Hex())
			}
			continue
		}

		owner := *wallet.Owner
		factory := *wallet.Factory

		derived, deriveErr := derive(owner, factory, wallet.Salt)
		if deriveErr != nil {
			stats.SkippedDeriveError++
			logf("  [warn] derive failed for owner=%s factory=%s salt=%s: %v",
				owner.Hex(), factory.Hex(), wallet.Salt.String(), deriveErr)
			continue
		}

		canonical := strings.EqualFold(derived.Hex(), wallet.Address.Hex())

		if canonical {
			stats.CanonicalConfirmed++

			indexKey := WalletBySaltKey(owner, factory, wallet.Salt)
			existing, lookupErr := db.GetKey(indexKey)
			alreadyCorrect := lookupErr == nil && strings.EqualFold(string(existing), wallet.Address.Hex())

			if alreadyCorrect {
				stats.SecondaryIndexExisting++
				if opts.Verbose {
					logf("  [ok ]   %s already indexed (owner=%s factory=%s salt=%s)",
						wallet.Address.Hex(), owner.Hex(), factory.Hex(), wallet.Salt.String())
				}
				continue
			}

			stats.SecondaryIndexWritten++
			logf("  [idx]   index → %s (owner=%s factory=%s salt=%s)",
				wallet.Address.Hex(), owner.Hex(), factory.Hex(), wallet.Salt.String())
			if !opts.DryRun {
				if setErr := db.Set(indexKey, []byte(strings.ToLower(wallet.Address.Hex()))); setErr != nil {
					logf("  [err] write secondary index: %v", setErr)
				}
			}
			continue
		}

		// Mismatch — stored address is no longer derivable from
		// (owner, factory, salt). Zombie from a previous factory
		// implementation.
		if wallet.StaleDerivation {
			stats.AlreadyStale++
			if opts.Verbose {
				logf("  [old]   %s already stale (owner=%s)", wallet.Address.Hex(), owner.Hex())
			}
			continue
		}

		stats.NewlyMarkedStale++
		logf("  [stale] %s ≠ live derivation %s (owner=%s factory=%s salt=%s)",
			wallet.Address.Hex(), derived.Hex(), owner.Hex(), factory.Hex(), wallet.Salt.String())
		if !opts.DryRun {
			if markErr := MarkWalletStale(db, owner, wallet.Address.Hex()); markErr != nil {
				logf("  [err] mark stale: %v", markErr)
			}
		}
	}

	return stats, nil
}
