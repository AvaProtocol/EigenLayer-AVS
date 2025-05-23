package taskengine

import (
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
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
		jsvm: NewGojaVM(),
	}

	// These are built-in func
	for key, value := range macros.GetEnvs(nil) {
		if err := r.jsvm.Set(key, value); err != nil {
			if vm.logger != nil {
				vm.logger.Error("failed to set macro env in JS VM", "key", key, "error", err)
			}
		}
	}
	/// Binding the data from previous step into jsvm
	for key, value := range vm.vars {
		if err := r.jsvm.Set(key, value); err != nil {
			if vm.logger != nil {
				vm.logger.Error("failed to set variable in JS VM", "key", key, "error", err)
			}
		}
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

		protoValue, errConv := structpb.NewValue(resultValue)
		if errConv != nil {
			s.Log += fmt.Sprintf("\nfailed to convert JS result to protobuf Value: %v", errConv)
			// If conversion to structpb.Value fails, OutputData will remain nil or its previous state.
			// The overall step success might still depend on the 'err' from r.jsvm.RunString()
		} else {
			// This assumes CustomCodeNode_Output.Data is defined as google.protobuf.Value in your .proto file
			s.OutputData = &avsproto.Execution_Step_CustomCode{
				CustomCode: &avsproto.CustomCodeNode_Output{
					Data: protoValue,
				},
			}
		}
		r.SetOutputVarForStep(stepID, resultValue)
	}

	return s, err
}
