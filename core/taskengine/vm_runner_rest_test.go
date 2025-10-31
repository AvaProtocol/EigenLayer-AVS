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
	"google.golang.org/protobuf/types/known/structpb"
)

func TestRestRequest(t *testing.T) {
	// Create mock httpbin server that mimics httpbin.org/post response format
	httpbinServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}

		// Read the form data from the request body
		if err := r.ParseForm(); err != nil {
			t.Errorf("failed to parse form data: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Create response structure that matches httpbin.org/post
		httpbinResponse := map[string]interface{}{
			"args":  map[string]interface{}{},
			"data":  "",
			"files": map[string]interface{}{},
			"form": map[string]interface{}{
				"chat_id":              r.Form.Get("chat_id"),
				"disable_notification": r.Form.Get("disable_notification"),
				"text":                 r.Form.Get("text"),
			},
			"headers": map[string]interface{}{
				"Accept":         "*/*",
				"Content-Length": r.Header.Get("Content-Length"),
				"Content-Type":   r.Header.Get("Content-Type"),
				"Host":           r.Host,
				"User-Agent":     r.Header.Get("User-Agent"),
			},
			"json":   nil,
			"origin": r.RemoteAddr,
			"url":    r.URL.String(),
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(httpbinResponse); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer httpbinServer.Close()

	node := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url: httpbinServer.URL, // Use mock server instead of https://httpbin.org/post
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

	n := NewRestProcessor(vm)

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

	// Check if data field exists (standard format)
	responseData, exists := dataMap["data"]
	if !exists {
		t.Errorf("response does not contain 'data' field, available fields: %v", dataMap)
		return
	}

	responseMap, ok := responseData.(map[string]any)
	if !ok {
		t.Errorf("data field is not map[string]any, got %T: %v", responseData, responseData)
		return
	}

	// Check if form field exists in response data
	formField, exists := responseMap["form"]
	if !exists {
		t.Errorf("response data does not contain 'form' field, available fields: %v", responseMap)
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

	n := NewRestProcessor(vm)

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

	n := NewRestProcessor(vm)

	step, err := n.Execute("123abc", node)

	if err != nil {
		t.Errorf("expected rest node run successful but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected rest node run successfully but failed")
	}

	responseData := gow.ValueToMap(step.GetRestApi().Data)
	if responseData["data"].(string) != "my name is unit test" {
		t.Errorf("expected response to be 'my name is unit test', got: %s", responseData["data"])
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

	n := NewRestProcessor(vm)
	step, err := n.Execute("123abc", node)

	if err != nil {
		t.Errorf("expected rest node run successful but got error: %v", err)
	}
	if !step.Success {
		t.Errorf("expected rest node run successfully but failed")
	}
	responseData := gow.ValueToMap(step.GetRestApi().Data)
	if responseData["data"].(string) != "my name is first" {
		t.Errorf("expected response to be 'my name is first', got: %s", responseData["data"])
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
	if responseData2["data"].(string) != "my name is second" {
		t.Errorf("expected response to be 'my name is second', got: %s", responseData2["data"])
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

	n := NewRestProcessor(vm)

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

	if err != nil {
		t.Errorf("HTTP 404 should not cause execution error, but got: %v", err)
	}

	if step.Success {
		t.Errorf("expected step.Success to be false for 404 response (HTTP errors are failures)")
	}

	// Verify the response data contains the 404 status
	responseData := gow.ValueToMap(step.GetRestApi().Data)
	if status, ok := responseData["status"].(float64); !ok || status != 404 {
		t.Errorf("expected status 404 in response data, got: %v", responseData["status"])
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

	if err != nil {
		t.Errorf("HTTP 500 should not cause execution error, but got: %v", err)
	}

	if step.Success {
		t.Errorf("expected step.Success to be false for 500 response (HTTP errors are failures)")
	}

	// Verify the response data contains the 500 status
	responseData = gow.ValueToMap(step.GetRestApi().Data)
	if status, ok := responseData["status"].(float64); !ok || status != 500 {
		t.Errorf("expected status 500 in response data, got: %v", responseData["status"])
	}
}

func TestRestRequestTelegramMockServer(t *testing.T) {
	// Test values stored in variables for both mock response and verification
	expectedMessageId := 123
	expectedFromFirstName := "TestBot"
	expectedChatUsername := "testuser"

	// Create mock Telegram server with test response format
	telegramServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it's a Telegram bot API call
		if !strings.Contains(r.URL.Path, "/bot") || !strings.Contains(r.URL.Path, "/sendMessage") {
			t.Errorf("expected Telegram bot API path, got: %s", r.URL.Path)
		}

		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}

		// Return test response using the test variables
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		telegramResponse := map[string]interface{}{
			"ok": true,
			"result": map[string]interface{}{
				"message_id": expectedMessageId,
				"from": map[string]interface{}{
					"id":         12345,
					"is_bot":     true,
					"first_name": expectedFromFirstName,
					"username":   "test_bot",
				},
				"chat": map[string]interface{}{
					"id":         67890,
					"first_name": "Test User",
					"username":   expectedChatUsername,
					"type":       "private",
				},
				"date": 1640995200,
				"text": "Test message",
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
			Url: telegramServer.URL + "/bot123456:TEST-TOKEN/sendMessage",
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body:   `{"chat_id": 67890, "text": "Test message"}`,
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

	processor := NewRestProcessor(vm)
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

	// Check that we can access the data directly (standard format)
	dataField, exists := responseData["data"]
	if !exists {
		t.Fatalf("❌ Response missing 'data' field. Available fields: %v", getStringMapKeys(responseData))
	}

	dataMap, ok := dataField.(map[string]interface{})
	if !ok {
		t.Fatalf("❌ Data is not map[string]interface{}, got %T: %v", dataField, dataField)
	}

	// Test basic response structure
	okField, exists := dataMap["ok"]
	if !exists {
		t.Fatalf("❌ Telegram response missing 'ok' field")
	}
	if ok, isOk := okField.(bool); !isOk || !ok {
		t.Errorf("❌ Expected ok=true, got: %v (type: %T)", okField, okField)
	}

	// Get result object
	resultField, exists := dataMap["result"]
	if !exists {
		t.Fatalf("❌ Telegram response missing 'result' field")
	}

	resultMap, ok := resultField.(map[string]interface{})
	if !ok {
		t.Fatalf("❌ Result is not map[string]interface{}, got %T", resultField)
	}

	// Test 1: message_id
	messageId, exists := resultMap["message_id"]
	if !exists {
		t.Fatalf("❌ Result missing 'message_id' field")
	}
	if msgId, ok := messageId.(float64); !ok || int(msgId) != expectedMessageId {
		t.Errorf("❌ Expected message_id=%d, got: %v (type: %T)", expectedMessageId, messageId, messageId)
	}

	// Test 2: from.first_name
	fromField, exists := resultMap["from"]
	if !exists {
		t.Fatalf("❌ Result missing 'from' field")
	}

	fromMap, ok := fromField.(map[string]interface{})
	if !ok {
		t.Fatalf("❌ From is not map[string]interface{}, got %T", fromField)
	}

	firstName, exists := fromMap["first_name"]
	if !exists {
		t.Fatalf("❌ From missing 'first_name' field")
	}
	if name, ok := firstName.(string); !ok || name != expectedFromFirstName {
		t.Errorf("❌ Expected from.first_name='%s', got: %v", expectedFromFirstName, firstName)
	}

	// Test 3: chat.username
	chatField, exists := resultMap["chat"]
	if !exists {
		t.Fatalf("❌ Result missing 'chat' field")
	}

	chatMap, ok := chatField.(map[string]interface{})
	if !ok {
		t.Fatalf("❌ Chat is not map[string]interface{}, got %T", chatField)
	}

	username, exists := chatMap["username"]
	if !exists {
		t.Fatalf("❌ Chat missing 'username' field")
	}
	if user, ok := username.(string); !ok || user != expectedChatUsername {
		t.Errorf("❌ Expected chat.username='%s', got: %v", expectedChatUsername, username)
	}

	t.Logf("✅ All Telegram response fields verified successfully")
	t.Logf("✅ message_id: %v", messageId)
	t.Logf("✅ from.first_name: %v", firstName)
	t.Logf("✅ chat.username: %v", username)
}

func TestRestRequestSendGridGlobalSecret(t *testing.T) {
	// Test values to validate in the SendGrid mock response
	expectedAccountType := "paid"
	expectedReputation := 99.8

	// Create mock SendGrid server that mimics SendGrid API response format
	sendGridServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it's a SendGrid API call to the user account endpoint
		if !strings.Contains(r.URL.Path, "/v3/user/account") {
			t.Errorf("expected SendGrid user account API path, got: %s", r.URL.Path)
		}

		if r.Method != "GET" {
			t.Errorf("expected GET request, got %s", r.Method)
		}

		// Verify that the Authorization header is present and uses the sendgrid_key
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			t.Errorf("expected Authorization header with Bearer token, got: %s", authHeader)
		}

		// Check that the API key from the template was properly substituted
		expectedKey := "SENDGRID_KEY_FOR_UNIT_TESTS"
		if !strings.Contains(authHeader, expectedKey) {
			t.Errorf("expected sendgrid_key to be substituted in Authorization header, got: %s", authHeader)
		}

		// Return mock SendGrid account information response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		sendGridResponse := map[string]interface{}{
			"type":       expectedAccountType,
			"reputation": expectedReputation,
		}

		if err := json.NewEncoder(w).Encode(sendGridResponse); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	defer sendGridServer.Close()

	// Create REST API node that calls our mock SendGrid server using the global sendgrid_key secret
	node := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url: sendGridServer.URL + "/v3/user/account",
			Headers: map[string]string{
				"Authorization": "Bearer {{apContext.configVars.sendgrid_key}}",
				"Content-Type":  "application/json",
			},
			Body:   "",
			Method: "GET",
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "sendgrid-test",
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
			Target: "sendgrid-test",
		},
	}

	// Create VM with SendGrid secret available through global macro secrets
	globalSecrets := map[string]string{
		"sendgrid_key": "SENDGRID_KEY_FOR_UNIT_TESTS",
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "sendgrid-test",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), globalSecrets)

	if err != nil {
		t.Fatalf("failed to create VM: %v", err)
	}

	processor := NewRestProcessor(vm)
	step, err := processor.Execute("sendgrid-test", node)

	if err != nil {
		t.Fatalf("expected successful execution but got error: %v", err)
	}

	if !step.Success {
		t.Fatalf("expected step.Success to be true but got false, error: %s", step.Error)
	}

	// Test the response data structure - verify SendGrid API response is accessible
	responseData := gow.ValueToMap(step.GetRestApi().Data)

	// Verify no typeUrl/value wrapping (ensuring proper response structure)
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

	// Check status code
	status, exists := responseData["status"]
	if !exists {
		t.Fatalf("❌ Response missing 'status' field. Available fields: %v", getStringMapKeys(responseData))
	}
	if sc, ok := status.(float64); !ok || sc != 200 {
		t.Errorf("❌ Expected status 200, got: %v (type: %T)", status, status)
	}

	// Check that we can access the data directly (standard format)
	dataField, exists := responseData["data"]
	if !exists {
		t.Fatalf("❌ Response missing 'data' field. Available fields: %v", getStringMapKeys(responseData))
	}

	dataMap, ok := dataField.(map[string]interface{})
	if !ok {
		t.Fatalf("❌ Data is not map[string]interface{}, got %T: %v", dataField, dataField)
	}

	// Test 1: account type
	accountType, exists := dataMap["type"]
	if !exists {
		t.Fatalf("❌ SendGrid response missing 'type' field")
	}
	if accType, ok := accountType.(string); !ok || accType != expectedAccountType {
		t.Errorf("❌ Expected account type='%s', got: %v (type: %T)", expectedAccountType, accountType, accountType)
	}

	// Test 2: reputation
	reputation, exists := dataMap["reputation"]
	if !exists {
		t.Fatalf("❌ SendGrid response missing 'reputation' field")
	}
	if rep, ok := reputation.(float64); !ok || rep != expectedReputation {
		t.Errorf("❌ Expected reputation=%f, got: %v (type: %T)", expectedReputation, reputation, reputation)
	}

	t.Logf("✅ SUCCESS: SendGrid global secret test passed!")
	t.Logf("✅ sendgrid_key global secret was properly substituted in Authorization header")
	t.Logf("✅ SendGrid API response structure properly accessible: type='%s', reputation=%f",
		expectedAccountType, expectedReputation)
	t.Logf("✅ Verified global secret access via {{apContext.configVars.sendgrid_key}} template")
}

func TestRestSummarizeTelegramHTML(t *testing.T) {
	// Expect composed subject/body
	expectedWorkflowName := "Using sdk test wallet"

	// Mock Telegram endpoint that inspects incoming request
	telegramServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST request, got %s", r.Method)
		}
		if !strings.Contains(strings.ToLower(r.URL.Path), "/sendmessage") {
			t.Errorf("expected /sendMessage path, got: %s", r.URL.Path)
		}
		// Read JSON body
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode telegram request body: %v", err)
		}
		// Verify parse_mode and text
		if pm, _ := req["parse_mode"].(string); pm != "HTML" {
			t.Errorf("expected parse_mode=HTML, got: %v", pm)
		}
		text, _ := req["text"].(string)
		t.Logf("telegram composed text: %q", text)
		// Verify that summarization happened - check for HTML formatting and key workflow info
		if !strings.Contains(text, "<b>") || !strings.Contains(text, "</b>") {
			t.Errorf("telegram text missing HTML bold tags, summarization may not have occurred. got: %q", text)
		}
		if !strings.Contains(text, expectedWorkflowName) {
			t.Errorf("telegram text missing workflow name %q. got: %q", expectedWorkflowName, text)
		}
		if !strings.Contains(strings.ToLower(text), "succeed") {
			t.Errorf("telegram text missing success indicator. got: %q", text)
		}
		if text == "placeholder" {
			t.Errorf("telegram text was not replaced by summary, still shows placeholder")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "result": map[string]interface{}{"message_id": 1}})
	}))
	defer telegramServer.Close()

	// Node with summarize=true via Config.Options
	optsVal, _ := structpb.NewValue(map[string]interface{}{"summarize": true})
	node := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url:     telegramServer.URL + "/botX/sendMessage",
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    `{"chat_id": 1, "text": "placeholder"}`,
			Method:  "POST",
			Options: optsVal,
		},
	}

	nodes := []*avsproto.TaskNode{{
		Id:       "tg-sum",
		Name:     "restApi",
		TaskType: &avsproto.TaskNode_RestApi{RestApi: node},
	}}
	trigger := &avsproto.TaskTrigger{Id: "trig", Name: "trig"}
	edges := []*avsproto.TaskEdge{{Id: "e1", Source: trigger.Id, Target: "tg-sum"}}

	vm, err := NewVMWithData(&model.Task{Task: &avsproto.Task{Id: "tg-sum", Nodes: nodes, Edges: edges, Trigger: trigger}}, nil, testutil.GetTestSmartWalletConfig(), nil)
	if err != nil {
		t.Fatalf("failed to create VM: %v", err)
	}

	// Provide settings.name and a last successful step
	vm.AddVar("settings", map[string]interface{}{"name": expectedWorkflowName})
	vm.ExecutionLogs = append(vm.ExecutionLogs, &avsproto.Execution_Step{Name: "run_swap", Success: true})

	processor := NewRestProcessor(vm)
	step, err := processor.Execute("tg-sum", node)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !step.Success {
		t.Fatalf("expected step success, got failure: %s", step.Error)
	}
}

