package taskengine

import (
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
)

const TaskIDLength = 26

func ValidWalletAddress(address string) bool {
	return common.IsHexAddress(address)
}

func ValidWalletOwner(db storage.Storage, u *model.User, smartWalletAddress common.Address) (bool, error) {
	// the smart wallet address is the default one (if SmartAccountAddress is set)
	// Note: nil check is required because some callers create User objects with only Address field set
	if u.SmartAccountAddress != nil && u.SmartAccountAddress.Hex() == smartWalletAddress.Hex() {
		return true, nil
	}

	// not default, look up in our storage
	exists, err := db.Exist([]byte(WalletStorageKey(u.Address, smartWalletAddress.Hex())))
	if exists {
		return true, nil
	}

	return false, err
}

func ValidateTaskId(id string) bool {
	if len(id) != TaskIDLength {
		return false
	}

	return true
}
