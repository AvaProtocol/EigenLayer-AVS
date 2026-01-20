package taskengine

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// buildTriggerVariableData creates the proper trigger variable structure for JavaScript VM access
// This function consolidates the common logic used in VM creation, task execution, and simulation
func buildTriggerVariableData(trigger *avsproto.TaskTrigger, triggerDataMap map[string]interface{}, triggerInputData map[string]interface{}) map[string]interface{} {
	triggerVarData := make(map[string]interface{})

	// Check if trigger is nil to avoid runtime panic
	if trigger == nil {
		return triggerVarData
	}

	// For manual triggers, use the actual user data from triggerInputData
	// Also include headers and pathParams directly for template access (e.g., ManualTrigger.pathParams.endpoint)
	if trigger.GetType() == avsproto.TriggerType_TRIGGER_TYPE_MANUAL {
		if inputData, exists := triggerInputData["data"]; exists {
			triggerVarData["data"] = inputData
		}
		// Add headers and pathParams directly for template access
		if headers, exists := triggerInputData["headers"]; exists {
			triggerVarData["headers"] = headers
		}
		if pathParams, exists := triggerInputData["pathParams"]; exists {
			triggerVarData["pathParams"] = pathParams
		}
		triggerVarData["input"] = triggerInputData
	} else {
		// For other trigger types, use the trigger output data
		triggerVarData["data"] = triggerDataMap
		if triggerInputData != nil {
			triggerVarData["input"] = triggerInputData
		}
	}

	return triggerVarData
}

// updateTriggerVariableInVM updates the trigger variable in the VM with proper mutex handling
// This function consolidates the common logic used in task execution and simulation
func updateTriggerVariableInVM(vm *VM, triggerVarName string, triggerVarData map[string]interface{}) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	// Debug logging for troubleshooting stepInput.test.ts PartialSuccess issue
	if vm.logger != nil {
		hasHeaders := false
		hasPathParams := false
		if _, ok := triggerVarData["headers"]; ok {
			hasHeaders = true
		}
		if _, ok := triggerVarData["pathParams"]; ok {
			hasPathParams = true
		}
		vm.logger.Info("🔍 updateTriggerVariableInVM DEBUG - Updating trigger variable",
			"triggerVarName", triggerVarName,
			"triggerVarDataKeys", GetMapKeys(triggerVarData),
			"hasHeaders", hasHeaders,
			"hasPathParams", hasPathParams)
	}

	existingTriggerVar := vm.vars[triggerVarName]
	if existingMap, ok := existingTriggerVar.(map[string]interface{}); ok {
		// Merge with existing trigger variable data
		for key, value := range triggerVarData {
			existingMap[key] = value
		}
		vm.vars[triggerVarName] = existingMap
		if vm.logger != nil {
			vm.logger.Info("🔍 updateTriggerVariableInVM DEBUG - Merged with existing trigger variable",
				"triggerVarName", triggerVarName,
				"finalKeys", GetMapKeys(existingMap))
		}
	} else {
		// Create new trigger variable
		vm.vars[triggerVarName] = triggerVarData
		if vm.logger != nil {
			vm.logger.Info("🔍 updateTriggerVariableInVM DEBUG - Created new trigger variable",
				"triggerVarName", triggerVarName,
				"finalKeys", GetMapKeys(triggerVarData))
		}
	}
}

// CreateNodeExecutionStep creates a standardized execution step for node execution
// This function consolidates the common logic used across all node runners
// The Input field contains the node's configuration, not input data from previous steps
//
// Parameters:
//   - stepID: The ID of the step being executed
//   - node: The actual TaskNode being executed (used to get nodeType and type-specific config like is_simulated)
//   - vm: The VM context
func CreateNodeExecutionStep(stepID string, node *avsproto.TaskNode, vm *VM) *avsproto.Execution_Step {
	t0 := time.Now()

	// Get node data using helper function to reduce duplication
	nodeName, nodeConfig := vm.GetNodeDataForExecution(stepID)

	// Get nodeType from the TaskNode
	var nodeType avsproto.NodeType
	if node != nil {
		nodeType = node.Type
	}

	step := &avsproto.Execution_Step{
		Id:         stepID,
		OutputData: nil,
		Log:        "",
		Error:      "",
		Success:    true, // Assume success initially
		StartAt:    t0.UnixMilli(),
		Type:       nodeType.String(),
		Name:       nodeName,
		Config:     nodeConfig, // Include node configuration for debugging
	}

	// Attach execution_context to the step (e.g., is_simulated, chain_id, provider)
	if vm != nil {
		// Determine if operation should be simulated
		isSimulated := false
		provider := string(ProviderChainRPC)

		// For contract write operations, check the node config for is_simulated flag
		if nodeType == avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE {
			// Default to simulation for safety
			isSimulated = true

			// Check if node config explicitly sets is_simulated
			if node != nil {
				if contractWrite := node.GetContractWrite(); contractWrite != nil && contractWrite.Config != nil {
					if contractWrite.Config.IsSimulated != nil {
						isSimulated = *contractWrite.Config.IsSimulated
					}
				}
			}
		}

		// Set provider based on simulation flag
		if isSimulated {
			provider = string(ProviderTenderly)
		}

		var chainID interface{} = nil
		if vm.smartWalletConfig != nil && vm.smartWalletConfig.ChainID != 0 {
			chainID = vm.smartWalletConfig.ChainID
		}
		ctxMap := map[string]interface{}{
			"is_simulated": isSimulated,
			"provider":     provider,
		}
		if chainID != nil {
			ctxMap["chain_id"] = chainID
		}
		if ctxVal, err := structpb.NewValue(ctxMap); err == nil {
			step.ExecutionContext = ctxVal
		}
	}

	return step
}

