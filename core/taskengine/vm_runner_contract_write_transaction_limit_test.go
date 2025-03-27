package taskengine

import (
	"math/big"
	"testing"
	"time"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/AvaProtocol/ap-avs/core/testutil"
	"github.com/AvaProtocol/ap-avs/pkg/erc4337/preset"
	"github.com/AvaProtocol/ap-avs/pkg/erc4337/userop"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type MockStorage struct {
	mock.Mock
}

func (m *MockStorage) GetCounter(key []byte, defaultValue ...uint64) (uint64, error) {
	args := m.Called(key, defaultValue)
	return args.Get(0).(uint64), args.Error(1)
}

func (m *MockStorage) IncCounter(key []byte, defaultValue ...uint64) (uint64, error) {
	args := m.Called(key, defaultValue)
	return args.Get(0).(uint64), args.Error(1)
}

func (m *MockStorage) Setup() error { return nil }
func (m *MockStorage) Close() error { return nil }
func (m *MockStorage) GetSequence(prefix []byte, inflightItem uint64) (interface{}, error) { return nil, nil }
func (m *MockStorage) Exist(key []byte) (bool, error) { return false, nil }
func (m *MockStorage) GetKey(key []byte) ([]byte, error) { return nil, nil }
func (m *MockStorage) GetByPrefix(prefix []byte) (interface{}, error) { return nil, nil }
func (m *MockStorage) GetKeyHasPrefix(prefix []byte) ([][]byte, error) { return nil, nil }
func (m *MockStorage) FirstKVHasPrefix(prefix []byte) ([]byte, []byte, error) { return nil, nil, nil }
func (m *MockStorage) ListKeys(prefix string) ([]string, error) { return nil, nil }
func (m *MockStorage) ListKeysMulti(prefixes []string) ([]string, error) { return nil, nil }
func (m *MockStorage) CountKeysByPrefix(prefix []byte) (int64, error) { return 0, nil }
func (m *MockStorage) CountKeysByPrefixes(prefixes [][]byte) (int64, error) { return 0, nil }
func (m *MockStorage) BatchWrite(updates map[string][]byte) error { return nil }
func (m *MockStorage) Move(src, dest []byte) error { return nil }
func (m *MockStorage) Set(key, value []byte) error { return nil }
func (m *MockStorage) Delete(key []byte) error { return nil }
func (m *MockStorage) Vacuum() error { return nil }
func (m *MockStorage) DbPath() string { return "" }

func TestTransactionSponsorshipLimit(t *testing.T) {
	origSendUserOp := preset.SendUserOp
	defer func() { preset.SendUserOp = origSendUserOp }()

	testCases := []struct {
		name            string
		transactionCount uint64
		expectPaymaster bool
	}{
		{"First transaction", 0, false},
		{"5th transaction", 4, false},
		{"10th transaction", 9, false},
		{"11th transaction", 10, true},  // Note: 10 is the 11th transaction (0-indexed)
		{"20th transaction", 19, true},
	}

	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")
	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	
	contractAddress := common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e")
	node := &avsproto.ContractWriteNode{
		ContractAddress: contractAddress.Hex(),
		CallData:        "0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e8",
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockStorage := new(MockStorage)
			
			vm := &VM{db: mockStorage}

			counterKey := ContractWriteCounterKey(owner)
			mockStorage.On("GetCounter", counterKey, []uint64{0}).Return(tc.transactionCount, nil)
			mockStorage.On("IncCounter", counterKey, []uint64{0}).Return(tc.transactionCount + 1, nil)

			processor := &ContractWriteProcessor{
				CommonProcessor: &CommonProcessor{
					vm: vm,
				},
				owner:             owner,
				smartWalletConfig: smartWalletConfig,
			}

			var capturedPaymaster *preset.VerifyingPaymasterRequest
			
			preset.SendUserOp = func(
				config *config.SmartWalletConfig,
				owner common.Address,
				callData []byte,
				paymasterReq *preset.VerifyingPaymasterRequest,
			) (*userop.UserOperation, *types.Receipt, error) {
				capturedPaymaster = paymasterReq
				return &userop.UserOperation{}, &types.Receipt{}, nil
			}

			processor.Execute("test", node)

			mockStorage.AssertExpectations(t)
			
			if tc.expectPaymaster {
				assert.NotNil(t, capturedPaymaster, "Expected paymaster request for transaction %d", tc.transactionCount)
				assert.Equal(t, smartWalletConfig.PaymasterAddress, capturedPaymaster.PaymasterAddress)
				assert.NotNil(t, capturedPaymaster.ValidUntil)
				assert.NotNil(t, capturedPaymaster.ValidAfter)
				now := time.Now().Unix()
				assert.True(t, capturedPaymaster.ValidUntil.Int64() > now)
				assert.True(t, capturedPaymaster.ValidUntil.Int64() <= now+600+5) // 10 minutes + 5 seconds buffer
				assert.True(t, capturedPaymaster.ValidAfter.Int64() <= now)
			} else {
				assert.Nil(t, capturedPaymaster, "Expected no paymaster request for transaction %d", tc.transactionCount)
			}
		})
	}
}
