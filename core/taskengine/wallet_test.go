package taskengine

import (
	"math/big"
	"strings"
	"testing"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestSetWalletHiddenStatus(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	u := testutil.TestUser1()
	defaultFactory := effectiveFactoryHex(t, n.smartWalletConfig)

	saltValue := "12345"
	// Ensure wallet exists
	_, err := n.GetWallet(u, &avsproto.GetWalletReq{
		Salt: saltValue,
	})
	if err != nil {
		t.Fatalf("Failed to create/get wallet: %v", err)
	}

	// Check it's initially not hidden
	listResp, err := n.ListWallets(u, &avsproto.ListWalletReq{})
	if err != nil {
		t.Fatalf("ListWallets failed: %v", err)
	}
	found := false
	var initialWallet *avsproto.SmartWallet
	for _, w := range listResp.Items {
		if w.Salt == saltValue && w.Factory == defaultFactory {
			initialWallet = w
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Wallet with salt %s and factory %s not found in initial list", saltValue, defaultFactory)
	}
	if initialWallet.IsHidden {
		t.Errorf("Wallet with salt %s should initially not be hidden", saltValue)
	}

	// Hide the wallet
	_, err = n.SetWallet(u.Address, &avsproto.SetWalletReq{
		Salt:           saltValue,
		FactoryAddress: defaultFactory, // Explicitly set default factory
		IsHidden:       true,
	})
	if err != nil {
		t.Fatalf("Failed to hide wallet: %v", err)
	}

	// Check it's now hidden in the list
	listResp, err = n.ListWallets(u, &avsproto.ListWalletReq{})
	if err != nil {
		t.Fatalf("ListWallets after hiding failed: %v", err)
	}
	foundAndHidden := false
	for _, w := range listResp.Items {
		if w.Salt == saltValue && w.Factory == defaultFactory {
			if w.IsHidden {
				foundAndHidden = true
			} else {
				t.Errorf("Wallet with salt %s and factory %s found but IsHidden is false after setting to true", saltValue, defaultFactory)
			}
			break
		}
	}
	if !foundAndHidden {
		t.Errorf("Wallet with salt %s and factory %s not found or not hidden in list after attempting to hide", saltValue, defaultFactory)
	}

	// Unhide the wallet
	_, err = n.SetWallet(u.Address, &avsproto.SetWalletReq{
		Salt:           saltValue,
		FactoryAddress: defaultFactory,
		IsHidden:       false,
	})
	if err != nil {
		t.Fatalf("Failed to unhide wallet: %v", err)
	}

	// Check it's no longer hidden
	listResp, err = n.ListWallets(u, &avsproto.ListWalletReq{})
	if err != nil {
		t.Fatalf("ListWallets after unhiding failed: %v", err)
	}
	foundAndNotHidden := false
	for _, w := range listResp.Items {
		if w.Salt == saltValue && w.Factory == defaultFactory {
			if !w.IsHidden {
				foundAndNotHidden = true
			} else {
				t.Errorf("Wallet with salt %s and factory %s found but IsHidden is true after setting to false", saltValue, defaultFactory)
			}
			break
		}
	}
	if !foundAndNotHidden {
		t.Errorf("Wallet with salt %s and factory %s not found or still hidden in list after attempting to unhide", saltValue, defaultFactory)
	}
}

// Only the MA v2 factory creates wallets since the EntryPoint v0.7 cutover. A
// legacy factory must not mint a new record (it would be listed and capped, and
// the one-off retirement migration does not run again), but a wallet that
// already has a record stays readable and its hidden flag can still change.
func TestGetWalletRefusesNewLegacyFactoryWallets(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	u := testutil.TestUser1()
	chainID := n.resolveUserChainID(u)

	saltValue := "67890"
	legacyFactory := "0x9406Cc6185a346906296840746125a0E44976454"

	_, err := n.GetWallet(u, &avsproto.GetWalletReq{Salt: saltValue, FactoryAddress: legacyFactory})
	require.Error(t, err, "a new legacy-factory wallet is refused")
	assert.Contains(t, err.Error(), LegacyFactoryWalletError)

	items, err := db.GetByPrefix(WalletByOwnerPrefix(chainID, u.Address))
	if err != nil {
		require.ErrorIs(t, err, badger.ErrKeyNotFound)
	}
	for _, item := range items {
		stored := &model.SmartWallet{}
		require.NoError(t, stored.FromStorageData(item.Value))
		require.NotNil(t, stored.Factory)
		assert.NotEqual(t, strings.ToLower(legacyFactory), strings.ToLower(stored.Factory.Hex()), "nothing stored for the refused factory")
	}

	// A pre-cutover record (here, one the migration retired) stays readable.
	factory := common.HexToAddress(legacyFactory)
	salt, ok := new(big.Int).SetString(saltValue, 10)
	require.True(t, ok)
	derived, err := aa.DeriveSenderAddressAuto(rpcConn, u.Address, factory, salt)
	require.NoError(t, err)
	require.NoError(t, StoreWallet(db, chainID, u.Address, &model.SmartWallet{
		Owner: &u.Address, Address: derived, Factory: &factory, Salt: salt, IsHidden: true, Retired: true,
	}))

	resp, err := n.GetWallet(u, &avsproto.GetWalletReq{Salt: saltValue, FactoryAddress: legacyFactory})
	require.NoError(t, err, "an existing legacy record is still returned")
	assert.True(t, strings.EqualFold(derived.Hex(), resp.Address))

	_, err = n.SetWallet(u.Address, &avsproto.SetWalletReq{Salt: saltValue, FactoryAddress: legacyFactory, IsHidden: false})
	require.NoError(t, err, "the hidden flag of an existing record can still change")
	stored, err := GetWallet(db, chainID, u.Address, derived.Hex())
	require.NoError(t, err)
	assert.False(t, stored.IsHidden)
	assert.True(t, stored.Retired, "unhiding does not un-retire")

	listResp, err := n.ListWallets(u, &avsproto.ListWalletReq{FactoryAddress: legacyFactory})
	require.NoError(t, err)
	for _, w := range listResp.Items {
		assert.False(t, strings.EqualFold(w.Address, derived.Hex()), "a retired wallet is never listed")
	}
}

func TestHideDefaultWallet(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	u := testutil.TestUser1()
	defaultFactory := effectiveFactoryHex(t, n.smartWalletConfig)
	defaultSalt := "0"

	// Ensure default wallet exists and get its address
	defaultWalletResp, err := n.GetWallet(u, &avsproto.GetWalletReq{
		Salt:           defaultSalt,    // Explicitly asking for salt 0
		FactoryAddress: defaultFactory, // Explicitly asking for default factory
	})
	if err != nil {
		t.Fatalf("Failed to get default wallet: %v", err)
	}
	defaultWalletAddress := defaultWalletResp.Address

	if defaultWalletResp.IsHidden {
		t.Errorf("Default wallet %s should initially not be hidden", defaultWalletAddress)
	}

	// Hide the default wallet
	_, err = n.SetWallet(u.Address, &avsproto.SetWalletReq{
		Salt:           defaultSalt,
		FactoryAddress: defaultFactory,
		IsHidden:       true,
	})
	if err != nil {
		t.Fatalf("Failed to hide default wallet: %v", err)
	}

	// Check it's now hidden
	listResp, err := n.ListWallets(u, &avsproto.ListWalletReq{})
	if err != nil {
		t.Fatalf("ListWallets after hiding default wallet failed: %v", err)
	}
	foundAndHidden := false
	for _, w := range listResp.Items {
		if w.Address == defaultWalletAddress {
			if w.IsHidden {
				foundAndHidden = true
			} else {
				t.Errorf("Default wallet %s found but IsHidden is false after setting to true", defaultWalletAddress)
			}
			break
		}
	}
	if !foundAndHidden {
		t.Errorf("Default wallet %s not found or not hidden after attempting to hide", defaultWalletAddress)
	}

	// Unhide the default wallet
	_, err = n.SetWallet(u.Address, &avsproto.SetWalletReq{
		Salt:           defaultSalt,
		FactoryAddress: defaultFactory,
		IsHidden:       false,
	})
	if err != nil {
		t.Fatalf("Failed to unhide default wallet: %v", err)
	}

	// Check it's no longer hidden
	listResp, err = n.ListWallets(u, &avsproto.ListWalletReq{})
	if err != nil {
		t.Fatalf("ListWallets after unhiding default wallet failed: %v", err)
	}
	foundAndNotHidden := false
	for _, w := range listResp.Items {
		if w.Address == defaultWalletAddress {
			if !w.IsHidden {
				foundAndNotHidden = true
			} else {
				t.Errorf("Default wallet %s found but IsHidden is true after setting to false", defaultWalletAddress)
			}
			break
		}
	}
	if !foundAndNotHidden {
		t.Errorf("Default wallet %s not found or still hidden after attempting to unhide", defaultWalletAddress)
	}
}
