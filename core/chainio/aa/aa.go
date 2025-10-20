package aa

import (
	_ "embed"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	// "github.com/ethereum/go-ethereum/accounts/abi/bind"
)

//go:embed account.abi
var simpleAccountABIJSON string

var (
	factoryABI  abi.ABI
	defaultSalt = big.NewInt(0)

	simpleAccountABI  *abi.ABI
	EntrypointAddress = common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789")
	factoryAddress    common.Address
)

func SetFactoryAddress(address common.Address) {
	factoryAddress = address
}

func SetEntrypointAddress(address common.Address) {
	EntrypointAddress = address
}

func buildFactoryABI() {
	var err error
	factoryABI, err = abi.JSON(strings.NewReader(SimpleFactoryMetaData.ABI))
	if err != nil {
		panic(fmt.Errorf("Invalid factory ABI: %w", err))
	}
}

// Get InitCode returns initcode for a given address with a given salt
func GetInitCode(ownerAddress string, salt *big.Int) (string, error) {
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

func GetSenderAddress(conn *ethclient.Client, ownerAddress common.Address, salt *big.Int) (*common.Address, error) {
	// Check if factory address is set
	if (factoryAddress == common.Address{}) {
		return nil, fmt.Errorf("factory address not initialized - call aa.SetFactoryAddress() first")
	}

	simpleFactory, err := NewSimpleFactory(factoryAddress, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to create factory instance at %s: %w", factoryAddress.Hex(), err)
	}

	sender, err := simpleFactory.GetAddress(nil, ownerAddress, salt)
	if err != nil {
		return nil, fmt.Errorf("failed to derive wallet address from factory %s for owner %s with salt %s: %w",
			factoryAddress.Hex(), ownerAddress.Hex(), salt.String(), err)
	}
	return &sender, err
}

// Compute smart wallet address for a particular factory
func GetSenderAddressForFactory(conn *ethclient.Client, ownerAddress common.Address, customFactoryAddress common.Address, salt *big.Int) (*common.Address, error) {
	simpleFactory, err := NewSimpleFactory(customFactoryAddress, conn)
	if err != nil {
		return nil, err
	}

	sender, err := simpleFactory.GetAddress(nil, ownerAddress, salt)
	return &sender, err
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

// Generate calldata for UserOps
func PackExecute(targetAddress common.Address, ethValue *big.Int, calldata []byte) ([]byte, error) {
	var err error
	if simpleAccountABI == nil {
		simpleAccountABI, err = loadSimpleAccountABI()
		if err != nil {
			return nil, err
		}
	}

	return simpleAccountABI.Pack("execute", targetAddress, ethValue, calldata)
}

// Generate calldata for batch UserOps - executes multiple contract calls in one transaction
func PackExecuteBatch(targetAddresses []common.Address, calldataArray [][]byte) ([]byte, error) {
	var err error
	if simpleAccountABI == nil {
		simpleAccountABI, err = loadSimpleAccountABI()
		if err != nil {
			return nil, err
		}
	}

	return simpleAccountABI.Pack("executeBatch", targetAddresses, calldataArray)
}

// PackExecuteBatchWithValues generates calldata for batch UserOps with ETH values per call
// Supports atomic batching of operations with different ETH amounts (e.g., contract call + paymaster reimbursement)
// Uses manual ABI encoding to bypass Go's ABI encoder bug with empty []byte slices
func PackExecuteBatchWithValues(targetAddresses []common.Address, values []*big.Int, calldataArray [][]byte) ([]byte, error) {
	// Debug logging to track what we're packing
	fmt.Printf("🔍 PackExecuteBatchWithValues INPUT DEBUG:\n")
	fmt.Printf("   Targets: %d\n", len(targetAddresses))
	fmt.Printf("   Values: %d\n", len(values))
	fmt.Printf("   Calldatas: %d\n", len(calldataArray))
	for i, cd := range calldataArray {
		fmt.Printf("   calldataArray[%d]: len=%d, cap=%d, isNil=%v, value=%v\n",
			i, len(cd), cap(cd), cd == nil, cd)
	}

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
		fmt.Printf("   Func[%d] will be at relative offset: %d bytes\n", i, calldataContentStart)
		// Move to next calldata element position
		calldataContentStart += 32 // length field
		if len(calldata) > 0 {
			// Pad to 32-byte boundary
			paddedLength := ((len(calldata) + 31) / 32) * 32
			calldataContentStart += paddedLength
		}
	}

	// Add the offsets (each offset is relative to the start of data section)
	fmt.Printf("   Writing %d offset pointers:\n", len(relativeOffsets))
	for i, offset := range relativeOffsets {
		offsetBytes := common.LeftPadBytes(big.NewInt(int64(offset)).Bytes(), 32)
		fmt.Printf("   Offset[%d]: %d bytes -> %x\n", i, offset, offsetBytes)
		result = append(result, offsetBytes...)
	}

	// Add actual calldata contents
	for i, calldata := range calldataArray {
		lengthBytes := common.LeftPadBytes(big.NewInt(int64(len(calldata))).Bytes(), 32)
		fmt.Printf("   calldataArray[%d] length bytes: %x (length: %d)\n", i, lengthBytes, len(calldata))
		result = append(result, lengthBytes...)

		if len(calldata) > 0 {
			// Pad to 32-byte boundary
			paddedLength := ((len(calldata) + 31) / 32) * 32
			padded := make([]byte, paddedLength)
			copy(padded, calldata)
			result = append(result, padded...)
			fmt.Printf("   calldataArray[%d] data bytes: %x (padded to %d bytes)\n", i, padded, paddedLength)
		} else {
			fmt.Printf("   calldataArray[%d] is empty - no data appended after length\n", i)
		}
	}

	fmt.Printf("   Packed result: %d bytes\n", len(result))
	fmt.Printf("   Full packed calldata: %x\n", result)
	return result, nil
}
