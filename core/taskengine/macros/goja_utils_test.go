package macros

import (
	"testing"

	"github.com/dop251/goja"
)

func TestObjectToString(t *testing.T) {
	runtime := goja.New()
	
	ConfigureGojaRuntime(runtime)
	
	result, err := runtime.RunString("({}).toString()")
	if err != nil {
		t.Errorf("Failed to run script: %v", err)
	}
	
	if result.String() != "[object Object]" {
		t.Errorf("Expected '[object Object]' but got '%s'", result.String())
	}
	
	result, err = runtime.RunString("'Result: ' + {}")
	if err != nil {
		t.Errorf("Failed to run script: %v", err)
	}
	
	if result.String() != "Result: [object Object]" {
		t.Errorf("Expected 'Result: [object Object]' but got '%s'", result.String())
	}
	
	result, err = runtime.RunString("'Value: ' + {a: {b: 1}}")
	if err != nil {
		t.Errorf("Failed to run script: %v", err)
	}
	
	if result.String() != "Value: [object Object]" {
		t.Errorf("Expected 'Value: [object Object]' but got '%s'", result.String())
	}
}
