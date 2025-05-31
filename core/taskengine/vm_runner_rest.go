package taskengine

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/go-resty/resty/v2"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

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
	executionLogStep := &avsproto.Execution_Step{
		NodeId:     stepID,
		OutputData: nil,
		Log:        "",
		Error:      "",
		Success:    true, // Assume success
		StartAt:    t0.UnixMilli(),
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

	body = r.vm.preprocessTextWithVariableMapping(body)

	// Process headers map for template variables
	processedHeaders := make(map[string]string)
	for key, value := range headers {
		processedKey := r.vm.preprocessTextWithVariableMapping(key)
		processedValue := r.vm.preprocessTextWithVariableMapping(value)
		processedHeaders[processedKey] = processedValue
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

	// Execute request
	var response *resty.Response
	var err error
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

	if err != nil {
		errorMsg := fmt.Sprintf("HTTP request failed: connection error or timeout: %v", err)
		executionLogStep.Success = false
		executionLogStep.Error = errorMsg
		executionLogStep.EndAt = time.Now().UnixMilli()
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errorMsg))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, fmt.Errorf(errorMsg)
	}

	// Check for HTTP error status codes
	if response.StatusCode() >= 400 {
		responseBody := string(response.Body())
		errorMsg := fmt.Sprintf("unexpected HTTP status code: %d", response.StatusCode())

		// Add detailed error logging
		if r.vm.logger != nil {
			r.vm.logger.Error("REST API request failed with error status code",
				"statusCode", response.StatusCode(),
				"method", method,
				"url", url,
				"requestBody", body,
				"responseBody", responseBody,
				"responseHeaders", response.Header())
		}

		executionLogStep.Success = false
		executionLogStep.Error = errorMsg
		executionLogStep.EndAt = time.Now().UnixMilli()
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errorMsg))
		logBuilder.WriteString(fmt.Sprintf("Response body: %s\n", responseBody))
		logBuilder.WriteString(fmt.Sprintf("Response headers: %v\n", response.Header()))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, fmt.Errorf(errorMsg)
	}

	logBuilder.WriteString(fmt.Sprintf("Request completed successfully with status: %d\n", response.StatusCode()))
	logBuilder.WriteString(fmt.Sprintf("Response headers: %v\n", response.Header()))

	// Process response
	responseData := r.processResponse(response)

	// Create protobuf output
	anyData, err := structpb.NewValue(responseData)
	if err != nil {
		errorMsg := fmt.Sprintf("failed to convert response to protobuf: %v", err)
		executionLogStep.Success = false
		executionLogStep.Error = errorMsg
		executionLogStep.EndAt = time.Now().UnixMilli()
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errorMsg))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, fmt.Errorf(errorMsg)
	}

	anyProto, err := anypb.New(anyData)
	if err != nil {
		errorMsg := fmt.Sprintf("failed to create Any proto: %v", err)
		executionLogStep.Success = false
		executionLogStep.Error = errorMsg
		executionLogStep.EndAt = time.Now().UnixMilli()
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errorMsg))
		executionLogStep.Log = logBuilder.String()
		return executionLogStep, fmt.Errorf(errorMsg)
	}

	// Set output data
	restOutput := &avsproto.RestAPINode_Output{
		Data: anyProto,
	}
	executionLogStep.OutputData = &avsproto.Execution_Step_RestApi{
		RestApi: restOutput,
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

// processResponse converts HTTP response to structured data
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
