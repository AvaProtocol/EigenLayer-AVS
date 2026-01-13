package taskengine

import (
	"encoding/json"
	"fmt"
	"html"
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

// SendGrid Dynamic Template ID for AI-generated workflow summaries
const (
	SendGridSummaryTemplateID = "d-3b4b885af0fc45ad822024ebc72f169c"
)

// Status HTML templates for email notifications
const (
	// StatusHtmlFailedTemplate is the HTML template for failed workflow status badge
	StatusHtmlFailedTemplate = `<div style="display:inline-block; padding:8px 16px; background-color:#FEE2E2; color:#991B1B; border-radius:8px; font-weight:500; margin:8px 0"><svg width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg" style="vertical-align:middle; margin-right:6px"><circle cx="8" cy="8" r="7" fill="#EF4444"/><path d="M10 6L6 10M6 6L10 10" stroke="white" stroke-width="2" stroke-linecap="round"/></svg>Execution failed</div>`
	// StatusHtmlSuccessTemplate is the HTML template for successful workflow status badge
	StatusHtmlSuccessTemplate = `<div style="display:inline-block; padding:8px 16px; background-color:#D1FAE5; color:#065F46; border-radius:8px; font-weight:500; margin:8px 0"><svg width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg" style="vertical-align:middle; margin-right:6px"><circle cx="8" cy="8" r="7" fill="#10B981"/><path d="M11 6L7 10L5 8" stroke="white" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>All steps completed successfully</div>`
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

	// Optionally compose summary and inject into provider payloads when enabled via nodeConfig.options.summarize
	// Keep a reference to the composed summary so we can return it to clients even
	// when the external notification provider (e.g., SendGrid) returns an empty body.
	var summaryForClient *Summary
	if shouldSummarize(r.vm, node) {
		provider := detectNotificationProvider(url)
		if r.vm.logger != nil {
			r.vm.logger.Info("REST API summarize enabled", "provider", provider, "url", url)
		}
		if provider != "" {
			// Build summary from current VM context (AI if enabled, else deterministic)
			currentName := r.vm.GetNodeNameAsVar(stepID)
			if r.vm.logger != nil {
				r.vm.logger.Info("REST API calling ComposeSummarySmart", "currentName", currentName)
			}
			s := ComposeSummarySmart(r.vm, currentName)
			if r.vm.logger != nil {
				r.vm.logger.Info("REST API summary generated", "subject", s.Subject, "bodyLength", len(s.Body))
			}
			summaryForClient = &s

			// Attempt to parse body as JSON and inject subject/body accordingly
			var bodyObj map[string]interface{}
			if err := json.Unmarshal([]byte(body), &bodyObj); err == nil {
				switch provider {
				case "sendgrid":
					// Always use Dynamic Templates for AI summaries - inject template_id automatically
					bodyObj["template_id"] = SendGridSummaryTemplateID

					// Get dynamic template data from summary (subject/title/analysis...)
					dynamicData := s.SendGridDynamicData()

					// Enrich with runner/eoaAddress for template usage
					smartWallet := ""
					ownerEOA := ""
					r.vm.mu.Lock()
					if aaSender, ok := r.vm.vars["aa_sender"].(string); ok && aaSender != "" {
						smartWallet = aaSender
					}
					// Get execution index for "Run #X:" prefix
					executionIndex := int64(-1)
					if wc, ok := r.vm.vars[WorkflowContextVarName].(map[string]interface{}); ok {
						if runner, ok := wc["runner"].(string); ok && runner != "" && smartWallet == "" {
							smartWallet = runner
						}
						if owner, ok := wc["owner"].(string); ok && owner != "" {
							ownerEOA = owner
						}
						if eoa, ok := wc["eoaAddress"].(string); ok && eoa != "" && ownerEOA == "" {
							ownerEOA = eoa
						}
						// Try to get execution index from workflow context (it may be populated for deployed workflows)
						if idx, ok := wc["executionIndex"]; ok {
							switch v := idx.(type) {
							case int64:
								executionIndex = v
							case float64:
								executionIndex = int64(v)
							case int:
								executionIndex = int64(v)
							}
						}
					}
					if smartWallet == "" {
						if settings, ok := r.vm.vars["settings"].(map[string]interface{}); ok {
							if runner, ok := settings["runner"].(string); ok && strings.TrimSpace(runner) != "" {
								smartWallet = runner
							}
						}
					}
					r.vm.mu.Unlock()
					dynamicData["runner"] = smartWallet
					dynamicData["eoaAddress"] = shortHex(ownerEOA)
					dynamicData["year"] = fmt.Sprintf("%d", time.Now().Year()) // Current year for email footer

					// Compute skipped nodes (by name) = workflow nodes - executed step names
					// Skip branch condition pseudo-nodes which are routing points, not actual nodes
					// Include the CURRENT node since it's executing (not yet in ExecutionLogs)
					executedNames := make(map[string]struct{})
					for _, st := range r.vm.ExecutionLogs {
						name := st.GetName()
						if name == "" {
							name = st.GetId()
						}
						if name != "" {
							executedNames[name] = struct{}{}
						}
					}
					// Add current node name to executed list (it's executing but not yet in logs)
					currentNodeName := ""
					r.vm.mu.Lock()
					if currentTaskNode, ok := r.vm.TaskNodes[stepID]; ok && currentTaskNode != nil {
						currentNodeName = currentTaskNode.Name
					}
					r.vm.mu.Unlock()
					if currentNodeName == "" {
						currentNodeName = stepID
					}
					if currentNodeName != "" {
						executedNames[currentNodeName] = struct{}{}
					}

					skippedNodes := make([]string, 0, len(r.vm.TaskNodes))
					r.vm.mu.Lock()
					taskNodeCount := len(r.vm.TaskNodes)
					for nodeID, n := range r.vm.TaskNodes {
						if n != nil {
							// Skip branch condition nodes (they have IDs like "nodeId.conditionId")
							// These are routing/edge nodes, not actual executable nodes
							if strings.Contains(nodeID, ".") {
								continue
							}
							if _, ok := executedNames[n.Name]; !ok {
								skippedNodes = append(skippedNodes, n.Name)
							}
						}
					}
					r.vm.mu.Unlock()

					// Debug logging for skipped nodes calculation
					r.vm.logger.Debug("Skipped nodes calculation",
						"totalTaskNodes", taskNodeCount,
						"executedCount", len(executedNames),
						"executedNames", getMapKeys(executedNames),
						"skippedCount", len(skippedNodes),
						"skippedNodes", skippedNodes)

					if len(skippedNodes) > 0 {
						dynamicData["skippedNodes"] = skippedNodes
					}

					// Collect branch selections with condition expressions
					branchSelections := make([]map[string]interface{}, 0, 2)
					for _, st := range r.vm.ExecutionLogs {
						t := strings.ToUpper(st.GetType())
						if !strings.Contains(t, "BRANCH") {
							continue
						}
						entry := map[string]interface{}{}
						name := st.GetName()
						if name == "" {
							name = st.GetId()
						}
						entry["step"] = name
						// selected conditionId from branch output
						if br := st.GetBranch(); br != nil && br.GetData() != nil {
							if m, ok := br.GetData().AsInterface().(map[string]interface{}); ok {
								if cid, ok := m["conditionId"].(string); ok {
									entry["condition_id"] = cid
									if idx := strings.LastIndex(cid, "."); idx >= 0 && idx+1 < len(cid) {
										switch cid[idx+1:] {
										case "0":
											entry["path"] = "if"
										case "1":
											entry["path"] = "else"
										default:
											entry["path"] = cid[idx+1:]
										}
									}
								}
							}
						}
						// conditions array from config
						if st.GetConfig() != nil {
							if cfgMap, ok := st.GetConfig().AsInterface().(map[string]interface{}); ok {
								if conds, ok := cfgMap["conditions"].([]interface{}); ok {
									condList := make([]map[string]interface{}, 0, len(conds))
									for _, c := range conds {
										if cm, ok := c.(map[string]interface{}); ok {
											condList = append(condList, map[string]interface{}{
												"id":         cm["id"],
												"type":       cm["type"],
												"expression": cm["expression"],
											})
										}
									}
									if len(condList) > 0 {
										entry["conditions"] = condList
									}
								}
							}
						}
						branchSelections = append(branchSelections, entry)
					}
					if len(branchSelections) > 0 {
						dynamicData["branchSelections"] = branchSelections
					}

					// Check if we have an AI summary (from context-memory or OpenAI)
					// If so, use it as-is (pass-through) instead of overwriting with deterministic summary
					// Check for Subject, AnalysisHtml, or Body to determine if we have an AI summary
					hasAISummary := (strings.TrimSpace(summaryForClient.Subject) != "" || strings.TrimSpace(summaryForClient.AnalysisHtml) != "") && strings.TrimSpace(summaryForClient.Body) != ""

					// Compose deterministic branch/skip summary strings
					// When branches/skips are present, use the structured summary as the primary analysisHtml
					// Pass currentNodeName so BuildBranchAndSkippedSummary uses the same calculation logic
					// BUT: Skip this if we have an AI summary (context-memory) - use original response as pass-through
					if !isSingleNodeImmediate(r.vm) && !hasAISummary {
						if text, html := BuildBranchAndSkippedSummary(r.vm, currentNodeName); strings.TrimSpace(html) != "" {
							// Replace analysisHtml with the structured branch summary
							// The old analysisHtml (from Summary.Body) often duplicates this information
							dynamicData["analysisHtml"] = html
							dynamicData["branchSummaryHtml"] = html

							// Compute and set status, statusHtml, subject, and summary (deterministic)
							executedSteps := len(r.vm.ExecutionLogs)

							// Count actual skipped nodes by name (not by alternate branch paths)
							// vm.TaskNodes includes nodes on ALL branch paths, but only one path is taken
							skippedCount := len(skippedNodes) // Use actual skipped node names count

							// For summary display, use executedSteps as total if nothing was truly skipped
							totalSteps := executedSteps
							if skippedCount > 0 {
								totalSteps = executedSteps + skippedCount
							}

							failed, failedName, failedReason := findEarliestFailure(r.vm)

							// Use ExecutionResultStatus enum
							var resultStatus ExecutionResultStatus
							var statusText, statusBgColor, statusTextColor string
							if failed {
								resultStatus = ExecutionFailure
								statusText = fmt.Sprintf("but failed at the '%s' step due to %s.", safeName(failedName), firstLine(failedReason))
								statusBgColor = "#FEE2E2"   // light red
								statusTextColor = "#991B1B" // dark red
							} else if skippedCount > 0 {
								resultStatus = ExecutionPartialSuccess
								statusText = fmt.Sprintf("but %d nodes were skipped due to Branch condition.", skippedCount)
								statusBgColor = "#FEF3C7"   // light yellow
								statusTextColor = "#92400E" // dark yellow/amber
							} else {
								resultStatus = ExecutionSuccess
								statusText = "All steps completed successfully"
								statusBgColor = "#D1FAE5"   // light green
								statusTextColor = "#065F46" // dark green
							}

							// Generate status badge HTML with colors for the badge itself
							iconSvg := ""
							switch resultStatus {
							case ExecutionSuccess:
								iconSvg = `<svg width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg" style="vertical-align:middle; margin-right:6px"><circle cx="8" cy="8" r="7" fill="#10B981"/><path d="M11 6L7 10L5 8" stroke="white" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>`
							case ExecutionPartialSuccess:
								iconSvg = `<svg width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg" style="vertical-align:middle; margin-right:6px"><circle cx="8" cy="8" r="7" fill="#F59E0B"/><circle cx="8" cy="5" r="1" fill="white"/><rect x="7.5" y="7" width="1" height="4" rx="0.5" fill="white"/></svg>`
							case ExecutionFailure:
								iconSvg = `<svg width="16" height="16" viewBox="0 0 16 16" fill="none" xmlns="http://www.w3.org/2000/svg" style="vertical-align:middle; margin-right:6px"><circle cx="8" cy="8" r="7" fill="#EF4444"/><path d="M10 6L6 10M6 6L10 10" stroke="white" stroke-width="2" stroke-linecap="round"/></svg>`
							}
							statusHtml := fmt.Sprintf(
								`<div style="display:inline-block; padding:8px 16px; background-color:%s; color:%s; border-radius:8px; font-weight:500; margin:8px 0">%s%s</div>`,
								statusBgColor,
								statusTextColor,
								iconSvg,
								statusText,
							)

							workflowName := resolveWorkflowName(r.vm)
							summaryLine := fmt.Sprintf("Your workflow '%s' executed %d out of %d total steps", workflowName, executedSteps, totalSteps)

							// Update subject based on status
							var subjectStatusText string
							switch resultStatus {
							case ExecutionSuccess:
								subjectStatusText = "successfully completed"
							case ExecutionPartialSuccess:
								subjectStatusText = "partially executed"
							case ExecutionFailure:
								subjectStatusText = "failed to execute"
							}

							// Build subject with appropriate prefix
							var newSubject string
							if r.vm.IsSimulation {
								// Simulation: workflow_name status
								newSubject = fmt.Sprintf("Simulation: %s %s", workflowName, subjectStatusText)
								if r.vm.logger != nil {
									r.vm.logger.Info("Using Simulation prefix for email subject", "subject", newSubject, "isSimulation", r.vm.IsSimulation)
								}
							} else {
								// Deployed run: Run #X: workflow_name status
								if executionIndex >= 0 {
									newSubject = fmt.Sprintf("Run #%d: %s %s", executionIndex+1, workflowName, subjectStatusText) // +1 for 1-based display
									if r.vm.logger != nil {
										r.vm.logger.Info("Using Run # prefix for email subject", "subject", newSubject, "executionIndex", executionIndex, "displayIndex", executionIndex+1)
									}
								} else {
									// Fallback if execution index not available
									newSubject = fmt.Sprintf("%s %s", workflowName, subjectStatusText)
									if r.vm.logger != nil {
										r.vm.logger.Warn("Execution index not available, using basic subject format", "subject", newSubject)
									}
								}
							}

							dynamicData["statusHtml"] = statusHtml
							dynamicData["summary"] = summaryLine
							dynamicData["subject"] = newSubject

							// Set preheader from deterministic summary line (fallback to subject)
							preheader := extractPreheaderFromSummaryText(text, newSubject)
							dynamicData["preheader"] = preheader
						}
					} else if hasAISummary {
						// Use original AI summary (context-memory) as pass-through
						// Keep original subject/body/analysisHtml from context-memory
						// Use statusHtml from context-memory if available, otherwise generate locally
						if r.vm.logger != nil {
							r.vm.logger.Info("Using AI summary as pass-through", "subject", summaryForClient.Subject, "hasStatusHtml", summaryForClient.StatusHtml != "")
						}

						// Use statusHtml from context-memory if available
						if summaryForClient.StatusHtml != "" {
							dynamicData["statusHtml"] = summaryForClient.StatusHtml
						} else {
							// Fallback: Generate minimal statusHtml for email template (based on workflow success)
							failed, _, _ := findEarliestFailure(r.vm)
							if failed {
								dynamicData["statusHtml"] = StatusHtmlFailedTemplate
							} else {
								dynamicData["statusHtml"] = StatusHtmlSuccessTemplate
							}
						}

						// Use SummaryLine if available (from context-memory), otherwise extract from body
						summaryLine := summaryForClient.SummaryLine
						if summaryLine == "" {
							// Fallback: extract summary line from body or use subject
							summaryLine = summaryForClient.Subject
							if strings.Contains(summaryForClient.Body, "\n\n") {
								firstPara := strings.Split(summaryForClient.Body, "\n\n")[0]
								if len(firstPara) > 0 && len(firstPara) < 200 {
									summaryLine = firstPara
								}
							}
						}
						dynamicData["summary"] = summaryLine
					}

					// Ensure 'from' object includes a display name for better inbox rendering
					if fromObj, ok := bodyObj["from"].(map[string]interface{}); ok {
						if _, hasName := fromObj["name"]; !hasName || strings.TrimSpace(asString(fromObj["name"])) == "" {
							fromObj["name"] = "AP Studio Notification"
							bodyObj["from"] = fromObj
						}
					}

					// Provide dynamic_template_data both at top-level and per-personalization to satisfy API variants
					bodyObj["dynamic_template_data"] = dynamicData

					// Attach dynamic_template_data per-personalization; DO NOT set subject here when using template {{{subject}}}
					if pers, ok := bodyObj["personalizations"].([]interface{}); ok {
						for i := range pers {
							if p, ok := pers[i].(map[string]interface{}); ok {
								// Pass all dynamic template data (SendGrid expects snake_case)
								p["dynamic_template_data"] = dynamicData
								pers[i] = p
							}
						}
						bodyObj["personalizations"] = pers
					}

					// Remove any content array; Dynamic Templates should not include 'content'
					delete(bodyObj, "content")
				case "telegram":
					// Format summary for Telegram: concise, chat-friendly message
					telegramMsg := FormatSummaryForChannel(s, "telegram")
					bodyObj["parse_mode"] = "HTML"
					bodyObj["text"] = telegramMsg
				}
				// Use FormatAsJSON to ensure HTML characters are not escaped in the body
				body = FormatAsJSON(bodyObj)
				if r.vm.logger != nil {
					r.vm.logger.Debug("REST API injected composed summary into provider payload", "provider", provider)
				}
			} else if r.vm.logger != nil {
				r.vm.logger.Warn("REST API summarize enabled but body is not JSON, skipping injection")
			}
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
	statusSuccess := response.StatusCode() < 400
	if bodyStr != "" {
		var jsonData interface{}
		if err := json.Unmarshal(response.Body(), &jsonData); err == nil {
			bodyData = jsonData
		} else {
			bodyData = bodyStr
		}
	} else {
		// If the provider didn't return a body (common for notification providers with 202 Accepted),
		// surface the composed summary in the server response so clients have subject/body to display.
		// For single-node notification calls, derive success from the HTTP response status.
		if summaryForClient != nil {
			// Build a summary for clients based on HTTP response status
			var subj string
			var bod string
			if statusSuccess {
				subj = summaryForClient.Subject
				bod = summaryForClient.Body
			} else {
				subj = summaryForClient.Subject
				if strings.TrimSpace(summaryForClient.Body) != "" {
					bod = summaryForClient.Body
				} else {
					bod = fmt.Sprintf(
						"Smart wallet executed 1 notification step, but the provider returned HTTP %d.\n\nThe email could not be sent.",
						response.StatusCode(),
					)
				}
			}
			bodyData = map[string]interface{}{
				"subject": subj,
				"body":    bod,
			}
		} else {
			bodyData = ""
		}
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

// detectNotificationProvider returns "sendgrid" or "telegram" based on URL patterns; empty string if unknown
func detectNotificationProvider(u string) string {
	lu := strings.ToLower(u)
	// SendGrid: allow detection by path for tests using mock servers
	if strings.Contains(lu, "/v3/mail/send") || strings.Contains(lu, "/mail/send") || strings.Contains(lu, "api.sendgrid.com") && strings.Contains(lu, "/mail/send") {
		return "sendgrid"
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

// buildStyledHTMLEmail wraps a plain-text body into a simple, safe HTML layout
// suitable for email clients. It escapes the input text and preserves paragraph
// breaks by turning double newlines into <p> blocks and single newlines into <br/>.
func buildStyledHTMLEmail(subject, body string) string {
	// Escape HTML to avoid injection
	safe := html.EscapeString(body)
	// Normalize newlines
	safe = strings.ReplaceAll(safe, "\r\n", "\n")
	// Split by paragraphs (double newline)
	parts := strings.Split(safe, "\n\n")
	var paragraphs []string
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		// Convert single newlines within a paragraph to <br/>
		p = strings.ReplaceAll(p, "\n", "<br/>")
		paragraphs = append(paragraphs, "<p style=\"margin:0 0 16px 0;\">"+p+"</p>")
	}

	content := strings.Join(paragraphs, "\n")
	// Minimal, responsive-friendly light theme
	return "<!DOCTYPE html><html><head><meta charset=\"UTF-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">" +
		"<title>" + html.EscapeString(subject) + "</title>" +
		"<style>body{background:#FFFFFF;color:#1F2937;margin:0;padding:0;font-family:Arial,Helvetica,sans-serif;-webkit-font-smoothing:antialiased;}" +
		".container{max-width:640px;margin:0 auto;padding:32px 24px;}h1,h2,h3,h4,h5,h6{color:#111827;margin-top:24px;margin-bottom:12px;}a{color:#8B5CF6;text-decoration:none;}" +
		"p{margin:0 0 16px 0;line-height:1.6;}.divider{border-top:1px solid #E5E7EB;margin:24px 0;}" +
		"@media(max-width:480px){.container{padding:24px 16px;}h1,h2,h3{font-size:1.2rem;}}</style></head>" +
		"<body><div class=\"container\">" + content + "</div></body></html>"
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

// shortHex formats a hex string as 0xABCD…WXYZ for compact display. If too short, returns as-is.
func shortHex(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if strings.HasPrefix(s, "0x") {
		if len(s) > 10 { // 0x + 4 prefix + … + 4 suffix
			return s[:6] + "…" + s[len(s)-4:]
		}
		return s
	}
	if len(s) > 8 {
		return s[:4] + "…" + s[len(s)-4:]
	}
	return s
}

// extractPreheaderFromSummaryText finds the first meaningful line after the
// "Workflow Summary" heading and truncates it for email preheader usage.
func extractPreheaderFromSummaryText(text, fallback string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return fallback
	}
	lines := strings.Split(t, "\n")
	for _, ln := range lines {
		s := strings.TrimSpace(ln)
		if s == "" {
			continue
		}
		if strings.EqualFold(s, "Summary") {
			continue
		}
		// Truncate to ~180 chars to fit preheader best practices
		if len(s) > 180 {
			return s[:177] + "..."
		}
		return s
	}
	return fallback
}

// getMapKeys returns keys from a map[string]struct{} for logging
func getMapKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
