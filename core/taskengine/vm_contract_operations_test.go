package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
)

// TestVM_ContractRead_BasicExecution tests basic contract reading functionality
func TestVM_ContractRead_BasicExecution(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	vm := NewVM()
	vm.WithDb(db)
	vm.WithLogger(testutil.GetLogger())
	vm.smartWalletConfig = testutil.GetTestSmartWalletConfig()

	// Test reading decimals from ETH/USD price feed
	node := &avsproto.ContractReadNode{
		Config: &avsproto.ContractReadNode_Config{
			ContractAddress: "0x5f4ec3df9cbd43714fe2740f5e3616155c5b8419", // Chainlink ETH/USD
			ContractAbi:     "[{\"inputs\":[],\"name\":\"decimals\",\"outputs\":[{\"internalType\":\"uint8\",\"name\":\"\",\"type\":\"uint8\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
			MethodCalls: []*avsproto.ContractReadNode_MethodCall{
				{
					CallData:   "0x313ce567", // decimals()
					MethodName: "decimals",
				},
			},
		},
	}

	executionStep, _ := vm.runContractRead("test_decimals", node)

	assert.NotNil(t, executionStep)
	assert.Equal(t, "test_decimals", executionStep.Id)

	// Contract read may succeed or fail depending on network, but should not panic
	t.Logf("Contract read decimals - Success: %v, Error: %s", executionStep.Success, executionStep.Error)
}

// TestVM_ContractRead_DecimalFormatting tests the new decimal formatting functionality
func TestVM_ContractRead_DecimalFormatting(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	vm := NewVM()
	vm.WithDb(db)
	vm.WithLogger(testutil.GetLogger())
	vm.smartWalletConfig = testutil.GetTestSmartWalletConfig()

	// Test reading price data with decimal formatting
	node := &avsproto.ContractReadNode{
		Config: &avsproto.ContractReadNode_Config{
			ContractAddress: "0x5f4ec3df9cbd43714fe2740f5e3616155c5b8419", // Chainlink ETH/USD
			ContractAbi:     "[{\"inputs\":[],\"name\":\"decimals\",\"outputs\":[{\"internalType\":\"uint8\",\"name\":\"\",\"type\":\"uint8\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"latestRoundData\",\"outputs\":[{\"internalType\":\"uint80\",\"name\":\"roundId\",\"type\":\"uint80\"},{\"internalType\":\"int256\",\"name\":\"answer\",\"type\":\"int256\"},{\"internalType\":\"uint256\",\"name\":\"startedAt\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"internalType\":\"uint80\",\"name\":\"answeredInRound\",\"type\":\"uint80\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
			MethodCalls: []*avsproto.ContractReadNode_MethodCall{
				{
					CallData:      "0x313ce567", // decimals()
					MethodName:    "decimals",
					ApplyToFields: []string{"answer"}, // Apply decimal formatting to the "answer" field
				},
				{
					CallData:   "0xfeaf968c", // latestRoundData()
					MethodName: "latestRoundData",
				},
			},
		},
	}

	executionStep, _ := vm.runContractRead("test_decimal_formatting", node)

	assert.NotNil(t, executionStep)
	assert.Equal(t, "test_decimal_formatting", executionStep.Id)

	// Contract read may succeed or fail depending on network, but should not panic
	t.Logf("Contract read with decimal formatting - Success: %v, Error: %s", executionStep.Success, executionStep.Error)

	if executionStep.Success {
		// Check that we have contract read output
		if contractReadOutput := executionStep.GetContractRead(); contractReadOutput != nil {
			var results []interface{}
			if contractReadOutput.GetData() != nil {
				// Extract results from the protobuf Value
				if contractReadOutput.GetData().GetListValue() != nil {
					// Data is an array
					for _, item := range contractReadOutput.GetData().GetListValue().GetValues() {
						results = append(results, item.AsInterface())
					}
				} else {
					// Data might be a single object, wrap it in an array for consistency
					results = append(results, contractReadOutput.GetData().AsInterface())
				}
			}

			t.Logf("Contract read results count: %d", len(results))

			// We should have 1 result (decimals() call is skipped, only latestRoundData() result)
			if len(results) > 0 {
				if resultMap, ok := results[0].(map[string]interface{}); ok {
					t.Logf("Method result - Success: %v, Method: %s",
						resultMap["success"], resultMap["methodName"])

					// Log data fields if available
					if data, ok := resultMap["data"].(map[string]interface{}); ok {
						t.Logf("  Data fields: %d", len(data))
						for fieldName, fieldValue := range data {
							t.Logf("  Field: %s = %v", fieldName, fieldValue)
						}
					}
				}
			}
		}
	}
}

