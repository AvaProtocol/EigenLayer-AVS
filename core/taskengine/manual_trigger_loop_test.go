package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoopNode_ManualTriggerDataAccess(t *testing.T) {
	t.Run("should access ManualTrigger.data automatically", func(t *testing.T) {
		// Create VM
		vm := NewVM()
		if vm == nil {
			t.Fatal("Failed to create VM")
		}

		// Simulate the exact scenario from the user's input
		// This matches the structure that comes from the client
		inputVariables := map[string]interface{}{
			"manualTrigger": map[string]interface{}{
				"data": []interface{}{
					map[string]interface{}{"key": "value1"},
					map[string]interface{}{"key": "value2"},
				},
				"input": map[string]interface{}{
					"data": []interface{}{
						map[string]interface{}{"key": "value1"},
						map[string]interface{}{"key": "value2"},
					},
					"headers":    []interface{}{map[string]interface{}{"key": "headerKey", "value": "headerValue"}},
					"pathParams": []interface{}{map[string]interface{}{"key": "pathKey", "value": "pathValue"}},
				},
			},
			"workflowContext": map[string]interface{}{
				"id":             "b11e5165-be36-4b97-a36a-52ac8c2c67ca",
				"chainId":        nil,
				"name":           "Jul 11, 2025 5:04 PM",
				"userId":         "03396f6d-9fb7-49f9-96a5-9025c4998e40",
				"eoaAddress":     "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9",
				"startAt":        "2025-07-12T00:04:25.789Z",
				"expiredAt":      "2025-08-12T00:04:25.789Z",
				"maxExecution":   0,
				"status":         "draft",
				"completedAt":    nil,
				"lastRanAt":      nil,
				"executionCount": nil,
				"createdAt":      "2025-07-12T00:04:25.794Z",
				"updatedAt":      "2025-07-23T05:49:59.386Z",
			},
		}

		// Create the exact loop node configuration from the user's example
		nodeConfig := map[string]interface{}{
			"inputNodeName": "manualTrigger", // This should automatically access .data
			"iterVal":       "value",
			"iterKey":       "index",
			"executionMode": "sequential",
			"runner": map[string]interface{}{
				"type": "customCode",
				"config": map[string]interface{}{
					"lang":   avsproto.Lang_LANG_JAVASCRIPT,
					"source": "return value;",
				},
			},
		}

		// Create the task node using the same method as the client
		node, err := CreateNodeFromType("loop", nodeConfig, "")
		require.NoError(t, err, "Should create loop node successfully")
		require.NotNil(t, node, "Node should not be nil")

		// Set a valid JavaScript identifier name
		node.Name = "testLoopNode"

		t.Logf("🧪 Testing loop node with inputNodeName: %s", nodeConfig["inputNodeName"])
		t.Logf("📥 Input data structure: %+v", inputVariables["manualTrigger"])

		// Execute the loop node with the input variables
		executionStep, err := vm.RunNodeWithInputs(node, inputVariables)

		// Verify the execution was successful
		require.NoError(t, err, "Loop node execution should not return error")
		require.NotNil(t, executionStep, "Execution step should not be nil")
		assert.True(t, executionStep.Success, "Loop node execution should be successful. Error: %s", executionStep.Error)

		// Verify the loop processed the correct number of items
		if loopOutput := executionStep.GetLoop(); loopOutput != nil && loopOutput.Data != nil {
			results := loopOutput.Data.AsInterface()
			t.Logf("📤 Loop results: %+v", results)

			if resultsArray, ok := results.([]interface{}); ok {
				assert.Len(t, resultsArray, 2, "Should process 2 items from manualTrigger.data")

				// Verify each result matches the expected output
				expectedResults := []interface{}{
					map[string]interface{}{"key": "value1"},
					map[string]interface{}{"key": "value2"},
				}

				for i, expectedResult := range expectedResults {
					assert.Equal(t, expectedResult, resultsArray[i], "Result %d should match expected value", i)
				}

				t.Logf("✅ Successfully processed %d items from manualTrigger.data", len(resultsArray))
			} else {
				t.Errorf("❌ Expected array results, got %T: %+v", results, results)
			}
		} else {
			t.Error("❌ Loop output data is nil")
		}
	})

	t.Run("should handle error when data field is not an array", func(t *testing.T) {
		vm := NewVM()
		if vm == nil {
			t.Fatal("Failed to create VM")
		}

		// Test with non-array data
		inputVariables := map[string]interface{}{
			"manualTrigger": map[string]interface{}{
				"data": "not an array", // This should cause an error
				"input": map[string]interface{}{
					"data": "not an array",
				},
			},
		}

		nodeConfig := map[string]interface{}{
			"inputNodeName": "manualTrigger",
			"iterVal":       "value",
			"iterKey":       "index",
			"executionMode": "sequential",
			"runner": map[string]interface{}{
				"type": "customCode",
				"config": map[string]interface{}{
					"lang":   avsproto.Lang_LANG_JAVASCRIPT,
					"source": "return value;",
				},
			},
		}

		node, err := CreateNodeFromType("loop", nodeConfig, "")
		require.NoError(t, err)
		node.Name = "testLoopNode"

		// Execute the loop node - this should fail with a clear error message
		executionStep, err := vm.RunNodeWithInputs(node, inputVariables)

		if err != nil {
			// Error returned directly from RunNodeWithInputs
			assert.Contains(t, err.Error(), "expected array", "Error message should indicate array was expected")
			t.Logf("✅ Correctly handled non-array data with error: %s", err.Error())
		} else {
			// Error in execution step
			require.NotNil(t, executionStep, "Execution step should not be nil")
			assert.False(t, executionStep.Success, "Loop node execution should fail")
			assert.Contains(t, executionStep.Error, "expected array", "Error message should indicate array was expected")
			t.Logf("✅ Correctly handled non-array data with error: %s", executionStep.Error)
		}
	})

	t.Run("should handle missing data field gracefully", func(t *testing.T) {
		vm := NewVM()
		if vm == nil {
			t.Fatal("Failed to create VM")
		}

		// Test with missing data field
		inputVariables := map[string]interface{}{
			"manualTrigger": map[string]interface{}{
				"input": map[string]interface{}{
					"headers": []interface{}{},
				},
				// No "data" field
			},
		}

		nodeConfig := map[string]interface{}{
			"inputNodeName": "manualTrigger",
			"iterVal":       "value",
			"iterKey":       "index",
			"executionMode": "sequential",
			"runner": map[string]interface{}{
				"type": "customCode",
				"config": map[string]interface{}{
					"lang":   avsproto.Lang_LANG_JAVASCRIPT,
					"source": "return value;",
				},
			},
		}

		node, err := CreateNodeFromType("loop", nodeConfig, "")
		require.NoError(t, err)
		node.Name = "testLoopNode"

		// Execute the loop node - this should fail with a clear error message
		executionStep, err := vm.RunNodeWithInputs(node, inputVariables)

		if err != nil {
			// Error returned directly from RunNodeWithInputs
			assert.Contains(t, err.Error(), "no 'data' field", "Error message should indicate missing data field")
			t.Logf("✅ Correctly handled missing data field with error: %s", err.Error())
		} else {
			// Error in execution step
			require.NotNil(t, executionStep, "Execution step should not be nil")
			assert.False(t, executionStep.Success, "Loop node execution should fail")
			assert.Contains(t, executionStep.Error, "no 'data' field", "Error message should indicate missing data field")
			t.Logf("✅ Correctly handled missing data field with error: %s", executionStep.Error)
		}
	})
}

