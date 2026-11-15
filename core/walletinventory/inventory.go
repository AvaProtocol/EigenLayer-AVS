// Package walletinventory owns the network-independent MA v2 wallet inventory.
// Runtime records remain chain-scoped; visibility, admission and allocation do not.
package walletinventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/big"
	"sort"
	"strings"
	"sync"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
)

const Profile = "alchemy.ma-v2.semi-modular-bytecode.1"
const DefaultLimit = 3

var (
	ErrLimit         = errors.New("max smart wallet count reached for owner")
	ErrConflict      = errors.New("wallet derivation conflicts with the registered identity")
	ErrKeyReused     = errors.New("allocation idempotency key reused with different inputs")
	ErrSaltExhausted = errors.New("smart wallet salt space exhausted")
	ErrBadAllocation = errors.New("invalid wallet allocation request")
	// These shards are deliberately independent of execution and session locks.
	// Inventory is a leaf: no network calls, simulation or other locks while held.
	inventoryLocks [256]sync.Mutex
)

type Metadata struct {
	Migrated bool   `json:"migrated,omitempty"`
	Revision uint64 `json:"revision"`
	NextSalt string `json:"nextSalt"`
	Conflict bool   `json:"conflict,omitempty"`
}

type Usage struct {
	HasHistory  bool   `json:"-"`
	Scope       string `json:"scope"`
	ActiveCount int    `json:"activeCount"`
	ActiveLimit int    `json:"activeLimit"`
	Revision    uint64 `json:"revision"`
}

type replay struct {
	Digest  string `json:"digest"`
	Address string `json:"address"`
}

func ownerString(owner common.Address) string { return strings.ToLower(owner.Hex()) }
func Prefix(owner common.Address) []byte      { return []byte("wi:" + ownerString(owner) + ":") }
func Key(owner common.Address, address string) string {
	return string(Prefix(owner)) + strings.ToLower(address)
}
func Owner(db storage.Storage, address common.Address) (common.Address, bool, error) {
	raw, err := db.GetKey([]byte("wiowner:" + strings.ToLower(address.Hex())))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return common.Address{}, false, nil
	}
	if err != nil {
		return common.Address{}, false, err
	}
	if !common.IsHexAddress(string(raw)) {
		return common.Address{}, false, fmt.Errorf("invalid global wallet owner index")
	}
	owner := common.HexToAddress(string(raw))
	w, err := Get(db, owner, address.Hex())
	if err != nil {
		return common.Address{}, false, err
	}
	if w.Retired || w.StaleDerivation {
		return common.Address{}, false, nil
	}
	return owner, true, nil
}
func metaKey(owner common.Address) []byte { return []byte("wimeta:" + ownerString(owner)) }
func saltKey(owner common.Address, salt *big.Int) []byte {
	return []byte("wisalt:" + ownerString(owner) + ":" + Profile + ":" + salt.String())
}
func lock(db storage.Storage, owner common.Address) *sync.Mutex {
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%s:%s", db.DbPath(), ownerString(owner))
	return &inventoryLocks[h.Sum32()%uint32(len(inventoryLocks))]
}
func commit(db storage.Storage, writes map[string][]byte) error {
	writer, ok := db.(interface {
		AtomicWrite(map[string][]byte, [][]byte) error
	})
	if !ok {
		return fmt.Errorf("wallet inventory requires atomic storage")
	}
	return writer.AtomicWrite(writes, nil)
}
func encode(v interface{}) ([]byte, error) { return json.Marshal(v) }
func IsDerived(w *model.SmartWallet) bool {
	return w != nil && w.Address != nil && *w.Address != (common.Address{}) && w.Owner != nil && w.Factory != nil && *w.Factory == aa.MAv2FactoryAddress() && w.Salt != nil && w.Salt.Sign() >= 0 && w.Salt.BitLen() <= 256 && !w.IsEOA7702()
}
func Active(w *model.SmartWallet) bool {
	return IsDerived(w) && !w.IsHidden && !w.Retired && !w.StaleDerivation
}

