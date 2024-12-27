package taskengine

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/AvaProtocol/ap-avs/core/taskengine/macros"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
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

	for key, value := range macros.GetEnvs(nil) {
		fmt.Println("Set", key)
		r.jsvm.Set(key, value)
	}
	for key, value := range vm.vars {
		r.jsvm.Set(key, map[string]any{
			"data": value,
		})
	}

	return &r
}

func (r *JSProcessor) Execute(stepID string, node *avsproto.CustomCodeNode) (*avsproto.Execution_Step, error) {
	t0 := time.Now().Unix()

	s := &avsproto.Execution_Step{
		NodeId:     stepID,
		Log:        "",
		OutputData: "",
		Success:    true,
		Error:      "",
		StartAt:    t0,
	}

	var err error
	defer func() {
		s.EndAt = time.Now().Unix()
		s.Success = err == nil
		if err != nil {
			s.Error = err.Error()
		}
	}()

	var log strings.Builder

	log.WriteString(fmt.Sprintf("Start execute user-input JS code at %s", time.Now()))
	//result, err := r.jsvm.RunString("(function() {" + node.Source + "})()")
	result, err := r.jsvm.RunString(node.Source)
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
		// TODO: capsize
		if outputData, serilizeError := json.Marshal(resultValue); serilizeError == nil {
			s.OutputData = string(outputData)
		} else {
			log.WriteString("cannot serilize output data to log")
		}
		r.SetOutputVarForStep(stepID, resultValue)
	}

	return s, err
}
