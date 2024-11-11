package taskengine

import (
	"fmt"
	"strings"

	"github.com/AvaProtocol/ap-avs/model"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
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
