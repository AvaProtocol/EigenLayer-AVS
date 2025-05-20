package taskengine

import (
	"fmt"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
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
	r := FilterProcessor{
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
		jsvm: NewGojaVM(),
	}

	// These are built-in func
	for key, value := range macros.GetEnvs(nil) {
		if err := r.jsvm.Set(key, value); err != nil {
			if r.vm.logger != nil {
				r.vm.logger.Error("failed to set macro env in JS VM", "key", key, "error", err)
			}
		}
	}
	// Binding the data from previous step into jsvm
	for key, value := range vm.vars {
		if err := r.jsvm.Set(key, value); err != nil {
			if vm.logger != nil {
				vm.logger.Error("failed to set variable in JS VM", "key", key, "error", err)
			}
		}
	}

	return &r
}

func (r *FilterProcessor) Execute(stepID string, node *avsproto.FilterNode) (*avsproto.Execution_Step, error) {
	t0 := time.Now().UnixMilli()
	s := &avsproto.Execution_Step{
		NodeId:     stepID,
		Log:        "",
		OutputData: nil,
		Success:    true,
		Error:      "",
		StartAt:    t0,
	}

	var err error
	defer func() {
		s.EndAt = time.Now().UnixMilli()
		s.Success = err == nil
		if err != nil {
			s.Error = err.Error()
		}
	}()

	var log strings.Builder
	log.WriteString(fmt.Sprintf("start filter input %s with expression %s at %s", node.Input, node.Expression, time.Now()))
	script := fmt.Sprintf(`values.filter((value, index, items) => { %s})`, node.Expression)
	if !strings.Contains(node.Expression, "return") {
		script = fmt.Sprintf(`values.filter((value, index, items) => { return %s})`, node.Expression)
	}

	if err := r.jsvm.Set("values", r.vm.vars[node.Input]); err != nil {
		if r.vm.logger != nil {
			r.vm.logger.Error("failed to set values in JS VM", "error", err)
		}
	}

	result, err := r.jsvm.RunString(script)
	if err != nil {
		log.WriteString(fmt.Sprintf("an error has occurred when processing your filter expression: %v", err))
		s.Log = log.String()
		s.Error = err.Error()
		return s, err
	}

	log.WriteString(fmt.Sprintf("\ncomplete filter input %s", node.Input))
	// Convert the result back to a Go slice of empty interfaces because we dont know its type, but we do know it's an array
	filteredValues := result.Export().([]interface{})

	s.Log = log.String()
	if err != nil {
		s.Success = false
		s.Error = err.Error()
		return s, err
	}
	r.SetOutputVarForStep(stepID, filteredValues)

	value, err := structpb.NewValue(filteredValues)
	if err == nil {
		pbResult, _ := anypb.New(value)
		s.OutputData = &avsproto.Execution_Step_Filter{
			Filter: &avsproto.FilterNode_Output{
				Data: pbResult,
			},
		}
	}

	if err != nil {
		log.WriteString(fmt.Sprintf("succeed perform filter input but cannot serialize data to the log. ignore data serlization: %v", err))
	}

	return s, nil
}
