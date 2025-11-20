package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListWalletsStoresDefaultWallet tests that ListWallets stores the default salt:0 wallet
// in the database when it doesn't exist yet.
func TestListWalletsStoresDefaultWallet(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// Verify database is empty initially
	dbItems, err := db.GetByPrefix(WalletByOwnerPrefix(user.Address))
	require.NoError(t, err)
	assert.Equal(t, 0, len(dbItems), "Database should be empty initially")

	// Call ListWallets - this should create and store the default salt:0 wallet
	listResp, err := engine.ListWallets(user.Address, &avsproto.ListWalletReq{})
	require.NoError(t, err, "ListWallets should succeed")
	require.Greater(t, len(listResp.Items), 0, "ListWallets should return at least the default wallet")

	// Find the default wallet in the response
	var defaultWallet *avsproto.SmartWallet
	for _, w := range listResp.Items {
		if w.Salt == "0" && w.Factory == config.SmartWallet.FactoryAddress.Hex() {
			defaultWallet = w
			break
		}
	}
	require.NotNil(t, defaultWallet, "Default wallet (salt:0) should be in the response")

	// Verify the wallet is now stored in the database
	dbItems, err = db.GetByPrefix(WalletByOwnerPrefix(user.Address))
	require.NoError(t, err)
	assert.Equal(t, 1, len(dbItems), "Database should contain exactly 1 wallet after ListWallets")

	// Verify the stored wallet matches what was returned
	storedWallet := &model.SmartWallet{}
	err = storedWallet.FromStorageData(dbItems[0].Value)
	require.NoError(t, err)
	assert.Equal(t, defaultWallet.Address, storedWallet.Address.Hex(), "Stored wallet address should match")
	assert.Equal(t, "0", storedWallet.Salt.String(), "Stored wallet salt should be 0")
	assert.Equal(t, config.SmartWallet.FactoryAddress.Hex(), storedWallet.Factory.Hex(), "Stored wallet factory should match")
	assert.False(t, storedWallet.IsHidden, "Default wallet should not be hidden")
}

// TestListWalletsDoesNotDuplicateExistingWallet tests that ListWallets doesn't create
// duplicate entries for an existing wallet.
func TestListWalletsDoesNotDuplicateExistingWallet(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// First, get the default wallet to ensure it exists in the database
	getResp, err := engine.GetWallet(user, &avsproto.GetWalletReq{
		Salt: "0",
	})
	require.NoError(t, err, "GetWallet should succeed")
	defaultAddress := getResp.Address

	// Verify the wallet is stored
	dbItems, err := db.GetByPrefix(WalletByOwnerPrefix(user.Address))
	require.NoError(t, err)
	assert.Equal(t, 1, len(dbItems), "Database should contain exactly 1 wallet after GetWallet")

	// Call ListWallets - this should NOT create a duplicate
	listResp, err := engine.ListWallets(user.Address, &avsproto.ListWalletReq{})
	require.NoError(t, err, "ListWallets should succeed")

	// Verify the wallet is still only stored once
	dbItems, err = db.GetByPrefix(WalletByOwnerPrefix(user.Address))
	require.NoError(t, err)
	assert.Equal(t, 1, len(dbItems), "Database should still contain exactly 1 wallet after ListWallets")

	// Verify the stored wallet address matches
	storedWallet := &model.SmartWallet{}
	err = storedWallet.FromStorageData(dbItems[0].Value)
	require.NoError(t, err)
	assert.Equal(t, defaultAddress, storedWallet.Address.Hex(), "Stored wallet address should match the original")

	// Verify the wallet appears in the list response
	found := false
	for _, w := range listResp.Items {
		if w.Address == defaultAddress {
			found = true
			break
		}
	}
	assert.True(t, found, "Default wallet should appear in ListWallets response")
}

