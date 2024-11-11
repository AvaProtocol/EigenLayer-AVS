package model

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
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
		return fmt.Errorf("Rpc error")
	}
	u.SmartAccountAddress = smartAccountAddress
	return nil
}

type SmartWallet struct {
	Owner   *common.Address `json:"owner"`
	Address *common.Address `json:"address"`
	Factory *common.Address `json:"factory,omitempty"`
	Salt    *big.Int        `json:"salt"`
}

func (w *SmartWallet) ToJSON() ([]byte, error) {
	return json.Marshal(w)
}

func (w *SmartWallet) FromStorageData(body []byte) error {
	err := json.Unmarshal(body, w)

	return err
}
