package taskengine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/AvaProtocol/ap-avs/core/testutil"
	"github.com/AvaProtocol/ap-avs/model"
	"github.com/AvaProtocol/ap-avs/pkg/gow"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

func TestRestRequest(t *testing.T) {
	node := &avsproto.RestAPINode{
		Url: "https://httpbin.org/post",
		Headers: map[string]string{
			"Content-type": "application/x-www-form-urlencoded",
		},
		Body:   "chat_id=123&disable_notification=true&text=%2AThis+is+a+test+format%2A",
		Method: "POST",
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "123abc",
			Name: "restApi",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: node,
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
			Target: "123abc",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	n := NewRestProrcessor(vm)

	step, err := n.Execute("123abc", node)

	if err != nil {
		t.Errorf("Expected rest node run successful but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("Expected rest node run successfully but failed")
	}

	if !strings.Contains(step.Log, "Execute POST httpbin.org at") {
		t.Errorf("Expected log to contain request trace data but found no")
	}

	if step.Error != "" {
		t.Errorf("Expected log to contain request trace data but found no")
	}

	outputData := gow.AnyToMap(step.GetRestApi().Data)["form"].(map[string]any)
	//[chat_id:123 disable_notification:true text:*This is a test format*]

	if outputData["chat_id"].(string) != "123" {
		t.Errorf("Expected chat_id to be 123 but got: %s", outputData["chat_id"])
	}

	if outputData["text"].(string) != "*This is a test format*" {
		t.Errorf("Expected text to be *This is a test format* but got: %s", outputData["text"])
	}

	if outputData["disable_notification"].(string) != "true" {
		t.Errorf("Expected notification to be disabled but got: %s", outputData["disable_notification"])
	}
}

func TestRestRequestHandleEmptyResponse(t *testing.T) {
	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(""))
	}))
	defer ts.Close()

	node := &avsproto.RestAPINode{
		Url: ts.URL,
		Headers: map[string]string{
			"Content-type": "application/x-www-form-urlencoded",
		},
		Body:   "",
		Method: "POST",
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "123abc",
			Name: "restApi",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: node,
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
			Target: "123abc",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	n := NewRestProrcessor(vm)

	step, err := n.Execute("123abc", node)

	if err != nil {
		t.Errorf("Expected rest node run successful but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("Expected rest node run successfully but failed")
	}

	if gow.AnyToString(step.GetRestApi().Data) != "" {
		t.Errorf("Expected an empty response, got: %s", step.OutputData)
	}
}

func TestRestRequestRenderVars(t *testing.T) {
	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		w.Write([]byte(body))
	}))
	defer ts.Close()

	node := &avsproto.RestAPINode{
		Url: ts.URL,
		Headers: map[string]string{
			"Content-type": "application/x-www-form-urlencoded",
		},
		Body:   "my name is {{myNode.data.name}}",
		Method: "POST",
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "123abc",
			Name: "restApi",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: node,
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
			Target: "123abc",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	vm.AddVar("myNode", map[string]map[string]string{
		"data": map[string]string{
			"name": "unit test",
		},
	})

	n := NewRestProrcessor(vm)

	step, err := n.Execute("123abc", node)

	if err != nil {
		t.Errorf("Expected rest node run successful but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("Expected rest node run successfully but failed")
	}

	if gow.AnyToString(step.GetRestApi().Data) != "my name is unit test" {
		t.Errorf("Expected response to be 'my name is unit test', got: %s", step.OutputData)
	}
}

func TestRestRequestRenderVarsMultipleExecutions(t *testing.T) {
	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		w.Write([]byte(body))
	}))
	defer ts.Close()

	originalUrl := ts.URL + "?name={{myNode.data.name}}"
	originalBody := "my name is {{myNode.data.name}}"
	originalHeaders := map[string]string{
		"Content-type": "application/x-www-form-urlencoded",
		"X-Name":       "{{myNode.data.name}}",
	}

	node := &avsproto.RestAPINode{
		Url:     originalUrl,
		Headers: originalHeaders,
		Body:    originalBody,
		Method:  "POST",
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "123abc",
			Name: "restApi",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: node,
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
			Target: "123abc",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	// First execution with first value
	vm.AddVar("myNode", map[string]map[string]string{
		"data": {
			"name": "first",
		},
	})

	n := NewRestProrcessor(vm)
	step, err := n.Execute("123abc", node)

	if err != nil {
		t.Errorf("Expected rest node run successful but got error: %v", err)
	}
	if !step.Success {
		t.Errorf("Expected rest node run successfully but failed")
	}
	if gow.AnyToString(step.GetRestApi().Data) != "my name is first" {
		t.Errorf("Expected response to be 'my name is first', got: %s", step.OutputData)
	}

	// Second execution with different value
	vm.AddVar("myNode", map[string]map[string]string{
		"data": {
			"name": "second",
		},
	})

	step, err = n.Execute("123abc", node)

	if err != nil {
		t.Errorf("Expected rest node run successful but got error: %v", err)
	}
	if !step.Success {
		t.Errorf("Expected rest node run successfully but failed")
	}
	if gow.AnyToString(step.GetRestApi().Data) != "my name is second" {
		t.Errorf("Expected response to be 'my name is second', got: %s", step.OutputData)
	}

	// Verify original node values remain unchanged
	if node.Url != originalUrl {
		t.Errorf("Expected URL to be %s, got %s", originalUrl, node.Url)
	}
	if node.Body != originalBody {
		t.Errorf("Expected Body to be %s, got %s", originalBody, node.Body)
	}
	if !reflect.DeepEqual(node.Headers, originalHeaders) {
		t.Errorf("Expected Headers to be %v, got %v", originalHeaders, node.Headers)
	}
}

func TestRestRequestErrorHandling(t *testing.T) {
	node := &avsproto.RestAPINode{
		Url:    "http://non-existent-domain-that-will-fail.invalid",
		Method: "GET",
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "error-test",
			Name: "restApi",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: node,
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
			Target: "error-test",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "error-test",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	n := NewRestProrcessor(vm)

	step, err := n.Execute("error-test", node)

	if err == nil {
		t.Errorf("Expected error for non-existent domain, but got nil")
	}

	if !strings.Contains(err.Error(), "HTTP request failed: connection error or timeout") {
		t.Errorf("Expected error message to contain connection failure information, got: %v", err)
	}

	if step.Success {
		t.Errorf("Expected step.Success to be false for failed request")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // 404
	}))
	defer ts.Close()

	node404 := &avsproto.RestAPINode{
		Url:    ts.URL,
		Method: "GET",
	}

	step, err = n.Execute("error-test", node404)

	if err == nil {
		t.Errorf("Expected error for 404 status code, but got nil")
	}

	if !strings.Contains(err.Error(), "unexpected HTTP status code: 404") {
		t.Errorf("Expected error message to contain status code 404, got: %v", err)
	}

	if step.Success {
		t.Errorf("Expected step.Success to be false for 404 response")
	}

	// Test 500 Server Error
	ts500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // 500
	}))
	defer ts500.Close()

	node500 := &avsproto.RestAPINode{
		Url:    ts500.URL,
		Method: "GET",
	}

	step, err = n.Execute("error-test", node500)

	if err == nil {
		t.Errorf("Expected error for 500 status code, but got nil")
	}

	if !strings.Contains(err.Error(), "unexpected HTTP status code: 500") {
		t.Errorf("Expected error message to contain status code 500, got: %v", err)
	}

	if step.Success {
		t.Errorf("Expected step.Success to be false for 500 response")
	}
}
