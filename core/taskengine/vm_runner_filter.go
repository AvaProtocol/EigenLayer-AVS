package taskengine

import (
	"fmt"
	"regexp"
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
	// 1. Full template: "{{ value.age >= 18 }}" -> strip braces, use as JavaScript
	// 2. Mixed template: "{{ trigger.data.minAge }} <= value.age" -> process variables
	// 3. Pure JavaScript: "value.age < 18" -> use as-is
	cleanExpression := expression
	if strings.HasPrefix(expression, "{{") && strings.HasSuffix(expression, "}}") && strings.Count(expression, "{{") == 1 {
		// Full template expression - strip braces and use as JavaScript
		cleanExpression = strings.TrimSpace(expression[2 : len(expression)-2])
	} else if strings.Contains(expression, "{{") {
		// Mixed expression - process template variables
		cleanExpression = r.vm.preprocessTextWithVariableMapping(expression)
		// DEBUG: Log the template processing result
		if r.vm.logger != nil {
			r.vm.logger.Debug("Template processing result",
				"original", expression,
				"processed", cleanExpression)
		}
	}
	// else: Pure JavaScript expression - use as-is
	return cleanExpression
}

// wrapExpressionForExecution wraps the expression appropriately based on its content
func (r *FilterProcessor) wrapExpressionForExecution(cleanExpression string) string {
	// SECURITY: Validate the JavaScript expression before execution
	validator := NewExpressionValidator(DefaultSecurityConfig())
	sanitizedExpr := validator.SanitizeExpression(cleanExpression)

	if err := validator.ValidateExpression(sanitizedExpr); err != nil {
		if r.vm.logger != nil {
			r.vm.logger.Warn("Invalid or potentially dangerous filter expression detected",
				"original_expr", cleanExpression,
				"sanitized_expr", sanitizedExpr,
				"error", err.Error())
		}
		// SECURITY: Never bypass security validation
		// If validation fails, return a safe expression that evaluates to false
		return `(() => { return false; })()`
	}

	// Check if the expression already contains control flow statements
	if strings.Contains(sanitizedExpr, "if") || strings.Contains(sanitizedExpr, "return") {
		// For complex expressions with control flow, wrap in a function without additional return
		return fmt.Sprintf(`(() => { %s })()`, sanitizedExpr)
	} else {
		// For simple expressions, wrap with return
		return fmt.Sprintf(`(() => { return %s; })()`, sanitizedExpr)
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

	// Determine the variable name to use in JavaScript BEFORE locking mutex
	inputVarName := r.vm.GetNodeNameAsVar(sourceID)

	// Clean the expression for processing
	cleanExpression := r.processExpression(expression)

	// Add the expected log format for the tests
	logBuilder.WriteString(fmt.Sprintf("Source node ID: '%s', Variable name: '%s', Original Expression: '%s', Clean Expression: '%s'\n",
		sourceID, inputVarName, expression, cleanExpression))

	logBuilder.WriteString(fmt.Sprintf("Filter configuration - source_id: %s, expression: %s\n", sourceID, expression))
	logBuilder.WriteString(fmt.Sprintf("Using input variable: %s (from source: %s)\n", inputVarName, sourceID))

	// Get the input data to filter
	r.vm.mu.Lock()
	inputVar, exists := r.vm.vars[inputVarName]
	r.vm.mu.Unlock()

	if !exists {
		errMsg := fmt.Sprintf("input variable for source '%s' not found", sourceID)
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errMsg))
		finalizeExecutionStep(executionLogStep, false, errMsg, logBuilder.String())
		return executionLogStep, fmt.Errorf(errMsg)
	}

	// Apply template processing to the expression if it contains {{ }}
	var processedExpression string
	if strings.Contains(expression, "{{") {
		processedExpression = r.vm.preprocessText(expression)
		logBuilder.WriteString(fmt.Sprintf("Expression after preprocessing: %s\n", processedExpression))
	} else {
		processedExpression = cleanExpression
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
		filteredResult, evaluationError = r.filterArray(data, processedExpression, inputVarName, jsvm, &logBuilder)

	case []map[string]interface{}:
		logBuilder.WriteString(fmt.Sprintf("Filtering map array with %d items\n", len(data)))
		filteredResult, evaluationError = r.filterMapArray(data, processedExpression, inputVarName, jsvm, &logBuilder)

	case map[string]interface{}:
		logBuilder.WriteString("Filtering single map\n")
		filteredResult, evaluationError = r.filterSingleMap(data, processedExpression, inputVarName, jsvm, &logBuilder)

	default:
		// Unsupported data type for filtering
		evaluationError = fmt.Errorf("input variable '%s' (after unwrapping) has an unsupported type for filtering: %T", inputVarName, actualDataToFilter)
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", evaluationError.Error()))
	}

	if evaluationError != nil {
		finalizeExecutionStep(executionLogStep, false, evaluationError.Error(), logBuilder.String())
		return executionLogStep, evaluationError
	}

	logBuilder.WriteString(fmt.Sprintf("Filter completed: %d items passed\n", len(filteredResult)))

	// Convert result to protobuf and set output
	// We need to create a Value that contains a ListValue for AnyToSlice to work properly
	listValue := &structpb.ListValue{}
	for _, item := range filteredResult {
		itemValue, err := structpb.NewValue(item)
		if err != nil {
			logBuilder.WriteString(fmt.Sprintf("Error converting item to proto value: %v\n", err))
			finalizeExecutionStep(executionLogStep, false, err.Error(), logBuilder.String())
			return executionLogStep, err
		}
		listValue.Values = append(listValue.Values, itemValue)
	}

	// Wrap the ListValue in a Value
	outputValue := &structpb.Value{
		Kind: &structpb.Value_ListValue{
			ListValue: listValue,
		},
	}

	anyOutput, err := anypb.New(outputValue)
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

	// Use shared function to finalize execution step with success
	finalizeExecutionStep(executionLogStep, true, "", logBuilder.String())
	return executionLogStep, nil
}