func TestRestSummarizeSendGridInjection(t *testing.T) {
	expectedWorkflowName := "Using sdk test wallet"

	// Mock SendGrid endpoint; verify subject and content[0].value
	sendgridServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/v3/mail/send") && !strings.Contains(r.URL.Path, "/mail/send") {
			t.Errorf("expected /v3/mail/send path, got: %s", r.URL.Path)
		}
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode sendgrid request: %v", err)
		}
		subj, _ := req["subject"].(string)
		// content should be array with first element value == expectedBody
		content, _ := req["content"].([]interface{})
		var body string
		if len(content) > 0 {
			if first, ok := content[0].(map[string]interface{}); ok {
				body, _ = first["value"].(string)
			}
		}
		t.Logf("sendgrid composed subject: %q body: %q", subj, body)
		// Verify subject contains workflow name and success indicator (could be AI-generated)
		if !strings.Contains(subj, expectedWorkflowName) {
			t.Errorf("subject missing workflow name. want to contain %q got %q", expectedWorkflowName, subj)
		}
		if !strings.Contains(strings.ToLower(subj), "succeed") {
			t.Errorf("subject missing success indicator. got %q", subj)
		}
		if len(content) == 0 {
			t.Errorf("expected content array to be non-empty")
		} else if first, ok := content[0].(map[string]interface{}); ok {
			if val, _ := first["value"].(string); val != "" {
				// Verify body was generated and replaced placeholder (could be AI-generated)
				if val == "placeholder" {
					t.Errorf("content[0].value was not replaced by summary, still shows placeholder")
				}
				if !strings.Contains(val, expectedWorkflowName) {
					t.Errorf("content[0].value missing workflow name. want to contain %q got %q", expectedWorkflowName, val)
				}
			} else {
				t.Errorf("content[0].value is empty")
			}
		} else {
			t.Errorf("content[0] not an object")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"message": "accepted"})
	}))
	defer sendgridServer.Close()

	optsVal, _ := structpb.NewValue(map[string]interface{}{"summarize": true})
	node := &avsproto.RestAPINode{Config: &avsproto.RestAPINode_Config{
		Url:     sendgridServer.URL + "/v3/mail/send",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    `{"personalizations":[{"to":[{"email":"user@example.com"}]}],"from":{"email":"noreply@example.com"},"subject":"placeholder","content":[{"type":"text/plain","value":"placeholder"}]}`,
		Method:  "POST",
		Options: optsVal,
	}}

	nodes := []*avsproto.TaskNode{{Id: "sg-sum", Name: "restApi", TaskType: &avsproto.TaskNode_RestApi{RestApi: node}}}
	trigger := &avsproto.TaskTrigger{Id: "trig", Name: "trig"}
	edges := []*avsproto.TaskEdge{{Id: "e1", Source: trigger.Id, Target: "sg-sum"}}
	vm, err := NewVMWithData(&model.Task{Task: &avsproto.Task{Id: "sg-sum", Nodes: nodes, Edges: edges, Trigger: trigger}}, nil, testutil.GetTestSmartWalletConfig(), nil)
	if err != nil {
		t.Fatalf("failed to create VM: %v", err)
	}
	vm.AddVar("settings", map[string]interface{}{"name": expectedWorkflowName})
	vm.ExecutionLogs = append(vm.ExecutionLogs, &avsproto.Execution_Step{Name: "run_swap", Success: true})

	processor := NewRestProcessor(vm)
	step, err := processor.Execute("sg-sum", node)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !step.Success {
		t.Fatalf("expected step success, got failure: %s", step.Error)
	}
}

