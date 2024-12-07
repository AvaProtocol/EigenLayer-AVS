package taskengine

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

type RestProcessor struct {
	client *resty.Client
}

func NewRestProrcessor() *RestProcessor {
	client := resty.New()

	// Unique settings at Client level
	// --------------------------------
	// Enable debug mode
	// client.SetDebug(true)

	// Set client timeout as per your need
	client.SetTimeout(1 * time.Minute)

	r := RestProcessor{
		client: client,
	}

	return &r
}

func (r *RestProcessor) Execute(stepID string, node *avsproto.RestAPINode) (*avsproto.Execution_Step, error) {
	s := &avsproto.Execution_Step{
		NodeId:     stepID,
		Log:        "",
		OutputData: "",
		Success:    true,
		Error:      "",
	}

	var log strings.Builder

	request := r.client.R().
		SetBody([]byte(node.Body))

	for k, v := range node.Headers {
		request = request.SetHeader(k, v)
	}

	var resp *resty.Response
	var err error
	if strings.EqualFold(node.Method, "post") {
		resp, err = request.Post(node.Url)
	} else if strings.EqualFold(node.Method, "get") {
		resp, err = request.Get(node.Url)
	} else if strings.EqualFold(node.Method, "delete") {
		resp, err = request.Delete(node.Url)
	}

	u, err := url.Parse(node.Url)
	if err != nil {
		return nil, err
	}

	log.WriteString(fmt.Sprintf("Execute %s %s at %s", node.Method, u.Hostname(), time.Now()))
	s.Log = log.String()
	s.OutputData = string(resp.Body())
	if err != nil {
		s.Success = false
		s.Error = err.Error()
		return s, err
	}

	return s, nil
}
