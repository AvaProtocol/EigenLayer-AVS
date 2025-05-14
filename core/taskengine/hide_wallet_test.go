package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestHideWallet(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	u := testutil.TestUser1()

	saltValue := "54321"
	_, err := n.GetWallet(u, &avsproto.GetWalletReq{
		Salt: saltValue,
	})
	if err != nil {
		t.Errorf("Failed to create wallet: %v", err)
	}

	wallets, _ := n.GetSmartWallets(u.Address, nil)
	var found bool
	for _, w := range wallets {
		if w.Salt == saltValue {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Wallet with salt %s not found in wallet list", saltValue)
	}

	_, err = n.HideWallet(u, &avsproto.GetWalletReq{
		Salt: saltValue,
	}, true)
	if err != nil {
		t.Errorf("Failed to hide wallet: %v", err)
	}

	wallets, _ = n.GetSmartWallets(u.Address, nil)
	for _, w := range wallets {
		if w.Salt == saltValue {
			t.Errorf("Hidden wallet with salt %s should not be in wallet list", saltValue)
		}
	}

	_, err = n.HideWallet(u, &avsproto.GetWalletReq{
		Salt: saltValue,
	}, false)
	if err != nil {
		t.Errorf("Failed to unhide wallet: %v", err)
	}

	wallets, _ = n.GetSmartWallets(u.Address, nil)
	found = false
	for _, w := range wallets {
		if w.Salt == saltValue {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Unhidden wallet with salt %s should be in wallet list", saltValue)
	}
}

func TestHideWalletWithCustomFactory(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	u := testutil.TestUser1()

	saltValue := "98765"
	factoryAddress := "0x9406Cc6185a346906296840746125a0E44976454"
	_, err := n.GetWallet(u, &avsproto.GetWalletReq{
		Salt:           saltValue,
		FactoryAddress: factoryAddress,
	})
	if err != nil {
		t.Errorf("Failed to create wallet: %v", err)
	}

	wallets, _ := n.GetSmartWallets(u.Address, &avsproto.ListWalletReq{
		FactoryAddress: factoryAddress,
	})
	var found bool
	for _, w := range wallets {
		if w.Salt == saltValue {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Wallet with salt %s and factory %s not found in wallet list", saltValue, factoryAddress)
	}

	_, err = n.HideWallet(u, &avsproto.GetWalletReq{
		Salt:           saltValue,
		FactoryAddress: factoryAddress,
	}, true)
	if err != nil {
		t.Errorf("Failed to hide wallet: %v", err)
	}

	wallets, _ = n.GetSmartWallets(u.Address, &avsproto.ListWalletReq{
		FactoryAddress: factoryAddress,
	})
	for _, w := range wallets {
		if w.Salt == saltValue {
			t.Errorf("Hidden wallet with salt %s should not be in wallet list", saltValue)
		}
	}
}

func TestHideDefaultWallet(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	u := testutil.TestUser1()

	defaultWallet, err := n.GetWallet(u, &avsproto.GetWalletReq{})
	if err != nil {
		t.Errorf("Failed to get default wallet: %v", err)
	}

	wallets, _ := n.GetSmartWallets(u.Address, nil)
	var found bool
	for _, w := range wallets {
		if w.Address == defaultWallet.Address {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Default wallet not found in wallet list")
	}

	_, err = n.HideWallet(u, &avsproto.GetWalletReq{}, true)
	if err != nil {
		t.Errorf("Failed to hide default wallet: %v", err)
	}

	wallets, _ = n.GetSmartWallets(u.Address, nil)
	for _, w := range wallets {
		if w.Address == defaultWallet.Address {
			t.Errorf("Hidden default wallet should not be in wallet list")
		}
	}
}