func TestRestRequestArbitraryGlobalSecret(t *testing.T) {
	// This test demonstrates that ANY variable name can be used in config
	// without being defined anywhere in the source code

	// Create a completely arbitrary secret name that doesn't exist anywhere in the codebase
	arbitrarySecretName := "my_custom_api_service_key_12345"
	arbitrarySecretValue := "sk_test_arbitrary_value_that_works_dynamically"

	// Create mock server that expects this arbitrary secret
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")

		// Verify that our arbitrary secret was properly substituted
		if !strings.Contains(authHeader, arbitrarySecretValue) {
			t.Errorf("expected arbitrary secret to be substituted in Authorization header, got: %s", authHeader)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Arbitrary secret works!",
		})
	}))
	defer testServer.Close()

	// Create REST API node using the arbitrary secret name
	node := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url: testServer.URL + "/test",
			Headers: map[string]string{
				// Use the arbitrary secret name in template - no source code definition needed!
				"Authorization": "Bearer {{apContext.configVars." + arbitrarySecretName + "}}",
				"Content-Type":  "application/json",
			},
			Body:   "",
			Method: "GET",
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "arbitrary-secret-test",
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
			Target: "arbitrary-secret-test",
		},
	}

	// Create VM with the arbitrary secret - this proves no source code definition is needed
	globalSecrets := map[string]string{
		arbitrarySecretName: arbitrarySecretValue, // Any name works!
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "arbitrary-secret-test",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), globalSecrets)

	if err != nil {
		t.Fatalf("failed to create VM: %v", err)
	}

	processor := NewRestProcessor(vm)
	step, err := processor.Execute("arbitrary-secret-test", node)

	if err != nil {
		t.Fatalf("expected successful execution but got error: %v", err)
	}

	if !step.Success {
		t.Fatalf("expected step.Success to be true but got false, error: %s", step.Error)
	}

	// Verify the response
	responseData := gow.ValueToMap(step.GetRestApi().Data)
	dataField := responseData["data"].(map[string]interface{})

	if success, ok := dataField["success"].(bool); !ok || !success {
		t.Errorf("expected success=true in response")
	}

	if message, ok := dataField["message"].(string); !ok || message != "Arbitrary secret works!" {
		t.Errorf("expected message='Arbitrary secret works!' in response, got: %v", dataField["message"])
	}

	t.Logf("✅ SUCCESS: Arbitrary secret test passed!")
	t.Logf("✅ Secret name '%s' was dynamically loaded from config", arbitrarySecretName)
	t.Logf("✅ No source code definition needed - template {{apContext.configVars.%s}} works!", arbitrarySecretName)
	t.Logf("✅ This proves that ANY variable name defined in aggregator.yaml under macros.secrets will work")
}
