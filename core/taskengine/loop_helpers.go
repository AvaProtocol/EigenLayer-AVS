package taskengine

import (
	"fmt"
	"strings"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Loop Helper Functions
//
// This file contains shared helper functions for loop execution.
// These functions support template variable substitution, nested node creation, and
// input variable resolution for loop iterations in VM.executeLoopWithQueue.
// These functions handle template variable substitution, nested node creation, and
// input variable resolution for loop iterations.

// substituteTemplateVariables replaces template variables like {{value}} and {{index}} with actual values.
// This is a pure function with no VM dependency.
//
// Example:
//
//	text = "Transfer {{value}} to {{recipient}}"
//	iterInputs = {"value": "100", "recipient": "0x123"}
//	result = "Transfer 100 to 0x123"
func substituteTemplateVariables(text string, iterInputs map[string]interface{}) string {
	if text == "" {
		return text
	}

	// Simple template variable substitution
	// Replace {{value}} with the current iteration value
	// Replace {{index}} with the current iteration index
	result := text

	for varName, varValue := range iterInputs {
		placeholder := fmt.Sprintf("{{%s}}", varName)
		replacement := fmt.Sprintf("%v", varValue)
		result = strings.ReplaceAll(result, placeholder, replacement)
	}

	return result
}

// processContractWriteTemplates processes template variables in contract write configuration.
// This function creates a copy of the configuration with template variables substituted.
//
// CRITICAL: ABI is NEVER subject to template substitution for security reasons.
// CallData should be used as-is (literal hex string).
//
// The function supports advanced preprocessing for dot notation (e.g., {{value.address}})
// by first applying loop-specific substitution, then VM variable preprocessing.
func processContractWriteTemplates(vm *VM, contractWrite *avsproto.ContractWriteNode, iterInputs map[string]interface{}) *avsproto.ContractWriteNode {
	// Create a copy of the contract write configuration
	processed := &avsproto.ContractWriteNode{
		Config: &avsproto.ContractWriteNode_Config{
			ContractAddress: substituteTemplateVariables(contractWrite.Config.ContractAddress, iterInputs),
			ContractAbi:     contractWrite.Config.ContractAbi, // ⚠️ CRITICAL: ABI is NEVER subject to template substitution
			CallData:        substituteTemplateVariables(contractWrite.Config.CallData, iterInputs),
		},
	}

	// Process method calls
	for _, methodCall := range contractWrite.Config.MethodCalls {
		var processedCallData *string
		if methodCall.CallData != nil {
			// Use callData as-is without template substitution (callData should be literal hex string)
			processedCallData = methodCall.CallData
		}
		processedMethodCall := &avsproto.ContractWriteNode_MethodCall{
			CallData:   processedCallData,
			MethodName: substituteTemplateVariables(methodCall.MethodName, iterInputs),
		}

		// Process methodParams with advanced template variable preprocessing
		// This supports dot notation like {{value.address}} in addition to simple {{value}} substitution
		processedMethodParams := make([]string, len(methodCall.MethodParams))
		for i, param := range methodCall.MethodParams {
			// First apply loop-specific template substitution ({{value}}, {{index}})
			paramWithLoopVars := substituteTemplateVariables(param, iterInputs)
			// Then apply advanced preprocessing for dot notation and other VM variables
			processedMethodParams[i] = vm.preprocessTextWithVariableMapping(paramWithLoopVars)
		}
		processedMethodCall.MethodParams = processedMethodParams

		processed.Config.MethodCalls = append(processed.Config.MethodCalls, processedMethodCall)
	}

	return processed
}

// processContractReadTemplates processes template variables in contract read configuration.
// Similar to processContractWriteTemplates but includes support for applyToFields copying.
//
// CRITICAL: ABI is NEVER subject to template substitution for security reasons.
func processContractReadTemplates(vm *VM, contractRead *avsproto.ContractReadNode, iterInputs map[string]interface{}) *avsproto.ContractReadNode {
	// Create a copy of the contract read configuration
	processed := &avsproto.ContractReadNode{
		Config: &avsproto.ContractReadNode_Config{
			ContractAddress: substituteTemplateVariables(contractRead.Config.ContractAddress, iterInputs),
			ContractAbi:     contractRead.Config.ContractAbi, // ⚠️ CRITICAL: ABI is NEVER subject to template substitution
		},
	}

	// Process method calls
	for _, methodCall := range contractRead.Config.MethodCalls {
		var processedCallData *string
		if methodCall.CallData != nil {
			// Use callData as-is without template substitution (callData should be literal hex string)
			processedCallData = methodCall.CallData
		}
		processedMethodCall := &avsproto.ContractReadNode_MethodCall{
			CallData:      processedCallData,
			MethodName:    substituteTemplateVariables(methodCall.MethodName, iterInputs),
			ApplyToFields: make([]string, len(methodCall.ApplyToFields)),
		}

		// Process methodParams with advanced template variable preprocessing
		// This supports dot notation like {{value.address}} in addition to simple {{value}} substitution
		processedMethodParams := make([]string, len(methodCall.MethodParams))
		for i, param := range methodCall.MethodParams {
			// First apply loop-specific template substitution ({{value}}, {{index}})
			paramWithLoopVars := substituteTemplateVariables(param, iterInputs)
			// Then apply advanced preprocessing for dot notation and other VM variables
			processedMethodParams[i] = vm.preprocessTextWithVariableMapping(paramWithLoopVars)
		}
		processedMethodCall.MethodParams = processedMethodParams

		// Copy applyToFields (no template substitution needed for field names)
		copy(processedMethodCall.ApplyToFields, methodCall.ApplyToFields)

		processed.Config.MethodCalls = append(processed.Config.MethodCalls, processedMethodCall)
	}

	return processed
}

// processRestApiTemplates processes template variables in REST API configuration.
// This function is simpler than contract node processing as it doesn't need ABI handling.
//
// Both header keys and values are subject to template substitution.
func processRestApiTemplates(restApi *avsproto.RestAPINode, iterInputs map[string]interface{}) *avsproto.RestAPINode {
	// Create a copy of the REST API configuration
	processed := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url:    substituteTemplateVariables(restApi.Config.Url, iterInputs),
			Method: substituteTemplateVariables(restApi.Config.Method, iterInputs),
			Body:   substituteTemplateVariables(restApi.Config.Body, iterInputs),
		},
	}

	// Process headers
	if restApi.Config.Headers != nil {
		processed.Config.Headers = make(map[string]string)
		for key, value := range restApi.Config.Headers {
			processedKey := substituteTemplateVariables(key, iterInputs)
			processedValue := substituteTemplateVariables(value, iterInputs)
			processed.Config.Headers[processedKey] = processedValue
		}
	}

	return processed
}

