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
	}
}

func (r *RestProcessor) Execute(stepID string, node *avsproto.RestAPINode) (*avsproto.Execution_Step, error) {
	t0 := time.Now()

	// Get node data using helper function to reduce duplication
	nodeName, nodeInput := r.vm.GetNodeDataForExecution(stepID)

	executionLogStep := &avsproto.Execution_Step{
		Id:         stepID,
		OutputData: nil,
		Log:        "",
		Error:      "",
		Success:    true, // Assume success
		StartAt:    t0.UnixMilli(),
		Type:       avsproto.NodeType_NODE_TYPE_REST_API.String(),
		Name:       nodeName,
		Input:      nodeInput, // Include node input data for debugging
	}

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
	if url == "" || method == "" {
		err := fmt.Errorf("missing required configuration: url and method (provide via Config or input variables)")
		executionLogStep.Success = false
		executionLogStep.Error = err.Error()
		executionLogStep.EndAt = time.Now().UnixMilli()
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, err
	}

	// Preprocess URL, body, and headers for template variables
	if r.vm.logger != nil {
		r.vm.logger.Debug("REST API URL before template processing", "url", url)
	}
	url = r.vm.preprocessTextWithVariableMapping(url)
	if r.vm.logger != nil {
		r.vm.logger.Debug("REST API URL after template processing", "url", url)
	}

	// Process headers map for template variables first (needed to detect JSON content)
	processedHeaders := make(map[string]string)
	for key, value := range headers {
		processedKey := r.vm.preprocessTextWithVariableMapping(key)
		processedValue := r.vm.preprocessTextWithVariableMapping(value)
		processedHeaders[processedKey] = processedValue
	}

	// Check if this is JSON content by examining headers
	isJSONContent := false
	for key, value := range processedHeaders {
		if strings.ToLower(key) == "content-type" && strings.Contains(strings.ToLower(value), "application/json") {
			isJSONContent = true
			break
		}
	}

	// Process body with appropriate escaping based on content type
	if isJSONContent {
		if r.vm.logger != nil {
			r.vm.logger.Debug("REST API body before JSON-aware template processing", "body", body)
		}
		body = r.preprocessJSONWithVariableMapping(body)
		if r.vm.logger != nil {
			r.vm.logger.Debug("REST API body after JSON-aware template processing", "body", body)
		}
	} else {
		body = r.vm.preprocessTextWithVariableMapping(body)
	}

	// Validate template format in URL, body, and headers for malformed syntax
	if err := r.vm.validateTemplateFormat(url); err != nil {
		if r.vm.logger != nil {
			r.vm.logger.Error("REST API URL contains malformed template syntax", "url", url, "error", err)
		}
		executionLogStep.Success = false
		executionLogStep.Error = err.Error()
		executionLogStep.EndAt = time.Now().UnixMilli()
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, err
	}

	if err := r.vm.validateTemplateFormat(body); err != nil {
		if r.vm.logger != nil {
			r.vm.logger.Error("REST API request body contains malformed template syntax", "body", body, "error", err)
		}
		executionLogStep.Success = false
		executionLogStep.Error = err.Error()
		executionLogStep.EndAt = time.Now().UnixMilli()
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, err
	}

	for key, value := range processedHeaders {
		if err := r.vm.validateTemplateFormat(key); err != nil {
			if r.vm.logger != nil {
				r.vm.logger.Error("REST API header key contains malformed template syntax", "key", key, "error", err)
			}
			executionLogStep.Success = false
			executionLogStep.Error = err.Error()
			executionLogStep.EndAt = time.Now().UnixMilli()
			logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
			executionLogStep.Log = logBuilder.String()
			return executionLogStep, err
		}
		if err := r.vm.validateTemplateFormat(value); err != nil {
			if r.vm.logger != nil {
				r.vm.logger.Error("REST API header value contains malformed template syntax", "key", key, "value", value, "error", err)
			}
			executionLogStep.Success = false
			executionLogStep.Error = err.Error()
			executionLogStep.EndAt = time.Now().UnixMilli()
			logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
			executionLogStep.Log = logBuilder.String()
			return executionLogStep, err
		}
	}

	logBuilder.WriteString(fmt.Sprintf("Making %s request to: %s\n", method, url))
	if body != "" {
		logBuilder.WriteString(fmt.Sprintf("Request body: %s\n", body))
	}
	if len(processedHeaders) > 0 {
		logBuilder.WriteString(fmt.Sprintf("Request headers: %v\n", processedHeaders))
	}

	// Add debug logging for processed request details
	if r.vm.logger != nil {
		r.vm.logger.Debug("REST API request details",
			"method", method,
			"url", url,
			"body", body,
			"headers", processedHeaders)
	}

	// Validate JSON body if content-type is application/json
	if body != "" {
		for key, value := range processedHeaders {
			if strings.ToLower(key) == "content-type" && strings.Contains(strings.ToLower(value), "application/json") {
				var jsonTest interface{}
				if err := json.Unmarshal([]byte(body), &jsonTest); err != nil {
					errorMsg := fmt.Sprintf("invalid JSON in request body: %v", err)
					if r.vm.logger != nil {
						r.vm.logger.Error("REST API request body is not valid JSON",
							"body", body,
							"jsonError", err,
							"url", url)
					}
					executionLogStep.Success = false
					executionLogStep.Error = errorMsg
					executionLogStep.EndAt = time.Now().UnixMilli()
					logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errorMsg))
					logBuilder.WriteString(fmt.Sprintf("Request body: %s\n", body))
					executionLogStep.Log = logBuilder.String()
					return executionLogStep, fmt.Errorf(errorMsg)
				}
				break
			}
		}
	}

	// Create and execute HTTP request
	request := r.HttpClient.R()

	// Set headers
	for key, value := range processedHeaders {
		request.SetHeader(key, value)
	}

	// Set body if provided
	if body != "" {
		request.SetBody(body)
	}

	// Declare response and error variables
	var response *resty.Response
	var err error

	// Check if this is a mock URL and return mock response
	if mockResponse := r.getMockResponse(url, method, body, processedHeaders); mockResponse != nil {
		response = mockResponse
		err = nil
	} else {
		// Execute actual request
		switch strings.ToUpper(method) {
		case "GET":
			response, err = request.Get(url)
		case "POST":
			response, err = request.Post(url)
		case "PUT":
			response, err = request.Put(url)
		case "DELETE":
			response, err = request.Delete(url)
		case "PATCH":
			response, err = request.Patch(url)
		case "HEAD":
			response, err = request.Head(url)
		case "OPTIONS":
			response, err = request.Options(url)
		default:
			err = fmt.Errorf("unsupported HTTP method: %s", method)
		}
	}

	if err != nil {
		errorMsg := fmt.Sprintf("HTTP request failed: connection error or timeout: %v", err)
		executionLogStep.Success = false
		executionLogStep.Error = errorMsg
		executionLogStep.EndAt = time.Now().UnixMilli()
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errorMsg))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, fmt.Errorf(errorMsg)
	}

	logBuilder.WriteString(fmt.Sprintf("Request completed with status: %d\n", response.StatusCode()))
	logBuilder.WriteString(fmt.Sprintf("Response headers: %v\n", response.Header()))

	// Process response - store full response structure for workflow compatibility
	responseData := r.processResponse(response)

	// Convert the response to protobuf Value for storage
	// Use convertToProtobufCompatible to handle []string values in headers
	compatibleData := convertToProtobufCompatible(responseData)
	valueData, err := structpb.NewValue(compatibleData)
	if err != nil {
		if r.vm.logger != nil {
			r.vm.logger.Error("Failed to convert response to protobuf Value", "error", err)
		}
		// Create a simple string representation as fallback
		valueData = structpb.NewStringValue(fmt.Sprintf("Error converting response: %v", err))
	}

	// Store the Value directly in the output
	outputData := &avsproto.RestAPINode_Output{
		Data: valueData,
	}
	executionLogStep.OutputData = &avsproto.Execution_Step_RestApi{
		RestApi: outputData,
	}

	// Set output variable for following nodes (workflow behavior)
	r.SetOutputVarForStep(stepID, responseData)

	executionLogStep.EndAt = time.Now().UnixMilli()
	executionLogStep.Log = logBuilder.String()

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

