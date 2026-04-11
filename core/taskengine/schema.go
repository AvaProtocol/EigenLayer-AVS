package taskengine

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	storageschema "github.com/AvaProtocol/EigenLayer-AVS/storage/schema"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
)

func UserTaskStoragePrefix(address common.Address) []byte {
	return []byte(fmt.Sprintf("u:%s", strings.ToLower(address.String())))
}

func SmartWalletTaskStoragePrefix(owner common.Address, smartWalletAddress common.Address) []byte {
	return []byte(fmt.Sprintf("u:%s:%s", strings.ToLower(owner.Hex()), strings.ToLower(smartWalletAddress.Hex())))
}

func TaskByStatusStoragePrefix(status avsproto.TaskStatus) []byte {
	return storageschema.TaskByStatusStoragePrefix(status)
}

func WalletByOwnerPrefix(owner common.Address) []byte {
	return []byte(fmt.Sprintf(
		"w:%s",
		strings.ToLower(owner.String()),
	))
}

func WalletStorageKey(owner common.Address, smartWalletAddress string) string {
	return fmt.Sprintf(
		"w:%s:%s",
		strings.ToLower(owner.Hex()),
		strings.ToLower(smartWalletAddress),
	)
}

// WalletBySaltKey returns the secondary index key that maps a
// (owner, factory, salt) tuple to its current canonical wallet address.
//
// The primary wallet record is keyed by the *derived* wallet address
// (`w:<owner>:<address>`), which means that when a factory's account
// implementation is upgraded and `factory.getAddress(owner, salt)` starts
// returning a new address, the new address looks like a brand-new wallet
// to the primary store and a fresh row gets inserted alongside the old
// one. This index lets us cheaply ask "for this (owner, factory, salt)
// triple, which derived address is currently canonical?" so that the write
// path can detect the upgrade and mark the old row as stale instead of
// silently accumulating zombies.
//
// Salt is encoded in decimal (matching `(*big.Int).String()`) so the key
// is stable across encodings. The prefix is `wsalt:` (not `w:`) so it does
// not collide with `WalletByOwnerPrefix`.
func WalletBySaltKey(owner common.Address, factory common.Address, salt *big.Int) []byte {
	saltStr := "0"
	if salt != nil {
		saltStr = salt.String()
	}
	return []byte(fmt.Sprintf(
		"wsalt:%s:%s:%s",
		strings.ToLower(owner.Hex()),
		strings.ToLower(factory.Hex()),
		saltStr,
	))
}

// LookupCanonicalWalletAddress returns the wallet address currently
// registered as canonical for the given (owner, factory, salt) triple, or
// badger.ErrKeyNotFound if none has been recorded yet.
func LookupCanonicalWalletAddress(db storage.Storage, owner common.Address, factory common.Address, salt *big.Int) (common.Address, error) {
	raw, err := db.GetKey(WalletBySaltKey(owner, factory, salt))
	if err != nil {
		return common.Address{}, err
	}
	return common.HexToAddress(string(raw)), nil
}

func GetWallet(db storage.Storage, owner common.Address, smartWalletAddress string) (*model.SmartWallet, error) {
	walletKey := WalletStorageKey(owner, smartWalletAddress)
	walletData, err := db.GetKey([]byte(walletKey))
	if err != nil {
		return nil, err // Includes badger.ErrKeyNotFound
	}

	var walletModel model.SmartWallet
	if unmarshalErr := walletModel.FromStorageData(walletData); unmarshalErr != nil {
		return nil, fmt.Errorf("failed to unmarshal wallet data for key %s: %w", walletKey, unmarshalErr)
	}
	return &walletModel, nil
}

// StoreWallet persists the wallet record under its primary key
// (`w:<owner>:<address>`) and, when the wallet has a non-nil factory and
// salt and is not flagged stale, also writes the `(owner, factory, salt)`
// secondary index entry pointing at this wallet's address. Both writes
// happen in a single BatchWrite so the index can never be left dangling.
//
// Stale records (StaleDerivation == true) only update the primary entry —
// the secondary index intentionally continues to point at the new
// canonical wallet for that triple, not the stale one.
func StoreWallet(db storage.Storage, owner common.Address, wallet *model.SmartWallet) error {
	if wallet.Address == nil {
		return fmt.Errorf("cannot store wallet with nil address")
	}
	walletKey := WalletStorageKey(owner, wallet.Address.String())
	updatedWalletData, err := wallet.ToJSON()
	if err != nil {
		return fmt.Errorf("failed to marshal wallet for storage (key: %s): %w", walletKey, err)
	}

	// Fast path: no secondary index to update.
	if wallet.StaleDerivation || wallet.Factory == nil || wallet.Salt == nil {
		return db.Set([]byte(walletKey), updatedWalletData)
	}

	indexKey := WalletBySaltKey(owner, *wallet.Factory, wallet.Salt)
	indexValue := []byte(strings.ToLower(wallet.Address.Hex()))
	return db.BatchWrite(map[string][]byte{
		walletKey:        updatedWalletData,
		string(indexKey): indexValue,
	})
}

