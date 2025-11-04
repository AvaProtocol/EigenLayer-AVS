package taskengine

import (
	"fmt"
	"regexp"
	"strings"

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
	vm.mu.Lock()
	for key, value := range vm.vars {
		if err := r.jsvm.Set(key, value); err != nil {
			vm.mu.Unlock()
			if vm.logger != nil {
				vm.logger.Error("failed to set variable in JS VM", "key", key, "error", err)
			}
			return nil
		}
	}
	vm.mu.Unlock()

	if registry != nil {
		if err := r.jsvm.Set("require", registry.RequireFunction(jsvm)); err != nil {
			if vm.logger != nil {
				vm.logger.Error("failed to set require function in JS VM", "error", err)
			}
		}
	}

	return &r
}

// NewJSProcessorWithIsolatedVars creates a JS processor with isolated variables for parallel execution
func NewJSProcessorWithIsolatedVars(vm *VM, isolatedVars map[string]any) *JSProcessor {
	// ALWAYS create a fresh JS VM for isolated execution to prevent variable sharing
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

	// Set built-in macros first
	for key, value := range macros.GetEnvs(nil) {
		if err := r.jsvm.Set(key, value); err != nil {
			if vm.logger != nil {
				vm.logger.Error("failed to set macro env in JS VM", "key", key, "error", err)
			}
		}
	}

	// Use isolated variables instead of shared VM vars
	// Set these AFTER macros to ensure they take precedence
	for key, value := range isolatedVars {
		// Debug: Log each variable being set with more detail
		if vm.logger != nil {
			vm.logger.Info("Setting isolated JS variable",
				"key", key,
				"value", value,
				"valueType", fmt.Sprintf("%T", value))
		}
		if err := r.jsvm.Set(key, value); err != nil {
			if vm.logger != nil {
				vm.logger.Error("failed to set isolated variable in JS VM", "key", key, "error", err)
			}
			return nil
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
			// Trim whitespace from named imports and convert 'as' syntax to colon syntax for Goja compatibility
			trimmedImports := strings.TrimSpace(namedImports)
			// Convert ES6 'as' syntax to JavaScript colon syntax: "v4 as uuidv4" -> "v4: uuidv4"
			convertedImports := strings.ReplaceAll(trimmedImports, " as ", ": ")
			transformed.WriteString(fmt.Sprintf("const { %s } = require('%s');\n", convertedImports, moduleName))
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
	// A simple regex to detect import statements (can be made more robust).
	// This regex looks for lines starting with `import` followed by anything, then `from`.
	// It also handles multiline imports to some extent by looking for `import {`
	// and `import * as` patterns.
	importRegex := regexp.MustCompile(`(?m)^\s*import\s+.*\s+from\s+['"].*['"];?|import\s*\{[^}]*\}\s*from\s*['"].*['"];?|import\s*\*\s*as\s+\w+\s+from\s*['"].*['"];?`)
	return importRegex.MatchString(code)
}

func containsModuleSyntax(code string) bool {
	// Split code into lines and check each line
	lines := strings.Split(code, "\n")

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		// Skip comment lines
		if strings.HasPrefix(trimmedLine, "//") || strings.HasPrefix(trimmedLine, "/*") {
			continue
		}

		// Check for ES6 import/export statements
		if regexp.MustCompile(`\b(import|export)\s+`).MatchString(line) {
			return true
		}

		// Check for dynamic imports
		if regexp.MustCompile(`\bimport\s*\(`).MatchString(line) {
			return true
		}

		// Check for require statements
		if regexp.MustCompile(`\brequire\s*\(`).MatchString(line) {
			return true
		}
	}

	return false
}

func containsReturnStatement(code string) bool {
	// Check for return statements that are not in comments
	lines := strings.Split(code, "\n")

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		// Skip comment lines
		if strings.HasPrefix(trimmedLine, "//") || strings.HasPrefix(trimmedLine, "/*") {
			continue
		}

		// Check for return statements
		if regexp.MustCompile(`\breturn\b`).MatchString(line) {
			return true
		}
	}

	return false
}

