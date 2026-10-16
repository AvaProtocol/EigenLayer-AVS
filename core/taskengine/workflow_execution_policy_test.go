package taskengine

import (
	"context"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStandaloneActionsRequireWorkflow(t *testing.T) {
	for _, simulated := range []bool{false, true} {
		write := &avsproto.ContractWriteNode{Config: &avsproto.ContractWriteNode_Config{IsSimulated: &simulated}}
		transfer := &avsproto.ETHTransferNode{Config: &avsproto.ETHTransferNode_Config{}}
		for name, node := range map[string]*avsproto.TaskNode{
			"write":         {TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: write}},
			"transfer":      {TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: transfer}},
			"write loop":    {TaskType: &avsproto.TaskNode_Loop{Loop: &avsproto.LoopNode{Runner: &avsproto.LoopNode_ContractWrite{ContractWrite: write}}}},
			"transfer loop": {TaskType: &avsproto.TaskNode_Loop{Loop: &avsproto.LoopNode{Runner: &avsproto.LoopNode_EthTransfer{EthTransfer: transfer}}}},
		} {
			t.Run(name+"/"+map[bool]string{false: "execute", true: "simulate"}[simulated], func(t *testing.T) {
				// An empty engine proves rejection happens before any wallet, provider,
				// or storage access. Both public RPC entry points enforce the policy.
				engine := &Engine{}
				req := &avsproto.RunNodeWithInputsReq{Node: node}
				response, err := engine.RunNodeImmediatelyRPCWithContext(context.Background(), nil, req)
				require.Nil(t, response)
				require.Equal(t, codes.InvalidArgument, status.Code(err))
				require.Contains(t, err.Error(), "complete workflow")
				response, err = engine.RunNodeImmediatelyRPCIdempotent(context.Background(), nil, req, "legacy-execute")
				require.Nil(t, response)
				require.Equal(t, codes.InvalidArgument, status.Code(err))
			})
		}
	}
}

func TestStandaloneActionRejectsCachedExecute(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	engine := &Engine{db: db}
	user := &model.User{Address: common.HexToAddress("0x1111111111111111111111111111111111111111")}
	engine.writeIdempotentResponse(idempotencyCacheKey(user.Address.Hex(), "old-action"), &avsproto.RunNodeWithInputsResp{Success: true})
	req := &avsproto.RunNodeWithInputsReq{Node: &avsproto.TaskNode{TaskType: &avsproto.TaskNode_ContractWrite{
		ContractWrite: &avsproto.ContractWriteNode{Config: &avsproto.ContractWriteNode_Config{}},
	}}}
	response, err := engine.RunNodeImmediatelyRPCIdempotent(context.Background(), user, req, "old-action")
	require.Nil(t, response)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestStandaloneOverridesRequireWorkflow(t *testing.T) {
	req := customCodeReq("return 1")
	req.Erc20Overrides = []*avsproto.ERC20StateOverride{{}}
	response, err := (&Engine{}).RunNodeImmediatelyRPCWithContext(context.Background(), nil, req)
	require.Nil(t, response)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestExecutionContextUsesWorkflowMode(t *testing.T) {
	for _, simulated := range []bool{false, true} {
		legacyFlag := !simulated
		node := &avsproto.TaskNode{Id: "action", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
			TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{IsSimulated: &legacyFlag},
			}},
		}
		vm := NewVM().SetSimulation(simulated)
		vm.TaskNodes[node.Id] = node
		step := CreateNodeExecutionStep(node.Id, node, vm)
		require.Equal(t, simulated, step.GetExecutionContext().AsInterface().(map[string]interface{})["is_simulated"])
	}
}
