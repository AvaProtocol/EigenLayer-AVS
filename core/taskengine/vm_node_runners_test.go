package taskengine

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// TestVM_ContractReadRunner tests the contract read node runner
func TestVM_ContractReadRunner(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	vm := NewVM()
	vm.WithDb(db)
	vm.WithLogger(testutil.GetLogger())

	// Test contract read with proper configuration
	node := &avsproto.ContractReadNode{
		Config: &avsproto.ContractReadNode_Config{
			ContractAddress: "0x5f4ec3df9cbd43714fe2740f5e3616155c5b8419", // Chainlink ETH/USD price feed
			ContractAbi:     "[{\"inputs\":[],\"name\":\"decimals\",\"outputs\":[{\"internalType\":\"uint8\",\"name\":\"\",\"type\":\"uint8\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
			MethodCalls: []*avsproto.ContractReadNode_MethodCall{
				{
					CallData:   "0xfeaf968c", // decimals() function
					MethodName: "decimals",
				},
			},
		},
	}

	// Set smart wallet config for RPC access
	vm.smartWalletConfig = testutil.GetTestSmartWalletConfig()

	// Execute contract read
	executionStep, err := vm.runContractRead("test_contract_read", node)

	// The contract read might fail due to network/config issues, but should not panic
	assert.NotNil(t, executionStep)
	assert.Equal(t, "test_contract_read", executionStep.Id)

	// Log the result for debugging
	t.Logf("Contract read result - Success: %v, Error: %s", executionStep.Success, executionStep.Error)

	if err != nil {
		t.Logf("Contract read error (may be expected): %v", err)
	}
}

// TestVM_ContractReadRunner_MissingConfig tests contract read with missing configuration
func TestVM_ContractReadRunner_MissingConfig(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	// Test contract read with missing smart wallet config
	node := &avsproto.ContractReadNode{
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
	}

	// Don't set smart wallet config to trigger error
	vm.smartWalletConfig = nil

	// Execute contract read (should fail gracefully)
	executionStep, err := vm.runContractRead("test_missing_config", node)

	// Should return execution step with failure
	assert.NotNil(t, executionStep)
	assert.Equal(t, "test_missing_config", executionStep.Id)
	assert.False(t, executionStep.Success)
	assert.Contains(t, executionStep.Error, "smart wallet config")

	// Should also return an error
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "smart wallet config")
}

// TestVM_CustomCodeRunner tests the custom code node runner
func TestVM_CustomCodeRunner(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	// Test simple custom code execution
	node := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Source: `
				return {
					message: "Hello from custom code",
					timestamp: Date.now(),
					success: true
				};
			`,
		},
	}

	// Execute custom code
	executionStep, err := vm.runCustomCode("test_custom_code", node)

	// Verify execution results
	assert.NoError(t, err)
	assert.NotNil(t, executionStep)
	assert.Equal(t, "test_custom_code", executionStep.Id)
	assert.True(t, executionStep.Success)
	assert.Empty(t, executionStep.Error)

	t.Logf("Custom code execution - Success: %v", executionStep.Success)
}

// TestVM_CustomCodeRunner_SyntaxError tests custom code with syntax error
func TestVM_CustomCodeRunner_SyntaxError(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	// Test custom code with syntax error
	node := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Source: `
				// Invalid JavaScript syntax
				return {
					message: "This should fail"
					// Missing comma and closing brace
			`,
		},
	}

	// Execute custom code (should fail gracefully)
	executionStep, err := vm.runCustomCode("test_syntax_error", node)

	// Should return execution step with failure
	assert.NotNil(t, executionStep)
	assert.Equal(t, "test_syntax_error", executionStep.Id)
	assert.False(t, executionStep.Success)
	assert.NotEmpty(t, executionStep.Error)

	// May or may not return system error depending on implementation
	t.Logf("Syntax error handling - Step Success: %v, Step Error: %s", executionStep.Success, executionStep.Error)
	if err != nil {
		t.Logf("System error: %v", err)
	}
}

// TestVM_RestAPIRunner tests the REST API node runner
func TestVM_RestAPIRunner(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	// Test REST API call to public endpoint
	node := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url:    "https://httpbin.org/json",
			Method: "GET",
			Headers: map[string]string{
				"User-Agent": "EigenLayer-AVS-Test",
			},
		},
	}

	// Execute REST API call
	executionStep, err := vm.runRestApi("test_rest_api", node)

	// Verify execution results
	assert.NotNil(t, executionStep)
	assert.Equal(t, "test_rest_api", executionStep.Id)

	// The REST call might succeed or fail depending on network
	t.Logf("REST API call - Success: %v, Error: %s", executionStep.Success, executionStep.Error)

	if err != nil {
		t.Logf("REST API error (may be expected): %v", err)
	}
}

// TestVM_EthTransferRunner tests the ETH transfer node runner
func TestVM_EthTransferRunner(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	// Test ETH transfer with missing smart wallet config (should fail gracefully)
	node := &avsproto.ETHTransferNode{
		Config: &avsproto.ETHTransferNode_Config{
			Destination: "0x742d35Cc6634C0532925a3b8D091d2B5e57a9C7E",
			Amount:      "1000000000000000", // 0.001 ETH in wei
		},
	}

	// Don't set smart wallet config to test error handling
	vm.smartWalletConfig = nil

	// Execute ETH transfer (should fail gracefully)
	executionStep, err := vm.runEthTransfer("test_eth_transfer", node)

	// Should return execution step with failure
	assert.NotNil(t, executionStep)
	assert.Equal(t, "test_eth_transfer", executionStep.Id)
	assert.False(t, executionStep.Success)
	assert.NotEmpty(t, executionStep.Error)

	// Should also return an error
	assert.Error(t, err)

	t.Logf("ETH transfer error handling - Step Error: %s, System Error: %v", executionStep.Error, err)
}