// MarkWalletStale flips a wallet record's StaleDerivation and IsHidden
// flags and re-persists it. The secondary index is intentionally NOT
// updated by this call (StoreWallet skips the index for stale records),
// so callers that want the index to point at a fresh canonical wallet
// must call StoreWallet on the new wallet *after* this returns.
func MarkWalletStale(db storage.Storage, owner common.Address, smartWalletAddress string) error {
	wallet, err := GetWallet(db, owner, smartWalletAddress)
	if err != nil {
		return fmt.Errorf("failed to load wallet to mark stale (%s): %w", smartWalletAddress, err)
	}
	wallet.StaleDerivation = true
	wallet.IsHidden = true
	return StoreWallet(db, owner, wallet)
}

func TaskStorageKey(id string, status avsproto.TaskStatus) []byte {
	return storageschema.TaskStorageKey(id, status)
}

func TaskUserKey(t *model.Task) []byte {
	return []byte(fmt.Sprintf(
		"u:%s:%s:%s",
		strings.ToLower(t.Owner),
		strings.ToLower(t.SmartWalletAddress),
		t.Key(),
	))
}

func TaskExecutionPrefix(taskID string) []byte {
	return []byte(fmt.Sprintf("history:%s", taskID))
}

func TaskExecutionKey(t *model.Task, executionID string) []byte {
	return []byte(fmt.Sprintf(
		"history:%s:%s",
		t.Id,
		executionID,
	))
}

func TaskTriggerKey(t *model.Task, executionID string) []byte {
	return []byte(fmt.Sprintf(
		"trigger:%s:%s",
		t.Id,
		executionID,
	))
}

// Pending execution queue (per task) — used to tie pre-created execution IDs
// to operator-triggered executions in FIFO order
func PendingExecutionPrefix(taskID string) []byte {
	return []byte(fmt.Sprintf("pending:%s:", taskID))
}

func PendingExecutionKey(t *model.Task, executionID string) []byte {
	return []byte(fmt.Sprintf("pending:%s:%s", t.Id, executionID))
}

func ExecutionIdFromStorageKey(key []byte) string {
	// key layout: history:01JG2FE5MDVKBPHEG0PEYSDKAC:01JG2FE5MFKTH0754RGF2DMVY7
	return string(key[35:])
}

func ExecutionIdFromPendingKey(key []byte) string {
	// key layout: pending:01JG2FE5MDVKBPHEG0PEYSDKAC:01JG2FE5MFKTH0754RGF2DMVY7
	// "pending:" = 8 bytes, task id = 26 bytes, colon = 1 => start at 8+26+1 = 35
	return string(key[35:])
}

func TaskIdFromExecutionStorageKey(key []byte) string {
	// key layout: history:01JG2FE5MDVKBPHEG0PEYSDKAC:01JG2FE5MFKTH0754RGF2DMVY7
	return string(key[8:34])
}

func TaskIdFromTaskStatusStorageKey(key []byte) []byte {
	// Exampley key u:0xd7050816337a3f8f690f8083b5ff8019d50c0e50:0x415f09526f25d6520d471890abf0953b0505313d:01JMN2JHAGXTNSY46KH0KYY0MZ
	return key[88:114]
}

func SecretStorageKey(secret *model.Secret) (string, error) {
	user := secret.User
	if user == nil {
		return "", fmt.Errorf("Secret is missing required user field")
	}

	key := ""
	if secret.WorkflowID != "" {
		// Ensure the workflow belongs to this user
		key = string(SecretStorageKeyForWorkflow(user, secret.Name, secret.WorkflowID))
	} else if secret.OrgID != "" {
		key = string(SecretStorageKeyForOrg(user, secret.OrgID, secret.Name))
	} else {
		key = string(SecretStorageKeyForUser(user, secret.Name))
	}
	return key, nil
}

