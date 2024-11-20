package taskengine

import (
	"testing"

	"github.com/AvaProtocol/ap-avs/model"
	"github.com/AvaProtocol/ap-avs/storage"
	"github.com/ethereum/go-ethereum/common"
)

func TestWalletOwnerReturnTrueForDefaultAddress(t *testing.T) {
	smartAddress := common.HexToAddress("0x5Df343de7d99fd64b2479189692C1dAb8f46184a")

	result, err := ValidWalletOwner(nil, &model.User{
		Address:             common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5"),
		SmartAccountAddress: &smartAddress,
	}, common.HexToAddress("0x5Df343de7d99fd64b2479189692C1dAb8f46184a"))

	if !result || err != nil {
		t.Errorf("expect true, got false")
	}
}

func TestWalletOwnerReturnTrueForNonDefaultAddress(t *testing.T) {
	db := TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	eoa := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")
	defaultSmartWallet := common.HexToAddress("0x5Df343de7d99fd64b2479189692C1dAb8f46184a")
	customSmartWallet := common.HexToAddress("0xdD85693fd14b522a819CC669D6bA388B4FCd158d")

	result, err := ValidWalletOwner(db, &model.User{
		Address:             eoa,
		SmartAccountAddress: &defaultSmartWallet,
	}, customSmartWallet)
	if result == true {
		t.Errorf("expect 0xdD85693fd14b522a819CC669D6bA388B4FCd158d not owned by 0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5, got true")
	}

	// setup wallet binding
	db.Set([]byte(WalletStorageKey(eoa, customSmartWallet.Hex())), []byte("1"))

	result, err = ValidWalletOwner(db, &model.User{
		Address:             eoa,
		SmartAccountAddress: &defaultSmartWallet,
	}, customSmartWallet)
	if !result || err != nil {
		t.Errorf("expect 0xdD85693fd14b522a819CC669D6bA388B4FCd158d owned by 0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5, got false")
	}
}
