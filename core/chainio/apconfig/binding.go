// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package apconfig

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

// APConfigMetaData contains all meta data concerning the APConfig contract.
var APConfigMetaData = &bind.MetaData{
	ABI: "[{\"type\":\"function\",\"name\":\"declareAlias\",\"inputs\":[{\"name\":\"_alias\",\"type\":\"address\",\"internalType\":\"address\"}],\"outputs\":[],\"stateMutability\":\"nonpayable\"},{\"type\":\"function\",\"name\":\"getAlias\",\"inputs\":[{\"name\":\"_operator\",\"type\":\"address\",\"internalType\":\"address\"}],\"outputs\":[{\"name\":\"\",\"type\":\"address\",\"internalType\":\"address\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"getOperatorForAlias\",\"inputs\":[{\"name\":\"_alias\",\"type\":\"address\",\"internalType\":\"address\"}],\"outputs\":[{\"name\":\"\",\"type\":\"address\",\"internalType\":\"address\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"undeclare\",\"inputs\":[],\"outputs\":[],\"stateMutability\":\"nonpayable\"},{\"type\":\"event\",\"name\":\"AliasDeclared\",\"inputs\":[{\"name\":\"operator\",\"type\":\"address\",\"indexed\":true,\"internalType\":\"address\"},{\"name\":\"aliasAddress\",\"type\":\"address\",\"indexed\":true,\"internalType\":\"address\"}],\"anonymous\":false},{\"type\":\"event\",\"name\":\"AliasUndeclared\",\"inputs\":[{\"name\":\"operator\",\"type\":\"address\",\"indexed\":true,\"internalType\":\"address\"}],\"anonymous\":false}]",
}

// APConfigABI is the input ABI used to generate the binding from.
// Deprecated: Use APConfigMetaData.ABI instead.
var APConfigABI = APConfigMetaData.ABI

// APConfig is an auto generated Go binding around an Ethereum contract.
type APConfig struct {
	APConfigCaller     // Read-only binding to the contract
	APConfigTransactor // Write-only binding to the contract
	APConfigFilterer   // Log filterer for contract events
}

// APConfigCaller is an auto generated read-only Go binding around an Ethereum contract.
type APConfigCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// APConfigTransactor is an auto generated write-only Go binding around an Ethereum contract.
type APConfigTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// APConfigFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type APConfigFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// APConfigSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type APConfigSession struct {
	Contract     *APConfig         // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// APConfigCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type APConfigCallerSession struct {
	Contract *APConfigCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts   // Call options to use throughout this session
}

// APConfigTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type APConfigTransactorSession struct {
	Contract     *APConfigTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts   // Transaction auth options to use throughout this session
}

// APConfigRaw is an auto generated low-level Go binding around an Ethereum contract.
type APConfigRaw struct {
	Contract *APConfig // Generic contract binding to access the raw methods on
}

// APConfigCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type APConfigCallerRaw struct {
	Contract *APConfigCaller // Generic read-only contract binding to access the raw methods on
}

// APConfigTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type APConfigTransactorRaw struct {
	Contract *APConfigTransactor // Generic write-only contract binding to access the raw methods on
}

