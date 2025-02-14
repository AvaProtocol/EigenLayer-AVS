package taskengine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AvaProtocol/ap-avs/core/testutil"
	"github.com/AvaProtocol/ap-avs/model"
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
		&avsproto.TaskNode{
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
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123abc",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	n := NewRestProrcessor(vm)

	step, err := n.Execute("123abc", node)

	if err != nil {
		t.Errorf("expected rest node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected rest node run succesfully but failed")
	}

	if !strings.Contains(step.Log, "Execute POST httpbin.org at") {
		t.Errorf("expected log contains request trace data but found no")
	}

	if step.Error != "" {
		t.Errorf("expected log contains request trace data but found no")
	}
	if !strings.Contains(step.OutputData, "*This is a test format") {
		t.Errorf("expected step result contains the http endpoint response body: %s", step.OutputData)
	}
}

func TestRestRequestHandleEmptyResponse(t *testing.T) {
	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
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
		&avsproto.TaskNode{
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
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123abc",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	n := NewRestProrcessor(vm)

	step, err := n.Execute("123abc", node)

	if err != nil {
		t.Errorf("expected rest node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected rest node run succesfully but failed")
	}

	if step.OutputData != "" {
		t.Errorf("expected an empty response, got: %s", step.OutputData)
	}
}

func TestRestRequestRenderVars(t *testing.T) {
	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
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
		&avsproto.TaskNode{
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
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123abc",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		&avsproto.Task{
			Id:      "123abc",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	vm.AddVar("myNode", map[string]map[string]string{
		"data": map[string]string{
			"name": "unitest",
		},
	})

	n := NewRestProrcessor(vm)

	step, err := n.Execute("123abc", node)

	if err != nil {
		t.Errorf("expected rest node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected rest node run succesfully but failed")
	}

	if step.OutputData != "my name is unitest" {
		t.Errorf("expected response is `my name is unitest`,  got: %s", step.OutputData)
	}
}
