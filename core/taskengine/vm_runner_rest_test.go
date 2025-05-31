package taskengine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestRestRequest(t *testing.T) {
	node := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url: "https://httpbin.org/post",
			Headers: map[string]string{
				"Content-type": "application/x-www-form-urlencoded",
			},
			Body:   "chat_id=123&disable_notification=true&text=%2AThis+is+a+test+format%2A",
			Method: "POST",
		},
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
		t.Errorf("expected rest node run successful but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected rest node run successfully but failed")
	}

	if !strings.Contains(step.Log, "Executing REST API Node ID:") {
		t.Errorf("expected log to contain request trace data but found no")
	}

	if step.Error != "" {
		t.Errorf("expected no error but got: %s", step.Error)
	}

	dataMap := gow.ValueToMap(step.GetRestApi().Data)

	// Check if body field exists
	bodyField, exists := dataMap["body"]
	if !exists {
		t.Errorf("response does not contain 'body' field, available fields: %v", dataMap)
		return
	}

	bodyMap, ok := bodyField.(map[string]any)
	if !ok {
		t.Errorf("body field is not map[string]any, got %T: %v", bodyField, bodyField)
		return
	}

	// Check if form field exists in body
	formField, exists := bodyMap["form"]
	if !exists {
		t.Errorf("response body does not contain 'form' field, available fields: %v", bodyMap)
		return
	}

	outputData, ok := formField.(map[string]any)
	if !ok {
		t.Errorf("form field is not map[string]any, got %T: %v", formField, formField)
		return
	}
	//[chat_id:123 disable_notification:true text:*This is a test format*]

	if outputData["chat_id"].(string) != "123" {
		t.Errorf("expected chat_id to be 123 but got: %s", outputData["chat_id"])
	}

	if outputData["text"].(string) != "*This is a test format*" {
		t.Errorf("expected text to be *This is a test format* but got: %s", outputData["text"])
	}

	if outputData["disable_notification"].(string) != "true" {
		t.Errorf("expected notification to be disabled but got: %s", outputData["disable_notification"])
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
		Config: &avsproto.RestAPINode_Config{
			Url: ts.URL,
			Headers: map[string]string{
				"Content-type": "application/x-www-form-urlencoded",
			},
			Body:   "",
			Method: "POST",
		},
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
		t.Errorf("expected rest node run successful but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected rest node run successfully but failed")
	}

	if gow.ValueToString(step.GetRestApi().Data) != "" {
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
		Config: &avsproto.RestAPINode_Config{
			Url: ts.URL,
			Headers: map[string]string{
				"Content-type": "application/x-www-form-urlencoded",
			},
			Body:   "my name is {{myNode.data.name}}",
			Method: "POST",
		},
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
		t.Errorf("expected rest node run successful but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected rest node run successfully but failed")
	}

	responseData := gow.ValueToMap(step.GetRestApi().Data)
	if responseData["body"].(string) != "my name is unit test" {
		t.Errorf("expected response to be 'my name is unit test', got: %s", responseData["body"])
	}
}

func TestRestRequestRenderVarsMultipleExecutions(t *testing.T) {
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

	originalUrl := ts.URL + "?name={{myNode.data.name}}"
	originalBody := "my name is {{myNode.data.name}}"
	originalHeaders := map[string]string{
		"Content-type": "application/x-www-form-urlencoded",
		"X-Name":       "{{myNode.data.name}}",
	}

	node := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url:     originalUrl,
			Headers: originalHeaders,
			Body:    originalBody,
			Method:  "POST",
		},
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
		t.Errorf("expected rest node run successful but got error: %v", err)
	}
	if !step.Success {
		t.Errorf("expected rest node run successfully but failed")
	}
	responseData := gow.ValueToMap(step.GetRestApi().Data)
	if responseData["body"].(string) != "my name is first" {
		t.Errorf("expected response to be 'my name is first', got: %s", responseData["body"])
	}

	// Second execution with different value
	vm.AddVar("myNode", map[string]map[string]string{
		"data": {
			"name": "second",
		},
	})

	step, err = n.Execute("123abc", node)

	if err != nil {
		t.Errorf("expected rest node run successful but got error: %v", err)
	}
	if !step.Success {
		t.Errorf("expected rest node run successfully but failed")
	}
	responseData2 := gow.ValueToMap(step.GetRestApi().Data)
	if responseData2["body"].(string) != "my name is second" {
		t.Errorf("expected response to be 'my name is second', got: %s", responseData2["body"])
	}

	// Verify original node values remain unchanged
	if node.Config.Url != originalUrl {
		t.Errorf("expected URL to be %s, got %s", originalUrl, node.Config.Url)
	}
	if node.Config.Body != originalBody {
		t.Errorf("expected Body to be %s, got %s", originalBody, node.Config.Body)
	}
	if !reflect.DeepEqual(node.Config.Headers, originalHeaders) {
		t.Errorf("expected Headers to be %v, got %v", originalHeaders, node.Config.Headers)
	}
}

