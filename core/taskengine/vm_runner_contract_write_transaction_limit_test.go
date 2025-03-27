package taskengine

import (
	"testing"
	//"time"
	//"math/big"
	"fmt"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/testutil"

	// "github.com/AvaProtocol/ap-avs/pkg/erc4337/preset"
	// "github.com/AvaProtocol/ap-avs/pkg/erc4337/userop"

	"github.com/AvaProtocol/ap-avs/model"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/AvaProtocol/ap-avs/storage"
)

func TestTransactionSponsorshipLimit(t *testing.T) {
	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	testCases := []struct {
		name             string
		transactionCount uint64
		expectPaymaster  bool
	}{
		{"First transaction", 1, true},
		{"5th transaction", 4, true},
		{"10th transaction", 9, true},
		{"11th transaction", 11, false},
		{"20th transaction", 19, false},
	}

	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")

	baseSepoliaUsdcAddress := common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e")
	node := &avsproto.ContractWriteNode{
		ContractAddress: baseSepoliaUsdcAddress.Hex(),
		CallData:        "0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e80000000000000000000000000000000000000000000000000000000",
	}

	for i, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.TestMustDB()
			defer storage.Destroy(db.(*storage.BadgerStorage))

			//vm := NewVM().WithDb(db)
			vm, _ := NewVMWithData(&model.Task{
				&avsproto.Task{
					Id: fmt.Sprintf("test-%d", i),
					Trigger: &avsproto.TaskTrigger{
						Id:   "triggertest",
						Name: "triggertest",
					},
					Nodes: []*avsproto.TaskNode{
						&avsproto.TaskNode{
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
				err := db.SetCounter(counterKey, tc.transactionCount)
				if err != nil {
					t.Fatalf("Failed to set counter: %v", err)
				}
			}

			processor := &ContractWriteProcessor{
				CommonProcessor: &CommonProcessor{
					vm: vm,
				},
				owner:             owner,
				smartWalletConfig: smartWalletConfig,
			}

			step, err := processor.Execute("transfer1", node)
			if err != nil {
				t.Fatalf("Failed to execute step: %v", err)
			}
			capturedPaymaster := step.OutputData.(*avsproto.Execution_Step_ContractWrite).ContractWrite.UserOp.PaymasterAndData

			if tc.expectPaymaster {

				if len(capturedPaymaster) <= 20 {
					t.Errorf("Expected paymaster request for transaction %d, but got nil", tc.transactionCount)
					return
				}

				// if capturedPaymaster.PaymasterAddress != smartWalletConfig.PaymasterAddress {
				// 	t.Errorf("Expected paymaster address %s, got %s",
				// 		smartWalletConfig.PaymasterAddress.Hex(),
				// 		capturedPaymaster.PaymasterAddress.Hex())
				// }

				// if capturedPaymaster.ValidUntil == nil {
				// 	t.Errorf("Expected ValidUntil to be set, but it was nil")
				// }

				// if capturedPaymaster.ValidAfter == nil {
				// 	t.Errorf("Expected ValidAfter to be set, but it was nil")
				// }

				// now := time.Now().Unix()
				// if capturedPaymaster.ValidUntil.Int64() <= now {
				// 	t.Errorf("Expected ValidUntil to be in the future, but it was %d (now: %d)",
				// 		capturedPaymaster.ValidUntil.Int64(), now)
				// }

				// if capturedPaymaster.ValidUntil.Int64() > now+600+5 { // 10 minutes + 5 seconds buffer
				// 	t.Errorf("Expected ValidUntil to be at most 10 minutes in the future, but it was %d (now: %d)",
				// 		capturedPaymaster.ValidUntil.Int64(), now)
				// }

				// if capturedPaymaster.ValidAfter.Int64() > now {
				// 	t.Errorf("Expected ValidAfter to be in the past or present, but it was %d (now: %d)",
				// 		capturedPaymaster.ValidAfter.Int64(), now)
				// }
			} else {
				if len(capturedPaymaster) > 0 {
					t.Errorf("Expected no paymaster request for transaction %d, but got one", tc.transactionCount)
				}
			}
		})
	}
}