// TestListWalletsWithMultipleWallets tests that ListWallets properly handles the case
// where the owner has multiple wallets (salt:0 from ListWallets, salt:1 and salt:2 from GetWallet).
func TestListWalletsWithMultipleWallets(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// Step 1: Call ListWallets first - this should create salt:0
	listResp1, err := engine.ListWallets(user.Address, &avsproto.ListWalletReq{})
	require.NoError(t, err, "First ListWallets call should succeed")

	// Verify salt:0 is stored
	dbItems, err := db.GetByPrefix(WalletByOwnerPrefix(user.Address))
	require.NoError(t, err)
	assert.Equal(t, 1, len(dbItems), "Should have 1 wallet (salt:0) after first ListWallets")

	var salt0Address string
	for _, w := range listResp1.Items {
		if w.Salt == "0" {
			salt0Address = w.Address
			break
		}
	}
	require.NotEmpty(t, salt0Address, "Salt:0 wallet should be in the response")

	// Step 2: Call GetWallet for salt:1
	getResp1, err := engine.GetWallet(user, &avsproto.GetWalletReq{
		Salt: "1",
	})
	require.NoError(t, err, "GetWallet for salt:1 should succeed")
	salt1Address := getResp1.Address
	require.NotEqual(t, salt0Address, salt1Address, "Salt:1 address should be different from salt:0")

	// Verify we now have 2 wallets
	dbItems, err = db.GetByPrefix(WalletByOwnerPrefix(user.Address))
	require.NoError(t, err)
	assert.Equal(t, 2, len(dbItems), "Should have 2 wallets (salt:0, salt:1) after GetWallet")

	// Step 3: Call GetWallet for salt:2
	getResp2, err := engine.GetWallet(user, &avsproto.GetWalletReq{
		Salt: "2",
	})
	require.NoError(t, err, "GetWallet for salt:2 should succeed")
	salt2Address := getResp2.Address
	require.NotEqual(t, salt0Address, salt2Address, "Salt:2 address should be different from salt:0")
	require.NotEqual(t, salt1Address, salt2Address, "Salt:2 address should be different from salt:1")

	// Verify we now have 3 wallets
	dbItems, err = db.GetByPrefix(WalletByOwnerPrefix(user.Address))
	require.NoError(t, err)
	assert.Equal(t, 3, len(dbItems), "Should have 3 wallets (salt:0, salt:1, salt:2) after second GetWallet")

	// Step 4: Call ListWallets again - should return all 3 wallets and not create duplicates
	listResp2, err := engine.ListWallets(user.Address, &avsproto.ListWalletReq{})
	require.NoError(t, err, "Second ListWallets call should succeed")

	// Verify we still have exactly 3 wallets (no duplicates)
	dbItems, err = db.GetByPrefix(WalletByOwnerPrefix(user.Address))
	require.NoError(t, err)
	assert.Equal(t, 3, len(dbItems), "Should still have exactly 3 wallets after second ListWallets")

	// Verify all 3 wallets are in the response
	addressesInResponse := make(map[string]bool)
	for _, w := range listResp2.Items {
		addressesInResponse[w.Address] = true
	}
	assert.True(t, addressesInResponse[salt0Address], "Salt:0 wallet should be in ListWallets response")
	assert.True(t, addressesInResponse[salt1Address], "Salt:1 wallet should be in ListWallets response")
	assert.True(t, addressesInResponse[salt2Address], "Salt:2 wallet should be in ListWallets response")

	// Verify the stored wallets match what was returned
	storedAddresses := make(map[string]*model.SmartWallet)
	for _, item := range dbItems {
		wallet := &model.SmartWallet{}
		err := wallet.FromStorageData(item.Value)
		require.NoError(t, err)
		storedAddresses[wallet.Address.Hex()] = wallet
	}

	// Verify salt values in stored wallets
	assert.Equal(t, "0", storedAddresses[salt0Address].Salt.String(), "Stored salt:0 wallet should have salt 0")
	assert.Equal(t, "1", storedAddresses[salt1Address].Salt.String(), "Stored salt:1 wallet should have salt 1")
	assert.Equal(t, "2", storedAddresses[salt2Address].Salt.String(), "Stored salt:2 wallet should have salt 2")

	// Verify all wallets belong to the same owner
	for address, wallet := range storedAddresses {
		assert.Equal(t, user.Address, *wallet.Owner, "Wallet %s should belong to test user", address)
		assert.Equal(t, config.SmartWallet.FactoryAddress.Hex(), wallet.Factory.Hex(), "Wallet %s should use default factory", address)
	}
}

