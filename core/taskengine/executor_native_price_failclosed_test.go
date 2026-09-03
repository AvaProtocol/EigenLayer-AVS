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

func TestBillableExecutionChainIDs(t *testing.T) {
	t.Parallel()

	write := func(id int64) *avsproto.TaskNode {
		return &avsproto.TaskNode{
			Id: "w", Name: "w", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
			TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{ChainId: id},
			}},
		}
	}
	transfer := func(id int64) *avsproto.TaskNode {
		return &avsproto.TaskNode{
			Id: "t", Name: "t", Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER,
			TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{
				Config: &avsproto.ETHTransferNode_Config{ChainId: id},
			}},
		}
	}

	got := billableExecutionChainIDs(&model.Workflow{Task: &avsproto.Task{
		Nodes: []*avsproto.TaskNode{
			write(1),
			write(999),
			write(1),
			transfer(56),
			{Id: "rest", TaskType: &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{}}},
			{
				Id: "loop", Type: avsproto.NodeType_NODE_TYPE_LOOP,
				TaskType: &avsproto.TaskNode_Loop{Loop: &avsproto.LoopNode{
					Runner: &avsproto.LoopNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{
						Config: &avsproto.ContractWriteNode_Config{ChainId: 137},
					}},
				}},
			},
			write(0),
		},
	}})
	assert.Equal(t, []int64{1, 999, 56, 137}, got)

	assert.Nil(t, billableExecutionChainIDs(nil))
	assert.Nil(t, billableExecutionChainIDs(&model.Workflow{Task: &avsproto.Task{
		Nodes: []*avsproto.TaskNode{{Id: "code", TaskType: &avsproto.TaskNode_CustomCode{CustomCode: &avsproto.CustomCodeNode{}}}},
	}}))
}

func newBillingExecutor(t *testing.T, withPriceService bool, extraChainIDs ...int64) *WorkflowExecutor {
	t.Helper()
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	aggConfig := testutil.GetAggregatorConfig()
	if aggConfig.FeeRates == nil {
		aggConfig.FeeRates = config.GetDefaultFeeRatesConfig()
	}
	aggConfig.FeeRates.ExecutionFeeUSD = 0.02
	aggConfig.FeeRates.CreditLimitUSD = 100.0
	if len(extraChainIDs) > 0 {
		aggConfig.IsGateway = true
		chains := []*config.ChainConfig{
			{ChainID: 1, Name: "ethereum", SmartWallet: aggConfig.SmartWallet},
		}
		for _, id := range extraChainIDs {
			chains = append(chains, &config.ChainConfig{
				ChainID:     id,
				Name:        "extra",
				SmartWallet: aggConfig.SmartWallet,
			})
		}
		aggConfig.Chains = chains
	}

	engine := New(db, aggConfig, nil, testutil.GetLogger())
	var ps PriceService
	if withPriceService {
		ps = &mockPriceService{}
		engine.SetPriceService(ps)
	}
	return NewExecutor(aggConfig.SmartWallet, db, testutil.GetLogger(), engine, ps)
}

func manualCustomCodeTask(owner, wallet, taskID string) *model.Workflow {
	return &model.Workflow{Task: &avsproto.Task{
		Id:                 taskID,
		Owner:              strings.ToLower(owner),
		SmartWalletAddress: wallet,
		Status:             avsproto.TaskStatus_Enabled,
		MaxExecution:       10,
		Trigger: &avsproto.TaskTrigger{
			Id:   "trigger1",
			Name: "manualTrigger",
			Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
			TriggerType: &avsproto.TaskTrigger_Manual{
				Manual: &avsproto.ManualTrigger{Config: &avsproto.ManualTrigger_Config{}},
			},
		},
		Nodes: []*avsproto.TaskNode{{
			Id: "node1", Name: "testNode", Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{CustomCode: &avsproto.CustomCodeNode{
				Config: &avsproto.CustomCodeNode_Config{Source: "return 'ok'"},
			}},
		}},
		Edges: []*avsproto.TaskEdge{{Id: "edge1", Source: "trigger1", Target: "node1"}},
	}}
}

func hyperliquidWriteTask(owner, wallet, taskID string) *model.Workflow {
	task := manualCustomCodeTask(owner, wallet, taskID)
	task.Nodes = []*avsproto.TaskNode{{
		Id: "write1", Name: "write", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
		TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{
			Config: &avsproto.ContractWriteNode_Config{
				ContractAddress: "0x0000000000000000000000000000000000000001",
				ChainId:         int64(ChainIDHyperliquidMainnet),
			},
		}},
	}}
	task.Edges = []*avsproto.TaskEdge{{Id: "edge1", Source: "trigger1", Target: "write1"}}
	return task
}

