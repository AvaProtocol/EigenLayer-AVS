// Provide primitive to work with a bundler RPC
// Bundler RPC is stateless
package bundler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

const (
	// EntryPointV06Address is the canonical ERC-4337 EntryPoint v0.6 contract address
	// This address is the same across all EVM-compatible chains (Ethereum, Base, etc.)
	// Reference: https://github.com/eth-infinitism/account-abstraction/blob/develop/deployments.json
	EntryPointV06Address = "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"
)

// safePreview returns a truncated preview of s with ellipsis when longer than n
func safePreview(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// BundlerClient defines a client for interacting with an EIP-4337 bundler RPC endpoint.
type BundlerClient struct {
	client *rpc.Client
	url    string // Store the original URL for HTTP requests
}

// SimulateUserOperation runs a full validation/simulation cycle on the bundler.
// This catches signature/paymaster validation errors that gas estimation ignores.
func (bc *BundlerClient) SimulateUserOperation(
	ctx context.Context,
	userOp userop.UserOperation,
	entrypoint common.Address,
) error {
	uo := UserOperation{
		Sender:               userOp.Sender,
		Nonce:                fmt.Sprintf("0x%x", userOp.Nonce),
		InitCode:             fmt.Sprintf("0x%x", userOp.InitCode),
		CallData:             fmt.Sprintf("0x%x", userOp.CallData),
		CallGasLimit:         fmt.Sprintf("0x%x", userOp.CallGasLimit),
		VerificationGasLimit: fmt.Sprintf("0x%x", userOp.VerificationGasLimit),
		PreVerificationGas:   fmt.Sprintf("0x%x", userOp.PreVerificationGas),
		MaxFeePerGas:         fmt.Sprintf("0x%x", userOp.MaxFeePerGas),
		MaxPriorityFeePerGas: fmt.Sprintf("0x%x", userOp.MaxPriorityFeePerGas),
		PaymasterAndData:     fmt.Sprintf("0x%x", userOp.PaymasterAndData),
		Signature:            fmt.Sprintf("0x%x", userOp.Signature),
	}

	// JSON-RPC request: eth_simulateUserOperation
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_simulateUserOperation",
		"params":  []interface{}{uo, entrypoint.Hex(), "latest"},
		"id":      1,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal simulate request: %w", err)
	}

	log.Printf("🔍 HTTP SIMULATE REQUEST DEBUG")
	log.Printf("  URL: %s", bc.url)
	log.Printf("  Request Body: %s", string(body))

	hreq, err := http.NewRequestWithContext(ctx, "POST", bc.url, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to create simulate HTTP request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(hreq)
	if err != nil {
		return fmt.Errorf("failed to send simulate HTTP request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("🔍 HTTP SIMULATE RESPONSE DEBUG")
	log.Printf("  Status Code: %d", resp.StatusCode)
	log.Printf("  Response Body: %s", string(respBody))

	// Parse JSON-RPC generically to be resilient to bundlers that use string/null ids
	var generic map[string]interface{}
	if err := json.Unmarshal(respBody, &generic); err != nil {
		// Non-fatal: some bundlers may return non-conforming payloads; let send decide
		return nil
	}
	if errObj, ok := generic["error"].(map[string]interface{}); ok {
		code, _ := errObj["code"].(float64)
		msg, _ := errObj["message"].(string)
		// If method is not supported (-32601), treat as non-fatal and continue.
		if int(code) == -32601 || msg == "Method not found" {
			log.Printf("eth_simulateUserOperation not supported by bundler (continuing): %v", msg)
			return nil
		}
		return fmt.Errorf("JSON-RPC error %d: %s", int(code), msg)
	}
	return nil
}

// NewBundlerClient creates a new BundlerClient that connects to the given URL.
func NewBundlerClient(url string) (*BundlerClient, error) {
	// Use DialHTTP instead of Dial as it is more compatible with HTTP-based bundler
	// endpoints, but it also supports other protocols such as WebSocket.
	c, err := rpc.DialHTTP(url)
	if err != nil {
		return nil, fmt.Errorf("error creating bundler client: %w", err)
	}
	return &BundlerClient{client: c, url: url}, nil
}

// Close closes the underlying RPC client connection.
func (bc *BundlerClient) Close() {
	bc.client.Close()
}

// SendUserOperation sends a UserOperation to the bundler.
func (bc *BundlerClient) SendUserOperation(
	ctx context.Context,
	userOp userop.UserOperation,
	entrypoint common.Address,
) (string, error) {
	// Try HTTP method first (similar to gas estimation fix)
	txHash, err := bc.sendUserOperationHTTP(ctx, userOp, entrypoint)
	if err != nil {
		log.Printf("⚠️ HTTP SendUserOperation failed, trying RPC fallback: %v", err)
		// Fallback to RPC method
		return bc.sendUserOperationRPC(ctx, userOp, entrypoint)
	}
	return txHash, nil
}

// sendUserOperationHTTP sends UserOperation via direct HTTP request
func (bc *BundlerClient) sendUserOperationHTTP(
	ctx context.Context,
	userOp userop.UserOperation,
	entrypoint common.Address,
) (string, error) {
	uo := UserOperation{
		Sender:               userOp.Sender,
		Nonce:                fmt.Sprintf("0x%x", userOp.Nonce),
		InitCode:             fmt.Sprintf("0x%x", userOp.InitCode),
		CallData:             fmt.Sprintf("0x%x", userOp.CallData),
		CallGasLimit:         fmt.Sprintf("0x%x", userOp.CallGasLimit),
		VerificationGasLimit: fmt.Sprintf("0x%x", userOp.VerificationGasLimit),
		PreVerificationGas:   fmt.Sprintf("0x%x", userOp.PreVerificationGas),
		MaxFeePerGas:         fmt.Sprintf("0x%x", userOp.MaxFeePerGas),
		MaxPriorityFeePerGas: fmt.Sprintf("0x%x", userOp.MaxPriorityFeePerGas),
		PaymasterAndData:     fmt.Sprintf("0x%x", userOp.PaymasterAndData),
		Signature:            fmt.Sprintf("0x%x", userOp.Signature),
	}

	log.Printf("🔍 BUNDLER SEND DEBUG - eth_sendUserOperation")
	log.Printf("  Method: eth_sendUserOperation")
	log.Printf("  Entrypoint: %s", entrypoint.Hex())
	log.Printf("  UserOp Structure:")
	log.Printf("    sender: %s", uo.Sender)
	log.Printf("    nonce: %s", uo.Nonce)
	log.Printf("    initCode: %s", safePreview(uo.InitCode, 50))
	log.Printf("    callData: %s", safePreview(uo.CallData, 50))
	log.Printf("    signature: %s", safePreview(uo.Signature, 50))
	log.Printf("🔍 END BUNDLER SEND DEBUG")

	// Create JSON-RPC request
	// IMPORTANT: Some bundlers require EIP-55 checksummed addresses for EntryPoint
	reqData := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_sendUserOperation",
		"params":  []interface{}{uo, entrypoint.Hex()},
		"id":      1,
	}

	reqBody, err := json.Marshal(reqData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON-RPC request: %w", err)
	}

	log.Printf("🔍 HTTP SEND REQUEST DEBUG")
	log.Printf("  URL: %s", bc.url)
	log.Printf("  Request Body: %s", string(reqBody))
	log.Printf("🔍 END HTTP SEND REQUEST DEBUG")

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", bc.url, bytes.NewBuffer(reqBody))
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send HTTP request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	log.Printf("🔍 HTTP SEND RESPONSE DEBUG")
	log.Printf("  Status Code: %d", resp.StatusCode)
	log.Printf("  Response Body: %s", string(respBody))
	log.Printf("🔍 END HTTP SEND RESPONSE DEBUG")

	// Check for HTTP errors
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), string(respBody))
	}

	// Parse JSON-RPC response
	var jsonRpcResp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  string `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(respBody, &jsonRpcResp); err != nil {
		return "", fmt.Errorf("failed to parse JSON response: %w", err)
	}

	// Check for JSON-RPC errors
	if jsonRpcResp.Error != nil {
		return "", fmt.Errorf("JSON-RPC error %d: %s", jsonRpcResp.Error.Code, jsonRpcResp.Error.Message)
	}

	return jsonRpcResp.Result, nil
}

// sendUserOperationRPC sends UserOperation via RPC client (fallback)
func (bc *BundlerClient) sendUserOperationRPC(
	ctx context.Context,
	userOp userop.UserOperation,
	entrypoint common.Address,
) (string, error) {
	var txHash string

	uo := UserOperation{
		Sender:               userOp.Sender,
		Nonce:                fmt.Sprintf("0x%x", userOp.Nonce),
		InitCode:             fmt.Sprintf("0x%x", userOp.InitCode),
		CallData:             fmt.Sprintf("0x%x", userOp.CallData),
		CallGasLimit:         fmt.Sprintf("0x%x", userOp.CallGasLimit),
		VerificationGasLimit: fmt.Sprintf("0x%x", userOp.VerificationGasLimit),
		PreVerificationGas:   fmt.Sprintf("0x%x", userOp.PreVerificationGas),
		MaxFeePerGas:         fmt.Sprintf("0x%x", userOp.MaxFeePerGas),
		MaxPriorityFeePerGas: fmt.Sprintf("0x%x", userOp.MaxPriorityFeePerGas),
		PaymasterAndData:     fmt.Sprintf("0x%x", userOp.PaymasterAndData),
		Signature:            fmt.Sprintf("0x%x", userOp.Signature),
	}

	// IMPORTANT: Use EIP-55 checksummed EntryPoint address (same as HTTP method)
	err := bc.client.CallContext(ctx, &txHash, "eth_sendUserOperation", uo, entrypoint.Hex())
	return txHash, err
}

// EstimateUserOperationGas estimates the gas required for a UserOperation.
// https://eips.ethereum.org/EIPS/eip-4337#rpc-methods-eth-namespace
// * eth_estimateUserOperationGas
// Estimate the gas values for a UserOperation. Given UserOperation optionally without gas limits and gas prices, return the needed gas limits. The signature field is ignored by the wallet, so that the operation will not require user's approval. Still, it might require putting a "semi-valid" signature (e.g. a signature in the right length)
func (bc *BundlerClient) EstimateUserOperationGas(
	ctx context.Context,
	userOp userop.UserOperation,
	entrypoint common.Address,
	// https://geth.ethereum.org/docs/interacting-with-geth/rpc/ns-eth
	// Optionally accepts the State Override Set to allow users to modify the state during the gas estimation.
	// This field as well as its behavior is equivalent to the ones defined for eth_call RPC method.
	override map[string]any,
) (*GasEstimation, error) {
	var result struct {
		// PreVerificationGas   int64 `json:"preVerificationGas"`
		// VerificationGasLimit int64 `json:"verificationGasLimit"`
		// CallGasLimit         int64 `json:"callGasLimit"`
		// VerificationGas      int64 `json:"verificationGas"`

		PreVerificationGas   string `json:"preVerificationGas"`
		VerificationGasLimit string `json:"verificationGasLimit"`
		CallGasLimit         string `json:"callGasLimit"`
	}

	uo := UserOperation{
		Sender:               userOp.Sender,
		Nonce:                fmt.Sprintf("0x%x", userOp.Nonce),
		InitCode:             fmt.Sprintf("0x%x", userOp.InitCode),
		CallData:             fmt.Sprintf("0x%x", userOp.CallData),
		CallGasLimit:         fmt.Sprintf("0x%x", userOp.CallGasLimit),
		VerificationGasLimit: fmt.Sprintf("0x%x", userOp.VerificationGasLimit),
		PreVerificationGas:   fmt.Sprintf("0x%x", userOp.PreVerificationGas),
		MaxFeePerGas:         fmt.Sprintf("0x%x", userOp.MaxFeePerGas),
		MaxPriorityFeePerGas: fmt.Sprintf("0x%x", userOp.MaxPriorityFeePerGas),
		PaymasterAndData:     fmt.Sprintf("0x%x", userOp.PaymasterAndData),
		Signature:            fmt.Sprintf("0x%x", userOp.Signature),
	}

	// 🔍 DEBUG: Log the complete bundler request details
	log.Printf("🔍 BUNDLER REQUEST DEBUG - eth_estimateUserOperationGas")
	log.Printf("  Method: eth_estimateUserOperationGas")
	log.Printf("  Entrypoint: %s", entrypoint.Hex())
	log.Printf("  UserOp Structure:")
	log.Printf("    sender: %s", uo.Sender.Hex())
	log.Printf("    nonce: %s", uo.Nonce)
	log.Printf("    initCode: %s", uo.InitCode)
	log.Printf("    callData: %s", uo.CallData)
	log.Printf("    callGasLimit: %s", uo.CallGasLimit)
	log.Printf("    verificationGasLimit: %s", uo.VerificationGasLimit)
	log.Printf("    preVerificationGas: %s", uo.PreVerificationGas)
	log.Printf("    maxFeePerGas: %s", uo.MaxFeePerGas)
	log.Printf("    maxPriorityFeePerGas: %s", uo.MaxPriorityFeePerGas)
	log.Printf("    paymasterAndData: %s", uo.PaymasterAndData)
	log.Printf("    signature: %s", uo.Signature)
	log.Printf("  Full JSON-RPC Call Parameters:")
	log.Printf("    [0] UserOp: %+v", uo)
	log.Printf("    [1] Entrypoint: %s", entrypoint.Hex())
	log.Printf("    [2] Override: %+v", map[string]string{})
	log.Printf("🔍 END BUNDLER REQUEST DEBUG")

	// Use direct HTTP request instead of RPC client for better compatibility
	gasResult, err := bc.estimateUserOperationGasHTTP(ctx, uo, entrypoint)
	if err != nil {
		return nil, fmt.Errorf("eth_estimateUserOperationGas RPC response error: %w", err)
	}
	result = *gasResult

	gasEstimation := &GasEstimation{
		PreVerificationGas:   new(big.Int),
		VerificationGasLimit: new(big.Int),
		CallGasLimit:         new(big.Int),
		//VerificationGas:      new(big.Int),
	}

	// gasEstimation.PreVerificationGas.SetInt64(result.PreVerificationGas)
	// gasEstimation.VerificationGasLimit.SetInt64(result.VerificationGasLimit)
	// gasEstimation.CallGasLimit.SetInt64(result.CallGasLimit)
	// gasEstimation.VerificationGas.SetInt64(result.VerificationGas)
	gasEstimation.PreVerificationGas.SetString(result.PreVerificationGas[2:], 16)
	gasEstimation.VerificationGasLimit.SetString(result.VerificationGasLimit[2:], 16)
	gasEstimation.CallGasLimit.SetString(result.CallGasLimit[2:], 16)

	return gasEstimation, nil
}

// estimateUserOperationGasHTTP makes a direct HTTP request to the bundler for gas estimation
func (bc *BundlerClient) estimateUserOperationGasHTTP(ctx context.Context, uo UserOperation, entrypoint common.Address) (*struct {
	PreVerificationGas   string `json:"preVerificationGas"`
	VerificationGasLimit string `json:"verificationGasLimit"`
	CallGasLimit         string `json:"callGasLimit"`
}, error) {
	// Create JSON-RPC request
	request := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_estimateUserOperationGas",
		"params": []interface{}{
			uo,
			entrypoint.Hex(),
			map[string]interface{}{}, // empty override map
		},
		"id": 1,
	}

	// Marshal request to JSON
	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	log.Printf("🔍 HTTP REQUEST DEBUG")
	log.Printf("  URL: %s", bc.url)
	log.Printf("  Request Body: %s", string(requestBody))
	log.Printf("🔍 END HTTP REQUEST DEBUG")

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", bc.url, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	// Make HTTP request
	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	log.Printf("🔍 HTTP RESPONSE DEBUG")
	log.Printf("  Status Code: %d", resp.StatusCode)
	log.Printf("  Response Body: %s", string(respBody))
	log.Printf("🔍 END HTTP RESPONSE DEBUG")

	// Check for HTTP errors
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), string(respBody))
	}

	// Parse JSON-RPC response
	var jsonRpcResp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  *struct {
			PreVerificationGas   string `json:"preVerificationGas"`
			VerificationGasLimit string `json:"verificationGasLimit"`
			CallGasLimit         string `json:"callGasLimit"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(respBody, &jsonRpcResp); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	// Check for JSON-RPC errors
	if jsonRpcResp.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error %d: %s", jsonRpcResp.Error.Code, jsonRpcResp.Error.Message)
	}

	if jsonRpcResp.Result == nil {
		return nil, fmt.Errorf("missing result in JSON-RPC response")
	}

	return jsonRpcResp.Result, nil
}

