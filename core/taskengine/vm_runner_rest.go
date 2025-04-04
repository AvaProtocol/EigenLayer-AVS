package taskengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

type RestProcessor struct {
	*CommonProcessor
	client *resty.Client
}

func NewRestProrcessor(vm *VM) *RestProcessor {
	client := resty.New()

	// Unique settings at Client level
	// --------------------------------
	// Enable debug mode
	// client.SetDebug(true)

	// Set client timeout as per your need
	client.SetTimeout(1 * time.Minute)

	r := RestProcessor{
		client: client,
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
	}

	return &r
}

func (r *RestProcessor) Execute(stepID string, node *avsproto.RestAPINode) (*avsproto.Execution_Step, error) {
	t0 := time.Now().UnixMilli()
	s := &avsproto.Execution_Step{
		NodeId:     stepID,
		Log:        "",
		OutputData: nil,
		Success:    true, // Start optimistically
		Error:      "",
		StartAt:    t0,
	}

	// Clone the restapi node to avoid modifying the pointer memory
	processedNode := &avsproto.RestAPINode{
		Url:     node.Url,
		Method:  node.Method,
		Body:    node.Body,
		Headers: make(map[string]string),
	}

	// Copy headers
	for k, v := range node.Headers {
		processedNode.Headers[k] = v
	}

	// Preprocess URL, body, and headers without modifying original node
	if strings.Contains(processedNode.Url, "{{") {
		processedNode.Url = r.vm.preprocessText(processedNode.Url)
	}
	if strings.Contains(processedNode.Body, "{{") {
		processedNode.Body = r.vm.preprocessText(processedNode.Body)
	}
	for headerName, headerValue := range processedNode.Headers {
		if strings.Contains(headerValue, "{{") {
			processedNode.Headers[headerName] = r.vm.preprocessText(headerValue)
		}
	}

	var err error
	defer func() {
		s.EndAt = time.Now().UnixMilli()
	}()

	var log strings.Builder

	request := r.client.R().
		SetBody([]byte(processedNode.Body))

	for k, v := range processedNode.Headers {
		request = request.SetHeader(k, v)
	}

	u, err := url.Parse(processedNode.Url)
	if err != nil {
		s.Success = false
		s.Error = fmt.Sprintf("Cannot parse URL: %s", processedNode.Url)
		return s, err
	}

	log.WriteString(fmt.Sprintf("Execute %s %s at %s", processedNode.Method, u.Hostname(), time.Now()))
	s.Log = log.String()

	var resp *resty.Response
	if strings.EqualFold(processedNode.Method, "post") {
		resp, err = request.Post(processedNode.Url)
	} else if strings.EqualFold(processedNode.Method, "get") {
		resp, err = request.Get(processedNode.Url)
	} else if strings.EqualFold(processedNode.Method, "delete") {
		resp, err = request.Delete(processedNode.Url)
	} else {
		resp, err = request.Get(processedNode.Url)
	}

	// Handle connection errors
	if err != nil {
		s.Success = false
		s.Error = "HTTP request failed: connection error, timeout, or DNS resolution failure"
		return s, fmt.Errorf("%s: %v", s.Error, err)
	}

	// Check HTTP status code - treat non-2xx/3xx as errors
	if resp.StatusCode() < 200 || resp.StatusCode() >= 400 {
		s.Success = false
		errorMsg := fmt.Sprintf("Unexpected status code: %d", resp.StatusCode())
		s.Error = errorMsg
		return s, errors.New(errorMsg)
	}

	response := string(resp.Body())

	// Attempt to detect json and auto convert to a map to use in subsequent step
	if len(response) >= 1 && (response[0] == '{' || response[0] == '[') {
		var parseData map[string]any
		if err := json.Unmarshal([]byte(response), &parseData); err == nil {
			r.SetOutputVarForStep(stepID, parseData)
		} else {
			r.SetOutputVarForStep(stepID, response)
		}
	} else {
		r.SetOutputVarForStep(stepID, response)
	}

	value, err := structpb.NewValue(r.GetOutputVar(stepID))
	if err != nil {
		s.Success = false
		s.Error = err.Error()
		return s, err
	}

	pbResult, err := anypb.New(value)
	if err != nil {
		s.Success = false
		s.Error = err.Error()
		return s, err
	}

	s.OutputData = &avsproto.Execution_Step_RestApi{
		RestApi: &avsproto.RestAPINode_Output{
			Data: pbResult,
		},
	}

	// If we reach here, everything was successful
	return s, nil
}
