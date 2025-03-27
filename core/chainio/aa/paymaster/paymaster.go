// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package paymaster

import (
	"errors"
	"math/big"
	"strings"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
)

// Reference imports to suppress errors if they are not otherwise used.
var (
	_ = errors.New
	_ = big.NewInt
	_ = strings.NewReader
	_ = ethereum.NotFound
	_ = bind.Bind
	_ = common.Big1
	_ = types.BloomLookup
	_ = event.NewSubscription
	_ = abi.ConvertType
)

// UserOperation is an auto generated low-level Go binding around an user-defined struct.
type UserOperation struct {
	Sender               common.Address
	Nonce                *big.Int
	InitCode             []byte
	CallData             []byte
	CallGasLimit         *big.Int
	VerificationGasLimit *big.Int
	PreVerificationGas   *big.Int
	MaxFeePerGas         *big.Int
	MaxPriorityFeePerGas *big.Int
	PaymasterAndData     []byte
	Signature            []byte
}

// PayMasterMetaData contains all meta data concerning the PayMaster contract.
var PayMasterMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"contractIEntryPoint\",\"name\":\"_entryPoint\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"_verifyingSigner\",\"type\":\"address\"}],\"stateMutability\":\"nonpayable\",\"type\":\"constructor\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"previousOwner\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"OwnershipTransferred\",\"type\":\"event\"},{\"inputs\":[{\"internalType\":\"uint32\",\"name\":\"unstakeDelaySec\",\"type\":\"uint32\"}],\"name\":\"addStake\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"deposit\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"entryPoint\",\"outputs\":[{\"internalType\":\"contractIEntryPoint\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"getDeposit\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"sender\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"initCode\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"callData\",\"type\":\"bytes\"},{\"internalType\":\"uint256\",\"name\":\"callGasLimit\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"verificationGasLimit\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"preVerificationGas\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"maxFeePerGas\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"maxPriorityFeePerGas\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"paymasterAndData\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"signature\",\"type\":\"bytes\"}],\"internalType\":\"structUserOperation\",\"name\":\"userOp\",\"type\":\"tuple\"},{\"internalType\":\"uint48\",\"name\":\"validUntil\",\"type\":\"uint48\"},{\"internalType\":\"uint48\",\"name\":\"validAfter\",\"type\":\"uint48\"}],\"name\":\"getHash\",\"outputs\":[{\"internalType\":\"bytes32\",\"name\":\"\",\"type\":\"bytes32\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"owner\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"bytes\",\"name\":\"paymasterAndData\",\"type\":\"bytes\"}],\"name\":\"parsePaymasterAndData\",\"outputs\":[{\"internalType\":\"uint48\",\"name\":\"validUntil\",\"type\":\"uint48\"},{\"internalType\":\"uint48\",\"name\":\"validAfter\",\"type\":\"uint48\"},{\"internalType\":\"bytes\",\"name\":\"signature\",\"type\":\"bytes\"}],\"stateMutability\":\"pure\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"enumIPaymaster.PostOpMode\",\"name\":\"mode\",\"type\":\"uint8\"},{\"internalType\":\"bytes\",\"name\":\"context\",\"type\":\"bytes\"},{\"internalType\":\"uint256\",\"name\":\"actualGasCost\",\"type\":\"uint256\"}],\"name\":\"postOp\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"renounceOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"name\":\"senderNonce\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"transferOwnership\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"unlockStake\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"components\":[{\"internalType\":\"address\",\"name\":\"sender\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"nonce\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"initCode\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"callData\",\"type\":\"bytes\"},{\"internalType\":\"uint256\",\"name\":\"callGasLimit\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"verificationGasLimit\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"preVerificationGas\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"maxFeePerGas\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"maxPriorityFeePerGas\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"paymasterAndData\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"signature\",\"type\":\"bytes\"}],\"internalType\":\"structUserOperation\",\"name\":\"userOp\",\"type\":\"tuple\"},{\"internalType\":\"bytes32\",\"name\":\"userOpHash\",\"type\":\"bytes32\"},{\"internalType\":\"uint256\",\"name\":\"maxCost\",\"type\":\"uint256\"}],\"name\":\"validatePaymasterUserOp\",\"outputs\":[{\"internalType\":\"bytes\",\"name\":\"context\",\"type\":\"bytes\"},{\"internalType\":\"uint256\",\"name\":\"validationData\",\"type\":\"uint256\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"verifyingSigner\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"addresspayable\",\"name\":\"withdrawAddress\",\"type\":\"address\"}],\"name\":\"withdrawStake\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"addresspayable\",\"name\":\"withdrawAddress\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"withdrawTo\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]",
}

