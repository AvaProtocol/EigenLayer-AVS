package taskengine

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
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
	// The defer function serves as the single source of truth for setting Success: false
	defer func() {
		s.EndAt = time.Now().UnixMilli()
		s.Success = err == nil
		if err != nil {
			s.Error = err.Error()
		}
	}()

	var log strings.Builder

	request := r.client.R().
		SetBody([]byte(processedNode.Body))

	for k, v := range processedNode.Headers {
		request = request.SetHeader(k, v)
	}

	u, err := url.Parse(processedNode.Url)
	if err != nil {
		s.Error = fmt.Sprintf("cannot parse url: %s", processedNode.Url)
		return nil, err
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

	response := string(resp.Body())

	//maybeJSON := false

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
	if err == nil {
		pbResult, _ := anypb.New(value)
		s.OutputData = &avsproto.Execution_Step_RestApi{
			RestApi: &avsproto.RestAPINode_Output{
				Data: pbResult,
			},
		}
	}

	if err != nil {
		s.Error = err.Error()
		return s, err
	} else {
		// Check HTTP status codes from the resty response
		// - 2xx (200-299): Success
		// - 3xx (300-399): Redirection (also considered successful)
		// - 4xx (400-499): Client errors
		// - 5xx (500-599): Server errors
		// Any status code outside 2xx-3xx range is considered an error
		// Status code 0 indicates a connection failure
		if resp.StatusCode() == 0 {
			err = fmt.Errorf("HTTP request failed: connection error or timeout")
			s.Error = err.Error()
			return s, err
		}
		if resp.StatusCode() < 200 || resp.StatusCode() >= 400 {
			err = fmt.Errorf("unexpected HTTP status code: %d", resp.StatusCode())
			s.Error = err.Error()
			return s, err
		}
	}

	return s, nil
}