// getMockResponse checks if the URL should be mocked and returns a mock response if applicable
func (r *RestProcessor) getMockResponse(url, method, body string, headers map[string]string) *resty.Response {
	// Check if this is a mock URL that should return a predefined response
	if strings.HasPrefix(url, MockAPIEndpoint+"/") || url == MockAPIEndpoint {
		// Create a temporary mock server
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// Set content type to JSON
			w.Header().Set("Content-Type", "application/json")

			// Create response based on URL path
			if strings.HasSuffix(url, "/data") {
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
		request := r.HttpClient.R()

		// Set headers
		for key, value := range headers {
			request.SetHeader(key, value)
		}

		// Set body if provided
		if body != "" {
			request.SetBody(body)
		}

		// Make request to mock server
		var response *resty.Response
		var err error

		switch strings.ToUpper(method) {
		case "GET":
			response, err = request.Get(server.URL)
		case "POST":
			response, err = request.Post(server.URL)
		case "PUT":
			response, err = request.Put(server.URL)
		case "DELETE":
			response, err = request.Delete(server.URL)
		case "PATCH":
			response, err = request.Patch(server.URL)
		case "HEAD":
			response, err = request.Head(server.URL)
		case "OPTIONS":
			response, err = request.Options(server.URL)
		default:
			return nil
		}

		if err != nil {
			return nil
		}

		return response
	}

	// Return nil if URL should not be mocked
	return nil
}
