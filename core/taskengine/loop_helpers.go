package taskengine

import (
	"fmt"
	"math/big"
	"strings"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Loop Helper Functions
//
// This file contains shared helper functions for loop execution.
// These functions support template variable substitution, nested node creation, and
// input variable resolution for loop iterations in VM.executeLoopWithQueue.

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
func processContractWriteTemplates(vm *VM, contractWrite *avsproto.ContractWriteNode, iterInputs map[string]interface{}) (*avsproto.ContractWriteNode, error) {
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
			if err := validateHandlebarsIfTemplate(param); err != nil {
				return nil, err
			}
			// First apply loop-specific template substitution ({{value}}, {{index}})
			paramWithLoopVars := substituteTemplateVariables(param, iterInputs)
			// Then apply advanced preprocessing for dot notation and other VM variables
			processedMethodParams[i] = vm.preprocessTextWithVariableMapping(paramWithLoopVars)
		}
		processedMethodCall.MethodParams = processedMethodParams

		processed.Config.MethodCalls = append(processed.Config.MethodCalls, processedMethodCall)
	}

	return processed, nil
}

// processContractReadTemplates processes template variables in contract read configuration.
// Similar to processContractWriteTemplates but includes support for applyToFields copying.
//
// CRITICAL: ABI is NEVER subject to template substitution for security reasons.
func processContractReadTemplates(vm *VM, contractRead *avsproto.ContractReadNode, iterInputs map[string]interface{}) (*avsproto.ContractReadNode, error) {
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
			if err := validateHandlebarsIfTemplate(param); err != nil {
				return nil, err
			}
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

	return processed, nil
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
//
// inputNodeName is a template expression (e.g., "{{settings.address_list}}" or
// "{{custom_code1.data}}"). The expression is resolved against vm.vars using the
// Goja JS VM. The resolved value must be an array.
//
// Returns:
//   - []interface{}: The resolved input array
//   - string: The template expression that was resolved
//   - error: Error if expression is invalid, value not found, or not an array
func resolveLoopInputVariable(vm *VM, inputNodeName string) ([]interface{}, string, error) {
	// Extract the expression from {{ }} delimiters
	expr := strings.TrimSpace(inputNodeName)
	if strings.HasPrefix(expr, "{{") && strings.HasSuffix(expr, "}}") {
		expr = strings.TrimSpace(expr[2 : len(expr)-2])
	}

	if expr == "" {
		return nil, inputNodeName, fmt.Errorf("loop input expression is empty")
	}

	// Resolve the expression via Goja JS VM
	jsvm := NewGojaVM()
	vm.mu.Lock()
	currentVars := make(map[string]any, len(vm.vars))
	for k, val := range vm.vars {
		currentVars[k] = val
	}
	vm.mu.Unlock()

	for key, value := range currentVars {
		if err := jsvm.Set(key, value); err != nil {
			if vm.logger != nil {
				vm.logger.Error("failed to set variable in JS VM for loop input resolution", "key", key, "error", err)
			}
		}
	}

	resolved, ok := vm.resolveVariablePath(jsvm, expr, currentVars)
	if !ok {
		return nil, inputNodeName, fmt.Errorf("loop input expression {{%s}} could not be resolved", expr)
	}

	// Convert resolved value to []interface{}
	return toInterfaceSlice(resolved, inputNodeName)
}

// toInterfaceSlice converts a resolved value to []interface{}, returning
// a descriptive error if the value is not an array.
func toInterfaceSlice(value interface{}, label string) ([]interface{}, string, error) {
	if arr, ok := value.([]interface{}); ok {
		return arr, label, nil
	}
	return nil, label, fmt.Errorf("loop input %s resolved to %T, expected array", label, value)
}

// aggregateIterationGasCosts sums gas costs from loop iteration steps and sets
// them on the parent loop step. This ensures that on-chain operations executed
// inside a loop (contractWrite, ethTransfer) have their gas costs propagated
// to the workflow-level aggregation in CalculateTotalGasCost.
func aggregateIterationGasCosts(parentStep *avsproto.Execution_Step, iterSteps []*avsproto.Execution_Step, vm *VM) {
	if parentStep == nil || len(iterSteps) == 0 {
		return
	}

	totalGasUsed := new(big.Int)
	totalGasCost := new(big.Int)
	var lastGasPrice *big.Int
	iterationsWithGas := 0

	for _, step := range iterSteps {
		if step.TotalGasCost == "" || step.TotalGasCost == "0" {
			continue
		}
		stepCost, ok := new(big.Int).SetString(step.TotalGasCost, 10)
		if !ok {
			continue
		}
		totalGasCost.Add(totalGasCost, stepCost)
		iterationsWithGas++

		if step.GasUsed != "" {
			if gu, ok := new(big.Int).SetString(step.GasUsed, 10); ok {
				totalGasUsed.Add(totalGasUsed, gu)
			}
		}
		if step.GasPrice != "" {
			if gp, ok := new(big.Int).SetString(step.GasPrice, 10); ok {
				lastGasPrice = gp
			}
		}
	}

	if iterationsWithGas == 0 {
		return
	}

	parentStep.GasUsed = totalGasUsed.String()
	parentStep.TotalGasCost = totalGasCost.String()
	// Compute implied gas price (totalGasCost / totalGasUsed) so that
	// GasUsed * GasPrice == TotalGasCost holds for the aggregated step.
	// Using last iteration's price would break the invariant when
	// iterations have different gas prices.
	if totalGasUsed.Sign() > 0 {
		impliedPrice := new(big.Int).Div(totalGasCost, totalGasUsed)
		parentStep.GasPrice = impliedPrice.String()
	} else if lastGasPrice != nil {
		parentStep.GasPrice = lastGasPrice.String()
	}

	if vm != nil && vm.logger != nil {
		vm.logger.Info("Aggregated gas costs from loop iterations",
			"step_id", parentStep.Id,
			"iterations_with_gas", iterationsWithGas,
			"total_gas_used", parentStep.GasUsed,
			"total_gas_cost", parentStep.TotalGasCost)
	}
}