// createNestedNodeFromLoop creates a TaskNode for loop iteration from LoopNode definition.
// This function handles the creation of nested nodes for all supported node types.
//
// Supported node types:
// - ETHTransferNode
// - ContractWriteNode (with template processing)
// - ContractReadNode (with template processing)
// - GraphQLQueryNode
// - RestAPINode (with template processing)
// - CustomCodeNode
//
// Returns nil if no nested node is specified in the loop definition.
func createNestedNodeFromLoop(loopNodeDef *avsproto.LoopNode, iterationStepID string, iterInputs map[string]interface{}, vm *VM) *avsproto.TaskNode {
	nodeName := fmt.Sprintf("loop_iteration_%s", iterationStepID)

	if ethTransfer := loopNodeDef.GetEthTransfer(); ethTransfer != nil {
		return &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_ETH_TRANSFER,
			TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: ethTransfer},
		}
	} else if contractWrite := loopNodeDef.GetContractWrite(); contractWrite != nil {
		// Don't do template substitution here - let execution-time processing handle it
		// This allows dot notation like {{value.field}} to work correctly
		return &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
			TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: contractWrite},
		}
	} else if contractRead := loopNodeDef.GetContractRead(); contractRead != nil {
		// Don't do template substitution here - let execution-time processing handle it
		return &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_CONTRACT_READ,
			TaskType: &avsproto.TaskNode_ContractRead{ContractRead: contractRead},
		}
	} else if graphqlQuery := loopNodeDef.GetGraphqlDataQuery(); graphqlQuery != nil {
		return &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_GRAPHQL_QUERY,
			TaskType: &avsproto.TaskNode_GraphqlQuery{GraphqlQuery: graphqlQuery},
		}
	} else if restApi := loopNodeDef.GetRestApi(); restApi != nil {
		// Don't do template substitution here - let execution-time processing handle it
		return &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_REST_API,
			TaskType: &avsproto.TaskNode_RestApi{RestApi: restApi},
		}
	} else if customCode := loopNodeDef.GetCustomCode(); customCode != nil {
		return &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{CustomCode: customCode},
		}
	}

	return nil
}

