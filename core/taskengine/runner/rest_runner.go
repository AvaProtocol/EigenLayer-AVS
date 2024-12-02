package runner

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/AvaProtocol/ap-avs/core/taskengine/types"
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

func (r *RestProcessor) Execute(stepID string, node *avsproto.RestAPINode) (*types.StepExecution, error) {
	s := &types.StepExecution{
		NodeID: stepID,
		Logs:   []string{},
	}

	request := r.client.R().
		SetHeader("Content-Type", "application/x-www-form-urlencoded").
		SetBody([]byte(node.Body))

	var resp *resty.Response
	var err error
	if strings.EqualFold(node.Method, "post") {
		resp, err = request.Post(node.Url)
	} else if strings.EqualFold(node.Method, "get") {
		resp, err = request.Get(node.Url)
	}

	s.Logs = append(s.Logs, fmt.Sprintf("Request %s %s at %s", node.Method, node.Url, time.Now()))
	s.Result = string(resp.Body())
	if err != nil {
		return s, err
	}

	return s, nil
}
