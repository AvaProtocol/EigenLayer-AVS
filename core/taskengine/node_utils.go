package taskengine

import (
	"fmt"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// buildTriggerVariableData creates the proper trigger variable structure for JavaScript VM access
// This function consolidates the common logic used in VM creation, task execution, and simulation
func buildTriggerVariableData(trigger *avsproto.TaskTrigger, triggerDataMap map[string]interface{}, triggerInputData map[string]interface{}) map[string]any {
	triggerVarData := map[string]any{}

	// Handle manual triggers specially - extract headers and pathParams to top level
	if trigger != nil && trigger.GetType() == avsproto.TriggerType_TRIGGER_TYPE_MANUAL {
		// For manual triggers, the triggerDataMap may contain headers and pathParams
		// We need to extract them to the top level and put the rest in "data"
		dataMap := make(map[string]interface{})

		for k, v := range triggerDataMap {
			switch k {
			case "headers":
				triggerVarData["headers"] = v
			case "pathParams":
				triggerVarData["pathParams"] = v
			default:
				dataMap[k] = v
			}
		}

		// Set the data field with the remaining fields
		if len(dataMap) > 0 {
			triggerVarData["data"] = dataMap
		} else {
			triggerVarData["data"] = nil
		}

	} else {
		// For other trigger types, use the original structure
		triggerVarData["data"] = triggerDataMap

		// Handle the case where triggers have no data to prevent template resolution issues
		if len(triggerDataMap) == 0 {
			triggerVarData["data"] = nil
		}
	}

	// Add trigger input data if available
	if triggerInputData != nil {
		triggerVarData["input"] = triggerInputData
	}

	return triggerVarData
}

// updateTriggerVariableInVM updates the trigger variable in the VM with proper mutex handling
// This function consolidates the common logic used in task execution and simulation
func updateTriggerVariableInVM(vm *VM, triggerVarName string, triggerVarData map[string]any) {
	vm.mu.Lock()
	existingTriggerVar := vm.vars[triggerVarName]
	if existingMap, ok := existingTriggerVar.(map[string]any); ok {
		// Merge with existing trigger variable data
		for key, value := range triggerVarData {
			existingMap[key] = value
		}
		vm.vars[triggerVarName] = existingMap
	} else {
		// Create new trigger variable
		vm.vars[triggerVarName] = triggerVarData
	}
	vm.mu.Unlock()
}

// createNodeExecutionStep creates a standardized execution step for node execution
// This function consolidates the common logic used across all node runners
// The Input field contains the node's configuration, not input data from previous steps
func createNodeExecutionStep(stepID string, nodeType avsproto.NodeType, vm *VM) *avsproto.Execution_Step {
	t0 := time.Now()

	// Get node data using helper function to reduce duplication
	nodeName, nodeConfig := vm.GetNodeDataForExecution(stepID)

	return &avsproto.Execution_Step{
		Id:         stepID,
		OutputData: nil,
		Log:        "",
		Error:      "",
		Success:    true, // Assume success initially
		StartAt:    t0.UnixMilli(),
		Type:       nodeType.String(),
		Name:       nodeName,
		Input:      nodeConfig, // Include node configuration for debugging
	}
}

// extractAndSetNodeInputData extracts input data from a node and sets it as a variable in the VM
// This function consolidates the common logic used in node execution
func extractAndSetNodeInputData(vm *VM, node *avsproto.TaskNode) {
	inputData := ExtractNodeInputData(node)
	if inputData != nil {
		processor := &CommonProcessor{vm: vm}
		processor.SetInputVarForStep(node.Id, inputData)
	}
}

// validateNodeForExecution validates a node for execution, including name validation
// This function consolidates the common validation logic used across node runners
func validateNodeForExecution(node *avsproto.TaskNode) error {
	if node == nil {
		return fmt.Errorf("node cannot be nil")
	}

	// Validate node name for JavaScript compatibility
	if err := model.ValidateNodeNameForJavaScript(node.Name); err != nil {
		return fmt.Errorf("node name validation failed: %w", err)
	}

	return nil
}

// setNodeOutputData sets the output data for a node execution step
// This function consolidates the common logic used across node runners
func setNodeOutputData(processor *CommonProcessor, stepID string, outputData interface{}) {
	processor.SetOutputVarForStep(stepID, outputData)
}

// finalizeExecutionStep sets the final execution step properties
// This function consolidates the common logic used across node runners
func finalizeExecutionStep(step *avsproto.Execution_Step, success bool, errorMsg string, logContent string) {
	step.EndAt = time.Now().UnixMilli()
	step.Success = success
	step.Error = errorMsg
	step.Log = logContent
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
