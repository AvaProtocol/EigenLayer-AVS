package taskengine

import (
	"encoding/json"
	"fmt"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
)

type ExtendedWallet struct {
	model.SmartWallet
	IsHidden bool `json:"is_hidden,omitempty"`
}

func GetExtendedWallet(db storage.Storage, owner common.Address, smartWalletAddress string) (*ExtendedWallet, error) {
	walletKey := WalletStorageKey(owner, smartWalletAddress)
	walletData, err := db.GetKey([]byte(walletKey))
	if err != nil {
		return nil, err // Includes badger.ErrKeyNotFound
	}

	var extWallet ExtendedWallet
	if unmarshalErr := json.Unmarshal(walletData, &extWallet); unmarshalErr != nil {
		var wallet model.SmartWallet
		if regErr := wallet.FromStorageData(walletData); regErr != nil {
			return nil, fmt.Errorf("failed to unmarshal wallet data for key %s: %w", walletKey, unmarshalErr)
		}
		extWallet = ExtendedWallet{
			SmartWallet: wallet,
			IsHidden:    false, // Default to false for existing wallets
		}
	}
	return &extWallet, nil
}

func StoreExtendedWallet(db storage.Storage, owner common.Address, wallet *ExtendedWallet) error {
	if wallet.Address == nil {
		return fmt.Errorf("cannot store wallet with nil address")
	}
	walletKey := WalletStorageKey(owner, wallet.Address.String())
	updatedWalletData, err := json.Marshal(wallet)
	if err != nil {
		return fmt.Errorf("failed to marshal wallet for storage (key: %s): %w", walletKey, err)
	}
	return db.Set([]byte(walletKey), updatedWalletData)
}
