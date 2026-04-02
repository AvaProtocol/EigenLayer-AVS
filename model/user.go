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
	Owner      *common.Address `json:"owner"`
	Address    *common.Address `json:"address"`
	Factory    *common.Address `json:"factory,omitempty"`
	Salt       *big.Int        `json:"salt"`
	IsHidden   bool            `json:"is_hidden,omitempty"`
	WalletType int32           `json:"wallet_type,omitempty"` // 0/1=basic, 2=calibur
}

// CaliburKeyInfo stores the aggregator's sub-key credentials for a Calibur wallet.
// Stored separately from the wallet record because it has a different lifecycle
// (registration/revocation) and contains sensitive key material.
type CaliburKeyInfo struct {
	PublicKey   string `json:"public_key"`   // Hex-encoded secp256k1 public key
	PrivateKey  string `json:"private_key"`  // Hex-encoded secp256k1 private key
	KeyHash     string `json:"key_hash"`     // Hex-encoded Calibur keyHash (bytes32)
	HookAddress string `json:"hook_address"` // Permission hook contract address
	Expiry      int64  `json:"expiry"`       // Unix timestamp, 0 = no expiry
	Status      string `json:"status"`       // "active", "revoked"
}

func (k *CaliburKeyInfo) ToJSON() ([]byte, error) {
	return json.Marshal(k)
}

func (k *CaliburKeyInfo) FromStorageData(body []byte) error {
	return json.Unmarshal(body, k)
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
	Enabled   uint64
	Completed uint64
	Failed    uint64
	Disabled  uint64
}