// GetUserOperationByHash fetches a UserOperation by its hash.
func (bc *BundlerClient) GetUserOperationByHash(ctx context.Context, hash string) (interface{}, error) {
	var userOp interface{}
	err := bc.client.CallContext(ctx, &userOp, "eth_getUserOperationByHash", hash)
	return userOp, err
}

// GetUserOperationReceipt fetches the receipt of a UserOperation.
func (bc *BundlerClient) GetUserOperationReceipt(ctx context.Context, hash string) (interface{}, error) {
	var receipt interface{}
	err := bc.client.CallContext(ctx, &receipt, "eth_getUserOperationReceipt", hash)
	return receipt, err
}

// SendBundleNow triggers immediate bundling of pending UserOps.
// This is a debug method (debug_bundler_sendBundleNow) that forces the bundler to create and send a bundle immediately
// instead of waiting for the configured bundle interval or other auto-bundling conditions.
func (bc *BundlerClient) SendBundleNow(ctx context.Context) error {
	log.Printf("🔨 Calling debug_bundler_sendBundleNow to trigger immediate bundling")

	var result interface{}
	err := bc.client.CallContext(ctx, &result, "debug_bundler_sendBundleNow")
	if err != nil {
		return fmt.Errorf("debug_bundler_sendBundleNow failed: %w", err)
	}

	log.Printf("✅ debug_bundler_sendBundleNow returned: %+v", result)
	return nil
}

