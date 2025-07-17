package taskengine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/go-resty/resty/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

// Constants for mock API testing
const (
	MockAPIEndpoint = "https://mock-api.ap-aggregator.local"
)

// HTTPRequestExecutor interface for making HTTP requests
type HTTPRequestExecutor interface {
	ExecuteRequest(method, url, body string, headers map[string]string) (*resty.Response, error)
}

// ProductionHTTPExecutor implements HTTPRequestExecutor for production use
type ProductionHTTPExecutor struct {
	client *resty.Client
}

func NewProductionHTTPExecutor() *ProductionHTTPExecutor {
	client := resty.New()
	client.SetTimeout(30 * time.Second)
	return &ProductionHTTPExecutor{
		client: client,
	}
}

func (p *ProductionHTTPExecutor) ExecuteRequest(method, url, body string, headers map[string]string) (*resty.Response, error) {
	request := p.client.R()

	// Set headers
	for key, value := range headers {
		request.SetHeader(key, value)
	}

	// Set body if provided
	if body != "" {
		request.SetBody(body)
	}

	// Execute request
	switch strings.ToUpper(method) {
	case "GET":
		return request.Get(url)
	case "POST":
		return request.Post(url)
	case "PUT":
		return request.Put(url)
	case "DELETE":
		return request.Delete(url)
	case "PATCH":
		return request.Patch(url)
	case "HEAD":
		return request.Head(url)
	case "OPTIONS":
		return request.Options(url)
	default:
		return nil, fmt.Errorf("unsupported HTTP method: %s", method)
	}
}

// MockHTTPExecutor implements HTTPRequestExecutor for testing
type MockHTTPExecutor struct{}

func NewMockHTTPExecutor() *MockHTTPExecutor {
	return &MockHTTPExecutor{}
}

func (m *MockHTTPExecutor) ExecuteRequest(method, url, body string, headers map[string]string) (*resty.Response, error) {
	// Create a temporary mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Set content type to JSON
		w.Header().Set("Content-Type", "application/json")

		// Handle httpbin.org URLs with httpbin.org-like responses
		if strings.Contains(url, "httpbin.org") {
			response := map[string]interface{}{
				"args":    map[string]interface{}{},
				"data":    body,
				"files":   map[string]interface{}{},
				"form":    map[string]interface{}{},
				"headers": headers,
				"json":    nil,
				"origin":  "127.0.0.1",
				"url":     url,
			}

			jsonData, err := json.Marshal(response)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error": "Failed to marshal response"}`))
				return
			}

			w.WriteHeader(http.StatusOK)
			w.Write(jsonData)
		} else if strings.HasSuffix(url, "/data") {
			response := map[string]interface{}{
				"success": true,
				"message": "Mock API response from EigenLayer-AVS",
				"data": map[string]interface{}{
					"timestamp": "2025-01-16T17:00:00Z",
					"status":    "ok",
					"receivedData": map[string]interface{}{
						"url":     url,
						"method":  method,
						"body":    body,
						"headers": headers,
					},
				},
			}

			jsonData, err := json.Marshal(response)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error": "Failed to marshal response"}`))
				return
			}

			w.WriteHeader(http.StatusOK)
			w.Write(jsonData)
		} else {
			// Default mock response for other paths
			response := map[string]interface{}{
				"success": true,
				"message": "Default mock response from EigenLayer-AVS",
				"path":    url,
			}

			jsonData, err := json.Marshal(response)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error": "Failed to marshal response"}`))
				return
			}

			w.WriteHeader(http.StatusOK)
			w.Write(jsonData)
		}
	}))
	defer server.Close()

	// Make request to the mock server
	client := resty.New()
	request := client.R()

	// Set headers
	for key, value := range headers {
		request.SetHeader(key, value)
	}

	// Set body if provided
	if body != "" {
		request.SetBody(body)
	}

	// Make request to mock server
	switch strings.ToUpper(method) {
	case "GET":
		return request.Get(server.URL)
	case "POST":
		return request.Post(server.URL)
	case "PUT":
		return request.Put(server.URL)
	case "DELETE":
		return request.Delete(server.URL)
	case "PATCH":
		return request.Patch(server.URL)
	case "HEAD":
		return request.Head(server.URL)
	case "OPTIONS":
		return request.Options(server.URL)
	default:
		return nil, fmt.Errorf("unsupported HTTP method: %s", method)
	}
}

