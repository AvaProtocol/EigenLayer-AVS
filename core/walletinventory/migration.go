package walletinventory

import (
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
)

// Migrate imports the union of MA v2 runtime records. Each owner's import and
// marker commit together; retries never overwrite a post-migration archive.
func Migrate(db storage.Storage) (int, error) {
	rows, err := db.GetByPrefix([]byte("w:"))
	if err != nil {
		return 0, err
	}
	groups := map[common.Address]map[string]*model.SmartWallet{}
	highest := map[common.Address]*big.Int{}
	for _, row := range rows {
		parts := strings.Split(string(row.Key), ":")
		if len(parts) != 4 || !common.IsHexAddress(parts[2]) || !common.IsHexAddress(parts[3]) {
			return 0, fmt.Errorf("invalid wallet runtime key %q", row.Key)
		}
		var w model.SmartWallet
		if err = w.FromStorageData(row.Value); err != nil {
			return 0, fmt.Errorf("decode %s: %w", row.Key, err)
		}
		if !IsDerived(&w) {
			continue
		}
		owner := common.HexToAddress(parts[2])
		if *w.Owner != owner || !strings.EqualFold(w.Address.Hex(), parts[3]) {
			return 0, fmt.Errorf("wallet key/owner mismatch: %s", row.Key)
		}
		if groups[owner] == nil {
			groups[owner] = map[string]*model.SmartWallet{}
		}
		if highest[owner] == nil || w.Salt.Cmp(highest[owner]) > 0 {
			highest[owner] = new(big.Int).Set(w.Salt)
		}
		if w.Retired || w.StaleDerivation {
			continue
		}

		address := strings.ToLower(w.Address.Hex())
		if previous := groups[owner][address]; previous != nil {
			if previous.Salt.Cmp(w.Salt) != 0 {
				return 0, fmt.Errorf("wallet address has conflicting salts: %s", row.Key)
			}
			if previous.IsHidden != w.IsHidden {
				log.Printf("global-wallet-inventory: mixed visibility owner=%s wallet=%s", owner.Hex(), address)
			}
			previous.IsHidden = previous.IsHidden && w.IsHidden
		} else {
			copy := w
			groups[owner][address] = &copy
		}
	}
	count := 0
	owners := make([]common.Address, 0, len(groups))
	for owner := range groups {
		owners = append(owners, owner)
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i].Hex() < owners[j].Hex() })
	for _, owner := range owners {
		changed, err := migrateOwner(db, owner, groups[owner], highest[owner])
		if err != nil {
			return count, err
		}
		count += changed
	}
	return count, nil
}
func migrateOwner(db storage.Storage, owner common.Address, wallets map[string]*model.SmartWallet, highest *big.Int) (int, error) {
	mu := lock(db, owner)
	mu.Lock()
	defer mu.Unlock()
	meta, err := readMeta(db, owner)
	if err != nil {
		return 0, err
	}
	if meta.Migrated {
		return 0, nil
	}
	writes := map[string][]byte{}
	var deletes [][]byte
	salts := map[string][]*model.SmartWallet{}
	// Include already registered identities even if the migration's runtime
	// scan predates their creation. A retry must not replace their salt index.
	current, err := listLocked(db, owner)
	if err != nil {
		return 0, err
	}
	merged := make(map[string]*model.SmartWallet, len(wallets)+len(current))
	for address, wallet := range wallets {
		merged[address] = wallet
	}
	for _, wallet := range current {
		address := strings.ToLower(wallet.Address.Hex())
		if incoming := merged[address]; incoming != nil && incoming.Salt.Cmp(wallet.Salt) != 0 {
			meta.Conflict = true
			log.Printf("global-wallet-inventory: conflicting salts owner=%s wallet=%s; preserving registered identity", owner.Hex(), address)
		}
		merged[address] = wallet // Current global visibility always wins.
	}
	next, _ := new(big.Int).SetString(meta.NextSalt, 10)
	if highest != nil && highest.Cmp(next) >= 0 {
		next = new(big.Int).Add(highest, big.NewInt(1))
	}
	for _, incoming := range merged {
		w := *incoming
		existing, err := Get(db, owner, w.Address.Hex())
		if err == nil {
			w = *existing
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return 0, err
		}
		w.WalletID = IdentityID(owner, w.Salt, *w.Address)
		w.DeploymentProfile = Profile
		raw, err := w.ToJSON()
		if err != nil {
			return 0, err
		}
		writes[Key(owner, w.Address.Hex())] = raw
		writes["wiowner:"+strings.ToLower(w.Address.Hex())] = []byte(ownerString(owner))
		salts[w.Salt.String()] = append(salts[w.Salt.String()], &w)
		if w.Salt.Cmp(next) >= 0 {
			next = new(big.Int).Add(w.Salt, big.NewInt(1))
		}
	}
	for _, rows := range salts {
		if len(rows) > 1 {
			meta.Conflict = true
			deletes = append(deletes, saltKey(owner, rows[0].Salt))
			log.Printf("global-wallet-inventory: conflicting addresses owner=%s salt=%s; new activations blocked", owner.Hex(), rows[0].Salt)
		} else {
			indexed, err := db.GetKey(saltKey(owner, rows[0].Salt))
			if err == nil && !strings.EqualFold(string(indexed), rows[0].Address.Hex()) {
				return 0, fmt.Errorf("%w: invalid preexisting salt index for %s", ErrConflict, rows[0].Salt)
			}
			if err != nil && !errors.Is(err, badger.ErrKeyNotFound) {
				return 0, err
			}
			writes[string(saltKey(owner, rows[0].Salt))] = []byte(strings.ToLower(rows[0].Address.Hex()))
		}
	}
	meta.Migrated = true
	meta.NextSalt = next.String()
	meta.Revision++
	writes[string(metaKey(owner))], err = encode(meta)
	if err != nil {
		return 0, err
	}
	if err = commit(db, writes, deletes...); err != nil {
		return 0, err
	}
	log.Printf("global-wallet-inventory: imported owner=%s identities=%d conflict=%t", owner.Hex(), len(wallets), meta.Conflict)
	return len(wallets), nil
}
