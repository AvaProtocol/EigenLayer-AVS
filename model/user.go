package model

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

type User struct {
	Address             common.Address
	SmartAccountAddress *common.Address
}

func (u *User) LoadDefaultSmartWallet(rpcClient *ethclient.Client) error {
	smartAccountAddress, err := aa.GetSenderAddress(rpcClient, u.Address, big.NewInt(0))
	if err != nil {
		return fmt.Errorf("failed to derive smart wallet address for owner %s: %w", u.Address.Hex(), err)
	}
	u.SmartAccountAddress = smartAccountAddress
	return nil
}

// Return the smartwallet struct re-present the default wallet for this user
func (u *User) ToSmartWallet() *SmartWallet {
	return &SmartWallet{
		Owner:   &u.Address,
		Address: u.SmartAccountAddress,
	}
}

type SmartWallet struct {
	Owner    *common.Address `json:"owner"`
	Address  *common.Address `json:"address"`
	Factory  *common.Address `json:"factory,omitempty"`
	Salt     *big.Int        `json:"salt"`
	IsHidden bool            `json:"is_hidden,omitempty"`
}

func (w *SmartWallet) ToJSON() ([]byte, error) {
	return json.Marshal(w)
}

func (w *SmartWallet) FromStorageData(body []byte) error {
	err := json.Unmarshal(body, w)

	return err
}

type SmartWalletTaskStat struct {
	Total     uint64
	Active    uint64
	Completed uint64
	Failed    uint64
	Inactive  uint64
}
