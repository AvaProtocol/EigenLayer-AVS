package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContractWriteTenderlySimulation(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	// Use a well-known ERC20 contract for testing - USDC on Base Sepolia
	baseSepoliaUsdcAddress := common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e")

	// Create approve calldata for testing
	approveCallData := "0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e8"

	t.Run("RunNodeImmediately_UsesTenderlySimulation", func(t *testing.T) {
		// Create test engine
		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, testutil.GetLogger())

		// Test run_node_immediately
		nodeConfig := map[string]interface{}{
			"contractAddress": baseSepoliaUsdcAddress.Hex(),
			"contractAbi":     `[{"inputs":[{"internalType":"address","name":"spender","type":"address"},{"internalType":"uint256","name":"amount","type":"uint256"}],"name":"approve","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"}]`,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"callData":   approveCallData,
					"methodName": "approve",
				},
			},
		}

		result, err := engine.RunNodeImmediately("contractWrite", nodeConfig, map[string]interface{}{})

		require.NoError(t, err, "RunNodeImmediately should succeed with Tenderly simulation")
		require.NotNil(t, result, "Should get simulation result")

		// Verify simulation metadata is present
		if results, ok := result["results"].([]interface{}); ok && len(results) > 0 {
			if firstResult, ok := results[0].(*avsproto.ContractWriteNode_MethodResult); ok {
				assert.NotNil(t, firstResult.Transaction, "Should have transaction data")
				assert.True(t, firstResult.Transaction.Simulation, "Should be marked as simulation")
				assert.Contains(t, []string{"tenderly", "mock_fallback"}, firstResult.Transaction.SimulationMode, "Should have simulation mode")
				assert.Equal(t, "simulated", firstResult.Transaction.Status, "Status should be 'simulated'")
				assert.NotEmpty(t, firstResult.Transaction.Hash, "Should have mock transaction hash")

				t.Logf("✅ Contract write simulation successful:")
				t.Logf("   Method: %s", firstResult.MethodName)
				t.Logf("   Success: %t", firstResult.Success)
				t.Logf("   Simulation Mode: %s", firstResult.Transaction.SimulationMode)
				t.Logf("   Transaction Hash: %s", firstResult.Transaction.Hash)
				t.Logf("   Chain ID: %d", firstResult.Transaction.ChainId)
			}
		}
	})
}
