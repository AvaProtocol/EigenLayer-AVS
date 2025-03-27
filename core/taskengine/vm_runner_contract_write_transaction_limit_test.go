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
	"github.com/AvaProtocol/ap-avs/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)



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
			db := testutil.TestMustDB()
			defer storage.Destroy(db.(*storage.BadgerStorage))
			
			vm := NewVM().WithDb(db)
			vm.vars = make(map[string]any) // Ensure vars is initialized

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
