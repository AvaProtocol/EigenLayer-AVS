package walletinventory

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func testDB(t *testing.T) storage.Storage {
	t.Helper()
	db, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}
func testWallet(owner common.Address, salt int64) *model.SmartWallet {
	n := big.NewInt(salt)
	factory := aa.MAv2FactoryAddress()
	hash := crypto.Keccak256(owner.Bytes(), n.Bytes())
	address := common.BytesToAddress(hash[12:])
	return &model.SmartWallet{Owner: &owner, Address: &address, Factory: &factory, Salt: n}
}

var testOwner = common.HexToAddress("0x1111111111111111111111111111111111111111")

func TestGlobalAdmissionHideCreateRestore(t *testing.T) {
	db := testDB(t)
	for salt := int64(0); salt < 3; salt++ {
		_, err := Register(db, 1, testOwner, testWallet(testOwner, salt), 3)
		require.NoError(t, err)
	}
	_, err := Register(db, 8453, testOwner, testWallet(testOwner, 4), 3)
	require.ErrorIs(t, err, ErrLimit)
	zero := testWallet(testOwner, 0)
	_, err = SetHidden(db, testOwner, zero.Address.Hex(), true, 3)
	require.NoError(t, err)
	four, err := Register(db, 8453, testOwner, testWallet(testOwner, 4), 3)
	require.NoError(t, err)
	require.NotEmpty(t, four.WalletID)
	require.Equal(t, Profile, four.DeploymentProfile)
	_, err = SetHidden(db, testOwner, zero.Address.Hex(), false, 3)
	require.ErrorIs(t, err, ErrLimit)
	// Ensuring a hidden wallet on a new network cannot restore it or consume a slot.
	hidden, err := Register(db, 10, testOwner, zero, 3)
	require.NoError(t, err)
	require.True(t, hidden.IsHidden)
	materialized, err := Materialize(db, 11155111, testOwner, four.Address.Hex())
	require.NoError(t, err)
	require.Equal(t, four.WalletID, materialized.WalletID)
	list, usage, err := Snapshot(db, testOwner, 3)
	require.NoError(t, err)
	require.Len(t, list, 4)
	require.Equal(t, 3, usage.ActiveCount)
	two := testWallet(testOwner, 2)
	_, err = SetHidden(db, testOwner, two.Address.Hex(), true, 3)
	require.NoError(t, err)
	_, err = SetHidden(db, testOwner, zero.Address.Hex(), false, 3)
	require.NoError(t, err)
}
func TestConcurrentCrossNetworkAdmission(t *testing.T) {
	db := testDB(t)
	for salt := int64(0); salt < 2; salt++ {
		_, err := Register(db, 1, testOwner, testWallet(testOwner, salt), 3)
		require.NoError(t, err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := int64(2); i < 22; i++ {
		wg.Add(1)
		go func(salt int64) {
			defer wg.Done()
			_, err := Register(db, salt, testOwner, testWallet(testOwner, salt), 3)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
		} else {
			require.ErrorIs(t, err, ErrLimit)
		}
	}
	require.Equal(t, 1, admitted)
	_, usage, err := Snapshot(db, testOwner, 3)
	require.NoError(t, err)
	require.Equal(t, 3, usage.ActiveCount)
}
func TestDurableAllocationReplayAcrossNetworksAndRestart(t *testing.T) {
	path := t.TempDir()
	db, err := storage.NewWithPath(path)
	require.NoError(t, err)
	derive := func(salt *big.Int) (*model.SmartWallet, error) { return testWallet(testOwner, salt.Int64()), nil }
	original, err := Allocate(context.Background(), db, 1, testOwner, "press-1", Profile, 3, derive)
	require.NoError(t, err)
	_, err = SetHidden(db, testOwner, original.Address.Hex(), true, 3)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	db, err = storage.NewWithPath(path)
	require.NoError(t, err)
	defer db.Close()
	again, err := Allocate(context.Background(), db, 8453, testOwner, "press-1", Profile, 3, func(*big.Int) (*model.SmartWallet, error) { t.Fatal("replay must not derive again"); return nil, nil })
	require.NoError(t, err)
	require.Equal(t, original.WalletID, again.WalletID)
	require.True(t, again.IsHidden)
	next, err := Allocate(context.Background(), db, 10, testOwner, "press-2", Profile, 3, derive)
	require.NoError(t, err)
	require.Equal(t, "1", next.Salt.String())
	// Keys are private to an owner, even when the client key is identical.
	other := common.HexToAddress("0x2222222222222222222222222222222222222222")
	own, err := Allocate(context.Background(), db, 1, other, "press-1", Profile, 3, func(salt *big.Int) (*model.SmartWallet, error) { return testWallet(other, salt.Int64()), nil })
	require.NoError(t, err)
	require.NotEqual(t, own.WalletID, original.WalletID)
}

type failingStore struct {
	storage.Storage
	failWrite bool
	failRead  bool
}

func (s *failingStore) AtomicWrite(writes map[string][]byte, deletes [][]byte) error {
	if s.failWrite {
		return errors.New("commit failed")
	}
	return s.Storage.(interface {
		AtomicWrite(map[string][]byte, [][]byte) error
	}).AtomicWrite(writes, deletes)
}
func (s *failingStore) GetByPrefix(prefix []byte) ([]*storage.KeyValueItem, error) {
	if s.failRead {
		return nil, errors.New("scan failed")
	}
	return s.Storage.GetByPrefix(prefix)
}
func TestAdmissionFailsClosedAndAllocationCommitIsAtomic(t *testing.T) {
	base := testDB(t)
	db := &failingStore{Storage: base, failRead: true}
	_, err := Register(db, 1, testOwner, testWallet(testOwner, 0), 3)
	require.ErrorContains(t, err, "scan failed")
	db.failRead = false
	db.failWrite = true
	_, err = Allocate(context.Background(), db, 1, testOwner, "retry", Profile, 3, func(n *big.Int) (*model.SmartWallet, error) { return testWallet(testOwner, n.Int64()), nil })
	require.ErrorContains(t, err, "commit failed")
	_, err = BySalt(base, testOwner, big.NewInt(0))
	require.ErrorIs(t, err, badger.ErrKeyNotFound)
	db.failWrite = false
	w, err := Allocate(context.Background(), db, 1, testOwner, "retry", Profile, 3, func(n *big.Int) (*model.SmartWallet, error) { return testWallet(testOwner, n.Int64()), nil })
	require.NoError(t, err)
	require.Equal(t, "0", w.Salt.String())
}
func putRuntime(t *testing.T, db storage.Storage, chain int64, w *model.SmartWallet) {
	t.Helper()
	raw, err := w.ToJSON()
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte(fmt.Sprintf("w:%d:%s:%s", chain, ownerString(*w.Owner), stringsLower(w.Address.Hex()))), raw))
}
func stringsLower(s string) string { return fmt.Sprintf("0x%x", common.HexToAddress(s).Bytes()) }
func TestMigrationUnionVisibilityHistoryAndRestart(t *testing.T) {
	db := testDB(t)
	zero := testWallet(testOwner, 0)
	zero.IsHidden = true
	putRuntime(t, db, 1, zero)
	zero2 := testWallet(testOwner, 0)
	putRuntime(t, db, 8453, zero2)
	eight := testWallet(testOwner, 8)
	eight.IsHidden = true
	putRuntime(t, db, 1, eight)
	imported, err := Migrate(db)
	require.NoError(t, err)
	require.Equal(t, 2, imported)
	w, err := BySalt(db, testOwner, big.NewInt(0))
	require.NoError(t, err)
	require.False(t, w.IsHidden)
	_, err = SetHidden(db, testOwner, w.Address.Hex(), true, 3)
	require.NoError(t, err)
	imported, err = Migrate(db)
	require.NoError(t, err)
	require.Zero(t, imported)
	w, err = BySalt(db, testOwner, big.NewInt(0))
	require.NoError(t, err)
	require.True(t, w.IsHidden)
	next, err := Allocate(context.Background(), db, 10, testOwner, "new", Profile, 3, func(n *big.Int) (*model.SmartWallet, error) { return testWallet(testOwner, n.Int64()), nil })
	require.NoError(t, err)
	require.Equal(t, "9", next.Salt.String())
}
func TestMigrationConflictingAddressesBlockActivation(t *testing.T) {
	db := testDB(t)
	first := testWallet(testOwner, 0)
	putRuntime(t, db, 1, first)
	second := testWallet(testOwner, 0)
	different := common.HexToAddress("0x3333333333333333333333333333333333333333")
	second.Address = &different
	putRuntime(t, db, 8453, second)
	_, err := Migrate(db)
	require.NoError(t, err)
	_, err = Register(db, 1, testOwner, testWallet(testOwner, 1), 3)
	require.ErrorIs(t, err, ErrConflict)
	// Known addresses remain recoverable rather than being replaced.
	_, err = Get(db, testOwner, first.Address.Hex())
	require.NoError(t, err)
	_, err = Get(db, testOwner, second.Address.Hex())
	require.NoError(t, err)
	rows, _, err := Snapshot(db, testOwner, 3)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NotEqual(t, rows[0].WalletID, rows[1].WalletID,
		"mirrors must preserve both recovery addresses rather than upserting the same ID")
}

