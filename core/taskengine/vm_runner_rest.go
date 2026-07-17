package taskengine

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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
	// Return the API response data wrapped in a map structure, compatible with httpbin.org format
	response := map[string]interface{}{
		"args":    parseQueryParams(url),
		"data":    parseJSONBody(body), // data is the parsed JSON body (not raw string)
		"headers": headers,
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

func NewRestProcessor(vm *VM) *RestProcessor {
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
	executionLogStep := CreateNodeExecutionStep(stepID, r.GetTaskNode(), r.vm)

	var logBuilder strings.Builder
	logBuilder.WriteString(formatNodeExecutionLogHeader(executionLogStep))

	// Extract configuration from the node's Config message
	var url, method, body string
	var headers map[string]string
	var err error
	var stepSuccess bool
	var errorMessage string

	defer func() {
		finalizeStep(executionLogStep, stepSuccess, err, errorMessage, logBuilder.String())
	}()

	// Start with Config values (if available)
	if node.Config != nil {
		url = node.Config.Url
		method = node.Config.Method
		body = node.Config.Body
		headers = node.Config.Headers
		if r.vm.logger != nil {
			r.vm.logger.Debug("REST API extracted headers from node config", "stepID", stepID, "headers", headers)
		}
	}
	if headers == nil {
		headers = make(map[string]string)
		if r.vm.logger != nil {
			r.vm.logger.Debug("REST API headers were nil, initialized empty map", "stepID", stepID)
		}
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
			if r.vm.logger != nil {
				r.vm.logger.Debug("REST API overriding headers with VM variable", "stepID", stepID, "originalHeaders", headers, "newHeaders", headersMap)
			}
			headers = headersMap
		} else {
			if r.vm.logger != nil {
				r.vm.logger.Debug("REST API found headers VM var but wrong type", "stepID", stepID, "headersVarType", fmt.Sprintf("%T", headersVar))
			}
		}
	} else {
		if r.vm.logger != nil {
			r.vm.logger.Debug("REST API no headers VM variable found, keeping config headers", "stepID", stepID, "headers", headers)
		}
	}
	r.vm.mu.Unlock()

	// Validate required fields
	if url == "" {
		err = NewMissingRequiredFieldError("url")
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		return executionLogStep, err
	}

	// LANGUAGE ENFORCEMENT: Validate Handlebars template size for fields that contain template delimiters.
	// Only apply when {{ is present to avoid reducing the REST body size limit (10MB) to the
	// Handlebars template limit (100KB) for non-template content.
	if strings.Contains(url, "{{") {
		if err = ValidateInputByLanguage(url, avsproto.Lang_LANG_HANDLEBARS); err != nil {
			logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
			return executionLogStep, err
		}
	}
	if strings.Contains(body, "{{") {
		if err = ValidateInputByLanguage(body, avsproto.Lang_LANG_HANDLEBARS); err != nil {
			logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
			return executionLogStep, err
		}
	}
	for key, value := range headers {
		if strings.Contains(key, "{{") {
			if err = ValidateInputByLanguage(key, avsproto.Lang_LANG_HANDLEBARS); err != nil {
				logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
				return executionLogStep, err
			}
		}
		if strings.Contains(value, "{{") {
			if err = ValidateInputByLanguage(value, avsproto.Lang_LANG_HANDLEBARS); err != nil {
				logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
				return executionLogStep, err
			}
		}
	}

	// Preprocess URL, body, and headers for template variables
	if r.vm.logger != nil {
		r.vm.logger.Debug("REST API URL before template processing", "url", url)
	}

	originalURL := url
	url = r.vm.preprocessTextWithVariableMapping(url)

	// Validate URL template variable resolution
	if err = ValidateTemplateVariableResolution(url, originalURL, r.vm, "url"); err != nil {
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		return executionLogStep, err
	}

	// Preprocess headers first so we can check content type
	processedHeaders := make(map[string]string)
	for key, value := range headers {
		processedKey := r.vm.preprocessTextWithVariableMapping(key)
		processedValue := r.vm.preprocessTextWithVariableMapping(value)

		// Validate header template variable resolution
		if validateErr := ValidateTemplateVariableResolution(processedKey, key, r.vm, "header key"); validateErr != nil {
			logBuilder.WriteString(fmt.Sprintf("Error: %s\n", validateErr.Error()))
			err = validateErr
			return executionLogStep, err
		}
		if validateErr := ValidateTemplateVariableResolution(processedValue, value, r.vm, "header value"); validateErr != nil {
			logBuilder.WriteString(fmt.Sprintf("Error: %s\n", validateErr.Error()))
			err = validateErr
			return executionLogStep, err
		}

		processedHeaders[processedKey] = processedValue
	}

	// Server-side auth providers (options.auth): mint + attach a provider token so the
	// credential never lives in the workflow JSON. GoPlus first; on "" (keys unset or
	// mint failed) we send no Authorization header — GoPlus still answers keyless.
	if restAuthProvider(node) == "goplus" {
		if token := goplusTokenProvider(); token != "" {
			processedHeaders["Authorization"] = token
			if r.vm.logger != nil {
				r.vm.logger.Debug("REST: attached GoPlus session token (authed)", "stepID", stepID)
			}
		} else if r.vm.logger != nil {
			// Keys unset or mint failed: GoPlus still answers keyless (lower limits).
			r.vm.logger.Warn("REST: GoPlus auth requested but no token minted; falling back to keyless", "stepID", stepID)
		}
	}

	// Only apply JSON preprocessing if content type is JSON
	contentType := ""
	for key, value := range processedHeaders {
		if strings.ToLower(key) == "content-type" {
			contentType = strings.ToLower(value)
			break
		}
	}

	// Check body size limit (before processing to avoid wasting resources)
	if len(body) > MaxRestAPIBodySize {
		err = NewStructuredError(
			avsproto.ErrorCode_INVALID_NODE_CONFIG,
			fmt.Sprintf("%s: %d bytes (max: %d bytes)", ValidationErrorMessages.RestAPIBodyTooLarge, len(body), MaxRestAPIBodySize),
			map[string]interface{}{
				"field":   "body",
				"issue":   "size limit exceeded",
				"size":    len(body),
				"maxSize": MaxRestAPIBodySize,
			},
		)
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		return executionLogStep, err
	}

	// Check if content type is JSON-related
	isJSONContent := strings.Contains(contentType, "application/json") ||
		strings.Contains(contentType, "text/json") ||
		strings.Contains(contentType, "+json") || // For types like application/vnd.api+json
		strings.HasSuffix(contentType, "/json")

	// Validate JSON format if content type is JSON and body is not empty
	if isJSONContent && body != "" {
		var jsonTest interface{}
		if jsonErr := json.Unmarshal([]byte(body), &jsonTest); jsonErr != nil {
			err = NewStructuredError(
				avsproto.ErrorCode_INVALID_NODE_CONFIG,
				fmt.Sprintf("%s: %s", ValidationErrorMessages.RestAPIBodyInvalidJSON, jsonErr.Error()),
				map[string]interface{}{
					"field":       "body",
					"issue":       "invalid JSON format",
					"contentType": contentType,
					"error":       jsonErr.Error(),
				},
			)
			logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
			return executionLogStep, err
		}
	}

	originalBody := body
	if isJSONContent {
		// Apply JSON-aware preprocessing for JSON content
		body = r.preprocessJSONWithVariableMapping(body)
	} else {
		// Use regular text preprocessing for non-JSON content
		body = r.vm.preprocessTextWithVariableMapping(body)
	}

	// Validate body template variable resolution
	if validateErr := ValidateTemplateVariableResolution(body, originalBody, r.vm, "body"); validateErr != nil {
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", validateErr.Error()))
		err = validateErr
		return executionLogStep, err
	}

	if r.vm.logger != nil {
		r.vm.logger.Debug("REST API URL after template processing", "url", url)
		r.vm.logger.Debug("REST API processedHeaders for content-type detection", "processedHeaders", processedHeaders)
		r.vm.logger.Debug("REST API body preprocessing", "contentType", contentType, "usedJSONPreprocessing", isJSONContent)
	}

	// Optionally forward raw execution data to Studio /api/notify (Path B), which summarizes
	// AND sends and returns the summary (surfaced to the SDK after the POST). The gateway no
	// longer summarizes or sends notifications itself — it is a pass-through to /api/notify.
	isStudioNotify := false
	if shouldSummarize(r.vm, node) {
		provider := detectNotificationProvider(url)
		if r.vm.logger != nil {
			r.vm.logger.Info("REST API summarize enabled", "provider", provider, "url", url)
		}
		if provider == "studio-notify" {
			isStudioNotify = true
			if cm := globalSummarizer; cm != nil {
				if payload, err := cm.BuildNotifyPayload(r.vm, r.vm.GetNodeNameAsVar(stepID)); err == nil {
					var bodyObj map[string]interface{}
					if json.Unmarshal([]byte(body), &bodyObj) == nil {
						// Node fields (channel/format/recipient/...) win; add the raw execution data.
						for k, v := range payload {
							if _, exists := bodyObj[k]; !exists {
								bodyObj[k] = v
							}
						}
						body = FormatAsJSON(bodyObj)
					} else if r.vm.logger != nil {
						r.vm.logger.Warn("REST API notify: body is not JSON, skipping payload injection")
					}
				} else if r.vm.logger != nil {
					r.vm.logger.Warn("REST API notify: failed to build raw payload", "error", err)
				}
			} else if r.vm.logger != nil {
				r.vm.logger.Warn("REST API notify: no summarizer configured to build payload")
			}
		} else if provider == "telegram" && r.vm.logger != nil {
			// Legacy direct-to-Telegram nodes no longer get gateway-side summary injection
			// (Path B routes telegram through /api/notify). Warn so the no-op is debuggable.
			r.vm.logger.Warn("REST API summarize on legacy direct-Telegram node — re-save the workflow in Studio to route through /api/notify")
		}
	}

	// Default method to GET if not specified
	if method == "" {
		method = "GET"
	}

	// Validate URL format
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		err = fmt.Errorf("invalid URL format (must start with http:// or https://): %s", url)
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
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
		logBuilder.WriteString(fmt.Sprintf("Request body: %s\n", FormatAsJSON(body)))
	}

	// Declare response variable
	var response *resty.Response

	// Determine which executor to use based on URL
	var executor HTTPRequestExecutor
	isMockEndpoint := strings.HasPrefix(url, MockAPIEndpoint+"/") || url == MockAPIEndpoint
	if isMockEndpoint {
		executor = NewMockHTTPExecutor()
	} else {
		executor = r.executor
	}
	response, err = executor.ExecuteRequest(method, url, body, processedHeaders)

	if err != nil {
		// Format connection errors to match test expectations
		err = fmt.Errorf("HTTP request failed: connection error or timeout - %s", err.Error())
		logBuilder.WriteString(fmt.Sprintf("Request failed: %s\n", err.Error()))
		return executionLogStep, err
	}

	if response == nil {
		err = fmt.Errorf("received nil response")
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
		return executionLogStep, err
	}

	logBuilder.WriteString(fmt.Sprintf("Request completed with status: %d\n", response.StatusCode()))
	if r.vm.logger != nil {
		r.vm.logger.Debug("REST API response headers", "headers", response.Header())
	}
	logBuilder.WriteString(fmt.Sprintf("Response headers: %s\n", FormatAsJSON(response.Header())))

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
			if isStudioNotify {
				// /api/notify returns { ok, summary }. Surface the run summary (subject + body
				// line) to the SDK so the UI keeps showing it — same shape the legacy local
				// summary produced. Omitted for format=plain (no summary in the response).
				if m, ok := jsonData.(map[string]interface{}); ok {
					if summaryVal, ok := m["summary"].(map[string]interface{}); ok {
						subj, _ := summaryVal["subject"].(string)
						bodyText := ""
						if bodyMap, ok := summaryVal["body"].(map[string]interface{}); ok {
							bodyText, _ = bodyMap["summary"].(string)
						}
						bodyData = map[string]interface{}{"subject": subj, "body": bodyText}
					}
				}
			}
		} else {
			bodyData = bodyStr
		}
	} else {
		// No body returned (common for notification providers with 202 Accepted). The
		// studio-notify summary, when the provider returns one, is extracted from the
		// response body above; otherwise there is nothing to surface.
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

	// Create protobuf output
	outputValue, err := structpb.NewValue(responseData)
	if err != nil {
		logBuilder.WriteString(fmt.Sprintf("Error converting response to protobuf: %s\n", err.Error()))
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

	// Determine if the step was successful based on HTTP status code
	if response.StatusCode() >= 400 {
		stepSuccess = false
		errorMessage = fmt.Sprintf("HTTP %d: %s", response.StatusCode(), http.StatusText(response.StatusCode()))
		logBuilder.WriteString(fmt.Sprintf("HTTP Status Error: %s\n", errorMessage))
	} else {
		stepSuccess = true
		errorMessage = ""
	}

	// Defer will finalize the execution step
	return executionLogStep, nil
}

// shouldSummarize checks node.Config.Options for summarize=true
func shouldSummarize(vm *VM, node *avsproto.RestAPINode) bool {
	if vm == nil {
		return false
	}
	// Check protobuf node.Config.Options
	if node != nil && node.Config != nil && node.Config.Options != nil {
		if optsMap, ok := node.Config.Options.AsInterface().(map[string]interface{}); ok {
			if v, ok := optsMap["summarize"].(bool); ok {
				return v
			}
		}
	}
	return false
}

// detectNotificationProvider returns "studio-notify" or "telegram" based on URL patterns; empty string if unknown
func detectNotificationProvider(u string) string {
	lu := strings.ToLower(u)
	// Studio /api/notify — Path B distribution endpoint (path-based so mock servers match).
	if strings.Contains(lu, "/api/notify") {
		return "studio-notify"
	}
	if strings.Contains(lu, "api.telegram.org") && strings.Contains(lu, "/sendmessage") {
		return "telegram"
	}
	// Heuristic for tests using mock servers but Telegram path shape
	if strings.Contains(lu, "/bot") && strings.Contains(lu, "/sendmessage") {
		return "telegram"
	}
	return ""
}

// ── REST auth providers ─────────────────────────────────────────────────────
// A restApi node can request server-side credential injection with
//
//	config.options.auth = { "provider": "goplus" }
//
// The gateway mints the provider's token from macros.secrets and attaches it as
// the Authorization header at execution time, so the secret never appears in the
// (client-visible) workflow JSON. GoPlus is the first provider; add more here.

// restAuthProvider returns the lower-cased options.auth.provider (e.g. "goplus"),
// or "" when none is set. Mirrors shouldSummarize's options-bag read.
func restAuthProvider(node *avsproto.RestAPINode) string {
	if node == nil || node.Config == nil || node.Config.Options == nil {
		return ""
	}
	optsMap, ok := node.Config.Options.AsInterface().(map[string]interface{})
	if !ok {
		return ""
	}
	auth, ok := optsMap["auth"].(map[string]interface{})
	if !ok {
		return ""
	}
	provider, _ := auth["provider"].(string)
	return strings.ToLower(strings.TrimSpace(provider))
}

// goplusTokenProvider is the seam the REST runner calls to obtain a GoPlus
// Authorization header value; overridable in tests. Returns "" for keyless.
var goplusTokenProvider = goplusAuthHeader

// goplusHTTPClient bounds the token-mint call so a slow/hung GoPlus endpoint
// cannot block REST node execution indefinitely.
var goplusHTTPClient = &http.Client{Timeout: 15 * time.Second}

// goplusTokenCache holds the module-cached GoPlus session token, refreshed shortly
// before expiry. Keyed by the app_key it was minted for, so a rotated key never
// reuses a stale token. The token value already includes the "Bearer " prefix.
var goplusTokenCache struct {
	sync.RWMutex
	token   string
	appKey  string
	expires time.Time
}

// goplusAuthHeader returns the Authorization header value for GoPlus, minting +
// caching a signed session token from macros.secrets (goplus_app_key /
// goplus_app_secret): sign = sha1_hex(app_key + time + app_secret) → POST /v1/token.
// Returns "" to fall back to keyless GoPlus access (lower rate limits) when the
// keys are unset or minting fails — the caller then sends no Authorization header.
func goplusAuthHeader() string {
	appKey := GetMacroSecret("goplus_app_key")
	appSecret := GetMacroSecret("goplus_app_secret")
	if appKey == "" || appSecret == "" {
		return ""
	}

	goplusTokenCache.RLock()
	if goplusTokenCache.token != "" && goplusTokenCache.appKey == appKey && time.Until(goplusTokenCache.expires) > 60*time.Second {
		token := goplusTokenCache.token
		goplusTokenCache.RUnlock()
		return token
	}
	goplusTokenCache.RUnlock()

	// Mint the token WITHOUT holding the cache lock, so an in-flight HTTP call (up to
	// the client timeout) never serializes unrelated REST executions that only need to
	// RLock the cache. A few goroutines may race and mint duplicates on a cold/expiring
	// cache — cheap and harmless (last writer wins).
	now := time.Now().Unix()
	sum := sha1.Sum([]byte(appKey + strconv.FormatInt(now, 10) + appSecret))
	reqBody, _ := json.Marshal(map[string]interface{}{
		"app_key": appKey,
		"sign":    hex.EncodeToString(sum[:]),
		"time":    now,
	})
	req, err := http.NewRequest(http.MethodPost, "https://api.gopluslabs.io/api/v1/token", bytes.NewReader(reqBody))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := goplusHTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var parsed struct {
		Code   int `json:"code"`
		Result struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int64  `json:"expires_in"`
		} `json:"result"`
	}
	if decodeErr := json.NewDecoder(resp.Body).Decode(&parsed); decodeErr != nil || parsed.Code != 1 || parsed.Result.AccessToken == "" {
		return ""
	}
	// GoPlus returns the token already prefixed with "Bearer "; normalize defensively
	// so a change on their side can't produce an invalid Authorization header.
	token := parsed.Result.AccessToken
	if !strings.HasPrefix(token, "Bearer ") {
		token = "Bearer " + token
	}
	// Publish under the write lock — brief, no network held.
	goplusTokenCache.Lock()
	goplusTokenCache.token = token
	goplusTokenCache.appKey = appKey
	goplusTokenCache.expires = time.Now().Add(time.Duration(parsed.Result.ExpiresIn) * time.Second)
	goplusTokenCache.Unlock()
	return token
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
		if strings.Contains(expr, "{{") || strings.Contains(expr, "}}") {
			if r.vm.logger != nil {
				r.vm.logger.Warn("Nested expression detected in JSON preprocessing, replacing with empty string", "expression", expr)
			}
			result = result[:start] + result[end+2:]
			continue
		}

		// Try to resolve the variable path (same as VM)
		exportedValue, resolved := r.vm.resolveVariablePath(jsvm, expr, currentVars)
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
		} else if mapValue, okMap := exportedValue.(map[string]interface{}); okMap {
			// Serialize objects as JSON instead of "[object Object]" for better usability
			if jsonBytes, err := json.Marshal(mapValue); err == nil {
				replacement = string(jsonBytes)
			} else {
				replacement = "[object Object]" // Fallback to old behavior if JSON marshal fails
			}
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