func TestLoopNode_ContractWrite_Approve_PerIterationData(t *testing.T) {
	// Fast, isolated test: simulate a loop over two approve calls and assert per-iteration data is populated
	vm := NewVM()
	require.NotNil(t, vm)

	// Force simulation path for contract write
	vm.SetSimulation(true)

	// Minimal Tenderly client stub is required by processor; use real initializer with test config
	logger := testutil.GetLogger()
	testConfig := testutil.GetTestConfig()
	require.NotNil(t, testConfig)
	tenderlyClient := NewTenderlyClient(testConfig, logger)
	require.NotNil(t, tenderlyClient)
	// Initialize VM with smart wallet config
	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	vmWithCfg, err := NewVMWithData(nil, nil, smartWalletConfig, nil)
	require.NoError(t, err)
	require.NotNil(t, vmWithCfg)
	vm = vmWithCfg
	vm.tenderlyClient = tenderlyClient
	vm.SetSimulation(true)

	// Provide settings.runner and chain_id expected by ContractWrite
	inputVariables := map[string]interface{}{
		"writeParams": []interface{}{
			map[string]interface{}{"spender": "0x0000000000000000000000000000000000000001", "amount": "0"},
			map[string]interface{}{"spender": "0x0000000000000000000000000000000000000001", "amount": "0"},
		},
		"settings": map[string]interface{}{
			"runner":   "0x5a8A8a79DdF433756D4D97DCCE33334D9E218856",
			"chain_id": int64(11155111),
			"chain":    "sepolia",
		},
	}

	// Build loop node with ContractWrite runner (USDC approve signature)
	nodeConfig := map[string]interface{}{
		"inputNodeName": "writeParams",
		"iterVal":       "value",
		"iterKey":       "index",
		"executionMode": "sequential",
		"runner": map[string]interface{}{
			"type": "contractWrite",
			"config": map[string]interface{}{
				"contractAddress": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
				"contractAbi": []interface{}{
					map[string]interface{}{
						"type":            "function",
						"name":            "approve",
						"stateMutability": "nonpayable",
						"inputs": []interface{}{
							map[string]interface{}{"name": "spender", "type": "address"},
							map[string]interface{}{"name": "amount", "type": "uint256"},
						},
						"outputs": []interface{}{map[string]interface{}{"name": "", "type": "bool"}},
					},
				},
				"methodCalls": []interface{}{
					map[string]interface{}{
						"methodName":   "approve",
						"methodParams": []interface{}{"{{value.spender}}", "{{value.amount}}"},
					},
				},
			},
		},
	}

	node, err := CreateNodeFromType("loop", nodeConfig, "")
	require.NoError(t, err)
	require.NotNil(t, node)
	node.Name = "testLoopContractWrite"

	// Execute
	step, err := vm.RunNodeWithInputs(node, inputVariables)
	require.NoError(t, err)
	require.NotNil(t, step)
	require.True(t, step.Success, "execution failed: %s", step.Error)

	// Assert per-iteration data are non-nil and shaped as approve:true
	loopOutput := step.GetLoop()
	require.NotNil(t, loopOutput)
	require.NotNil(t, loopOutput.Data)
	iface := loopOutput.Data.AsInterface()
	arr, ok := iface.([]interface{})
	require.True(t, ok, "expected array, got %T", iface)
	require.Len(t, arr, 2)

	for i, it := range arr {
		require.NotNil(t, it, "iteration %d data is nil", i)
		// Expect a map like { approve: true } when value is boolean
		if m, ok := it.(map[string]interface{}); ok {
			if v, exists := m["approve"]; exists {
				// bool true is ideal; some sims may return "true" string
				switch val := v.(type) {
				case bool:
					assert.True(t, val, "iteration %d approve should be true", i)
				case string:
					assert.Equal(t, "true", val, "iteration %d approve string should be 'true'", i)
				}
			}
		}
	}
}

