package taskengine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// Exercise the late-confirmation path without a real bundler or transaction.
// A mined outer EntryPoint transaction can contain a failed UserOperation.
func TestPendingContractWriteConfirmationHonorsUserOpOutcome(t *testing.T) {
	for _, successful := range []bool{false, true} {
		name := "inner_revert"
		if successful {
			name = "confirmed"
		}
		t.Run(name, func(t *testing.T) {
			txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var result any
				if req.Method == "eth_getUserOperationReceipt" {
					result = map[string]any{
						"success": successful,
						"receipt": map[string]any{"transactionHash": txHash, "blockNumber": "0x64", "status": "0x1"},
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			}))
			defer server.Close()
			vm := NewVM().WithLogger(testutil.GetLogger())
			vm.smartWalletConfig = &config.SmartWalletConfig{
				ChainID: 13371337, BundlerProvider: config.BundlerProviderSelfHosted,
				BundlerURL: server.URL, EthRpcUrl: server.URL,
			}
			metadata, err := structpb.NewValue([]any{map[string]any{
				"methodName": "approve", "success": false,
				"receipt": map[string]any{
					"status": "pending", "executionStatus": "pending", "transactionHash": "pending",
					"userOpHash": "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				},
			}})
			require.NoError(t, err)
			step := &avsproto.Execution_Step{Id: "approve", Success: false, Metadata: metadata}
			require.True(t, vm.waitForOnChainConfirmationIfNeeded(step))
			require.Equal(t, successful, step.Success, "outer transaction inclusion does not prove inner success")
			if successful {
				require.Empty(t, step.Error)
			} else {
				require.NotEmpty(t, step.Error, "a confirmed inner revert must reach the terminal report")
			}
			result := step.Metadata.AsInterface().([]any)[0].(map[string]any)
			receipt := result["receipt"].(map[string]any)
			require.Equal(t, txHash, receipt["transactionHash"], "standard bundler receipts nest the transaction hash")
			wantStatus := "failed"
			if successful {
				wantStatus = "confirmed"
			}
			require.Equal(t, wantStatus, receipt["executionStatus"], "a resolved operation must not retain pending metadata")
		})
	}
}

func TestPendingETHTransferConfirmationHonorsUserOpOutcome(t *testing.T) {
	for _, successful := range []bool{false, true} {
		name := "inner_revert"
		if successful {
			name = "confirmed"
		}
		t.Run(name, func(t *testing.T) {
			txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var result any
				if req.Method == "eth_getUserOperationReceipt" {
					result = map[string]any{"success": successful, "receipt": map[string]any{
						"transactionHash": txHash, "status": "0x1", "blockNumber": "0x64",
					}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			}))
			defer server.Close()
			vm := NewVM().WithLogger(testutil.GetLogger())
			vm.smartWalletConfig = &config.SmartWalletConfig{
				ChainID: 13371337, BundlerProvider: config.BundlerProviderSelfHosted,
				BundlerURL: server.URL, EthRpcUrl: server.URL,
			}
			metadata, err := structpb.NewValue(map[string]any{
				"receiptStatus": "pending", "transactionHash": "pending", "userOpHash": "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			})
			require.NoError(t, err)
			step := &avsproto.Execution_Step{Id: "transfer", Success: true, Metadata: metadata,
				OutputData: &avsproto.Execution_Step_EthTransfer{EthTransfer: &avsproto.ETHTransferNode_Output{}},
			}
			require.True(t, vm.waitForOnChainConfirmationIfNeeded(step))
			require.Equal(t, successful, step.Success)
			got := step.Metadata.AsInterface().(map[string]any)
			require.Equal(t, txHash, got["transactionHash"])
			want := "failed"
			if successful {
				want = "confirmed"
			}
			require.Equal(t, want, got["receiptStatus"])
		})
	}
}

func TestPendingWriteConfirmationTransportFailureStaysUnresolved(t *testing.T) {
	vm := NewVM().WithLogger(testutil.GetLogger())
	vm.smartWalletConfig = &config.SmartWalletConfig{BundlerProvider: config.BundlerProviderSelfHosted}
	metadata, err := structpb.NewValue([]any{map[string]any{
		"success": false, "receipt": map[string]any{"status": "pending", "userOpHash": "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}})
	require.NoError(t, err)
	step := &avsproto.Execution_Step{Metadata: metadata}
	require.True(t, vm.waitForOnChainConfirmationIfNeeded(step))
	require.False(t, step.Success)
	require.Contains(t, step.Error, "confirmation unresolved", "an unavailable bundler is not evidence of a mined revert")
	require.Equal(t, "pending", step.Metadata.AsInterface().([]any)[0].(map[string]any)["receipt"].(map[string]any)["status"])
	vm.ExecutionLogs = []*avsproto.Execution_Step{step}
	_, failed, status := vm.AnalyzeExecutionResult()
	require.Equal(t, 1, failed)
	require.Equal(t, ExecutionFailed, status, "an unresolved transaction must not produce a successful workflow")
}

func TestUpdateConfirmedWritePreservesOtherOperations(t *testing.T) {
	const first = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const second = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	metadata, err := structpb.NewValue([]any{
		map[string]any{"methodName": "approve", "success": false, "receipt": map[string]any{"status": "pending", "userOpHash": first}},
		map[string]any{"methodName": "supply", "success": false, "receipt": map[string]any{"status": "pending", "userOpHash": second}},
	})
	require.NoError(t, err)
	step := &avsproto.Execution_Step{Metadata: metadata}
	updateConfirmedWriteMetadata(step, first, &waitForUserOpConfirmationResult{Success: true, TxHash: first})
	require.False(t, step.Success, "a pending sibling prevents whole-node success")
	require.Equal(t, second, extractUserOpHashFromStep(step), "poll the remaining pending operation, not the first confirmed one")
	updateConfirmedWriteMetadata(step, second, &waitForUserOpConfirmationResult{Success: false, TxHash: second})
	require.False(t, step.Success)
	require.NotEmpty(t, step.Error)
	require.False(t, shouldWaitForContractWriteConfirmation(step, false))
	methods := step.Metadata.AsInterface().([]any)
	require.Equal(t, true, methods[0].(map[string]any)["success"])
	require.Equal(t, false, methods[1].(map[string]any)["success"])
}