func TestExecutor_FailClosed_HyperliquidWriteOnEthDefault(t *testing.T) {
	executor := newBillingExecutor(t, true)
	task := hyperliquidWriteTask("", "", "fail-closed-hype-write")

	execution, err := executor.RunTask(task, &QueueExecutionData{
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		ExecutionID: ulid.Make().String(),
	})
	require.NoError(t, err)
	require.NotNil(t, execution)
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED, execution.Status)
	assert.Contains(t, execution.Error, "no live native USD price")
	assert.Contains(t, execution.Error, "999")
}

func TestExecutor_FailClosed_NilPriceServiceHyperliquidWrite(t *testing.T) {
	executor := newBillingExecutor(t, false)
	task := hyperliquidWriteTask("", "", "fail-closed-nil-ps")

	execution, err := executor.RunTask(task, &QueueExecutionData{
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		ExecutionID: ulid.Make().String(),
	})
	require.NoError(t, err)
	require.NotNil(t, execution)
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED, execution.Status)
	assert.Contains(t, execution.Error, "no live native USD price")
	assert.Contains(t, execution.Error, "999")
}

func TestExecutor_FailClosed_OffchainTaskDoesNotFailClosed(t *testing.T) {
	executor := newBillingExecutor(t, true)
	owner := common.HexToAddress("0xd7050816337a3f8f690f8083b5ff8019d50c0e50")
	smartWalletAddr := "0x415f09526f25d6520d471890abf0953b0505313d"
	walletAddr := common.HexToAddress(smartWalletAddr)
	StoreWallet(executor.db, int64(1), owner, &model.SmartWallet{Address: &walletAddr})

	task := manualCustomCodeTask(owner.Hex(), smartWalletAddr, "offchain-ok")
	execution, err := executor.RunTask(task, &QueueExecutionData{
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		ExecutionID: ulid.Make().String(),
	})
	require.NoError(t, err)
	require.NotNil(t, execution)
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution.Status, execution.Error)
	assert.NotContains(t, execution.Error, "no live native USD price")
}

func TestExecutor_CreditCheck_HyperliquidOutstandingFailClosed(t *testing.T) {
	executor := newBillingExecutor(t, true, int64(ChainIDHyperliquidMainnet))
	owner := common.HexToAddress("0xd7050816337a3f8f690f8083b5ff8019d50c0e50")
	smartWalletAddr := "0x415f09526f25d6520d471890abf0953b0505313d"
	walletAddr := common.HexToAddress(smartWalletAddr)
	StoreWallet(executor.db, int64(1), owner, &model.SmartWallet{Address: &walletAddr})

	require.NoError(t, executor.feeLedger.RecordValueFee(&FeeRecord{
		ExecutionID:  "prior-hype",
		TaskID:       "other",
		Owner:        owner.Hex(),
		FeeAmountWei: "1000000000000000",
		ChainID:      int64(ChainIDHyperliquidMainnet),
	}))

	task := manualCustomCodeTask(owner.Hex(), smartWalletAddr, "eth-task-hype-debt")
	execution, err := executor.RunTask(task, &QueueExecutionData{
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		ExecutionID: ulid.Make().String(),
	})
	require.NoError(t, err)
	require.NotNil(t, execution)
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED, execution.Status)
	assert.Contains(t, execution.Error, "cannot convert credit limit")
	assert.Contains(t, execution.Error, "999")
}

func TestExecutor_CreditCheck_HyperliquidZeroOutstandingDoesNotBlockEth(t *testing.T) {
	executor := newBillingExecutor(t, true, int64(ChainIDHyperliquidMainnet))
	owner := common.HexToAddress("0xd7050816337a3f8f690f8083b5ff8019d50c0e50")
	smartWalletAddr := "0x415f09526f25d6520d471890abf0953b0505313d"
	walletAddr := common.HexToAddress(smartWalletAddr)
	StoreWallet(executor.db, int64(1), owner, &model.SmartWallet{Address: &walletAddr})

	task := manualCustomCodeTask(owner.Hex(), smartWalletAddr, "eth-task-no-hype-debt")
	execution, err := executor.RunTask(task, &QueueExecutionData{
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		ExecutionID: ulid.Make().String(),
	})
	require.NoError(t, err)
	require.NotNil(t, execution)
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, execution.Status, execution.Error)
}