// TestListWalletsBeforeAndAfterGetWallet tests the scenario where ListWallets is called
// before GetWallet, ensuring the default wallet is stored, and then GetWallet stores
// additional wallets correctly.
func TestListWalletsBeforeAndAfterGetWallet(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// Phase 1: Owner with no wallets yet - call ListWallets
	// This should create and store salt:0
	dbItems, err := db.GetByPrefix(WalletByOwnerPrefix(user.Address))
	require.NoError(t, err)
	assert.Equal(t, 0, len(dbItems), "Database should be empty initially")

	listResp1, err := engine.ListWallets(user.Address, &avsproto.ListWalletReq{})
	require.NoError(t, err, "ListWallets should succeed when owner has no wallets")

	// Verify salt:0 is now stored
	dbItems, err = db.GetByPrefix(WalletByOwnerPrefix(user.Address))
	require.NoError(t, err)
	assert.Equal(t, 1, len(dbItems), "Database should contain salt:0 after ListWallets")

	var salt0FromList string
	for _, w := range listResp1.Items {
		if w.Salt == "0" {
			salt0FromList = w.Address
			break
		}
	}
	require.NotEmpty(t, salt0FromList, "Salt:0 wallet should be in ListWallets response")

	// Verify we can retrieve it directly from the database
	retrievedWallet, err := GetWallet(db, user.Address, salt0FromList)
	require.NoError(t, err, "Should be able to retrieve salt:0 wallet from database")
	assert.Equal(t, "0", retrievedWallet.Salt.String(), "Retrieved wallet should have salt 0")

	// Phase 2: Call GetWallet for salt:1
	getResp1, err := engine.GetWallet(user, &avsproto.GetWalletReq{
		Salt: "1",
	})
	require.NoError(t, err, "GetWallet for salt:1 should succeed")

	// Verify we now have 2 wallets
	dbItems, err = db.GetByPrefix(WalletByOwnerPrefix(user.Address))
	require.NoError(t, err)
	assert.Equal(t, 2, len(dbItems), "Should have 2 wallets after GetWallet for salt:1")

	// Phase 3: Call GetWallet for salt:2
	getResp2, err := engine.GetWallet(user, &avsproto.GetWalletReq{
		Salt: "2",
	})
	require.NoError(t, err, "GetWallet for salt:2 should succeed")

	// Verify we now have 3 wallets
	dbItems, err = db.GetByPrefix(WalletByOwnerPrefix(user.Address))
	require.NoError(t, err)
	assert.Equal(t, 3, len(dbItems), "Should have 3 wallets after GetWallet for salt:2")

	// Phase 4: Call ListWallets again - should return all 3 wallets
	listResp2, err := engine.ListWallets(user.Address, &avsproto.ListWalletReq{})
	require.NoError(t, err, "Second ListWallets call should succeed")

	// Verify response contains all 3 wallets
	addressesInResponse := make(map[string]bool)
	for _, w := range listResp2.Items {
		addressesInResponse[w.Address] = true
	}
	assert.True(t, addressesInResponse[salt0FromList], "Salt:0 wallet should be in response")
	assert.True(t, addressesInResponse[getResp1.Address], "Salt:1 wallet should be in response")
	assert.True(t, addressesInResponse[getResp2.Address], "Salt:2 wallet should be in response")

	// Verify database still has exactly 3 wallets (no duplicates created)
	dbItems, err = db.GetByPrefix(WalletByOwnerPrefix(user.Address))
	require.NoError(t, err)
	assert.Equal(t, 3, len(dbItems), "Should still have exactly 3 wallets after second ListWallets")

	// Verify all wallets can be retrieved individually
	_, err = GetWallet(db, user.Address, salt0FromList)
	require.NoError(t, err, "Should be able to retrieve salt:0 from database")

	_, err = GetWallet(db, user.Address, getResp1.Address)
	require.NoError(t, err, "Should be able to retrieve salt:1 from database")

	_, err = GetWallet(db, user.Address, getResp2.Address)
	require.NoError(t, err, "Should be able to retrieve salt:2 from database")
}

