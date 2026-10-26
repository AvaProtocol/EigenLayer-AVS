package aa

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// UnpackExecuteCalldata decodes smart-wallet execution calldata back into
// per-call (target, value, data) tuples. It dispatches on the 4-byte function
// selector, so it stays correct whichever pack helper produced the calldata:
//   - execute(address,uint256,bytes)          → one entry
//   - executeBatch((address,uint256,bytes)[]) → N entries, per-tuple values
//
// Calldata wrapped in executeUserOp is unwrapped first.
func UnpackExecuteCalldata(calldata []byte) (targets []common.Address, values []*big.Int, datas [][]byte, err error) {
	calldata = UnwrapExecuteUserOp(calldata)
	if len(calldata) < 4 {
		return nil, nil, nil, fmt.Errorf("calldata too short: %d bytes", len(calldata))
	}
	parsedABI, err := ensureModularAccountABI()
	if err != nil {
		return nil, nil, nil, err
	}

	selector := calldata[:4]
	method, err := parsedABI.MethodById(selector)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unrecognized smart-wallet method selector 0x%x: %w", selector, err)
	}
	args, err := method.Inputs.Unpack(calldata[4:])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to unpack %s calldata: %w", method.Name, err)
	}

	switch method.Name {
	case "execute":
		dest, ok := args[0].(common.Address)
		if !ok {
			return nil, nil, nil, fmt.Errorf("execute: unexpected dest type %T", args[0])
		}
		value, ok := args[1].(*big.Int)
		if !ok {
			return nil, nil, nil, fmt.Errorf("execute: unexpected value type %T", args[1])
		}
		data, ok := args[2].([]byte)
		if !ok {
			return nil, nil, nil, fmt.Errorf("execute: unexpected data type %T", args[2])
		}
		return []common.Address{dest}, []*big.Int{value}, [][]byte{data}, nil

	case "executeBatch":
		if len(args) != 1 {
			return nil, nil, nil, fmt.Errorf("executeBatch: unexpected argument count %d", len(args))
		}
		calls := *abi.ConvertType(args[0], new([]Call)).(*[]Call)
		targets = make([]common.Address, len(calls))
		values = make([]*big.Int, len(calls))
		datas = make([][]byte, len(calls))
		for i, call := range calls {
			targets[i] = call.Target
			values[i] = call.Value
			datas[i] = call.Data
		}
		return targets, values, datas, nil

	default:
		return nil, nil, nil, fmt.Errorf("unsupported smart-wallet method %q", method.Name)
	}
}