func TestRestRequestErrorHandling(t *testing.T) {
	node := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url:    "http://non-existent-domain-that-will-fail.invalid",
			Method: "GET",
		},
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
		t.Errorf("expected error for non-existent domain, but got nil")
	}

	if !strings.Contains(err.Error(), "HTTP request failed: connection error or timeout") {
		t.Errorf("expected error message to contain connection failure information, got: %v", err)
	}

	if step.Success {
		t.Errorf("expected step.Success to be false for failed request")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // 404
	}))
	defer ts.Close()

	node404 := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url:    ts.URL,
			Method: "GET",
		},
	}

	step, err = n.Execute("error-test", node404)

	if err == nil {
		t.Errorf("expected error for 404 status code, but got nil")
	}

	if !strings.Contains(err.Error(), "unexpected HTTP status code: 404") {
		t.Errorf("expected error message to contain status code 404, got: %v", err)
	}

	if step.Success {
		t.Errorf("expected step.Success to be false for 404 response")
	}

	// Test 500 Server Error
	ts500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // 500
	}))
	defer ts500.Close()

	node500 := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url:    ts500.URL,
			Method: "GET",
		},
	}

	step, err = n.Execute("error-test", node500)

	if err == nil {
		t.Errorf("expected error for 500 status code, but got nil")
	}

	if !strings.Contains(err.Error(), "unexpected HTTP status code: 500") {
		t.Errorf("expected error message to contain status code 500, got: %v", err)
	}

	if step.Success {
		t.Errorf("expected step.Success to be false for 500 response")
	}
}