// NewAPConfig creates a new instance of APConfig, bound to a specific deployed contract.
func NewAPConfig(address common.Address, backend bind.ContractBackend) (*APConfig, error) {
	contract, err := bindAPConfig(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &APConfig{APConfigCaller: APConfigCaller{contract: contract}, APConfigTransactor: APConfigTransactor{contract: contract}, APConfigFilterer: APConfigFilterer{contract: contract}}, nil
}

// NewAPConfigCaller creates a new read-only instance of APConfig, bound to a specific deployed contract.
func NewAPConfigCaller(address common.Address, caller bind.ContractCaller) (*APConfigCaller, error) {
	contract, err := bindAPConfig(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &APConfigCaller{contract: contract}, nil
}

// NewAPConfigTransactor creates a new write-only instance of APConfig, bound to a specific deployed contract.
func NewAPConfigTransactor(address common.Address, transactor bind.ContractTransactor) (*APConfigTransactor, error) {
	contract, err := bindAPConfig(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &APConfigTransactor{contract: contract}, nil
}

// NewAPConfigFilterer creates a new log filterer instance of APConfig, bound to a specific deployed contract.
func NewAPConfigFilterer(address common.Address, filterer bind.ContractFilterer) (*APConfigFilterer, error) {
	contract, err := bindAPConfig(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &APConfigFilterer{contract: contract}, nil
}

// bindAPConfig binds a generic wrapper to an already deployed contract.
func bindAPConfig(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := APConfigMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_APConfig *APConfigRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _APConfig.Contract.APConfigCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_APConfig *APConfigRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _APConfig.Contract.APConfigTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_APConfig *APConfigRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _APConfig.Contract.APConfigTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_APConfig *APConfigCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _APConfig.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_APConfig *APConfigTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _APConfig.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_APConfig *APConfigTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _APConfig.Contract.contract.Transact(opts, method, params...)
}

// GetAlias is a free data retrieval call binding the contract method 0x99900d11.
//
// Solidity: function getAlias(address _operator) view returns(address)
func (_APConfig *APConfigCaller) GetAlias(opts *bind.CallOpts, _operator common.Address) (common.Address, error) {
	var out []interface{}
	err := _APConfig.contract.Call(opts, &out, "getAlias", _operator)

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// GetAlias is a free data retrieval call binding the contract method 0x99900d11.
//
// Solidity: function getAlias(address _operator) view returns(address)
func (_APConfig *APConfigSession) GetAlias(_operator common.Address) (common.Address, error) {
	return _APConfig.Contract.GetAlias(&_APConfig.CallOpts, _operator)
}

// GetAlias is a free data retrieval call binding the contract method 0x99900d11.
//
// Solidity: function getAlias(address _operator) view returns(address)
func (_APConfig *APConfigCallerSession) GetAlias(_operator common.Address) (common.Address, error) {
	return _APConfig.Contract.GetAlias(&_APConfig.CallOpts, _operator)
}

// GetOperatorForAlias is a free data retrieval call binding the contract method 0x8139d05b.
//
// Solidity: function getOperatorForAlias(address _alias) view returns(address)
func (_APConfig *APConfigCaller) GetOperatorForAlias(opts *bind.CallOpts, _alias common.Address) (common.Address, error) {
	var out []interface{}
	err := _APConfig.contract.Call(opts, &out, "getOperatorForAlias", _alias)

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// GetOperatorForAlias is a free data retrieval call binding the contract method 0x8139d05b.
//
// Solidity: function getOperatorForAlias(address _alias) view returns(address)
func (_APConfig *APConfigSession) GetOperatorForAlias(_alias common.Address) (common.Address, error) {
	return _APConfig.Contract.GetOperatorForAlias(&_APConfig.CallOpts, _alias)
}

// GetOperatorForAlias is a free data retrieval call binding the contract method 0x8139d05b.
//
// Solidity: function getOperatorForAlias(address _alias) view returns(address)
func (_APConfig *APConfigCallerSession) GetOperatorForAlias(_alias common.Address) (common.Address, error) {
	return _APConfig.Contract.GetOperatorForAlias(&_APConfig.CallOpts, _alias)
}

// DeclareAlias is a paid mutator transaction binding the contract method 0xf405566d.
//
// Solidity: function declareAlias(address _alias) returns()
func (_APConfig *APConfigTransactor) DeclareAlias(opts *bind.TransactOpts, _alias common.Address) (*types.Transaction, error) {
	return _APConfig.contract.Transact(opts, "declareAlias", _alias)
}

// DeclareAlias is a paid mutator transaction binding the contract method 0xf405566d.
//
// Solidity: function declareAlias(address _alias) returns()
func (_APConfig *APConfigSession) DeclareAlias(_alias common.Address) (*types.Transaction, error) {
	return _APConfig.Contract.DeclareAlias(&_APConfig.TransactOpts, _alias)
}

// DeclareAlias is a paid mutator transaction binding the contract method 0xf405566d.
//
// Solidity: function declareAlias(address _alias) returns()
func (_APConfig *APConfigTransactorSession) DeclareAlias(_alias common.Address) (*types.Transaction, error) {
	return _APConfig.Contract.DeclareAlias(&_APConfig.TransactOpts, _alias)
}

// Undeclare is a paid mutator transaction binding the contract method 0x2c46b3e1.
//
// Solidity: function undeclare() returns()
func (_APConfig *APConfigTransactor) Undeclare(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _APConfig.contract.Transact(opts, "undeclare")
}

// Undeclare is a paid mutator transaction binding the contract method 0x2c46b3e1.
//
// Solidity: function undeclare() returns()
func (_APConfig *APConfigSession) Undeclare() (*types.Transaction, error) {
	return _APConfig.Contract.Undeclare(&_APConfig.TransactOpts)
}

// Undeclare is a paid mutator transaction binding the contract method 0x2c46b3e1.
//
// Solidity: function undeclare() returns()
func (_APConfig *APConfigTransactorSession) Undeclare() (*types.Transaction, error) {
	return _APConfig.Contract.Undeclare(&_APConfig.TransactOpts)
}

// APConfigAliasDeclaredIterator is returned from FilterAliasDeclared and is used to iterate over the raw logs and unpacked data for AliasDeclared events raised by the APConfig contract.
type APConfigAliasDeclaredIterator struct {
	Event *APConfigAliasDeclared // Event containing the contract specifics and raw log

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
func (it *APConfigAliasDeclaredIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(APConfigAliasDeclared)
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
		it.Event = new(APConfigAliasDeclared)
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
func (it *APConfigAliasDeclaredIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *APConfigAliasDeclaredIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// APConfigAliasDeclared represents a AliasDeclared event raised by the APConfig contract.
type APConfigAliasDeclared struct {
	Operator     common.Address
	AliasAddress common.Address
	Raw          types.Log // Blockchain specific contextual infos
}

// FilterAliasDeclared is a free log retrieval operation binding the contract event 0xf9528232876ca43a75b6f0ae52cdae8a80b29a7a53569f2e9966c414fa029195.
//
// Solidity: event AliasDeclared(address indexed operator, address indexed aliasAddress)
func (_APConfig *APConfigFilterer) FilterAliasDeclared(opts *bind.FilterOpts, operator []common.Address, aliasAddress []common.Address) (*APConfigAliasDeclaredIterator, error) {

	var operatorRule []interface{}
	for _, operatorItem := range operator {
		operatorRule = append(operatorRule, operatorItem)
	}
	var aliasAddressRule []interface{}
	for _, aliasAddressItem := range aliasAddress {
		aliasAddressRule = append(aliasAddressRule, aliasAddressItem)
	}

	logs, sub, err := _APConfig.contract.FilterLogs(opts, "AliasDeclared", operatorRule, aliasAddressRule)
	if err != nil {
		return nil, err
	}
	return &APConfigAliasDeclaredIterator{contract: _APConfig.contract, event: "AliasDeclared", logs: logs, sub: sub}, nil
}

// WatchAliasDeclared is a free log subscription operation binding the contract event 0xf9528232876ca43a75b6f0ae52cdae8a80b29a7a53569f2e9966c414fa029195.
//
// Solidity: event AliasDeclared(address indexed operator, address indexed aliasAddress)
func (_APConfig *APConfigFilterer) WatchAliasDeclared(opts *bind.WatchOpts, sink chan<- *APConfigAliasDeclared, operator []common.Address, aliasAddress []common.Address) (event.Subscription, error) {

	var operatorRule []interface{}
	for _, operatorItem := range operator {
		operatorRule = append(operatorRule, operatorItem)
	}
	var aliasAddressRule []interface{}
	for _, aliasAddressItem := range aliasAddress {
		aliasAddressRule = append(aliasAddressRule, aliasAddressItem)
	}

	logs, sub, err := _APConfig.contract.WatchLogs(opts, "AliasDeclared", operatorRule, aliasAddressRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(APConfigAliasDeclared)
				if err := _APConfig.contract.UnpackLog(event, "AliasDeclared", log); err != nil {
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

// ParseAliasDeclared is a log parse operation binding the contract event 0xf9528232876ca43a75b6f0ae52cdae8a80b29a7a53569f2e9966c414fa029195.
//
// Solidity: event AliasDeclared(address indexed operator, address indexed aliasAddress)
func (_APConfig *APConfigFilterer) ParseAliasDeclared(log types.Log) (*APConfigAliasDeclared, error) {
	event := new(APConfigAliasDeclared)
	if err := _APConfig.contract.UnpackLog(event, "AliasDeclared", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// APConfigAliasUndeclaredIterator is returned from FilterAliasUndeclared and is used to iterate over the raw logs and unpacked data for AliasUndeclared events raised by the APConfig contract.
type APConfigAliasUndeclaredIterator struct {
	Event *APConfigAliasUndeclared // Event containing the contract specifics and raw log

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
func (it *APConfigAliasUndeclaredIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(APConfigAliasUndeclared)
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
		it.Event = new(APConfigAliasUndeclared)
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
func (it *APConfigAliasUndeclaredIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *APConfigAliasUndeclaredIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// APConfigAliasUndeclared represents a AliasUndeclared event raised by the APConfig contract.
type APConfigAliasUndeclared struct {
	Operator common.Address
	Raw      types.Log // Blockchain specific contextual infos
}

// FilterAliasUndeclared is a free log retrieval operation binding the contract event 0x8f92aba3a92a6e1c96ff1ae5812518155f45d1baf5651a7653e2250371805c0d.
//
// Solidity: event AliasUndeclared(address indexed operator)
func (_APConfig *APConfigFilterer) FilterAliasUndeclared(opts *bind.FilterOpts, operator []common.Address) (*APConfigAliasUndeclaredIterator, error) {

	var operatorRule []interface{}
	for _, operatorItem := range operator {
		operatorRule = append(operatorRule, operatorItem)
	}

	logs, sub, err := _APConfig.contract.FilterLogs(opts, "AliasUndeclared", operatorRule)
	if err != nil {
		return nil, err
	}
	return &APConfigAliasUndeclaredIterator{contract: _APConfig.contract, event: "AliasUndeclared", logs: logs, sub: sub}, nil
}

// WatchAliasUndeclared is a free log subscription operation binding the contract event 0x8f92aba3a92a6e1c96ff1ae5812518155f45d1baf5651a7653e2250371805c0d.
//
// Solidity: event AliasUndeclared(address indexed operator)
func (_APConfig *APConfigFilterer) WatchAliasUndeclared(opts *bind.WatchOpts, sink chan<- *APConfigAliasUndeclared, operator []common.Address) (event.Subscription, error) {

	var operatorRule []interface{}
	for _, operatorItem := range operator {
		operatorRule = append(operatorRule, operatorItem)
	}

	logs, sub, err := _APConfig.contract.WatchLogs(opts, "AliasUndeclared", operatorRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(APConfigAliasUndeclared)
				if err := _APConfig.contract.UnpackLog(event, "AliasUndeclared", log); err != nil {
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

// ParseAliasUndeclared is a log parse operation binding the contract event 0x8f92aba3a92a6e1c96ff1ae5812518155f45d1baf5651a7653e2250371805c0d.
//
// Solidity: event AliasUndeclared(address indexed operator)
func (_APConfig *APConfigFilterer) ParseAliasUndeclared(log types.Log) (*APConfigAliasUndeclared, error) {
	event := new(APConfigAliasUndeclared)
	if err := _APConfig.contract.UnpackLog(event, "AliasUndeclared", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
