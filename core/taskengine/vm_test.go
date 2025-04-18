package taskengine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"os"

	"github.com/dop251/goja"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestVMCompile(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "123",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://webhook.site/15431497-2b59-4000-97ee-245fef272967",
					Method: "POST",
					Body:   "a=123",
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	vm.Compile()
	if vm.entrypoint != "123" {
		t.Errorf("Error compute entrypoint. Expected 123 Got %s", vm.entrypoint)
	}
	if len(vm.plans) < 1 {
		t.Errorf("Expect steps is populated, got nil")
	}
}

func TestRunSimpleTasks(t *testing.T) {
	// Setup test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Write(body)
	}))
	defer ts.Close()

	nodes := []*avsproto.TaskNode{
		{
			Id:   "123",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    ts.URL,
					Method: "POST",
					Body:   `{"name":"Alice"}`,
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)
	if err != nil {
		t.Errorf("expect vm initialized")
	}

	vm.Compile()

	if vm.entrypoint != "123" {
		t.Errorf("Error compute entrypoint. Expected 123 Got %s", vm.entrypoint)
	}
	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected no error Got error %v", err)
	}

	if !strings.Contains(vm.ExecutionLogs[0].Log, "Execute") {
		t.Errorf("error generating log for executing. expect a log line displaying the request attempt, got nothing")
	}

	data := vm.vars["httpnode"].(map[string]any)["data"]

	if data.(map[string]any)["name"].(string) != "Alice" {
		t.Errorf("step result isn't store properly, expect 123 got %s", data)
	}
}

func TestRunSequentialTasks(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "123",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://httpbin.org/post",
					Method: "POST",
					Body:   "post123",
				},
			},
		},
		{
			Id:   "456",
			Name: "graphql",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://httpbin.org/get?query123=abc",
					Method: "GET",
					Headers: map[string]string{
						"content-type": "application/json",
					},
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123",
		},
		{
			Id:     "e2",
			Source: "123",
			Target: "456",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	vm.Compile()

	if len(vm.plans) < 2 {
		t.Errorf("incorrect generated plan")
	}

	if vm.entrypoint != "123" {
		t.Errorf("Error compute entrypoint. Expected 123 Got %s", vm.entrypoint)
	}
	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected run ok, got error %v", err)
	}

	if len(vm.ExecutionLogs) < 2 {
		t.Errorf("Missing an execution")
	}

	if !strings.Contains(vm.ExecutionLogs[0].Log, "Execute POST httpbin.org at") || !strings.Contains(vm.ExecutionLogs[1].Log, "Execute GET httpbin.org") {
		t.Errorf("error generating log for executing. expect a log line displaying the request attempt, got nothing")
	}

	if !vm.ExecutionLogs[0].Success || !vm.ExecutionLogs[1].Success {
		t.Errorf("incorrect success status, expect all success but got failure")
	}

	if vm.ExecutionLogs[0].NodeId != "123" || vm.ExecutionLogs[1].NodeId != "456" {
		t.Errorf("incorrect node id in execution log")
	}

	outputData := gow.AnyToMap(vm.ExecutionLogs[0].GetRestApi().Data)
	if outputData["data"].(string) != "post123" {
		t.Errorf("rest node result is incorrect, should contains the string post123")
	}
	outputData = gow.AnyToMap(vm.ExecutionLogs[1].GetRestApi().Data)
	if outputData["args"].(map[string]any)["query123"].(string) != "abc" {
		t.Errorf("rest node result is incorrect, should contains the string query123")
	}
}

