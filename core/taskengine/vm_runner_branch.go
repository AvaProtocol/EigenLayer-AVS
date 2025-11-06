package taskengine

import (
	"fmt"
	"strings"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/dop251/goja"
	"google.golang.org/protobuf/types/known/structpb"
)

type BranchProcessor struct {
	*CommonProcessor
}

func NewBranchProcessor(vm *VM) *BranchProcessor {
	return &BranchProcessor{
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
	}
}

// Helper function to safely extract string values from map[string]interface{}
func getString(m map[string]interface{}, key string) string {
	if val, ok := m[key]; ok {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return ""
}

func (r *BranchProcessor) Validate(node *avsproto.BranchNode) error {
	if node == nil {
		return fmt.Errorf("invalid node data")
	}

	if node.Config == nil {
		return fmt.Errorf("BranchNode Config is nil")
	}

	conditions := node.Config.Conditions
	if len(conditions) == 0 {
		return fmt.Errorf("no conditions defined")
	}

	// Validate each condition
	for i, condition := range conditions {
		if condition == nil {
			return fmt.Errorf("condition at index %d is nil", i)
		}

		if strings.TrimSpace(condition.Id) == "" {
			return fmt.Errorf("condition at index %d has empty ID", i)
		}

		if strings.TrimSpace(condition.Type) == "" {
			return fmt.Errorf("condition at index %d has empty type", i)
		}

		// Validate condition type
		if condition.Type != "if" && condition.Type != "else" {
			return fmt.Errorf("condition at index %d has invalid type: %s (must be 'if' or 'else')", i, condition.Type)
		}

		// First condition must be 'if'
		if i == 0 && condition.Type != "if" {
			return fmt.Errorf("first condition must be 'if', got '%s'", condition.Type)
		}

		// For 'if' conditions, expression can be empty (treated as false)
		// For 'else' conditions, expression should be empty
		if condition.Type == "else" && strings.TrimSpace(condition.Expression) != "" {
			return fmt.Errorf("condition at index %d is 'else' type but has non-empty expression", i)
		}

		// LANGUAGE ENFORCEMENT: Validate expression using centralized validator
		// Note: For 'else' conditions with no expression, no validation needed
		if condition.Type == "if" && strings.TrimSpace(condition.Expression) != "" {
			// BranchNode uses JavaScript for expressions - hardcoded language
			// Using centralized ValidateInputByLanguage for consistency
			if err := ValidateInputByLanguage(condition.Expression, avsproto.Lang_LANG_JAVASCRIPT); err != nil {
				return fmt.Errorf("condition at index %d expression validation failed: %w", i, err)
			}
		}
	}

	return nil
}

func (r *BranchProcessor) Execute(stepID string, node *avsproto.BranchNode) (*avsproto.Execution_Step, *Step, error) {
	// Use shared function to create execution step
	executionStep := CreateNodeExecutionStep(stepID, r.GetTaskNode(), r.vm)

	// Initialize success to false for branches - only set to true when condition matches
	executionStep.Success = false

	var log strings.Builder
	log.WriteString(formatNodeExecutionLogHeader(executionStep))

	// Track condition evaluation details for email summarization
	type ConditionEvaluation struct {
		Index          int                    `json:"index"`
		ID             string                 `json:"id"`
		Type           string                 `json:"type"`
		Label          string                 `json:"label"` // If, ElseIf, Else
		Expression     string                 `json:"expression"`
		Evaluated      bool                   `json:"evaluated"`      // true if this condition was evaluated
		Result         bool                   `json:"result"`         // evaluation result (true/false)
		Taken          bool                   `json:"taken"`          // true if this condition was taken
		ProcessedExpr  string                 `json:"processedExpr"`  // expression after template processing
		VariableValues map[string]interface{} `json:"variableValues"` // variable values at evaluation time
	}
	conditionEvaluations := make([]ConditionEvaluation, 0, len(node.Config.Conditions))

	// Helper to store metadata before finalizing
	storeMetadata := func() {
		if len(conditionEvaluations) > 0 {
			// Convert ConditionEvaluation structs to []interface{} for protobuf compatibility
			evalsForProto := make([]interface{}, 0, len(conditionEvaluations))
			for _, eval := range conditionEvaluations {
				evalMap := map[string]interface{}{
					"index":          eval.Index,
					"id":             eval.ID,
					"type":           eval.Type,
					"label":          eval.Label,
					"expression":     eval.Expression,
					"evaluated":      eval.Evaluated,
					"result":         eval.Result,
					"taken":          eval.Taken,
					"processedExpr":  eval.ProcessedExpr,
					"variableValues": eval.VariableValues,
				}
				evalsForProto = append(evalsForProto, evalMap)
			}

			metadata := map[string]interface{}{
				"conditionEvaluations": evalsForProto,
			}
			if metadataValue, metaErr := structpb.NewValue(metadata); metaErr == nil {
				executionStep.Metadata = metadataValue
			}
		}
	}

	var err error
	defer func() {
		storeMetadata()
		finalizeStep(executionStep, err == nil && executionStep.Success, err, "", log.String())
	}()

	// Get conditions from Config message (static configuration)
	if err = validateNodeConfig(node.Config, "BranchNode"); err != nil {
		log.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		return executionStep, nil, err
	}

	conditions := node.Config.Conditions
	if len(conditions) == 0 {
		err = fmt.Errorf("there is no condition to evaluate")
		log.WriteString("Error: " + err.Error() + "\n")
		return executionStep, nil, err
	}

	if conditions[0].Type != "if" {
		err = fmt.Errorf("the first condition need to be an if but got: " + conditions[0].Type)
		log.WriteString("Error: " + err.Error() + "\n")
		return executionStep, nil, err
	}

	// Initialize JavaScript VM for this branch evaluation
	jsvm := NewGojaVM()

	// Set variables from VM context
	r.vm.mu.Lock()
	for key, value := range r.vm.vars {
		if setErr := jsvm.Set(key, value); setErr != nil {
			r.vm.mu.Unlock()
			err = setErr
			log.WriteString("Error setting variable '" + key + "' in JS VM: " + err.Error() + "\n")
			return executionStep, nil, err
		}
	}
	r.vm.mu.Unlock()

	// Helper function to get condition label (If, ElseIf, Else)
	getConditionLabel := func(index int, condType string) string {
		if condType == "else" {
			return "Else"
		}
		if index == 0 {
			return "If"
		}
		return "ElseIf"
	}

	// Helper function to log false condition details using consolidated parser
	logFalseConditionDetails := func(label string, expression string, processedExpr string, jsvm *goja.Runtime) {
		log.WriteString(fmt.Sprintf("%s condition resolved to false\n", label))

		// Use consolidated parser to extract operands from the ORIGINAL expression
		// (not the evaluated result processedExpr which might be "false")
		parsed := parseComparisonExpression(expression, jsvm)
		if parsed.Valid {
			// Show comparison using shared formatter
			comparisonText := formatComparisonForLog(parsed)
			if comparisonText != "" {
				log.WriteString(fmt.Sprintf("  %s\n", comparisonText))
				return
			}
		}

		// Fallback: show full expression if not a simple comparison
		log.WriteString(fmt.Sprintf("  Expression: %s\n", expression))
	}

	// Evaluate conditions in order
	for i, condition := range conditions {
		conditionLabel := getConditionLabel(i, condition.Type)

		if condition.Type == "else" {
			// Else condition - take as fallback
			executionStep.Success = true
			// Create standardized branch data
			branchData := map[string]interface{}{
				"conditionId": fmt.Sprintf("%s.%s", stepID, condition.Id),
			}

			// Convert to protobuf Value
			dataValue, err := structpb.NewValue(branchData)
			if err != nil {
				// Fallback to empty data on error
				dataValue, _ = structpb.NewValue(map[string]interface{}{})
			}

			branchOutput := &avsproto.BranchNode_Output{
				Data: dataValue,
			}

			executionStep.OutputData = &avsproto.Execution_Step_Branch{
				Branch: branchOutput,
			}

			// Find the actual target node from the edges
			conditionId := fmt.Sprintf("%s.%s", stepID, condition.Id)
			var targetNodeId string
			var targetNodeName string
			r.vm.mu.Lock()
			if r.vm.task != nil && r.vm.task.Edges != nil {
				for _, edge := range r.vm.task.Edges {
					if edge.Source == conditionId {
						targetNodeId = edge.Target
						// Get target node name
						if targetNode, exists := r.vm.TaskNodes[targetNodeId]; exists && targetNode != nil {
							targetNodeName = targetNode.Name
						}
						break
					}
				}
			}
			r.vm.mu.Unlock()

			// Record this evaluation
			conditionEvaluations = append(conditionEvaluations, ConditionEvaluation{
				Index:      i,
				ID:         condition.Id,
				Type:       condition.Type,
				Label:      conditionLabel,
				Expression: "",
				Evaluated:  true,
				Result:     true,
				Taken:      true,
			})

			// Set the output variable for the branch node
			branchVarOutput := map[string]interface{}{
				"condition_results": []map[string]interface{}{
					{
						"id":           condition.Id,
						"result":       true,
						"next_node_id": targetNodeId,
					},
				},
			}
			// Use shared function to set output variable for this step
			setNodeOutputData(r.CommonProcessor, stepID, branchVarOutput)

			// Find the next step in the plan based on this condition ID
			r.vm.mu.Lock() // Lock for reading vm.plans
			nextStepInPlan, exists := r.vm.plans[fmt.Sprintf("%s.%s", stepID, condition.Id)]
			r.vm.mu.Unlock()
			if !exists {
				// If no plan exists, create a simple step for unit testing purposes
				nextStepInPlan = &Step{NodeID: fmt.Sprintf("%s.%s", stepID, condition.Id), Next: []string{}}
			}

			// Log the resolution with next node information (with blank line before)
			log.WriteString("\n")
			nodeName := executionStep.GetName()
			if nodeName == "" || nodeName == "unknown" {
				nodeName = stepID
			}
			if targetNodeName != "" {
				log.WriteString(fmt.Sprintf("BranchNode '%s' resolved to %s condition -> led to node '%s'\n", nodeName, conditionLabel, targetNodeName))
			} else {
				log.WriteString(fmt.Sprintf("BranchNode '%s' resolved to %s condition -> no next node\n", nodeName, conditionLabel))
			}

			// Store metadata before returning
			storeMetadata()
			// Note: defer will handle finalization
			return executionStep, nextStepInPlan, nil
		}

		// Regular if condition
		// BranchNode uses JavaScript (hardcoded) - validation done in Validate() method
		expression := condition.Expression
		if expression == "" {
			// Record this evaluation
			conditionEvaluations = append(conditionEvaluations, ConditionEvaluation{
				Index:      i,
				ID:         condition.Id,
				Type:       condition.Type,
				Label:      conditionLabel,
				Expression: expression,
				Evaluated:  true,
				Result:     false,
				Taken:      false,
			})
			log.WriteString(fmt.Sprintf("%s: false (empty expression)\n", conditionLabel))
			continue
		}

		// Preprocess the expression for template variables
		processedExpression := r.vm.preprocessTextWithVariableMapping(expression)

		// Trim whitespace
		trimmedProcessed := strings.TrimSpace(processedExpression)

		// Check if the expression is empty after processing
		if trimmedProcessed == "" {
			// Record this evaluation
			conditionEvaluations = append(conditionEvaluations, ConditionEvaluation{
				Index:         i,
				ID:            condition.Id,
				Type:          condition.Type,
				Label:         conditionLabel,
				Expression:    expression,
				Evaluated:     true,
				Result:        false,
				Taken:         false,
				ProcessedExpr: "",
			})
			log.WriteString(fmt.Sprintf("%s: false (expression empty after processing)\n", conditionLabel))
			log.WriteString(fmt.Sprintf("  Original expression: %s\n", expression))
			continue
		}

		// SECURITY: Validate the processed expression before execution
		validationResult := ValidateCodeInjection(trimmedProcessed)
		if !validationResult.Valid {
			// Record this evaluation
			conditionEvaluations = append(conditionEvaluations, ConditionEvaluation{
				Index:         i,
				ID:            condition.Id,
				Type:          condition.Type,
				Label:         conditionLabel,
				Expression:    expression,
				Evaluated:     true,
				Result:        false,
				Taken:         false,
				ProcessedExpr: trimmedProcessed,
			})
			log.WriteString(fmt.Sprintf("%s: false (security validation failed)\n", conditionLabel))
			log.WriteString(fmt.Sprintf("  Error: %s\n", validationResult.Error))
			log.WriteString(fmt.Sprintf("  Original expression: %s\n", condition.Expression))
			log.WriteString(fmt.Sprintf("  Processed expression: %s\n", trimmedProcessed))
			continue // Skip this condition (treated as false)
		}

		// Evaluate the expression
		wrappedExpression := fmt.Sprintf("(%s)", trimmedProcessed)
		value, err := jsvm.RunString(wrappedExpression)
		if err != nil {
			// Record this evaluation
			conditionEvaluations = append(conditionEvaluations, ConditionEvaluation{
				Index:         i,
				ID:            condition.Id,
				Type:          condition.Type,
				Label:         conditionLabel,
				Expression:    expression,
				Evaluated:     true,
				Result:        false,
				Taken:         false,
				ProcessedExpr: processedExpression,
			})
			log.WriteString(fmt.Sprintf("%s: false (evaluation error)\n", conditionLabel))
			log.WriteString(fmt.Sprintf("  Error: %s\n", err.Error()))
			log.WriteString(fmt.Sprintf("  Original expression: %s\n", condition.Expression))
			log.WriteString(fmt.Sprintf("  Processed expression: %s\n", processedExpression))
			log.WriteString(fmt.Sprintf("  Wrapped expression: %s\n", wrappedExpression))
			continue // Skip this condition (treated as false)
		}

		boolValue, ok := value.Export().(bool)
		if !ok {
			// Record this evaluation
			conditionEvaluations = append(conditionEvaluations, ConditionEvaluation{
				Index:         i,
				ID:            condition.Id,
				Type:          condition.Type,
				Label:         conditionLabel,
				Expression:    expression,
				Evaluated:     true,
				Result:        false,
				Taken:         false,
				ProcessedExpr: trimmedProcessed,
			})
			log.WriteString(fmt.Sprintf("%s: false (non-boolean result)\n", conditionLabel))
			log.WriteString(fmt.Sprintf("  Expression: %s\n", expression))
			log.WriteString(fmt.Sprintf("  Result type: %T, value: %v\n", value.Export(), value.Export()))
			continue
		}

		// Extract comparison operands for email summary (using consolidated parser)
		// Pass the ORIGINAL expression (not the evaluated result) for parsing
		// This provides structured operand data instead of dumping entire variables
		operandValues := extractComparisonOperands(expression, jsvm)

		if boolValue {
			// Record this evaluation (condition was true and taken)
			conditionEvaluations = append(conditionEvaluations, ConditionEvaluation{
				Index:          i,
				ID:             condition.Id,
				Type:           condition.Type,
				Label:          conditionLabel,
				Expression:     expression,
				Evaluated:      true,
				Result:         true,
				Taken:          true,
				ProcessedExpr:  trimmedProcessed,
				VariableValues: operandValues,
			})

			// Log the comparison details (similar to false condition logging)
			log.WriteString(fmt.Sprintf("%s condition resolved to true\n", conditionLabel))
			parsed := parseComparisonExpression(expression, jsvm)
			if parsed.Valid {
				comparisonText := formatComparisonForLog(parsed)
				if comparisonText != "" {
					log.WriteString(fmt.Sprintf("  %s\n", comparisonText))
				}
			} else {
				// Fallback: show expression if not a comparison
				log.WriteString(fmt.Sprintf("  Expression: %s\n", expression))
			}

			executionStep.Success = true

			// Create standardized branch data
			branchData := map[string]interface{}{
				"conditionId": fmt.Sprintf("%s.%s", stepID, condition.Id),
			}

			// Convert to protobuf Value
			dataValue, err := structpb.NewValue(branchData)
			if err != nil {
				// Fallback to empty data on error
				dataValue, _ = structpb.NewValue(map[string]interface{}{})
			}

			branchOutput := &avsproto.BranchNode_Output{
				Data: dataValue,
			}

			executionStep.OutputData = &avsproto.Execution_Step_Branch{
				Branch: branchOutput,
			}

			// Find the actual target node from the edges
			conditionId := fmt.Sprintf("%s.%s", stepID, condition.Id)
			var targetNodeId string
			var targetNodeName string
			r.vm.mu.Lock()
			if r.vm.task != nil && r.vm.task.Edges != nil {
				for _, edge := range r.vm.task.Edges {
					if edge.Source == conditionId {
						targetNodeId = edge.Target
						// Get target node name
						if targetNode, exists := r.vm.TaskNodes[targetNodeId]; exists && targetNode != nil {
							targetNodeName = targetNode.Name
						}
						break
					}
				}
			}
			r.vm.mu.Unlock()

			// Set the output variable for the branch node
			branchVarOutput := map[string]interface{}{
				"condition_results": []map[string]interface{}{
					{
						"id":           condition.Id,
						"result":       true,
						"next_node_id": targetNodeId,
					},
				},
			}
			// Use shared function to set output variable for this step
			setNodeOutputData(r.CommonProcessor, stepID, branchVarOutput)

			// Find the next step in the plan based on this condition ID
			r.vm.mu.Lock() // Lock for reading vm.plans
			nextStepInPlan, exists := r.vm.plans[fmt.Sprintf("%s.%s", stepID, condition.Id)]
			r.vm.mu.Unlock()
			if !exists {
				// If no plan exists, create a simple step for unit testing purposes
				nextStepInPlan = &Step{NodeID: fmt.Sprintf("%s.%s", stepID, condition.Id), Next: []string{}}
			}

			// Log the resolution with next node information (with blank line before)
			log.WriteString("\n")
			nodeName := executionStep.GetName()
			if nodeName == "" || nodeName == "unknown" {
				nodeName = stepID
			}
			if targetNodeName != "" {
				log.WriteString(fmt.Sprintf("BranchNode '%s' resolved to %s condition -> led to node '%s'\n", nodeName, conditionLabel, targetNodeName))
			} else {
				log.WriteString(fmt.Sprintf("BranchNode '%s' resolved to %s condition -> no next node\n", nodeName, conditionLabel))
			}

			// Store metadata before returning
			storeMetadata()
			// Note: defer will handle finalization
			return executionStep, nextStepInPlan, nil
		} else {
			// Condition evaluated to false
			// Record this evaluation with operand values
			conditionEvaluations = append(conditionEvaluations, ConditionEvaluation{
				Index:          i,
				ID:             condition.Id,
				Type:           condition.Type,
				Label:          conditionLabel,
				Expression:     expression,
				Evaluated:      true,
				Result:         false,
				Taken:          false,
				ProcessedExpr:  trimmedProcessed,
				VariableValues: operandValues,
			})

			// Log detailed information
			logFalseConditionDetails(conditionLabel, expression, trimmedProcessed, jsvm)
		}
	}

	// If no condition evaluated to true
	log.WriteString("No branch condition evaluated to true.\n")

	// Check if there are any 'else' conditions defined
	hasElseCondition := false
	for _, condition := range conditions {
		if condition.Type == "else" {
			hasElseCondition = true
			break
		}
	}

	// Check if we have any conditions at all
	if len(conditions) == 0 {
		// No conditions at all - this is an error
		noConditionMetError := "no branch condition met"
		log.WriteString(noConditionMetError + "\n")
		storeMetadata()
		finalizeStep(executionStep, false, nil, noConditionMetError, log.String())
		return executionStep, nil, fmt.Errorf(noConditionMetError)
	} else if hasElseCondition {
		// If there's an else condition but we reached here, it means the else condition failed to execute
		// This should be an error because else conditions should always execute if reached
		noConditionMetError := "no branch condition met"
		log.WriteString(noConditionMetError + "\n")
		storeMetadata()
		finalizeStep(executionStep, false, nil, noConditionMetError, log.String())
		return executionStep, nil, fmt.Errorf(noConditionMetError)
	} else {
		// If there are only 'if' conditions and none matched, this is a valid "no-op" scenario
		log.WriteString("No conditions matched and no else condition defined - this is a valid no-op.\n")
		executionStep.Success = true
		executionStep.OutputData = nil // No branch action taken

		// Set the output variable for the branch node with empty results
		branchVarOutput := map[string]interface{}{
			"condition_results": []map[string]interface{}{},
		}
		// Use shared function to set output variable for this step
		setNodeOutputData(r.CommonProcessor, stepID, branchVarOutput)

		// Store metadata and finalize
		storeMetadata()
		finalizeStep(executionStep, true, nil, "", log.String())
		return executionStep, nil, nil // Success with no next step
	}
}

// ComparisonOperands represents parsed operands from a comparison expression
type ComparisonOperands struct {
	LeftExpr  string
	RightExpr string
	Operator  string
	Left      interface{}
	Right     interface{}
	Valid     bool
}

// extractComparisonOperands extracts and evaluates operands from a comparison expression.
// For expressions like "a > b", "x === y", etc., it returns structured operand data.
// This provides context for email summaries and logs without dumping entire variable objects.
func extractComparisonOperands(expr string, jsvm *goja.Runtime) map[string]interface{} {
	parsed := parseComparisonExpression(expr, jsvm)
	if !parsed.Valid {
		return nil
	}

	return map[string]interface{}{
		"leftExpr":  parsed.LeftExpr,
		"rightExpr": parsed.RightExpr,
		"operator":  parsed.Operator,
		"left":      parsed.Left,
		"right":     parsed.Right,
	}
}

// parseComparisonExpression is the core parser that extracts operands from comparison expressions.
// It returns a structured ComparisonOperands that can be formatted for logs or emails.
// Handles nested operators by tracking parentheses/brackets to find the top-level comparison.
func parseComparisonExpression(expr string, jsvm *goja.Runtime) ComparisonOperands {
	result := ComparisonOperands{Valid: false}

	if jsvm == nil || strings.TrimSpace(expr) == "" {
		return result
	}

	// Remove template delimiters if present
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "{{") && strings.HasSuffix(expr, "}}") {
		expr = strings.TrimSpace(expr[2 : len(expr)-2])
	}

	// Common comparison operators in order of precedence (longer first to avoid false matches)
	operators := []string{"===", "!==", "==", "!=", "<=", ">=", "<", ">"}

	// Find the top-level comparison operator (not inside parentheses, brackets, or quotes)
	for _, op := range operators {
		pos := findTopLevelOperator(expr, op)
		if pos >= 0 {
			result.LeftExpr = strings.TrimSpace(expr[:pos])
			result.RightExpr = strings.TrimSpace(expr[pos+len(op):])
			result.Operator = op

			// Evaluate left operand
			if leftVal, err := jsvm.RunString(fmt.Sprintf("(%s)", result.LeftExpr)); err == nil {
				result.Left = leftVal.Export()
			}

			// Evaluate right operand
			if rightVal, err := jsvm.RunString(fmt.Sprintf("(%s)", result.RightExpr)); err == nil {
				result.Right = rightVal.Export()
			}

			result.Valid = true
			return result
		}
	}

	return result
}

