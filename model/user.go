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
	// ChainID is the chain context for wallet RPC handlers that accept
	// *User. Today that's only Engine.GetWallet — SetWallet and
	// ListWallets keep their legacy owner-only signatures and always
	// operate on the gateway's default chain; plumbing *User through
	// them is a follow-up.
	//
	// The REST adapter sets this from the JWT's `aud` claim (see
	// aggregator/rest/middleware/jwt.go:audienceChainID) and it is the
	// authoritative source for the handlers that consult it — wallet
	// RPC payloads do NOT override it. Zero means "fall back to the
	// gateway's default chain", which is what the gRPC path uses since
	// gRPC isn't JWT-authenticated.
	ChainID int64
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
	Owner   *common.Address `json:"owner"`
	Address *common.Address `json:"address"`
	Factory *common.Address `json:"factory,omitempty"`
	Salt    *big.Int        `json:"salt"`
	// IsHidden is a user-toggleable flag controlling whether the wallet is
	// surfaced in default ListWallets responses.
	IsHidden bool `json:"is_hidden,omitempty"`
	// StaleDerivation is a system-set flag indicating that this wallet's
	// (owner, factory, salt) tuple no longer derives to this Address — i.e.
	// the factory's account implementation was upgraded and a newer wallet
	// address now claims the (owner, factory, salt) slot. Stale records are
	// preserved (the on-chain wallet may still hold assets) but excluded
	// from the (owner, factory, salt) secondary index and force-hidden in
	// list responses.
	StaleDerivation bool `json:"stale_derivation,omitempty"`

	// Kind is empty/omitted for derived CREATE2 wallets. "eoa_7702" is the
	// owner's EOA delegated to SemiModularAccount7702 (Track B). Additive;
	// existing rows stay empty.
	Kind string `json:"kind,omitempty"`
	// Delegate is the SMA-7702 implementation the EOA designates. Nil on
	// derived wallets.
	Delegate *common.Address `json:"delegate,omitempty"`
}

// WalletKindEOA7702 is SmartWallet.Kind for a 7702-delegated EOA runner.
const WalletKindEOA7702 = "eoa_7702"

// IsEOA7702 reports a Track B EOA runner record.
func (w *SmartWallet) IsEOA7702() bool {
	return w != nil && w.Kind == WalletKindEOA7702
}

// ValidateEOA7702Shape checks the B2 record contract: Address==Owner, no
// factory, no salt, Delegate set. Canonical-delegate equality is enforced
// at upsert (engine), not here — model must not import config.
func (w *SmartWallet) ValidateEOA7702Shape() error {
	if w == nil {
		return fmt.Errorf("nil eoa_7702 wallet")
	}
	if w.Kind != WalletKindEOA7702 {
		return fmt.Errorf("kind %q is not %s", w.Kind, WalletKindEOA7702)
	}
	if w.Owner == nil || w.Address == nil {
		return fmt.Errorf("eoa_7702 wallet needs owner and address")
	}
	if *w.Owner != *w.Address {
		return fmt.Errorf("eoa_7702 address %s must equal owner %s", w.Address.Hex(), w.Owner.Hex())
	}
	if w.Factory != nil && *w.Factory != (common.Address{}) {
		return fmt.Errorf("eoa_7702 wallet must not record a factory")
	}
	if w.Salt != nil {
		return fmt.Errorf("eoa_7702 wallet must not record a salt")
	}
	if w.Delegate == nil || *w.Delegate == (common.Address{}) {
		return fmt.Errorf("eoa_7702 wallet needs a delegate")
	}
	return nil
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
