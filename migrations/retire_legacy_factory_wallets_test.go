package migrations

import (
	"bytes"
	"log"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

var (
	retireOwner         = common.HexToAddress("0x00000000000000000000000000000000000000A1")
	simpleFactory       = common.HexToAddress("0xB99BC2E399e06CddCF5E725c0ea341E8f0322834")
	unknownFactory      = common.HexToAddress("0x00000000000000000000000000000000000000F1")
	maV2Wallet          = common.HexToAddress("0x0000000000000000000000000000000000000B01")
	simpleWallet        = common.HexToAddress("0x0000000000000000000000000000000000000B02")
	unknownWallet       = common.HexToAddress("0x0000000000000000000000000000000000000B03")
	noFactoryWallet     = common.HexToAddress("0x0000000000000000000000000000000000000B04")
	simpleWalletOtherCh = common.HexToAddress("0x0000000000000000000000000000000000000B05")
)

func storeTestWallet(t *testing.T, db storage.Storage, chainID int64, w *model.SmartWallet) {
	t.Helper()
	require.NoError(t, taskengine.StoreWallet(db, chainID, retireOwner, w))
}

func loadTestWallet(t *testing.T, db storage.Storage, chainID int64, address common.Address) *model.SmartWallet {
	t.Helper()
	w, err := taskengine.GetWallet(db, chainID, retireOwner, address.Hex())
	require.NoError(t, err)
	return w
}

func walletRecord(address common.Address, factory *common.Address, salt int64) *model.SmartWallet {
	owner := retireOwner
	return &model.SmartWallet{Owner: &owner, Address: &address, Factory: factory, Salt: big.NewInt(salt)}
}

func TestIsLegacyFactoryWallet(t *testing.T) {
	maV2 := aa.MAv2FactoryAddress()
	owner := retireOwner
	assert.False(t, IsLegacyFactoryWallet(walletRecord(maV2Wallet, &maV2, 0)))
	assert.True(t, IsLegacyFactoryWallet(walletRecord(simpleWallet, &simpleFactory, 0)))
	assert.True(t, IsLegacyFactoryWallet(walletRecord(unknownWallet, &unknownFactory, 3)))
	assert.True(t, IsLegacyFactoryWallet(walletRecord(noFactoryWallet, nil, 1)))
	eoa := &model.SmartWallet{Owner: &owner, Address: &owner, Kind: model.WalletKindEOA7702}
	assert.False(t, IsLegacyFactoryWallet(eoa))
	assert.False(t, IsLegacyFactoryWallet(nil))
	assert.False(t, IsLegacyFactoryWallet(&model.SmartWallet{}))
}

func TestRetireLegacyFactoryWallets(t *testing.T) {
	db, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	defer storage.Destroy(db.(*storage.BadgerStorage))

	maV2 := aa.MAv2FactoryAddress()
	owner := retireOwner
	storeTestWallet(t, db, 8453, walletRecord(maV2Wallet, &maV2, 0))
	legacy := walletRecord(simpleWallet, &simpleFactory, 0)
	legacy.IsHidden = false
	storeTestWallet(t, db, 8453, legacy)
	storeTestWallet(t, db, 8453, walletRecord(unknownWallet, &unknownFactory, 5))
	storeTestWallet(t, db, 8453, walletRecord(noFactoryWallet, nil, 1))
	storeTestWallet(t, db, 11155111, walletRecord(simpleWalletOtherCh, &simpleFactory, 0))
	eoa := &model.SmartWallet{Owner: &owner, Address: &owner, Kind: model.WalletKindEOA7702, Delegate: &maV2}
	require.NoError(t, db.Set([]byte(taskengine.WalletStorageKey(8453, owner, owner.Hex())), mustJSON(t, eoa)))

	// Two workflows on the SimpleAccount wallet, one on the MA v2 wallet.
	for _, k := range []string{
		string(taskengine.SmartWalletTaskStoragePrefix(owner, simpleWallet)) + ":01wf1",
		string(taskengine.SmartWalletTaskStoragePrefix(owner, simpleWallet)) + ":01wf2",
		string(taskengine.SmartWalletTaskStoragePrefix(owner, maV2Wallet)) + ":01wf3",
	} {
		require.NoError(t, db.Set([]byte(k), []byte("a")))
	}

	saltKey := taskengine.WalletBySaltKey(8453, owner, simpleFactory, big.NewInt(0))
	saltBefore, err := db.GetKey(saltKey)
	require.NoError(t, err)

	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	n, err := RetireLegacyFactoryWallets(db)
	require.NoError(t, err)
	assert.Equal(t, 4, n)

	for _, tc := range []struct {
		chainID int64
		address common.Address
		retired bool
	}{
		{8453, maV2Wallet, false},
		{8453, simpleWallet, true},
		{8453, unknownWallet, true},
		{8453, noFactoryWallet, true},
		{11155111, simpleWalletOtherCh, true},
		{8453, owner, false},
	} {
		w := loadTestWallet(t, db, tc.chainID, tc.address)
		assert.Equal(t, tc.retired, w.Retired, "retired %s on %d", tc.address.Hex(), tc.chainID)
		if tc.retired {
			assert.True(t, w.IsHidden, "a retired wallet is hidden too")
		}
	}

	// Everything else on the record survives.
	kept := loadTestWallet(t, db, 8453, unknownWallet)
	assert.Equal(t, unknownFactory, *kept.Factory)
	assert.Equal(t, "5", kept.Salt.String())
	assert.Equal(t, owner, *kept.Owner)

	// The salt index is not rewritten.
	saltAfter, err := db.GetKey(saltKey)
	require.NoError(t, err)
	assert.Equal(t, saltBefore, saltAfter)

	out := logs.String()
	assert.Equal(t, 4, strings.Count(out, RetireLegacyWalletsLogPrefix+" retired "))
	assert.Contains(t, out, RetireLegacyWalletsLogPrefix+" done retired=4 skipped=0")
	assert.Contains(t, out, "wallet="+strings.ToLower(simpleWallet.Hex())+" factory="+strings.ToLower(simpleFactory.Hex())+" salt=0 workflows=2 was_hidden=false")
	assert.Contains(t, out, "wallet="+strings.ToLower(noFactoryWallet.Hex())+" factory=none salt=1 workflows=0")
	assert.Contains(t, out, "chain=11155111 owner="+strings.ToLower(owner.Hex()))

	// Idempotent: nothing left to retire.
	n, err = RetireLegacyFactoryWallets(db)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Contains(t, logs.String(), RetireLegacyWalletsLogPrefix+" done retired=0 skipped=0")
}

func TestRetireLegacyFactoryWalletsKeyFormats(t *testing.T) {
	owner := retireOwner
	chainID, parsedOwner, ok := parseWalletKey(taskengine.WalletStorageKey(8453, owner, simpleWallet.Hex()))
	require.True(t, ok)
	assert.Equal(t, "8453", chainID)
	assert.Equal(t, strings.ToLower(owner.Hex()), parsedOwner)
	assert.True(t, strings.HasPrefix(string(taskengine.WalletByOwnerPrefix(1, owner)), walletKeyPrefix))
	assert.False(t, strings.HasPrefix(string(taskengine.WalletBySaltKey(1, owner, simpleFactory, big.NewInt(0))), walletKeyPrefix))

	_, _, ok = parseWalletKey("wsalt:1:0xowner:0xfactory:0")
	assert.False(t, ok)
	_, _, ok = parseWalletKey("w:1:0xowner")
	assert.False(t, ok)
}

func mustJSON(t *testing.T, w *model.SmartWallet) []byte {
	t.Helper()
	data, err := w.ToJSON()
	require.NoError(t, err)
	return data
}