// IdentityID includes the pinned address so conflicting historical records
// remain distinct recovery identities while the canonical salt index is blocked.
func IdentityID(owner common.Address, salt *big.Int, address common.Address) string {
	sum := sha256.Sum256([]byte(ownerString(owner) + ":" + Profile + ":" + salt.String() + ":" + strings.ToLower(address.Hex())))
	return hex.EncodeToString(sum[:])
}
func readMeta(db storage.Storage, owner common.Address) (Metadata, error) {
	raw, err := db.GetKey(metaKey(owner))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return Metadata{NextSalt: "0"}, nil
	}
	if err != nil {
		return Metadata{}, err
	}
	var m Metadata
	if err = json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	next, ok := new(big.Int).SetString(m.NextSalt, 10)
	if !ok || next.Sign() < 0 || next.BitLen() > 257 {
		return m, fmt.Errorf("invalid inventory salt state")
	}
	return m, nil
}
func Get(db storage.Storage, owner common.Address, address string) (*model.SmartWallet, error) {
	raw, err := db.GetKey([]byte(Key(owner, address)))
	if err != nil {
		return nil, err
	}
	var w model.SmartWallet
	if err = w.FromStorageData(raw); err != nil {
		return nil, err
	}
	if !IsDerived(&w) || *w.Owner != owner || !strings.EqualFold(w.Address.Hex(), address) || w.WalletID != IdentityID(owner, w.Salt, *w.Address) || w.DeploymentProfile != Profile {
		return nil, fmt.Errorf("invalid inventory identity")
	}
	return &w, nil
}
func BySalt(db storage.Storage, owner common.Address, salt *big.Int) (*model.SmartWallet, error) {
	raw, err := db.GetKey(saltKey(owner, salt))
	if err != nil {
		return nil, err
	}
	if !common.IsHexAddress(string(raw)) {
		return nil, fmt.Errorf("invalid inventory salt index")
	}
	w, err := Get(db, owner, string(raw))
	if err == nil && w.Salt.Cmp(salt) != 0 {
		return nil, ErrConflict
	}
	return w, err
}
func listLocked(db storage.Storage, owner common.Address) ([]*model.SmartWallet, error) {
	items, err := db.GetByPrefix(Prefix(owner))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return []*model.SmartWallet{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]*model.SmartWallet, 0, len(items))
	for _, item := range items {
		var w model.SmartWallet
		if err = w.FromStorageData(item.Value); err != nil {
			return nil, err
		}
		if !IsDerived(&w) || *w.Owner != owner || string(item.Key) != Key(owner, w.Address.Hex()) || w.WalletID != IdentityID(owner, w.Salt, *w.Address) || w.DeploymentProfile != Profile {
			return nil, fmt.Errorf("invalid inventory record %s", item.Key)
		}
		result = append(result, &w)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Salt.Cmp(result[j].Salt) < 0 })
	return result, nil
}
func Snapshot(db storage.Storage, owner common.Address, limit int) ([]*model.SmartWallet, Usage, error) {
	mu := lock(db, owner)
	mu.Lock()
	defer mu.Unlock()
	list, err := listLocked(db, owner)
	if err != nil {
		return nil, Usage{}, err
	}
	meta, err := readMeta(db, owner)
	if err != nil {
		return nil, Usage{}, err
	}
	usage := Usage{Scope: "owner", ActiveLimit: limit, Revision: meta.Revision, HasHistory: meta.NextSalt != "0"}
	for _, w := range list {
		if Active(w) {
			usage.ActiveCount++
		}
	}
	return list, usage, nil
}
func runtimeWrites(chainID int64, owner common.Address, w *model.SmartWallet) (map[string][]byte, error) {
	raw, err := w.ToJSON()
	if err != nil {
		return nil, err
	}
	writes := map[string][]byte{fmt.Sprintf("w:%d:%s:%s", chainID, ownerString(owner), strings.ToLower(w.Address.Hex())): raw}
	if w.Salt != nil && w.Factory != nil && !w.StaleDerivation {
		writes[fmt.Sprintf("wsalt:%d:%s:%s:%s", chainID, ownerString(owner), strings.ToLower(w.Factory.Hex()), w.Salt.String())] = []byte(strings.ToLower(w.Address.Hex()))
	}
	return writes, nil
}