func (r *JSProcessor) Execute(stepID string, node *avsproto.CustomCodeNode) (*avsproto.Execution_Step, error) {
	// Use shared function to create execution step
	executionStep := createNodeExecutionStep(stepID, avsproto.NodeType_NODE_TYPE_CUSTOM_CODE, r.vm)

	var sb strings.Builder
	sb.WriteString(formatNodeExecutionLogHeader(executionStep))

	var err error
	defer func() {
		finalizeStep(executionStep, err == nil, err, "", sb.String())
	}()

	// Get configuration from Config message (static configuration)
	if err = validateNodeConfig(node.Config, "CustomCodeNode"); err != nil {
		sb.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		return executionStep, err
	}

	langStr := node.Config.Lang.String()
	sourceStr := node.Config.Source

	if sourceStr == "" {
		err = NewMissingRequiredFieldError("source")
		sb.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		return executionStep, err
	}

	// LANGUAGE ENFORCEMENT: CustomCodeNode uses JavaScript
	// Using centralized ValidateInputByLanguage for consistency (includes size check)
	if err = ValidateInputByLanguage(sourceStr, avsproto.Lang_LANG_JAVASCRIPT); err != nil {
		sb.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		return executionStep, err
	}

	// Preprocess source for template variables
	sourceStr = r.vm.preprocessTextWithVariableMapping(sourceStr)

	sb.WriteString(" Lang: ")
	sb.WriteString(langStr)

	// Set variables in the JS environment from vm.vars
	r.vm.mu.Lock()                      // Lock for reading r.vm.vars
	for key, value := range r.vm.vars { // Direct map iteration
		// key is already a string due to map[string]any definition for r.vm.vars
		if err := r.jsvm.Set(key, value); err != nil {
			r.vm.mu.Unlock()
			if r.vm.logger != nil {
				r.vm.logger.Error("failed to set variable in JS VM", "key", key, "error", err)
			}
			sb.WriteString(fmt.Sprintf("\nError setting JS variable '%s': %v", key, err))
			err = fmt.Errorf("failed to set JS variable '%s': %w", key, err)
			return executionStep, err
		}
	}
	r.vm.mu.Unlock()

	// Transform the code if it contains module syntax or return statements
	codeToExecute := sourceStr

	// Check if the code contains ES6 imports and transform them
	if containsES6Imports(codeToExecute) {
		codeToExecute = transformES6Imports(codeToExecute)
		sb.WriteString("\nTransformed ES6 imports to CommonJS")
	} else if containsModuleSyntax(codeToExecute) || containsReturnStatement(codeToExecute) {
		// If it contains CommonJS require statements or return statements, wrap it in a function
		codeToExecute = wrapCode(codeToExecute)
		sb.WriteString("\nWrapped code in function to support return statements")
	}

	// Execute the script
	result, err := r.jsvm.RunString(codeToExecute)
	if err != nil {
		sb.WriteString(fmt.Sprintf("\nError executing script: %s", err.Error()))
		err = fmt.Errorf("failed to execute script: %w", err)
		return executionStep, err
	}

	// Convert the result to a protobuf struct
	exportedVal := result.Export()
	outputStruct, err := structpb.NewValue(exportedVal)
	if err != nil {
		sb.WriteString(fmt.Sprintf("\nError converting execution result to Value: %s", err.Error()))
		err = fmt.Errorf("failed to convert execution result to Value: %w", err)
		return executionStep, err
	}

	executionStep.OutputData = &avsproto.Execution_Step_CustomCode{
		CustomCode: &avsproto.CustomCodeNode_Output{
			Data: outputStruct,
		},
	}

	// Use shared function to set output variable for this step
	setNodeOutputData(r.CommonProcessor, stepID, exportedVal)

	sb.WriteString(fmt.Sprintf("\nExecution result: %v", exportedVal))

	// Use shared function to finalize execution step with success
	finalizeStep(executionStep, true, nil, "", sb.String())

	return executionStep, nil
}