func TestRunTaskWithBranchNode(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Conditions: []*avsproto.Condition{
						{
							Id:         "a1",
							Type:       "if",
							Expression: "a >= 5",
						},
						{
							Id:   "a2",
							Type: "else",
						},
					},
				},
			},
		},
		{
			Id:   "notification1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://httpbin.org/post",
					Method: "POST",
					Body:   "hit=notification1",
				},
			},
		},
		{
			Id:   "notification2",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://httpbin.org/get?hit=notification2",
					Method: "GET",
					Headers: map[string]string{
						"content-type": "application/json",
					},
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "branch1",
		},
		{
			Id:     "e1",
			Source: "branch1.a1",
			Target: "notification1",
		},
		{
			Id:     "e1",
			Source: "branch1.a2",
			Target: "notification2",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	vm.vars["a"] = 10
	vm.Compile()

	if vm.entrypoint != "branch1" {
		t.Errorf("Error compute entrypoint. Expected branch1, got %s", vm.entrypoint)
		return
	}

	if len(vm.plans) != 3 {
		t.Errorf("Invalid plan generation. Expect one step, got %d", len(vm.plans))
	}

	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected success, got error %v", err)
		return
	}

	if vm.instructionCount != 2 {
		t.Errorf("incorrect steps, expect 2 got %d", vm.instructionCount)
	}
	if len(vm.ExecutionLogs) != 2 {
		t.Errorf("incorrect log, expect 2 got %d", len(vm.ExecutionLogs))
	}

	outputdata := gow.AnyToMap(vm.ExecutionLogs[1].GetRestApi().Data)
	if outputdata["data"] != `hit=notification1` {
		t.Errorf("expect executing notification1 and set output data to notification1")
	}

	vm.Reset()

	// Now test the else branch
	vm.vars["a"] = 1
	vm.Compile()
	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected success, got error %v", err)
		return
	}

	if vm.instructionCount != 2 {
		t.Errorf("incorrect steps, expect 2 got %d", vm.instructionCount)
	}
	if len(vm.ExecutionLogs) != 2 {
		t.Errorf("incorrect log, expect 2 got %d", len(vm.ExecutionLogs))
	}
	outputdata = gow.AnyToMap(vm.ExecutionLogs[1].GetRestApi().Data)
	if outputdata["args"].(map[string]any)["hit"].(string) != `notification2` {
		t.Errorf("expect executing notification2 to be run but it didn't run")
	}
}

func TestRenderString(t *testing.T) {
	vm := goja.New()
	vm.Set("myTrigger", map[string]any{
		"data": map[string]any{
			"token_symbol": "0xtoken",
			"amount":       123456,
			"tx_hash":      "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		},
	})
	vm.Set("target", "123")

	result, err := vm.RunString(`JSON.stringify({
      chat_id:-4609037622,
	  text: ` + "`Congrat, your walllet ${target} received ${myTrigger.data.amount} ${myTrigger.data.token_symbol} at [${myTrigger.data.tx_hash}](sepolia.etherscan.io/tx/${myTrigger.data.tx_hash}`" + `
	  })`)
	v := result.Export().(string)
	if err != nil || !strings.Contains(v, "123456") || !strings.Contains(v, "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8") {
		t.Errorf("text not render correctly")
	}
}

func TestEvaluateEvent(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Conditions: []*avsproto.Condition{
						{
							Id:         "a1",
							Type:       "if",
							Expression: `triggertest.data.address == "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238" && bigGt(toBigInt(triggertest.data.data), toBigInt("1200000"))`},
					},
				},
			},
		},
		{
			Id:   "notification1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://httpbin.org/post",
					Method: "POST",
					Body:   "hit=notification1",
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "branch1",
		},
		{
			Id:     "e1",
			Source: "branch1.a1",
			Target: "notification1",
		},
	}

	mark := avsproto.TriggerReason{
		BlockNumber: 7212417,
		TxHash:      "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		LogIndex:    98,
	}

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "sampletaskid1",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, &mark, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	vm.Compile()

	if vm.entrypoint != "branch1" {
		t.Errorf("Error compute entrypoint. Expected branch1, got %s", vm.entrypoint)
		return
	}

	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected success, got error %v", err)
		return
	}

	if vm.ExecutionLogs[0].GetBranch().ConditionId != "branch1.a1" {
		t.Errorf("expression evaluate incorrect")
	}
}

