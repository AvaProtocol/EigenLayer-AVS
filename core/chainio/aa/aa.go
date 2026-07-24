package aa

import (
	_ "embed"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	// "github.com/ethereum/go-ethereum/accounts/abi/bind"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

//go:embed account.abi
var simpleAccountABIJSON string

var (
	factoryABI  abi.ABI
	defaultSalt = big.NewInt(0)

	simpleAccountABI     *abi.ABI
	simpleAccountABIOnce sync.Once
	simpleAccountABIErr  error
	EntrypointAddress    = common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789")

	// factoryAddress is set via SetFactoryAddress() from config
	// It uses the default from config.DefaultFactoryProxyAddressHex if not overridden in YAML
	factoryAddress   common.Address
	factoryAddressMu sync.RWMutex
)

// SetFactoryAddress sets the factory proxy address from config
// This should be called during initialization with the value from config.SmartWallet.FactoryAddress
// which already handles default value and YAML override
func SetFactoryAddress(address common.Address) {
	factoryAddressMu.Lock()
	defer factoryAddressMu.Unlock()
	factoryAddress = address
}

// getFactoryAddress returns the current factory address, with fallback to default from config
func getFactoryAddress() common.Address {
	factoryAddressMu.RLock()
	defer factoryAddressMu.RUnlock()
	if factoryAddress != (common.Address{}) {
		return factoryAddress
	}
	// Fallback to default if not set (shouldn't happen in normal operation)
	// This uses the default from config package, which can be overridden in YAML
	return common.HexToAddress(config.DefaultFactoryProxyAddressHex)
}

func SetEntrypointAddress(address common.Address) {
	EntrypointAddress = address
}

func buildFactoryABI() {
	var err error
	factoryABI, err = abi.JSON(strings.NewReader(SimpleFactoryMetaData.ABI))
	if err != nil {
		panic(fmt.Errorf("invalid factory ABI: %w", err))
	}
}

// GetInitCode returns initcode for a given address with a given salt using the factory address from config
func GetInitCode(ownerAddress string, salt *big.Int) (string, error) {
	return GetInitCodeForFactory(ownerAddress, getFactoryAddress(), salt)
}

// GetInitCodeForFactory returns initcode for a given address with a given salt
// factoryAddress should be the factory proxy address
func GetInitCodeForFactory(ownerAddress string, factoryAddress common.Address, salt *big.Int) (string, error) {
	var err error

	buildFactoryABI()

	var data []byte
	data = append(data, factoryAddress.Bytes()...)

	calldata, err := factoryABI.Pack("createAccount", common.HexToAddress(ownerAddress), salt)

	if err != nil {
		return "", err
	}

	data = append(data, calldata...)

	return hexutil.Encode(data), nil
	//return common.Bytes2Hex(data), nil
}

// computeSmartWalletAddress computes the smart wallet address using the standard CREATE2 formula:
// keccak256(0xff || factoryAddr || salt || keccak256(initCode))[12:]
func computeSmartWalletAddress(factoryAddr common.Address, ownerAddress common.Address, salt *big.Int) (common.Address, error) {
	// Convert salt to bytes32
	saltBytes := make([]byte, 32)
	salt.FillBytes(saltBytes)

	// Get the init code for the wallet (factory + calldata)
	initCodeHex, err := GetInitCodeForFactory(ownerAddress.Hex(), factoryAddr, salt)
	if err != nil {
		return common.Address{}, fmt.Errorf("failed to get init code: %w", err)
	}
	initCodeBytes, err := hexutil.Decode(initCodeHex)
	if err != nil {
		return common.Address{}, fmt.Errorf("failed to decode init code: %w", err)
	}
	initCodeHash := crypto.Keccak256(initCodeBytes)

	// CREATE2 formula: keccak256(0xff || factoryAddr || salt || keccak256(initCode))[12:]
	var b []byte
	b = append(b, 0xff)
	b = append(b, factoryAddr.Bytes()...)
	b = append(b, saltBytes...)
	b = append(b, initCodeHash...)
	hash := crypto.Keccak256(b)

	// Take last 20 bytes for address
	return common.BytesToAddress(hash[12:]), nil
}

// GetSenderAddress is a wrapper that uses the factory address from config
// It calls GetSenderAddressForFactory with the factory address set via SetFactoryAddress()
// which reads from config (with default value and YAML override support)
func GetSenderAddress(conn *ethclient.Client, ownerAddress common.Address, salt *big.Int) (*common.Address, error) {
	return GetSenderAddressForFactory(conn, ownerAddress, getFactoryAddress(), salt)
}

// GetSenderAddressForFactory computes the smart wallet address using the factory proxy address
// Callers should pass the default factory address from config.DefaultFactoryProxyAddressHex
// or a custom factory address if needed.
//
// NOTE: This function always uses the on-chain factory.getAddress() method as the source of truth,
// because the factory contract uses CREATE2 with ERC1967Proxy creation code, which is different
// from the init code format used in UserOps (factory address + calldata). The factory's getAddress
// method correctly calculates the address that will actually be deployed.
func GetSenderAddressForFactory(conn *ethclient.Client, ownerAddress common.Address, factoryAddress common.Address, salt *big.Int) (*common.Address, error) {
	// Always use on-chain factory.getAddress() as the source of truth
	// The factory contract's getAddress method uses CREATE2 with ERC1967Proxy creation code,
	// which is different from the init code format (factory address + calldata) used in UserOps.
	// This ensures we get the correct address that will actually be deployed.
	simpleFactory, err := NewSimpleFactory(factoryAddress, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to create factory instance at %s: %w", factoryAddress.Hex(), err)
	}

	sender, err := simpleFactory.GetAddress(nil, ownerAddress, salt)
	if err != nil {
		return nil, fmt.Errorf("failed to derive wallet address from factory %s for owner %s with salt %s: %w",
			factoryAddress.Hex(), ownerAddress.Hex(), salt.String(), err)
	}
	return &sender, nil
}

func GetNonce(conn *ethclient.Client, ownerAddress common.Address, salt *big.Int) (*big.Int, error) {
	if salt == nil {
		salt = defaultSalt
	}

	entrypoint, err := NewEntryPoint(EntrypointAddress, conn)
	if err != nil {
		return nil, err
	}

	return entrypoint.GetNonce(nil, ownerAddress, salt)
}

func MustNonce(conn *ethclient.Client, ownerAddress common.Address, salt *big.Int) *big.Int {
	nonce, e := GetNonce(conn, ownerAddress, salt)
	if e != nil {
		panic(e)
	}

	return nonce
}

// loadSimpleAccountABI loads the SimpleAccount ABI from embedded JSON
// This ensures we always use the latest ABI file, even if Go bindings are outdated
func loadSimpleAccountABI() (*abi.ABI, error) {
	parsedABI, err := abi.JSON(strings.NewReader(simpleAccountABIJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to parse SimpleAccount ABI: %w", err)
	}
	return &parsedABI, nil
}

// ensureSimpleAccountABI lazily parses and caches the embedded SimpleAccount ABI exactly once.
// The parse is deterministic, so a sync.Once is enough — and it removes the check-then-set data
// race on the package-level simpleAccountABI that the pack/unpack helpers previously shared (now
// hit on every paymaster-sponsored send via UnpackExecuteCalldata, not just batches).
func ensureSimpleAccountABI() (*abi.ABI, error) {
	simpleAccountABIOnce.Do(func() {
		simpleAccountABI, simpleAccountABIErr = loadSimpleAccountABI()
	})
	return simpleAccountABI, simpleAccountABIErr
}

// Generate calldata for UserOps
func PackExecute(targetAddress common.Address, ethValue *big.Int, calldata []byte) ([]byte, error) {
	parsedABI, err := ensureSimpleAccountABI()
	if err != nil {
		return nil, err
	}

	return parsedABI.Pack("execute", targetAddress, ethValue, calldata)
}

// Generate calldata for batch UserOps - executes multiple contract calls in one transaction
func PackExecuteBatch(targetAddresses []common.Address, calldataArray [][]byte) ([]byte, error) {
	parsedABI, err := ensureSimpleAccountABI()
	if err != nil {
		return nil, err
	}

	return parsedABI.Pack("executeBatch", targetAddresses, calldataArray)
}

// PackExecuteBatchWithValues generates calldata for batch UserOps with ETH values per call
// Supports atomic batching of operations with different ETH amounts (e.g., contract call + paymaster reimbursement)
// Uses manual ABI encoding to bypass Go's ABI encoder bug with empty []byte slices
func PackExecuteBatchWithValues(targetAddresses []common.Address, values []*big.Int, calldataArray [][]byte) ([]byte, error) {
	// Manual ABI encoding to bypass Go's ABI encoder bug
	// Function selector: executeBatchWithValues(address[],uint256[],bytes[])
	selector := []byte{0xc3, 0xff, 0x72, 0xfc}

	// Build the calldata manually using a simpler, correct approach
	var result []byte
	result = append(result, selector...)

	// Calculate offsets for dynamic arrays
	// Layout: selector + offset1 + offset2 + offset3 + data1 + data2 + data3
	offset1 := 96                                                      // After selector + 3 offsets (32 bytes each)
	offset2 := 96 + 32 + len(targetAddresses)*32                       // After targets array
	offset3 := 96 + 32 + len(targetAddresses)*32 + 32 + len(values)*32 // After values array

	// Add offsets (32 bytes each)
	result = append(result, common.LeftPadBytes(big.NewInt(int64(offset1)).Bytes(), 32)...)
	result = append(result, common.LeftPadBytes(big.NewInt(int64(offset2)).Bytes(), 32)...)
	result = append(result, common.LeftPadBytes(big.NewInt(int64(offset3)).Bytes(), 32)...)

	// Add targets array
	result = append(result, common.LeftPadBytes(big.NewInt(int64(len(targetAddresses))).Bytes(), 32)...)
	for _, addr := range targetAddresses {
		result = append(result, common.LeftPadBytes(addr.Bytes(), 32)...)
	}

	// Add values array
	result = append(result, common.LeftPadBytes(big.NewInt(int64(len(values))).Bytes(), 32)...)
	for _, value := range values {
		result = append(result, common.LeftPadBytes(value.Bytes(), 32)...)
	}

	// Add calldata array
	result = append(result, common.LeftPadBytes(big.NewInt(int64(len(calldataArray))).Bytes(), 32)...)

	// CRITICAL FIX: Offsets in the func[] array must be RELATIVE to the position right after the array length
	// The func[] array structure is:
	// [array_length (32 bytes)][offset_0 (32 bytes)][offset_1 (32 bytes)]...[data_0][data_1]...
	// Each offset points to the start of its data element, RELATIVE to the position right after the array length
	// This means the offset must account for the space taken by ALL the offset slots themselves

	// Calculate the starting position for the first calldata element
	// This is RELATIVE to the position right after the array length field
	// The data section starts AFTER: array_length (not counted) + all offset slots
	calldataContentStart := len(calldataArray) * 32 // Space for all offset slots (Func[0] starts after these)

	// First, calculate all the relative offsets
	relativeOffsets := make([]int, len(calldataArray))
	for i, calldata := range calldataArray {
		relativeOffsets[i] = calldataContentStart
		// Move to next calldata element position
		calldataContentStart += 32 // length field
		if len(calldata) > 0 {
			// Pad to 32-byte boundary
			paddedLength := ((len(calldata) + 31) / 32) * 32
			calldataContentStart += paddedLength
		}
	}

	// Add the offsets (each offset is relative to the start of data section)
	for _, offset := range relativeOffsets {
		offsetBytes := common.LeftPadBytes(big.NewInt(int64(offset)).Bytes(), 32)
		result = append(result, offsetBytes...)
	}

	// Add actual calldata contents
	for _, calldata := range calldataArray {
		lengthBytes := common.LeftPadBytes(big.NewInt(int64(len(calldata))).Bytes(), 32)
		result = append(result, lengthBytes...)

		if len(calldata) > 0 {
			// Pad to 32-byte boundary
			paddedLength := ((len(calldata) + 31) / 32) * 32
			padded := make([]byte, paddedLength)
			copy(padded, calldata)
			result = append(result, padded...)
		}
	}
	return result, nil
}

// UnpackExecuteCalldata decodes SimpleAccount smart-wallet calldata — produced by PackExecute,
// PackExecuteBatch, or PackExecuteBatchWithValues — back into per-call (target, value, data)
// tuples. It dispatches on the 4-byte function selector via the embedded ABI, so it stays correct
// regardless of which pack helper produced the calldata:
//   - execute(address,uint256,bytes)                  → one entry
//   - executeBatch(address[],bytes[])                 → N entries, all values 0
//   - executeBatchWithValues(address[],uint256[],bytes[]) → N entries with explicit values
//
// It is the inverse the paymaster reimbursement wrapper needs to append its own batch entries onto
// an already-batched call without having to know how that call was originally packed.
func UnpackExecuteCalldata(calldata []byte) (targets []common.Address, values []*big.Int, datas [][]byte, err error) {
	if len(calldata) < 4 {
		return nil, nil, nil, fmt.Errorf("calldata too short: %d bytes", len(calldata))
	}
	parsedABI, err := ensureSimpleAccountABI()
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
		dest, ok := args[0].([]common.Address)
		if !ok {
			return nil, nil, nil, fmt.Errorf("executeBatch: unexpected dest type %T", args[0])
		}
		data, ok := args[1].([][]byte)
		if !ok {
			return nil, nil, nil, fmt.Errorf("executeBatch: unexpected data type %T", args[1])
		}
		if len(dest) != len(data) {
			return nil, nil, nil, fmt.Errorf("executeBatch: dest/func length mismatch (%d vs %d)", len(dest), len(data))
		}
		vals := make([]*big.Int, len(dest))
		for i := range vals {
			vals[i] = big.NewInt(0)
		}
		return dest, vals, data, nil

	case "executeBatchWithValues":
		dest, ok := args[0].([]common.Address)
		if !ok {
			return nil, nil, nil, fmt.Errorf("executeBatchWithValues: unexpected dest type %T", args[0])
		}
		vals, ok := args[1].([]*big.Int)
		if !ok {
			return nil, nil, nil, fmt.Errorf("executeBatchWithValues: unexpected values type %T", args[1])
		}
		data, ok := args[2].([][]byte)
		if !ok {
			return nil, nil, nil, fmt.Errorf("executeBatchWithValues: unexpected data type %T", args[2])
		}
		if len(dest) != len(vals) || len(dest) != len(data) {
			return nil, nil, nil, fmt.Errorf("executeBatchWithValues: array length mismatch (dest=%d values=%d func=%d)", len(dest), len(vals), len(data))
		}
		return dest, vals, data, nil

	default:
		return nil, nil, nil, fmt.Errorf("unsupported smart-wallet method %q for reimbursement wrapping", method.Name)
	}
}