// findTopLevelOperator finds the position of an operator that is not inside parentheses, brackets, or quotes
// Returns -1 if no valid operator found at the top level
func findTopLevelOperator(expr string, op string) int {
	depth := 0        // Parentheses depth
	bracketDepth := 0 // Square bracket depth
	braceDepth := 0   // Curly brace depth
	inString := false
	inTemplate := false
	escapeNext := false

	for i := 0; i <= len(expr)-len(op); i++ {
		ch := expr[i]

		// Handle escape sequences
		if escapeNext {
			escapeNext = false
			continue
		}
		if ch == '\\' {
			escapeNext = true
			continue
		}

		// Track string literals
		if ch == '"' || ch == '\'' {
			if !inTemplate {
				inString = !inString
			}
			continue
		}

		// Track template literals (backticks)
		if ch == '`' {
			inTemplate = !inTemplate
			inString = inTemplate // Inside template is like inside string
			continue
		}

		// Skip if inside string/template
		if inString || inTemplate {
			continue
		}

		// Track nesting depth
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
		case '[':
			bracketDepth++
		case ']':
			bracketDepth--
		case '{':
			braceDepth++
		case '}':
			braceDepth--
		}

		// Only check for operator at top level (depth == 0)
		if depth == 0 && bracketDepth == 0 && braceDepth == 0 {
			if i+len(op) <= len(expr) && expr[i:i+len(op)] == op {
				// Special case: For single-char operators like '>', check if it's part of '=>'
				// to avoid matching the '>' in arrow functions
				if op == ">" && i+1 < len(expr) && expr[i+1] == '=' {
					// This is '=>' (arrow function), not a standalone '>'
					continue
				}
				// Special case: For '>=', check if it's preceded by '=' to avoid matching '==>'
				if op == ">=" && i > 0 && expr[i-1] == '=' {
					// This is part of '==>', skip it
					continue
				}
				return i
			}
		}
	}

	return -1
}