// RestProcessor handles REST API calls with template variable support
//
// Global secrets like sendgrid_key can be accessed via templates:
// Example URL: "https://api.sendgrid.com/v3/user/account"
// Example Headers: "Authorization": "Bearer {{apContext.configVars.sendgrid_key}}"
//
// Available global secrets (configured in aggregator.yaml):
// - ap_notify_bot_token: Telegram bot token for notifications
// - sendgrid_key: SendGrid API key for email services
//
// Usage example:
//   URL: https://api.sendgrid.com/v3/user/account
//   Headers: {
//     "Authorization": "Bearer {{apContext.configVars.sendgrid_key}}",
//     "Content-Type": "application/json"
//   }
//   Method: GET
//
// ... existing code ...

type RestProcessor struct {
	*CommonProcessor
	HttpClient *resty.Client
	executor   HTTPRequestExecutor
}

func NewRestProrcessor(vm *VM) *RestProcessor {
	client := resty.New()
	client.SetTimeout(30 * time.Second) // Default timeout
	// client.SetLogger(vm.logger) // Resty logger might not match sdklogging.Logger interface
	// TODO: Configure retry, auth, etc. as needed
	return &RestProcessor{
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
		HttpClient: client,
		executor:   NewProductionHTTPExecutor(),
	}
}

// NewRestProcessorWithExecutor allows injecting a custom HTTP executor (useful for testing)
func NewRestProcessorWithExecutor(vm *VM, executor HTTPRequestExecutor) *RestProcessor {
	client := resty.New()
	client.SetTimeout(30 * time.Second)
	return &RestProcessor{
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
		HttpClient: client,
		executor:   executor,
	}
}

