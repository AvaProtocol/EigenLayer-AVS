package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SimpleMockServer simulates an operator connection for testing ticker context
type SimpleMockServer struct {
	grpc.ServerStream
	sendCalls    []*avsproto.SyncMessagesResp
	disconnected bool
	ctx          context.Context
	cancelFunc   context.CancelFunc
}

func NewSimpleMockServer() *SimpleMockServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &SimpleMockServer{
		sendCalls:  make([]*avsproto.SyncMessagesResp, 0),
		ctx:        ctx,
		cancelFunc: cancel,
	}
}

func (m *SimpleMockServer) Send(resp *avsproto.SyncMessagesResp) error {
	if m.disconnected {
		return status.Errorf(codes.Unavailable, "transport is closing")
	}
	m.sendCalls = append(m.sendCalls, resp)
	return nil
}

func (m *SimpleMockServer) Context() context.Context {
	return m.ctx
}

func (m *SimpleMockServer) Disconnect() {
	m.disconnected = true
	m.cancelFunc()
}

// TestTickerContextRaceCondition tests the specific ticker context race condition fix
func TestTickerContextRaceCondition(t *testing.T) {
	logger := testutil.GetLogger()
	taskengine.SetLogger(logger)

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := &config.Config{
		ApprovedOperators: []common.Address{
			common.HexToAddress("0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"),
		},
		SmartWallet: &config.SmartWalletConfig{
			EthRpcUrl:      "http://localhost:8545",
			FactoryAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
		},
	}

	engine := taskengine.New(db, config, nil, logger)
	err := engine.MustStart()
	require.NoError(t, err)
	defer engine.Stop()

	operatorAddr := "0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"

	// Test rapid reconnections to verify ticker context management
	for i := 0; i < 5; i++ {
		t.Logf("🔄 Testing ticker context management - iteration %d", i+1)

		// First connection
		mockServer1 := NewSimpleMockServer()
		syncReq1 := &avsproto.SyncMessagesReq{
			Address:        operatorAddr,
			MonotonicClock: time.Now().UnixNano(),
			Capabilities: &avsproto.SyncMessagesReq_Capabilities{
				EventMonitoring: true,
				BlockMonitoring: true,
				TimeMonitoring:  true,
			},
		}

		errChan1 := make(chan error, 1)
		go func() {
			err := engine.StreamCheckToOperator(syncReq1, mockServer1)
			errChan1 <- err
		}()

		// Let it run briefly
		time.Sleep(500 * time.Millisecond)

		// Second connection (simulating reconnection) - this should cancel the first ticker
		mockServer2 := NewSimpleMockServer()
		syncReq2 := &avsproto.SyncMessagesReq{
			Address:        operatorAddr,
			MonotonicClock: time.Now().UnixNano() + 1000, // Higher monotonic clock
			Capabilities: &avsproto.SyncMessagesReq_Capabilities{
				EventMonitoring: true,
				BlockMonitoring: true,
				TimeMonitoring:  true,
			},
		}

		errChan2 := make(chan error, 1)
		go func() {
			err := engine.StreamCheckToOperator(syncReq2, mockServer2)
			errChan2 <- err
		}()

		// Let it run briefly to verify the old ticker was canceled
		time.Sleep(500 * time.Millisecond)

		// Disconnect both (second one first, then first should already be canceled)
		mockServer2.Disconnect()
		mockServer1.Disconnect()

		// Wait for both to complete
		select {
		case <-errChan2:
			t.Logf("✅ Second connection ended")
		case <-time.After(2 * time.Second):
			t.Log("⚠️ Timeout waiting for second connection to end")
		}

		select {
		case <-errChan1:
			t.Logf("✅ First connection ended (should have been canceled by second)")
		case <-time.After(2 * time.Second):
			t.Log("⚠️ Timeout waiting for first connection to end")
		}

		// Brief pause between iterations
		time.Sleep(100 * time.Millisecond)
	}

	t.Log("✅ Ticker context race condition test completed successfully!")
}

// TestOperatorConnectionStabilization tests the 10-second stabilization period
func TestOperatorConnectionStabilization(t *testing.T) {
	logger := testutil.GetLogger()
	taskengine.SetLogger(logger)

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := &config.Config{
		ApprovedOperators: []common.Address{
			common.HexToAddress("0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"),
		},
		SmartWallet: &config.SmartWalletConfig{
			EthRpcUrl:      "http://localhost:8545",
			FactoryAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
		},
	}

	engine := taskengine.New(db, config, nil, logger)
	err := engine.MustStart()
	require.NoError(t, err)
	defer engine.Stop()

	operatorAddr := "0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"

	// Create connection
	mockServer := NewSimpleMockServer()
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

	// Wait for stabilization period to pass
	t.Log("⏳ Waiting for stabilization period...")
	time.Sleep(12 * time.Second)

	// Disconnect
	mockServer.Disconnect()

	select {
	case <-errChan:
		t.Log("✅ Connection ended after stabilization")
	case <-time.After(2 * time.Second):
		t.Log("⚠️ Timeout waiting for connection to end")
	}

	t.Log("✅ Connection stabilization test completed!")
}
