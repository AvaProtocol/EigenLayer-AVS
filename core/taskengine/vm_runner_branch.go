package taskengine

import (
	"fmt"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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
	}

	return nil
}

func (r *BranchProcessor) Execute(stepID string, node *avsproto.BranchNode) (*avsproto.Execution_Step, *Step, error) {
	t0 := time.Now().UnixMilli()

	// Look up the task node to get the name
	var nodeName string = "unknown"
	r.vm.mu.Lock()
	if taskNode, exists := r.vm.TaskNodes[stepID]; exists {
		nodeName = taskNode.Name
	}
	r.vm.mu.Unlock()

	executionStep := &avsproto.Execution_Step{
		Id:      stepID,
		Success: false, // Default to false, set to true if a condition matches
		StartAt: t0,
		Type:    avsproto.NodeType_NODE_TYPE_BRANCH.String(),
		Name:    nodeName,
	}

	var log strings.Builder
	log.WriteString(fmt.Sprintf("Start branch execution for node %s at %s\n", stepID, time.Now()))

	// Get conditions from Config message (static configuration)
	if node.Config == nil {
		err := fmt.Errorf("BranchNode Config is nil")
		log.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		executionStep.Error = err.Error()
		executionStep.Success = false
		executionStep.Log = log.String()
		executionStep.EndAt = time.Now().UnixMilli()
		return executionStep, nil, err
	}

	conditions := node.Config.Conditions
	if len(conditions) == 0 {
		err := fmt.Errorf("there is no condition to evaluate")
		log.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		executionStep.Error = err.Error()
		executionStep.Success = false
		executionStep.Log = log.String()
		executionStep.EndAt = time.Now().UnixMilli()
		return executionStep, nil, err
	}

	if conditions[0].Type != "if" {
		err := fmt.Errorf("the first condition need to be an if but got: %s", conditions[0].Type)
		log.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		executionStep.Error = err.Error()
		executionStep.Success = false
		executionStep.Log = log.String()
		executionStep.EndAt = time.Now().UnixMilli()
		return executionStep, nil, err
	}

	// Evaluate conditions
	for i, condition := range conditions {
		log.WriteString(fmt.Sprintf("Evaluating condition '%s': %s\n", condition.Id, condition.Expression))

		// Handle 'else' conditions specially - they should always be true if reached
		if condition.Type == "else" {
			// Ensure else condition is not the first condition (validation should catch this, but double-check)
			if i == 0 {
				err := fmt.Errorf("else condition cannot be the first condition")
				log.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
				executionStep.Error = err.Error()
				executionStep.Success = false
				executionStep.Log = log.String()
				executionStep.EndAt = time.Now().UnixMilli()
				return executionStep, nil, err
			}
			log.WriteString(fmt.Sprintf("Condition '%s' is an 'else' condition, automatically true\n", condition.Id))
			executionStep.Success = true
			executionStep.OutputData = &avsproto.Execution_Step_Branch{
				Branch: &avsproto.BranchNode_Output{
					ConditionId: fmt.Sprintf("%s.%s", stepID, condition.Id),
				},
			}
			log.WriteString(fmt.Sprintf("Branching to else condition '%s'\n", condition.Id))
			executionStep.Log = log.String()
			executionStep.EndAt = time.Now().UnixMilli()

			// Find the next step in the plan based on this condition ID
			r.vm.mu.Lock() // Lock for reading vm.plans
			nextStepInPlan, exists := r.vm.plans[fmt.Sprintf("%s.%s", stepID, condition.Id)]
			r.vm.mu.Unlock()
			if !exists {
				// If no plan exists, create a simple step for unit testing purposes
				nextStepInPlan = &Step{NodeID: fmt.Sprintf("%s.%s", stepID, condition.Id), Next: []string{}}
			}
			return executionStep, nextStepInPlan, nil
		}

		// For 'if' conditions, evaluate the expression
		// Check if expression is empty or only whitespace
		trimmedExpression := strings.TrimSpace(condition.Expression)
		if trimmedExpression == "" {
			log.WriteString(fmt.Sprintf("Condition '%s' has empty expression, treating as false\n", condition.Id))
			continue // Skip this condition (treat as false)
		}

		// Preprocess the expression using the VM's current variable context
		processedExpression := condition.Expression
		if strings.Contains(processedExpression, "{{") {
			processedExpression = r.vm.preprocessText(processedExpression)
		} else {
			processedExpression = r.vm.preprocessTextWithVariableMapping(condition.Expression)
		}
		log.WriteString(fmt.Sprintf("Processed expression for '%s': %s\n", condition.Id, processedExpression))

		// Create a temporary JS VM to evaluate the processed expression
		jsvm := NewGojaVM()

		// Populate the JS VM with variables from the main VM
		r.vm.mu.Lock()                      // Lock for reading vm.vars
		for key, value := range r.vm.vars { // CHANGED from r.vm.vars.Range
			if err := jsvm.Set(key, value); err != nil {
				r.vm.mu.Unlock()
				err := fmt.Errorf("failed to set var '%s' in JS VM for branch condition: %w", key, err)
				log.WriteString(fmt.Sprintf("Error setting JS var: %s\n", err.Error()))
				executionStep.Error = err.Error()
				executionStep.Log = log.String()
				executionStep.EndAt = time.Now().UnixMilli()
				return executionStep, nil, err
			}
		}
		r.vm.mu.Unlock()

		// Evaluate the expression
		value, err := jsvm.RunString(fmt.Sprintf("(%s)", processedExpression)) // Wrap in parens for safety
		if err != nil {
			log.WriteString(fmt.Sprintf("Error evaluating expression for '%s': %s\n", condition.Id, err.Error()))
			// Check if this is a syntax error or reference error (both should fail the branch)
			errorStr := err.Error()
			if strings.Contains(errorStr, "SyntaxError") || strings.Contains(errorStr, "unexpected") || strings.Contains(errorStr, "Unexpected") ||
				strings.Contains(errorStr, "ReferenceError") || strings.Contains(errorStr, "is not defined") {
				// Syntax errors and undefined variable errors should fail the entire branch
				executionStep.Error = fmt.Sprintf("failed to evaluate expression for condition '%s': %v", condition.Id, err)
				executionStep.Success = false
				executionStep.Log = log.String()
				executionStep.EndAt = time.Now().UnixMilli()
				return executionStep, nil, fmt.Errorf("failed to evaluate expression for condition '%s': %w", condition.Id, err)
			}
			// Other runtime errors should just skip this condition
			continue
		}

		boolValue, ok := value.Export().(bool)
		if !ok {
			log.WriteString(fmt.Sprintf("Expression for '%s' did not evaluate to a boolean value, got: %T %v\n", condition.Id, value.Export(), value.Export()))
			continue
		}

		log.WriteString(fmt.Sprintf("Condition '%s' evaluated to: %t\n", condition.Id, boolValue))
		if boolValue {
			executionStep.Success = true
			executionStep.OutputData = &avsproto.Execution_Step_Branch{
				Branch: &avsproto.BranchNode_Output{
					ConditionId: fmt.Sprintf("%s.%s", stepID, condition.Id),
				},
			}
			log.WriteString(fmt.Sprintf("Branching to condition '%s'\n", condition.Id))
			executionStep.Log = log.String()
			executionStep.EndAt = time.Now().UnixMilli()

			// Find the next step in the plan based on this condition ID
			r.vm.mu.Lock() // Lock for reading vm.plans
			nextStepInPlan, exists := r.vm.plans[fmt.Sprintf("%s.%s", stepID, condition.Id)]
			r.vm.mu.Unlock()
			if !exists {
				// If no plan exists, create a simple step for unit testing purposes
				nextStepInPlan = &Step{NodeID: fmt.Sprintf("%s.%s", stepID, condition.Id), Next: []string{}}
			}
			return executionStep, nextStepInPlan, nil
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
		executionStep.Error = noConditionMetError
		executionStep.Success = false
		executionStep.Log = log.String()
		executionStep.EndAt = time.Now().UnixMilli()
		return executionStep, nil, fmt.Errorf(noConditionMetError)
	} else if hasElseCondition {
		// If there's an else condition but we reached here, it means the else condition failed to execute
		// This should be an error because else conditions should always execute if reached
		noConditionMetError := "no branch condition met"
		log.WriteString(noConditionMetError + "\n")
		executionStep.Error = noConditionMetError
		executionStep.Success = false
		executionStep.Log = log.String()
		executionStep.EndAt = time.Now().UnixMilli()
		return executionStep, nil, fmt.Errorf(noConditionMetError)
	} else {
		// If there are only 'if' conditions and none matched, this is a valid "no-op" scenario
		log.WriteString("No conditions matched and no else condition defined - this is a valid no-op.\n")
		executionStep.Success = true
		executionStep.OutputData = nil // No branch action taken
		executionStep.Log = log.String()
		executionStep.EndAt = time.Now().UnixMilli()
		return executionStep, nil, nil // Success with no next step
	}
}
