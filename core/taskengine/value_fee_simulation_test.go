package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestSimulateTask_ValueFeeMatchesWorkflowDefinition is a regression test for
// the simulation/production fee divergence: a workflow whose loop runs a
// contractWrite was being classified as "no on-chain execution nodes" during
// SimulateTask (because Tenderly-backed loop steps record no real gas), while
// the same workflow in production reported Tier 1 / 0.03%. The classifier now
// reads from the workflow definition (task.Nodes), so simulation and production
// must agree.
//
// This test asserts the engine-level path: SimulateTask -> buildValueFee.
func TestSimulateTask_ValueFeeMatchesWorkflowDefinition(t *testing.T) {
	trigger := &avsproto.TaskTrigger{
		Id:   "trigger1",
		Name: "manualTrigger",
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: &avsproto.ManualTrigger{
				Config: &avsproto.ManualTrigger_Config{
					Lang: avsproto.Lang_LANG_JSON,
					Data: func() *structpb.Value {
						data, _ := structpb.NewValue(map[string]interface{}{"value": "1500000"})
						return data
					}(),
				},
			},
		},
	}

	// Revenue-splitter shape: customCode produces an array of recipients,
	// loop iterates and contractWrite-transfers to each.
	splitNode := &avsproto.TaskNode{
		Id:   "split1",
		Name: "split1",
		Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
		TaskType: &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{
				Config: &avsproto.CustomCodeNode_Config{
					Source: `return [
						{ recipient: "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557", amount: "200000" },
						{ recipient: "0xfE66125343Aabda4A330DA667431eC1Acb7BbDA9", amount: "450000" }
					];`,
				},
			},
		},
	}

	loopNode := &avsproto.TaskNode{
		Id:   "loop1",
		Name: "loop1",
		Type: avsproto.NodeType_NODE_TYPE_LOOP,
		TaskType: &avsproto.TaskNode_Loop{
			Loop: &avsproto.LoopNode{
				Config: &avsproto.LoopNode_Config{
					InputVariable: "{{split1.data}}",
					IterVal:       "value",
					IterKey:       "index",
				},
				Runner: &avsproto.LoopNode_ContractWrite{
					ContractWrite: &avsproto.ContractWriteNode{
						Config: &avsproto.ContractWriteNode_Config{
							ContractAddress: "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
							ContractAbi: func() []*structpb.Value {
								v, _ := structpb.NewValue(map[string]interface{}{
									"name": "transfer", "type": "function", "stateMutability": "nonpayable",
									"inputs": []interface{}{
										map[string]interface{}{"name": "to", "type": "address"},
										map[string]interface{}{"name": "amount", "type": "uint256"},
									},
									"outputs": []interface{}{map[string]interface{}{"name": "", "type": "bool"}},
								})
								return []*structpb.Value{v}
							}(),
							MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
								{
									MethodName:   "transfer",
									MethodParams: []string{"{{value.recipient}}", "{{value.amount}}"},
								},
							},
						},
					},
				},
			},
		},
	}

	nodes := []*avsproto.TaskNode{splitNode, loopNode}
	edges := []*avsproto.TaskEdge{
		{Id: "e1", Source: "trigger1", Target: "split1"},
		{Id: "e2", Source: "split1", Target: "loop1"},
	}

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	cfg := testutil.GetAggregatorConfig()
	engine := New(db, cfg, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, map[string]interface{}{
		"settings": map[string]interface{}{
			"name":   "Revenue Splitter Fee Test",
			"runner": "0x6cF121b8783Ae78A30A46DD4Ae1609E436422C26",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, execution)
	require.NotNil(t, execution.ValueFee, "ValueFee must be populated by SimulateTask")

	// The classifier must look at the workflow definition, not at runtime gas.
	// Even though the loop runs under simulation (no real gas metered), the
	// presence of a contractWrite runner means this workflow IS on-chain in
	// production and must be Tier 1 / 0.03%.
	assert.Equal(t, avsproto.ExecutionTier_EXECUTION_TIER_1, execution.ValueFee.Tier,
		"loop(contractWrite) workflow must be Tier 1 in simulation, matching production")
	assert.Equal(t, "0.03", execution.ValueFee.Fee.Amount)
	assert.Equal(t, "PERCENTAGE", execution.ValueFee.Fee.Unit)
	assert.NotEqual(t, "Workflow has no on-chain execution nodes", execution.ValueFee.Reason,
		"must not report off-chain reason for a workflow whose loop runs contractWrite")
}

// TestSimulateTask_ValueFeeOffChainOnly is the negative case: a workflow with
// no on-chain nodes must classify as UNSPECIFIED / 0% in simulation.
func TestSimulateTask_ValueFeeOffChainOnly(t *testing.T) {
	trigger := &avsproto.TaskTrigger{
		Id:   "trigger1",
		Name: "manualTrigger",
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: &avsproto.ManualTrigger{
				Config: &avsproto.ManualTrigger_Config{
					Lang: avsproto.Lang_LANG_JSON,
					Data: func() *structpb.Value {
						data, _ := structpb.NewValue(map[string]interface{}{"x": 1})
						return data
					}(),
				},
			},
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "code1",
			Name: "code1",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{Source: "return { ok: true };"},
				},
			},
		},
	}
	edges := []*avsproto.TaskEdge{{Id: "e1", Source: "trigger1", Target: "code1"}}

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	cfg := testutil.GetAggregatorConfig()
	engine := New(db, cfg, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	execution, err := engine.SimulateTask(user, trigger, nodes, edges, map[string]interface{}{
		"settings": map[string]interface{}{"name": "Off-chain only", "runner": "0x6cF121b8783Ae78A30A46DD4Ae1609E436422C26"},
	})
	require.NoError(t, err)
	require.NotNil(t, execution.ValueFee)
	assert.Equal(t, avsproto.ExecutionTier_EXECUTION_TIER_UNSPECIFIED, execution.ValueFee.Tier)
	assert.Equal(t, "0", execution.ValueFee.Fee.Amount)
}
