package taskengine

import (
	"fmt"
	"maps"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
)

func LoadSecretForTask(db storage.Storage, task *model.Task) (map[string]string, error) {
	secrets := map[string]string{}

	if task.Owner == "" {
		return nil, fmt.Errorf("missing user in task structure")
	}

	user := &model.User{
		Address: common.HexToAddress(task.Owner),
	}

	prefixes := []string{
		SecretStoragePrefix(user),
	}

	secretKeys, err := db.ListKeysMulti(prefixes)
	if err != nil {
		return nil, err
	}
	// Copy global static secret we loaded from config file.
	maps.Copy(secrets, macroSecrets)

	// Load secret at user level. It has higher priority
	// TODO: load secret at org level first, when we introduce that
	for _, k := range secretKeys {
		secretWithNameOnly := SecretNameFromKey(k)
		if secretWithNameOnly.WorkflowID == "" {
			if value, err := db.GetKey([]byte(k)); err == nil {
				secrets[secretWithNameOnly.Name] = string(value)
			}
		}
	}

	// Now we get secret at workflow level, the lowest level.
	for _, k := range secretKeys {
		secretWithNameOnly := SecretNameFromKey(k)
		if _, ok := secrets[secretWithNameOnly.Name]; ok {
			// Our priority is define in this issue: https://github.com/AvaProtocol/EigenLayer-AVS/issues/104#issue-2793661337
			// Regarding the scope of permissions, the top level permission could always overwrite lower levels. For example, org > user > workflow
			continue
		}

		if secretWithNameOnly.WorkflowID == task.Id {
			if value, err := db.GetKey([]byte(k)); err == nil {
				secrets[secretWithNameOnly.Name] = string(value)
			}
		}
	}

	return secrets, nil
}