func TestMigrationPreservesAnIdentityCreatedAfterTheRuntimeScan(t *testing.T) {
	db := testDB(t)
	legacy := testWallet(testOwner, 0)
	current := testWallet(testOwner, 0)
	address := common.HexToAddress("0x3333333333333333333333333333333333333333")
	current.Address = &address
	_, err := Register(db, 8453, testOwner, current, 3)
	require.NoError(t, err)
	_, err = SetHidden(db, testOwner, address.Hex(), true, 3)
	require.NoError(t, err)
	// The scan happened before Register, so only the old address is in it.
	legacyScan := map[string]*model.SmartWallet{
		strings.ToLower(legacy.Address.Hex()): legacy,
	}
	failing := &failingStore{Storage: db, failWrite: true}
	_, err = migrateOwner(failing, testOwner, legacyScan, legacy.Salt)
	require.ErrorContains(t, err, "commit failed")
	original, err := BySalt(db, testOwner, big.NewInt(0))
	require.NoError(t, err)
	require.Equal(t, address, *original.Address, "a failed conflict import must not delete the previous index")
	_, err = migrateOwner(db, testOwner, legacyScan, legacy.Salt)
	require.NoError(t, err)
	rows, usage, err := Snapshot(db, testOwner, 3)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, 1, usage.ActiveCount)
	stored, err := Get(db, testOwner, address.Hex())
	require.NoError(t, err)
	require.True(t, stored.IsHidden)
	_, err = BySalt(db, testOwner, big.NewInt(0))
	require.ErrorIs(t, err, badger.ErrKeyNotFound, "a conflicting canonical index must be removed atomically")
	_, err = Register(db, 1, testOwner, testWallet(testOwner, 1), 3)
	require.ErrorIs(t, err, ErrConflict)
	_, err = SetHidden(db, testOwner, address.Hex(), false, 3)
	require.ErrorIs(t, err, ErrConflict)
}

