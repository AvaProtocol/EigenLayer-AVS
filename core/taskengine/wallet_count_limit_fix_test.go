package taskengine

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWalletCountLimitDoesNotBlockExistingWalletAccess reproduces and validates the fix for
// the issue where users couldn't access existing wallets once they hit the wallet count limit.
//
// The original bug: withdrawFunds would fail with "max smart wallet count reached for owner (limit=3)"
// because the limit check was applied before any wallet operations, even accessing existing wallets.
//
// The fix: The limit check now only applies when creating NEW wallet database entries,
// allowing users to access existing wallets regardless of the current count.
func TestWalletCountLimitDoesNotBlockExistingWalletAccess(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	// Set a low limit to easily test the boundary condition
	config.SmartWallet.MaxWalletsPerOwner = 2
	engine := New(db, config, nil, testutil.GetLogger())

	user := testutil.TestUser1()
	factoryAddr := config.SmartWallet.FactoryAddress

	// Step 1: Create wallets up to the limit (2 wallets)
	addr1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	wallet1 := &model.SmartWallet{
		Owner:   &user.Address,
		Address: &addr1,
		Factory: &factoryAddr,
		Salt:    big.NewInt(0),
	}
	err := StoreWallet(db, user.Address, wallet1)
	require.NoError(t, err)

	addr2 := common.HexToAddress("0x2222222222222222222222222222222222222222")
	wallet2 := &model.SmartWallet{
		Owner:   &user.Address,
		Address: &addr2,
		Factory: &factoryAddr,
		Salt:    big.NewInt(1),
	}
	err = StoreWallet(db, user.Address, wallet2)
	require.NoError(t, err)

	t.Run("Direct database access to existing wallets should work at limit", func(t *testing.T) {
		// This is what validateSmartWalletOwnership() does in withdrawFunds
		retrievedWallet1, err := GetWallet(db, user.Address, wallet1.Address.Hex())
		require.NoError(t, err)
		assert.Equal(t, wallet1.Address.Hex(), retrievedWallet1.Address.Hex())

		retrievedWallet2, err := GetWallet(db, user.Address, wallet2.Address.Hex())
		require.NoError(t, err)
		assert.Equal(t, wallet2.Address.Hex(), retrievedWallet2.Address.Hex())
	})

	t.Run("Engine.GetWalletFromDB should work for existing wallets at limit", func(t *testing.T) {
		// This is the exact method called by validateSmartWalletOwnership() in WithdrawFunds
		retrievedWallet1, err := engine.GetWalletFromDB(user.Address, wallet1.Address.Hex())
		require.NoError(t, err)
		assert.Equal(t, wallet1.Address.Hex(), retrievedWallet1.Address.Hex())

		retrievedWallet2, err := engine.GetWalletFromDB(user.Address, wallet2.Address.Hex())
		require.NoError(t, err)
		assert.Equal(t, wallet2.Address.Hex(), retrievedWallet2.Address.Hex())
	})

	t.Run("ListWallets should work for existing wallets at limit", func(t *testing.T) {
		listResp, err := engine.ListWallets(user.Address, &avsproto.ListWalletReq{})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(listResp.Items), 2, "Should be able to list existing wallets even at limit")
	})

	t.Run("Reproducing the original bug: GetWallet for existing wallet should work", func(t *testing.T) {
		// This test validates that after our fix, users can access existing wallets even when
		// they have reached the wallet count limit.
		//
		// Strategy: First call GetWallet to create a new wallet (when we're not at limit yet),
		// then add more wallets to exceed the limit, then call GetWallet again with the same salt
		// to access the existing wallet.

		// Reset the database to have just 1 wallet initially
		// Remove the second wallet created in the setup
		db.Delete([]byte(WalletStorageKey(user.Address, addr2.Hex())))

		// Now we have 1 wallet (limit=2), so we can create one more
		// First, call GetWallet to create a wallet with salt=999 (this should succeed)
		payload := &avsproto.GetWalletReq{
			Salt: "999",
		}

		resp1, err1 := engine.GetWallet(user, payload)
		require.NoError(t, err1, "Should be able to create wallet when under limit")
		walletAddr := resp1.Address
		t.Logf("DEBUG: Created wallet at address: %s", walletAddr)

		// Now add the second wallet back to reach the limit
		err2 := StoreWallet(db, user.Address, wallet2)
		require.NoError(t, err2)

		// Now we have 3 wallets (limit=2), so we're over the limit
		// Verify we can still access the existing wallet with salt=999
		resp2, err2 := engine.GetWallet(user, payload)
		if err2 != nil {
			t.Logf("DEBUG: GetWallet failed with error: %v", err2)
		}
		require.NoError(t, err2, "Should be able to access existing wallet even when over limit")
		assert.Equal(t, walletAddr, resp2.Address, "Should return the same wallet address as before")
	})

	t.Run("Creating NEW wallets should still be blocked at limit", func(t *testing.T) {
		// Try to create a new wallet with a salt that doesn't exist
		payload := &avsproto.GetWalletReq{
			Salt: "999999", // This salt shouldn't have an existing wallet
		}

		// This should fail because we're trying to create a NEW wallet when at the limit
		_, err := engine.GetWallet(user, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max smart wallet count reached for owner (limit=2)")
	})
}