// TestListWalletsDatabaseStateVerification provides a comprehensive test that verifies
// the exact database state at each step of wallet creation.
func TestListWalletsDatabaseStateVerification(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// Helper function to verify database state
	verifyDatabaseState := func(t *testing.T, expectedCount int, description string) map[string]*model.SmartWallet {
		dbItems, err := db.GetByPrefix(WalletByOwnerPrefix(user.Address))
		require.NoError(t, err, "Failed to query database: %s", description)
		assert.Equal(t, expectedCount, len(dbItems), "Database state check failed: %s (expected %d wallets, got %d)", description, expectedCount, len(dbItems))

		storedWallets := make(map[string]*model.SmartWallet)
		for _, item := range dbItems {
			wallet := &model.SmartWallet{}
			err := wallet.FromStorageData(item.Value)
			require.NoError(t, err)
			storedWallets[wallet.Address.Hex()] = wallet
		}
		return storedWallets
	}

	// Initial state: No wallets
	_ = verifyDatabaseState(t, 0, "Initial state - no wallets")

	// Step 1: ListWallets should create salt:0
	_, err := engine.ListWallets(user.Address, &avsproto.ListWalletReq{})
	require.NoError(t, err)
	wallets1 := verifyDatabaseState(t, 1, "After ListWallets - should have salt:0")

	// Verify salt:0 properties
	var salt0Address string
	for addr, wallet := range wallets1 {
		salt0Address = addr
		assert.Equal(t, "0", wallet.Salt.String(), "First wallet should have salt 0")
		assert.Equal(t, config.SmartWallet.FactoryAddress.Hex(), wallet.Factory.Hex(), "Wallet should use default factory")
		assert.Equal(t, user.Address, *wallet.Owner, "Wallet should belong to test user")
		assert.False(t, wallet.IsHidden, "Wallet should not be hidden")
		break
	}

	// Step 2: GetWallet for salt:1 should add another wallet
	getResp1, err := engine.GetWallet(user, &avsproto.GetWalletReq{Salt: "1"})
	require.NoError(t, err)
	wallets2 := verifyDatabaseState(t, 2, "After GetWallet salt:1 - should have 2 wallets")
	assert.Contains(t, wallets2, salt0Address, "Salt:0 wallet should still exist")
	assert.Contains(t, wallets2, getResp1.Address, "Salt:1 wallet should exist")
	assert.Equal(t, "1", wallets2[getResp1.Address].Salt.String(), "Second wallet should have salt 1")

	// Step 3: GetWallet for salt:2 should add another wallet
	getResp2, err := engine.GetWallet(user, &avsproto.GetWalletReq{Salt: "2"})
	require.NoError(t, err)
	wallets3 := verifyDatabaseState(t, 3, "After GetWallet salt:2 - should have 3 wallets")
	assert.Contains(t, wallets3, salt0Address, "Salt:0 wallet should still exist")
	assert.Contains(t, wallets3, getResp1.Address, "Salt:1 wallet should still exist")
	assert.Contains(t, wallets3, getResp2.Address, "Salt:2 wallet should exist")
	assert.Equal(t, "2", wallets3[getResp2.Address].Salt.String(), "Third wallet should have salt 2")

	// Step 4: ListWallets again should not create duplicates
	listResp2, err := engine.ListWallets(user.Address, &avsproto.ListWalletReq{})
	require.NoError(t, err)
	wallets4 := verifyDatabaseState(t, 3, "After second ListWallets - should still have 3 wallets (no duplicates)")

	// Verify all wallets are still present and unchanged
	assert.Contains(t, wallets4, salt0Address, "Salt:0 wallet should still exist after second ListWallets")
	assert.Contains(t, wallets4, getResp1.Address, "Salt:1 wallet should still exist after second ListWallets")
	assert.Contains(t, wallets4, getResp2.Address, "Salt:2 wallet should still exist after second ListWallets")

	// Verify response contains all wallets
	responseAddresses := make(map[string]bool)
	for _, w := range listResp2.Items {
		responseAddresses[w.Address] = true
	}
	assert.True(t, responseAddresses[salt0Address], "Salt:0 should be in ListWallets response")
	assert.True(t, responseAddresses[getResp1.Address], "Salt:1 should be in ListWallets response")
	assert.True(t, responseAddresses[getResp2.Address], "Salt:2 should be in ListWallets response")

	t.Logf("✅ Successfully verified: All wallets stored correctly and retrievable")
	t.Logf("   - Salt:0 wallet: %s", salt0Address)
	t.Logf("   - Salt:1 wallet: %s", getResp1.Address)
	t.Logf("   - Salt:2 wallet: %s", getResp2.Address)
}