// PayMasterABI is the input ABI used to generate the binding from.
// Deprecated: Use PayMasterMetaData.ABI instead.
var PayMasterABI = PayMasterMetaData.ABI

// PayMaster is an auto generated Go binding around an Ethereum contract.
type PayMaster struct {
	PayMasterCaller     // Read-only binding to the contract
	PayMasterTransactor // Write-only binding to the contract
	PayMasterFilterer   // Log filterer for contract events
}

// PayMasterCaller is an auto generated read-only Go binding around an Ethereum contract.
type PayMasterCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// PayMasterTransactor is an auto generated write-only Go binding around an Ethereum contract.
type PayMasterTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// PayMasterFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type PayMasterFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// PayMasterSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type PayMasterSession struct {
	Contract     *PayMaster        // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// PayMasterCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type PayMasterCallerSession struct {
	Contract *PayMasterCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts    // Call options to use throughout this session
}

// PayMasterTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type PayMasterTransactorSession struct {
	Contract     *PayMasterTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts    // Transaction auth options to use throughout this session
}

// PayMasterRaw is an auto generated low-level Go binding around an Ethereum contract.
type PayMasterRaw struct {
	Contract *PayMaster // Generic contract binding to access the raw methods on
}

// PayMasterCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type PayMasterCallerRaw struct {
	Contract *PayMasterCaller // Generic read-only contract binding to access the raw methods on
}

// PayMasterTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type PayMasterTransactorRaw struct {
	Contract *PayMasterTransactor // Generic write-only contract binding to access the raw methods on
}