func TestRestApiStandardFormat(t *testing.T) {
	// Create VM
	vm := NewVM()
	if vm == nil {
		t.Fatal("Failed to create VM")
	}

	// Add input variables to VM
	inputVariables := map[string]interface{}{
		"manualTrigger": map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{"key": "value1"},
				map[string]interface{}{"key": "value2"},
			},
			"input": map[string]interface{}{
				"data":       []interface{}{map[string]interface{}{"key": "value1"}, map[string]interface{}{"key": "value2"}},
				"headers":    []interface{}{map[string]interface{}{"key": "headerKey", "value": "headerValue"}},
				"pathParams": []interface{}{map[string]interface{}{"key": "pathKey", "value": "pathValue"}},
			},
		},
	}

	for key, value := range inputVariables {
		vm.AddVar(key, value)
	}

	// Create a loop node with REST API runner to test standard format
	loopNode, err := CreateNodeFromType("loop", map[string]interface{}{
		"inputNodeName": "manualTrigger",
		"iterVal":       "value",
		"iterKey":       "index",
		"executionMode": "sequential",
		"runner": map[string]interface{}{
			"type": "restApi",
			"config": map[string]interface{}{
				"url":    "https://mock-api.ap-aggregator.local",
				"method": "GET",
				"body":   "",
			},
		},
	}, "")

	require.NoError(t, err, "Failed to create loop node")
	require.NotNil(t, loopNode, "Loop node should not be nil")

	// Execute the loop node using RunNodeWithInputs
	executionStep, err := vm.RunNodeWithInputs(loopNode, inputVariables)

	// Should succeed
	require.NoError(t, err, "Expected successful execution")
	require.NotNil(t, executionStep, "Expected execution step")
	require.True(t, executionStep.Success, "Expected successful execution, got error: %s", executionStep.Error)

	// Extract the loop result
	loopOutput := executionStep.GetLoop()
	require.NotNil(t, loopOutput, "Expected loop output")
	require.NotNil(t, loopOutput.Data, "Expected loop data")

	// Convert to Go interface
	loopResultInterface := loopOutput.Data.AsInterface()
	loopArray, ok := loopResultInterface.([]interface{})
	require.True(t, ok, "Expected loopResult to be array, got %T: %+v", loopResultInterface, loopResultInterface)
	require.Len(t, loopArray, 2, "Expected 2 loop iterations")

	// Check each iteration result for standard format
	for i, iterResult := range loopArray {
		iterMap, ok := iterResult.(map[string]interface{})
		require.True(t, ok, "Expected iteration %d result to be map, got %T: %+v", i, iterResult, iterResult)

		// Debug: Log the actual response content
		t.Logf("🔍 Iteration %d response: %+v", i, iterMap)

		// Verify we have the standard REST API response format
		status, hasStatus := iterMap["status"]
		require.True(t, hasStatus, "Expected 'status' field in iteration %d result: %+v", i, iterMap)

		statusText, hasStatusText := iterMap["statusText"]
		require.True(t, hasStatusText, "Expected 'statusText' field in iteration %d result: %+v", i, iterMap)

		url, hasUrl := iterMap["url"]
		require.True(t, hasUrl, "Expected 'url' field in iteration %d result: %+v", i, iterMap)

		headers, hasHeaders := iterMap["headers"]
		require.True(t, hasHeaders, "Expected 'headers' field in iteration %d result: %+v", i, iterMap)

		data, hasData := iterMap["data"]
		require.True(t, hasData, "Expected 'data' field in iteration %d result: %+v", i, iterMap)

		// Verify field types and values
		var statusInt int
		switch s := status.(type) {
		case int:
			statusInt = s
		case float64:
			statusInt = int(s)
		default:
			t.Errorf("Expected status to be int or float64 in iteration %d, got %T", i, status)
			continue
		}
		assert.Equal(t, 200, statusInt, "Expected status 200 in iteration %d", i)

		statusTextStr, ok := statusText.(string)
		require.True(t, ok, "Expected statusText to be string in iteration %d, got %T", i, statusText)
		assert.Equal(t, "OK", statusTextStr, "Expected statusText 'OK' in iteration %d", i)

		urlStr, ok := url.(string)
		require.True(t, ok, "Expected url to be string in iteration %d, got %T", i, url)
		assert.Equal(t, "https://mock-api.ap-aggregator.local", urlStr, "Expected correct URL in iteration %d", i)

		headersMap, ok := headers.(map[string]interface{})
		require.True(t, ok, "Expected headers to be map in iteration %d, got %T", i, headers)
		assert.NotEmpty(t, headersMap, "Expected non-empty headers map in iteration %d", i)

		// Verify data field contains the response body content
		dataMap, ok := data.(map[string]interface{})
		require.True(t, ok, "Expected data to be map in iteration %d, got %T: %+v", i, data, data)

		// The data should contain the mock API response fields
		_, hasArgs := dataMap["args"]
		_, hasDataField := dataMap["data"]
		_, hasMethod := dataMap["method"]
		_, hasUrlInData := dataMap["url"]

		assert.True(t, hasArgs, "Expected 'args' field in data")
		assert.True(t, hasDataField, "Expected 'data' field in data")
		assert.True(t, hasMethod, "Expected 'method' field in data")
		assert.True(t, hasUrlInData, "Expected 'url' field in data")

		t.Logf("✅ Iteration %d standard format verified: status=%v, statusText=%s, url=%s, headers=%T, data=%T",
			i, status, statusTextStr, urlStr, headers, data)
	}
}