// TestWalletCountLimitReproducesOriginalWithdrawFundsBug validates that the specific
// withdrawFunds workflow now works correctly after our fix.
func TestWalletCountLimitReproducesOriginalWithdrawFundsBug(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	// Set limit to 3 to match the exact error message from the original issue
	config.SmartWallet.MaxWalletsPerOwner = 3
	engine := New(db, config, nil, testutil.GetLogger())

	user := testutil.TestUser1()
	factoryAddr := config.SmartWallet.FactoryAddress

	// Create exactly 3 wallets to reach the limit
	wallets := make([]*model.SmartWallet, 3)
	for i := 0; i < 3; i++ {
		addr := common.BigToAddress(big.NewInt(int64(i + 1000))) // Generate unique addresses
		wallet := &model.SmartWallet{
			Owner:   &user.Address,
			Address: &addr,
			Factory: &factoryAddr,
			Salt:    big.NewInt(int64(i)),
		}
		err := StoreWallet(db, user.Address, wallet)
		require.NoError(t, err)
		wallets[i] = wallet
	}

	t.Run("Reproduces original error message format", func(t *testing.T) {
		// Try to create a 4th wallet - this should fail with the exact error from the issue
		payload := &avsproto.GetWalletReq{
			Salt: "999", // New wallet
		}

		_, err := engine.GetWallet(user, payload)
		require.Error(t, err)
		// This should match the exact error message from the original issue
		assert.Contains(t, err.Error(), "max smart wallet count reached for owner (limit=3)")
	})

	t.Run("validateSmartWalletOwnership works for existing wallets at limit", func(t *testing.T) {
		// This simulates the exact code path that withdrawFunds uses
		for i, wallet := range wallets {
			retrievedWallet, err := engine.GetWalletFromDB(user.Address, wallet.Address.Hex())
			require.NoError(t, err, "Wallet %d should be accessible via GetWalletFromDB", i)
			assert.Equal(t, wallet.Address.Hex(), retrievedWallet.Address.Hex())

			// Verify ownership validation would pass
			assert.Equal(t, user.Address, *retrievedWallet.Owner)
		}
	})

	t.Run("All wallet management operations work at limit", func(t *testing.T) {
		// List wallets
		listResp, err := engine.ListWallets(user.Address, &avsproto.ListWalletReq{})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(listResp.Items), 3, "Should list all wallets")

		// Access specific wallets
		for _, wallet := range wallets {
			retrievedWallet, err := GetWallet(db, user.Address, wallet.Address.Hex())
			require.NoError(t, err)
			assert.Equal(t, wallet.Address.Hex(), retrievedWallet.Address.Hex())
		}
	})

	t.Run("Confirms fix resolves withdrawFunds workflow", func(t *testing.T) {
		// This test confirms that the specific workflow that was failing in withdrawFunds now works:
		// 1. User has 3 wallets (at the limit)
		// 2. User calls withdrawFunds with a smart wallet address
		// 3. withdrawFunds calls validateSmartWalletOwnership
		// 4. validateSmartWalletOwnership calls engine.GetWalletFromDB
		// 5. This should work now (it was failing before our fix)

		testWallet := wallets[0] // Pick any existing wallet

		// Simulate the validateSmartWalletOwnership call from withdrawFunds
		walletModel, err := engine.GetWalletFromDB(user.Address, testWallet.Address.Hex())
		require.NoError(t, err, "GetWalletFromDB should work for existing wallet even at limit")

		// Validate the ownership check would pass
		require.NotNil(t, walletModel.Owner)
		assert.Equal(t, user.Address, *walletModel.Owner, "Ownership validation should pass")

		// This confirms that withdrawFunds would now work for this wallet
		t.Logf("✅ Confirmed: withdrawFunds workflow would succeed for wallet %s", testWallet.Address.Hex())
	})
}

// TestWalletCountLimitEdgeCases tests various edge cases around the wallet limit
func TestWalletCountLimitEdgeCases(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	config.SmartWallet.MaxWalletsPerOwner = 1 // Very restrictive limit for edge case testing
	engine := New(db, config, nil, testutil.GetLogger())

	user := testutil.TestUser1()
	_ = config.SmartWallet.FactoryAddress // Avoid unused variable warning

	t.Run("No wallets - should allow first wallet creation", func(t *testing.T) {
		// Create first wallet should work
		payload := &avsproto.GetWalletReq{
			Salt: "0",
		}

		resp, err := engine.GetWallet(user, payload)
		require.NoError(t, err, "Should be able to create first wallet")
		assert.NotEmpty(t, resp.Address)
	})

	t.Run("At limit - existing wallet access should work", func(t *testing.T) {
		// Access the wallet we just created
		payload := &avsproto.GetWalletReq{
			Salt: "0", // Same salt as above
		}

		resp, err := engine.GetWallet(user, payload)
		require.NoError(t, err, "Should be able to access existing wallet")
		assert.NotEmpty(t, resp.Address)
	})

	t.Run("At limit - new wallet creation should fail", func(t *testing.T) {
		// Try to create second wallet
		payload := &avsproto.GetWalletReq{
			Salt: "1", // Different salt = new wallet
		}

		_, err := engine.GetWallet(user, payload)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max smart wallet count reached for owner (limit=1)")
	})
}
