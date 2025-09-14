package taskengine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestGetExecution(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	// Get a wallet for the user to derive the correct smart wallet address
	walletResp, err := n.GetWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "12345",
	})
	if err != nil {
		t.Fatalf("Failed to get wallet: %v", err)
	}
	smartWalletAddress := walletResp.Address

	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	tr1.SmartWalletAddress = smartWalletAddress
	result, _ := n.CreateTask(testutil.TestUser1(), tr1)

	resultTrigger, err := n.TriggerTask(testutil.TestUser1(), &avsproto.TriggerTaskReq{
		TaskId:      result.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				Data: &structpb.Value{
					Kind: &structpb.Value_NumberValue{
						NumberValue: 101,
					},
				},
			},
		},
		IsBlocking: true,
	})

	if err != nil {
		t.Errorf("expected first trigger to succeed but got error: %v", err)
		return
	}

	if resultTrigger == nil {
		t.Errorf("resultTrigger is nil")
		return
	}

	execution, err := n.GetExecution(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})

	if err != nil {
		t.Errorf("failed to get execution: %v", err)
		return
	}

	if execution == nil {
		t.Errorf("execution is nil")
		return
	}

	if len(execution.Steps) != 2 {
		t.Errorf("Expected 2 steps (trigger + node), but got %d", len(execution.Steps))
		return
	}

	triggerStep := execution.Steps[0]
	if triggerStep.Type != avsproto.TriggerType_TRIGGER_TYPE_BLOCK.String() {
		t.Errorf("First step should be trigger step, but got type: %s", triggerStep.Type)
	}

	if execution.Steps[1].Id != "ping1" {
		t.Errorf("wrong node id in execution log, expected ping1 but got %s", execution.Steps[1].Id)
	}

	step := execution.Steps[1]
	if step.GetRestApi() == nil {
		t.Errorf("RestApi data is nil")
		return
	}

	responseData := gow.ValueToMap(step.GetRestApi().Data)
	if responseData == nil {
		t.Errorf("Failed to convert response data to map")
		return
	}

	var bodyContent string
	if bodyStr, ok := responseData["data"].(string); ok {
		bodyContent = bodyStr
	} else if bodyMap, ok := responseData["data"].(map[string]interface{}); ok {
		if bodyBytes, err := json.Marshal(bodyMap); err == nil {
			bodyContent = string(bodyBytes)
		} else {
			t.Errorf("Failed to marshal body map to string: %v", err)
			return
		}
	} else {
		t.Errorf("Response data is neither string nor map, got type: %T", responseData["data"])
		return
	}

	if !strings.Contains(bodyContent, "mock-api.ap-aggregator.local") {
		maxLen := 100
		if len(bodyContent) < maxLen {
			maxLen = len(bodyContent)
		}
		t.Errorf("Invalid output data. Expected body to contain 'mock-api.ap-aggregator.local' but got: %s", bodyContent[:maxLen]+"...")
	}

	executionStatus, err := n.GetExecutionStatus(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})
	if err != nil {
		t.Fatalf("Error getting execution status after processing: %v", err)
	}

	if executionStatus.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS {
		t.Errorf("expected status to be success but got %v", executionStatus.Status)
	}
}

func TestTriggerSync(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	// Get a wallet for the user to derive the correct smart wallet address
	walletResp, err := n.GetWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "12345",
	})
	if err != nil {
		t.Fatalf("Failed to get wallet: %v", err)
	}
	smartWalletAddress := walletResp.Address

	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	tr1.SmartWalletAddress = smartWalletAddress
	result, _ := n.CreateTask(testutil.TestUser1(), tr1)

	resultTrigger, err := n.TriggerTask(testutil.TestUser1(), &avsproto.TriggerTaskReq{
		TaskId:      result.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				Data: &structpb.Value{
					Kind: &structpb.Value_NumberValue{
						NumberValue: 101,
					},
				},
			},
		},
		IsBlocking: true,
	})

	if err != nil {
		t.Errorf("expected first trigger to succeed but got error: %v", err)
		return
	}

	if resultTrigger == nil {
		t.Errorf("resultTrigger is nil")
		return
	}

	execution, err := n.GetExecution(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})

	if err != nil {
		t.Errorf("failed to get execution: %v", err)
		return
	}

	if execution == nil {
		t.Errorf("execution is nil")
		return
	}

	if len(execution.Steps) != 2 {
		t.Errorf("Expected 2 steps (trigger + node), but got %d", len(execution.Steps))
		return
	}

	triggerStep := execution.Steps[0]
	if triggerStep.Type != avsproto.TriggerType_TRIGGER_TYPE_BLOCK.String() {
		t.Errorf("First step should be trigger step, but got type: %s", triggerStep.Type)
	}

	if execution.Steps[1].Id != "ping1" {
		t.Errorf("wrong node id in execution log, expected ping1 but got %s", execution.Steps[1].Id)
	}

	step := execution.Steps[1]
	if step.GetRestApi() == nil {
		t.Errorf("RestApi data is nil")
		return
	}

	responseData := gow.ValueToMap(step.GetRestApi().Data)
	if responseData == nil {
		t.Errorf("Failed to convert response data to map")
		return
	}

	var bodyContent string
	if bodyStr, ok := responseData["data"].(string); ok {
		bodyContent = bodyStr
	} else if bodyMap, ok := responseData["data"].(map[string]interface{}); ok {
		if bodyBytes, err := json.Marshal(bodyMap); err == nil {
			bodyContent = string(bodyBytes)
		} else {
			t.Errorf("Failed to marshal body map to string: %v", err)
			return
		}
	} else {
		t.Errorf("Response data is neither string nor map, got type: %T", responseData["data"])
		return
	}

	if !strings.Contains(bodyContent, "mock-api.ap-aggregator.local") {
		maxLen := 100
		if len(bodyContent) < maxLen {
			maxLen = len(bodyContent)
		}
		t.Errorf("Invalid output data. Expected body to contain 'mock-api.ap-aggregator.local' but got: %s", bodyContent[:maxLen]+"...")
	}

	executionStatus, err := n.GetExecutionStatus(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})
	if err != nil {
		t.Fatalf("Error getting execution status after processing: %v", err)
	}

	if executionStatus.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS {
		t.Errorf("expected status to be success but got %v", executionStatus.Status)
	}
}

