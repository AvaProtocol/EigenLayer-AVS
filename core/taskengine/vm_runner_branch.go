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

func (r *BranchProcessor) Validate(node *avsproto.BranchNode) error {
	if node == nil {
		return fmt.Errorf("invalid node data")
	}
	if len(node.Conditions) == 0 {
		return fmt.Errorf("there is no condition to evaluate")
	}

	if node.Conditions[0].Type != "if" {
		return fmt.Errorf("the first condition need to be an if but got :%s", node.Conditions[0].Type)
	}

	for i, condition := range node.Conditions {
		if condition == nil {
			return fmt.Errorf("condition at index %d is nil", i)
		}

		if condition.Id == "" {
			return fmt.Errorf("condition at index %d has empty ID", i)
		}

		if condition.Type == "" {
			return fmt.Errorf("condition at index %d has empty type", i)
		}

		if condition.Type != "if" && condition.Type != "else" {
			return fmt.Errorf("condition at index %d has invalid type: %s (must be 'if' or 'else')", i, condition.Type)
		}

		if condition.Type == "else" && i < len(node.Conditions)-1 {
			if r.vm.logger != nil {
				r.vm.logger.Warn("'else' condition is not the last one, subsequent conditions will be ignored")
			}
		}
	}

	return nil
}

func (r *BranchProcessor) Execute(stepID string, node *avsproto.BranchNode) (*avsproto.Execution_Step, *Step, error) {
	t0 := time.Now().UnixMilli()
	executionStep := &avsproto.Execution_Step{
		NodeId:  stepID,
		Success: false, // Default to false, set to true if a condition matches
		StartAt: t0,
	}

	var log strings.Builder
	log.WriteString(fmt.Sprintf("Start branch execution for node %s at %s\n", stepID, time.Now()))

	// Evaluate conditions
	for _, condition := range node.Conditions {
		log.WriteString(fmt.Sprintf("Evaluating condition '%s': %s\n", condition.Id, condition.Expression))

		// Preprocess the expression using the VM's current variable context
		processedExpression := r.vm.preprocessText(condition.Expression)
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
			// Don't fail the whole branch node for one bad condition expression, just log and continue
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
				err := fmt.Errorf("branch condition '%s' met, but no corresponding path defined in plans", condition.Id)
				executionStep.Error = err.Error()
				executionStep.Success = false
				return executionStep, nil, err
			}
			return executionStep, nextStepInPlan, nil
		}
	}

	// If no condition evaluated to true
	log.WriteString("No branch condition evaluated to true.\n")
	// Removed DefaultBranch handling as it's not in the protobuf definition

	// No condition true, and no default branch
	noConditionMetError := "no branch condition met"
	log.WriteString(noConditionMetError + "\n")
	executionStep.Error = noConditionMetError
	executionStep.Success = false
	executionStep.Log = log.String()
	executionStep.EndAt = time.Now().UnixMilli()
	return executionStep, nil, fmt.Errorf(noConditionMetError)
}
