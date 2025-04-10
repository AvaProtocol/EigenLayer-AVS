package taskengine

import (
	"fmt"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/dop251/goja"
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

	return nil
}

func (r *BranchProcessor) Execute(stepID string, node *avsproto.BranchNode) (*avsproto.Execution_Step, error) {
	t0 := time.Now()
	s := &avsproto.Execution_Step{
		NodeId:     stepID,
		OutputData: nil,
		Log:        "",
		Error:      "",
		Success:    true,
		StartAt:    t0.UnixMilli(),
	}

	var sb strings.Builder
	sb.WriteString("Execute Branch: ")
	sb.WriteString(stepID)
	outcome := ""

	// Initialize goja runtime
	jsvm := goja.New()
	
	macros.ConfigureGojaRuntime(jsvm)

	// Set variables in the JS environment. The value is wrapped into a data, follow a similar approach by other nocode provider
	// even though we arent necessarily need to do this
	for key, value := range r.vm.vars {
		jsvm.Set(key, value)
	}

	if err := r.Validate(node); err != nil {
		sb.WriteString("\nInvalid branch node: ")
		sb.WriteString(err.Error())
		s.Log = sb.String()
		s.Success = false
		s.Error = err.Error()
		return nil, err
	}

	for _, statement := range node.Conditions {
		if strings.EqualFold(statement.Type, "else") {
			outcome = fmt.Sprintf("%s.%s", stepID, statement.Id)
			sb.WriteString("\n")
			sb.WriteString(time.Now().String())
			sb.WriteString("evaluate else, follow else path")
			sb.WriteString(outcome)
			s.Log = sb.String()
			s.Success = true
			s.OutputData = &avsproto.Execution_Step_Branch{
				Branch: &avsproto.BranchNode_Output{
					ConditionId: outcome,
				},
			}
			return s, nil
		}

		sb.WriteString(fmt.Sprintf("\n%s evaluate condition: %s expression: `%s`", time.Now(), statement.Id, statement.Expression))

		// Evaluate the condition using goja, notice how we wrap into a function to prevent the value is leak across goja run
		expression := strings.Trim(statement.Expression, "\n \t")

		var branchResult bool

		if expression == "" {
			branchResult = false
		} else {
			script := fmt.Sprintf(`(() => %s )()`, expression)
			result, err := jsvm.RunString(script)

			if err != nil {
				s.Success = false
				s.Error = fmt.Errorf("error evaluating the statement: %w", err).Error()
				sb.WriteString("error evaluating expression")
				s.Log = sb.String()
				s.EndAt = time.Now().UnixMilli()
				return s, fmt.Errorf("error evaluating the statement: %w", err)
			}

			var ok bool
			branchResult, ok = result.Export().(bool)
			if !ok {
				s.Success = false
				s.Error = fmt.Errorf("error evaluating the statement: %w", err).Error()
				sb.WriteString("error evaluating expression")
				s.Log = sb.String()
				s.EndAt = time.Now().UnixMilli()
				return s, fmt.Errorf("error evaluating the statement: %w", err)
			}
		}

		if branchResult {
			outcome = fmt.Sprintf("%s.%s", stepID, statement.Id)
			sb.WriteString("\nexpression resolves to true, follow path")
			sb.WriteString(outcome)
			s.Log = sb.String()
			s.OutputData = &avsproto.Execution_Step_Branch{
				Branch: &avsproto.BranchNode_Output{
					ConditionId: outcome,
				},
			}
			s.EndAt = time.Now().UnixMilli()
			return s, nil
		} else {
			sb.WriteString("\nexpression resolves to false, move to next statement\n")
		}
	}

	sb.WriteString("\nno condition matched. halt execution")
	s.Log = sb.String()
	s.EndAt = time.Now().UnixMilli()
	if r.vm.logger != nil {
		r.vm.logger.Debug("No condition matched. halt execution", "execution_log", s.Log)
	}
	return s, nil
}
