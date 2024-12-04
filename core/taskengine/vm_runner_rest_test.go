package taskengine

import (
	"strings"
	"testing"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

func TestRestRequest(t *testing.T) {
	n := NewRestProrcessor()

	node := &avsproto.RestAPINode{
		Url: "https://webhook.site/4a2cb0c4-86ea-4189-b1e3-ce168f5d4840",
		Headers: map[string]string{
			"Content-type": "application/x-www-form-urlencoded",
		},
		Body:   "chat_id=123&disable_notification=true&text=%2AThis+is+a+test+format%2A",
		Method: "POST",
	}
	step, err := n.Execute("foo123", node)

	if err != nil {
		t.Errorf("expected rest node run succesfull but got error: %v", err)
	}

	if !step.Success {
		t.Errorf("expected rest node run succesfully but failed")
	}

	if !strings.Contains(step.Log, "Execute POST https://webhook.site/4a2cb0c4-86ea-4189-b1e3-ce168f5d4840 at") {
		t.Errorf("expected log contains request trace data but found no")
	}

	if step.Error != "" {
		t.Errorf("expected log contains request trace data but found no")
	}
	if !strings.Contains(step.Result, "This URL has no default content configured") {
		t.Errorf("expected step result contains the http endpoint response body")
	}
}
