// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package aa

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

// SimpleFactoryMetaData contains all meta data concerning the SimpleFactory contract.
var SimpleFactoryMetaData = &bind.MetaData{
	ABI: "[{\"inputs\":[{\"internalType\":\"contractIEntryPoint\",\"name\":\"_entryPoint\",\"type\":\"address\"}],\"stateMutability\":\"nonpayable\",\"type\":\"constructor\"},{\"inputs\":[],\"name\":\"accountImplementation\",\"outputs\":[{\"internalType\":\"contractSimpleAccount\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"salt\",\"type\":\"uint256\"}],\"name\":\"createAccount\",\"outputs\":[{\"internalType\":\"contractSimpleAccount\",\"name\":\"ret\",\"type\":\"address\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"salt\",\"type\":\"uint256\"}],\"name\":\"getAddress\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
}

// SimpleFactoryABI is the input ABI used to generate the binding from.
// Deprecated: Use SimpleFactoryMetaData.ABI instead.
var SimpleFactoryABI = SimpleFactoryMetaData.ABI

// SimpleFactory is an auto generated Go binding around an Ethereum contract.
type SimpleFactory struct {
	SimpleFactoryCaller     // Read-only binding to the contract
	SimpleFactoryTransactor // Write-only binding to the contract
	SimpleFactoryFilterer   // Log filterer for contract events
}

// SimpleFactoryCaller is an auto generated read-only Go binding around an Ethereum contract.
type SimpleFactoryCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// SimpleFactoryTransactor is an auto generated write-only Go binding around an Ethereum contract.
type SimpleFactoryTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// SimpleFactoryFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type SimpleFactoryFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// SimpleFactorySession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type SimpleFactorySession struct {
	Contract     *SimpleFactory    // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// SimpleFactoryCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type SimpleFactoryCallerSession struct {
	Contract *SimpleFactoryCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts        // Call options to use throughout this session
}

// SimpleFactoryTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type SimpleFactoryTransactorSession struct {
	Contract     *SimpleFactoryTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts        // Transaction auth options to use throughout this session
}

// SimpleFactoryRaw is an auto generated low-level Go binding around an Ethereum contract.
type SimpleFactoryRaw struct {
	Contract *SimpleFactory // Generic contract binding to access the raw methods on
}

// SimpleFactoryCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type SimpleFactoryCallerRaw struct {
	Contract *SimpleFactoryCaller // Generic read-only contract binding to access the raw methods on
}

// SimpleFactoryTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type SimpleFactoryTransactorRaw struct {
	Contract *SimpleFactoryTransactor // Generic write-only contract binding to access the raw methods on
}