func TestRestRequestTelegramMockServer(t *testing.T) {
	// Create mock Telegram server with the exact response format
	telegramServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it's a Telegram bot API call
		if !strings.Contains(r.URL.Path, "/bot") || !strings.Contains(r.URL.Path, "/sendMessage") {
			t.Errorf("expected Telegram bot API path, got: %s", r.URL.Path)
		}

		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}

		// Return the exact Telegram response format
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		telegramResponse := map[string]interface{}{
			"ok": true,
			"result": map[string]interface{}{
				"message_id": 492,
				"from": map[string]interface{}{
					"id":         7771086042,
					"is_bot":     true,
					"first_name": "AvaProtocolBotDev",
					"username":   "AvaProtocolDevBot",
				},
				"chat": map[string]interface{}{
					"id":         452247333,
					"first_name": "Chris | Ava Protocol",
					"username":   "kezjo",
					"type":       "private",
				},
				"date": 1748665041,
				"text": "Hello from script",
			},
		}

		if err := json.NewEncoder(w).Encode(telegramResponse); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer telegramServer.Close()

	// Create REST API node that calls our mock Telegram server
	node := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url: telegramServer.URL + "/bot123456:ABC-DEF1234/sendMessage",
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body:   `{"chat_id": 452247333, "text": "Hello from script"}`,
			Method: "POST",
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "telegram-test",
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
			Target: "telegram-test",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "telegram-test",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("failed to create VM: %v", err)
	}

	processor := NewRestProrcessor(vm)
	step, err := processor.Execute("telegram-test", node)

	if err != nil {
		t.Fatalf("expected successful execution but got error: %v", err)
	}

	if !step.Success {
		t.Fatalf("expected step.Success to be true but got false, error: %s", step.Error)
	}

	// Test the response data structure - this is the key test for the fix
	responseData := gow.ValueToMap(step.GetRestApi().Data)

	// Verify no typeUrl/value wrapping (the old broken structure)
	if _, hasTypeUrl := responseData["typeUrl"]; hasTypeUrl {
		t.Errorf("❌ Response still has typeUrl field - double-wrapping issue not fixed!")
	}
	if _, hasValue := responseData["value"]; hasValue {
		t.Errorf("❌ Response still has value field - double-wrapping issue not fixed!")
	}

	// Verify we have proper response structure
	if responseData == nil {
		t.Fatalf("❌ Response data is nil")
	}

	// Check that we can access the body directly
	bodyField, exists := responseData["body"]
	if !exists {
		t.Fatalf("❌ Response missing 'body' field. Available fields: %v", getStringMapKeys(responseData))
	}

	bodyMap, ok := bodyField.(map[string]interface{})
	if !ok {
		t.Fatalf("❌ Body is not map[string]interface{}, got %T: %v", bodyField, bodyField)
	}

	// Test Telegram response structure access
	// Should be able to access bodyMap["ok"] directly
	okField, exists := bodyMap["ok"]
	if !exists {
		t.Fatalf("❌ Telegram response missing 'ok' field. Available fields: %v", getStringMapKeys(bodyMap))
	}
	if ok, isOk := okField.(bool); !isOk || !ok {
		t.Errorf("❌ Expected ok=true, got: %v (type: %T)", okField, okField)
	}

	// Test nested result access
	resultField, exists := bodyMap["result"]
	if !exists {
		t.Fatalf("❌ Telegram response missing 'result' field")
	}

	resultMap, ok := resultField.(map[string]interface{})
	if !ok {
		t.Fatalf("❌ Result is not map[string]interface{}, got %T", resultField)
	}

	// Test message_id access
	messageId, exists := resultMap["message_id"]
	if !exists {
		t.Fatalf("❌ Result missing 'message_id' field")
	}
	if msgId, ok := messageId.(float64); !ok || msgId != 492 {
		t.Errorf("❌ Expected message_id=492, got: %v (type: %T)", messageId, messageId)
	}

	// Test nested from object access
	fromField, exists := resultMap["from"]
	if !exists {
		t.Fatalf("❌ Result missing 'from' field")
	}

	fromMap, ok := fromField.(map[string]interface{})
	if !ok {
		t.Fatalf("❌ From is not map[string]interface{}, got %T", fromField)
	}

	// Test from.id access
	fromId, exists := fromMap["id"]
	if !exists {
		t.Fatalf("❌ From missing 'id' field")
	}
	if id, ok := fromId.(float64); !ok || id != 7771086042 {
		t.Errorf("❌ Expected from.id=7771086042, got: %v (type: %T)", fromId, fromId)
	}

	// Test from.first_name access
	firstName, exists := fromMap["first_name"]
	if !exists {
		t.Fatalf("❌ From missing 'first_name' field")
	}
	if name, ok := firstName.(string); !ok || name != "AvaProtocolBotDev" {
		t.Errorf("❌ Expected from.first_name='AvaProtocolBotDev', got: %v", firstName)
	}

	// Test nested chat object access
	chatField, exists := resultMap["chat"]
	if !exists {
		t.Fatalf("❌ Result missing 'chat' field")
	}

	chatMap, ok := chatField.(map[string]interface{})
	if !ok {
		t.Fatalf("❌ Chat is not map[string]interface{}, got %T", chatField)
	}

	// Test chat.id access
	chatId, exists := chatMap["id"]
	if !exists {
		t.Fatalf("❌ Chat missing 'id' field")
	}
	if id, ok := chatId.(float64); !ok || id != 452247333 {
		t.Errorf("❌ Expected chat.id=452247333, got: %v (type: %T)", chatId, chatId)
	}

	// Test text field access
	textField, exists := resultMap["text"]
	if !exists {
		t.Fatalf("❌ Result missing 'text' field")
	}
	if text, ok := textField.(string); !ok || text != "Hello from script" {
		t.Errorf("❌ Expected text='Hello from script', got: %v", textField)
	}

	// Test date field access
	dateField, exists := resultMap["date"]
	if !exists {
		t.Fatalf("❌ Result missing 'date' field")
	}
	if date, ok := dateField.(float64); !ok || date != 1748665041 {
		t.Errorf("❌ Expected date=1748665041, got: %v (type: %T)", dateField, dateField)
	}

	t.Logf("✅ SUCCESS: Telegram mock server test passed!")
	t.Logf("✅ Response structure properly accessible without typeUrl/value wrapping")
	t.Logf("✅ Can access nested fields: ok=%v, message_id=%v, from.id=%v, chat.id=%v, text='%v'",
		okField, messageId, fromId, chatId, textField)
}