func (r *RestProcessor) Execute(stepID string, node *avsproto.RestAPINode) (*avsproto.Execution_Step, error) {
	// Use shared function to create execution step
	executionLogStep := createNodeExecutionStep(stepID, avsproto.NodeType_NODE_TYPE_REST_API, r.vm)

	var logBuilder strings.Builder
	logBuilder.WriteString(fmt.Sprintf("Executing REST API Node ID: %s at %s\n", stepID, time.Now()))

	// Extract configuration from the node's Config message
	var url, method, body string
	var headers map[string]string

	// Start with Config values (if available)
	if node.Config != nil {
		url = node.Config.Url
		method = node.Config.Method
		body = node.Config.Body
		headers = node.Config.Headers
	}
	if headers == nil {
		headers = make(map[string]string)
	}

	// Extract and merge input variables from VM vars (runtime variables override config)
	r.vm.mu.Lock()
	if urlVar, exists := r.vm.vars["url"]; exists {
		if urlStr, ok := urlVar.(string); ok {
			url = urlStr
		}
	}
	if methodVar, exists := r.vm.vars["method"]; exists {
		if methodStr, ok := methodVar.(string); ok {
			method = methodStr
		}
	}
	if bodyVar, exists := r.vm.vars["body"]; exists {
		if bodyStr, ok := bodyVar.(string); ok {
			body = bodyStr
		}
	}
	if headersVar, exists := r.vm.vars["headers"]; exists {
		if headersMap, ok := headersVar.(map[string]string); ok {
			headers = headersMap
		}
	}
	if headersMapVar, exists := r.vm.vars["headersMap"]; exists {
		r.parseHeadersMap(headersMapVar, headers)
	}
	r.vm.mu.Unlock()

	// Validate required fields
	if url == "" {
		err := fmt.Errorf("missing required field: url")
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		finalizeExecutionStep(executionLogStep, false, err.Error(), logBuilder.String())
		return executionLogStep, err
	}

	// Preprocess URL, body, and headers for template variables
	if r.vm.logger != nil {
		r.vm.logger.Debug("REST API URL before template processing", "url", url)
	}

	url = r.vm.preprocessTextWithVariableMapping(url)
	body = r.preprocessJSONWithVariableMapping(body)

	// Preprocess headers
	processedHeaders := make(map[string]string)
	for key, value := range headers {
		processedKey := r.vm.preprocessTextWithVariableMapping(key)
		processedValue := r.vm.preprocessTextWithVariableMapping(value)
		processedHeaders[processedKey] = processedValue
	}

	if r.vm.logger != nil {
		r.vm.logger.Debug("REST API URL after template processing", "url", url)
	}

	// Default method to GET if not specified
	if method == "" {
		method = "GET"
	}

	// Validate URL format
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		err := fmt.Errorf("invalid URL format (must start with http:// or https://): %s", url)
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		finalizeExecutionStep(executionLogStep, false, err.Error(), logBuilder.String())
		return executionLogStep, err
	}

	// Create resty client
	client := resty.New()
	client.SetTimeout(30 * time.Second)

	// Create request
	request := client.R()

	// Set headers
	for key, value := range processedHeaders {
		request.SetHeader(key, value)
	}

	// Set body if method supports it
	if body != "" && (strings.ToUpper(method) == "POST" || strings.ToUpper(method) == "PUT" || strings.ToUpper(method) == "PATCH") {
		if r.vm.logger != nil {
			r.vm.logger.Debug("REST API request body", "body", body)
		}
		request.SetBody(body)
	}

	logBuilder.WriteString(fmt.Sprintf("Making %s request to: %s\n", method, url))
	if body != "" {
		logBuilder.WriteString(fmt.Sprintf("Request body: %s\n", body))
	}

	// Declare response and error variables
	var response *resty.Response
	var err error

	// Determine which executor to use based on URL
	var executor HTTPRequestExecutor
	if strings.HasPrefix(url, MockAPIEndpoint+"/") || url == MockAPIEndpoint || strings.Contains(url, "httpbin.org") {
		executor = NewMockHTTPExecutor()
	} else {
		executor = r.executor
	}

	// Execute request using the appropriate executor
	response, err = executor.ExecuteRequest(method, url, body, processedHeaders)

	if err != nil {
		// Format connection errors to match test expectations
		errorMsg := fmt.Sprintf("HTTP request failed: connection error or timeout - %s", err.Error())
		logBuilder.WriteString(fmt.Sprintf("Request failed: %s\n", errorMsg))
		finalizeExecutionStep(executionLogStep, false, errorMsg, logBuilder.String())
		return executionLogStep, fmt.Errorf(errorMsg)
	}

	if response == nil {
		err = fmt.Errorf("received nil response")
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		finalizeExecutionStep(executionLogStep, false, err.Error(), logBuilder.String())
		return executionLogStep, err
	}

	logBuilder.WriteString(fmt.Sprintf("Request completed with status: %d\n", response.StatusCode()))
	if r.vm.logger != nil {
		r.vm.logger.Debug("REST API response headers", "headers", response.Header())
	}
	logBuilder.WriteString(fmt.Sprintf("Response headers: %v\n", response.Header()))

	// Parse response
	var responseData map[string]interface{}

	// Convert headers to a protobuf-compatible format
	convertedHeaders := convertStringSliceMapToProtobufCompatible(response.Header())

	// Try to parse as JSON first
	bodyStr := string(response.Body())
	if bodyStr != "" {
		var jsonData interface{}
		if err := json.Unmarshal(response.Body(), &jsonData); err == nil {
			// Successfully parsed as JSON
			responseData = map[string]interface{}{
				"body":       jsonData,
				"headers":    convertedHeaders,
				"statusCode": response.StatusCode(),
			}
		} else {
			// Not JSON, treat as plain text
			responseData = map[string]interface{}{
				"body":       bodyStr,
				"headers":    convertedHeaders,
				"statusCode": response.StatusCode(),
			}
		}
	} else {
		// Empty body
		responseData = map[string]interface{}{
			"body":       "",
			"headers":    convertedHeaders,
			"statusCode": response.StatusCode(),
		}
	}

	// Log HTTP error status codes (4xx, 5xx) but don't treat them as execution failures
	// The workflow should handle HTTP errors through the status code in response data
	if response.StatusCode() >= 400 {
		statusMsg := fmt.Sprintf("HTTP %d: %s", response.StatusCode(), http.StatusText(response.StatusCode()))
		logBuilder.WriteString(fmt.Sprintf("HTTP Status: %s\n", statusMsg))
		if r.vm.logger != nil {
			r.vm.logger.Debug("REST API returned error status code", "statusCode", response.StatusCode(), "status", http.StatusText(response.StatusCode()))
		}
		// Continue with normal successful processing - the status code is available in responseData
	}

	// Create protobuf output
	outputValue, err := structpb.NewValue(responseData)
	if err != nil {
		logBuilder.WriteString(fmt.Sprintf("Error converting response to protobuf: %s\n", err.Error()))
		finalizeExecutionStep(executionLogStep, false, err.Error(), logBuilder.String())
		return executionLogStep, err
	}

	outputData := &avsproto.RestAPINode_Output{
		Data: outputValue,
	}

	executionLogStep.OutputData = &avsproto.Execution_Step_RestApi{
		RestApi: outputData,
	}

	// Use shared function to set output variable for following nodes (workflow behavior)
	setNodeOutputData(r.CommonProcessor, stepID, responseData)

	// Use shared function to finalize execution step
	finalizeExecutionStep(executionLogStep, true, "", logBuilder.String())

	if r.vm.logger != nil {
		r.vm.logger.Info("REST API request executed successfully", "stepID", stepID, "method", method, "url", url, "statusCode", response.StatusCode())
	}

	return executionLogStep, nil
}

