package taskengine

import (
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

type JSProcessor struct {
	*CommonProcessor
	jsvm *goja.Runtime
}

func NewJSProcessor(vm *VM) *JSProcessor {
	r := JSProcessor{
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
		jsvm: goja.New(),
	}

	// These are built-in func
	for key, value := range macros.GetEnvs(nil) {
		r.jsvm.Set(key, value)
	}
	/// Binding the data from previous step into jsvm
	for key, value := range vm.vars {
		r.jsvm.Set(key, value)
	}

	return &r
}

func (r *JSProcessor) Execute(stepID string, node *avsproto.CustomCodeNode) (*avsproto.Execution_Step, error) {
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

	log.WriteString(fmt.Sprintf("Start execute user-input JS code at %s", time.Now()))
	result, err := r.jsvm.RunString("(function() {" + node.Source + "})()")

	log.WriteString(fmt.Sprintf("Complete Execute user-input JS code at %s", time.Now()))
	if err != nil {
		s.Success = false
		s.Error = err.Error()
		log.WriteString("\nerror running JavaScript code:")
		log.WriteString(err.Error())
	}
	s.Log = log.String()

	if result != nil {
		resultValue := result.Export()

		value, err := structpb.NewValue(resultValue)
		if err != nil {
			//return nil, fmt.Errorf("failed to convert to structpb.Value: %v", err)
			return s, err
		}
		pbResult, _ := anypb.New(value)

		s.OutputData = &avsproto.Execution_Step_CustomCode{
			CustomCode: &avsproto.CustomCodeNode_Output{
				Data: pbResult,
			},
		}
		r.SetOutputVarForStep(stepID, resultValue)
	}

	return s, err
}
