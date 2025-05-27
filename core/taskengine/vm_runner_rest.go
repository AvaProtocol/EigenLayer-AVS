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

	// Get configuration from Config message (static configuration) and input variables (runtime)
	// Input variables take precedence over Config
	var url, method, body string
	var headers map[string]string

	// Start with Config values (if available)
	if node.Config != nil {
		url = node.Config.Url
		method = node.Config.Method
		body = node.Config.Body
		headers = node.Config.Headers
	}

	// Override with input variables if available (for manual execution)
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
	r.vm.mu.Unlock()

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
	url = r.vm.preprocessText(url)
	body = r.vm.preprocessText(body)

	// Process headers map for template variables
	processedHeaders := make(map[string]string)
	for key, value := range headers {
		processedHeaders[key] = r.vm.preprocessText(value)
	}

	logBuilder.WriteString(fmt.Sprintf("URL: %s, Method: %s\n", url, method))
	logBuilder.WriteString(fmt.Sprintf("Body: %s\n", body))

	// Create the HTTP request
	req := r.HttpClient.R()

	// Set headers
	for key, value := range processedHeaders {
		req.SetHeader(key, value)
		logBuilder.WriteString(fmt.Sprintf("Header: %s = %s\n", key, value))
	}

	// Set body if provided
	if body != "" {
		req.SetBody(body)
	}

	logBuilder.WriteString(fmt.Sprintf("Processed URL: %s\n", url))
	logBuilder.WriteString(fmt.Sprintf("Method: %s\n", method))

	// Execute the request
	var resp *resty.Response
	var err error

	switch strings.ToUpper(method) {
	case "GET":
		resp, err = req.Get(url)
	case "POST":
		resp, err = req.Post(url)
	case "PUT":
		resp, err = req.Put(url)
	case "DELETE":
		resp, err = req.Delete(url)
	case "PATCH":
		resp, err = req.Patch(url)
	case "HEAD":
		resp, err = req.Head(url)
	case "OPTIONS":
		resp, err = req.Options(url)
	default:
		err = fmt.Errorf("unsupported HTTP method: %s", method)
	}

	if err != nil {
		errMsg := fmt.Sprintf("HTTP request failed: connection error or timeout")
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errMsg))
		executionLogStep.Error = errMsg
		executionLogStep.Success = false
		executionLogStep.Log = logBuilder.String()
		executionLogStep.EndAt = time.Now().UnixMilli()
		return executionLogStep, fmt.Errorf(errMsg)
	}

	logBuilder.WriteString(fmt.Sprintf("Response Status: %s, Code: %d\n", resp.Status(), resp.StatusCode()))
	logBuilder.WriteString(fmt.Sprintf("Response Body: %s\n", string(resp.Body())))

	// Attempt to parse response body as JSON
	var responseJSON map[string]interface{}
	jsonParseError := json.Unmarshal(resp.Body(), &responseJSON)

	var resultForOutput interface{}
	if jsonParseError == nil {
		resultForOutput = responseJSON
		logBuilder.WriteString("Response body successfully parsed as JSON.\n")
	} else {
		resultForOutput = string(resp.Body()) // Store as string if not JSON
		logBuilder.WriteString(fmt.Sprintf("Response body is not valid JSON, stored as string. Parse error: %v\n", jsonParseError))
	}

	// Convert http.Header (map[string][]string) to a protobuf-compatible format
	responseHeaders := make(map[string]interface{})
	for key, values := range resp.Header() {
		if len(values) == 1 {
			responseHeaders[key] = values[0] // Single value as string
		} else {
			responseHeaders[key] = values // Multiple values as array
		}
	}

	// Store the full response details (status, headers, body) in a structured way if needed,
	// or just the body as is common.
	fullResponseOutput := map[string]interface{}{
		"statusCode": resp.StatusCode(),
		"status":     resp.Status(),
		"headers":    responseHeaders, // Converted headers
		"body":       resultForOutput, // Parsed JSON or raw string
	}

	outputProtoStruct, err := structpb.NewValue(fullResponseOutput)
	if err != nil {
		errMsg := fmt.Sprintf("error converting REST API response to protobuf Value: %v", err)
		logBuilder.WriteString(fmt.Sprintf("Error: %s\n", errMsg))
		executionLogStep.Error = errMsg
		executionLogStep.Success = false // Response obtained, but conversion for logging/output failed
		executionLogStep.Log = logBuilder.String()
		executionLogStep.EndAt = time.Now().UnixMilli()
		return executionLogStep, fmt.Errorf(errMsg)
	}

	anyOutput, err := anypb.New(outputProtoStruct)
	if err != nil {
		executionLogStep.Error = fmt.Sprintf("failed to marshal output to Any: %v", err)
		executionLogStep.Success = false
		executionLogStep.Log = logBuilder.String()
		executionLogStep.EndAt = time.Now().UnixMilli()
		return executionLogStep, fmt.Errorf("failed to marshal output to Any: %w", err)
	}

	executionLogStep.OutputData = &avsproto.Execution_Step_RestApi{
		RestApi: &avsproto.RestAPINode_Output{
			Data: anyOutput,
		},
	}

	r.SetOutputVarForStep(stepID, fullResponseOutput) // Store the map[string]interface{} result in VM vars

	if resp.IsError() { // Consider HTTP status >= 400 an error for Success flag
		errMsg := fmt.Sprintf("unexpected HTTP status code: %d", resp.StatusCode())
		executionLogStep.Success = false
		executionLogStep.Error = errMsg
		executionLogStep.Log = logBuilder.String()
		executionLogStep.EndAt = time.Now().UnixMilli()
		return executionLogStep, fmt.Errorf(errMsg)
	}

	executionLogStep.Log = logBuilder.String()
	executionLogStep.EndAt = time.Now().UnixMilli()
	return executionLogStep, nil
}