func TestCompatibilityWriterCannotAdmitOrRestoreIdentities(t *testing.T) {
	db := testDB(t)
	zero := testWallet(testOwner, 0)
	require.ErrorIs(t, StoreCompatibility(db, 1, testOwner, zero), ErrUnregistered)
	_, err := Register(db, 1, testOwner, zero, 0)
	require.ErrorContains(t, err, "limit must be positive")
	_, err = Register(db, 1, testOwner, zero, 1)
	require.NoError(t, err)
	_, err = SetHidden(db, testOwner, zero.Address.Hex(), true, 1)
	require.NoError(t, err)
	require.NoError(t, StoreCompatibility(db, 8453, testOwner, zero))
	stored, err := Get(db, testOwner, zero.Address.Hex())
	require.NoError(t, err)
	require.True(t, stored.IsHidden)
	require.ErrorIs(t, StoreCompatibility(db, 1, testOwner, testWallet(testOwner, 4)), ErrUnregistered)
}

func TestAllocationRejectsHeaderNormalizationAndEncodingAmbiguity(t *testing.T) {
	db := testDB(t)
	for _, key := range []string{"", " key", "key ", "two keys", "添加-1", "key\nnext", strings.Repeat("x", 257)} {
		_, err := Allocate(context.Background(), db, 1, testOwner, key, Profile, 3, func(*big.Int) (*model.SmartWallet, error) {
			t.Fatal("invalid header keys must fail before derivation")
			return nil, nil
		})
		require.ErrorIs(t, err, ErrBadAllocation)
	}
	require.True(t, ValidAllocationKey(strings.Repeat("x", 256)))
	require.True(t, ValidAllocationKey("wallet-4_retry:1"))
}