// PendingUserOp represents a UserOperation in the bundler's mempool
type PendingUserOp struct {
	Sender               common.Address `json:"sender"`
	Nonce                string         `json:"nonce"`
	InitCode             string         `json:"initCode"`
	CallData             string         `json:"callData"`
	CallGasLimit         string         `json:"callGasLimit"`
	VerificationGasLimit string         `json:"verificationGasLimit"`
	PreVerificationGas   string         `json:"preVerificationGas"`
	MaxFeePerGas         string         `json:"maxFeePerGas"`
	MaxPriorityFeePerGas string         `json:"maxPriorityFeePerGas"`
	PaymasterAndData     string         `json:"paymasterAndData"`
	Signature            string         `json:"signature"`
}

// DumpMempool queries the bundler for all pending UserOps in its mempool.
// This is a debug method (debug_bundler_dumpMempool) that returns all UserOps waiting to be bundled.
// Use this to check for stuck UserOps before sending new ones.
func (bc *BundlerClient) DumpMempool(ctx context.Context, entrypoint common.Address) ([]PendingUserOp, error) {
	log.Printf("🔍 Calling debug_bundler_dumpMempool to query pending UserOps")

	var result []PendingUserOp
	err := bc.client.CallContext(ctx, &result, "debug_bundler_dumpMempool", entrypoint.Hex())
	if err != nil {
		return nil, fmt.Errorf("debug_bundler_dumpMempool failed: %w", err)
	}

	log.Printf("📋 debug_bundler_dumpMempool returned %d pending UserOp(s)", len(result))
	return result, nil
}