func SecretStorageKeyForUser(u *model.User, name string) string {
	return fmt.Sprintf(
		"secret:_:%s:_:%s",
		strings.ToLower(u.Address.Hex()),
		name,
	)
}
func SecretStorageKeyForOrg(u *model.User, org string, name string) string {
	return fmt.Sprintf(
		"secret:%s:%s:_:%s",
		org,
		strings.ToLower(u.Address.Hex()),
		name,
	)
}
func SecretStorageKeyForWorkflow(u *model.User, name string, workflow string) string {
	return fmt.Sprintf(
		"secret:_:%s:%s:%s",
		strings.ToLower(u.Address.Hex()),
		workflow,
		name,
	)
}
func SecretStoragePrefix(u *model.User) string {
	return fmt.Sprintf("secret:_:%s", strings.ToLower(u.Address.Hex()))
}

// A key had this format secret:<org>:0xd7050816337a3f8f690f8083b5ff8019d50c0e50:<workflow-id>:telebot2
// example secret:_:0xd7050816337a3f8f690f8083b5ff8019d50c0e50:_:telebot2            secret for all workflow
// example secret:_:0xd7050816337a3f8f690f8083b5ff8019d50c0e50:workflow1234:telebot2 secret for workflow1234
func SecretNameFromKey(key string) *model.Secret {
	parts := strings.Split(key, ":")
	secretWithNameOnly := &model.Secret{
		Name: parts[4],
	}

	if parts[1] != "_" {
		secretWithNameOnly.OrgID = parts[1]
	}

	if parts[3] != "_" {
		secretWithNameOnly.WorkflowID = parts[3]
	}

	return secretWithNameOnly
}

// ContractWriteCounterKey returns the key for the contract write counter of a given eoa in our kv store
func ContractWriteCounterKey(eoa common.Address) []byte {
	return []byte(fmt.Sprintf("ct:cw:%s", strings.ToLower(eoa.Hex())))
}

// IsWalletHidden checks if a wallet is marked as hidden.
// It returns true if hidden, false otherwise. If the wallet is not found or an error occurs,
// it returns false and the error (except for badger.ErrKeyNotFound where it still returns false for IsHidden).
func IsWalletHidden(db storage.Storage, owner common.Address, smartWalletAddress string) (bool, error) {
	wallet, err := GetWallet(db, owner, smartWalletAddress)
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return false, nil // Wallet doesn't exist, so not hidden by our definition, no error to bubble up for this specific check.
		}
		return false, err // Other error
	}
	if wallet == nil { // Should ideally not happen if err is nil, but as a safeguard
		return false, fmt.Errorf("wallet not found but no error returned for %s", smartWalletAddress)
	}
	return wallet.IsHidden, nil
}

// SetWalletHiddenStatus sets the hidden status of a specified wallet.
func SetWalletHiddenStatus(db storage.Storage, owner common.Address, smartWalletAddress string, hidden bool) error {
	wallet, err := GetWallet(db, owner, smartWalletAddress)
	if err != nil {
		// If wallet not found, and we intend to "unhide" (which is a no-op) or "hide" (which means creating it as hidden).
		// For simplicity, let's assume for now that SetWalletHiddenStatus operates on existing wallets.
		// If the requirement is to create a wallet if it doesn't exist and set its hidden status, this logic needs expansion.
		return fmt.Errorf("failed to get wallet %s to set hidden status: %w", smartWalletAddress, err)
	}
	if wallet == nil {
		return fmt.Errorf("wallet not found for %s but no error on GetWallet", smartWalletAddress)
	}

	if wallet.IsHidden == hidden {
		return nil // No change needed
	}

	wallet.IsHidden = hidden
	return StoreWallet(db, owner, wallet)
}

// Fee ledger storage keys

// FeeLedgerKey returns the key for a user's outstanding value fee balance.
// Format: "fl:<owner_hex>" → JSON-encoded FeeLedgerEntry
func FeeLedgerKey(owner common.Address) []byte {
	return []byte(fmt.Sprintf("fl:%s", strings.ToLower(owner.Hex())))
}

// FeeRecordKey returns the key for an individual fee record (audit trail).
// Format: "fr:<owner_hex>:<execution_id>" → JSON-encoded FeeRecord
func FeeRecordKey(owner common.Address, executionID string) []byte {
	return []byte(fmt.Sprintf("fr:%s:%s", strings.ToLower(owner.Hex()), executionID))
}

// FeeRecordPrefix returns the prefix for all fee records for an owner.
func FeeRecordPrefix(owner common.Address) []byte {
	return []byte(fmt.Sprintf("fr:%s:", strings.ToLower(owner.Hex())))
}
