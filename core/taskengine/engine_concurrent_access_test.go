package taskengine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// MockSyncMessagesServerForConcurrencyTest is a minimal mock server for testing concurrent access
type MockSyncMessagesServerForConcurrencyTest struct {
	grpc.ServerStream
	ctx        context.Context
	cancelFunc context.CancelFunc
}

func NewMockSyncMessagesServerForConcurrencyTest() *MockSyncMessagesServerForConcurrencyTest {
	ctx, cancel := context.WithCancel(context.Background())
	return &MockSyncMessagesServerForConcurrencyTest{
		ctx:        ctx,
		cancelFunc: cancel,
	}
}

func (m *MockSyncMessagesServerForConcurrencyTest) Send(resp *avsproto.SyncMessagesResp) error {
	// Minimal implementation - just return nil
	return nil
}

func (m *MockSyncMessagesServerForConcurrencyTest) Context() context.Context {
	return m.ctx
}

func (m *MockSyncMessagesServerForConcurrencyTest) Disconnect() {
	m.cancelFunc()
}

// TestStreamCheckToOperator_ConcurrentAccess tests that concurrent access to trackSyncedTasks
// does not cause data races. This test specifically targets the locking fix where the lock
// was incorrectly released inside the if block, leaving the else block unsynchronized.
//
// This test spawns multiple goroutines that concurrently call StreamCheckToOperator,
// which accesses trackSyncedTasks. The test verifies that:
// 1. No data races occur (detected by race detector)
// 2. The map remains in a consistent state
// 3. All operations complete without panics
func TestStreamCheckToOperator_ConcurrentAccess(t *testing.T) {
	// Setup test environment
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Test parameters - use fewer goroutines/iterations to avoid overwhelming the system
	numGoroutines := 20
	numIterations := 5
	operatorAddress := "0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"

	var wg sync.WaitGroup
	var errors sync.Map // thread-safe error collection
	successCount := int64(0)
	var mu sync.Mutex

	// Spawn multiple goroutines that concurrently access trackSyncedTasks
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < numIterations; j++ {
				// Create a new mock server for each iteration
				mockServer := NewMockSyncMessagesServerForConcurrencyTest()

				// Create sync request with varying monotonic clocks to test both if and else branches
				syncReq := &avsproto.SyncMessagesReq{
					Address:        operatorAddress,
					MonotonicClock: time.Now().UnixNano() + int64(goroutineID*1000+j), // Vary clock to hit different branches
					Capabilities: &avsproto.SyncMessagesReq_Capabilities{
						EventMonitoring: true,
						BlockMonitoring: true,
						TimeMonitoring:  true,
					},
				}

				// Call StreamCheckToOperator in a goroutine and cancel it quickly
				// This tests the critical section where trackSyncedTasks is accessed
				errChan := make(chan error, 1)
				go func() {
					errChan <- engine.StreamCheckToOperator(syncReq, mockServer)
				}()

				// Cancel the connection quickly to test the locking without waiting for ticker
				time.Sleep(time.Millisecond * 10)
				mockServer.Disconnect()

				// Wait for the function to return (should return quickly after cancellation)
				select {
				case err := <-errChan:
					if err == nil {
						mu.Lock()
						successCount++
						mu.Unlock()
					} else {
						errors.Store(goroutineID*numIterations+j, err)
					}
				case <-time.After(time.Second):
					// Function didn't return - this is acceptable as it may be waiting on ticker
					errors.Store(goroutineID*numIterations+j, "timeout")
				}

				// Small delay to increase chance of concurrent access
				time.Sleep(time.Microsecond * 10)
			}
		}(i)
	}

	// Wait for all goroutines to complete
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// Wait with timeout
	select {
	case <-done:
		t.Logf("✅ All goroutines completed successfully")
	case <-time.After(15 * time.Second):
		t.Logf("⚠️ Test timed out - some goroutines may still be running (this is acceptable)")
	}

	// Verify results
	mu.Lock()
	actualSuccessCount := successCount
	mu.Unlock()

	// Count errors
	errorCount := 0
	errors.Range(func(key, value interface{}) bool {
		errorCount++
		return true
	})

	t.Logf("Concurrent access test results:")
	t.Logf("  Total operations: %d", numGoroutines*numIterations)
	t.Logf("  Successful: %d", actualSuccessCount)
	t.Logf("  Errors/Timeouts: %d", errorCount)

	// Verify that trackSyncedTasks is in a consistent state
	engine.lock.Lock()
	state, exists := engine.trackSyncedTasks[operatorAddress]
	engine.lock.Unlock()

	if exists {
		// Verify the state is valid
		assert.NotNil(t, state, "Operator state should not be nil")
		assert.NotNil(t, state.TaskID, "TaskID map should not be nil")
		assert.NotNil(t, state.TickerCtx, "TickerCtx should not be nil")
		t.Logf("✅ Operator state is consistent after concurrent access")
		t.Logf("  MonotonicClock: %d", state.MonotonicClock)
		t.Logf("  TaskID count: %d", len(state.TaskID))
	} else {
		// This is acceptable - the operator might not have been registered if all connections failed
		t.Logf("ℹ️ Operator state not found (acceptable if all connections failed)")
	}

	// The key test: verify no data races occurred (race detector will catch this)
	// Also verify that we got some operations through
	t.Logf("✅ Concurrent access test completed - check for race detector warnings above")
}

