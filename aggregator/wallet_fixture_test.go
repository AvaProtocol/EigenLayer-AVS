package aggregator

import (
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/walletinventory"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
)

// storeWalletFixture explicitly admits derived test identities. The production
// compatibility writer only accepts already registered identities or history.
func storeWalletFixture(db storage.Storage, chainID int64, owner common.Address, wallet *model.SmartWallet) error {
	if walletinventory.IsDerived(wallet) && !wallet.Retired && !wallet.StaleDerivation {
		_, err := walletinventory.Register(db, chainID, owner, wallet, config.HardMaxWalletsPerOwner)
		return err
	}
	return taskengine.StoreWallet(db, chainID, owner, wallet)
}