// GetPendingUserOpsForSender returns pending UserOps for a specific sender address.
// This filters the mempool to find UserOps that may be blocking new submissions.
func (bc *BundlerClient) GetPendingUserOpsForSender(ctx context.Context, entrypoint common.Address, sender common.Address) ([]PendingUserOp, error) {
	allPending, err := bc.DumpMempool(ctx, entrypoint)
	if err != nil {
		return nil, err
	}

	var senderOps []PendingUserOp
	for _, op := range allPending {
		if op.Sender == sender {
			senderOps = append(senderOps, op)
		}
	}

	if len(senderOps) > 0 {
		log.Printf("⚠️  Found %d pending UserOp(s) for sender %s", len(senderOps), sender.Hex())
		for i, op := range senderOps {
			log.Printf("   [%d] nonce=%s", i+1, op.Nonce)
		}
	}

	return senderOps, nil
}

// ClearState clears the bundler's mempool, dropping all pending UserOps.
// This is a debug method (debug_bundler_clearState) that resets the bundler state.
// Use this to clear stuck UserOps that are blocking new submissions.
// WARNING: This clears ALL pending UserOps, not just for a specific sender.
func (bc *BundlerClient) ClearState(ctx context.Context) error {
	log.Printf("🗑️  Calling debug_bundler_clearState to clear bundler mempool")

	var result interface{}
	err := bc.client.CallContext(ctx, &result, "debug_bundler_clearState")
	if err != nil {
		return fmt.Errorf("debug_bundler_clearState failed: %w", err)
	}

	log.Printf("✅ debug_bundler_clearState returned: %+v", result)
	return nil
}

