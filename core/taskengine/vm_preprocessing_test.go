package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestRenderString(t *testing.T) {
	vm, err := NewVMWithData(&model.Workflow{
		Task: &avsproto.Task{
			Id: "sampletaskid1",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "test_trigger",
			},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("expect vm initialized, got error: %v", err)
	}

	if vm == nil {
		t.Fatal("vm is nil")
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
	vm, err := NewVMWithData(&model.Workflow{
		Task: &avsproto.Task{
			Id: "sampletaskid1",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "test_trigger",
			},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("expect vm initialized, got error: %v", err)
	}

	if vm == nil {
		t.Fatal("vm is nil")
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
					"name":  "item1",
					"value": 100,
				},
				map[string]interface{}{
					"name":  "item2",
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

// TestPreprocessText_CaseAliasing is the back-compat guarantee that the
// REST migration carries: a workflow stored in the gRPC era references
// `{{settings.chain_id}}`, but a v4 SDK client now writes
// `inputVariables.settings = { chainId: ... }`. The VM resolver
// expandCaseAliases bridges the gap so neither side has to rewrite the
// other. See vm_template_compat.go for the helper.
func TestPreprocessText_CaseAliasing(t *testing.T) {
	vm, err := NewVMWithData(&model.Workflow{
		Task: &avsproto.Task{
			Id:      "sampletaskid1",
			Trigger: &avsproto.TaskTrigger{Id: "trigger1", Name: "test_trigger"},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)
	if err != nil {
		t.Fatalf("vm: %v", err)
	}

	// SDK v4 wrote the input variables in camelCase.
	vm.vars["settings"] = map[string]interface{}{
		"chainId":            int64(11155111),
		"smartWalletAddress": "0xabc",
	}

	// Stored workflow still references the original snake_case names.
	cases := map[string]string{
		"chainId snake form": "chain={{settings.chain_id}}",
		"camel form":         "chain={{settings.chainId}}",
		"snake wallet":       "wallet={{settings.smart_wallet_address}}",
		"camel wallet":       "wallet={{settings.smartWalletAddress}}",
	}
	expected := map[string]string{
		"chainId snake form": "chain=11155111",
		"camel form":         "chain=11155111",
		"snake wallet":       "wallet=0xabc",
		"camel wallet":       "wallet=0xabc",
	}
	for name, tmpl := range cases {
		got := vm.preprocessText(tmpl)
		if got != expected[name] {
			t.Errorf("%s: expected %q, got %q", name, expected[name], got)
		}
		got2 := vm.preprocessTextWithVariableMapping(tmpl)
		if got2 != expected[name] {
			t.Errorf("%s (variable-mapping path): expected %q, got %q", name, expected[name], got2)
		}
	}
}

func TestPreprocessTextDate(t *testing.T) {
	vm, err := NewVMWithData(&model.Workflow{
		Task: &avsproto.Task{
			Id: "sampletaskid1",
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "test_trigger",
			},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("expect vm initialized, got error: %v", err)
	}

	if vm == nil {
		t.Fatal("vm is nil")
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