func TestRegularRestApiStandardFormat(t *testing.T) {
	// Create VM
	vm := NewVM()
	if vm == nil {
		t.Fatal("Failed to create VM")
	}

	// Create a regular REST API node (not in a loop)
	restApiNode, err := CreateNodeFromType("restApi", map[string]interface{}{
		"url":    "https://mock-api.ap-aggregator.local",
		"method": "GET",
		"body":   "",
	}, "")

	require.NoError(t, err, "Failed to create REST API node")
	require.NotNil(t, restApiNode, "REST API node should not be nil")

	// Execute the REST API node using RunNodeWithInputs
	executionStep, err := vm.RunNodeWithInputs(restApiNode, map[string]interface{}{})

	// Should succeed
	require.NoError(t, err, "Expected successful execution")
	require.NotNil(t, executionStep, "Expected execution step")
	require.True(t, executionStep.Success, "Expected successful execution, got error: %s", executionStep.Error)

	// Extract the REST API result
	restApiOutput := executionStep.GetRestApi()
	require.NotNil(t, restApiOutput, "Expected REST API output")
	require.NotNil(t, restApiOutput.Data, "Expected REST API data")

	// Convert to Go interface
	resultInterface := restApiOutput.Data.AsInterface()
	resultMap, ok := resultInterface.(map[string]interface{})
	require.True(t, ok, "Expected result to be map, got %T: %+v", resultInterface, resultInterface)

	// Debug: Log the actual response content
	t.Logf("🔍 Regular REST API response: %+v", resultMap)

	// Verify we have the standard REST API response format
	status, hasStatus := resultMap["status"]
	require.True(t, hasStatus, "Expected 'status' field in result: %+v", resultMap)

	statusText, hasStatusText := resultMap["statusText"]
	require.True(t, hasStatusText, "Expected 'statusText' field in result: %+v", resultMap)

	url, hasUrl := resultMap["url"]
	require.True(t, hasUrl, "Expected 'url' field in result: %+v", resultMap)

	headers, hasHeaders := resultMap["headers"]
	require.True(t, hasHeaders, "Expected 'headers' field in result: %+v", resultMap)

	data, hasData := resultMap["data"]
	require.True(t, hasData, "Expected 'data' field in result: %+v", resultMap)

	// Verify field types and values
	var statusInt int
	switch s := status.(type) {
	case int:
		statusInt = s
	case float64:
		statusInt = int(s)
	default:
		t.Errorf("Expected status to be int or float64, got %T", status)
		return
	}
	assert.Equal(t, 200, statusInt, "Expected status 200")

	statusTextStr, ok := statusText.(string)
	require.True(t, ok, "Expected statusText to be string, got %T", statusText)
	assert.Equal(t, "OK", statusTextStr, "Expected statusText 'OK'")

	urlStr, ok := url.(string)
	require.True(t, ok, "Expected url to be string, got %T", url)
	assert.Equal(t, "https://mock-api.ap-aggregator.local", urlStr, "Expected correct URL")

	headersMap, ok := headers.(map[string]interface{})
	require.True(t, ok, "Expected headers to be map, got %T", headers)
	assert.NotEmpty(t, headersMap, "Expected non-empty headers map")

	// Verify data field contains the response body content
	dataMap, ok := data.(map[string]interface{})
	require.True(t, ok, "Expected data to be map, got %T: %+v", data, data)

	// The data should contain the mock API response fields
	_, hasArgs := dataMap["args"]
	_, hasDataField := dataMap["data"]
	_, hasMethod := dataMap["method"]
	_, hasUrlInData := dataMap["url"]

	assert.True(t, hasArgs, "Expected 'args' field in data")
	assert.True(t, hasDataField, "Expected 'data' field in data")
	assert.True(t, hasMethod, "Expected 'method' field in data")
	assert.True(t, hasUrlInData, "Expected 'url' field in data")

	t.Logf("✅ Regular REST API standard format verified: status=%v, statusText=%s, url=%s, headers=%T, data=%T",
		status, statusTextStr, urlStr, headers, data)
}
