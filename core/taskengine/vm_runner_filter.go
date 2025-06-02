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

func (r *FilterProcessor) Execute(stepID string, node *avsproto.FilterNode) (*avsproto.Execution_Step, error) {
	t0 := time.Now()

	// Look up the task node to get the name
	var nodeName string = "unknown"
	r.vm.mu.Lock()
	if taskNode, exists := r.vm.TaskNodes[stepID]; exists {
		nodeName = taskNode.Name
	}
	r.vm.mu.Unlock()

	executionLogStep := &avsproto.Execution_Step{
		NodeId:     stepID,
		OutputData: nil,
		Log:        "",
		Error:      "",
		Success:    true,
		StartAt:    t0.UnixMilli(),
		NodeType:   avsproto.NodeType_NODE_TYPE_FILTER,
		NodeName:   nodeName,
	}

	var logBuilder strings.Builder
	logBuilder.WriteString(fmt.Sprintf("Executing Filter Node ID: %s at %s\n", stepID, time.Now()))

	// Get configuration from input variables (new architecture)
	r.vm.mu.Lock()
	inputVar, inputExists := r.vm.vars["input"]
	expressionVar, exprExists := r.vm.vars["expression"]
	r.vm.mu.Unlock()

	if !inputExists || !exprExists {
		errMsg := "missing required input variables: input, expression"
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errMsg))
		executionLogStep.Error = errMsg
		executionLogStep.Success = false
		executionLogStep.Log = logBuilder.String()
		executionLogStep.EndAt = time.Now().UnixMilli()
		return executionLogStep, fmt.Errorf(errMsg)
	}

	inputVarName, ok := inputVar.(string)
	if !ok {
		errMsg := "input variable name must be a string"
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errMsg))
		executionLogStep.Error = errMsg
		executionLogStep.Success = false
		executionLogStep.Log = logBuilder.String()
		executionLogStep.EndAt = time.Now().UnixMilli()
		return executionLogStep, fmt.Errorf(errMsg)
	}

	expression, ok := expressionVar.(string)
	if !ok {
		errMsg := "expression must be a string"
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errMsg))
		executionLogStep.Error = errMsg
		executionLogStep.Success = false
		executionLogStep.Log = logBuilder.String()
		executionLogStep.EndAt = time.Now().UnixMilli()
		return executionLogStep, fmt.Errorf(errMsg)
	}

	logBuilder.WriteString(fmt.Sprintf("Input variable: '%s', Expression: '%s'\n", inputVarName, expression))

	// Get the input variable from the VM
	r.vm.mu.Lock()
	rawInputVal, exists := r.vm.vars[inputVarName]
	r.vm.mu.Unlock()

	if !exists {
		errMsg := fmt.Sprintf("input variable '%s' not found in VM state for filter node", inputVarName)
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errMsg))
		executionLogStep.Error = errMsg
		executionLogStep.Success = false
		executionLogStep.Log = logBuilder.String()
		executionLogStep.EndAt = time.Now().UnixMilli()
		return executionLogStep, fmt.Errorf(errMsg)
	}
	logBuilder.WriteString(fmt.Sprintf("Retrieved input data from var '%s': %v\n", inputVarName, rawInputVal))

	// Input data might be wrapped, e.g., map[string]interface{}{"data": actual_array_or_object}
	actualDataToFilter := rawInputVal
	if mapVal, ok := rawInputVal.(map[string]interface{}); ok {
		if dataFieldVal, dataOk := mapVal["data"]; dataOk {
			actualDataToFilter = dataFieldVal
			logBuilder.WriteString(fmt.Sprintf("Extracted '.data' field for filtering: %v\n", actualDataToFilter))
		}
	}

	// Reset and prepare the JSVM for this execution
	r.jsvm.ClearInterrupt()

	// Set other VM variables in the JS environment for context if the filter expression needs them
	r.vm.mu.Lock()
	for key, value := range r.vm.vars {
		if key == inputVarName {
			continue
		}
		if err := r.jsvm.Set(key, value); err != nil {
			r.vm.mu.Unlock()
			errMsg := fmt.Sprintf("failed to set context variable '%s' in JS VM for filter: %v", key, err)
			logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errMsg))
			executionLogStep.Error = errMsg
			executionLogStep.Success = false
			executionLogStep.Log = logBuilder.String()
			executionLogStep.EndAt = time.Now().UnixMilli()
			return executionLogStep, fmt.Errorf(errMsg)
		}
	}
	r.vm.mu.Unlock()

	var filteredResult interface{}
	var evaluationError error

	switch dataToProcess := actualDataToFilter.(type) {
	case []interface{}:
		logBuilder.WriteString(fmt.Sprintf("Input is a slice with %d items. Filtering each item...\n", len(dataToProcess)))
		resultSlice := make([]interface{}, 0)
		for i, item := range dataToProcess {
			loopVarNameForItem := "value"
			if err := r.jsvm.Set(loopVarNameForItem, item); err != nil {
				evaluationError = fmt.Errorf("failed to set loop item '%s' (index %d) in JS VM: %w", loopVarNameForItem, i, err)
				break
			}
			if err := r.jsvm.Set("index", i); err != nil {
				evaluationError = fmt.Errorf("failed to set index %d in JS VM: %w", i, err)
				break
			}

			// Check if the expression already contains control flow statements
			var script string
			if strings.Contains(expression, "if") || strings.Contains(expression, "return") {
				// For complex expressions with control flow, wrap in a function without additional return
				script = fmt.Sprintf(`(() => { %s })()`, expression)
			} else {
				// For simple expressions, wrap with return
				script = fmt.Sprintf(`(() => { return %s; })()`, expression)
			}
			val, err := r.jsvm.RunString(script)
			if err != nil {
				logBuilder.WriteString(fmt.Sprintf("Error evaluating filter expression for item %d (%v): %s. Skipping item.\n", i, item, err.Error()))
				continue
			}
			if boolVal, ok := val.Export().(bool); ok && boolVal {
				resultSlice = append(resultSlice, item)
			} else if !ok {
				logBuilder.WriteString(fmt.Sprintf("Filter expression for item %d did not return a boolean. Got: %T. Skipping item.\n", i, val.Export()))
			}
		}
		if evaluationError == nil {
			filteredResult = resultSlice
		}

	case []map[string]interface{}:
		logBuilder.WriteString(fmt.Sprintf("Input is a slice of maps with %d items. Filtering each item...\n", len(dataToProcess)))
		resultSlice := make([]interface{}, 0)
		for i, item := range dataToProcess {
			loopVarNameForItem := "value"
			if err := r.jsvm.Set(loopVarNameForItem, item); err != nil {
				evaluationError = fmt.Errorf("failed to set loop item '%s' (index %d) in JS VM: %w", loopVarNameForItem, i, err)
				break
			}
			if err := r.jsvm.Set("index", i); err != nil {
				evaluationError = fmt.Errorf("failed to set index %d in JS VM: %w", i, err)
				break
			}

			// Check if the expression already contains control flow statements
			var script string
			if strings.Contains(expression, "if") || strings.Contains(expression, "return") {
				// For complex expressions with control flow, wrap in a function without additional return
				script = fmt.Sprintf(`(() => { %s })()`, expression)
			} else {
				// For simple expressions, wrap with return
				script = fmt.Sprintf(`(() => { return %s; })()`, expression)
			}
			val, err := r.jsvm.RunString(script)
			if err != nil {
				logBuilder.WriteString(fmt.Sprintf("Error evaluating filter expression for item %d (%v): %s. Skipping item.\n", i, item, err.Error()))
				continue
			}
			if boolVal, ok := val.Export().(bool); ok && boolVal {
				resultSlice = append(resultSlice, item)
			} else if !ok {
				logBuilder.WriteString(fmt.Sprintf("Filter expression for item %d did not return a boolean. Got: %T. Skipping item.\n", i, val.Export()))
			}
		}
		if evaluationError == nil {
			filteredResult = resultSlice
		}

	case map[string]interface{}:
		logBuilder.WriteString("Input is a map/object. Applying filter expression directly to it...\n")
		itemVarNameForMap := "value"
		if err := r.jsvm.Set(itemVarNameForMap, dataToProcess); err != nil {
			evaluationError = fmt.Errorf("failed to set input map as '%s' in JS VM: %w", itemVarNameForMap, err)
		} else {
			// Check if the expression already contains control flow statements
			var script string
			if strings.Contains(expression, "if") || strings.Contains(expression, "return") {
				// For complex expressions with control flow, wrap in a function without additional return
				script = fmt.Sprintf(`(() => { %s })()`, expression)
			} else {
				// For simple expressions, wrap with return
				script = fmt.Sprintf(`(() => { return %s; })()`, expression)
			}
			val, err := r.jsvm.RunString(script)
			if err != nil {
				logBuilder.WriteString(fmt.Sprintf("Error evaluating filter expression for map: %s\n", err.Error()))
				evaluationError = err
			} else {
				if boolVal, ok := val.Export().(bool); ok && boolVal {
					filteredResult = dataToProcess
					logBuilder.WriteString("Map passed filter.\n")
				} else {
					filteredResult = nil
					logBuilder.WriteString("Map did not pass filter (or expression not boolean).\n")
				}
			}
		}

	default:
		evaluationError = fmt.Errorf("input variable '%s' (after unwrapping) has an unsupported type for filtering: %T", inputVarName, actualDataToFilter)
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", evaluationError.Error()))
	}

	if evaluationError != nil {
		executionLogStep.Error = evaluationError.Error()
		executionLogStep.Success = false
		executionLogStep.Log = logBuilder.String()
		executionLogStep.EndAt = time.Now().UnixMilli()
		return executionLogStep, evaluationError
	}

	logBuilder.WriteString(fmt.Sprintf("Filtered result: %v\n", filteredResult))

	outputProtoStruct, err := structpb.NewValue(filteredResult)
	if err != nil {
		logBuilder.WriteString(fmt.Sprintf("Error converting execution result to proto struct: %v\n", err))
		executionLogStep.Error = err.Error()
		executionLogStep.Success = false
	} else {
		anyOutput, err := anypb.New(outputProtoStruct)
		if err != nil {
			logBuilder.WriteString(fmt.Sprintf("Error marshalling output to Any: %v\n", err))
			executionLogStep.Error = err.Error()
			executionLogStep.Success = false
		} else {
			executionLogStep.OutputData = &avsproto.Execution_Step_Filter{
				Filter: &avsproto.FilterNode_Output{
					Data: anyOutput,
				},
			}
			r.SetOutputVarForStep(stepID, filteredResult)
		}
	}

	executionLogStep.Log = logBuilder.String()
	executionLogStep.EndAt = time.Now().UnixMilli()
	return executionLogStep, nil
}