// NewSimpleFactory creates a new instance of SimpleFactory, bound to a specific deployed contract.
func NewSimpleFactory(address common.Address, backend bind.ContractBackend) (*SimpleFactory, error) {
	contract, err := bindSimpleFactory(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &SimpleFactory{SimpleFactoryCaller: SimpleFactoryCaller{contract: contract}, SimpleFactoryTransactor: SimpleFactoryTransactor{contract: contract}, SimpleFactoryFilterer: SimpleFactoryFilterer{contract: contract}}, nil
}

// NewSimpleFactoryCaller creates a new read-only instance of SimpleFactory, bound to a specific deployed contract.
func NewSimpleFactoryCaller(address common.Address, caller bind.ContractCaller) (*SimpleFactoryCaller, error) {
	contract, err := bindSimpleFactory(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &SimpleFactoryCaller{contract: contract}, nil
}

// NewSimpleFactoryTransactor creates a new write-only instance of SimpleFactory, bound to a specific deployed contract.
func NewSimpleFactoryTransactor(address common.Address, transactor bind.ContractTransactor) (*SimpleFactoryTransactor, error) {
	contract, err := bindSimpleFactory(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &SimpleFactoryTransactor{contract: contract}, nil
}

// NewSimpleFactoryFilterer creates a new log filterer instance of SimpleFactory, bound to a specific deployed contract.
func NewSimpleFactoryFilterer(address common.Address, filterer bind.ContractFilterer) (*SimpleFactoryFilterer, error) {
	contract, err := bindSimpleFactory(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &SimpleFactoryFilterer{contract: contract}, nil
}

// bindSimpleFactory binds a generic wrapper to an already deployed contract.
func bindSimpleFactory(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := SimpleFactoryMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_SimpleFactory *SimpleFactoryRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _SimpleFactory.Contract.SimpleFactoryCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_SimpleFactory *SimpleFactoryRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _SimpleFactory.Contract.SimpleFactoryTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_SimpleFactory *SimpleFactoryRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _SimpleFactory.Contract.SimpleFactoryTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_SimpleFactory *SimpleFactoryCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _SimpleFactory.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_SimpleFactory *SimpleFactoryTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _SimpleFactory.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_SimpleFactory *SimpleFactoryTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _SimpleFactory.Contract.contract.Transact(opts, method, params...)
}

// AccountImplementation is a free data retrieval call binding the contract method 0x11464fbe.
//
// Solidity: function accountImplementation() view returns(address)
func (_SimpleFactory *SimpleFactoryCaller) AccountImplementation(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _SimpleFactory.contract.Call(opts, &out, "accountImplementation")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// AccountImplementation is a free data retrieval call binding the contract method 0x11464fbe.
//
// Solidity: function accountImplementation() view returns(address)
func (_SimpleFactory *SimpleFactorySession) AccountImplementation() (common.Address, error) {
	return _SimpleFactory.Contract.AccountImplementation(&_SimpleFactory.CallOpts)
}

// AccountImplementation is a free data retrieval call binding the contract method 0x11464fbe.
//
// Solidity: function accountImplementation() view returns(address)
func (_SimpleFactory *SimpleFactoryCallerSession) AccountImplementation() (common.Address, error) {
	return _SimpleFactory.Contract.AccountImplementation(&_SimpleFactory.CallOpts)
}

// GetAddress is a free data retrieval call binding the contract method 0x8cb84e18.
//
// Solidity: function getAddress(address owner, uint256 salt) view returns(address)
func (_SimpleFactory *SimpleFactoryCaller) GetAddress(opts *bind.CallOpts, owner common.Address, salt *big.Int) (common.Address, error) {
	var out []interface{}
	err := _SimpleFactory.contract.Call(opts, &out, "getAddress", owner, salt)

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// GetAddress is a free data retrieval call binding the contract method 0x8cb84e18.
//
// Solidity: function getAddress(address owner, uint256 salt) view returns(address)
func (_SimpleFactory *SimpleFactorySession) GetAddress(owner common.Address, salt *big.Int) (common.Address, error) {
	return _SimpleFactory.Contract.GetAddress(&_SimpleFactory.CallOpts, owner, salt)
}

// GetAddress is a free data retrieval call binding the contract method 0x8cb84e18.
//
// Solidity: function getAddress(address owner, uint256 salt) view returns(address)
func (_SimpleFactory *SimpleFactoryCallerSession) GetAddress(owner common.Address, salt *big.Int) (common.Address, error) {
	return _SimpleFactory.Contract.GetAddress(&_SimpleFactory.CallOpts, owner, salt)
}

// CreateAccount is a paid mutator transaction binding the contract method 0x5fbfb9cf.
//
// Solidity: function createAccount(address owner, uint256 salt) returns(address ret)
func (_SimpleFactory *SimpleFactoryTransactor) CreateAccount(opts *bind.TransactOpts, owner common.Address, salt *big.Int) (*types.Transaction, error) {
	return _SimpleFactory.contract.Transact(opts, "createAccount", owner, salt)
}

// CreateAccount is a paid mutator transaction binding the contract method 0x5fbfb9cf.
//
// Solidity: function createAccount(address owner, uint256 salt) returns(address ret)
func (_SimpleFactory *SimpleFactorySession) CreateAccount(owner common.Address, salt *big.Int) (*types.Transaction, error) {
	return _SimpleFactory.Contract.CreateAccount(&_SimpleFactory.TransactOpts, owner, salt)
}

// CreateAccount is a paid mutator transaction binding the contract method 0x5fbfb9cf.
//
// Solidity: function createAccount(address owner, uint256 salt) returns(address ret)
func (_SimpleFactory *SimpleFactoryTransactorSession) CreateAccount(owner common.Address, salt *big.Int) (*types.Transaction, error) {
	return _SimpleFactory.Contract.CreateAccount(&_SimpleFactory.TransactOpts, owner, salt)
}