// TestVM_ContractRead_LatestRoundData tests reading price data
func TestVM_ContractRead_LatestRoundData(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	vm := NewVM()
	vm.WithDb(db)
	vm.WithLogger(testutil.GetLogger())
	vm.smartWalletConfig = testutil.GetTestSmartWalletConfig()

	// Test reading latest round data from ETH/USD price feed
	node := &avsproto.ContractReadNode{
		Config: &avsproto.ContractReadNode_Config{
			ContractAddress: "0x5f4ec3df9cbd43714fe2740f5e3616155c5b8419",
			ContractAbi:     "[{\"inputs\":[],\"name\":\"latestRoundData\",\"outputs\":[{\"internalType\":\"uint80\",\"name\":\"roundId\",\"type\":\"uint80\"},{\"internalType\":\"int256\",\"name\":\"answer\",\"type\":\"int256\"},{\"internalType\":\"uint256\",\"name\":\"startedAt\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"internalType\":\"uint80\",\"name\":\"answeredInRound\",\"type\":\"uint80\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
			MethodCalls: []*avsproto.ContractReadNode_MethodCall{
				{
					CallData:   "0xfeaf968c", // This is decimals, but for demo purposes
					MethodName: "decimals",
				},
			},
		},
	}

	executionStep, _ := vm.runContractRead("test_latest_round", node)

	assert.NotNil(t, executionStep)
	assert.Equal(t, "test_latest_round", executionStep.Id)

	t.Logf("Contract read latest round - Success: %v, Error: %s", executionStep.Success, executionStep.Error)
}

