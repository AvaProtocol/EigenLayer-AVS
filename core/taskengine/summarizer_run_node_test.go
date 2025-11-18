package taskengine

import (
	"strings"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestComposeSummary_RunNodeRestSubjectAndBody(t *testing.T) {
	vm := NewVM()

	vm.AddVar("aa_sender", "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f")
	vm.mu.Lock()
	vm.vars["settings"] = map[string]interface{}{
		"name":   "Automated Stop-Loss on Uniswap V3",
		"chain":  "Sepolia",
		"runner": "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
	}
	vm.TaskNodes["email_report_success"] = &avsproto.TaskNode{
		Id:   "email_report_success",
		Name: "email_report_success",
		Type: avsproto.NodeType_NODE_TYPE_REST_API,
	}
	vm.mu.Unlock()

	restPayload := map[string]interface{}{
		"status":     202,
		"statusText": "Accepted",
		"url":        "https://api.sendgrid.com/v3/mail/send",
	}
	restValue, err := structpb.NewValue(restPayload)
	if err != nil {
		t.Fatalf("failed to build rest payload: %v", err)
	}

	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "email_report_success",
			Name:    "email_report_success",
			Success: true,
			Type:    avsproto.NodeType_NODE_TYPE_REST_API.String(),
			OutputData: &avsproto.Execution_Step_RestApi{
				RestApi: &avsproto.RestAPINode_Output{
					Data: restValue,
				},
			},
		},
	}

	summary := ComposeSummary(vm, "email_report_success")

	expectedSubject := "Run Node: Automated Stop-Loss on Uniswap V3 succeeded"
	if summary.Subject != expectedSubject {
		t.Fatalf("expected subject %q, got %q", expectedSubject, summary.Subject)
	}

	if !strings.Contains(summary.Body, "REST API node") {
		t.Fatalf("expected body to mention REST API node, got: %q", summary.Body)
	}

	if !strings.Contains(summary.Body, "HTTP 202") {
		t.Fatalf("expected body to mention HTTP status, got: %q", summary.Body)
	}

	if !strings.Contains(summary.Body, "SendGrid") {
		t.Fatalf("expected body to mention SendGrid provider, got: %q", summary.Body)
	}
}