// formatComparisonForLog formats comparison operands for plain text execution logs
// Uses shared formatValueConcise from summarizer.go
func formatComparisonForLog(operands ComparisonOperands) string {
	if !operands.Valid {
		return ""
	}
	leftVal := formatValueConcise(operands.Left)
	rightVal := formatValueConcise(operands.Right)
	return fmt.Sprintf("Expression: %s %s %s\nEvaluated: %s %s %s",
		operands.LeftExpr, operands.Operator, operands.RightExpr,
		leftVal, operands.Operator, rightVal)
}

// extractVariableValuesFromExpression extracts all top-level variable values from an expression
// For expressions like "{{balance1.data.find(...) > Number(settings.amount)}}", it extracts
// the values of "balance1", "settings", etc. from the VM for display in logs and emails.
func extractVariableValuesFromExpression(expression string, vm *VM, jsvm *goja.Runtime) map[string]interface{} {
	if vm == nil || jsvm == nil || strings.TrimSpace(expression) == "" {
		return make(map[string]interface{})
	}

	// Extract variable names from the expression (looking for top-level identifiers)
	varNames := extractTopLevelVariables(expression)
	if len(varNames) == 0 {
		return make(map[string]interface{})
	}

	// Get the values from the VM
	result := make(map[string]interface{})
	vm.mu.Lock()
	defer vm.mu.Unlock()

	for _, varName := range varNames {
		if val, exists := vm.vars[varName]; exists {
			// Store the variable value
			// Truncate large objects for readability
			result[varName] = truncateForDisplay(val)
		}
	}

	return result
}