// TestVM_ContractRead_ErrorHandling tests various error conditions
func TestVM_ContractRead_ErrorHandling(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	tests := []struct {
		name        string
		setupVM     func(*VM)
		node        *avsproto.ContractReadNode
		expectError bool
		errorText   string
	}{
		{
			name:    "Missing Smart Wallet Config",
			setupVM: func(v *VM) { v.smartWalletConfig = nil },
			node: &avsproto.ContractReadNode{
				Config: &avsproto.ContractReadNode_Config{
					ContractAddress: "0x5f4ec3df9cbd43714fe2740f5e3616155c5b8419",
					ContractAbi:     "[{\"inputs\":[],\"name\":\"decimals\",\"outputs\":[{\"internalType\":\"uint8\",\"name\":\"\",\"type\":\"uint8\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
					MethodCalls: []*avsproto.ContractReadNode_MethodCall{
						{
							CallData:   "0xfeaf968c",
							MethodName: "decimals",
						},
					},
				},
			},
			expectError: true,
			errorText:   "smart wallet config",
		},
		{
			name: "Invalid Contract Address",
			setupVM: func(v *VM) {
				// Create a mock smart wallet config with a test RPC URL
				// to ensure we test the contract address validation, not RPC URL validation
				config := &config.SmartWalletConfig{
					EthRpcUrl:         "http://localhost:99999/definitely-not-a-real-endpoint", // Guaranteed to fail
					BundlerURL:        "https://bundler.test",
					EthWsUrl:          "wss://localhost:99999/ws",
					FactoryAddress:    common.HexToAddress("0x29adA1b5217242DEaBB142BC3b1bCfFdd56008e7"),
					EntrypointAddress: common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"),
					PaymasterAddress:  common.HexToAddress("0x742d35Cc6634C0532925a3b8D091d2B5e57a9C7E"),
				}
				v.smartWalletConfig = config
			},
			node: &avsproto.ContractReadNode{
				Config: &avsproto.ContractReadNode_Config{
					ContractAddress: "invalid-address",
					ContractAbi:     "[{\"inputs\":[],\"name\":\"decimals\",\"outputs\":[{\"internalType\":\"uint8\",\"name\":\"\",\"type\":\"uint8\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
					MethodCalls: []*avsproto.ContractReadNode_MethodCall{
						{
							CallData:   "0xfeaf968c",
							MethodName: "decimals",
						},
					},
				},
			},
			expectError: true,
			errorText:   "", // Error text may vary
		},
		{
			name: "Empty Config",
			setupVM: func(v *VM) {
				SetRpc(testutil.GetTestRPCURL())
				v.smartWalletConfig = testutil.GetTestSmartWalletConfig()
			},
			node: &avsproto.ContractReadNode{
				Config: &avsproto.ContractReadNode_Config{},
			},
			expectError: true,
			errorText:   "missing required configuration: contractAddress and contractAbi are required",
		},
		{
			name: "Method Name Mismatch",
			setupVM: func(v *VM) {
				// Use a valid RPC URL to test the validation logic
				config := &config.SmartWalletConfig{
					EthRpcUrl:         "https://sepolia.drpc.org", // Real endpoint for testing
					BundlerURL:        "https://bundler.test",
					EthWsUrl:          "wss://sepolia.drpc.org",
					FactoryAddress:    common.HexToAddress("0x29adA1b5217242DEaBB142BC3b1bCfFdd56008e7"),
					EntrypointAddress: common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"),
					PaymasterAddress:  common.HexToAddress("0x742d35Cc6634C0532925a3b8D091d2B5e57a9C7E"),
				}
				v.smartWalletConfig = config
			},
			node: &avsproto.ContractReadNode{
				Config: &avsproto.ContractReadNode_Config{
					ContractAddress: "0x5f4ec3df9cbd43714fe2740f5e3616155c5b8419", // Valid Chainlink contract
					ContractAbi:     "[{\"inputs\":[],\"name\":\"decimals\",\"outputs\":[{\"internalType\":\"uint8\",\"name\":\"\",\"type\":\"uint8\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"latestRoundData\",\"outputs\":[{\"internalType\":\"uint80\",\"name\":\"roundId\",\"type\":\"uint80\"},{\"internalType\":\"int256\",\"name\":\"answer\",\"type\":\"int256\"},{\"internalType\":\"uint256\",\"name\":\"startedAt\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"internalType\":\"uint80\",\"name\":\"answeredInRound\",\"type\":\"uint80\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
					MethodCalls: []*avsproto.ContractReadNode_MethodCall{
						{
							CallData:   "0x313ce567",      // decimals() function selector
							MethodName: "latestRoundData", // Wrong method name - should be "decimals"
						},
					},
				},
			},
			expectError: true,
			errorText:   "method name mismatch", // Should contain the validation error
		},
		{
			name: "Client Error Scenario",
			setupVM: func(v *VM) {
				// Use a valid RPC URL to test the validation logic
				config := &config.SmartWalletConfig{
					EthRpcUrl:         "https://sepolia.drpc.org", // Real endpoint for testing
					BundlerURL:        "https://bundler.test",
					EthWsUrl:          "wss://sepolia.drpc.org",
					FactoryAddress:    common.HexToAddress("0x29adA1b5217242DEaBB142BC3b1bCfFdd56008e7"),
					EntrypointAddress: common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"),
					PaymasterAddress:  common.HexToAddress("0x742d35Cc6634C0532925a3b8D091d2B5e57a9C7E"),
				}
				v.smartWalletConfig = config
			},
			node: &avsproto.ContractReadNode{
				Config: &avsproto.ContractReadNode_Config{
					ContractAddress: "0xF4030086522a5bEEa4988F8cA5B36dbC97BeE88c", // Client's contract address
					ContractAbi:     "[{\"inputs\":[],\"name\":\"decimals\",\"outputs\":[{\"internalType\":\"uint8\",\"name\":\"\",\"type\":\"uint8\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"description\",\"outputs\":[{\"internalType\":\"string\",\"name\":\"\",\"type\":\"string\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"version\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"uint80\",\"name\":\"_roundId\",\"type\":\"uint80\"}],\"name\":\"getRoundData\",\"outputs\":[{\"internalType\":\"uint80\",\"name\":\"roundId\",\"type\":\"uint80\"},{\"internalType\":\"int256\",\"name\":\"answer\",\"type\":\"int256\"},{\"internalType\":\"uint256\",\"name\":\"startedAt\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"internalType\":\"uint80\",\"name\":\"answeredInRound\",\"type\":\"uint80\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"latestRoundData\",\"outputs\":[{\"internalType\":\"uint80\",\"name\":\"roundId\",\"type\":\"uint80\"},{\"internalType\":\"int256\",\"name\":\"answer\",\"type\":\"int256\"},{\"internalType\":\"uint256\",\"name\":\"startedAt\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"internalType\":\"uint80\",\"name\":\"answeredInRound\",\"type\":\"uint80\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
					MethodCalls: []*avsproto.ContractReadNode_MethodCall{
						{
							CallData:   "0x313ce567",      // decimals() function selector - this is correct for decimals
							MethodName: "latestRoundData", // Wrong! This should be "decimals"
						},
					},
				},
			},
			expectError: true,
			errorText:   "method name mismatch", // Should catch the exact client error
		},
		{
			name: "Empty Contract Response",
			setupVM: func(v *VM) {
				// Use a valid RPC URL but point to a non-existent contract or one that returns empty data
				config := &config.SmartWalletConfig{
					EthRpcUrl:         "https://sepolia.drpc.org", // Real endpoint
					BundlerURL:        "https://bundler.test",
					EthWsUrl:          "wss://sepolia.drpc.org",
					FactoryAddress:    common.HexToAddress("0x29adA1b5217242DEaBB142BC3b1bCfFdd56008e7"),
					EntrypointAddress: common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"),
					PaymasterAddress:  common.HexToAddress("0x742d35Cc6634C0532925a3b8D091d2B5e57a9C7E"),
				}
				v.smartWalletConfig = config
			},
			node: &avsproto.ContractReadNode{
				Config: &avsproto.ContractReadNode_Config{
					ContractAddress: "0xF4030086522a5bEEa4988F8cA5B36dbC97BeE88c", // Address that returns empty data
					ContractAbi:     "[{\"inputs\":[],\"name\":\"decimals\",\"outputs\":[{\"internalType\":\"uint8\",\"name\":\"\",\"type\":\"uint8\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"latestRoundData\",\"outputs\":[{\"internalType\":\"uint80\",\"name\":\"roundId\",\"type\":\"uint80\"},{\"internalType\":\"int256\",\"name\":\"answer\",\"type\":\"int256\"},{\"internalType\":\"uint256\",\"name\":\"startedAt\",\"type\":\"uint256\"},{\"internalType\":\"uint256\",\"name\":\"updatedAt\",\"type\":\"uint256\"},{\"internalType\":\"uint80\",\"name\":\"answeredInRound\",\"type\":\"uint80\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
					MethodCalls: []*avsproto.ContractReadNode_MethodCall{
						{
							CallData:   "0xfeaf968c",      // latestRoundData() function selector - correct
							MethodName: "latestRoundData", // Correct method name
						},
						{
							CallData:   "0x313ce567", // decimals() function selector - correct
							MethodName: "decimals",   // Correct method name
						},
					},
				},
			},
			expectError: true,
			errorText:   "contract does not exist", // Should catch contract not found
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVM()
			vm.WithLogger(testutil.GetLogger())
			tt.setupVM(vm)

			executionStep, _ := vm.runContractRead("test_error", tt.node)

			assert.NotNil(t, executionStep)
			assert.Equal(t, "test_error", executionStep.Id)

			if tt.expectError {
				assert.False(t, executionStep.Success)
				assert.NotEmpty(t, executionStep.Error)
				if tt.errorText != "" {
					assert.Contains(t, executionStep.Error, tt.errorText)
				}
			}

			t.Logf("%s - Success: %v, Error: %s", tt.name, executionStep.Success, executionStep.Error)
		})
	}
}