func TestTriggerAsync(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	// Get a wallet for the user to derive the correct smart wallet address
	walletResp, err := n.GetWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "12345",
	})
	if err != nil {
		t.Fatalf("Failed to get wallet: %v", err)
	}
	smartWalletAddress := walletResp.Address

	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	tr1.SmartWalletAddress = smartWalletAddress
	result, _ := n.CreateTask(testutil.TestUser1(), tr1)

	resultTrigger, err := n.TriggerTask(testutil.TestUser1(), &avsproto.TriggerTaskReq{
		TaskId:      result.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				Data: &structpb.Value{
					Kind: &structpb.Value_NumberValue{
						NumberValue: 101,
					},
				},
			},
		},
		IsBlocking: true,
	})

	if err != nil {
		t.Errorf("expected first trigger to succeed but got error: %v", err)
		return
	}

	if resultTrigger == nil {
		t.Errorf("resultTrigger is nil")
		return
	}

	execution, err := n.GetExecution(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})

	if err != nil {
		t.Errorf("failed to get execution: %v", err)
		return
	}

	if execution == nil {
		t.Errorf("execution is nil")
		return
	}

	if len(execution.Steps) != 2 {
		t.Errorf("Expected 2 steps (trigger + node), but got %d", len(execution.Steps))
		return
	}

	triggerStep := execution.Steps[0]
	if triggerStep.Type != avsproto.TriggerType_TRIGGER_TYPE_BLOCK.String() {
		t.Errorf("First step should be trigger step, but got type: %s", triggerStep.Type)
	}

	if execution.Steps[1].Id != "ping1" {
		t.Errorf("wrong node id in execution log, expected ping1 but got %s", execution.Steps[1].Id)
	}

	step := execution.Steps[1]
	if step.GetRestApi() == nil {
		t.Errorf("RestApi data is nil")
		return
	}

	responseData := gow.ValueToMap(step.GetRestApi().Data)
	if responseData == nil {
		t.Errorf("Failed to convert response data to map")
		return
	}

	var bodyContent string
	if bodyStr, ok := responseData["data"].(string); ok {
		bodyContent = bodyStr
	} else if bodyMap, ok := responseData["data"].(map[string]interface{}); ok {
		if bodyBytes, err := json.Marshal(bodyMap); err == nil {
			bodyContent = string(bodyBytes)
		} else {
			t.Errorf("Failed to marshal body map to string: %v", err)
			return
		}
	} else {
		t.Errorf("Response data is neither string nor map, got type: %T", responseData["data"])
		return
	}

	if !strings.Contains(bodyContent, "mock-api.ap-aggregator.local") {
		maxLen := 100
		if len(bodyContent) < maxLen {
			maxLen = len(bodyContent)
		}
		t.Errorf("Invalid output data. Expected body to contain 'mock-api.ap-aggregator.local' but got: %s", bodyContent[:maxLen]+"...")
	}

	executionStatus, err := n.GetExecutionStatus(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})
	if err != nil {
		t.Fatalf("Error getting execution status after processing: %v", err)
	}

	if executionStatus.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS {
		t.Errorf("expected status to be success but got %v", executionStatus.Status)
	}
}

func TestTriggerCompletedTaskReturnError(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	// Get a wallet for the user to derive the correct smart wallet address
	walletResp, err := n.GetWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "12345",
	})
	if err != nil {
		t.Fatalf("Failed to get wallet: %v", err)
	}
	smartWalletAddress := walletResp.Address

	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	tr1.MaxExecution = 1
	tr1.SmartWalletAddress = smartWalletAddress
	result, _ := n.CreateTask(testutil.TestUser1(), tr1)

	resultTrigger, err := n.TriggerTask(testutil.TestUser1(), &avsproto.TriggerTaskReq{
		TaskId:      result.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				Data: &structpb.Value{
					Kind: &structpb.Value_NumberValue{
						NumberValue: 101,
					},
				},
			},
		},
		IsBlocking: true,
	})

	if err != nil || resultTrigger == nil {
		t.Errorf("expected first trigger to succeed but got error: %v", err)
		return
	}

	resultTrigger, err = n.TriggerTask(testutil.TestUser1(), &avsproto.TriggerTaskReq{
		TaskId:      result.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				Data: &structpb.Value{
					Kind: &structpb.Value_NumberValue{
						NumberValue: 101,
					},
				},
			},
		},
		IsBlocking: true,
	})

	if err == nil || resultTrigger != nil {
		t.Errorf("expect trigger error but succeed")
	}
}
