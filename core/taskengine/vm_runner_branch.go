package taskengine

import (
	"fmt"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
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

func (r *BranchProcessor) Execute(stepID string, node *avsproto.BranchNode) (*avsproto.Execution_Step, error) {
	t0 := time.Now()
	s := &avsproto.Execution_Step{
		NodeId:     stepID,
		OutputData: "",
		Log:        "",
		Error:      "",
		Success:    true,
		StartAt:    t0.Unix(),
	}

	var sb strings.Builder
	sb.WriteString("Execute Branch: ")
	sb.WriteString(stepID)
	outcome := ""

	// Initialize goja runtime
	jsvm := goja.New()

	// Set variables in the JS environment. The value is wrapped into a data, follow a similar approach by other nocode provider
	// even though we arent necessarily need to do this
	for key, value := range r.vm.vars {
		jsvm.Set(key, value)
	}

	for _, statement := range node.Conditions {
		if strings.EqualFold(statement.Type, "else") {
			outcome = fmt.Sprintf("%s.%s", stepID, statement.Id)
			sb.WriteString("\n")
			sb.WriteString(time.Now().String())
			sb.WriteString("evaluate else, follow else path")
			sb.WriteString(outcome)
			s.Log = sb.String()
			s.OutputData = outcome
			return s, nil
		}

		sb.WriteString(fmt.Sprintf("\n%s evaluate condition: %s expression: `%s`", time.Now(), statement.Id, statement.Expression))

		// Evaluate the condition using goja, notice how we wrap into a function to prevent the value is leak across goja run
		script := fmt.Sprintf(`(() => %s )()`, strings.Trim(statement.Expression, "\n \t"))

		result, err := jsvm.RunString(script)
		if err != nil {
			s.Success = false
			s.Error = fmt.Errorf("error evaluating the statement: %w", err).Error()
			sb.WriteString("error evaluating expression")
			s.Log = sb.String()
			s.EndAt = time.Now().Unix()
			return s, fmt.Errorf("error evaluating the statement: %w", err)
		}

		branchResult, ok := result.Export().(bool)
		if !ok {
			s.Success = false
			s.Error = fmt.Errorf("error evaluating the statement: %w", err).Error()
			sb.WriteString("error evaluating expression")
			s.Log = sb.String()
			s.EndAt = time.Now().Unix()
			return s, fmt.Errorf("error evaluating the statement: %w", err)
		}

		if branchResult {
			outcome = fmt.Sprintf("%s.%s", stepID, statement.Id)
			sb.WriteString("\nexpression result to true. follow path ")
			sb.WriteString(outcome)
			s.Log = sb.String()
			s.OutputData = outcome
			s.EndAt = time.Now().Unix()
			return s, nil
		}
	}

	sb.WriteString("\nno condition matched. halt execution")
	s.Log = sb.String()
	s.EndAt = time.Now().Unix()
	return s, nil
}