// TestVM_ContractWrite_BasicExecution tests basic contract write functionality
func TestVM_ContractWrite_BasicExecution(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	vm := NewVM()
	vm.WithDb(db)
	vm.WithLogger(testutil.GetLogger())
	vm.smartWalletConfig = testutil.GetTestSmartWalletConfig()

	// Test contract write (will likely fail due to lack of actual transaction setup, but should not panic)
	node := &avsproto.ContractWriteNode{
		Config: &avsproto.ContractWriteNode_Config{
			ContractAddress: "0x742d35Cc6634C0532925a3b8D091d2B5e57a9C7E", // Test address
			ContractAbi:     "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"to\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"name\":\"transfer\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]",
			MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
				{
					CallData:   "0xa9059cbb", // transfer function selector
					MethodName: "transfer",
				},
			},
		},
	}

	executionStep, err := vm.runContractWrite("test_write", node)

	// Contract write will likely fail in test environment, but should handle gracefully
	assert.NotNil(t, executionStep)
	assert.Equal(t, "test_write", executionStep.Id)

	// Check the new enhanced output structure
	if executionStep.Success {
		contractWriteOutput := executionStep.GetContractWrite()
		if contractWriteOutput != nil && contractWriteOutput.GetData() != nil {
			var results []interface{}
			// Extract results from the protobuf Value
			if contractWriteOutput.GetData().GetListValue() != nil {
				// Data is an array
				for _, item := range contractWriteOutput.GetData().GetListValue().GetValues() {
					results = append(results, item.AsInterface())
				}
			} else {
				// Data might be a single object, wrap it in an array for consistency
				results = append(results, contractWriteOutput.GetData().AsInterface())
			}

			if len(results) > 0 {
				if resultMap, ok := results[0].(map[string]interface{}); ok {
					hash := ""
					if transaction, ok := resultMap["transaction"].(map[string]interface{}); ok {
						if h, ok := transaction["hash"].(string); ok {
							hash = h
						}
					}
					t.Logf("Contract write result - Method: %s, Success: %v, Hash: %s",
						resultMap["methodName"], resultMap["success"], hash)
				}
			}
		}
	}

	t.Logf("Contract write - Success: %v, Error: %s", executionStep.Success, executionStep.Error)
	if err != nil {
		t.Logf("Contract write system error (expected): %v", err)
	}
}