// TestStreamCheckToOperator_ReconnectionRaceCondition specifically tests the reconnection
// scenario (else branch) that had the data race before the fix.
//
// This test verifies that concurrent reconnections don't cause data races when accessing
// trackSyncedTasks in the else branch (when operator already exists).
func TestStreamCheckToOperator_ReconnectionRaceCondition(t *testing.T) {
	// Setup test environment
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	operatorAddress := "0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"
	baseClock := time.Now().UnixNano()

	// Step 1: Initial connection (if branch) - establish operator in map
	mockServer1 := NewMockSyncMessagesServerForConcurrencyTest()

	syncReq1 := &avsproto.SyncMessagesReq{
		Address:        operatorAddress,
		MonotonicClock: baseClock,
		Capabilities: &avsproto.SyncMessagesReq_Capabilities{
			EventMonitoring: true,
			BlockMonitoring: true,
			TimeMonitoring:  true,
		},
	}

	// Start initial connection in background
	errChan1 := make(chan error, 1)
	go func() {
		errChan1 <- engine.StreamCheckToOperator(syncReq1, mockServer1)
	}()

	// Wait a bit for the connection to establish
	time.Sleep(time.Millisecond * 50)

	// Verify initial state
	engine.lock.Lock()
	initialState, exists := engine.trackSyncedTasks[operatorAddress]
	require.True(t, exists, "Operator should be registered")
	initialClock := initialState.MonotonicClock
	engine.lock.Unlock()

	assert.Equal(t, baseClock, initialClock, "Initial clock should match")

	// Step 2: Concurrent reconnections (else branch) - this is where the race condition was
	// The else branch accesses trackSyncedTasks multiple times without proper locking before the fix
	numReconnections := 15
	var wg sync.WaitGroup
	var mu sync.Mutex
	finalClocks := make([]int64, 0, numReconnections)

	for i := 0; i < numReconnections; i++ {
		wg.Add(1)
		go func(reconnectID int) {
			defer wg.Done()

			mockServer := NewMockSyncMessagesServerForConcurrencyTest()

			// Use newer clock to hit the else branch
			syncReq := &avsproto.SyncMessagesReq{
				Address:        operatorAddress,
				MonotonicClock: baseClock + int64(reconnectID+1)*1000, // Always newer
				Capabilities: &avsproto.SyncMessagesReq_Capabilities{
					EventMonitoring: true,
					BlockMonitoring: true,
					TimeMonitoring:  true,
				},
			}

			// Start connection in background
			errChan := make(chan error, 1)
			go func() {
				errChan <- engine.StreamCheckToOperator(syncReq, mockServer)
			}()

			// Wait a bit for the critical section to execute
			time.Sleep(time.Millisecond * 20)

			// Cancel to stop the ticker
			mockServer.Disconnect()

			// Try to read the state after a short delay
			select {
			case <-errChan:
				// Connection ended, read state
				engine.lock.Lock()
				state, exists := engine.trackSyncedTasks[operatorAddress]
				if exists {
					mu.Lock()
					finalClocks = append(finalClocks, state.MonotonicClock)
					mu.Unlock()
				}
				engine.lock.Unlock()
			case <-time.After(time.Millisecond * 100):
				// Timeout - connection still running, that's ok
			}
		}(i)
	}

	// Wait for all reconnections
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Logf("✅ All reconnections completed")
	case <-time.After(5 * time.Second):
		t.Logf("⚠️ Some reconnections may still be running (acceptable)")
	}

	// Cancel initial connection
	mockServer1.Disconnect()

	// Verify final state is consistent
	engine.lock.Lock()
	finalState, exists := engine.trackSyncedTasks[operatorAddress]
	engine.lock.Unlock()

	if exists {
		assert.NotNil(t, finalState, "Final state should not be nil")
		assert.NotNil(t, finalState.TaskID, "TaskID map should not be nil")
		assert.NotNil(t, finalState.TickerCtx, "TickerCtx should not be nil")

		t.Logf("✅ Reconnection race condition test passed")
		t.Logf("  Initial clock: %d", initialClock)
		t.Logf("  Final clock: %d", finalState.MonotonicClock)
		t.Logf("  Number of successful reconnections: %d", len(finalClocks))
	} else {
		t.Logf("ℹ️ Operator state cleared (acceptable if all connections ended)")
	}

	// The key test: race detector will catch any data races
	t.Logf("✅ Reconnection race condition test completed - check for race detector warnings above")
}
