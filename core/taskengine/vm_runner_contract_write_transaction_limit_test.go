package taskengine

import (
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// capturedPaymasterRequest stores the paymaster request for verification
var capturedPaymasterRequest *preset.VerifyingPaymasterRequest

func mockSendUserOp(
	config *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	paymasterReq *preset.VerifyingPaymasterRequest,
) (*userop.UserOperation, *types.Receipt, error) {
	capturedPaymasterRequest = paymasterReq
	return &userop.UserOperation{}, &types.Receipt{}, nil
}

/*
func TestTransactionSponsorshipLimit(t *testing.T) {
	testCases := []struct {
		name             string
		transactionCount uint64
		expectPaymaster  bool
		isWhitelisted    bool
	}{
		{"First transaction", 0, true, false}, // Note: 0 is the first transaction (0-indexed)
		{"5th transaction", 4, true, false},
		{"10th transaction", 9, true, false},
		{"11th transaction", 10, false, false}, // Note: 10 is the 11th transaction (0-indexed)
		{"20th transaction", 19, false, false},
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
				Task: &avsproto.Task{
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
				sendUserOpFunc:    mockSendUserOp,
			}

			capturedPaymasterRequest = nil

			processor.Execute("test", node)

			if tc.expectPaymaster {
				if capturedPaymasterRequest == nil {
					t.Errorf("Expected paymaster request for transaction %d, but got nil", tc.transactionCount)
					return
				}

				if capturedPaymasterRequest.PaymasterAddress != smartWalletConfig.PaymasterAddress {
					t.Errorf("Expected paymaster address %s, got %s",
						smartWalletConfig.PaymasterAddress.Hex(),
						capturedPaymasterRequest.PaymasterAddress.Hex())
				}

				if capturedPaymasterRequest.ValidUntil == nil {
					t.Errorf("Expected ValidUntil to be set, but it was nil")
				}

				if capturedPaymasterRequest.ValidAfter == nil {
					t.Errorf("Expected ValidAfter to be set, but it was nil")
				}

				now := time.Now().Unix()
				if capturedPaymasterRequest.ValidUntil.Int64() <= now {
					t.Errorf("Expected ValidUntil to be in the future, but it was %d (now: %d)",
						capturedPaymasterRequest.ValidUntil.Int64(), now)
				}

				if capturedPaymasterRequest.ValidUntil.Int64() > now+900+5 { // 15 minutes + 5 seconds buffer
					t.Errorf("Expected ValidUntil to be at most 15 minutes in the future, but it was %d (now: %d)",
						capturedPaymasterRequest.ValidUntil.Int64(), now)
				}

				if capturedPaymasterRequest.ValidAfter.Int64() > now {
					t.Errorf("Expected ValidAfter to be in the past or present, but it was %d (now: %d)",
						capturedPaymasterRequest.ValidAfter.Int64(), now)
				}
			} else {
				if capturedPaymasterRequest != nil {
					t.Errorf("Expected no paymaster request for transaction %d, but got one", tc.transactionCount)
				}
			}
		})
	}
}
*/
