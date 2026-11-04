package taskengine

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func ownerNativeNode() *avsproto.TaskNode {
	return &avsproto.TaskNode{Id: "native", Name: "native", Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER, TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{Config: &avsproto.ETHTransferNode_Config{ChainId: 11155111, Destination: "{{settings.destination}}", Amount: "{{amounts.data.native}}", ReserveGas: true}}}}
}

func TestOwnerNativeWorkflowCollectsOneAtomicBatchAndSubmitsSameHash(t *testing.T) {
	for _, nativeOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "mixed tokens", true: "ETH only"}[nativeOnly], func(t *testing.T) {
			n, user, key, backend := ownerEngineFixture(t)
			trigger, nodes, edges, input := ownerWorkflowFixture()
			nodes[0].GetCustomCode().Config.Source = "return {usdc:'1000000',weth:'20000000000000000',native:'1000000000000000000'};"
			if nativeOnly {
				nodes = nodes[:1]
				edges = edges[:1]
				edges = append(edges, &avsproto.TaskEdge{Id: "nativeEdge", Source: "amounts", Target: "native"})
			} else {
				edges = append(edges, &avsproto.TaskEdge{Id: "nativeEdge", Source: "transfer", Target: "native"})
			}
			nodes = append(nodes, ownerNativeNode())
			input["settings"].(map[string]interface{})["runner"] = user.SmartAccountAddress.Hex()
			p, err := n.PrepareOwnerWorkflow(context.Background(), user, trigger, nodes, edges, input, 11155111)
			require.NoError(t, err)
			require.Equal(t, "native", p.WriteNodeID)
			expectedCalls := 3
			if nativeOnly {
				expectedCalls = 1
			}
			require.Len(t, p.Calls, expectedCalls)
			_, values, data, err := aa.UnpackExecuteCalldata(p.Operation.CallData)
			require.NoError(t, err)
			require.Equal(t, new(big.Int).Sub(big.NewInt(1_000_000_000_000_000_000), preset.MaxOwnerGasCost(p.Operation)), values[len(values)-1])
			require.Empty(t, data[len(data)-1])
			for _, step := range p.Execution.Steps {
				require.NotEqual(t, "transfer", step.Id)
				require.NotEqual(t, "native", step.Id)
			}
			before := append([]byte(nil), p.Operation.CallData...)
			raw, err := n.db.GetKey(ownerExecutionKey(user.Address, p.Execution.Id))
			require.NoError(t, err)
			var record ownerExecutionRecord
			require.NoError(t, json.Unmarshal(raw, &record))
			require.NotNil(t, record.Batch)
			require.True(t, record.Batch.ReserveNativeGas)
			require.Equal(t, "1000000000000000000", record.Batch.Calls[len(record.Batch.Calls)-1].Value.String())
			require.Empty(t, record.Definition)
			require.Empty(t, record.InputJSON)
			resolved, err := record.Batch.Resolve(p.Operation)
			require.NoError(t, err)
			packed, err := aa.PackExecuteBatchMAv2(resolved)
			require.NoError(t, err)
			require.Equal(t, before, packed)
			sig := signOwnerFixture(t, p, key)
			result, err := n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
			require.NoError(t, err)
			require.Equal(t, p.UserOpHash, result.UserOpHash)
			require.Equal(t, before, backend.signed.CallData)
			_, err = n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
			require.NoError(t, err)
			require.EqualValues(t, 1, backend.prepared.Load())
			require.EqualValues(t, 1, backend.sent.Load())
		})
	}
}

func TestOwnerNativeAmountRejectsLeadingPlus(t *testing.T) {
	n, user, _, _ := ownerEngineFixture(t)
	trigger, nodes, edges, input := ownerWorkflowFixture()
	native := ownerNativeNode()
	native.GetEthTransfer().Config.Amount = "+1000000000000000000"
	nodes = append(nodes, native)
	edges = append(edges, &avsproto.TaskEdge{Id: "nativeEdge", Source: "transfer", Target: "native"})
	_, err := n.PrepareOwnerWorkflow(context.Background(), user, trigger, nodes, edges, input, 11155111)
	require.Error(t, err)
}

func TestOwnerReserveGasCannotBeStoredAsSessionWorkflow(t *testing.T) {
	for _, loop := range []bool{false, true} {
		t.Run(map[bool]string{false: "transfer", true: "loop runner"}[loop], func(t *testing.T) {
			n, user, _, _ := ownerEngineFixture(t)
			native := ownerNativeNode()
			node := native
			if loop {
				node = &avsproto.TaskNode{Id: "loop", Type: avsproto.NodeType_NODE_TYPE_LOOP, TaskType: &avsproto.TaskNode_Loop{Loop: &avsproto.LoopNode{Runner: &avsproto.LoopNode_EthTransfer{EthTransfer: native.GetEthTransfer()}}}}
			}
			_, err := n.CreateWorkflow(user, &avsproto.CreateTaskReq{Nodes: []*avsproto.TaskNode{node}})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.ErrorContains(t, err, "reserveGas")
			native.GetEthTransfer().Config.ReserveGas = false
			require.NoError(t, rejectOwnerReserveGas([]*avsproto.TaskNode{nil, node}))
		})
	}
}

func TestOwnerNativeGraphRejectsUnorderedOrUnsupportedWrites(t *testing.T) {
	for _, scenario := range []string{"disconnected", "nonterminal", "duplicate", "wrong chain"} {
		t.Run(scenario, func(t *testing.T) {
			trigger, nodes, edges, _ := ownerWorkflowFixture()
			native := ownerNativeNode()
			nodes = append(nodes, native)
			edges = append(edges, &avsproto.TaskEdge{Id: "nativeEdge", Source: "transfer", Target: "native"})
			switch scenario {
			case "disconnected":
				edges[len(edges)-1].Source = "amounts"
			case "nonterminal":
				edges = append(edges, &avsproto.TaskEdge{Id: "after", Source: "native", Target: "amounts"})
			case "duplicate":
				extra := ownerNativeNode()
				extra.Id = "native2"
				nodes = append(nodes, extra)
			case "wrong chain":
				native.GetEthTransfer().Config.ChainId = 8453
			}
			_, err := validateOwnerGraph(trigger, nodes, edges, 11155111)
			require.Error(t, err)
		})
	}
}
