package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestInputVariables_SimulateTask tests input variables functionality in SimulateTask
func TestInputVariables_SimulateTask(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// Create input variables
	inputVariables := map[string]interface{}{
		"userToken": "0x1234567890abcdef",
		"amount":    1000000,
		"recipient": "0xabcdef1234567890",
		"isEnabled": true,
		"config": map[string]interface{}{
			"slippage": 0.5,
			"gasLimit": 200000,
		},
		"tokenList": []interface{}{"USDC", "USDT", "DAI"},
	}

	// Create nodes that reference input variables
	nodes := []*avsproto.TaskNode{
		{
			Id:   "customcode1",
			Name: "testInputVariables",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						// JavaScript code that uses input variables
						Source: `
							// Test basic variable access
							const token = userToken;
							const amt = amount;
							const recip = recipient;
							const enabled = isEnabled;
							
							// Test object access
							const slippage = config.slippage;
							const gas = config.gasLimit;
							
							// Test array access
							const firstToken = tokenList[0];
							const tokenCount = tokenList.length;
							
							return {
								token: token,
								amount: amt,
								recipient: recip,
								enabled: enabled,
								slippage: slippage,
								gasLimit: gas,
								firstToken: firstToken,
								tokenCount: tokenCount,
								message: "Input variables work!"
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
			Target: "customcode1",
		},
	}

	// Execute SimulateTask with input variables
	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)
	require.NoError(t, err)
	require.NotNil(t, execution)

	// Verify execution was successful
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution.Status)
	assert.Empty(t, execution.Error)
	assert.Len(t, execution.Steps, 2) // trigger + custom code node

	// Find the custom code step
	var customCodeStep *avsproto.Execution_Step
	for _, step := range execution.Steps {
		if step.Name == "testInputVariables" {
			customCodeStep = step
			break
		}
	}
	require.NotNil(t, customCodeStep, "Custom code step not found")

	// Verify the step was successful
	assert.True(t, customCodeStep.Success)
	assert.Empty(t, customCodeStep.Error)

	// Verify the output contains expected values from input variables
	require.NotNil(t, customCodeStep.GetCustomCode())
	require.NotNil(t, customCodeStep.GetCustomCode().Data)

	// Convert output to map for easier verification
	outputInterface := customCodeStep.GetCustomCode().Data.AsInterface()
	outputMap, ok := outputInterface.(map[string]interface{})
	require.True(t, ok, "Expected output to be a map")
	require.NotNil(t, outputMap)

	// Verify all input variables were accessible
	assert.Equal(t, "0x1234567890abcdef", outputMap["token"])
	assert.Equal(t, float64(1000000), outputMap["amount"]) // JSON numbers are float64
	assert.Equal(t, "0xabcdef1234567890", outputMap["recipient"])
	assert.Equal(t, true, outputMap["enabled"])
	assert.Equal(t, 0.5, outputMap["slippage"])
	assert.Equal(t, float64(200000), outputMap["gasLimit"])
	assert.Equal(t, "USDC", outputMap["firstToken"])
	assert.Equal(t, float64(3), outputMap["tokenCount"])
	assert.Equal(t, "Input variables work!", outputMap["message"])

	t.Logf("✅ SimulateTask with input variables completed successfully")
}

// TestInputVariables_DeployedTask tests input variables functionality in deployed task execution
func TestInputVariables_DeployedTask(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())

	// Create input variables using protobuf Values (as they would be stored)
	inputVarsProto := make(map[string]*structpb.Value)

	// Basic types
	userTokenValue, _ := structpb.NewValue("0x1234567890abcdef")
	inputVarsProto["userToken"] = userTokenValue

	amountValue, _ := structpb.NewValue(1000000)
	inputVarsProto["amount"] = amountValue

	recipientValue, _ := structpb.NewValue("0xabcdef1234567890")
	inputVarsProto["recipient"] = recipientValue

	isEnabledValue, _ := structpb.NewValue(true)
	inputVarsProto["isEnabled"] = isEnabledValue

	// Complex object
	configObj := map[string]interface{}{
		"slippage": 0.5,
		"gasLimit": 200000,
	}
	configValue, _ := structpb.NewValue(configObj)
	inputVarsProto["config"] = configValue

	// Array
	tokenList := []interface{}{"USDC", "USDT", "DAI"}
	tokenListValue, _ := structpb.NewValue(tokenList)
	inputVarsProto["tokenList"] = tokenListValue

	// Create a task with input variables
	task := &model.Task{
		Task: &avsproto.Task{
			Id:             "test-task-input-vars",
			Owner:          "", // Empty owner to skip wallet validation
			Status:         avsproto.TaskStatus_Active,
			Name:           "Test Input Variables",
			ExecutionCount: 0,
			InputVariables: inputVarsProto, // Store input variables with the task
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "manualTrigger",
				Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
				TriggerType: &avsproto.TaskTrigger_Manual{
					Manual: &avsproto.ManualTrigger{
						Config: &avsproto.ManualTrigger_Config{
							Lang: avsproto.Lang_LANG_JAVASCRIPT,
						},
					},
				},
			},
			Nodes: []*avsproto.TaskNode{
				{
					Id:   "customcode1",
					Name: "testInputVariables",
					Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
					TaskType: &avsproto.TaskNode_CustomCode{
						CustomCode: &avsproto.CustomCodeNode{
							Config: &avsproto.CustomCodeNode_Config{
								// JavaScript code that uses input variables
								Source: `
									// Test basic variable access
									const token = userToken;
									const amt = amount;
									const recip = recipient;
									const enabled = isEnabled;
									
									// Test object access
									const slippage = config.slippage;
									const gas = config.gasLimit;
									
									// Test array access
									const firstToken = tokenList[0];
									const tokenCount = tokenList.length;
									
									return {
										token: token,
										amount: amt,
										recipient: recip,
										enabled: enabled,
										slippage: slippage,
										gasLimit: gas,
										firstToken: firstToken,
										tokenCount: tokenCount,
										message: "Deployed task input variables work!",
										executionType: "deployed"
									};
								`,
							},
						},
					},
				},
			},
			Edges: []*avsproto.TaskEdge{
				{
					Id:     "edge1",
					Source: "trigger1",
					Target: "customcode1",
				},
			},
		},
	}

	// Execute the deployed task
	executionId := ulid.Make().String()
	queueData := &QueueExecutionData{
		ExecutionID:   executionId,
		TriggerType:   avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerOutput: &avsproto.ManualTrigger_Output{},
	}

	execution, err := executor.RunTask(task, queueData)
	require.NoError(t, err)
	require.NotNil(t, execution)

	// Verify execution was successful
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution.Status)
	assert.Empty(t, execution.Error)
	assert.Len(t, execution.Steps, 2) // trigger + custom code node

	// Find the custom code step
	var customCodeStep *avsproto.Execution_Step
	for _, step := range execution.Steps {
		if step.Name == "testInputVariables" {
			customCodeStep = step
			break
		}
	}
	require.NotNil(t, customCodeStep, "Custom code step not found")

	// Verify the step was successful
	assert.True(t, customCodeStep.Success)
	assert.Empty(t, customCodeStep.Error)

	// Verify the output contains expected values from input variables
	require.NotNil(t, customCodeStep.GetCustomCode())
	require.NotNil(t, customCodeStep.GetCustomCode().Data)

	// Convert output to map for easier verification
	outputInterface := customCodeStep.GetCustomCode().Data.AsInterface()
	outputMap, ok := outputInterface.(map[string]interface{})
	require.True(t, ok, "Expected output to be a map")
	require.NotNil(t, outputMap)

	// Verify all input variables were accessible
	assert.Equal(t, "0x1234567890abcdef", outputMap["token"])
	assert.Equal(t, float64(1000000), outputMap["amount"]) // JSON numbers are float64
	assert.Equal(t, "0xabcdef1234567890", outputMap["recipient"])
	assert.Equal(t, true, outputMap["enabled"])
	assert.Equal(t, 0.5, outputMap["slippage"])
	assert.Equal(t, float64(200000), outputMap["gasLimit"])
	assert.Equal(t, "USDC", outputMap["firstToken"])
	assert.Equal(t, float64(3), outputMap["tokenCount"])
	assert.Equal(t, "Deployed task input variables work!", outputMap["message"])
	assert.Equal(t, "deployed", outputMap["executionType"])

	t.Logf("✅ Deployed task with input variables completed successfully")
}

// TestInputVariables_CreateTask tests input variables in task creation
func TestInputVariables_CreateTask(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// Create input variables using protobuf Values
	inputVarsProto := make(map[string]*structpb.Value)

	userTokenValue, _ := structpb.NewValue("0x1234567890abcdef")
	inputVarsProto["userToken"] = userTokenValue

	amountValue, _ := structpb.NewValue(1000000)
	inputVarsProto["amount"] = amountValue

	// Create a CreateTaskReq with input variables
	createReq := &avsproto.CreateTaskReq{
		Trigger: &avsproto.TaskTrigger{
			Id:   "trigger1",
			Name: "manualTrigger",
			Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
			TriggerType: &avsproto.TaskTrigger_Manual{
				Manual: &avsproto.ManualTrigger{
					Config: &avsproto.ManualTrigger_Config{
						Lang: avsproto.Lang_LANG_JAVASCRIPT,
					},
				},
			},
		},
		Nodes: []*avsproto.TaskNode{
			{
				Id:   "customcode1",
				Name: "testNode",
				Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
				TaskType: &avsproto.TaskNode_CustomCode{
					CustomCode: &avsproto.CustomCodeNode{
						Config: &avsproto.CustomCodeNode_Config{
							Source: "return { token: userToken, amount: amount };",
						},
					},
				},
			},
		},
		Edges: []*avsproto.TaskEdge{
			{
				Id:     "edge1",
				Source: "trigger1",
				Target: "customcode1",
			},
		},
		Name:           "Test Task with Input Variables",
		MaxExecution:   10,
		InputVariables: inputVarsProto, // Include input variables in creation request
	}

	// Create the task
	task, err := engine.CreateTask(user, createReq)
	require.NoError(t, err)
	require.NotNil(t, task)

	// Verify input variables were stored with the task
	assert.NotNil(t, task.InputVariables)
	assert.Len(t, task.InputVariables, 2)

	// Verify the values were stored correctly
	storedToken := task.InputVariables["userToken"]
	require.NotNil(t, storedToken)
	assert.Equal(t, "0x1234567890abcdef", storedToken.AsInterface())

	storedAmount := task.InputVariables["amount"]
	require.NotNil(t, storedAmount)
	assert.Equal(t, float64(1000000), storedAmount.AsInterface())

	t.Logf("✅ Task creation with input variables completed successfully")
	t.Logf("Task ID: %s", task.Id)
	t.Logf("Input variables stored: %d", len(task.InputVariables))
}

// TestInputVariables_TemplateResolution tests that input variables are properly resolved in templates
func TestInputVariables_TemplateResolution(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// Create input variables with template-style usage
	inputVariables := map[string]interface{}{
		"contractAddress": "0x1234567890abcdef",
		"methodName":      "transfer",
		"recipient":       "0xabcdef1234567890",
		"amount":          "1000000000000000000", // 1 ETH in wei
	}

	// Create a custom code node that uses template-style variable access
	nodes := []*avsproto.TaskNode{
		{
			Id:   "template_test",
			Name: "templateTest",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						// Test that variables are available for template resolution
						Source: `
							// Variables should be directly accessible
							if (typeof contractAddress === 'undefined') {
								throw new Error('contractAddress not available');
							}
							if (typeof methodName === 'undefined') {
								throw new Error('methodName not available');
							}
							if (typeof recipient === 'undefined') {
								throw new Error('recipient not available');
							}
							if (typeof amount === 'undefined') {
								throw new Error('amount not available');
							}
							
							return {
								contract: contractAddress,
								method: methodName,
								to: recipient,
								value: amount,
								templateResolution: true
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
			Target: "template_test",
		},
	}

	// Execute SimulateTask
	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)
	require.NoError(t, err)
	require.NotNil(t, execution)

	// Verify execution was successful
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution.Status)
	assert.Empty(t, execution.Error)

	// Find the template test step
	var templateStep *avsproto.Execution_Step
	for _, step := range execution.Steps {
		if step.Name == "templateTest" {
			templateStep = step
			break
		}
	}
	require.NotNil(t, templateStep, "Template test step not found")

	// Verify the step was successful (no undefined variable errors)
	assert.True(t, templateStep.Success)
	assert.Empty(t, templateStep.Error)

	// Verify the output contains resolved template values
	require.NotNil(t, templateStep.GetCustomCode())
	require.NotNil(t, templateStep.GetCustomCode().Data)
	outputInterface := templateStep.GetCustomCode().Data.AsInterface()
	outputMap, ok := outputInterface.(map[string]interface{})
	require.True(t, ok, "Expected output to be a map")
	require.NotNil(t, outputMap)

	assert.Equal(t, "0x1234567890abcdef", outputMap["contract"])
	assert.Equal(t, "transfer", outputMap["method"])
	assert.Equal(t, "0xabcdef1234567890", outputMap["to"])
	assert.Equal(t, "1000000000000000000", outputMap["value"])
	assert.Equal(t, true, outputMap["templateResolution"])

	t.Logf("✅ Template resolution with input variables completed successfully")
}

// TestInputVariables_EmptyInputVariables tests behavior when no input variables are provided
func TestInputVariables_EmptyInputVariables(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// No input variables provided
	var inputVariables map[string]interface{}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "customcode1",
			Name: "testEmpty",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: `
							// Should work without input variables
							return {
								message: "No input variables needed",
								success: true
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
			Target: "customcode1",
		},
	}

	// Execute SimulateTask with no input variables
	execution, err := engine.SimulateTask(user, trigger, nodes, edges, inputVariables)
	require.NoError(t, err)
	require.NotNil(t, execution)

	// Verify execution was successful even without input variables
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution.Status)
	assert.Empty(t, execution.Error)

	t.Logf("✅ Empty input variables test completed successfully")
}
