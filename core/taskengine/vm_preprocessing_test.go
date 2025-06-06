package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestRenderString(t *testing.T) {
	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "sampletaskid1",
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	vm.vars["test"] = "world"
	if vm.vars == nil {
		t.Errorf("expected VM vars to be initialized")
	}

	if vm.vars["test"] != "world" {
		t.Errorf("expected test variable to be set")
	}
}

func TestPreprocessText(t *testing.T) {
	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "sampletaskid1",
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	vm.vars["test"] = map[string]interface{}{
		"nested": map[string]interface{}{
			"value": "world",
		},
	}

	result := vm.preprocessText("hello {{test.nested.value}}")
	if result != "hello world" {
		t.Errorf("expect 'hello world' but got '%s'", result)
	}

	vm.vars["number"] = 42
	result = vm.preprocessText("the answer is {{number}}")
	if result != "the answer is 42" {
		t.Errorf("expect 'the answer is 42' but got '%s'", result)
	}

	vm.vars["boolean"] = true
	result = vm.preprocessText("value is {{boolean}}")
	if result != "value is true" {
		t.Errorf("expect 'value is true' but got '%s'", result)
	}

	vm.vars["array"] = []interface{}{"first", "second", "third"}
	result = vm.preprocessText("first item is {{array.0}}")
	if result != "first item is first" {
		t.Errorf("expect 'first item is first' but got '%s'", result)
	}

	result = vm.preprocessText("second item is {{array.1}}")
	if result != "second item is second" {
		t.Errorf("expect 'second item is second' but got '%s'", result)
	}

	vm.vars["complex"] = map[string]interface{}{
		"data": map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{
					"name": "item1",
					"value": 100,
				},
				map[string]interface{}{
					"name": "item2",
					"value": 200,
				},
			},
		},
	}

	result = vm.preprocessText("first item name: {{complex.data.items.0.name}}")
	if result != "first item name: item1" {
		t.Errorf("expect 'first item name: item1' but got '%s'", result)
	}

	result = vm.preprocessText("second item value: {{complex.data.items.1.value}}")
	if result != "second item value: 200" {
		t.Errorf("expect 'second item value: 200' but got '%s'", result)
	}

	result = vm.preprocessText("missing variable: {{nonexistent}}")
	if result != "missing variable: {{nonexistent}}" {
		t.Errorf("expect 'missing variable: {{nonexistent}}' but got '%s'", result)
	}

	result = vm.preprocessText("missing nested: {{test.missing.value}}")
	if result != "missing nested: {{test.missing.value}}" {
		t.Errorf("expect 'missing nested: {{test.missing.value}}' but got '%s'", result)
	}

	result = vm.preprocessText("multiple {{test.nested.value}} and {{number}} values")
	if result != "multiple world and 42 values" {
		t.Errorf("expect 'multiple world and 42 values' but got '%s'", result)
	}

	result = vm.preprocessText("no templates here")
	if result != "no templates here" {
		t.Errorf("expect 'no templates here' but got '%s'", result)
	}

	result = vm.preprocessText("{{")
	if result != "{{" {
		t.Errorf("expect '{{' but got '%s'", result)
	}

	result = vm.preprocessText("}}")
	if result != "}}" {
		t.Errorf("expect '}}' but got '%s'", result)
	}

	result = vm.preprocessText("{{incomplete")
	if result != "{{incomplete" {
		t.Errorf("expect '{{incomplete' but got '%s'", result)
	}

	result = vm.preprocessText("incomplete}}")
	if result != "incomplete}}" {
		t.Errorf("expect 'incomplete}}' but got '%s'", result)
	}
}

func TestPreprocessTextDate(t *testing.T) {
	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "sampletaskid1",
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	result := vm.preprocessText("{{date.now}}")
	if result == "{{date.now}}" {
		t.Errorf("expect date.now to be replaced with actual timestamp but got unchanged template")
	}

	result = vm.preprocessText("{{date.now_iso}}")
	if result == "{{date.now_iso}}" {
		t.Errorf("expect date.now_iso to be replaced with actual ISO timestamp but got unchanged template")
	}

	result = vm.preprocessText("current time: {{date.now}} and ISO: {{date.now_iso}}")
	if result == "current time: {{date.now}} and ISO: {{date.now_iso}}" {
		t.Errorf("expect both date templates to be replaced but got unchanged templates")
	}

	result = vm.preprocessText("{{date.unknown}}")
	if result != "{{date.unknown}}" {
		t.Errorf("expect unknown date template to remain unchanged but got '%s'", result)
	}
}