func TestReturnErrorWhenMissingEntrypoint(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Conditions: []*avsproto.Condition{
						{
							Id:         "a1",
							Type:       "if",
							Expression: `triggertest.data.address == "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238" && bigGt(toBigInt(triggertest.data.data), toBigInt("1200000"))`},
					},
				},
			},
		},
		{
			Id:   "notification1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://httpbin.org/post",
					Method: "POST",
					Body:   "hit=notification1",
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: "foo",
			Target: "branch1",
		},
		{
			Id:     "e1",
			Source: "branch1.a1",
			Target: "notification1",
		},
	}

	mark := avsproto.TriggerReason{
		BlockNumber: 7212417,
		TxHash:      "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		LogIndex:    98,
	}

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "sampletaskid1",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, &mark, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	err = vm.Compile()
	if err == nil || err.Error() != InvalidEntrypoint {
		t.Errorf("Expect return error due to invalid data")
	}
}

func TestParseEntrypointRegardlessOfOrdering(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch1",
			Name: "branch",
		},
		{
			Id:   "notification1",
			Name: "httpnode",
		},
		{
			Id:   "rest1",
			Name: "httpnode",
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: "rest1",
			Target: "notification1",
		},
		{
			Id:     "e1",
			Source: "notification1",
			Target: "branch1",
		},
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "notification1",
		},
	}

	mark := avsproto.TriggerReason{
		BlockNumber: 7212417,
		TxHash:      "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8",
		LogIndex:    98,
	}

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "sampletaskid1",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, &mark, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	err = vm.Compile()
	if err != nil {
		t.Errorf("Expect compile succesfully but got error: %v", err)
	}

	if vm.entrypoint != "notification1" {
		t.Errorf("expect entrypoint is notification1 but got %v", vm.entrypoint)
	}
}

func TestRunTaskWithCustomUserSecret(t *testing.T) {
	// Setup test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		response := map[string]interface{}{
			"data": string(body),
			"args": map[string]interface{}{
				"apikey": r.URL.Query().Get("apikey"),
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer ts.Close()

	nodes := []*avsproto.TaskNode{
		{
			Id:   "123",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    ts.URL + "?apikey={{apContext.configVars.apikey}}",
					Method: "POST",
					Body:   "my key is {{apContext.configVars.apikey}} in body",
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), map[string]string{
		"apikey": "secretapikey",
	})

	if err != nil {
		t.Errorf("expect vm initialized")
	}

	vm.Compile()

	if vm.entrypoint != "123" {
		t.Errorf("Error compute entrypoint. Expected 123 Got %s", vm.entrypoint)
	}
	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected no error Got error %v", err)
	}

	if !strings.Contains(vm.ExecutionLogs[0].Log, "Execute") {
		t.Errorf("error generating log for executing. expect a log line displaying the request attempt, got nothing")
	}

	data := vm.vars["httpnode"].(map[string]any)["data"].(map[string]any)
	if data["data"].(string) != "my key is secretapikey in body" {
		t.Errorf("secret doesn't render correctly in body, expect secretapikey but got %s", data["data"])
	}

	if data["args"].(map[string]interface{})["apikey"].(string) != "secretapikey" {
		t.Errorf("secret doesn't render correctly in uri, expect secretapikey but got %s", data["data"])
	}
}

