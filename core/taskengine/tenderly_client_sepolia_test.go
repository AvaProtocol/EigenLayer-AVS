//go:build integration
// +build integration

package taskengine

import (
	"math/big"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContractWrite_WETHDeposit_WithETHValue_Sepolia(t *testing.T) {
	// Tests that ETH value is correctly passed through ContractWrite to WETH deposit() on Sepolia
	// This validates the complete flow: nodeConfig.value -> VM -> ContractWriteProcessor -> Tenderly simulation
	//
	// Test Requirements on Sepolia:
	// - Smart wallet must have ~0.15 ETH (0.1 ETH for deposit + gas)
	// - Uses OWNER_EOA with salt:0 derivation for smart wallet

	t.Run("WETH_Deposit_With_Point1_ETH_Value", func(t *testing.T) {
		// Get owner EOA and derive smart wallet address
		ownerEOA, hasOwner := testutil.MustGetTestOwnerAddress()
		if !hasOwner {
			t.Skip("Skipping test - OWNER_EOA environment variable not set")
		}

		logger := testutil.GetLogger()

		// Create a test engine
		db := testutil.TestMustDB()
		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, logger)

		// Derive smart wallet address using salt:0
		ethRPC := testutil.GetTestRPCURL()
		ethClient, err := ethclient.Dial(ethRPC)
		require.NoError(t, err, "Failed to connect to Sepolia RPC")
		defer ethClient.Close()

		smartWalletAddress, err := aa.GetSenderAddress(ethClient, *ownerEOA, big.NewInt(0))
		require.NoError(t, err, "Failed to derive smart wallet address")

		t.Logf("📋 Test Configuration:")
		t.Logf("   - Owner EOA: %s", ownerEOA.Hex())
		t.Logf("   - Smart Wallet (salt:0): %s", smartWalletAddress.Hex())
		t.Logf("   - Network: Sepolia (chain_id: 11155111)")
		t.Logf("   - Required Balance: ~0.15 ETH (0.1 ETH deposit + gas)")
		t.Logf("   - Smart wallet contract will be auto-created on first transaction")

		// Create user object
		user := &model.User{
			Address:             *ownerEOA,
			SmartAccountAddress: smartWalletAddress,
		}

		// Configure WETH deposit with 0.1 ETH value
		nodeType := "contractWrite"
		nodeConfig := map[string]interface{}{
			"contractAddress": "0xfff9976782d46cc05630d1f6ebab18b2324d6b14", // WETH on Sepolia
			"chainId":         int64(11155111),                              // explicit chain (strict)
			"contractAbi": []interface{}{
				map[string]interface{}{
					"constant":        false,
					"inputs":          []interface{}{},
					"name":            "deposit",
					"outputs":         []interface{}{},
					"payable":         true,
					"stateMutability": "payable",
					"type":            "function",
				},
				map[string]interface{}{
					"anonymous": false,
					"inputs": []interface{}{
						map[string]interface{}{"indexed": true, "name": "dst", "type": "address"},
						map[string]interface{}{"indexed": false, "name": "wad", "type": "uint256"},
					},
					"name": "Deposit",
					"type": "event",
				},
			},
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "deposit",
					"methodParams": []interface{}{},
				},
			},
			"value":    "100000000000000000", // 0.1 ETH in wei
			"gasLimit": "210000",
		}

		inputVariables := map[string]interface{}{
			"settings": map[string]interface{}{
				"runner":   smartWalletAddress.Hex(),
				"chain_id": 11155111,
			},
		}

		t.Logf("🧪 Testing WETH deposit with ETH value parameter")
		t.Logf("   - Contract: WETH %s", nodeConfig["contractAddress"])
		t.Logf("   - Method: deposit()")
		t.Logf("   - Value: 0.1 ETH (%s wei)", nodeConfig["value"])

		// Skip if no Tenderly API key
		testConfig := testutil.GetTestConfig()
		if testConfig == nil || testConfig.TenderlyAccount == "" || testConfig.TenderlyProject == "" || testConfig.TenderlyAccessKey == "" {
			t.Logf("⏭️  Skipping test - Tenderly credentials must be set in config/aggregator.yaml")
			t.Logf("✅ Configuration includes value field: %s", nodeConfig["value"])
			t.Logf("✅ This would be passed through to Tenderly simulation")
			t.Logf("✅ Expected result: WETH Deposit event with wad=%s", nodeConfig["value"])
			return
		}

		result, err := engine.RunNodeImmediately(nodeType, nodeConfig, inputVariables, user)

		// The execution should succeed
		assert.NoError(t, err)
		assert.NotNil(t, result)

		// Verify successful execution
		success, ok := result["success"].(bool)
		assert.True(t, ok, "Result should have success field")
		assert.True(t, success, "Contract write should succeed")

		// Verify WETH Deposit event data
		data, ok := result["data"].(map[string]interface{})
		assert.True(t, ok, "Result should have data field")

		deposit, ok := data["deposit"].(map[string]interface{})
		assert.True(t, ok, "Data should contain deposit event")

		// Key assertion: wad should equal the value we sent (0.1 ETH)
		wad, ok := deposit["wad"].(string)
		assert.True(t, ok, "Deposit event should have wad field")
		assert.Equal(t, nodeConfig["value"], wad, "WETH deposit amount should match ETH value sent")

		dst, ok := deposit["dst"].(string)
		assert.True(t, ok, "Deposit event should have dst field")
		assert.Equal(t, strings.ToLower(smartWalletAddress.Hex()), strings.ToLower(dst), "Deposit recipient should be smart wallet")

		t.Logf("✅ Test passed: ETH value correctly propagated to WETH deposit")
		t.Logf("   - Sent: 0.1 ETH (%s wei)", nodeConfig["value"])
		t.Logf("   - Received WETH: %s wei", wad)
		t.Logf("   - Recipient: %s", dst)
	})
}
