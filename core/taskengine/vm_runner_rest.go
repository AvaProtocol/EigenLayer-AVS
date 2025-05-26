package taskengine

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	logBuilder.WriteString(fmt.Sprintf("URL: %s, Method: %s\n", node.Url, node.Method))

	// Preprocess URL, Body, and Headers using VM variables
	processedURL := r.vm.preprocessText(node.Url)
	processedBodyStr := r.vm.preprocessText(node.Body)
	processedHeaders := make(map[string]string)
	for key, valTpl := range node.Headers {
		processedHeaders[r.vm.preprocessText(key)] = r.vm.preprocessText(valTpl)
	}

	logBuilder.WriteString(fmt.Sprintf("Processed URL: %s\n", processedURL))
	if processedBodyStr != "" {
		logBuilder.WriteString(fmt.Sprintf("Processed Body: %s\n", processedBodyStr))
	}
	if len(processedHeaders) > 0 {
		logBuilder.WriteString(fmt.Sprintf("Processed Headers: %v\n", processedHeaders))
	}

	req := r.HttpClient.R()
	req.SetHeaders(processedHeaders)

	// Attempt to parse body as JSON. If not, send as raw string.
	var bodyData interface{}
	if processedBodyStr != "" {
		if err := json.Unmarshal([]byte(processedBodyStr), &bodyData); err == nil {
			req.SetBody(bodyData)
			logBuilder.WriteString("Body sent as parsed JSON.\n")
		} else {
			req.SetBody(processedBodyStr) // Send as raw string if not valid JSON
			logBuilder.WriteString("Body sent as raw string (not valid JSON).\n")
		}
	}

	var resp *resty.Response
	var err error

	switch strings.ToUpper(node.Method) {
	case http.MethodGet:
		resp, err = req.Get(processedURL)
	case http.MethodPost:
		resp, err = req.Post(processedURL)
	case http.MethodPut:
		resp, err = req.Put(processedURL)
	case http.MethodDelete:
		resp, err = req.Delete(processedURL)
	case http.MethodPatch:
		resp, err = req.Patch(processedURL)
	case http.MethodHead:
		resp, err = req.Head(processedURL)
	case http.MethodOptions:
		resp, err = req.Options(processedURL)
	default:
		err = fmt.Errorf("unsupported HTTP method: %s", node.Method)
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
	headers := make(map[string]interface{})
	for key, values := range resp.Header() {
		if len(values) == 1 {
			headers[key] = values[0] // Single value as string
		} else {
			headers[key] = values // Multiple values as array
		}
	}

	// Store the full response details (status, headers, body) in a structured way if needed,
	// or just the body as is common.
	fullResponseOutput := map[string]interface{}{
		"statusCode": resp.StatusCode(),
		"status":     resp.Status(),
		"headers":    headers,         // Converted headers
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
