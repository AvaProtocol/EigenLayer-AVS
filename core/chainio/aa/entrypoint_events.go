package aa

import (
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

// userOperationRevertReasonABIJSON is the EntryPoint event that carries a
// reverted operation's revert data. v0.7 declares it exactly as v0.6 did.
const userOperationRevertReasonABIJSON = `[
  {"type":"event","name":"UserOperationRevertReason","anonymous":false,
   "inputs":[{"name":"userOpHash","type":"bytes32","indexed":true},
             {"name":"sender","type":"address","indexed":true},
             {"name":"nonce","type":"uint256","indexed":false},
             {"name":"revertReason","type":"bytes","indexed":false}]}
]`

var (
	userOperationRevertReasonOnce  sync.Once
	userOperationRevertReasonEvent abi.Event
	userOperationRevertReasonErr   error
)

// UserOperationRevertReasonEvent returns the parsed EntryPoint
// UserOperationRevertReason event.
func UserOperationRevertReasonEvent() (abi.Event, error) {
	userOperationRevertReasonOnce.Do(func() {
		parsed, err := abi.JSON(strings.NewReader(userOperationRevertReasonABIJSON))
		if err != nil {
			userOperationRevertReasonErr = err
			return
		}
		userOperationRevertReasonEvent = parsed.Events["UserOperationRevertReason"]
	})
	return userOperationRevertReasonEvent, userOperationRevertReasonErr
}
