package taskengine

import (
	"context"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wellFormedUserOp returns a UserOperation with every big.Int/byte field populated
// so GetUserOpHash / PackForSignature don't panic in unit tests.
func wellFormedUserOp(sender common.Address) *userop.UserOperation {
	return &userop.UserOperation{
		Sender:               sender,
		Nonce:                big.NewInt(0),
		InitCode:             []byte{},
		CallData:             []byte{},
		CallGasLimit:         big.NewInt(0),
		VerificationGasLimit: big.NewInt(0),
		PreVerificationGas:   big.NewInt(0),
		MaxFeePerGas:         big.NewInt(0),
		MaxPriorityFeePerGas: big.NewInt(0),
		PaymasterAndData:     []byte{},
		Signature:            []byte{},
	}
}

// ondemandTestConfig builds a smart-wallet config sufficient to drive the real
// UserOp path in-process. PaymasterAddress is left zero so shouldUsePaymaster()
// short-circuits to self-funded (no bundler gas estimation needed under the mock).
func ondemandTestConfig() *config.SmartWalletConfig {
	return &config.SmartWalletConfig{
		EntrypointAddress: common.HexToAddress(config.DefaultEntrypointAddressHex),
		FactoryAddress:    common.HexToAddress("0x0000000000000000000000000000000000009999"),
		ChainID:           11155111,
	}
}

func minedReceipt(status uint64) *types.Receipt {
	return &types.Receipt{
		Status:            status,
		TxHash:            common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000ab"),
		BlockHash:         common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000de"),
		BlockNumber:       big.NewInt(100),
		TransactionIndex:  0,
		GasUsed:           21000,
		CumulativeGasUsed: 21000,
		EffectiveGasPrice: big.NewInt(1_000_000_000),
		Type:              2,
	}
}

// G2/G3: createRealTransactionResult must expose an unambiguous executionStatus
// (confirmed/failed/pending) plus a userOpHash, so the chat preview→confirm→execute
// flow can tell a mined success from a revert from a still-pending UserOp instead
// of collapsing pending into failure.
func TestCreateRealTransactionResultExecutionStatus(t *testing.T) {
	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	runner := common.HexToAddress("0x2222222222222222222222222222222222222222")
	contractAddr := common.HexToAddress("0x1c7d4b196cb0c7b01d743fbc6116a902379c7238")
	swConfig := ondemandTestConfig()

	vm, err := NewVMWithData(nil, nil, swConfig, nil)
	require.NoError(t, err)
	vm.AddVar("aa_sender", runner.Hex())

	processor := NewContractWriteProcessor(vm, nil, swConfig, owner)
	userOp := wellFormedUserOp(runner)

	receiptMapOf := func(t *testing.T, mr *avsproto.ContractWriteNode_MethodResult) map[string]any {
		require.NotNil(t, mr.Receipt)
		m, ok := mr.Receipt.AsInterface().(map[string]any)
		require.True(t, ok)
		return m
	}

	t.Run("mined success -> confirmed", func(t *testing.T) {
		mr := processor.createRealTransactionResult("transfer", contractAddr.Hex(), "0x", nil, userOp, minedReceipt(1))
		assert.True(t, mr.Success)
		assert.Empty(t, mr.Error)
		rm := receiptMapOf(t, mr)
		assert.Equal(t, "confirmed", rm["executionStatus"])
		assert.NotEmpty(t, rm["userOpHash"], "mined result must carry a userOpHash")
	})

	t.Run("mined revert -> failed", func(t *testing.T) {
		mr := processor.createRealTransactionResult("transfer", contractAddr.Hex(), "0x", nil, userOp, minedReceipt(0))
		assert.False(t, mr.Success)
		assert.NotEmpty(t, mr.Error)
		rm := receiptMapOf(t, mr)
		assert.Equal(t, "failed", rm["executionStatus"])
	})

	t.Run("submitted but not mined -> pending, not failure", func(t *testing.T) {
		mr := processor.createRealTransactionResult("transfer", contractAddr.Hex(), "0x", nil, userOp, nil)
		assert.False(t, mr.Success, "pending is not confirmed-success")
		assert.Empty(t, mr.Error, "pending must not be reported as an error")
		rm := receiptMapOf(t, mr)
		assert.Equal(t, "pending", rm["executionStatus"])
		assert.Equal(t, "pending", rm["transactionHash"])
		assert.NotEmpty(t, rm["userOpHash"], "pending result must carry a userOpHash to poll")
	})
}

// G1: the real UserOp path must forward the node's native ETH value into the
// packed execute(target, value, data) calldata, matching the simulation path.
// It previously hardcoded 0, so a payable call (e.g. wrapping ETH or an ETH-in
// swap) previewed correctly then executed with 0 wei.
func TestContractWriteRealPathForwardsNativeValue(t *testing.T) {
	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	runner := common.HexToAddress("0x2222222222222222222222222222222222222222")
	contractAddr := common.HexToAddress("0x1c7d4b196cb0c7b01d743fbc6116a902379c7238")
	swConfig := ondemandTestConfig()

	// A valid ERC20 transfer calldata (>= 4 bytes) — the inner call is irrelevant
	// here; we only assert on the value word of the outer execute() wrapper.
	transferCallData := "0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a00000000000000000000000000000000000000000000000000000000000003e8"

	// run executes a single real contractWrite method call with the given config
	// value and returns the value word decoded from the packed execute() calldata.
	run := func(t *testing.T, valueWei string) *big.Int {
		vm, err := NewVMWithData(nil, nil, swConfig, nil)
		require.NoError(t, err)
		vm.AddVar("aa_sender", runner.Hex())

		processor := NewContractWriteProcessor(vm, nil, swConfig, owner)

		var capturedCallData []byte
		processor.sendUserOpFunc = func(
			_ *config.SmartWalletConfig,
			_ common.Address,
			callData []byte,
			_ *preset.VerifyingPaymasterRequest,
			_ *common.Address,
			_ *big.Int,
			_ *big.Int,
			_ logger.Logger,
		) (*userop.UserOperation, *types.Receipt, error) {
			capturedCallData = callData
			return wellFormedUserOp(runner), minedReceipt(1), nil
		}

		cfg := &avsproto.ContractWriteNode_Config{
			ContractAddress: contractAddr.Hex(),
			ChainId:         swConfig.ChainID,
		}
		if valueWei != "" {
			cfg.Value = &valueWei
		}
		node := &avsproto.ContractWriteNode{Config: cfg}

		callData := transferCallData
		methodCall := &avsproto.ContractWriteNode_MethodCall{
			MethodName: "transfer",
			CallData:   &callData,
		}

		mr := processor.executeMethodCall(context.Background(), nil, "", contractAddr, methodCall, false, node)
		require.NotNil(t, mr)

		// execute(address dest, uint256 value, bytes data): after the 4-byte
		// selector, word 0 is dest and word 1 (bytes 36:68) is the ETH value.
		require.GreaterOrEqual(t, len(capturedCallData), 68, "expected packed execute(target,value,data) calldata")
		return new(big.Int).SetBytes(capturedCallData[36:68])
	}

	t.Run("config value is forwarded", func(t *testing.T) {
		got := run(t, "1230000000000000000") // 1.23 ETH
		assert.Equal(t, "1230000000000000000", got.String())
	})

	t.Run("unset value defaults to zero", func(t *testing.T) {
		got := run(t, "")
		assert.Equal(t, "0", got.String())
	})
}
