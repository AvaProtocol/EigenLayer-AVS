package model

import (
	"encoding/json"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

type User struct {
	Address             common.Address
	SmartAccountAddress *common.Address
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
