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

	if err != nil {
		t.Errorf("HTTP 404 should not cause execution error, but got: %v", err)
	}

	if !step.Success {
		t.Errorf("expected step.Success to be true even for 404 response")
	}

	// Verify the response data contains the 404 status
	responseData := gow.ValueToMap(step.GetRestApi().Data)
	if statusCode, ok := responseData["statusCode"].(float64); !ok || statusCode != 404 {
		t.Errorf("expected statusCode 404 in response data, got: %v", responseData["statusCode"])
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

	if !step.Success {
		t.Errorf("expected step.Success to be true even for 500 response")
	}

	// Verify the response data contains the 500 status
	responseData = gow.ValueToMap(step.GetRestApi().Data)
	if statusCode, ok := responseData["statusCode"].(float64); !ok || statusCode != 500 {
		t.Errorf("expected statusCode 500 in response data, got: %v", responseData["statusCode"])
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

	// Test basic response structure
	okField, exists := bodyMap["ok"]
	if !exists {
		t.Fatalf("❌ Telegram response missing 'ok' field")
	}
	if ok, isOk := okField.(bool); !isOk || !ok {
		t.Errorf("❌ Expected ok=true, got: %v (type: %T)", okField, okField)
	}

	// Get result object
	resultField, exists := bodyMap["result"]
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

	chatUsername, exists := chatMap["username"]
	if !exists {
		t.Fatalf("❌ Chat missing 'username' field")
	}
	if username, ok := chatUsername.(string); !ok || username != expectedChatUsername {
		t.Errorf("❌ Expected chat.username='%s', got: %v", expectedChatUsername, chatUsername)
	}

	t.Logf("✅ SUCCESS: Telegram mock server test passed!")
	t.Logf("✅ Response structure properly accessible without typeUrl/value wrapping")
	t.Logf("✅ Verified test fields: message_id=%d, from.first_name='%s', chat.username='%s'",
		expectedMessageId, expectedFromFirstName, expectedChatUsername)
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

	processor := NewRestProrcessor(vm)
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
	statusCode, exists := responseData["statusCode"]
	if !exists {
		t.Fatalf("❌ Response missing 'statusCode' field. Available fields: %v", getStringMapKeys(responseData))
	}
	if sc, ok := statusCode.(float64); !ok || sc != 200 {
		t.Errorf("❌ Expected statusCode 200, got: %v (type: %T)", statusCode, statusCode)
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

	// Test 1: account type
	accountType, exists := bodyMap["type"]
	if !exists {
		t.Fatalf("❌ SendGrid response missing 'type' field")
	}
	if accType, ok := accountType.(string); !ok || accType != expectedAccountType {
		t.Errorf("❌ Expected account type='%s', got: %v (type: %T)", expectedAccountType, accountType, accountType)
	}

	// Test 2: reputation
	reputation, exists := bodyMap["reputation"]
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

	processor := NewRestProrcessor(vm)
	step, err := processor.Execute("arbitrary-secret-test", node)

	if err != nil {
		t.Fatalf("expected successful execution but got error: %v", err)
	}

	if !step.Success {
		t.Fatalf("expected step.Success to be true but got false, error: %s", step.Error)
	}

	// Verify the response
	responseData := gow.ValueToMap(step.GetRestApi().Data)
	bodyField := responseData["body"].(map[string]interface{})

	if success, ok := bodyField["success"].(bool); !ok || !success {
		t.Errorf("expected success=true in response")
	}

	if message, ok := bodyField["message"].(string); !ok || message != "Arbitrary secret works!" {
		t.Errorf("expected message='Arbitrary secret works!' in response, got: %v", message)
	}

	t.Logf("✅ SUCCESS: Arbitrary secret test passed!")
	t.Logf("✅ Secret name '%s' was dynamically loaded from config", arbitrarySecretName)
	t.Logf("✅ No source code definition needed - template {{apContext.configVars.%s}} works!", arbitrarySecretName)
	t.Logf("✅ This proves that ANY variable name defined in aggregator.yaml under macros.secrets will work")
}