// setNodeOutputData sets the output data for a node execution step
// This function consolidates the common logic used across node runners
func setNodeOutputData(processor *CommonProcessor, stepID string, outputData interface{}) {
	processor.SetOutputVarForStep(stepID, outputData)
}

// finalizeStep is the single unified finalizer for both success and error paths.
// It sets end time, success flag, error message/code (when err != nil), and log content.
func finalizeStep(step *avsproto.Execution_Step, success bool, err error, errorMessage string, logContent string) {
	// If no explicit error provided but step failed and an errorMessage exists, create a structured error
	if err == nil && !success && strings.TrimSpace(errorMessage) != "" {
		// Create a structured error with INVALID_REQUEST code for validation errors
		// This ensures proper error code propagation to the response
		err = NewInvalidRequestError(errorMessage)
	}

	step.EndAt = time.Now().UnixMilli()
	step.Success = success
	if err != nil {
		step.Error = err.Error()
		step.ErrorCode = GetErrorCode(err)
	} else if !success {
		// If step failed but no error was provided, use errorMessage directly
		// This ensures error message is always set when step fails
		if strings.TrimSpace(errorMessage) != "" {
			step.Error = errorMessage
			step.ErrorCode = avsproto.ErrorCode_INVALID_REQUEST
		} else {
			// Ensure error is cleared on success/falsy err
			if step.Error != "" {
				step.Error = ""
			}
		}
	} else {
		// Ensure error is cleared on success/falsy err
		if step.Error != "" {
			step.Error = ""
		}
	}
	step.Log = logContent
}

// formatNodeExecutionLogHeader generates a standardized log header for node execution
// This centralizes the log format across all node runners for consistency
// Returns a formatted string like "Executing REST API 'fetch_data' (01k70sm85e929fqhdbh64nnm28)"
func formatNodeExecutionLogHeader(step *avsproto.Execution_Step) string {
	nodeName := step.Name
	nodeID := strings.ToLower(step.Id) // Convert to lowercase for better readability

	// Use node name if available, otherwise fall back to ID
	displayName := nodeName
	if displayName == "" || displayName == "unknown" {
		displayName = nodeID
		return fmt.Sprintf("Executing '%s'\n", displayName)
	}

	// Show both name (for readability) and ID (for reference), without node type
	return fmt.Sprintf("Executing '%s' (%s)\n", displayName, nodeID)
}

// Checks if a node's config is nil and returns a standardized error
// This centralizes the common config validation pattern used across all node runners
// Usage: if err := validateNodeConfig(node.Config, "NodeTypeName"); err != nil { return err }
func validateNodeConfig(config interface{}, nodeTypeName string) error {
	if config == nil {
		return fmt.Errorf("%s Config is nil", nodeTypeName)
	}
	return nil
}

// ---- Shared helpers for determining step success across runners ----

// hasReceiptFailure returns true if the given receipt has a status field equal to "0x0" (failure)
func hasReceiptFailure(receipt *structpb.Value) bool {
	if receipt == nil {
		return false
	}
	if recMap, ok := receipt.AsInterface().(map[string]interface{}); ok {
		if statusVal, ok2 := recMap["status"].(string); ok2 && (statusVal == "0x0" || statusVal == "0X0") {
			return true
		}
	}
	return false
}

// computeWriteStepSuccess inspects ContractWrite method results to derive step success and an error message
func computeWriteStepSuccess(results []*avsproto.ContractWriteNode_MethodResult) (bool, string) {
	stepSuccess := true
	stepErrorMsg := ""
	for _, mr := range results {
		if mr == nil {
			continue
		}
		if !mr.Success || hasReceiptFailure(mr.Receipt) {
			stepSuccess = false
			if stepErrorMsg == "" {
				if mr.Error != "" {
					stepErrorMsg = mr.Error
				} else {
					stepErrorMsg = "one or more contract writes failed"
				}
			}
		}
	}
	return stepSuccess, stepErrorMsg
}

