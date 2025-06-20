package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MockSyncMessagesServer simulates an operator connection
type MockSyncMessagesServer struct {
	grpc.ServerStream
	sendCalls    []*avsproto.SyncMessagesResp
	sendErrors   []error
	disconnected bool
	ctx          context.Context
	cancelFunc   context.CancelFunc
}

func NewMockSyncMessagesServer() *MockSyncMessagesServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &MockSyncMessagesServer{
		sendCalls:  make([]*avsproto.SyncMessagesResp, 0),
		sendErrors: make([]error, 0),
		ctx:        ctx,
		cancelFunc: cancel,
	}
}

func (m *MockSyncMessagesServer) Send(resp *avsproto.SyncMessagesResp) error {
	if m.disconnected {
		return status.Errorf(codes.Unavailable, "transport is closing")
	}
	m.sendCalls = append(m.sendCalls, resp)
	return nil
}

func (m *MockSyncMessagesServer) Context() context.Context {
	return m.ctx
}

func (m *MockSyncMessagesServer) Disconnect() {
	m.disconnected = true
	m.cancelFunc()
}

func (m *MockSyncMessagesServer) GetSentTasks() []string {
	var taskIds []string
	for _, call := range m.sendCalls {
		if call.Op == avsproto.MessageOp_MonitorTaskTrigger {
			taskIds = append(taskIds, call.Id)
		}
	}
	return taskIds
}

func (m *MockSyncMessagesServer) Reset() {
	m.sendCalls = make([]*avsproto.SyncMessagesResp, 0)
	m.sendErrors = make([]error, 0)
	m.disconnected = false
	m.ctx, m.cancelFunc = context.WithCancel(context.Background())
}

// TestOperatorReconnectionFlow tests the complete reconnection scenario
func TestOperatorReconnectionFlow(t *testing.T) {
	// Setup
	logger := testutil.GetLogger()
	taskengine.SetLogger(logger)

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := &config.Config{
		ApprovedOperators: []common.Address{
			common.HexToAddress("0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"),
		},
		SmartWallet: &config.SmartWalletConfig{
			EthRpcUrl:      "http://localhost:8545", // Dummy URL for testing
			FactoryAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
		},
	}

	engine := taskengine.New(db, config, nil, logger)
	err := engine.MustStart()
	require.NoError(t, err)

	// Step 1: Create a task that requires event monitoring
	userAddress := common.HexToAddress("0x1234567890123456789012345678901234567890")
	user := &model.User{
		Address:             userAddress,
		SmartAccountAddress: &userAddress, // Initialize to prevent nil pointer dereference
	}

	taskReq := &avsproto.CreateTaskReq{
		SmartWalletAddress: user.Address.Hex(),
		Trigger: &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Event{
				Event: &avsproto.EventTrigger{
					Config: &avsproto.EventTrigger_Config{
						Queries: []*avsproto.EventTrigger_Query{
							{
								Addresses: []string{"0xA0b86a33E6441d476c1bd0a4dc53dFEB3F81E76C"},
								Topics: []*avsproto.EventTrigger_Topics{
									{Values: []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"}}, // Transfer signature
									{Values: []string{""}}, // from - any address (empty means wildcard)
									{Values: []string{"0x000000000000000000000000" + user.Address.Hex()[2:]}}, // to - specific address (padded)
								},
							},
						},
					},
				},
			},
		},
		Nodes: []*avsproto.TaskNode{
			{
				Id:   "log_node",
				Name: "Log Transfer",
				TaskType: &avsproto.TaskNode_CustomCode{
					CustomCode: &avsproto.CustomCodeNode{
						Config: &avsproto.CustomCodeNode_Config{
							Source: `console.log("Transfer detected:", trigger.from, "->", trigger.to, "amount:", trigger.value);`,
						},
					},
				},
			},
		},
		Edges: []*avsproto.TaskEdge{
			{
				Id:     "trigger_to_log",
				Source: "trigger",
				Target: "log_node",
			},
		},
	}

	task, err := engine.CreateTask(user, taskReq)
	require.NoError(t, err)
	require.NotNil(t, task)

	t.Logf("✅ Created task: %s", task.Id)

	// Step 2: Operator connects and gets assignment
	operatorAddr := "0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"
	mockServer1 := NewMockSyncMessagesServer()

	syncReq := &avsproto.SyncMessagesReq{
		Address:        operatorAddr,
		MonotonicClock: time.Now().UnixNano(),
		Capabilities: &avsproto.SyncMessagesReq_Capabilities{
			EventMonitoring: true,
			BlockMonitoring: true,
			TimeMonitoring:  true,
		},
	}

	// Start operator connection in background
	errChan1 := make(chan error, 1)
	go func() {
		err := engine.StreamCheckToOperator(syncReq, mockServer1)
		errChan1 <- err
	}()

	// Wait for operator to stabilize and receive tasks with configurable timeout
	t.Log("⏳ Waiting for operator connection to stabilize...")
	stabilizationTimeout := 12 * time.Second
	if testing.Short() {
		stabilizationTimeout = 3 * time.Second
	}

	stabilizationTimer := time.NewTimer(stabilizationTimeout)
	defer stabilizationTimer.Stop()

	select {
	case <-stabilizationTimer.C:
		t.Log("✅ Initial connection stabilization completed")
	case <-time.After(stabilizationTimeout + 2*time.Second):
		t.Log("ℹ️ Initial connection stabilization took longer than expected (this is normal)")
	}

	// Verify operator received the task
	sentTasks1 := mockServer1.GetSentTasks()
	assert.Greater(t, len(sentTasks1), 0, "Operator should have received tasks")
	assert.Contains(t, sentTasks1, task.Id, "Operator should have received the created task")

	t.Logf("✅ Operator received %d tasks: %v", len(sentTasks1), sentTasks1)

	// Step 3: Operator disconnects
	t.Log("🔌 Disconnecting operator...")
	mockServer1.Disconnect()

	// Wait for disconnection to be processed
	select {
	case err := <-errChan1:
		t.Logf("✅ Operator disconnected with error: %v", err)
	case <-time.After(5 * time.Second):
		t.Log("ℹ️ Operator disconnection cleanup took longer than expected (this is normal)")
	}

	// Step 4: Wait 10+ seconds, then operator reconnects
	t.Log("⏳ Waiting before reconnection...")
	time.Sleep(3 * time.Second)

	// Create new mock server for reconnection (simulating new operator instance)
	mockServer2 := NewMockSyncMessagesServer()
	syncReq2 := &avsproto.SyncMessagesReq{
		Address:        operatorAddr,
		MonotonicClock: time.Now().UnixNano(), // New monotonic clock
		Capabilities: &avsproto.SyncMessagesReq_Capabilities{
			EventMonitoring: true,
			BlockMonitoring: true,
			TimeMonitoring:  true,
		},
	}

	t.Log("🔄 Operator reconnecting...")
	errChan2 := make(chan error, 1)
	go func() {
		err := engine.StreamCheckToOperator(syncReq2, mockServer2)
		errChan2 <- err
	}()

	// Wait for operator to stabilize and receive tasks again with configurable timeout
	t.Log("⏳ Waiting for reconnected operator to stabilize...")
	reconnectionTimeout := 12 * time.Second
	if testing.Short() {
		reconnectionTimeout = 3 * time.Second
	}

	reconnectionTimer := time.NewTimer(reconnectionTimeout)
	defer reconnectionTimer.Stop()

	select {
	case <-reconnectionTimer.C:
		t.Log("✅ Reconnection stabilization completed")
	case <-time.After(reconnectionTimeout + 2*time.Second):
		t.Log("ℹ️ Reconnection stabilization took longer than expected (this is normal)")
	}

	// Step 5: Verify operator gets assignments again
	sentTasks2 := mockServer2.GetSentTasks()
	assert.Greater(t, len(sentTasks2), 0, "Reconnected operator should have received tasks")
	assert.Contains(t, sentTasks2, task.Id, "Reconnected operator should have received the task")

	t.Logf("✅ Reconnected operator received %d tasks: %v", len(sentTasks2), sentTasks2)

	// Step 6: Clean up
	mockServer2.Disconnect()

	select {
	case <-errChan2:
		t.Log("✅ Reconnected operator disconnected")
	case <-time.After(2 * time.Second):
		t.Log("ℹ️ Reconnected operator disconnection cleanup took longer than expected (this is normal)")
	}

	engine.Stop()

	t.Log("🎉 Test completed successfully!")
}

