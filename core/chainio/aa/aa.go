package aa

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa/simpleaccount"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	// "github.com/ethereum/go-ethereum/accounts/abi/bind"
)

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
	simpleFactory, err := NewSimpleFactory(factoryAddress, conn)
	if err != nil {
		return nil, err
	}

	sender, err := simpleFactory.GetAddress(nil, ownerAddress, salt)
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

// Generate calldata for UserOps
func PackExecute(targetAddress common.Address, ethValue *big.Int, calldata []byte) ([]byte, error) {
	var err error
	if simpleAccountABI == nil {
		simpleAccountABI, err = simpleaccount.SimpleAccountMetaData.GetAbi()
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
		simpleAccountABI, err = simpleaccount.SimpleAccountMetaData.GetAbi()
		if err != nil {
			return nil, err
		}
	}

	return simpleAccountABI.Pack("executeBatch", targetAddresses, calldataArray)
}
