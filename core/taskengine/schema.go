package taskengine

import (
	"fmt"
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

func StoreWallet(db storage.Storage, owner common.Address, wallet *model.SmartWallet) error {
	if wallet.Address == nil {
		return fmt.Errorf("cannot store wallet with nil address")
	}
	walletKey := WalletStorageKey(owner, wallet.Address.String())
	updatedWalletData, err := wallet.ToJSON()
	if err != nil {
		return fmt.Errorf("failed to marshal wallet for storage (key: %s): %w", walletKey, err)
	}
	return db.Set([]byte(walletKey), updatedWalletData)
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
