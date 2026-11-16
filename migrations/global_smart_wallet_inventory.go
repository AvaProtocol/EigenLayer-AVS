package migrations

import (
	"github.com/AvaProtocol/EigenLayer-AVS/core/walletinventory"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func GlobalSmartWalletInventory(db storage.Storage) (int, error) { return walletinventory.Migrate(db) }