func TestPreprocessText(t *testing.T) {
	// Setup a VM with some test variables
	// in a real task execution, these variables come from 3 places:
	//	1. previous node outputs
	//	2. trigger metadata
	//	3. secrets

	vm := &VM{
		vars: map[string]any{
			"user": map[string]any{
				"data": map[string]any{
					"name":    "Alice",
					"balance": 100,
					"items":   []string{"apple", "banana"},
					"address": "0x123",
					"active":  true,
				},
			},
			"token": map[string]any{
				"data": map[string]any{
					"symbol":  "ETH",
					"decimal": 18,
					"address": "0x123",
					"pairs": []map[string]any{
						{"symbol": "ETH/USD", "price": 2000},
						{"symbol": "ETH/EUR", "price": 1800},
					},
				},
			},
			"apContext": map[string]map[string]string{
				"configVars": {
					"my_awesome_secret": "my_awesome_secret_value",
				},
			},
		},
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple variable",
			input:    "Hello {{ user.data.name }}!",
			expected: "Hello Alice!",
		},
		{
			name:     "multiple variables",
			input:    "{{ user.data.name }} has {{ user.data.balance }} {{ token.data.symbol }}",
			expected: "Alice has 100 ETH",
		},
		{
			name:     "apContext variable",
			input:    "my secret is {{ apContext.configVars.my_awesome_secret}}",
			expected: "my secret is my_awesome_secret_value",
		},
		{
			name:     "invalid syntax - unclosed",
			input:    "Hello {{ user.data.name !",
			expected: "Hello {{ user.data.name !",
		},
		{
			name:     "invalid syntax - nested braces",
			input:    "Hello {{ user.data{{ .name }} }}",
			expected: "Hello  }}",
		},
		{
			name:     "multiple nested braces",
			input:    "Hello {{ user.data.name }} {{ {{ token.data.symbol }} }}",
			expected: "Hello Alice  }}",
		},
		{
			name:     "expression with calculation",
			input:    "Total: {{ user.data.balance * 2 }} {{ token.data.symbol }}",
			expected: "Total: 200 ETH",
		},
		{
			name:     "text without expressions",
			input:    "Hello World!",
			expected: "Hello World!",
		},
		{
			name:     "empty expression",
			input:    "Hello {{  }} World",
			expected: "Hello  World",
		},
		{
			name:     "multiple expressions in one line",
			input:    "{{ user.data.name }} owns {{ token.data.symbol }} at {{ token.data.address }}",
			expected: "Alice owns ETH at 0x123",
		},
		{
			name:     "javascript expression - array access",
			input:    "First item: {{ user.data.items[0] }}",
			expected: "First item: apple",
		},
		{
			name:     "javascript expression - string manipulation",
			input:    "Address: {{ user.data.address.toLowerCase() }}",
			expected: "Address: 0x123",
		},
		{
			name:     "javascript expression - conditional",
			input:    "Status: {{ user.data.active ? 'Online' : 'Offline' }}",
			expected: "Status: Online",
		},
		{
			name:     "javascript expression - template literal",
			input:    "{{ `${user.data.name}'s balance is ${user.data.balance}` }}",
			expected: "Alice's balance is 100",
		},
		{
			name:     "complex object access",
			input:    "ETH/USD Price: {{ token.data.pairs[0].price }}",
			expected: "ETH/USD Price: 2000",
		},
		{
			name:     "multiple nested properties",
			input:    "{{ token.data.pairs[0].symbol }} at {{ token.data.pairs[0].price }}",
			expected: "ETH/USD at 2000",
		},
		{
			name:     "invalid property access",
			input:    "{{ user.data.nonexistent.property }}",
			expected: "",
		},
		{
			name:     "invalid method call",
			input:    "{{ user.data.name.nonexistentMethod() }}",
			expected: "",
		},
		{
			name:     "mixed valid and invalid expressions",
			input:    "{{ user.data.name }} has {{ user.data.nonexistent }} {{ token.data.symbol }}",
			expected: "Alice has <nil> ETH",
		},
		{
			name:     "javascript expression - arithmetic",
			input:    "Total in USD: {{ token.data.pairs[0].price * user.data.balance }}",
			expected: "Total in USD: 200000",
		},
		{
			name:     "expression with spaces and newlines",
			input:    "{{ \n  user.data.name  \n }}",
			expected: "Alice",
		},
		{
			name:     "expression with comments",
			input:    "{{ /* comment */ user.data.name }}",
			expected: "Alice", // JavaScript comments in expressions are not supported
		},
		{
			name:     "max iterations test",
			input:    strings.Repeat("{{ user.data.name }}", VMMaxPreprocessIterations+1),
			expected: strings.Repeat("Alice", VMMaxPreprocessIterations) + "{{ user.data.name }}",
		},
		{
			name:     "javascript expression - date",
			input:    `{{ new Date("2014-04-07T13:58:10.104Z")}}`,
			expected: "2014-04-07 13:58:10.104 +0000 UTC",
		},
		{
			name:     "javascript object representation value",
			input:    `{{ {a: 1, b: 2} }}`,
			expected: "[object Object]",
		},
		{
			name:     "javascript object representation var",
			input:    `{{ user }}`,
			expected: "[object Object]",
		},		
	}	

	os.Setenv("TZ", "UTC")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := vm.preprocessText(tt.input)
			if result != tt.expected {
				t.Errorf("preprocessText(%q) = got %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
