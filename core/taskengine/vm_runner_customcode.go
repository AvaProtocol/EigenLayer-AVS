package taskengine

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dop251/goja"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/modules"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

type JSProcessor struct {
	*CommonProcessor
	jsvm     *goja.Runtime
	registry *modules.Registry
}

func NewJSProcessor(vm *VM) *JSProcessor {
	jsvm, registry, err := NewGojaVMWithModules()
	if err != nil {
		if vm.logger != nil {
			vm.logger.Error("failed to initialize JS VM with modules", "error", err)
		}
		jsvm = NewGojaVM()
	}

	r := JSProcessor{
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
		jsvm:     jsvm,
		registry: registry,
	}

	// These are built-in func
	for key, value := range macros.GetEnvs(nil) {
		if err := r.jsvm.Set(key, value); err != nil {
			if vm.logger != nil {
				vm.logger.Error("failed to set macro env in JS VM", "key", key, "error", err)
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
	
	if registry != nil {
		if err := r.jsvm.Set("require", registry.RequireFunction(jsvm)); err != nil {
			if vm.logger != nil {
				vm.logger.Error("failed to set require function in JS VM", "error", err)
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
	
	codeToRun := node.Source
	if !containsModuleSyntax(codeToRun) {
		codeToRun = "(function() {" + codeToRun + "})()"
	}
	
	result, err := r.jsvm.RunString(codeToRun)

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

func containsModuleSyntax(code string) bool {
	importRegex := regexp.MustCompile(`(?m)^\s*(import|export)\s+`)
	requireRegex := regexp.MustCompile(`\brequire\s*\(\s*['"]`)
	return importRegex.MatchString(code) || requireRegex.MatchString(code)
}