// TestVM_ContractWrite_ErrorHandling tests contract write error conditions
func TestVM_ContractWrite_ErrorHandling(t *testing.T) {
	// Setup test database for ContractWrite operations
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	tests := []struct {
		name        string
		setupVM     func(*VM)
		node        *avsproto.ContractWriteNode
		expectError bool
		errorText   string
	}{
		{
			name:    "Missing Smart Wallet Config",
			setupVM: func(v *VM) { v.smartWalletConfig = nil },
			node: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{
					ContractAddress: "0x742d35Cc6634C0532925a3b8D091d2B5e57a9C7E",
					ContractAbi:     "[{\"inputs\":[],\"name\":\"test\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]",
					MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
						{
							CallData:   "0xa9059cbb",
							MethodName: "test",
						},
					},
				},
			},
			expectError: true,
			errorText:   "smart wallet config",
		},
		{
			name: "Invalid Contract Address",
			setupVM: func(v *VM) {
				// Use a properly configured smart wallet config to ensure we test address validation
				config := &config.SmartWalletConfig{
					EthRpcUrl:         "https://sepolia.drpc.org", // Valid RPC URL
					BundlerURL:        "https://bundler.test",
					EthWsUrl:          "wss://sepolia.drpc.org",
					FactoryAddress:    common.HexToAddress("0x29adA1b5217242DEaBB142BC3b1bCfFdd56008e7"),
					EntrypointAddress: common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"),
					PaymasterAddress:  common.HexToAddress("0x742d35Cc6634C0532925a3b8D091d2B5e57a9C7E"),
				}
				v.smartWalletConfig = config
			},
			node: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{
					ContractAddress: "invalid-address",
					ContractAbi:     "[{\"inputs\":[],\"name\":\"test\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]",
					MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
						{
							CallData:   "0xa9059cbb",
							MethodName: "test",
						},
					},
				},
			},
			expectError: true,
			errorText:   "", // Error text may vary
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := NewVM()
			vm.WithDb(db)
			vm.WithLogger(testutil.GetLogger())
			tt.setupVM(vm)

			executionStep, _ := vm.runContractWrite("test_write_error", tt.node)

			assert.NotNil(t, executionStep)
			assert.Equal(t, "test_write_error", executionStep.Id)

			if tt.expectError {
				assert.False(t, executionStep.Success)
				assert.NotEmpty(t, executionStep.Error)
				if tt.errorText != "" {
					assert.Contains(t, executionStep.Error, tt.errorText)
				}
			}

			t.Logf("%s - Success: %v, Error: %s", tt.name, executionStep.Success, executionStep.Error)
		})
	}
}
