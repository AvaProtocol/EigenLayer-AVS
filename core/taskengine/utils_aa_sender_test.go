package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
)

func TestTaskNodesRequireAASender_DirectEthTransfer(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "n1",
			Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER,
			TaskType: &avsproto.TaskNode_EthTransfer{
				EthTransfer: &avsproto.ETHTransferNode{
					Config: &avsproto.ETHTransferNode_Config{
						Destination: "0x1234",
						Amount:      "1000",
					},
				},
			},
		},
	}
	assert.True(t, taskNodesRequireAASender(nodes))
}

func TestTaskNodesRequireAASender_DirectContractWrite(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "n1",
			Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
			TaskType: &avsproto.TaskNode_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode{
					Config: &avsproto.ContractWriteNode_Config{
						ContractAddress: "0x1234",
					},
				},
			},
		},
	}
	assert.True(t, taskNodesRequireAASender(nodes))
}

func TestTaskNodesRequireAASender_LoopWithEthTransferRunner(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "loop1",
			Type: avsproto.NodeType_NODE_TYPE_LOOP,
			TaskType: &avsproto.TaskNode_Loop{
				Loop: &avsproto.LoopNode{
					Config: &avsproto.LoopNode_Config{
						InputVariable:    "{{recipients}}",
						IterVal:          "value",
						IterKey:          "index",
						IterationTimeout: 60,
					},
					Runner: &avsproto.LoopNode_EthTransfer{
						EthTransfer: &avsproto.ETHTransferNode{
							Config: &avsproto.ETHTransferNode_Config{
								Destination: "{{value}}",
								Amount:      "1000",
							},
						},
					},
				},
			},
		},
	}
	assert.True(t, taskNodesRequireAASender(nodes))
}

func TestTaskNodesRequireAASender_LoopWithContractWriteRunner(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "loop1",
			Type: avsproto.NodeType_NODE_TYPE_LOOP,
			TaskType: &avsproto.TaskNode_Loop{
				Loop: &avsproto.LoopNode{
					Config: &avsproto.LoopNode_Config{
						InputVariable:    "{{items}}",
						IterVal:          "value",
						IterKey:          "index",
						IterationTimeout: 30,
					},
					Runner: &avsproto.LoopNode_ContractWrite{
						ContractWrite: &avsproto.ContractWriteNode{
							Config: &avsproto.ContractWriteNode_Config{
								ContractAddress: "0x1234",
							},
						},
					},
				},
			},
		},
	}
	assert.True(t, taskNodesRequireAASender(nodes))
}

func TestTaskNodesRequireAASender_LoopWithCustomCodeRunner(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "loop1",
			Type: avsproto.NodeType_NODE_TYPE_LOOP,
			TaskType: &avsproto.TaskNode_Loop{
				Loop: &avsproto.LoopNode{
					Config: &avsproto.LoopNode_Config{
						InputVariable:    "{{items}}",
						IterVal:          "value",
						IterKey:          "index",
						IterationTimeout: 30,
					},
					Runner: &avsproto.LoopNode_CustomCode{
						CustomCode: &avsproto.CustomCodeNode{
							Config: &avsproto.CustomCodeNode_Config{
								Source: "return value;",
							},
						},
					},
				},
			},
		},
	}
	assert.False(t, taskNodesRequireAASender(nodes))
}

func TestTaskNodesRequireAASender_CustomCodeOnly(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "code1",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Source: "return 1;",
					},
				},
			},
		},
	}
	assert.False(t, taskNodesRequireAASender(nodes))
}

func TestTaskNodesRequireAASender_EmptyNodes(t *testing.T) {
	assert.False(t, taskNodesRequireAASender(nil))
	assert.False(t, taskNodesRequireAASender([]*avsproto.TaskNode{}))
}

func TestTaskNodesRequireAASender_MixedNodesWithLoopEthTransfer(t *testing.T) {
	// Workflow: customCode → branch → loop(ethTransfer)
	// The loop with ethTransfer runner should trigger AA requirement
	nodes := []*avsproto.TaskNode{
		{
			Id:   "code1",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{Source: "return 1;"},
				},
			},
		},
		{
			Id:   "branch1",
			Type: avsproto.NodeType_NODE_TYPE_BRANCH,
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{Id: "0", Type: "if", Expression: "true"},
						},
					},
				},
			},
		},
		{
			Id:   "loop1",
			Type: avsproto.NodeType_NODE_TYPE_LOOP,
			TaskType: &avsproto.TaskNode_Loop{
				Loop: &avsproto.LoopNode{
					Config: &avsproto.LoopNode_Config{
						InputVariable:    "{{recipients}}",
						IterVal:          "value",
						IterKey:          "index",
						IterationTimeout: 60,
					},
					Runner: &avsproto.LoopNode_EthTransfer{
						EthTransfer: &avsproto.ETHTransferNode{
							Config: &avsproto.ETHTransferNode_Config{
								Destination: "{{value}}",
								Amount:      "1000",
							},
						},
					},
				},
			},
		},
	}
	assert.True(t, taskNodesRequireAASender(nodes))
}
