package taskengine

import (
	"encoding/json"
	"fmt"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
)

type WalletWithHiddenStatus struct {
	model.SmartWallet
	IsHidden bool `json:"is_hidden,omitempty"`
}

func GetWalletWithHiddenStatus(db storage.Storage, owner common.Address, smartWalletAddress string) (*WalletWithHiddenStatus, error) {
	walletKey := WalletStorageKey(owner, smartWalletAddress)
	walletData, err := db.GetKey([]byte(walletKey))
	if err != nil {
		return nil, err // Includes badger.ErrKeyNotFound
	}

	var extWallet WalletWithHiddenStatus
	if unmarshalErr := json.Unmarshal(walletData, &extWallet); unmarshalErr != nil {
		var wallet model.SmartWallet
		if regErr := wallet.FromStorageData(walletData); regErr != nil {
			return nil, fmt.Errorf("failed to unmarshal wallet data for key %s: %w", walletKey, unmarshalErr)
		}
		extWallet = WalletWithHiddenStatus{
			SmartWallet: wallet,
			IsHidden:    false, // Default to false for existing wallets
		}
	}
	return &extWallet, nil
}

func StoreWalletWithHiddenStatus(db storage.Storage, owner common.Address, wallet *WalletWithHiddenStatus) error {
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

func IsWalletHidden(db storage.Storage, owner common.Address, smartWalletAddress string) (bool, error) {
	extWallet, err := GetWalletWithHiddenStatus(db, owner, smartWalletAddress)
	if err != nil {
		return false, err
	}
	return extWallet.IsHidden, nil
}

func SetWalletHidden(db storage.Storage, owner common.Address, smartWalletAddress string, hidden bool) error {
	extWallet, err := GetWalletWithHiddenStatus(db, owner, smartWalletAddress)
	if err != nil {
		return err
	}
	
	extWallet.IsHidden = hidden
	return StoreWalletWithHiddenStatus(db, owner, extWallet)
}
