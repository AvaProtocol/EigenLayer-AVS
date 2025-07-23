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
		// Set content type to JSON by default
		w.Header().Set("Content-Type", "application/json")

		// Handle mock API endpoints
		if strings.HasPrefix(url, MockAPIEndpoint) {
			m.handleMockAPIResponse(w, req, url, method, body, headers)
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

	// Make request to mock server, preserving the original URL path
	var mockURL string
	if strings.HasPrefix(url, MockAPIEndpoint) {
		// Extract the path from the original mock API URL
		remaining := url[len(MockAPIEndpoint):] // Skip "https://mock-api.ap-aggregator.local"
		mockURL = server.URL + remaining
	} else {
		mockURL = server.URL
	}

	// Make request to mock server
	switch strings.ToUpper(method) {
	case "GET":
		return request.Get(mockURL)
	case "POST":
		return request.Post(mockURL)
	case "PUT":
		return request.Put(mockURL)
	case "DELETE":
		return request.Delete(mockURL)
	case "PATCH":
		return request.Patch(mockURL)
	case "HEAD":
		return request.Head(mockURL)
	case "OPTIONS":
		return request.Options(mockURL)
	default:
		return nil, fmt.Errorf("unsupported HTTP method: %s", method)
	}
}

// handleMockAPIResponse handles mock API responses with proper status codes
func (m *MockHTTPExecutor) handleMockAPIResponse(w http.ResponseWriter, req *http.Request, url, method, body string, headers map[string]string) {
	requestPath := req.URL.Path

	// Handle specific endpoints
	if m.handleStatusEndpoint(w, requestPath, url) {
		return
	}

	if m.handleDelayEndpoint(requestPath) {
		// Delay handling doesn't need response, continue to default
	}

	if m.handleHeadersEndpoint(w, requestPath, headers) {
		return
	}

	if m.handleIPEndpoint(w, requestPath) {
		return
	}

	// Default mock API response for other endpoints
	m.handleDefaultResponse(w, url, method, body, headers)
}

// handleStatusEndpoint handles /status/{code} endpoints for testing different HTTP status codes
func (m *MockHTTPExecutor) handleStatusEndpoint(w http.ResponseWriter, requestPath, url string) bool {
	if !strings.Contains(requestPath, "/status/") {
		return false
	}

	statusStr := m.extractPathParameter(requestPath, "/status/", 8)
	if statusStr == "" {
		return false
	}

	var statusCode int
	if _, err := fmt.Sscanf(statusStr, "%d", &statusCode); err != nil || statusCode < 100 || statusCode > 599 {
		return false
	}

	// Handle no-content responses
	if statusCode == 204 || statusCode == 304 {
		w.Header().Del("Content-Type")
		w.WriteHeader(statusCode)
		return true
	}

	// Handle other status codes with JSON response
	response := m.createStatusResponse(statusCode, url)
	m.writeJSONResponse(w, response, statusCode)
	return true
}

// handleDelayEndpoint handles /delay/{seconds} endpoints for testing timeouts
func (m *MockHTTPExecutor) handleDelayEndpoint(requestPath string) bool {
	if !strings.Contains(requestPath, "/delay/") {
		return false
	}

	delayStr := m.extractPathParameter(requestPath, "/delay/", 7)
	if delayStr == "" {
		return false
	}

	var delaySeconds int
	if _, err := fmt.Sscanf(delayStr, "%d", &delaySeconds); err == nil && delaySeconds > 0 && delaySeconds <= 10 {
		time.Sleep(time.Duration(delaySeconds) * time.Second)
	}
	return true
}

// handleHeadersEndpoint handles /headers endpoint
func (m *MockHTTPExecutor) handleHeadersEndpoint(w http.ResponseWriter, requestPath string, headers map[string]string) bool {
	if !strings.Contains(requestPath, "/headers") {
		return false
	}

	response := map[string]interface{}{
		"headers": headers,
	}
	m.writeJSONResponse(w, response, http.StatusOK)
	return true
}

// handleIPEndpoint handles /ip endpoint
func (m *MockHTTPExecutor) handleIPEndpoint(w http.ResponseWriter, requestPath string) bool {
	if !strings.Contains(requestPath, "/ip") {
		return false
	}

	response := map[string]interface{}{
		"origin": "127.0.0.1",
	}
	m.writeJSONResponse(w, response, http.StatusOK)
	return true
}

// handleDefaultResponse handles the default mock API response
func (m *MockHTTPExecutor) handleDefaultResponse(w http.ResponseWriter, url, method, body string, headers map[string]string) {
	// Return the API response data directly without wrapper
	response := map[string]interface{}{
		"args":    parseQueryParams(url),
		"body":    body,
		"headers": headers,
		"json":    parseJSONBody(body),
		"method":  method,
		"url":     url,
	}
	m.writeJSONResponse(w, response, http.StatusOK)
}

