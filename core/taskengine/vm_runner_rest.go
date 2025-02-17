package taskengine

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

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
	t0 := time.Now().Unix()
	s := &avsproto.Execution_Step{
		NodeId:     stepID,
		Log:        "",
		OutputData: "",
		Success:    true,
		Error:      "",
		StartAt:    t0,
	}

	// Clone the restapi node to 
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
		s.EndAt = time.Now().Unix()
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

	fmt.Println("post to url", processedNode.Url, "with body", processedNode.Body)

	u, err := url.Parse(processedNode.Url)
	if err != nil {
		s.Error = fmt.Sprintf("cannot parse url: %s", processedNode.Url)
		return nil, err
	}

	log.WriteString(fmt.Sprintf("Execute %s %s at %s", processedNode.Method, u.Hostname(), time.Now()))
	s.Log = log.String()

	s.OutputData = string(resp.Body())

	// Attempt to detect json and auto convert to a map to use in subsequent step
	if len(s.OutputData) >= 1 && (s.OutputData[0] == '{' || s.OutputData[0] == '[') {
		var parseData map[string]any
		if err := json.Unmarshal([]byte(s.OutputData), &parseData); err == nil {
			r.SetOutputVarForStep(stepID, parseData)
		} else {
			r.SetOutputVarForStep(stepID, s.OutputData)
		}
	} else {
		r.SetOutputVarForStep(stepID, s.OutputData)
	}

	if err != nil {
		s.Success = false
		s.Error = err.Error()
		return s, err
	}

	return s, nil
}
