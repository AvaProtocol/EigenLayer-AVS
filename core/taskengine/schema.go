package taskengine

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
)

func UserTaskStoragePrefix(address common.Address) []byte {
	return []byte(fmt.Sprintf("u:%s", strings.ToLower(address.String())))
}

func SmartWalletTaskStoragePrefix(owner common.Address, smartWalletAddress common.Address) []byte {
	return []byte(fmt.Sprintf("u:%s:%s", strings.ToLower(owner.Hex()), strings.ToLower(smartWalletAddress.Hex())))
}

func TaskByStatusStoragePrefix(status avsproto.TaskStatus) []byte {
	return []byte(fmt.Sprintf("t:%s:", TaskStatusToStorageKey(status)))
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

func TaskStorageKey(id string, status avsproto.TaskStatus) []byte {
	return []byte(fmt.Sprintf(
		"t:%s:%s",
		TaskStatusToStorageKey(status),
		id,
	))
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

func ExecutionIdFromStorageKey(key []byte) string {
	// key layout: history:01JG2FE5MDVKBPHEG0PEYSDKAC:01JG2FE5MFKTH0754RGF2DMVY7
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

// Convert task status gRPC enum into the storage prefix
// c: completed. task is completed and no longer being check for trigger anymore
// f: failed. task is failed to executed, and no longer being check for trigger anymore
// x: executing. task is being execured currently.
// l: cancelled. task is cancelled by user, no longer being check for trigger
// a: actived. task is actived, and will be checked for triggering. task may had executed zero or more time depend on repeatable or not
func TaskStatusToStorageKey(v avsproto.TaskStatus) string {
	switch v {
	case 1:
		return "c"
	case 2:
		return "f"
	case 3:
		return "l"
	case 4:
		return "x"
	}

	return "a"
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

// GetWalletModel retrieves a wallet model from storage.
// It returns badger.ErrKeyNotFound if the wallet is not found.
func GetWalletModel(db storage.Storage, owner common.Address, smartWalletAddress string) (*model.SmartWallet, error) {
	walletKey := WalletStorageKey(owner, smartWalletAddress)
	walletData, err := db.GetKey([]byte(walletKey))
	if err != nil {
		return nil, err // Includes badger.ErrKeyNotFound
	}

	var walletModel model.SmartWallet
	if unmarshalErr := json.Unmarshal(walletData, &walletModel); unmarshalErr != nil {
		return nil, fmt.Errorf("failed to unmarshal wallet data for key %s: %w", walletKey, unmarshalErr)
	}
	return &walletModel, nil
}

// StoreWallet saves a wallet model to storage.
func StoreWallet(db storage.Storage, owner common.Address, wallet *model.SmartWallet) error {
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
