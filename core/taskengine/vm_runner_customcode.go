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

var (
	importRegex  = regexp.MustCompile(`(?m)^\s*import\s+(?:(\w+)|(?:\*\s+as\s+(\w+))|(?:{\s*([^}]+)\s*}))\s+from\s+['"]([^'"]+)['"];?\s*$`)
	requireRegex = regexp.MustCompile(`\brequire\s*\(\s*['"]`)
)

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

func transformES6Imports(code string) string {
	// Find all import statements
	matches := importRegex.FindAllStringSubmatch(code, -1)
	if len(matches) == 0 {
		return code
	}

	// Build the transformed code
	var transformed strings.Builder
	transformed.WriteString("(function() {\n")

	// Add require statements for each import
	for _, match := range matches {
		defaultImport := match[1] // default import
		namespace := match[2]     // * as namespace
		namedImports := match[3]  // { import1, import2 as alias2 }
		moduleName := match[4]    // module name

		if defaultImport != "" {
			// Handle default import: import name from 'module'
			transformed.WriteString(fmt.Sprintf("const %s = require('%s');\n", defaultImport, moduleName))
		} else if namespace != "" {
			// Handle namespace import: import * as name from 'module'
			transformed.WriteString(fmt.Sprintf("const %s = require('%s');\n", namespace, moduleName))
		} else if namedImports != "" {
			// Handle named imports: import { name1, name2 as alias2 } from 'module'
			transformed.WriteString(fmt.Sprintf("const { %s } = require('%s');\n", namedImports, moduleName))
		}
	}

	// Add the original code with imports removed
	codeWithoutImports := importRegex.ReplaceAllString(code, "")
	transformed.WriteString(codeWithoutImports)
	transformed.WriteString("\n})()")

	return transformed.String()
}

func wrapCode(code string) string {
	return "(function() {\n" + code + "\n})()"
}

func containsES6Imports(code string) bool {
	importRegex := regexp.MustCompile(`(?m)^\s*(import|export)\s+`)
	return importRegex.MatchString(code)
}

func containsModuleSyntax(code string) bool {
	importRegex := regexp.MustCompile(`(?m)^\s*(import|export)\s+`)
	requireRegex := regexp.MustCompile(`\brequire\s*\(\s*['"]`)
	return importRegex.MatchString(code) || requireRegex.MatchString(code)
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

	// Transform ES6 imports to requires if needed, otherwise just wrap the code
	if containsES6Imports(codeToRun) {
		codeToRun = transformES6Imports(codeToRun)
	} else {
		codeToRun = wrapCode(codeToRun)
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
		} else {
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
