package taskengine

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Chain-scoped key formats:
//
//   t:{chainID}:{status}:{taskID}                — task by status
//   u:{chainID}:{owner}:{smartWallet}:{taskID}   — task by user/wallet
//   history:{chainID}:{taskID}:{executionID}     — execution history
//
// Legacy formats (pre-migration):
//
//   t:{status}:{taskID}
//   u:{owner}:{smartWallet}:{taskID}
//   history:{taskID}:{executionID}
//
// These parsers replace the brittle byte-offset extractors in schema.go and
// transparently handle both formats so callers can migrate one prefix family
// at a time without breaking reads of unmigrated keys.

// ErrLegacyKey indicates the key is in the pre-chain-scoped format. Callers
// that need to distinguish legacy from chain-scoped (e.g., the migration)
// can errors.Is(err, ErrLegacyKey).
var ErrLegacyKey = errors.New("storage key is in legacy (non-chain-scoped) format")

// ParsedTaskStatusKey is the result of parsing a `t:` key.
type ParsedTaskStatusKey struct {
	ChainID int64 // 0 when the key is legacy
	Status  avsproto.TaskStatus
	TaskID  string
}

// ParseTaskStatusKey decodes a `t:` key in either chain-scoped or legacy format.
// Returns ErrLegacyKey when the key has no chain segment; the rest of the result
// is still populated so callers can use it.
func ParseTaskStatusKey(key []byte) (ParsedTaskStatusKey, error) {
	s := string(key)
	if !strings.HasPrefix(s, "t:") {
		return ParsedTaskStatusKey{}, fmt.Errorf("not a task-status key: %s", s)
	}
	parts := strings.SplitN(s, ":", 4)
	switch len(parts) {
	case 3:
		// Legacy: t:{status}:{taskID}
		result := ParsedTaskStatusKey{
			Status: storageKeyToTaskStatus(parts[1]),
			TaskID: parts[2],
		}
		return result, ErrLegacyKey
	case 4:
		// Chain-scoped: t:{chainID}:{status}:{taskID}
		chainID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return ParsedTaskStatusKey{}, fmt.Errorf("invalid chain_id in key %s: %w", s, err)
		}
		return ParsedTaskStatusKey{
			ChainID: chainID,
			Status:  storageKeyToTaskStatus(parts[2]),
			TaskID:  parts[3],
		}, nil
	default:
		return ParsedTaskStatusKey{}, fmt.Errorf("malformed task-status key: %s", s)
	}
}

// ParsedExecutionKey is the result of parsing a `history:` key.
type ParsedExecutionKey struct {
	ChainID     int64 // 0 when the key is legacy
	TaskID      string
	ExecutionID string
}

// ParseExecutionKey decodes a `history:` key in either chain-scoped or legacy format.
func ParseExecutionKey(key []byte) (ParsedExecutionKey, error) {
	s := string(key)
	if !strings.HasPrefix(s, "history:") {
		return ParsedExecutionKey{}, fmt.Errorf("not an execution key: %s", s)
	}
	parts := strings.SplitN(s, ":", 4)
	switch len(parts) {
	case 3:
		// Legacy: history:{taskID}:{executionID}
		return ParsedExecutionKey{
			TaskID:      parts[1],
			ExecutionID: parts[2],
		}, ErrLegacyKey
	case 4:
		// Chain-scoped: history:{chainID}:{taskID}:{executionID}
		chainID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return ParsedExecutionKey{}, fmt.Errorf("invalid chain_id in key %s: %w", s, err)
		}
		return ParsedExecutionKey{
			ChainID:     chainID,
			TaskID:      parts[2],
			ExecutionID: parts[3],
		}, nil
	default:
		return ParsedExecutionKey{}, fmt.Errorf("malformed execution key: %s", s)
	}
}

// ParsedUserTaskKey is the result of parsing a `u:` key.
type ParsedUserTaskKey struct {
	ChainID            int64  // 0 when the key is legacy
	Owner              string // lowercase hex
	SmartWalletAddress string // lowercase hex
	TaskID             string
}

// ParseUserTaskKey decodes a `u:` key in either chain-scoped or legacy format.
//
// Disambiguation: legacy keys have exactly 4 colon-separated parts
// (u:{owner}:{wallet}:{taskID}); chain-scoped keys have 5 parts and the second
// part parses as a decimal integer. A legacy key whose owner happens to be a
// decimal-only string would be ambiguous, but EOA addresses are hex with a
// `0x` prefix so this cannot collide in practice.
func ParseUserTaskKey(key []byte) (ParsedUserTaskKey, error) {
	s := string(key)
	if !strings.HasPrefix(s, "u:") {
		return ParsedUserTaskKey{}, fmt.Errorf("not a user-task key: %s", s)
	}
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 4:
		// Legacy: u:{owner}:{wallet}:{taskID}
		return ParsedUserTaskKey{
			Owner:              parts[1],
			SmartWalletAddress: parts[2],
			TaskID:             parts[3],
		}, ErrLegacyKey
	case 5:
		// Chain-scoped: u:{chainID}:{owner}:{wallet}:{taskID}
		chainID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return ParsedUserTaskKey{}, fmt.Errorf("invalid chain_id in key %s: %w", s, err)
		}
		return ParsedUserTaskKey{
			ChainID:            chainID,
			Owner:              parts[2],
			SmartWalletAddress: parts[3],
			TaskID:             parts[4],
		}, nil
	default:
		return ParsedUserTaskKey{}, fmt.Errorf("malformed user-task key: %s", s)
	}
}

// IsChainScopedKey returns true when the key already carries a chain_id prefix.
// Used by the migration to skip keys it has already rewritten.
func IsChainScopedKey(key []byte) bool {
	s := string(key)
	switch {
	case strings.HasPrefix(s, "t:"):
		return strings.Count(s, ":") >= 3
	case strings.HasPrefix(s, "u:"):
		return strings.Count(s, ":") >= 4
	case strings.HasPrefix(s, "history:"):
		return strings.Count(s, ":") >= 3
	default:
		return false
	}
}

// storageKeyToTaskStatus is the inverse of schema.WorkflowStatusToStorageKey.
func storageKeyToTaskStatus(s string) avsproto.TaskStatus {
	switch s {
	case "c":
		return avsproto.TaskStatus_Completed
	case "f":
		return avsproto.TaskStatus_Failed
	case "i":
		return avsproto.TaskStatus_Disabled
	case "x":
		return avsproto.TaskStatus_Running
	case "a":
		return avsproto.TaskStatus_Enabled
	default:
		return avsproto.TaskStatus_Enabled
	}
}
