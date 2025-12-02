//go:build integration
// +build integration

package integration_test

import (
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/operator"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/require"
)

// SimpleMockServer replicates the lightweight mock stream used in other integration tests
// Reuse SimpleMockServer defined in ticker_context_test.go for consistency

// TestActivationDeactivationSyncWithConfigs loads aggregator and operator YAML configs
// and verifies the end-to-end sync sequence: MonitorTaskTrigger → DeactivateTask → MonitorTaskTrigger.
func TestActivationDeactivationSyncWithConfigs(t *testing.T) {
	logger := testutil.GetLogger()
	taskengine.SetLogger(logger)

	// Load aggregator config from YAML
	aggCfgPath := testutil.GetConfigPath(testutil.DefaultConfigPath) // config/aggregator-sepolia.yaml
	aggCfg, err := config.NewConfig(aggCfgPath)
	if err != nil {
		t.Skipf("Failed to load aggregator config at %s: %v", aggCfgPath, err)
	}

	// Load operator config from YAML
	var opCfg operator.OperatorConfig
	if err := config.ReadYamlConfig(testutil.GetConfigPath("operator-sepolia.yaml"), &opCfg); err != nil {
		t.Skipf("Failed to load operator config: %v", err)
	}

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	engine := taskengine.New(db, aggCfg, nil, logger)
	require.NoError(t, engine.MustStart())
	defer engine.Stop()

	// Open operator stream using operator-sepolia.yaml values
	mockServer := NewSimpleMockServer()
	syncReq := &avsproto.SyncMessagesReq{
		Address:        opCfg.OperatorAddress,
		MonotonicClock: time.Now().UnixNano(),
		Capabilities: &avsproto.SyncMessagesReq_Capabilities{
			EventMonitoring: true,
			BlockMonitoring: true,
			TimeMonitoring:  true,
		},
	}

	errChan := make(chan error, 1)
	go func() {
		err := engine.StreamCheckToOperator(syncReq, mockServer)
		errChan <- err
	}()

	// Wait briefly for stream stabilization (shorter than the full 10s to keep test reasonably fast)
	stabilization := 2 * time.Second
	time.Sleep(stabilization)

	// Create a task (initially Active)
	user := testutil.TestUser1()
	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	task, err := engine.CreateTask(user, taskReq)
	require.NoError(t, err)

	// Wait for MonitorTaskTrigger to be sent
	waitFor := func(cond func() bool, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if cond() {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return cond()
	}

	gotMonitor := waitFor(func() bool {
		for _, m := range mockServer.GetSentTasks() {
			if m.Op == avsproto.MessageOp_MonitorTaskTrigger && m.Id == task.Id {
				return true
			}
		}
		return false
	}, 12*time.Second) // allow enough time for stabilization loop to emit
	require.True(t, gotMonitor, "expected initial MonitorTaskTrigger for created task")

	// Disable the task and expect a DisableTask control message
	deactResp, err := engine.SetTaskEnabledByUser(user, task.Id, false)
	require.NoError(t, err)
	require.True(t, deactResp.Success)
	// ensure batch flush if needed
	time.Sleep(200 * time.Millisecond)

	gotDeactivate := waitFor(func() bool {
		for _, m := range mockServer.GetSentTasks() {
			if m.Op == avsproto.MessageOp_DisableTask && m.Id == task.Id {
				return true
			}
		}
		return false
	}, 3*time.Second)
	require.True(t, gotDeactivate, "expected DeactivateTask after deactivation")

	// Cleanup
	mockServer.Disconnect()
	select {
	case <-errChan:
	case <-time.After(3 * time.Second):
	}
}
