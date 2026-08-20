package taskengine

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

const (
	runnerAudChain   = int64(11155111)
	runnerOtherChain = int64(84532)
)

func newRunnerChainEngine(t *testing.T) (*Engine, storage.Storage) {
	t.Helper()
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	cfg := testutil.GetAggregatorConfig()
	cfg.SmartWallet.ChainID = runnerAudChain
	otherChainWallet := *cfg.SmartWallet
	otherChainWallet.ChainID = runnerOtherChain
	cfg.IsGateway = true
	cfg.Chains = []*config.ChainConfig{
		{ChainID: runnerAudChain, Name: "sepolia", SmartWallet: cfg.SmartWallet},
		{ChainID: runnerOtherChain, Name: "base-sepolia", SmartWallet: &otherChainWallet},
	}
	return New(db, cfg, nil, testutil.GetLogger()), db
}

// aaWorkflow requires an AA sender (it contains a contractWrite node, which
// is what taskNodesRequireAASender looks for) but only ever executes a
// customCode node that echoes the resolved `aa_sender`. The write is
// deliberately left off the edge path: the runner gate still runs, and its
// verdict becomes readable without simulating a real write.
func aaWorkflow() (*avsproto.TaskTrigger, []*avsproto.TaskNode, []*avsproto.TaskEdge) {
	trigger := &avsproto.TaskTrigger{
		Id: "manual_trigger", Name: "manual_trigger",
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: &avsproto.ManualTrigger{
				Config: &avsproto.ManualTrigger_Config{
					Lang: avsproto.Lang_LANG_JSON,
					Data: func() *structpb.Value {
						v, _ := structpb.NewValue(map[string]interface{}{"test": "manual"})
						return v
					}(),
				},
			},
		},
	}
	nodes := []*avsproto.TaskNode{
		{
			Id: "write1", Name: "write1",
			TaskType: &avsproto.TaskNode_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode{
					Config: &avsproto.ContractWriteNode_Config{
						ContractAddress: "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
						CallData:        "0x095ea7b3",
					},
				},
			},
		},
		{
			Id: "echo", Name: "echo",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{Source: "return aa_sender;"},
				},
			},
		},
	}
	edges := []*avsproto.TaskEdge{{Id: "e1", Source: "manual_trigger", Target: "echo"}}
	return trigger, nodes, edges
}

// resolvedSender runs the workflow on simChain and returns the aa_sender the
// runner gate settled on.
func resolvedSender(t *testing.T, engine *Engine, user *model.User, runner common.Address, simChain int64) string {
	t.Helper()
	trigger, nodes, edges := aaWorkflow()
	inputVariables := map[string]interface{}{
		"settings": map[string]interface{}{"name": "runner chain", "runner": runner.Hex()},
	}
	execution, err := engine.SimulateWorkflow(user, trigger, nodes, edges, inputVariables, simChain)
	require.NoError(t, err)
	require.NotEmpty(t, execution.Steps)
	for _, step := range execution.Steps {
		if step.Id == "echo" {
			require.True(t, step.Success, "echo step failed: %s", step.Error)
			return strings.TrimSpace(step.GetCustomCode().GetData().GetStringValue())
		}
	}
	t.Fatalf("no echo step in execution")
	return ""
}

// The runner is resolved against the chain the simulation targets, not the
// JWT audience.
//
// The failure this guards is quiet rather than loud. When the runner is not
// found on the chain being listed, resolution does not stop — it falls
// through to "first wallet owned by the user", which on a chain with working
// derivation is the owner's DEFAULT wallet. So a cross-chain simulation used
// to run as the wrong sender and report success, rather than reporting that
// it could not find the runner.
func TestSimulateResolvesRunnerOnTheSimulatedChain(t *testing.T) {
	engine, db := newRunnerChainEngine(t)

	owner := common.HexToAddress("0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788")
	factory := effectiveFactoryAddr(t, engine.smartWalletConfig)
	runner := common.HexToAddress("0x00000000000000000000000000000000000000f1")
	// Registered ONLY on the non-audience chain, so the two chains cannot
	// agree by accident.
	require.NoError(t, StoreWallet(db, runnerOtherChain, owner, mkWallet(owner, factory, runner, 3)))

	// The audience is the chain the runner is NOT on.
	user := &model.User{Address: owner, ChainID: runnerAudChain}

	onRunnerChain := resolvedSender(t, engine, user, runner, runnerOtherChain)
	require.Equal(t, strings.ToLower(runner.Hex()), strings.ToLower(onRunnerChain),
		"simulating on the runner's chain must resolve to the runner itself")

	// Control: the same call aimed at the audience chain, where the runner is
	// genuinely absent. It must NOT come back as the runner — that is the
	// silent substitution described above, and it is what proves the
	// assertion above is reading the simulated chain rather than the
	// audience one.
	onAudChain := resolvedSender(t, engine, user, runner, runnerAudChain)
	require.NotEqual(t, strings.ToLower(runner.Hex()), strings.ToLower(onAudChain),
		"the runner is not registered on the audience chain, so it cannot resolve there")
}

// The same routing on the RunNodeImmediately path, where the chain comes
// from settings.chain_id (the value vmSmartWalletConfig is resolved from)
// rather than a simulate argument.
//
// Here the wrong-chain outcome is loud rather than quiet: a runner missing
// from the listed chain falls through to the salt-0..4 derivation scan, and
// an address that is not a derived wallet is rejected outright.
func TestRunNodeResolvesRunnerOnTheSettingsChain(t *testing.T) {
	engine, db := newRunnerChainEngine(t)

	owner := common.HexToAddress("0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788")
	factory := effectiveFactoryAddr(t, engine.smartWalletConfig)
	runner := common.HexToAddress("0x00000000000000000000000000000000000000f2")
	require.NoError(t, StoreWallet(db, runnerOtherChain, owner, mkWallet(owner, factory, runner, 2)))

	user := &model.User{Address: owner, ChainID: runnerAudChain}
	nodeConfig := map[string]interface{}{
		"contractAddress": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		"callData":        "0x095ea7b3",
	}
	run := func(chainID int64) error {
		_, err := engine.RunNodeImmediately("contractWrite", nodeConfig, map[string]interface{}{
			"settings": map[string]interface{}{"runner": runner.Hex(), "chain_id": chainID},
		}, user, true)
		return err
	}

	// The runner is not on the audience chain, and it is not a derived
	// address either, so the scan rejects it.
	err := run(runnerAudChain)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match any derived address",
		"a runner absent from the named chain must be rejected")

	// Naming the chain it IS registered on must clear that check. Whatever
	// the write itself does afterwards, it must not be refused as an
	// unrecognised runner.
	if err := run(runnerOtherChain); err != nil {
		require.NotContains(t, err.Error(), "does not match any derived address",
			"the runner is registered on the named chain and must resolve there")
	}
}
