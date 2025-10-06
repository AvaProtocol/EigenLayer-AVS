package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestInputVariables_UsageExamples demonstrates exactly how to reference input variables in nodes
func TestInputVariables_UsageExamples(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// Define input variables that will be used in nodes
	inputVariables := map[string]interface{}{
		// Simple variables
		"tokenContract": "0xA0b86a33E6441e4EF45bAcfCaAd5C7f899342E38",
		"recipient":     "0x742d35Cc6634C0532925a3b8D2C0e3e0C8C8E1c7",
		"amount":        "1000000", // 1 USDC (6 decimals)
		"gasLimit":      200000,

		// Complex object
		"swapConfig": map[string]interface{}{
			"routerAddress": "0x7a250d5630B4cF539739dF2C5dAcb4c659F2488D",
			"amountIn":      "1000000000000000000", // 1 ETH
			"amountOutMin":  "0",
			"path":          []interface{}{"0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2", "0xA0b86a33E6441e4EF45bAcfCaAd5C7f899342E38"},
		},

		// User configuration
		"userConfig": map[string]interface{}{
			"slippage": 0.5,
			"deadline": 1703123400,
		},
	}

	// Create nodes that demonstrate how to reference input variables
	nodes := []*avsproto.TaskNode{
		{
			Id:   "example_node",
			Name: "inputVariableExamples",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: `
							// ==========================================
							// HOW TO REFERENCE INPUT VARIABLES IN NODES
							// ==========================================
							
							// 1. Simple variable access
							// Input: "tokenContract": "0xA0b86a33E6441e4EF45bAcfCaAd5C7f899342E38"
							// Usage: tokenContract
							const contract = tokenContract;
							
							// 2. Simple variable access (other types)
							// Input: "amount": "1000000"
							// Usage: amount
							const transferAmount = amount;
							
							// Input: "gasLimit": 200000
							// Usage: gasLimit  
							const gas = gasLimit;
							
							// 3. Object property access
							// Input: "swapConfig": { "routerAddress": "0x...", "amountIn": "1000..." }
							// Usage: swapConfig.routerAddress, swapConfig.amountIn
							const router = swapConfig.routerAddress;
							const amountIn = swapConfig.amountIn;
							const amountOut = swapConfig.amountOutMin;
							
							// 4. Array access
							// Input: "swapConfig": { "path": ["0x...", "0x..."] }
							// Usage: swapConfig.path[0], swapConfig.path[1]
							const tokenA = swapConfig.path[0];
							const tokenB = swapConfig.path[1];
							const pathLength = swapConfig.path.length;
							
							// 5. Nested object access
							// Input: "userConfig": { "slippage": 0.5, "deadline": 1703123400 }
							// Usage: userConfig.slippage, userConfig.deadline
							const slippageTolerance = userConfig.slippage;
							const transactionDeadline = userConfig.deadline;
							
							// Return all accessed variables for verification
							return {
								// Simple variables
								contract: contract,
								recipient: recipient,
								amount: transferAmount,
								gasLimit: gas,
								
								// Object properties
								routerAddress: router,
								amountIn: amountIn,
								amountOutMin: amountOut,
								
								// Array elements
								firstToken: tokenA,
								secondToken: tokenB,
								pathLength: pathLength,
								
								// Nested object properties
								slippage: slippageTolerance,
								deadline: transactionDeadline,
								
								// Verification message
								message: "All input variables accessed successfully!"
							};
						`,
					},
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "trigger1",
		Name: "manualTrigger",
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: &avsproto.ManualTrigger{
				Config: &avsproto.ManualTrigger_Config{
					Lang: avsproto.Lang_LANG_JAVASCRIPT,
					Data: func() *structpb.Value {
						data, _ := structpb.NewValue(map[string]interface{}{
							"test": "data",
						})
						return data
					}(),
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge1",
			Source: "trigger1",
			Target: "example_node",
		},
	}

	// Execute SimulateTask with input variables
	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)
	require.NoError(t, err)
	require.NotNil(t, execution)

	// Verify execution was successful
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution.Status)
	assert.Empty(t, execution.Error)

	// Find the example step
	var exampleStep *avsproto.Execution_Step
	for _, step := range execution.Steps {
		if step.Name == "inputVariableExamples" {
			exampleStep = step
			break
		}
	}
	require.NotNil(t, exampleStep, "Example step not found")

	// Verify the step was successful
	assert.True(t, exampleStep.Success)
	assert.Empty(t, exampleStep.Error)

	// Verify the output contains all expected values
	require.NotNil(t, exampleStep.GetCustomCode())
	require.NotNil(t, exampleStep.GetCustomCode().Data)

	outputInterface := exampleStep.GetCustomCode().Data.AsInterface()
	outputMap, ok := outputInterface.(map[string]interface{})
	require.True(t, ok, "Expected output to be a map")
	require.NotNil(t, outputMap)

	// Verify simple variables
	assert.Equal(t, "0xA0b86a33E6441e4EF45bAcfCaAd5C7f899342E38", outputMap["contract"])
	assert.Equal(t, "0x742d35Cc6634C0532925a3b8D2C0e3e0C8C8E1c7", outputMap["recipient"])
	assert.Equal(t, "1000000", outputMap["amount"])
	assert.Equal(t, float64(200000), outputMap["gasLimit"])

	// Verify object properties
	assert.Equal(t, "0x7a250d5630B4cF539739dF2C5dAcb4c659F2488D", outputMap["routerAddress"])
	assert.Equal(t, "1000000000000000000", outputMap["amountIn"])
	assert.Equal(t, "0", outputMap["amountOutMin"])

	// Verify array elements
	assert.Equal(t, "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2", outputMap["firstToken"])
	assert.Equal(t, "0xA0b86a33E6441e4EF45bAcfCaAd5C7f899342E38", outputMap["secondToken"])
	assert.Equal(t, float64(2), outputMap["pathLength"])

	// Verify nested object properties
	assert.Equal(t, 0.5, outputMap["slippage"])
	assert.Equal(t, float64(1703123400), outputMap["deadline"])

	// Verify success message
	assert.Equal(t, "All input variables accessed successfully!", outputMap["message"])

	t.Logf("✅ Input variable usage examples test completed successfully")

	// Print examples for documentation
	t.Logf("\n"+
		"=== INPUT VARIABLES USAGE EXAMPLES ===\n"+
		"Input Variables Defined:\n"+
		"  tokenContract: %v\n"+
		"  recipient: %v\n"+
		"  amount: %v\n"+
		"  gasLimit: %v\n"+
		"  swapConfig.routerAddress: %v\n"+
		"  swapConfig.path[0]: %v\n"+
		"  userConfig.slippage: %v\n"+
		"\n"+
		"How to Reference in Nodes:\n"+
		"  Simple: tokenContract, recipient, amount, gasLimit\n"+
		"  Object properties: swapConfig.routerAddress, swapConfig.amountIn\n"+
		"  Array elements: swapConfig.path[0], swapConfig.path[1]\n"+
		"  Nested properties: userConfig.slippage, userConfig.deadline\n",
		inputVariables["tokenContract"],
		inputVariables["recipient"],
		inputVariables["amount"],
		inputVariables["gasLimit"],
		inputVariables["swapConfig"].(map[string]interface{})["routerAddress"],
		inputVariables["swapConfig"].(map[string]interface{})["path"].([]interface{})[0],
		inputVariables["userConfig"].(map[string]interface{})["slippage"])
}