// extractTopLevelVariables extracts top-level variable names from an expression
// For "{{balance1.data.find(...) > Number(settings.amount)}}", returns ["balance1", "settings"]
func extractTopLevelVariables(expression string) []string {
	// Remove template delimiters
	expr := strings.TrimSpace(expression)
	expr = strings.TrimPrefix(expr, "{{")
	expr = strings.TrimSuffix(expr, "}}")
	expr = strings.TrimSpace(expr)

	if expr == "" {
		return nil
	}

	// Common JavaScript keywords/functions to exclude
	keywords := map[string]bool{
		"true": true, "false": true, "null": true, "undefined": true,
		"Number": true, "String": true, "Boolean": true, "Object": true,
		"Array": true, "Math": true, "Date": true, "JSON": true,
		"parseInt": true, "parseFloat": true, "isNaN": true,
	}

	// Extract identifiers (alphanumeric + underscore, starting with letter/underscore)
	varMap := make(map[string]bool)
	var currentVar strings.Builder
	inIdentifier := false

	for i := 0; i < len(expr); i++ {
		ch := expr[i]
		isAlphaNum := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '_'

		if isAlphaNum {
			if !inIdentifier {
				inIdentifier = true
			}
			currentVar.WriteByte(ch)
		} else {
			if inIdentifier {
				// End of identifier
				varName := currentVar.String()
				// Only include if it starts with a letter or underscore (not a number)
				if len(varName) > 0 && (varName[0] >= 'a' && varName[0] <= 'z' ||
					varName[0] >= 'A' && varName[0] <= 'Z' || varName[0] == '_') {
					// Check if it's not a keyword
					if !keywords[varName] {
						varMap[varName] = true
					}
				}
				currentVar.Reset()
				inIdentifier = false
			}
		}
	}

	// Handle last identifier if expression ends with one
	if inIdentifier {
		varName := currentVar.String()
		if len(varName) > 0 && (varName[0] >= 'a' && varName[0] <= 'z' ||
			varName[0] >= 'A' && varName[0] <= 'Z' || varName[0] == '_') {
			if !keywords[varName] {
				varMap[varName] = true
			}
		}
	}

	// Convert map to slice
	result := make([]string, 0, len(varMap))
	for varName := range varMap {
		result = append(result, varName)
	}

	return result
}

// truncateForDisplay truncates large values for readable display in logs/emails
func truncateForDisplay(val interface{}) interface{} {
	// For strings, truncate to 200 chars
	if str, ok := val.(string); ok {
		if len(str) > 200 {
			return str[:197] + "..."
		}
		return str
	}

	// For maps/objects, show structure but truncate nested content
	if m, ok := val.(map[string]interface{}); ok {
		if len(m) > 10 {
			// Too many keys, just show count
			return fmt.Sprintf("{%d keys}", len(m))
		}
		// Return as-is for small maps (let JSON formatter handle it)
		return m
	}

	// For arrays, limit to first 5 items
	if arr, ok := val.([]interface{}); ok {
		if len(arr) > 5 {
			return append(arr[:5], "...")
		}
		return arr
	}

	// Return other types as-is
	return val
}
