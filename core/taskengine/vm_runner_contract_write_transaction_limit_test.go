package taskengine

import (
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

var originalSendUserOp = preset.SendUserOp

type mockSendUserOpFunc func(
	config *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	paymasterReq *preset.VerifyingPaymasterRequest,
) (*userop.UserOperation, *types.Receipt, error)

func replaceSendUserOp(mock mockSendUserOpFunc) func() {
	originalSendUserOp = preset.SendUserOp
	preset.SendUserOp = mock
	return func() {
		preset.SendUserOp = originalSendUserOp
	}
}
func TestTransactionSponsorshipLimit(t *testing.T) {
	testCases := []struct {
		name            string
		transactionCount uint64
		expectPaymaster  bool
		isWhitelisted    bool
	}{
		{"First transaction", 0, false, false},
		{"5th transaction", 4, false, false},
		{"10th transaction", 9, false, false},
		{"11th transaction", 10, true, false},  // Note: 10 is the 11th transaction (0-indexed)
		{"20th transaction", 19, true, false},
		{"Whitelisted address with 1 transaction", 1, true, true},
		{"Whitelisted address with 11 transactions", 11, true, true},
		{"Whitelisted address with 20 transactions", 20, true, true},
	}

	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")
	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	
	contractAddress := common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e")
	node := &avsproto.ContractWriteNode{
		ContractAddress: contractAddress.Hex(),
		CallData:        "0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e8",
	}

	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)

	if err != nil {
		t.Fatalf("error connecting to websocket: %v", err)
	}
	defer client.Close()

	for i, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.TestMustDB()
			defer storage.Destroy(db.(*storage.BadgerStorage))
			
			if tc.isWhitelisted {
				smartWalletConfig.WhitelistAddresses = []common.Address{owner}
			} else {
				smartWalletConfig.WhitelistAddresses = []common.Address{}
			}

			vm, _ := NewVMWithData(&model.Task{
				&avsproto.Task{
					Id: fmt.Sprintf("test-%d", i),
					Trigger: &avsproto.TaskTrigger{
						Id:   "triggertest",
						Name: "triggertest",
					},
					Nodes: []*avsproto.TaskNode{
						{
							Id:   "transfer1",
							Name: "usdcTransfer",
							TaskType: &avsproto.TaskNode_ContractWrite{
								ContractWrite: node,
							},
						},
					},
				},
			}, nil, smartWalletConfig, nil)

			vm.WithDb(db)

			counterKey := ContractWriteCounterKey(owner)
			for i := uint64(0); i < tc.transactionCount; i++ {
				_, err := db.IncCounter(counterKey, 0)
				if err != nil {
					t.Fatalf("Failed to increment counter: %v", err)
				}
			}

			processor := &ContractWriteProcessor{
				CommonProcessor: &CommonProcessor{
					vm: vm,
				},
				client:            client,
				owner:             owner,
				smartWalletConfig: smartWalletConfig,
			}

			var capturedPaymaster *preset.VerifyingPaymasterRequest
			
			restore := replaceSendUserOp(func(
				config *config.SmartWalletConfig,
				owner common.Address,
				callData []byte,
				paymasterReq *preset.VerifyingPaymasterRequest,
			) (*userop.UserOperation, *types.Receipt, error) {
				capturedPaymaster = paymasterReq
				return &userop.UserOperation{}, &types.Receipt{}, nil
			})
			defer restore() // Restore the original function after the test

			processor.Execute("test", node)
			
			if tc.expectPaymaster {
				if capturedPaymaster == nil {
					t.Errorf("Expected paymaster request for transaction %d, but got nil", tc.transactionCount)
					return
				}
				
				if capturedPaymaster.PaymasterAddress != smartWalletConfig.PaymasterAddress {
					t.Errorf("Expected paymaster address %s, got %s", 
						smartWalletConfig.PaymasterAddress.Hex(), 
						capturedPaymaster.PaymasterAddress.Hex())
				}
				
				if capturedPaymaster.ValidUntil == nil {
					t.Errorf("Expected ValidUntil to be set, but it was nil")
				}
				
				if capturedPaymaster.ValidAfter == nil {
					t.Errorf("Expected ValidAfter to be set, but it was nil")
				}
				
				now := time.Now().Unix()
				if capturedPaymaster.ValidUntil.Int64() <= now {
					t.Errorf("Expected ValidUntil to be in the future, but it was %d (now: %d)", 
						capturedPaymaster.ValidUntil.Int64(), now)
				}
				
				if capturedPaymaster.ValidUntil.Int64() > now+600+5 { // 10 minutes + 5 seconds buffer
					t.Errorf("Expected ValidUntil to be at most 10 minutes in the future, but it was %d (now: %d)", 
						capturedPaymaster.ValidUntil.Int64(), now)
				}
				
				if capturedPaymaster.ValidAfter.Int64() > now {
					t.Errorf("Expected ValidAfter to be in the past or present, but it was %d (now: %d)", 
						capturedPaymaster.ValidAfter.Int64(), now)
				}
			} else {
				if capturedPaymaster != nil {
					t.Errorf("Expected no paymaster request for transaction %d, but got one", tc.transactionCount)
				}
			}
		})
	}
}
