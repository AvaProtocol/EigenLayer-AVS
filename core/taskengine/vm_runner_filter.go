package taskengine

import (
	"fmt"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/dop251/goja"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

type FilterProcessor struct {
	*CommonProcessor
	jsvm *goja.Runtime
}

func NewFilterProcessor(vm *VM) *FilterProcessor {
	return &FilterProcessor{
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
		jsvm: NewGojaVM(),
	}
}

// processExpression handles different expression patterns and returns a clean JavaScript expression
func (r *FilterProcessor) processExpression(expression string) string {
	// Handle different expression patterns:
	// 1. Full template: "{{ current.age >= 18 }}" -> strip braces, use as JavaScript
	// 2. Mixed template: "{{ trigger.data.minAge }} <= current.age" -> process variables
	// 3. Pure JavaScript: "current.age < 18" -> use as-is
	cleanExpression := expression
	if strings.HasPrefix(expression, "{{") && strings.HasSuffix(expression, "}}") && strings.Count(expression, "{{") == 1 {
		// Full template expression - strip braces and use as JavaScript
		cleanExpression = strings.TrimSpace(expression[2 : len(expression)-2])
	} else if strings.Contains(expression, "{{") {
		// Mixed expression - process template variables
		cleanExpression = r.vm.preprocessTextWithVariableMapping(expression)
	}
	// else: Pure JavaScript expression - use as-is
	return cleanExpression
}

// wrapExpressionForExecution wraps the expression appropriately based on its content
func (r *FilterProcessor) wrapExpressionForExecution(cleanExpression string) string {
	// Check if the expression already contains control flow statements
	if strings.Contains(cleanExpression, "if") || strings.Contains(cleanExpression, "return") {
		// For complex expressions with control flow, wrap in a function without additional return
		return fmt.Sprintf(`(() => { %s })()`, cleanExpression)
	} else {
		// For simple expressions, wrap with return
		return fmt.Sprintf(`(() => { return %s; })()`, cleanExpression)
	}
}

func (r *FilterProcessor) Execute(stepID string, node *avsproto.FilterNode) (*avsproto.Execution_Step, error) {
	// Use shared function to create execution step
	executionLogStep := createNodeExecutionStep(stepID, avsproto.NodeType_NODE_TYPE_FILTER, r.vm)

	var logBuilder strings.Builder
	logBuilder.WriteString(fmt.Sprintf("Executing Filter Node ID: %s at %s\n", stepID, time.Now()))

	// Get configuration from Config message (consistent with other processors)
	if node.Config == nil {
		errMsg := "FilterNode Config is nil"
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errMsg))
		finalizeExecutionStep(executionLogStep, false, errMsg, logBuilder.String())
		return executionLogStep, fmt.Errorf(errMsg)
	}

	expression := node.Config.Expression
	if expression == "" {
		errMsg := "FilterNode expression is empty"
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errMsg))
		finalizeExecutionStep(executionLogStep, false, errMsg, logBuilder.String())
		return executionLogStep, fmt.Errorf(errMsg)
	}

	sourceID := node.Config.SourceId
	if sourceID == "" {
		errMsg := "FilterNode sourceId is empty"
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errMsg))
		finalizeExecutionStep(executionLogStep, false, errMsg, logBuilder.String())
		return executionLogStep, fmt.Errorf(errMsg)
	}

	logBuilder.WriteString(fmt.Sprintf("Filter configuration - source_id: %s, expression: %s\n", sourceID, expression))

	// Get the input data to filter
	r.vm.mu.Lock()
	inputVar, exists := r.vm.vars[r.vm.GetNodeNameAsVar(sourceID)]
	r.vm.mu.Unlock()

	if !exists {
		errMsg := fmt.Sprintf("input variable for source '%s' not found", sourceID)
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errMsg))
		finalizeExecutionStep(executionLogStep, false, errMsg, logBuilder.String())
		return executionLogStep, fmt.Errorf(errMsg)
	}

	// Determine the variable name to use in JavaScript
	inputVarName := r.vm.GetNodeNameAsVar(sourceID)
	logBuilder.WriteString(fmt.Sprintf("Using input variable: %s (from source: %s)\n", inputVarName, sourceID))

	// Apply template processing to the expression if it contains {{ }}
	var processedExpression string
	if strings.Contains(expression, "{{") {
		processedExpression = r.vm.preprocessText(expression)
		logBuilder.WriteString(fmt.Sprintf("Expression after preprocessing: %s\n", processedExpression))
	} else {
		processedExpression = r.vm.preprocessTextWithVariableMapping(expression)
	}

	logBuilder.WriteString(fmt.Sprintf("Using processed expression: %s\n", processedExpression))

	// Create JavaScript VM for expression evaluation
	jsvm := NewGojaVM()

	// Set all variables from the VM context
	r.vm.mu.Lock()
	for key, value := range r.vm.vars {
		if err := jsvm.Set(key, value); err != nil {
			r.vm.mu.Unlock()
			errMsg := fmt.Sprintf("failed to set variable '%s' in JS VM: %v", key, err)
			logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errMsg))
			finalizeExecutionStep(executionLogStep, false, errMsg, logBuilder.String())
			return executionLogStep, fmt.Errorf(errMsg)
		}
	}
	r.vm.mu.Unlock()

	// Ensure the expression is wrapped in a function if it contains if/return
	finalExpression := processedExpression
	if strings.Contains(processedExpression, "if") || strings.Contains(processedExpression, "return") {
		finalExpression = fmt.Sprintf("(function(item, index) { %s })", processedExpression)
	} else {
		finalExpression = fmt.Sprintf("(function(item, index) { return %s; })", processedExpression)
	}

	logBuilder.WriteString(fmt.Sprintf("Final wrapped expression: %s\n", finalExpression))

	// Unwrap the data if it's in a map with 'data' key (from previous node output)
	var actualDataToFilter interface{}
	if dataMap, ok := inputVar.(map[string]interface{}); ok {
		if dataValue, exists := dataMap["data"]; exists {
			actualDataToFilter = dataValue
			logBuilder.WriteString("Unwrapped input data from 'data' key\n")
		} else {
			actualDataToFilter = inputVar
		}
	} else {
		actualDataToFilter = inputVar
	}

	logBuilder.WriteString(fmt.Sprintf("Data to filter type: %T, content: %v\n", actualDataToFilter, actualDataToFilter))

	var filteredResult []interface{}
	var evaluationError error

	// Handle different input types for filtering
	switch data := actualDataToFilter.(type) {
	case []interface{}:
		logBuilder.WriteString(fmt.Sprintf("Filtering array with %d items\n", len(data)))

		for i, item := range data {
			// Set the current item and index in the JS context
			loopVarNameForItem := fmt.Sprintf("%s_item", inputVarName)
			if err := jsvm.Set(loopVarNameForItem, item); err != nil {
				evaluationError = fmt.Errorf("failed to set loop item '%s' (index %d) in JS VM: %w", loopVarNameForItem, i, err)
				break
			}
			if err := jsvm.Set("index", i); err != nil {
				evaluationError = fmt.Errorf("failed to set index %d in JS VM: %w", i, err)
				break
			}

			// Evaluate the filter expression for this item
			result, err := jsvm.RunString(fmt.Sprintf("(%s)(%s, index)", finalExpression, loopVarNameForItem))
			if err != nil {
				logBuilder.WriteString(fmt.Sprintf("Error evaluating filter expression for item %d (%v): %s. Skipping item.\n", i, item, err.Error()))
				continue
			}

			// If the result is truthy, include the item
			if result.ToBoolean() {
				filteredResult = append(filteredResult, item)
				logBuilder.WriteString(fmt.Sprintf("Item %d passed filter\n", i))
			} else {
				logBuilder.WriteString(fmt.Sprintf("Item %d filtered out\n", i))
			}
		}

		if evaluationError == nil {
			logBuilder.WriteString(fmt.Sprintf("Filter completed: %d items passed out of %d total\n", len(filteredResult), len(data)))
		}

	case []map[string]interface{}:
		logBuilder.WriteString(fmt.Sprintf("Filtering map array with %d items\n", len(data)))

		for i, item := range data {
			// Set the current item and index in the JS context
			loopVarNameForItem := fmt.Sprintf("%s_item", inputVarName)
			if err := jsvm.Set(loopVarNameForItem, item); err != nil {
				evaluationError = fmt.Errorf("failed to set loop item '%s' (index %d) in JS VM: %w", loopVarNameForItem, i, err)
				break
			}
			if err := jsvm.Set("index", i); err != nil {
				evaluationError = fmt.Errorf("failed to set index %d in JS VM: %w", i, err)
				break
			}

			// Evaluate the filter expression for this item
			result, err := jsvm.RunString(fmt.Sprintf("(%s)(%s, index)", finalExpression, loopVarNameForItem))
			if err != nil {
				logBuilder.WriteString(fmt.Sprintf("Error evaluating filter expression for item %d (%v): %s. Skipping item.\n", i, item, err.Error()))
				continue
			}

			// If the result is truthy, include the item
			if result.ToBoolean() {
				filteredResult = append(filteredResult, item)
				logBuilder.WriteString(fmt.Sprintf("Map item %d passed filter\n", i))
			} else {
				logBuilder.WriteString(fmt.Sprintf("Map item %d filtered out\n", i))
			}
		}

		if evaluationError == nil {
			logBuilder.WriteString(fmt.Sprintf("Map filter completed: %d items passed out of %d total\n", len(filteredResult), len(data)))
		}

	case map[string]interface{}:
		// For map input, set it as a variable and evaluate the expression directly
		itemVarNameForMap := fmt.Sprintf("%s_item", inputVarName)
		if err := jsvm.Set(itemVarNameForMap, data); err != nil {
			evaluationError = fmt.Errorf("failed to set input map as '%s' in JS VM: %w", itemVarNameForMap, err)
		} else {
			// Evaluate the filter expression for the map
			result, err := jsvm.RunString(fmt.Sprintf("(%s)(%s, 0)", finalExpression, itemVarNameForMap))
			if err != nil {
				logBuilder.WriteString(fmt.Sprintf("Error evaluating filter expression for map: %s\n", err.Error()))
				evaluationError = err
			} else if result.ToBoolean() {
				filteredResult = append(filteredResult, data)
				logBuilder.WriteString("Map passed filter\n")
			} else {
				logBuilder.WriteString("Map filtered out\n")
			}
		}

	default:
		// Unsupported data type for filtering
		evaluationError = fmt.Errorf("input variable '%s' (after unwrapping) has an unsupported type for filtering: %T", inputVarName, actualDataToFilter)
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", evaluationError.Error()))
	}

	if evaluationError != nil {
		finalizeExecutionStep(executionLogStep, false, evaluationError.Error(), logBuilder.String())
		return executionLogStep, evaluationError
	}

	// Convert result to protobuf and set output
	outputProtoStruct, err := structpb.NewStruct(map[string]interface{}{
		"filtered_data": filteredResult,
		"total_items":   len(filteredResult),
	})
	if err != nil {
		logBuilder.WriteString(fmt.Sprintf("Error converting execution result to proto struct: %v\n", err))
		finalizeExecutionStep(executionLogStep, false, err.Error(), logBuilder.String())
		return executionLogStep, err
	} else {
		anyOutput, err := anypb.New(outputProtoStruct)
		if err != nil {
			logBuilder.WriteString(fmt.Sprintf("Error marshalling output to Any: %v\n", err))
			finalizeExecutionStep(executionLogStep, false, err.Error(), logBuilder.String())
			return executionLogStep, err
		} else {
			executionLogStep.OutputData = &avsproto.Execution_Step_Filter{
				Filter: &avsproto.FilterNode_Output{
					Data: anyOutput,
				},
			}
			// Use shared function to set output variable for this step
			setNodeOutputData(r.CommonProcessor, stepID, filteredResult)
		}
	}

	// Use shared function to finalize execution step with success
	finalizeExecutionStep(executionLogStep, true, "", logBuilder.String())
	return executionLogStep, nil
}