// filterArray filters an array of interface{} items
func (r *FilterProcessor) filterArray(data []interface{}, expression string, inputVarName string, jsvm *goja.Runtime, logBuilder *strings.Builder) ([]interface{}, error) {
	var filteredResult []interface{}

	// Determine if we need to wrap the expression in a function
	var finalExpression string
	if strings.Contains(expression, "if") || strings.Contains(expression, "return") {
		finalExpression = fmt.Sprintf("(function(value, index) { %s })", expression)
	} else {
		finalExpression = fmt.Sprintf("(function(value, index) { return %s; })", expression)
	}

	for i, item := range data {
		// Set the current item and index in the JS context
		if err := jsvm.Set("value", item); err != nil {
			return nil, fmt.Errorf("failed to set value (index %d) in JS VM: %w", i, err)
		}
		if err := jsvm.Set("index", i); err != nil {
			return nil, fmt.Errorf("failed to set index %d in JS VM: %w", i, err)
		}

		// Evaluate the filter expression for this item
		result, err := jsvm.RunString(fmt.Sprintf("(%s)(value, index)", finalExpression))
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

	return filteredResult, nil
}

// filterMapArray filters an array of map[string]interface{} items
func (r *FilterProcessor) filterMapArray(data []map[string]interface{}, expression string, inputVarName string, jsvm *goja.Runtime, logBuilder *strings.Builder) ([]interface{}, error) {
	var filteredResult []interface{}

	// Determine if we need to wrap the expression in a function
	var finalExpression string
	// Regular expression to detect "if" or "return" outside of string literals or comments
	controlFlowRegex := regexp.MustCompile(`\b(if|return)\b`)
	if controlFlowRegex.MatchString(expression) {
		finalExpression = fmt.Sprintf("(function(value, index) { %s })", expression)
	} else {
		finalExpression = fmt.Sprintf("(function(value, index) { return %s; })", expression)
	}

	for i, item := range data {
		// Set the current item and index in the JS context
		if err := jsvm.Set("value", item); err != nil {
			return nil, fmt.Errorf("failed to set value (index %d) in JS VM: %w", i, err)
		}
		if err := jsvm.Set("index", i); err != nil {
			return nil, fmt.Errorf("failed to set index %d in JS VM: %w", i, err)
		}

		// Evaluate the filter expression for this item
		result, err := jsvm.RunString(fmt.Sprintf("(%s)(value, index)", finalExpression))
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

	return filteredResult, nil
}

// filterSingleMap filters a single map[string]interface{} item
func (r *FilterProcessor) filterSingleMap(data map[string]interface{}, expression string, inputVarName string, jsvm *goja.Runtime, logBuilder *strings.Builder) ([]interface{}, error) {
	var filteredResult []interface{}

	// Determine if we need to wrap the expression in a function
	var finalExpression string
	if strings.Contains(expression, "if") || strings.Contains(expression, "return") {
		finalExpression = fmt.Sprintf("(function(value, index) { %s })", expression)
	} else {
		finalExpression = fmt.Sprintf("(function(value, index) { return %s; })", expression)
	}

	// Set the map as a variable and evaluate the expression directly
	if err := jsvm.Set("value", data); err != nil {
		return nil, fmt.Errorf("failed to set input map in JS VM: %w", err)
	}
	if err := jsvm.Set("index", 0); err != nil {
		return nil, fmt.Errorf("failed to set index in JS VM: %w", err)
	}

	// Evaluate the filter expression for the map
	result, err := jsvm.RunString(fmt.Sprintf("(%s)(value, index)", finalExpression))
	if err != nil {
		logBuilder.WriteString(fmt.Sprintf("Error evaluating filter expression for map: %s\n", err.Error()))
		return nil, err
	}

	if result.ToBoolean() {
		filteredResult = append(filteredResult, data)
		logBuilder.WriteString("Map passed filter\n")
	} else {
		logBuilder.WriteString("Map filtered out\n")
	}

	return filteredResult, nil
}
