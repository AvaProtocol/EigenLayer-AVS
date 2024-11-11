package taskengine

import (
	"github.com/AvaProtocol/ap-avs/model"
	"github.com/AvaProtocol/ap-avs/storage"
	"github.com/ethereum/go-ethereum/common"
)

func ValidWalletAddress(address string) bool {
	return common.IsHexAddress(address)
}

func ValidWalletOwner(db storage.Storage, u *model.User, smartWalletAddress common.Address) (bool, error) {
	// the smart wallet adress is the default one
	if u.Address.Hex() == smartWalletAddress.Hex() {
		return true, nil
	}

	// not default, look up in our storage
	exists, err := db.Exist([]byte(WalletStorageKey(u.Address, smartWalletAddress.Hex())))
	if exists {
		return true, nil
	}

	return false, err
}