// Materialize writes only compatibility/runtime records, using canonical visibility.
func Materialize(db storage.Storage, chainID int64, owner common.Address, address string) (*model.SmartWallet, error) {
	mu := lock(db, owner)
	mu.Lock()
	defer mu.Unlock()
	w, err := Get(db, owner, address)
	if err != nil {
		return nil, err
	}
	writes, err := runtimeWrites(chainID, owner, w)
	if err != nil {
		return nil, err
	}
	raw, readErr := db.GetKey([]byte(fmt.Sprintf("w:%d:%s:%s", chainID, ownerString(owner), strings.ToLower(address))))
	if readErr == nil && string(raw) == string(writes[fmt.Sprintf("w:%d:%s:%s", chainID, ownerString(owner), strings.ToLower(address))]) {
		return w, nil
	}
	if readErr != nil && !errors.Is(readErr, badger.ErrKeyNotFound) {
		return nil, readErr
	}
	if err = commit(db, writes); err != nil {
		return nil, err
	}
	return w, nil
}
func registerLocked(db storage.Storage, chainID int64, owner common.Address, incoming *model.SmartWallet, limit int, extra map[string][]byte) (*model.SmartWallet, error) {
	if !IsDerived(incoming) || *incoming.Owner != owner {
		return nil, fmt.Errorf("invalid derived wallet")
	}
	existing, err := BySalt(db, owner, incoming.Salt)
	if err == nil {
		if *existing.Address != *incoming.Address {
			return nil, ErrConflict
		}
		writes, writeErr := runtimeWrites(chainID, owner, existing)
		if writeErr != nil {
			return nil, writeErr
		}
		for k, v := range extra {
			writes[k] = v
		}
		if err = commit(db, writes); err != nil {
			return nil, err
		}
		return existing, nil
	}
	if !errors.Is(err, badger.ErrKeyNotFound) {
		return nil, err
	}
	if byAddress, addressErr := Get(db, owner, incoming.Address.Hex()); addressErr == nil {
		if byAddress.Salt.Cmp(incoming.Salt) != 0 {
			return nil, ErrConflict
		}
	} else if !errors.Is(addressErr, badger.ErrKeyNotFound) {
		return nil, addressErr
	}
	meta, err := readMeta(db, owner)
	if err != nil {
		return nil, err
	}
	if meta.Conflict {
		return nil, ErrConflict
	}
	list, err := listLocked(db, owner)
	if err != nil {
		return nil, err
	}
	count := 0
	for _, w := range list {
		if Active(w) {
			count++
		}
	}
	if Active(incoming) && limit > 0 && count >= limit {
		return nil, fmt.Errorf("%w (limit=%d)", ErrLimit, limit)
	}
	wallet := *incoming
	wallet.WalletID = IdentityID(owner, wallet.Salt, *wallet.Address)
	wallet.DeploymentProfile = Profile
	meta.Revision++
	next, _ := new(big.Int).SetString(meta.NextSalt, 10)
	if wallet.Salt.Cmp(next) >= 0 {
		meta.NextSalt = new(big.Int).Add(wallet.Salt, big.NewInt(1)).String()
	}
	writes, err := runtimeWrites(chainID, owner, &wallet)
	if err != nil {
		return nil, err
	}
	raw, err := wallet.ToJSON()
	if err != nil {
		return nil, err
	}
	writes[Key(owner, wallet.Address.Hex())] = raw
	writes["wiowner:"+strings.ToLower(wallet.Address.Hex())] = []byte(ownerString(owner))
	writes[string(saltKey(owner, wallet.Salt))] = []byte(strings.ToLower(wallet.Address.Hex()))
	writes[string(metaKey(owner))], err = encode(meta)
	if err != nil {
		return nil, err
	}
	for k, v := range extra {
		writes[k] = v
	}
	if err = commit(db, writes); err != nil {
		return nil, err
	}
	return &wallet, nil
}
func Register(db storage.Storage, chainID int64, owner common.Address, wallet *model.SmartWallet, limit int) (*model.SmartWallet, error) {
	mu := lock(db, owner)
	mu.Lock()
	defer mu.Unlock()
	return registerLocked(db, chainID, owner, wallet, limit, nil)
}

