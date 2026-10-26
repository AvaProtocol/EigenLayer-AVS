package migrations

import (
	"fmt"
	"log"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// RetireLegacyWalletsLogPrefix starts the one log line written per retired
// wallet, so operators can grep the gateway log for the full list.
const RetireLegacyWalletsLogPrefix = "retire-legacy-wallets:"

// walletKeyPrefix is the primary wallet namespace, `w:<chainID>:<owner>:<address>`
// (taskengine.WalletStorageKey). The `wsalt:` index does not match it.
const walletKeyPrefix = "w:"

// IsLegacyFactoryWallet reports a derived wallet whose factory is not the MA v2
// factory, including records stored before the factory was recorded.
//
// The gateway only creates and executes MA v2 accounts since the EntryPoint
// v0.7 cutover. The MA v2 factory is the same address on every chain, so this
// needs no chain config.
// A designated EOA (kind eoa_7702) has no factory and is never legacy.
func IsLegacyFactoryWallet(w *model.SmartWallet) bool {
	if w == nil || w.Address == nil || w.IsEOA7702() {
		return false
	}
	return w.Factory == nil || *w.Factory != aa.MAv2FactoryAddress()
}

// RetireLegacyFactoryWallets marks every legacy-factory wallet record Retired
// and hidden, on every chain and for every owner. Nothing is deleted: the
// on-chain account may still hold assets, and workflows, policies and fee
// records refer to wallets by address. Retired wallets drop out of
// ListWallets and out of the per-owner wallet cap.
//
// Only the primary record is rewritten. The `wsalt:` index keeps pointing at
// the same address, which is correct: the (owner, factory, salt) tuple still
// derives it, and no MA v2 wallet shares a legacy factory's index key.
//
// It logs one line per retired wallet (owner, chain, address, factory, salt,
// workflow count) for follow-up; scripts/retired_wallet_funds reports which of
// them still hold funds. Re-running finds nothing to change.
func RetireLegacyFactoryWallets(db storage.Storage) (int, error) {
	items, err := db.GetByPrefix([]byte(walletKeyPrefix))
	if err != nil {
		return 0, fmt.Errorf("scan %q: %w", walletKeyPrefix, err)
	}

	retired := 0
	skipped := 0
	for _, item := range items {
		key := string(item.Key)
		chainID, owner, ok := parseWalletKey(key)
		if !ok {
			skipped++
			continue
		}
		wallet := &model.SmartWallet{}
		if err := wallet.FromStorageData(item.Value); err != nil {
			skipped++
			log.Printf("%s skipped unreadable record key=%s error=%v", RetireLegacyWalletsLogPrefix, key, err)
			continue
		}
		if wallet.Retired || !IsLegacyFactoryWallet(wallet) {
			continue
		}

		workflows, err := countWalletWorkflows(db, owner, wallet.Address.Hex())
		if err != nil {
			return retired, fmt.Errorf("count workflows for %s: %w", key, err)
		}

		wasHidden := wallet.IsHidden
		wallet.Retired = true
		wallet.IsHidden = true
		data, err := wallet.ToJSON()
		if err != nil {
			return retired, fmt.Errorf("marshal %s: %w", key, err)
		}
		// One Set per record: BatchWrite drops commit errors. A failure here
		// fails the migration before it is marked done, and the next start
		// resumes (already-retired records are skipped).
		if err := db.Set([]byte(key), data); err != nil {
			return retired, fmt.Errorf("write %s: %w", key, err)
		}
		retired++

		log.Printf("%s retired chain=%s owner=%s wallet=%s factory=%s salt=%s workflows=%d was_hidden=%t stale=%t",
			RetireLegacyWalletsLogPrefix, chainID, owner, strings.ToLower(wallet.Address.Hex()),
			factoryString(wallet), saltString(wallet), workflows, wasHidden, wallet.StaleDerivation)
	}

	// One grep confirms the run. skipped counts unreadable values and keys
	// that are not w:<chain>:<owner>:<address>. Already-retired and MA v2
	// rows are left alone and are not part of that count.
	log.Printf("%s done retired=%d skipped=%d", RetireLegacyWalletsLogPrefix, retired, skipped)
	return retired, nil
}

// parseWalletKey splits `w:<chainID>:<owner>:<address>`.
func parseWalletKey(key string) (chainID string, owner string, ok bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 4 || parts[0] != "w" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// countWalletWorkflows counts task index rows `u:<owner>:<wallet>:<taskID>`
// (taskengine.SmartWalletTaskStoragePrefix), any status.
func countWalletWorkflows(db storage.Storage, owner string, wallet string) (int, error) {
	prefix := fmt.Sprintf("u:%s:%s:", strings.ToLower(owner), strings.ToLower(wallet))
	count := 0
	err := db.IterateKeysOnly([]byte(prefix), func([]byte) error {
		count++
		return nil
	})
	return count, err
}

func factoryString(w *model.SmartWallet) string {
	if w.Factory == nil {
		return "none"
	}
	return strings.ToLower(w.Factory.Hex())
}

func saltString(w *model.SmartWallet) string {
	if w.Salt == nil {
		return "none"
	}
	return w.Salt.String()
}