// TestOperatorReconnectionRaceCondition specifically tests for race conditions
func TestOperatorReconnectionRaceCondition(t *testing.T) {
	logger := testutil.GetLogger()
	taskengine.SetLogger(logger)

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := &config.Config{
		ApprovedOperators: []common.Address{
			common.HexToAddress("0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"),
		},
		SmartWallet: &config.SmartWalletConfig{
			EthRpcUrl:      "http://localhost:8545", // Dummy URL for testing
			FactoryAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
		},
	}

	engine := taskengine.New(db, config, nil, logger)
	err := engine.MustStart()
	require.NoError(t, err)
	defer engine.Stop()

	// Create a simple task
	userAddress := common.HexToAddress("0x1234567890123456789012345678901234567890")
	user := &model.User{
		Address:             userAddress,
		SmartAccountAddress: &userAddress, // Initialize to prevent nil pointer dereference
	}

	taskReq := &avsproto.CreateTaskReq{
		SmartWalletAddress: user.Address.Hex(),
		Trigger: &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Event{
				Event: &avsproto.EventTrigger{
					Config: &avsproto.EventTrigger_Config{
						Queries: []*avsproto.EventTrigger_Query{
							{
								Addresses: []string{"0xA0b86a33E6441d476c1bd0a4dc53dFEB3F81E76C"},
								Topics: []*avsproto.EventTrigger_Topics{
									{Values: []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"}}, // Transfer signature
								},
							},
						},
					},
				},
			},
		},
		Nodes: []*avsproto.TaskNode{
			{
				Id:   "log_node",
				Name: "Log Transfer",
				TaskType: &avsproto.TaskNode_CustomCode{
					CustomCode: &avsproto.CustomCodeNode{
						Config: &avsproto.CustomCodeNode_Config{
							Source: `console.log("Transfer detected");`,
						},
					},
				},
			},
		},
		Edges: []*avsproto.TaskEdge{{Id: "trigger_to_log", Source: "trigger", Target: "log_node"}},
	}

	_, err = engine.CreateTask(user, taskReq)
	require.NoError(t, err)

	operatorAddr := "0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"

	// Test rapid reconnections to trigger race conditions
	for i := 0; i < 3; i++ {
		t.Logf("🔄 Rapid reconnection test #%d", i+1)

		mockServer := NewMockSyncMessagesServer()
		syncReq := &avsproto.SyncMessagesReq{
			Address:        operatorAddr,
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

		// Short connection time to trigger rapid reconnection
		time.Sleep(2 * time.Second)
		mockServer.Disconnect()

		select {
		case <-errChan:
			// Connection ended as expected
		case <-time.After(3 * time.Second):
			t.Log("⚠️ Connection didn't end quickly")
		}

		time.Sleep(100 * time.Millisecond) // Brief pause between reconnections
	}

	t.Log("✅ Rapid reconnection test completed without hanging!")
}
