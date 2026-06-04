package taskengine

import (
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
)

const TaskIDLength = 26

// ValidWalletOwner reports whether the smart wallet at smartWalletAddress is
// owned by u on the given chain. Wallets are chain-scoped (the factory
// address differs per chain, so a wallet derived on chain A is not the same
// record as one on chain B even if the EOA owner matches), so the chainID is
// part of the ownership question.
func ValidWalletOwner(db storage.Storage, chainID int64, u *model.User, smartWalletAddress common.Address) (bool, error) {
	// the smart wallet address is the default one (if SmartAccountAddress is set)
	// Note: nil check is required because some callers create User objects with only Address field set
	if u.SmartAccountAddress != nil && u.SmartAccountAddress.Hex() == smartWalletAddress.Hex() {
		return true, nil
	}

	// not default, look up in our storage
	exists, err := db.Exist([]byte(WalletStorageKey(chainID, u.Address, smartWalletAddress.Hex())))
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
