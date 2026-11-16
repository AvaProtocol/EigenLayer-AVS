package walletinventory

import (
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestMigrationSkipsUnscopedLegacyWallets(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		t.Run(fmt.Sprintf("unreadable=%t", unreadable), func(t *testing.T) {
			db := testDB(t)
			legacy := testWallet(testOwner, 99)
			key := []byte(fmt.Sprintf("w:%s:%s", ownerString(testOwner), stringsLower(legacy.Address.Hex())))
			raw, err := legacy.ToJSON()
			require.NoError(t, err)
			if unreadable {
				raw = []byte("unreadable legacy value")
			}
			require.NoError(t, db.Set(key, raw))
			current := testWallet(testOwner, 2)
			putRuntime(t, db, 1, current)
			putRuntime(t, db, 8453, current)

			count, err := Migrate(db)
			require.NoError(t, err)
			require.Equal(t, 1, count)
			list, usage, err := Snapshot(db, testOwner, 3)
			require.NoError(t, err)
			require.Len(t, list, 1)
			require.Equal(t, current.Address, list[0].Address)
			require.Equal(t, 1, usage.ActiveCount)
			meta, err := readMeta(db, testOwner)
			require.NoError(t, err)
			require.True(t, meta.Migrated)
			require.Equal(t, "3", meta.NextSalt, "obsolete rows must not reserve current salts")
			preserved, err := db.GetKey(key)
			require.NoError(t, err)
			require.Equal(t, raw, preserved, "obsolete rows must remain available for recovery")

			before := migrationSnapshot(t, db)
			count, err = Migrate(db)
			require.NoError(t, err)
			require.Zero(t, count)
			require.Equal(t, before, migrationSnapshot(t, db))
		})
	}
}

func TestMigrationRejectsInvalidRuntimeRecordsBeforeWriting(t *testing.T) {
	for _, scenario := range []string{"decode", "owner mismatch", "address mismatch", "conflicting salts", "invalid scoped key", "unknown key format"} {
		t.Run(scenario, func(t *testing.T) {
			db := testDB(t)
			// A valid, earlier-sorting owner must not be partially migrated when
			// validation later encounters a corrupt runtime row.
			putRuntime(t, db, 1, testWallet(testOwner, 0))
			other := common.HexToAddress("0x2222222222222222222222222222222222222222")
			wallet := testWallet(other, 1)
			key := []byte(fmt.Sprintf("w:8453:%s:%s", ownerString(other), stringsLower(wallet.Address.Hex())))
			expected := ""
			switch scenario {
			case "decode":
				require.NoError(t, db.Set(key, []byte("invalid JSON")))
				expected = "decode "
			case "owner mismatch":
				wallet.Owner = &testOwner
				expected = "wallet key/owner mismatch"
			case "address mismatch":
				wallet.Address = testWallet(other, 2).Address
				expected = "wallet key/owner mismatch"
			case "conflicting salts":
				previous := testWallet(other, 0)
				previous.Address = wallet.Address
				putRuntime(t, db, 1, previous)
				expected = "wallet address has conflicting salts"
			case "invalid scoped key":
				key = []byte(fmt.Sprintf("w:8453:invalid-owner:%s", stringsLower(wallet.Address.Hex())))
				expected = "invalid wallet runtime key"
			case "unknown key format":
				key = []byte("w:unknown:format")
				expected = "invalid wallet runtime key"
			}
			if scenario != "decode" {
				raw, err := wallet.ToJSON()
				require.NoError(t, err)
				require.NoError(t, db.Set(key, raw))
			}
			before := migrationSnapshot(t, db)
			count, err := Migrate(db)
			require.ErrorContains(t, err, expected)
			require.Zero(t, count)
			require.Equal(t, before, migrationSnapshot(t, db), "validation must finish before any inventory or marker write")
		})
	}
}

func migrationSnapshot(t *testing.T, db storage.Storage) map[string]string {
	t.Helper()
	rows, err := db.GetByPrefix(nil)
	require.NoError(t, err)
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		result[string(row.Key)] = string(row.Value)
	}
	return result
}
