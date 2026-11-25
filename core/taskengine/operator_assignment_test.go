package taskengine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

// The production operators, both on the default approved list.
const (
	sepoliaOperator  = "0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"
	ethereumOperator = "0xc6B87cc9e85b07365b6aBEfff061F237F7cf7Dc3"
)

// recordingStream is an operator stream that records the tasks streamed to it
// and can be disconnected.
type recordingStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	tasks  []string
}

func newRecordingStream() *recordingStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &recordingStream{ctx: ctx, cancel: cancel}
}

func (s *recordingStream) Send(resp *avsproto.SyncMessagesResp) error {
	if resp.Op == avsproto.MessageOp_MonitorTaskTrigger {
		s.mu.Lock()
		s.tasks = append(s.tasks, resp.Id)
		s.mu.Unlock()
	}
	return nil
}

func (s *recordingStream) streamed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tasks...)
}

func (s *recordingStream) Context() context.Context     { return s.ctx }
func (s *recordingStream) SetHeader(metadata.MD) error  { return nil }
func (s *recordingStream) SendHeader(metadata.MD) error { return nil }
func (s *recordingStream) SetTrailer(metadata.MD)       {}
func (s *recordingStream) RecvMsg(interface{}) error    { return nil }
func (s *recordingStream) SendMsg(interface{}) error    { return nil }

func baseBlockTask() *model.Workflow {
	return &model.Workflow{Task: &avsproto.Task{
		Id:     model.GenerateID(),
		Status: avsproto.TaskStatus_Enabled,
		Trigger: &avsproto.TaskTrigger{
			Id:   "trigger",
			Name: "minute",
			Type: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
			TriggerType: &avsproto.TaskTrigger_Block{Block: &avsproto.BlockTrigger{
				Config: &avsproto.BlockTrigger_Config{Interval: 300, ChainId: 8453},
			}},
		},
	}}
}

func syncRequest(address string, chains ...int64) *avsproto.SyncMessagesReq {
	return &avsproto.SyncMessagesReq{
		Address:        address,
		MonotonicClock: time.Now().UnixNano(),
		Capabilities: &avsproto.SyncMessagesReq_Capabilities{
			EventMonitoring: true,
			BlockMonitoring: true,
			TimeMonitoring:  true,
		},
		SupportedChainIds: chains,
	}
}

// The 2026-11-25 incident: operator-sepolia reconnected first, claimed a Base
// mainnet task it cannot watch, and operator-ethereum then skipped the task as
// assigned elsewhere, so nobody monitored it. The task must go to the operator
// that covers its chain whichever one connects first.
//
// Slow by necessity: an operator's first task send waits out the gateway's
// 10s connection-stability period.
func TestOperatorClaimsOnlyTasksOnItsChains(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 10s connection-stability period")
	}
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	engine := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())
	require.True(t, engine.config.IsApprovedOperator(sepoliaOperator) && engine.config.IsApprovedOperator(ethereumOperator),
		"test needs both production operators on the approved list")

	task := baseBlockTask()
	engine.lock.Lock()
	engine.tasks[task.Id] = task
	engine.lock.Unlock()

	sepolia, ethereum := newRecordingStream(), newRecordingStream()
	go func() { _ = engine.StreamCheckToOperator(syncRequest(sepoliaOperator, 11155111, 84532), sepolia) }()
	time.Sleep(time.Second) // sepolia's first send runs a second before ethereum's
	go func() { _ = engine.StreamCheckToOperator(syncRequest(ethereumOperator, 1, 8453), ethereum) }()
	// The stream loop exits on its ticker context, which Stop cancels before
	// waiting for every stream to return.
	t.Cleanup(engine.Stop)

	require.Eventually(t, func() bool { return len(ethereum.streamed()) > 0 }, 15*time.Second, 200*time.Millisecond,
		"operator-ethereum was never sent the Base task")
	require.Equal(t, []string{task.Id}, ethereum.streamed())
	require.Empty(t, sepolia.streamed(), "operator-sepolia must not be sent a Base mainnet task")

	engine.assignmentMutex.RLock()
	defer engine.assignmentMutex.RUnlock()
	require.Equal(t, ethereumOperator, engine.taskAssignments[task.Id])
}

// A task already assigned to an operator that does not cover its chain (left
// by the old claim order) moves to one that does on the next assignment check.
func TestStaleAssignmentMovesToOperatorCoveringTheChain(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	engine := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	seedOperator(engine, sepoliaOperator, []int64{11155111, 84532})
	seedOperator(engine, ethereumOperator, []int64{1, 8453})
	engine.streamsMutex.Lock()
	engine.operatorStreams[sepoliaOperator] = newRecordingStream()
	engine.operatorStreams[ethereumOperator] = newRecordingStream()
	engine.streamsMutex.Unlock()

	task := baseBlockTask()
	engine.assignmentMutex.Lock()
	engine.taskAssignments[task.Id] = sepoliaOperator
	engine.assignmentMutex.Unlock()

	require.Equal(t, []string{ethereumOperator}, engine.getEligibleOperators(task))
	require.Equal(t, ethereumOperator, engine.assignTaskToOperator(task))
}