// FlushStuckUserOps checks for pending UserOps for a sender and flushes those with lower nonces.
// This handles the bundler bug where old UserOps get stuck and prevent new ones from being bundled correctly.
// The bundler bundles in FIFO order and only bundles 1 UserOp at a time, so we need to flush older UserOps first.
// This function will keep calling SendBundleNow until all stuck UserOps are processed.
// Returns the number of stuck UserOps that were flushed.
func (bc *BundlerClient) FlushStuckUserOps(ctx context.Context, entrypoint common.Address, sender common.Address, currentNonce *big.Int) (int, error) {
	totalFlushed := 0
	maxFlushAttempts := 10 // Safety limit to prevent infinite loops

	for attempt := 0; attempt < maxFlushAttempts; attempt++ {
		pendingOps, err := bc.GetPendingUserOpsForSender(ctx, entrypoint, sender)
		if err != nil {
			// Non-fatal: debug methods may not be available
			log.Printf("⚠️  Could not query pending UserOps (debug methods may be disabled): %v", err)
			return totalFlushed, nil
		}

		if len(pendingOps) == 0 {
			if attempt == 0 {
				log.Printf("✅ No pending UserOps for sender %s", sender.Hex())
			}
			break
		}

		// Check if any pending UserOps have lower nonces (stuck)
		stuckCount := 0
		for _, op := range pendingOps {
			// Parse the nonce from hex string
			nonceStr := op.Nonce
			if len(nonceStr) > 2 && nonceStr[:2] == "0x" {
				nonceStr = nonceStr[2:]
			}
			pendingNonce := new(big.Int)
			// Validate that SetString succeeded (returns false if string is invalid)
			if _, ok := pendingNonce.SetString(nonceStr, 16); !ok {
				log.Printf("⚠️  Skipping UserOp with invalid nonce format: %q", op.Nonce)
				continue
			}

			if pendingNonce.Cmp(currentNonce) < 0 {
				stuckCount++
				log.Printf("⚠️  Found stuck UserOp with nonce %s (current nonce is %s)", pendingNonce.String(), currentNonce.String())
			}
		}

		if stuckCount == 0 {
			// No more stuck UserOps with lower nonces
			break
		}

		log.Printf("🔄 Flushing %d stuck UserOp(s) with lower nonces (attempt %d/%d)", stuckCount, attempt+1, maxFlushAttempts)

		// Trigger bundling to process the stuck UserOps
		if err := bc.SendBundleNow(ctx); err != nil {
			log.Printf("⚠️  SendBundleNow failed during flush: %v", err)
			// Continue anyway, the UserOp might still get bundled
		}

		totalFlushed += stuckCount

		// Wait for the bundle to be processed before checking again
		// The bundler needs time to mine the transaction
		log.Printf("⏳ Waiting 3 seconds for bundle to be mined...")
		time.Sleep(3 * time.Second)
	}

	if totalFlushed > 0 {
		log.Printf("✅ Flushed %d stuck UserOp(s) total", totalFlushed)
	}

	return totalFlushed, nil
}