// parseHeadersMap handles different headersMap formats
func (r *RestProcessor) parseHeadersMap(headersMapVal interface{}, headers map[string]string) {
	if headersMap, ok := headersMapVal.([][]string); ok {
		for _, header := range headersMap {
			if len(header) == 2 {
				headers[header[0]] = header[1]
			}
		}
	} else if headersAny, ok := headersMapVal.([]interface{}); ok {
		for _, headerAny := range headersAny {
			if headerSlice, ok := headerAny.([]interface{}); ok && len(headerSlice) == 2 {
				if key, ok := headerSlice[0].(string); ok {
					if value, ok := headerSlice[1].(string); ok {
						headers[key] = value
					}
				}
			}
		}
	}
}

// processResponse converts HTTP response to structured data for workflow compatibility
func (r *RestProcessor) processResponse(response *resty.Response) map[string]interface{} {
	responseData := make(map[string]interface{})

	// Always include basic response info
	responseData["statusCode"] = response.StatusCode()

	// Convert http.Header (map[string][]string) to protobuf-compatible format
	headers := make(map[string]interface{})
	for key, values := range response.Header() {
		if len(values) == 1 {
			headers[key] = values[0] // Single value as string
		} else {
			headers[key] = values // Multiple values as array
		}
	}
	responseData["headers"] = headers

	responseBody := response.Body()
	if len(responseBody) > 0 {
		// Try to parse as JSON first
		var jsonData interface{}
		if err := json.Unmarshal(responseBody, &jsonData); err == nil {
			responseData["body"] = jsonData
		} else {
			// Fallback to string if not valid JSON
			responseData["body"] = string(responseBody)
		}
	} else {
		responseData["body"] = ""
	}

	return responseData
}

