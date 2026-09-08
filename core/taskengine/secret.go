package taskengine

import (
	"fmt"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Platform secret names the engine may read internally (BalanceNode,
// restApi options.auth.provider=moralis / goplus) but must NEVER copy
// into apContext.configVars. restApi / customCode templates would
// otherwise let a workflow spend or exfiltrate the platform keys.
//
// This is a denylist: a new macros.secrets credential that is engine-only
// MUST be added here, or it fails open into configVars. Notify tokens
// (sendgrid, telegram) stay interpolable on purpose.
const (
	platformSecretMoralisAPIKey   = "moralis_api_key"
	platformSecretGoplusAppKey    = "goplus_app_key"
	platformSecretGoplusAppSecret = "goplus_app_secret"
)

var platformSecretNames = map[string]struct{}{
	platformSecretMoralisAPIKey:   {},
	platformSecretGoplusAppKey:    {},
	platformSecretGoplusAppSecret: {},
}

// isPlatformSecretName is case-insensitive, matching the ap_ prefix rule
// in CreateSecret. Map keys are stored lowercase.
func isPlatformSecretName(name string) bool {
	_, ok := platformSecretNames[strings.ToLower(name)]
	return ok
}

// rejectReservedSecretName refuses user/workflow secrets that share a
// platform key name. Those names are omitted from apContext.configVars, so
// a write would succeed and then silently fail to interpolate.
func rejectReservedSecretName(name string) error {
	if isPlatformSecretName(name) {
		return status.Errorf(codes.InvalidArgument, "secret name %q is reserved for platform use", name)
	}
	return nil
}

func LoadSecretForTask(db storage.Storage, task *model.Workflow) (map[string]string, error) {
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
	copyMap(secrets, macroSecrets)

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

// copyMap is a replacement for maps.Copy for Go 1.18.1 compatibility
func copyMap(dst, src map[string]string) {
	for k, v := range src {
		dst[k] = v
	}
}
