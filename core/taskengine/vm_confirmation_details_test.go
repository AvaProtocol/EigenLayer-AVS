package taskengine

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

const confirmationTestHash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const confirmationTestOtherHash = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func confirmationTestVM(t *testing.T, reply func(string) any) *VM {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": reply(request.Method)})
	}))
	t.Cleanup(server.Close)
	vm := NewVM().WithLogger(testutil.GetLogger())
	vm.smartWalletConfig = &config.SmartWalletConfig{ChainID: 13371337, BundlerProvider: config.BundlerProviderSelfHosted, BundlerURL: server.URL, EthRpcUrl: server.URL}
	return vm
}

func pendingConfirmationStep(t *testing.T) *avsproto.Execution_Step {
	t.Helper()
	metadata, err := structpb.NewValue([]any{
		map[string]any{"methodName": "approve", "success": false, "receipt": map[string]any{"status": "pending", "userOpHash": confirmationTestHash}},
		map[string]any{"methodName": "supply", "success": false, "receipt": map[string]any{"status": "pending", "userOpHash": confirmationTestOtherHash}},
	})
	require.NoError(t, err)
	return &avsproto.Execution_Step{Id: "write", Name: "write", Metadata: metadata}
}

func TestConfirmationTimeoutKeepsPendingSiblingsAndCode(t *testing.T) {
	var polls atomic.Int32
	vm := confirmationTestVM(t, func(method string) any {
		if method == "eth_getUserOperationReceipt" {
			polls.Add(1)
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	step := pendingConfirmationStep(t)
	start := time.Now()
	result, err := vm.waitForUserOpConfirmationContext(ctx, confirmationTestHash)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, result)
	vm.markUserOpConfirmationUnresolved(step)
	require.Less(t, time.Since(start), time.Second, "a backoff must not sleep past the confirmation deadline")
	require.Greater(t, polls.Load(), int32(0), "exercise the real receipt-poll loop")
	require.Equal(t, avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED, step.ErrorCode)
	require.False(t, step.Success)
	methods := step.Metadata.AsInterface().([]any)
	for _, raw := range methods {
		require.Equal(t, "pending", raw.(map[string]any)["receipt"].(map[string]any)["status"])
	}
	require.Equal(t, confirmationTestHash, methods[0].(map[string]any)["receipt"].(map[string]any)["userOpHash"])
	require.Equal(t, confirmationTestOtherHash, methods[1].(map[string]any)["receipt"].(map[string]any)["userOpHash"])
}

func TestConfirmationOuterTransactionRevert(t *testing.T) {
	txHash := common.HexToHash(confirmationTestOtherHash)
	vm := confirmationTestVM(t, func(method string) any {
		switch method {
		case "eth_getUserOperationReceipt":
			return map[string]any{"success": true, "receipt": map[string]any{"transactionHash": txHash.Hex(), "status": "0x1"}}
		case "eth_getTransactionReceipt":
			return &types.Receipt{Status: types.ReceiptStatusFailed, TxHash: txHash, BlockNumber: big.NewInt(10), Logs: []*types.Log{}, EffectiveGasPrice: big.NewInt(1), GasUsed: 21000}
		}
		return nil
	})
	step := pendingConfirmationStep(t)
	// Only one submitted operation in this case.
	methods := step.Metadata.AsInterface().([]any)
	step.Metadata, _ = structpb.NewValue(methods[:1])
	require.True(t, vm.waitForOnChainConfirmationIfNeeded(step))
	require.False(t, step.Success)
	require.Equal(t, avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED, step.ErrorCode)
	require.Contains(t, step.Error, "outer transaction reverted")
}

func TestConfirmationSurvivesBlockingRequestDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls atomic.Int32
	txHash := common.HexToHash(confirmationTestOtherHash)
	vm := confirmationTestVM(t, func(method string) any {
		switch method {
		case "eth_getUserOperationReceipt":
			if polls.Add(1) == 1 {
				cancel() // The client disconnects while the submitted op is pending.
				return nil
			}
			return map[string]any{"success": true, "receipt": map[string]any{"transactionHash": txHash.Hex(), "status": "0x1"}}
		case "eth_getTransactionReceipt":
			return &types.Receipt{Status: types.ReceiptStatusSuccessful, TxHash: txHash, BlockNumber: big.NewInt(10), Logs: []*types.Log{}, EffectiveGasPrice: big.NewInt(1), GasUsed: 21000}
		}
		return nil
	})
	vm.WithContext(ctx)
	step := pendingConfirmationStep(t)
	methods := step.Metadata.AsInterface().([]any)
	step.Metadata, _ = structpb.NewValue(methods[:1])
	require.True(t, vm.waitForOnChainConfirmationIfNeeded(step))
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.EqualValues(t, 2, polls.Load())
	require.True(t, step.Success, "a later confirmation must not become unresolved because the client disconnected")
	require.Empty(t, step.Error)
	require.Equal(t, avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED, step.ErrorCode)
}

func confirmationRevertLog(t *testing.T, entrypoint common.Address, hash string, data []byte) *types.Log {
	t.Helper()
	event, err := aa.UserOperationRevertReasonEvent()
	require.NoError(t, err)
	encoded, err := event.Inputs.NonIndexed().Pack(big.NewInt(1), data)
	require.NoError(t, err)
	return &types.Log{Address: entrypoint, Topics: []common.Hash{event.ID, common.HexToHash(hash), {}}, Data: encoded}
}

func TestConfirmedRevertReasonMatchesOperationAndEntryPoint(t *testing.T) {
	entrypoint := common.HexToAddress("0x1111111111111111111111111111111111111111")
	stringType, err := abi.NewType("string", "", nil)
	require.NoError(t, err)
	packed, err := (abi.Arguments{{Type: stringType}}).Pack("allowance too low")
	require.NoError(t, err)
	data := append(crypto.Keccak256([]byte("Error(string)"))[:4], packed...)
	own := confirmationRevertLog(t, entrypoint, confirmationTestHash, data)
	other := confirmationRevertLog(t, entrypoint, confirmationTestOtherHash, data)
	forged := confirmationRevertLog(t, common.HexToAddress("0x2222222222222222222222222222222222222222"), confirmationTestHash, data)
	for _, matching := range []bool{false, true} {
		logs := []*types.Log{other, forged}
		if matching {
			logs = append(logs, own)
		}
		for _, onChain := range []bool{false, true} {
			rawLogs, err := json.Marshal(logs)
			require.NoError(t, err)
			var bundlerLogs []interface{}
			require.NoError(t, json.Unmarshal(rawLogs, &bundlerLogs))
			result := &waitForUserOpConfirmationResult{Fields: map[string]any{"logs": bundlerLogs}}
			if onChain {
				result.Receipt = &types.Receipt{Status: types.ReceiptStatusSuccessful, Logs: logs}
			}
			reason, raw := confirmedUserOpFailure(confirmationTestHash, entrypoint, result)
			if matching {
				require.Contains(t, reason, "allowance too low")
				require.Equal(t, "0x"+hex.EncodeToString(data), raw)
			} else {
				require.NotContains(t, reason, "allowance too low", "another operation or contract cannot supply the reason")
				require.Empty(t, raw)
			}
		}
	}
}

func TestConfirmedCustomRevertPreservesData(t *testing.T) {
	entrypoint := common.HexToAddress("0x1111111111111111111111111111111111111111")
	log := confirmationRevertLog(t, entrypoint, confirmationTestHash, []byte{0xde, 0xad, 0xbe, 0xef})
	reason, raw := confirmedUserOpFailure(confirmationTestHash, entrypoint, &waitForUserOpConfirmationResult{
		Receipt: &types.Receipt{Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{log}},
	})
	require.Contains(t, reason, "execution reverted with data 0xdeadbeef")
	require.Equal(t, "0xdeadbeef", raw)
}

func TestConfirmationDecodedRevertReachesExecutionStep(t *testing.T) {
	var log *types.Log
	txHash := common.HexToHash(confirmationTestOtherHash)
	vm := confirmationTestVM(t, func(method string) any {
		switch method {
		case "eth_getUserOperationReceipt":
			return map[string]any{"success": false, "receipt": map[string]any{"transactionHash": txHash.Hex(), "status": "0x1"}}
		case "eth_getTransactionReceipt":
			return &types.Receipt{Status: types.ReceiptStatusSuccessful, TxHash: txHash, BlockNumber: big.NewInt(10), Logs: []*types.Log{log}, EffectiveGasPrice: big.NewInt(1), GasUsed: 21000}
		}
		return nil
	})
	stringType, err := abi.NewType("string", "", nil)
	require.NoError(t, err)
	packed, err := (abi.Arguments{{Type: stringType}}).Pack("allowance too low")
	require.NoError(t, err)
	data := append(crypto.Keccak256([]byte("Error(string)"))[:4], packed...)
	log = confirmationRevertLog(t, vm.smartWalletConfig.EntryPointAddress(), confirmationTestHash, data)
	step := pendingConfirmationStep(t)
	methods := step.Metadata.AsInterface().([]any)
	step.Metadata, _ = structpb.NewValue(methods[:1])

	require.True(t, vm.waitForOnChainConfirmationIfNeeded(step))
	require.Equal(t, avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED, step.ErrorCode)
	require.Contains(t, step.Error, "allowance too low")
	receipt := step.Metadata.AsInterface().([]any)[0].(map[string]any)["receipt"].(map[string]any)
	require.Equal(t, "0x"+hex.EncodeToString(data), receipt["revertData"])
	wire, err := protojson.Marshal(step)
	require.NoError(t, err)
	require.Contains(t, string(wire), "USER_OP_EXECUTION_REVERTED", "the Studio consumer receives the enum name")
}

func TestConfirmedSuccessDoesNotRelabelAnEarlierRPCFailureAsReverted(t *testing.T) {
	step := pendingConfirmationStep(t)
	methods := step.Metadata.AsInterface().([]any)
	methods[1] = map[string]any{"methodName": "supply", "success": false, "error": "RPC service unavailable"}
	step.Metadata, _ = structpb.NewValue(methods)
	updateConfirmedWriteMetadata(step, confirmationTestHash, &waitForUserOpConfirmationResult{Success: true})
	require.False(t, step.Success)
	require.Contains(t, step.Error, "RPC service unavailable")
	require.NotEqual(t, avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED, step.ErrorCode)
}

func TestConfirmedRevertCodeAndMessageSelectTheSameCall(t *testing.T) {
	step := pendingConfirmationStep(t)
	methods := step.Metadata.AsInterface().([]any)
	methods[0] = map[string]any{"methodName": "approve", "success": false, "error": "RPC service unavailable"}
	step.Metadata, _ = structpb.NewValue(methods)
	updateConfirmedWriteMetadata(step, confirmationTestOtherHash, &waitForUserOpConfirmationResult{
		Success: false, FailureReason: "execution reverted: rejected supply", RevertData: "0xdeadbeef",
	})
	require.Equal(t, avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED, step.ErrorCode)
	require.Equal(t, "execution reverted: rejected supply", step.Error)
	require.Equal(t, "RPC service unavailable", step.Metadata.AsInterface().([]any)[0].(map[string]any)["error"])
}

func TestConfirmedPanicReason(t *testing.T) {
	entrypoint := common.HexToAddress("0x1111111111111111111111111111111111111111")
	uintType, err := abi.NewType("uint256", "", nil)
	require.NoError(t, err)
	packed, err := (abi.Arguments{{Type: uintType}}).Pack(big.NewInt(0x11))
	require.NoError(t, err)
	data := append(crypto.Keccak256([]byte("Panic(uint256)"))[:4], packed...)
	revertLog := confirmationRevertLog(t, entrypoint, confirmationTestHash, data)
	reason, raw := confirmedUserOpFailure(confirmationTestHash, entrypoint, &waitForUserOpConfirmationResult{
		Receipt: &types.Receipt{Status: types.ReceiptStatusSuccessful, Logs: []*types.Log{revertLog}},
	})
	require.Contains(t, reason, "underflow or overflow")
	require.Equal(t, "0x"+hex.EncodeToString(data), raw)
}
