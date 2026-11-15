package taskengine

import (
	"errors"

	"github.com/AvaProtocol/EigenLayer-AVS/core/walletinventory"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
)

const TaskIDLength = 26

// ValidWalletOwner reports whether the smart wallet at smartWalletAddress is
// owned by u on the operation network. MA v2 identities are global and may
// materialize their runtime row here; legacy and designated EOA records keep
// their network-specific ownership lookup.
func ValidWalletOwner(db storage.Storage, chainID int64, u *model.User, smartWalletAddress common.Address) (bool, error) {
	// the smart wallet address is the default one (if SmartAccountAddress is set)
	// Note: nil check is required because some callers create User objects with only Address field set
	if u.SmartAccountAddress != nil && u.SmartAccountAddress.Hex() == smartWalletAddress.Hex() {
		return true, nil
	}

	// A global derived identity is owned on every supported operation network.
	if _, err := walletinventory.Get(db, u.Address, smartWalletAddress.Hex()); err == nil {
		_, err = walletinventory.Materialize(db, chainID, u.Address, smartWalletAddress.Hex())
		return err == nil, err
	} else if !errors.Is(err, badger.ErrKeyNotFound) {
		return false, err
	}
	// Historical and designated-EOA records retain their local ownership checks.
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
