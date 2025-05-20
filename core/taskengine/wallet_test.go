package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestSetWalletHiddenStatus(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	u := testutil.TestUser1()
	defaultFactory := n.smartWalletConfig.FactoryAddress.Hex()

	saltValue := "12345"
	// Ensure wallet exists
	_, err := n.GetWallet(u, &avsproto.GetWalletReq{
		Salt: saltValue,
	})
	if err != nil {
		t.Fatalf("Failed to create/get wallet: %v", err)
	}

	// Check it's initially not hidden
	listResp, err := n.ListWallets(u.Address, &avsproto.ListWalletReq{})
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
	listResp, err = n.ListWallets(u.Address, &avsproto.ListWalletReq{})
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
	listResp, err = n.ListWallets(u.Address, &avsproto.ListWalletReq{})
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

func TestSetWalletHiddenStatusWithCustomFactory(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	u := testutil.TestUser1()

	saltValue := "67890"
	customFactoryAddress := "0x9406Cc6185a346906296840746125a0E44976454"
	// Ensure wallet exists
	_, err := n.GetWallet(u, &avsproto.GetWalletReq{
		Salt:           saltValue,
		FactoryAddress: customFactoryAddress,
	})
	if err != nil {
		t.Fatalf("Failed to create/get wallet with custom factory: %v", err)
	}

	// Check it's initially not hidden
	listResp, err := n.ListWallets(u.Address, &avsproto.ListWalletReq{FactoryAddress: customFactoryAddress})
	if err != nil {
		t.Fatalf("ListWallets failed for custom factory: %v", err)
	}
	found := false
	var initialWallet *avsproto.SmartWallet
	for _, w := range listResp.Items {
		if w.Salt == saltValue && w.Factory == customFactoryAddress {
			initialWallet = w
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Wallet with salt %s and factory %s not found in initial list", saltValue, customFactoryAddress)
	}
	if initialWallet.IsHidden {
		t.Errorf("Wallet with salt %s and factory %s should initially not be hidden", saltValue, customFactoryAddress)
	}

	// Hide the wallet
	_, err = n.SetWallet(u.Address, &avsproto.SetWalletReq{
		Salt:           saltValue,
		FactoryAddress: customFactoryAddress,
		IsHidden:       true,
	})
	if err != nil {
		t.Fatalf("Failed to hide wallet with custom factory: %v", err)
	}

	// Check it's now hidden in the list
	listResp, err = n.ListWallets(u.Address, &avsproto.ListWalletReq{FactoryAddress: customFactoryAddress})
	if err != nil {
		t.Fatalf("ListWallets for custom factory after hiding failed: %v", err)
	}
	foundAndHidden := false
	for _, w := range listResp.Items {
		if w.Salt == saltValue && w.Factory == customFactoryAddress {
			if w.IsHidden {
				foundAndHidden = true
			} else {
				t.Errorf("Wallet with salt %s and factory %s found but IsHidden is false after setting to true", saltValue, customFactoryAddress)
			}
			break
		}
	}
	if !foundAndHidden {
		t.Errorf("Wallet with salt %s and factory %s not found or not hidden in list after attempting to hide", saltValue, customFactoryAddress)
	}

	// Unhide the wallet
	_, err = n.SetWallet(u.Address, &avsproto.SetWalletReq{
		Salt:           saltValue,
		FactoryAddress: customFactoryAddress,
		IsHidden:       false,
	})
	if err != nil {
		t.Fatalf("Failed to unhide wallet with custom factory: %v", err)
	}

	listResp, err = n.ListWallets(u.Address, &avsproto.ListWalletReq{FactoryAddress: customFactoryAddress})
	if err != nil {
		t.Fatalf("ListWallets for custom factory after unhiding failed: %v", err)
	}
	foundAndNotHidden := false
	for _, w := range listResp.Items {
		if w.Salt == saltValue && w.Factory == customFactoryAddress {
			if !w.IsHidden {
				foundAndNotHidden = true
			} else {
				t.Errorf("Wallet with salt %s and factory %s found but IsHidden is true after setting to false", saltValue, customFactoryAddress)
			}
			break
		}
	}
	if !foundAndNotHidden {
		t.Errorf("Wallet with salt %s and factory %s not found or still hidden in list after attempting to unhide", saltValue, customFactoryAddress)
	}
}

func TestHideDefaultWallet(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	u := testutil.TestUser1()
	defaultFactory := n.smartWalletConfig.FactoryAddress.Hex()
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
	listResp, err := n.ListWallets(u.Address, &avsproto.ListWalletReq{})
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
	listResp, err = n.ListWallets(u.Address, &avsproto.ListWalletReq{})
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