// NewPayMaster creates a new instance of PayMaster, bound to a specific deployed contract.
func NewPayMaster(address common.Address, backend bind.ContractBackend) (*PayMaster, error) {
	contract, err := bindPayMaster(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &PayMaster{PayMasterCaller: PayMasterCaller{contract: contract}, PayMasterTransactor: PayMasterTransactor{contract: contract}, PayMasterFilterer: PayMasterFilterer{contract: contract}}, nil
}

// NewPayMasterCaller creates a new read-only instance of PayMaster, bound to a specific deployed contract.
func NewPayMasterCaller(address common.Address, caller bind.ContractCaller) (*PayMasterCaller, error) {
	contract, err := bindPayMaster(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &PayMasterCaller{contract: contract}, nil
}

// NewPayMasterTransactor creates a new write-only instance of PayMaster, bound to a specific deployed contract.
func NewPayMasterTransactor(address common.Address, transactor bind.ContractTransactor) (*PayMasterTransactor, error) {
	contract, err := bindPayMaster(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &PayMasterTransactor{contract: contract}, nil
}

// NewPayMasterFilterer creates a new log filterer instance of PayMaster, bound to a specific deployed contract.
func NewPayMasterFilterer(address common.Address, filterer bind.ContractFilterer) (*PayMasterFilterer, error) {
	contract, err := bindPayMaster(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &PayMasterFilterer{contract: contract}, nil
}

// bindPayMaster binds a generic wrapper to an already deployed contract.
func bindPayMaster(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := PayMasterMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_PayMaster *PayMasterRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _PayMaster.Contract.PayMasterCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_PayMaster *PayMasterRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _PayMaster.Contract.PayMasterTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_PayMaster *PayMasterRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _PayMaster.Contract.PayMasterTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_PayMaster *PayMasterCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _PayMaster.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_PayMaster *PayMasterTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _PayMaster.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_PayMaster *PayMasterTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _PayMaster.Contract.contract.Transact(opts, method, params...)
}

// EntryPoint is a free data retrieval call binding the contract method 0xb0d691fe.
//
// Solidity: function entryPoint() view returns(address)
func (_PayMaster *PayMasterCaller) EntryPoint(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _PayMaster.contract.Call(opts, &out, "entryPoint")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// EntryPoint is a free data retrieval call binding the contract method 0xb0d691fe.
//
// Solidity: function entryPoint() view returns(address)
func (_PayMaster *PayMasterSession) EntryPoint() (common.Address, error) {
	return _PayMaster.Contract.EntryPoint(&_PayMaster.CallOpts)
}

// EntryPoint is a free data retrieval call binding the contract method 0xb0d691fe.
//
// Solidity: function entryPoint() view returns(address)
func (_PayMaster *PayMasterCallerSession) EntryPoint() (common.Address, error) {
	return _PayMaster.Contract.EntryPoint(&_PayMaster.CallOpts)
}

// GetDeposit is a free data retrieval call binding the contract method 0xc399ec88.
//
// Solidity: function getDeposit() view returns(uint256)
func (_PayMaster *PayMasterCaller) GetDeposit(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _PayMaster.contract.Call(opts, &out, "getDeposit")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// GetDeposit is a free data retrieval call binding the contract method 0xc399ec88.
//
// Solidity: function getDeposit() view returns(uint256)
func (_PayMaster *PayMasterSession) GetDeposit() (*big.Int, error) {
	return _PayMaster.Contract.GetDeposit(&_PayMaster.CallOpts)
}

// GetDeposit is a free data retrieval call binding the contract method 0xc399ec88.
//
// Solidity: function getDeposit() view returns(uint256)
func (_PayMaster *PayMasterCallerSession) GetDeposit() (*big.Int, error) {
	return _PayMaster.Contract.GetDeposit(&_PayMaster.CallOpts)
}

// GetHash is a free data retrieval call binding the contract method 0x94e1fc19.
//
// Solidity: function getHash((address,uint256,bytes,bytes,uint256,uint256,uint256,uint256,uint256,bytes,bytes) userOp, uint48 validUntil, uint48 validAfter) view returns(bytes32)
func (_PayMaster *PayMasterCaller) GetHash(opts *bind.CallOpts, userOp UserOperation, validUntil *big.Int, validAfter *big.Int) ([32]byte, error) {
	var out []interface{}
	err := _PayMaster.contract.Call(opts, &out, "getHash", userOp, validUntil, validAfter)

	if err != nil {
		return *new([32]byte), err
	}

	out0 := *abi.ConvertType(out[0], new([32]byte)).(*[32]byte)

	return out0, err

}

// GetHash is a free data retrieval call binding the contract method 0x94e1fc19.
//
// Solidity: function getHash((address,uint256,bytes,bytes,uint256,uint256,uint256,uint256,uint256,bytes,bytes) userOp, uint48 validUntil, uint48 validAfter) view returns(bytes32)
func (_PayMaster *PayMasterSession) GetHash(userOp UserOperation, validUntil *big.Int, validAfter *big.Int) ([32]byte, error) {
	return _PayMaster.Contract.GetHash(&_PayMaster.CallOpts, userOp, validUntil, validAfter)
}

// GetHash is a free data retrieval call binding the contract method 0x94e1fc19.
//
// Solidity: function getHash((address,uint256,bytes,bytes,uint256,uint256,uint256,uint256,uint256,bytes,bytes) userOp, uint48 validUntil, uint48 validAfter) view returns(bytes32)
func (_PayMaster *PayMasterCallerSession) GetHash(userOp UserOperation, validUntil *big.Int, validAfter *big.Int) ([32]byte, error) {
	return _PayMaster.Contract.GetHash(&_PayMaster.CallOpts, userOp, validUntil, validAfter)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_PayMaster *PayMasterCaller) Owner(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _PayMaster.contract.Call(opts, &out, "owner")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_PayMaster *PayMasterSession) Owner() (common.Address, error) {
	return _PayMaster.Contract.Owner(&_PayMaster.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_PayMaster *PayMasterCallerSession) Owner() (common.Address, error) {
	return _PayMaster.Contract.Owner(&_PayMaster.CallOpts)
}

// ParsePaymasterAndData is a free data retrieval call binding the contract method 0x94d4ad60.
//
// Solidity: function parsePaymasterAndData(bytes paymasterAndData) pure returns(uint48 validUntil, uint48 validAfter, bytes signature)
func (_PayMaster *PayMasterCaller) ParsePaymasterAndData(opts *bind.CallOpts, paymasterAndData []byte) (struct {
	ValidUntil *big.Int
	ValidAfter *big.Int
	Signature  []byte
}, error) {
	var out []interface{}
	err := _PayMaster.contract.Call(opts, &out, "parsePaymasterAndData", paymasterAndData)

	outstruct := new(struct {
		ValidUntil *big.Int
		ValidAfter *big.Int
		Signature  []byte
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.ValidUntil = *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)
	outstruct.ValidAfter = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)
	outstruct.Signature = *abi.ConvertType(out[2], new([]byte)).(*[]byte)

	return *outstruct, err

}

// ParsePaymasterAndData is a free data retrieval call binding the contract method 0x94d4ad60.
//
// Solidity: function parsePaymasterAndData(bytes paymasterAndData) pure returns(uint48 validUntil, uint48 validAfter, bytes signature)
func (_PayMaster *PayMasterSession) ParsePaymasterAndData(paymasterAndData []byte) (struct {
	ValidUntil *big.Int
	ValidAfter *big.Int
	Signature  []byte
}, error) {
	return _PayMaster.Contract.ParsePaymasterAndData(&_PayMaster.CallOpts, paymasterAndData)
}

// ParsePaymasterAndData is a free data retrieval call binding the contract method 0x94d4ad60.
//
// Solidity: function parsePaymasterAndData(bytes paymasterAndData) pure returns(uint48 validUntil, uint48 validAfter, bytes signature)
func (_PayMaster *PayMasterCallerSession) ParsePaymasterAndData(paymasterAndData []byte) (struct {
	ValidUntil *big.Int
	ValidAfter *big.Int
	Signature  []byte
}, error) {
	return _PayMaster.Contract.ParsePaymasterAndData(&_PayMaster.CallOpts, paymasterAndData)
}

// SenderNonce is a free data retrieval call binding the contract method 0x9c90b443.
//
// Solidity: function senderNonce(address ) view returns(uint256)
func (_PayMaster *PayMasterCaller) SenderNonce(opts *bind.CallOpts, arg0 common.Address) (*big.Int, error) {
	var out []interface{}
	err := _PayMaster.contract.Call(opts, &out, "senderNonce", arg0)

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// SenderNonce is a free data retrieval call binding the contract method 0x9c90b443.
//
// Solidity: function senderNonce(address ) view returns(uint256)
func (_PayMaster *PayMasterSession) SenderNonce(arg0 common.Address) (*big.Int, error) {
	return _PayMaster.Contract.SenderNonce(&_PayMaster.CallOpts, arg0)
}

// SenderNonce is a free data retrieval call binding the contract method 0x9c90b443.
//
// Solidity: function senderNonce(address ) view returns(uint256)
func (_PayMaster *PayMasterCallerSession) SenderNonce(arg0 common.Address) (*big.Int, error) {
	return _PayMaster.Contract.SenderNonce(&_PayMaster.CallOpts, arg0)
}

// VerifyingSigner is a free data retrieval call binding the contract method 0x23d9ac9b.
//
// Solidity: function verifyingSigner() view returns(address)
func (_PayMaster *PayMasterCaller) VerifyingSigner(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _PayMaster.contract.Call(opts, &out, "verifyingSigner")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// VerifyingSigner is a free data retrieval call binding the contract method 0x23d9ac9b.
//
// Solidity: function verifyingSigner() view returns(address)
func (_PayMaster *PayMasterSession) VerifyingSigner() (common.Address, error) {
	return _PayMaster.Contract.VerifyingSigner(&_PayMaster.CallOpts)
}

// VerifyingSigner is a free data retrieval call binding the contract method 0x23d9ac9b.
//
// Solidity: function verifyingSigner() view returns(address)
func (_PayMaster *PayMasterCallerSession) VerifyingSigner() (common.Address, error) {
	return _PayMaster.Contract.VerifyingSigner(&_PayMaster.CallOpts)
}

// AddStake is a paid mutator transaction binding the contract method 0x0396cb60.
//
// Solidity: function addStake(uint32 unstakeDelaySec) payable returns()
func (_PayMaster *PayMasterTransactor) AddStake(opts *bind.TransactOpts, unstakeDelaySec uint32) (*types.Transaction, error) {
	return _PayMaster.contract.Transact(opts, "addStake", unstakeDelaySec)
}

// AddStake is a paid mutator transaction binding the contract method 0x0396cb60.
//
// Solidity: function addStake(uint32 unstakeDelaySec) payable returns()
func (_PayMaster *PayMasterSession) AddStake(unstakeDelaySec uint32) (*types.Transaction, error) {
	return _PayMaster.Contract.AddStake(&_PayMaster.TransactOpts, unstakeDelaySec)
}

// AddStake is a paid mutator transaction binding the contract method 0x0396cb60.
//
// Solidity: function addStake(uint32 unstakeDelaySec) payable returns()
func (_PayMaster *PayMasterTransactorSession) AddStake(unstakeDelaySec uint32) (*types.Transaction, error) {
	return _PayMaster.Contract.AddStake(&_PayMaster.TransactOpts, unstakeDelaySec)
}

// Deposit is a paid mutator transaction binding the contract method 0xd0e30db0.
//
// Solidity: function deposit() payable returns()
func (_PayMaster *PayMasterTransactor) Deposit(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _PayMaster.contract.Transact(opts, "deposit")
}

// Deposit is a paid mutator transaction binding the contract method 0xd0e30db0.
//
// Solidity: function deposit() payable returns()
func (_PayMaster *PayMasterSession) Deposit() (*types.Transaction, error) {
	return _PayMaster.Contract.Deposit(&_PayMaster.TransactOpts)
}

// Deposit is a paid mutator transaction binding the contract method 0xd0e30db0.
//
// Solidity: function deposit() payable returns()
func (_PayMaster *PayMasterTransactorSession) Deposit() (*types.Transaction, error) {
	return _PayMaster.Contract.Deposit(&_PayMaster.TransactOpts)
}

// PostOp is a paid mutator transaction binding the contract method 0xa9a23409.
//
// Solidity: function postOp(uint8 mode, bytes context, uint256 actualGasCost) returns()
func (_PayMaster *PayMasterTransactor) PostOp(opts *bind.TransactOpts, mode uint8, context []byte, actualGasCost *big.Int) (*types.Transaction, error) {
	return _PayMaster.contract.Transact(opts, "postOp", mode, context, actualGasCost)
}

// PostOp is a paid mutator transaction binding the contract method 0xa9a23409.
//
// Solidity: function postOp(uint8 mode, bytes context, uint256 actualGasCost) returns()
func (_PayMaster *PayMasterSession) PostOp(mode uint8, context []byte, actualGasCost *big.Int) (*types.Transaction, error) {
	return _PayMaster.Contract.PostOp(&_PayMaster.TransactOpts, mode, context, actualGasCost)
}

// PostOp is a paid mutator transaction binding the contract method 0xa9a23409.
//
// Solidity: function postOp(uint8 mode, bytes context, uint256 actualGasCost) returns()
func (_PayMaster *PayMasterTransactorSession) PostOp(mode uint8, context []byte, actualGasCost *big.Int) (*types.Transaction, error) {
	return _PayMaster.Contract.PostOp(&_PayMaster.TransactOpts, mode, context, actualGasCost)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_PayMaster *PayMasterTransactor) RenounceOwnership(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _PayMaster.contract.Transact(opts, "renounceOwnership")
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_PayMaster *PayMasterSession) RenounceOwnership() (*types.Transaction, error) {
	return _PayMaster.Contract.RenounceOwnership(&_PayMaster.TransactOpts)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_PayMaster *PayMasterTransactorSession) RenounceOwnership() (*types.Transaction, error) {
	return _PayMaster.Contract.RenounceOwnership(&_PayMaster.TransactOpts)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_PayMaster *PayMasterTransactor) TransferOwnership(opts *bind.TransactOpts, newOwner common.Address) (*types.Transaction, error) {
	return _PayMaster.contract.Transact(opts, "transferOwnership", newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_PayMaster *PayMasterSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _PayMaster.Contract.TransferOwnership(&_PayMaster.TransactOpts, newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_PayMaster *PayMasterTransactorSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _PayMaster.Contract.TransferOwnership(&_PayMaster.TransactOpts, newOwner)
}

// UnlockStake is a paid mutator transaction binding the contract method 0xbb9fe6bf.
//
// Solidity: function unlockStake() returns()
func (_PayMaster *PayMasterTransactor) UnlockStake(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _PayMaster.contract.Transact(opts, "unlockStake")
}

// UnlockStake is a paid mutator transaction binding the contract method 0xbb9fe6bf.
//
// Solidity: function unlockStake() returns()
func (_PayMaster *PayMasterSession) UnlockStake() (*types.Transaction, error) {
	return _PayMaster.Contract.UnlockStake(&_PayMaster.TransactOpts)
}

// UnlockStake is a paid mutator transaction binding the contract method 0xbb9fe6bf.
//
// Solidity: function unlockStake() returns()
func (_PayMaster *PayMasterTransactorSession) UnlockStake() (*types.Transaction, error) {
	return _PayMaster.Contract.UnlockStake(&_PayMaster.TransactOpts)
}

// ValidatePaymasterUserOp is a paid mutator transaction binding the contract method 0xf465c77e.
//
// Solidity: function validatePaymasterUserOp((address,uint256,bytes,bytes,uint256,uint256,uint256,uint256,uint256,bytes,bytes) userOp, bytes32 userOpHash, uint256 maxCost) returns(bytes context, uint256 validationData)
func (_PayMaster *PayMasterTransactor) ValidatePaymasterUserOp(opts *bind.TransactOpts, userOp UserOperation, userOpHash [32]byte, maxCost *big.Int) (*types.Transaction, error) {
	return _PayMaster.contract.Transact(opts, "validatePaymasterUserOp", userOp, userOpHash, maxCost)
}

// ValidatePaymasterUserOp is a paid mutator transaction binding the contract method 0xf465c77e.
//
// Solidity: function validatePaymasterUserOp((address,uint256,bytes,bytes,uint256,uint256,uint256,uint256,uint256,bytes,bytes) userOp, bytes32 userOpHash, uint256 maxCost) returns(bytes context, uint256 validationData)
func (_PayMaster *PayMasterSession) ValidatePaymasterUserOp(userOp UserOperation, userOpHash [32]byte, maxCost *big.Int) (*types.Transaction, error) {
	return _PayMaster.Contract.ValidatePaymasterUserOp(&_PayMaster.TransactOpts, userOp, userOpHash, maxCost)
}

// ValidatePaymasterUserOp is a paid mutator transaction binding the contract method 0xf465c77e.
//
// Solidity: function validatePaymasterUserOp((address,uint256,bytes,bytes,uint256,uint256,uint256,uint256,uint256,bytes,bytes) userOp, bytes32 userOpHash, uint256 maxCost) returns(bytes context, uint256 validationData)
func (_PayMaster *PayMasterTransactorSession) ValidatePaymasterUserOp(userOp UserOperation, userOpHash [32]byte, maxCost *big.Int) (*types.Transaction, error) {
	return _PayMaster.Contract.ValidatePaymasterUserOp(&_PayMaster.TransactOpts, userOp, userOpHash, maxCost)
}

// WithdrawStake is a paid mutator transaction binding the contract method 0xc23a5cea.
//
// Solidity: function withdrawStake(address withdrawAddress) returns()
func (_PayMaster *PayMasterTransactor) WithdrawStake(opts *bind.TransactOpts, withdrawAddress common.Address) (*types.Transaction, error) {
	return _PayMaster.contract.Transact(opts, "withdrawStake", withdrawAddress)
}

// WithdrawStake is a paid mutator transaction binding the contract method 0xc23a5cea.
//
// Solidity: function withdrawStake(address withdrawAddress) returns()
func (_PayMaster *PayMasterSession) WithdrawStake(withdrawAddress common.Address) (*types.Transaction, error) {
	return _PayMaster.Contract.WithdrawStake(&_PayMaster.TransactOpts, withdrawAddress)
}

// WithdrawStake is a paid mutator transaction binding the contract method 0xc23a5cea.
//
// Solidity: function withdrawStake(address withdrawAddress) returns()
func (_PayMaster *PayMasterTransactorSession) WithdrawStake(withdrawAddress common.Address) (*types.Transaction, error) {
	return _PayMaster.Contract.WithdrawStake(&_PayMaster.TransactOpts, withdrawAddress)
}

// WithdrawTo is a paid mutator transaction binding the contract method 0x205c2878.
//
// Solidity: function withdrawTo(address withdrawAddress, uint256 amount) returns()
func (_PayMaster *PayMasterTransactor) WithdrawTo(opts *bind.TransactOpts, withdrawAddress common.Address, amount *big.Int) (*types.Transaction, error) {
	return _PayMaster.contract.Transact(opts, "withdrawTo", withdrawAddress, amount)
}

// WithdrawTo is a paid mutator transaction binding the contract method 0x205c2878.
//
// Solidity: function withdrawTo(address withdrawAddress, uint256 amount) returns()
func (_PayMaster *PayMasterSession) WithdrawTo(withdrawAddress common.Address, amount *big.Int) (*types.Transaction, error) {
	return _PayMaster.Contract.WithdrawTo(&_PayMaster.TransactOpts, withdrawAddress, amount)
}

// WithdrawTo is a paid mutator transaction binding the contract method 0x205c2878.
//
// Solidity: function withdrawTo(address withdrawAddress, uint256 amount) returns()
func (_PayMaster *PayMasterTransactorSession) WithdrawTo(withdrawAddress common.Address, amount *big.Int) (*types.Transaction, error) {
	return _PayMaster.Contract.WithdrawTo(&_PayMaster.TransactOpts, withdrawAddress, amount)
}

// PayMasterOwnershipTransferredIterator is returned from FilterOwnershipTransferred and is used to iterate over the raw logs and unpacked data for OwnershipTransferred events raised by the PayMaster contract.
type PayMasterOwnershipTransferredIterator struct {
	Event *PayMasterOwnershipTransferred // Event containing the contract specifics and raw log

	contract *bind.BoundContract // Generic contract to use for unpacking event data
	event    string              // Event name to use for unpacking event data

	logs chan types.Log        // Log channel receiving the found contract events
	sub  ethereum.Subscription // Subscription for errors, completion and termination
	done bool                  // Whether the subscription completed delivering logs
	fail error                 // Occurred error to stop iteration
}

// Next advances the iterator to the subsequent event, returning whether there
// are any more events found. In case of a retrieval or parsing error, false is
// returned and Error() can be queried for the exact failure.
func (it *PayMasterOwnershipTransferredIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(PayMasterOwnershipTransferred)
			if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
				it.fail = err
				return false
			}
			it.Event.Raw = log
			return true

		default:
			return false
		}
	}
	// Iterator still in progress, wait for either a data or an error event
	select {
	case log := <-it.logs:
		it.Event = new(PayMasterOwnershipTransferred)
		if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
			it.fail = err
			return false
		}
		it.Event.Raw = log
		return true

	case err := <-it.sub.Err():
		it.done = true
		it.fail = err
		return it.Next()
	}
}

// Error returns any retrieval or parsing error occurred during filtering.
func (it *PayMasterOwnershipTransferredIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *PayMasterOwnershipTransferredIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// PayMasterOwnershipTransferred represents a OwnershipTransferred event raised by the PayMaster contract.
type PayMasterOwnershipTransferred struct {
	PreviousOwner common.Address
	NewOwner      common.Address
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterOwnershipTransferred is a free log retrieval operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_PayMaster *PayMasterFilterer) FilterOwnershipTransferred(opts *bind.FilterOpts, previousOwner []common.Address, newOwner []common.Address) (*PayMasterOwnershipTransferredIterator, error) {

	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _PayMaster.contract.FilterLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return &PayMasterOwnershipTransferredIterator{contract: _PayMaster.contract, event: "OwnershipTransferred", logs: logs, sub: sub}, nil
}

// WatchOwnershipTransferred is a free log subscription operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_PayMaster *PayMasterFilterer) WatchOwnershipTransferred(opts *bind.WatchOpts, sink chan<- *PayMasterOwnershipTransferred, previousOwner []common.Address, newOwner []common.Address) (event.Subscription, error) {

	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _PayMaster.contract.WatchLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(PayMasterOwnershipTransferred)
				if err := _PayMaster.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
					return err
				}
				event.Raw = log

				select {
				case sink <- event:
				case err := <-sub.Err():
					return err
				case <-quit:
					return nil
				}
			case err := <-sub.Err():
				return err
			case <-quit:
				return nil
			}
		}
	}), nil
}

// ParseOwnershipTransferred is a log parse operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_PayMaster *PayMasterFilterer) ParseOwnershipTransferred(log types.Log) (*PayMasterOwnershipTransferred, error) {
	event := new(PayMasterOwnershipTransferred)
	if err := _PayMaster.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