// extractPathParameter extracts a parameter from a URL path
func (m *MockHTTPExecutor) extractPathParameter(requestPath, prefix string, prefixLen int) string {
	idx := strings.Index(requestPath, prefix)
	if idx == -1 {
		return ""
	}

	remaining := requestPath[idx+prefixLen:]
	endIdx := strings.Index(remaining, "/")
	if endIdx == -1 {
		endIdx = len(remaining)
	}
	return remaining[:endIdx]
}

// createStatusResponse creates a response based on the status code
func (m *MockHTTPExecutor) createStatusResponse(statusCode int, url string) map[string]interface{} {
	if statusCode >= 400 {
		return map[string]interface{}{
			"error": http.StatusText(statusCode),
			"code":  statusCode,
			"url":   url,
		}
	}
	return map[string]interface{}{
		"status": http.StatusText(statusCode),
		"code":   statusCode,
		"url":    url,
	}
}

// writeJSONResponse writes a JSON response with the given status code
func (m *MockHTTPExecutor) writeJSONResponse(w http.ResponseWriter, response map[string]interface{}, statusCode int) {
	jsonData, err := json.Marshal(response)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "Failed to marshal response"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	w.Write(jsonData)
}

// parseQueryParams extracts query parameters from a URL
func parseQueryParams(url string) map[string]interface{} {
	args := make(map[string]interface{})

	// Find the query string part
	if idx := strings.Index(url, "?"); idx != -1 {
		queryString := url[idx+1:]

		// Split by & to get individual parameters
		params := strings.Split(queryString, "&")
		for _, param := range params {
			if param != "" {
				// Split by = to get key and value
				if eqIdx := strings.Index(param, "="); eqIdx != -1 {
					key := param[:eqIdx]
					value := param[eqIdx+1:]
					args[key] = value
				} else {
					// Parameter without value
					args[param] = ""
				}
			}
		}
	}

	return args
}

// parseJSONBody attempts to parse the body as JSON
func parseJSONBody(body string) interface{} {
	if body == "" {
		return nil
	}

	var jsonData interface{}
	if err := json.Unmarshal([]byte(body), &jsonData); err == nil {
		return jsonData
	}

	return nil
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

	// Preprocess headers first so we can check content type
	processedHeaders := make(map[string]string)
	for key, value := range headers {
		processedKey := r.vm.preprocessTextWithVariableMapping(key)
		processedValue := r.vm.preprocessTextWithVariableMapping(value)
		processedHeaders[processedKey] = processedValue
	}

	// Only apply JSON preprocessing if content type is JSON
	contentType := ""
	for key, value := range processedHeaders {
		if strings.ToLower(key) == "content-type" {
			contentType = strings.ToLower(value)
			break
		}
	}

	// Check if content type is JSON-related
	isJSONContent := strings.Contains(contentType, "application/json") ||
		strings.Contains(contentType, "text/json") ||
		strings.Contains(contentType, "+json") || // For types like application/vnd.api+json
		strings.HasSuffix(contentType, "/json")

	if isJSONContent {
		// Apply JSON-aware preprocessing for JSON content
		body = r.preprocessJSONWithVariableMapping(body)
	} else {
		// Use regular text preprocessing for non-JSON content
		body = r.vm.preprocessTextWithVariableMapping(body)
	}

	if r.vm.logger != nil {
		r.vm.logger.Debug("REST API URL after template processing", "url", url)
		r.vm.logger.Debug("REST API body preprocessing", "contentType", contentType, "usedJSONPreprocessing", isJSONContent)
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
	if strings.HasPrefix(url, MockAPIEndpoint+"/") || url == MockAPIEndpoint {
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

	// Parse response and transform to standard format
	var responseData map[string]interface{}

	// Convert headers to a protobuf-compatible format
	convertedHeaders := convertStringSliceMapToProtobufCompatible(response.Header())

	// Parse response body
	var bodyData interface{}
	bodyStr := string(response.Body())
	if bodyStr != "" {
		var jsonData interface{}
		if err := json.Unmarshal(response.Body(), &jsonData); err == nil {
			bodyData = jsonData
		} else {
			bodyData = bodyStr
		}
	} else {
		bodyData = ""
	}

	// Create standard format response
	responseData = map[string]interface{}{
		"status":     response.StatusCode(),
		"statusText": getStatusText(response.StatusCode()),
		"url":        url,
		"headers":    convertedHeaders,
		"data":       bodyData,
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
			responseData["data"] = jsonData
		} else {
			// Fallback to string if not valid JSON
			responseData["data"] = string(responseBody)
		}
	} else {
		responseData["data"] = ""
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