// StoreCompatibility is the low-level stored-record writer. It never restores an
// existing global identity from a stale network projection. Admission belongs to Register.
func StoreCompatibility(db storage.Storage, chainID int64, owner common.Address, wallet *model.SmartWallet) error {
	mu := lock(db, owner)
	mu.Lock()
	defer mu.Unlock()
	if !wallet.Retired && !wallet.StaleDerivation {
		_, err := registerLocked(db, chainID, owner, wallet, 0, nil)
		return err
	}
	writes, err := runtimeWrites(chainID, owner, wallet)
	if err != nil {
		return err
	}
	existing, getErr := Get(db, owner, wallet.Address.Hex())
	if getErr == nil {
		existing.StaleDerivation = wallet.StaleDerivation
		existing.Retired = wallet.Retired
		existing.IsHidden = true
		raw, err := existing.ToJSON()
		if err != nil {
			return err
		}
		writes[Key(owner, wallet.Address.Hex())] = raw
		writes["wiowner:"+strings.ToLower(wallet.Address.Hex())] = []byte(ownerString(owner))
		meta, err := readMeta(db, owner)
		if err != nil {
			return err
		}
		meta.Revision++
		writes[string(metaKey(owner))], err = encode(meta)
		if err != nil {
			return err
		}
	} else if !errors.Is(getErr, badger.ErrKeyNotFound) {
		return getErr
	}
	return commit(db, writes)
}
func SetHidden(db storage.Storage, owner common.Address, address string, hidden bool, limit int) (*model.SmartWallet, error) {
	mu := lock(db, owner)
	mu.Lock()
	defer mu.Unlock()
	w, err := Get(db, owner, address)
	if err != nil {
		return nil, err
	}
	if w.IsHidden == hidden {
		return w, nil
	}
	meta, err := readMeta(db, owner)
	if err != nil {
		return nil, err
	}
	if !hidden && !w.Retired && !w.StaleDerivation {
		if meta.Conflict {
			return nil, ErrConflict
		}
		list, err := listLocked(db, owner)
		if err != nil {
			return nil, err
		}
		active := 0
		for _, other := range list {
			if Active(other) {
				active++
			}
		}
		if limit > 0 && active >= limit {
			return nil, fmt.Errorf("%w (limit=%d)", ErrLimit, limit)
		}
	}
	w.IsHidden = hidden
	meta.Revision++
	raw, err := w.ToJSON()
	if err != nil {
		return nil, err
	}
	metaRaw, err := encode(meta)
	if err != nil {
		return nil, err
	}
	if err = commit(db, map[string][]byte{Key(owner, address): raw, string(metaKey(owner)): metaRaw}); err != nil {
		return nil, err
	}
	return w, nil
}

// Allocate durably maps (owner, operation, client key) to one identity. The
// network hint is intentionally excluded. Derivation happens outside the lock.
func Allocate(ctx context.Context, db storage.Storage, chainID int64, owner common.Address, key, profile string, limit int, derive func(*big.Int) (*model.SmartWallet, error)) (*model.SmartWallet, error) {
	if strings.TrimSpace(key) == "" || len(key) > 256 {
		return nil, fmt.Errorf("%w: idempotency key must be 1 to 256 bytes", ErrBadAllocation)
	}
	if profile == "" {
		profile = Profile
	}
	digest := sha256.Sum256([]byte(profile))
	keyHash := sha256.Sum256([]byte(key))
	replayKey := []byte("wialloc:" + ownerString(owner) + ":" + hex.EncodeToString(keyHash[:]))
	replayDigest := hex.EncodeToString(digest[:])
	mu := lock(db, owner)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		mu.Lock()
		raw, err := db.GetKey(replayKey)
		if err == nil {
			var saved replay
			if err = json.Unmarshal(raw, &saved); err != nil {
				mu.Unlock()
				return nil, err
			}
			if saved.Digest != replayDigest {
				mu.Unlock()
				return nil, ErrKeyReused
			}
			w, err := Get(db, owner, saved.Address)
			mu.Unlock()
			return w, err
		}
		if !errors.Is(err, badger.ErrKeyNotFound) {
			mu.Unlock()
			return nil, err
		}
		if profile != Profile {
			mu.Unlock()
			return nil, fmt.Errorf("%w: unsupported deployment profile", ErrBadAllocation)
		}
		meta, err := readMeta(db, owner)
		mu.Unlock()
		if err != nil {
			return nil, err
		}
		salt, _ := new(big.Int).SetString(meta.NextSalt, 10)
		if salt.BitLen() > 256 {
			return nil, ErrSaltExhausted
		}
		w, err := derive(salt)
		if err != nil {
			return nil, err
		}
		if !IsDerived(w) || *w.Owner != owner || w.Salt.Cmp(salt) != 0 {
			return nil, fmt.Errorf("allocator derived an invalid identity")
		}

		mu.Lock()
		latest, err := readMeta(db, owner)
		if err != nil {
			mu.Unlock()
			return nil, err
		}
		if latest.Revision != meta.Revision {
			mu.Unlock()
			continue
		}
		// Recheck replay even if another allocation of the same key changed no state.
		if _, err = db.GetKey(replayKey); err == nil {
			mu.Unlock()
			continue
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			mu.Unlock()
			return nil, err
		}
		saved, err := encode(replay{Digest: replayDigest, Address: w.Address.Hex()})
		if err != nil {
			mu.Unlock()
			return nil, err
		}
		result, err := registerLocked(db, chainID, owner, w, limit, map[string][]byte{string(replayKey): saved})
		mu.Unlock()
		return result, err
	}
}