// computeReadStepSuccess inspects ContractRead method results to derive step success and an error message
func computeReadStepSuccess(results []*avsproto.ContractReadNode_MethodResult) (bool, string) {
	stepSuccess := true
	stepErrorMsg := ""
	for _, mr := range results {
		if mr == nil {
			continue
		}
		if !mr.Success {
			stepSuccess = false
			if stepErrorMsg == "" {
				if mr.Error != "" {
					stepErrorMsg = mr.Error
				} else {
					// Fallback: if method failed but no error message, provide a generic one
					stepErrorMsg = fmt.Sprintf("method %s failed", mr.MethodName)
				}
			}
		}
	}
	return stepSuccess, stepErrorMsg
}

// convertStringSliceMapToProtobufCompatible converts a map[string][]string to protobuf-compatible map[string]interface{}
// Single values are stored as strings, multiple values as arrays
// This is commonly needed for HTTP headers and similar structures
func convertStringSliceMapToProtobufCompatible(input map[string][]string) map[string]interface{} {
	result := make(map[string]interface{})
	for key, values := range input {
		if len(values) == 1 {
			result[key] = values[0] // Single value as string
		} else {
			// Convert []string to []interface{} for protobuf compatibility
			interfaceValues := make([]interface{}, len(values))
			for i, v := range values {
				interfaceValues[i] = v
			}
			result[key] = interfaceValues // Multiple values as array
		}
	}
	return result
}

// convertArrayOfObjectsToProtobufCompatible converts an array of objects like [{"key":"value"}]
// to a protobuf-compatible format. This is commonly used for webhook headers and path parameters.
// It merges all objects in the array into a single map for easier JavaScript access.
func convertArrayOfObjectsToProtobufCompatible(input []interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	for _, item := range input {
		if objMap, ok := item.(map[string]interface{}); ok {
			// Merge all key-value pairs from each object into the result map
			for key, value := range objMap {
				result[key] = value
			}
		}
	}

	return result
}

// ---- Step Extraction Functions for Context-Memory API ----
// These functions extract configuration and output data from execution steps
// for use in the context-memory summarization API.

// isTriggerStep checks if the step type is a trigger type
func isTriggerStep(stepType string) bool {
	upper := strings.ToUpper(stepType)
	return strings.Contains(upper, "TRIGGER_TYPE_")
}

// ExtractStepConfig extracts the configuration for a step.
// For trigger steps: returns trigger config from TaskTrigger definition
// For node steps: returns full node config from TaskNodes
//
// Parameters:
//   - step: The execution step
//   - taskNodes: Map of node ID to TaskNode definition (from vm.TaskNodes)
//   - trigger: The task trigger definition (from vm.task.Task.Trigger)
func ExtractStepConfig(
	step *avsproto.Execution_Step,
	taskNodes map[string]*avsproto.TaskNode,
	trigger *avsproto.TaskTrigger,
) interface{} {
	if step == nil {
		return nil
	}

	// Check if this is a trigger step
	if isTriggerStep(step.GetType()) {
		// For trigger steps, prefer the TaskTrigger definition when available
		if trigger != nil {
			return TaskTriggerToConfig(trigger) // Reuse existing function!
		}

		// Fallback to step.Config for trigger steps when TaskTrigger is not available
		if step.GetConfig() != nil {
			return step.GetConfig().AsInterface()
		}

		// No trigger definition and no step-level config
		return nil
	}

	// For node steps, use TaskNodes for complete definition
	if taskNodes != nil {
		if taskNode, exists := taskNodes[step.GetId()]; exists && taskNode != nil {
			return ExtractNodeConfiguration(taskNode) // Reuse existing function!
		}
	}

	// Fallback to step.Config (when TaskNode not available)
	if step.GetConfig() != nil {
		return step.GetConfig().AsInterface()
	}

	return nil
}

// ExtractStepOutput extracts the output data from an Execution_Step.
// Handles all 15 output types (5 trigger + 10 node).
func ExtractStepOutput(step *avsproto.Execution_Step) interface{} {
	if step == nil {
		return nil
	}

	// Handle trigger outputs (all use google.protobuf.Value data field)
	if out := step.GetBlockTrigger(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}
	if out := step.GetFixedTimeTrigger(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}
	if out := step.GetCronTrigger(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}
	if out := step.GetEventTrigger(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}
	if out := step.GetManualTrigger(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}

	// Handle node outputs (all use google.protobuf.Value data field)
	if out := step.GetContractRead(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}
	if out := step.GetContractWrite(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}
	if out := step.GetEthTransfer(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}
	if out := step.GetGraphql(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}
	if out := step.GetCustomCode(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}
	if out := step.GetRestApi(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}
	if out := step.GetBranch(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}
	if out := step.GetFilter(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}
	if out := step.GetLoop(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}
	if out := step.GetBalance(); out != nil && out.Data != nil {
		return out.Data.AsInterface()
	}

	return nil
}