// buildIterInputs merges parent VM variables with iteration-specific variables
// to construct the input map for a loop iteration.
//
// Parameters:
//   - parentVars: All variables from the parent VM (settings, apContext, etc.)
//   - iterVal: The name of the iteration value variable (e.g., "value", "item")
//   - iterKey: The name of the iteration index variable (e.g., "index", "i") - optional
//   - index: The current iteration index (0-based)
//   - item: The current iteration item value
//
// Returns a new map containing both parent variables and iteration-specific variables.
// Does not modify the input maps.
func buildIterInputs(parentVars map[string]interface{}, iterVal string, iterKey string, index int, item interface{}) map[string]interface{} {
	iterInputs := make(map[string]interface{})
	// Copy parent variables (settings, apContext, etc.)
	for key, value := range parentVars {
		iterInputs[key] = value
	}
	// Add iteration-specific variables
	iterInputs[iterVal] = item
	if iterKey != "" {
		iterInputs[iterKey] = index
	}
	return iterInputs
}

// resolveLoopInputVariable resolves and validates the loop input variable.
// This function handles the complexity of different input variable formats and wrapping.
//
// The function tries two approaches:
// 1. Resolve as node ID (workflow execution): node_name → output variable
// 2. Resolve as direct variable name (immediate execution): variable_name → value
//
// It also handles wrapped data fields (common for trigger variables):
// - If the variable is a map with a "data" field, extract the array from that field
// - Supports both []interface{} and []any types
//
// Returns:
//   - []interface{}: The resolved input array
//   - string: The actual variable name that was used
//   - error: Error if variable not found, not an array, or invalid format
func resolveLoopInputVariable(vm *VM, inputNodeName string) ([]interface{}, string, error) {
	var inputVarName string
	var inputVar interface{}
	var exists bool

	// First try: resolve as node ID (workflow execution)
	inputVarName = vm.GetNodeNameAsVar(inputNodeName)
	vm.mu.Lock()
	inputVar, exists = vm.vars[inputVarName]
	vm.mu.Unlock()

	// Second try: use inputNodeName directly as variable name (immediate execution)
	if !exists {
		inputVarName = inputNodeName
		vm.mu.Lock()
		inputVar, exists = vm.vars[inputVarName]
		vm.mu.Unlock()
	}

	if !exists {
		return nil, "", fmt.Errorf("input variable %s not found (tried both as node name and direct variable)", inputVarName)
	}

	// Try to convert directly to array
	inputArray, ok := inputVar.([]interface{})
	if !ok {
		// Try to extract from data field if wrapped (common for trigger variables)
		if dataMap, ok := inputVar.(map[string]interface{}); ok {
			if dataValue, hasData := dataMap["data"]; hasData {
				// Try different array types that might be present
				if dataArray, ok := dataValue.([]interface{}); ok {
					inputArray = dataArray
				} else if dataSlice, ok := dataValue.([]any); ok {
					// Handle []any type
					inputArray = make([]interface{}, len(dataSlice))
					for i, v := range dataSlice {
						inputArray[i] = v
					}
				} else {
					// Data field exists but is not an array
					return nil, inputVarName, fmt.Errorf("input variable %s.data is type %T, expected array", inputVarName, dataValue)
				}
			} else {
				// No data field found
				return nil, inputVarName, fmt.Errorf("input variable %s is not an array and has no 'data' field (available keys: %v)", inputVarName, GetMapKeys(dataMap))
			}
		} else {
			return nil, inputVarName, fmt.Errorf("input variable %s is type %T, expected array or object with 'data' field", inputVarName, inputVar)
		}
	}

	return inputArray, inputVarName, nil
}