func TestRetainedHistoryAndSaltExhaustion(t *testing.T) {
	t.Run("stale history is never reused", func(t *testing.T) {
		db := testDB(t)
		old := testWallet(testOwner, 8)
		old.StaleDerivation = true
		old.IsHidden = true
		putRuntime(t, db, 1, old)
		_, err := Migrate(db)
		require.NoError(t, err)
		rows, usage, err := Snapshot(db, testOwner, 3)
		require.NoError(t, err)
		require.Empty(t, rows)
		require.True(t, usage.HasHistory)
		next, err := Allocate(context.Background(), db, 8453, testOwner, "next", Profile, 3, func(salt *big.Int) (*model.SmartWallet, error) { return testWallet(testOwner, salt.Int64()), nil })
		require.NoError(t, err)
		require.Equal(t, "9", next.Salt.String())
	})
	t.Run("uint256 boundary", func(t *testing.T) {
		db := testDB(t)
		last := testWallet(testOwner, 0)
		last.Salt = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
		_, err := Register(db, 1, testOwner, last, 3)
		require.NoError(t, err)
		_, err = Allocate(context.Background(), db, 1, testOwner, "exhausted", Profile, 3, func(*big.Int) (*model.SmartWallet, error) {
			t.Fatal("must not derive an overflowing salt")
			return nil, nil
		})
		require.ErrorIs(t, err, ErrSaltExhausted)
	})
}
func TestAllocationKeyConflictAndQuotaReplay(t *testing.T) {
	db := testDB(t)
	derive := func(salt *big.Int) (*model.SmartWallet, error) { return testWallet(testOwner, salt.Int64()), nil }
	first, err := Allocate(context.Background(), db, 1, testOwner, "same-key", Profile, 1, derive)
	require.NoError(t, err)
	replayed, err := Allocate(context.Background(), db, 8453, testOwner, "same-key", Profile, 1, derive)
	require.NoError(t, err)
	require.Equal(t, first.WalletID, replayed.WalletID)
	_, err = Allocate(context.Background(), db, 8453, testOwner, "same-key", "different-profile", 1, derive)
	require.ErrorIs(t, err, ErrKeyReused)
	_, err = Allocate(context.Background(), db, 8453, testOwner, "new-key", "different-profile", 1, derive)
	require.ErrorIs(t, err, ErrBadAllocation)
}
func TestConcurrentCreateAndRestoreShareOneSlot(t *testing.T) {
	db := testDB(t)
	for salt := int64(0); salt < 3; salt++ {
		_, err := Register(db, 1, testOwner, testWallet(testOwner, salt), 3)
		require.NoError(t, err)
	}
	zero := testWallet(testOwner, 0)
	_, err := SetHidden(db, testOwner, zero.Address.Hex(), true, 3)
	require.NoError(t, err)
	results := make(chan error, 2)
	start := make(chan struct{})
	go func() { <-start; _, err := SetHidden(db, testOwner, zero.Address.Hex(), false, 3); results <- err }()
	go func() { <-start; _, err := Register(db, 8453, testOwner, testWallet(testOwner, 4), 3); results <- err }()
	close(start)
	success := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, ErrLimit)
		}
	}
	require.Equal(t, 1, success)
	_, usage, err := Snapshot(db, testOwner, 3)
	require.NoError(t, err)
	require.Equal(t, 3, usage.ActiveCount)
}