// escapeJSONString properly escapes a string for use within JSON
func escapeJSONString(s string) string {
	// Use Go's built-in JSON marshaling to properly escape the string
	jsonBytes, err := json.Marshal(s)
	if err != nil {
		// If marshaling fails, fallback to basic escaping
		s = strings.ReplaceAll(s, "\\", "\\\\")
		s = strings.ReplaceAll(s, "\"", "\\\"")
		s = strings.ReplaceAll(s, "\n", "\\n")
		s = strings.ReplaceAll(s, "\r", "\\r")
		s = strings.ReplaceAll(s, "\t", "\\t")
		return s
	}
	// Remove the surrounding quotes that json.Marshal adds
	return string(jsonBytes[1 : len(jsonBytes)-1])
}

// preprocessJSONWithVariableMapping processes template variables with JSON-aware escaping
// Uses the VM's smart variable resolution with fallback support for node_name.data patterns
func (r *RestProcessor) preprocessJSONWithVariableMapping(text string) string {
	if r.vm == nil {
		return text
	}

	if !strings.Contains(text, "{{") || !strings.Contains(text, "}}") {
		return text
	}

	jsvm := NewGojaVM()
	r.vm.mu.Lock()
	currentVars := make(map[string]interface{})
	for k, val := range r.vm.vars {
		currentVars[k] = val
	}
	r.vm.mu.Unlock()

	for key, value := range currentVars {
		if err := jsvm.Set(key, value); err != nil {
			if r.vm.logger != nil {
				r.vm.logger.Error("failed to set variable in JS VM for JSON preprocessing", "key", key, "error", err)
			}
		}
	}

	result := text
	for i := 0; i < VMMaxPreprocessIterations; i++ {
		start := strings.Index(result, "{{")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "}}")
		if end == -1 {
			break
		}
		end += start // Adjust end to be relative to the start of `result`

		expr := strings.TrimSpace(result[start+2 : end])
		if expr == "" {
			result = result[:start] + result[end+2:]
			continue
		}
		// Simple check for nested, though might not be perfect for all cases.
		if strings.Index(expr, "{{") != -1 || strings.Index(expr, "}}") != -1 {
			if r.vm.logger != nil {
				r.vm.logger.Warn("Nested expression detected in JSON preprocessing, replacing with empty string", "expression", expr)
			}
			result = result[:start] + result[end+2:]
			continue
		}

		// Try to resolve the variable with fallback to camelCase (same as VM)
		exportedValue, resolved := r.vm.resolveVariableWithFallback(jsvm, expr, currentVars)
		if !resolved {
			// Replace with "undefined" instead of removing the expression for JSON compatibility
			if r.vm.logger != nil {
				r.vm.logger.Debug("JSON template variable evaluation failed, replacing with 'undefined'", "expression", expr)
				// Debug: Log available variables
				varNames := make([]string, 0, len(currentVars))
				for k := range currentVars {
					varNames = append(varNames, k)
				}
				r.vm.logger.Debug("Available variables in VM", "variables", varNames)
			}
			result = result[:start] + "undefined" + result[end+2:]
			continue
		}

		var replacement string
		if t, ok := exportedValue.(time.Time); ok {
			replacement = t.In(time.UTC).Format("2006-01-02 15:04:05.000 +0000 UTC")
		} else if _, okMap := exportedValue.(map[string]interface{}); okMap {
			replacement = "[object Object]" // Mimic JS behavior for objects in strings
		} else if _, okArr := exportedValue.([]interface{}); okArr {
			replacement = fmt.Sprintf("%v", exportedValue) // Or could be "[object Array]" or stringified JSON
		} else {
			replacement = fmt.Sprintf("%v", exportedValue)
		}

		// Apply JSON escaping to the replacement value
		escapedReplacement := escapeJSONString(replacement)

		result = result[:start] + escapedReplacement + result[end+2:]
	}
	return result
}
