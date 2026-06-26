package taskengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TestExtractNodeConfiguration_CarriesChainId locks G3: the per-node chain must
// survive ExtractNodeConfiguration (the map-based config used by
// runNodeImmediately / simulation / loop-nested) and be readable back by
// CreateNodeFromType. Before the fix these paths dropped chainId and ran on the
// wrong chain.
func TestExtractNodeConfiguration_CarriesChainId(t *testing.T) {
	const chainID = int64(8453)

	cases := []struct {
		name string
		node *avsproto.TaskNode
		read func(*avsproto.TaskNode) int64
	}{
		{
			name: "contractWrite",
			node: &avsproto.TaskNode{
				Id: "cw1", Name: "cw", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
				TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{
					Config: &avsproto.ContractWriteNode_Config{
						ContractAddress: "0x1234567890123456789012345678901234567890",
						ChainId:         chainID,
					},
				}},
			},
			read: func(n *avsproto.TaskNode) int64 { return n.GetContractWrite().GetConfig().GetChainId() },
		},
		{
			name: "contractRead",
			node: &avsproto.TaskNode{
				Id: "cr1", Name: "cr", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_READ,
				TaskType: &avsproto.TaskNode_ContractRead{ContractRead: &avsproto.ContractReadNode{
					Config: &avsproto.ContractReadNode_Config{
						ContractAddress: "0x1234567890123456789012345678901234567890",
						ChainId:         chainID,
					},
				}},
			},
			read: func(n *avsproto.TaskNode) int64 { return n.GetContractRead().GetConfig().GetChainId() },
		},
		{
			name: "ethTransfer",
			node: &avsproto.TaskNode{
				Id: "et1", Name: "et", Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER,
				TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{
					Config: &avsproto.ETHTransferNode_Config{
						Destination: "0x1234567890123456789012345678901234567890",
						Amount:      "1",
						ChainId:     chainID,
					},
				}},
			},
			read: func(n *avsproto.TaskNode) int64 { return n.GetEthTransfer().GetConfig().GetChainId() },
		},
	}

	nodeTypeString := map[string]string{
		"contractWrite": "contractWrite",
		"contractRead":  "contractRead",
		"ethTransfer":   "ethTransfer",
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ExtractNodeConfiguration(tc.node)
			require.NotNil(t, cfg)
			assert.EqualValues(t, chainID, cfg["chainId"], "ExtractNodeConfiguration must emit chainId")

			rebuilt, err := CreateNodeFromType(nodeTypeString[tc.name], cfg, tc.node.Id)
			require.NoError(t, err)
			assert.Equal(t, chainID, tc.read(rebuilt), "CreateNodeFromType must read chainId back onto the proto")
		})
	}
}

// TestValidateExplicitPartChains locks G4: in gateway mode, a chain-aware part
// naming an explicit chain the aggregator isn't configured for is rejected at
// create; chain_id == 0 (inherit) and configured chains are accepted.
func TestValidateExplicitPartChains(t *testing.T) {
	// Engine configured (gateway) for chains 1 and 8453 only.
	n := &Engine{
		config:       &config.Config{IsGateway: true},
		chainConfigs: map[int64]*config.ChainConfig{1: {ChainID: 1}, 8453: {ChainID: 8453}},
	}

	cwNode := func(chainID int64) *avsproto.TaskNode {
		return &avsproto.TaskNode{
			Id: "cw", Name: "cw", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
			TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{ChainId: chainID},
			}},
		}
	}

	t.Run("configured node chain passes", func(t *testing.T) {
		task := &model.Workflow{Task: &avsproto.Task{Nodes: []*avsproto.TaskNode{cwNode(8453)}}}
		require.NoError(t, n.validateExplicitPartChains(task))
	})

	t.Run("chain_id 0 (inherit) passes", func(t *testing.T) {
		task := &model.Workflow{Task: &avsproto.Task{Nodes: []*avsproto.TaskNode{cwNode(0)}}}
		require.NoError(t, n.validateExplicitPartChains(task))
	})

	t.Run("unconfigured explicit node chain is rejected", func(t *testing.T) {
		task := &model.Workflow{Task: &avsproto.Task{Nodes: []*avsproto.TaskNode{cwNode(137)}}}
		err := n.validateExplicitPartChains(task)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "137")
	})

	t.Run("unconfigured event trigger chain is rejected", func(t *testing.T) {
		task := &model.Workflow{Task: &avsproto.Task{Trigger: &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Event{Event: &avsproto.EventTrigger{
				Config: &avsproto.EventTrigger_Config{ChainId: 999999},
			}},
		}}}
		err := n.validateExplicitPartChains(task)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "event trigger")
	})

	t.Run("non-gateway mode skips validation", func(t *testing.T) {
		single := &Engine{config: &config.Config{IsGateway: false}}
		task := &model.Workflow{Task: &avsproto.Task{Nodes: []*avsproto.TaskNode{cwNode(137)}}}
		require.NoError(t, single.validateExplicitPartChains(task))
	})
}

// TestTriggerMonitoringChainID locks G2: the operator monitors a chain-watching
// trigger on the trigger's OWN chain, falling back to the task chain only when
// the trigger leaves its chain at 0 (legacy). Non-chain triggers carry the
// fallback through.
func TestTriggerMonitoringChainID(t *testing.T) {
	const taskChain = int64(11155111)

	eventTrigger := func(chainID int64) *avsproto.TaskTrigger {
		return &avsproto.TaskTrigger{TriggerType: &avsproto.TaskTrigger_Event{
			Event: &avsproto.EventTrigger{Config: &avsproto.EventTrigger_Config{ChainId: chainID}},
		}}
	}
	blockTrigger := func(chainID int64) *avsproto.TaskTrigger {
		return &avsproto.TaskTrigger{TriggerType: &avsproto.TaskTrigger_Block{
			Block: &avsproto.BlockTrigger{Config: &avsproto.BlockTrigger_Config{ChainId: chainID, Interval: 1}},
		}}
	}
	cronTrigger := &avsproto.TaskTrigger{TriggerType: &avsproto.TaskTrigger_Cron{
		Cron: &avsproto.CronTrigger{Config: &avsproto.CronTrigger_Config{Schedules: []string{"* * * * *"}}},
	}}

	assert.Equal(t, int64(8453), triggerMonitoringChainID(eventTrigger(8453), taskChain),
		"event trigger uses its own chain, not the task chain")
	assert.Equal(t, taskChain, triggerMonitoringChainID(eventTrigger(0), taskChain),
		"event trigger chain 0 falls back to the task chain (legacy)")
	assert.Equal(t, int64(8453), triggerMonitoringChainID(blockTrigger(8453), taskChain),
		"block trigger uses its own chain")
	assert.Equal(t, taskChain, triggerMonitoringChainID(cronTrigger, taskChain),
		"cron trigger carries the fallback (chain-agnostic)")
	assert.Equal(t, taskChain, triggerMonitoringChainID(nil, taskChain),
		"nil trigger carries the fallback")
}
