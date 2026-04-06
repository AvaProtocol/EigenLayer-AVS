package taskengine

import (
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFeeBilling_CreditGating_BlocksOnOutstandingBalance verifies that the executor
// blocks execution when the user has outstanding value fees exceeding the credit limit.
//
// Flow:
//  1. First execution succeeds (no outstanding balance)
//  2. Record a value fee in the ledger (simulating post-execution billing)
//  3. Second execution is blocked with INSUFFICIENT_CREDIT
func TestFeeBilling_CreditGating_BlocksOnOutstandingBalance(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	aggConfig := testutil.GetAggregatorConfig()

	// Set credit limit to 0 (zero tolerance — block on any outstanding)
	if aggConfig.FeeRates == nil {
		aggConfig.FeeRates = config.GetDefaultFeeRatesConfig()
	}
	aggConfig.FeeRates.CreditLimitUSD = 0.0

	engine := New(db, aggConfig, nil, testutil.GetLogger())

	// Use mock price service so we don't need real RPC for conversion
	priceService := &mockPriceService{}
	engine.SetPriceService(priceService)

	executor := NewExecutor(smartWalletConfig, db, testutil.GetLogger(), engine, priceService)

	owner := common.HexToAddress("0xd7050816337a3f8f690f8083b5ff8019d50c0e50")
	smartWalletAddr := "0x415f09526f25d6520d471890abf0953b0505313d"

	// Register the smart wallet for this owner so executor wallet validation passes
	walletAddr := common.HexToAddress(smartWalletAddr)
	StoreWallet(db, owner, &model.SmartWallet{Address: &walletAddr})

	// Create a simple task with manual trigger and custom code node (no on-chain ops)
	task := &model.Task{
		Task: &avsproto.Task{
			Id:                 "test-fee-gating",
			Owner:              strings.ToLower(owner.Hex()),
			SmartWalletAddress: smartWalletAddr,
			Status:             avsproto.TaskStatus_Enabled,
			Name:               "Test Fee Gating",
			ExecutionCount:     0,
			MaxExecution:       10,
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "manualTrigger",
				Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
				TriggerType: &avsproto.TaskTrigger_Manual{
					Manual: &avsproto.ManualTrigger{
						Config: &avsproto.ManualTrigger_Config{},
					},
				},
			},
			Nodes: []*avsproto.TaskNode{
				{
					Id:   "node1",
					Name: "testNode",
					Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
					TaskType: &avsproto.TaskNode_CustomCode{
						CustomCode: &avsproto.CustomCodeNode{
							Config: &avsproto.CustomCodeNode_Config{
								Source: "return 'ok'",
							},
						},
					},
				},
			},
			Edges: []*avsproto.TaskEdge{
				{Id: "edge1", Source: "trigger1", Target: "node1"},
			},
		},
	}

	// --- Execution 1: Should succeed (no outstanding balance) ---
	queueData1 := &QueueExecutionData{
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		ExecutionID: ulid.Make().String(),
	}

	execution1, err := executor.RunTask(task, queueData1)
	require.NoError(t, err)
	require.NotNil(t, execution1)
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution1.Status,
		"First execution should succeed — no outstanding balance")

	// --- Record a value fee (simulating post-execution billing) ---
	feeRecord := &FeeRecord{
		ExecutionID:  queueData1.ExecutionID,
		TaskID:       task.Id,
		Owner:        owner.Hex(),
		Tier:         "EXECUTION_TIER_1",
		FeeAmountWei: "1000000000000000", // 0.001 ETH — any non-zero amount
	}
	err = executor.feeLedger.RecordValueFee(feeRecord)
	require.NoError(t, err)

	// Verify outstanding balance is now positive
	outstanding, err := executor.feeLedger.GetOutstandingBalance(owner)
	require.NoError(t, err)
	assert.True(t, outstanding.Sign() > 0, "Outstanding balance should be positive after recording fee")

	// --- Execution 2: Should be blocked (outstanding > credit limit of 0) ---
	queueData2 := &QueueExecutionData{
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		ExecutionID: ulid.Make().String(),
	}

	execution2, err := executor.RunTask(task, queueData2)
	require.NoError(t, err) // RunTask returns error in execution record, not as Go error
	require.NotNil(t, execution2)
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED, execution2.Status,
		"Second execution should be blocked due to outstanding balance")
	assert.Contains(t, execution2.Error, "INSUFFICIENT_CREDIT",
		"Error should indicate insufficient credit")
}

// TestFeeBilling_CreditGating_AllowsWithNoCreditLimit verifies that execution is allowed
// when credit limit is set high enough to cover outstanding fees.
func TestFeeBilling_CreditGating_AllowsWithHighCreditLimit(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	aggConfig := testutil.GetAggregatorConfig()

	// Set credit limit to $100 — high enough to allow execution
	if aggConfig.FeeRates == nil {
		aggConfig.FeeRates = config.GetDefaultFeeRatesConfig()
	}
	aggConfig.FeeRates.CreditLimitUSD = 100.0

	engine := New(db, aggConfig, nil, testutil.GetLogger())
	priceService := &mockPriceService{}
	engine.SetPriceService(priceService)

	executor := NewExecutor(smartWalletConfig, db, testutil.GetLogger(), engine, priceService)

	owner := common.HexToAddress("0xd7050816337a3f8f690f8083b5ff8019d50c0e50")
	smartWalletAddr := "0x415f09526f25d6520d471890abf0953b0505313d"

	// Register the smart wallet for this owner
	walletAddr := common.HexToAddress(smartWalletAddr)
	StoreWallet(db, owner, &model.SmartWallet{Address: &walletAddr})

	task := &model.Task{
		Task: &avsproto.Task{
			Id:                 "test-fee-high-limit",
			Owner:              strings.ToLower(owner.Hex()),
			SmartWalletAddress: smartWalletAddr,
			Status:             avsproto.TaskStatus_Enabled,
			Name:               "Test High Credit Limit",
			ExecutionCount:     0,
			MaxExecution:       10,
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "manualTrigger",
				Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
				TriggerType: &avsproto.TaskTrigger_Manual{
					Manual: &avsproto.ManualTrigger{Config: &avsproto.ManualTrigger_Config{}},
				},
			},
			Nodes: []*avsproto.TaskNode{
				{
					Id:   "node1",
					Name: "testNode",
					Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
					TaskType: &avsproto.TaskNode_CustomCode{
						CustomCode: &avsproto.CustomCodeNode{
							Config: &avsproto.CustomCodeNode_Config{Source: "return 'ok'"},
						},
					},
				},
			},
			Edges: []*avsproto.TaskEdge{
				{Id: "edge1", Source: "trigger1", Target: "node1"},
			},
		},
	}

	// Record a small outstanding fee
	err := executor.feeLedger.RecordValueFee(&FeeRecord{
		ExecutionID:  "prior_exec",
		TaskID:       task.Id,
		Owner:        owner.Hex(),
		FeeAmountWei: "1000000000000000", // 0.001 ETH (~$2.50) — under $100 limit
	})
	require.NoError(t, err)

	// Execution should still succeed — within credit limit
	queueData := &QueueExecutionData{
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		ExecutionID: ulid.Make().String(),
	}

	execution, err := executor.RunTask(task, queueData)
	require.NoError(t, err)
	require.NotNil(t, execution)
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution.Status,
		"Execution should succeed when outstanding < credit limit")
}
