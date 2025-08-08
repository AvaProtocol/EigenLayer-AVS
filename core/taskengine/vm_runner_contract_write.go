package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/byte4"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/structpb"
)

type SendUserOpFunc func(
	config *config.SmartWalletConfig,
	owner common.Address,
	callData []byte,
	paymasterReq *preset.VerifyingPaymasterRequest,
	senderOverride *common.Address,
) (*userop.UserOperation, *types.Receipt, error)

type ContractWriteProcessor struct {
	*CommonProcessor
	client            *ethclient.Client
	smartWalletConfig *config.SmartWalletConfig
	owner             common.Address
	sendUserOpFunc    SendUserOpFunc
}

func NewContractWriteProcessor(vm *VM, client *ethclient.Client, smartWalletConfig *config.SmartWalletConfig, owner common.Address) *ContractWriteProcessor {
	r := &ContractWriteProcessor{
		client:            client,
		smartWalletConfig: smartWalletConfig,
		owner:             owner,
		sendUserOpFunc:    preset.SendUserOp, // Default to the real implementation
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
	}

	return r
}

func (r *ContractWriteProcessor) getInputData(node *avsproto.ContractWriteNode) (string, string, []*avsproto.ContractWriteNode_MethodCall, error) {
	var contractAddress, callData string
	var methodCalls []*avsproto.ContractWriteNode_MethodCall

	// Priority 1: Use node.Config if available (static configuration)
	if node.Config != nil {
		contractAddress = node.Config.ContractAddress
		callData = node.Config.CallData
		// Note: ABI is handled directly from protobuf Values in Execute method for optimization
		methodCalls = node.Config.MethodCalls
	}

	// Priority 2: Override with VM variables if set (dynamic runtime values)
	r.vm.mu.Lock()
	if addr, exists := r.vm.vars["contract_address"]; exists {
		if addrStr, ok := addr.(string); ok {
			contractAddress = addrStr
		}
	}
	if data, exists := r.vm.vars["call_data"]; exists {
		if dataStr, ok := data.(string); ok {
			callData = dataStr
		}
	}
	r.vm.mu.Unlock()

	// Apply template variable preprocessing
	contractAddress = r.vm.preprocessTextWithVariableMapping(contractAddress)
	callData = r.vm.preprocessTextWithVariableMapping(callData)

	// If we have method_calls from config but also call_data from variables, prefer method_calls
	// If no method_calls but we have call_data, create a single method call
	if len(methodCalls) == 0 && callData != "" {
		methodCalls = []*avsproto.ContractWriteNode_MethodCall{
			{
				CallData:   &callData,
				MethodName: UnknownMethodName, // Will be resolved from ABI if available
			},
		}
	}

	if contractAddress == "" {
		return "", "", nil, fmt.Errorf("missing required configuration: contractAddress")
	}

	// Validate contract address format
	if !common.IsHexAddress(contractAddress) {
		return "", "", nil, fmt.Errorf("invalid contract address format: %s", contractAddress)
	}

	if len(methodCalls) == 0 {
		return "", "", nil, fmt.Errorf("missing required configuration: either method_calls or call_data must be provided")
	}

	return contractAddress, callData, methodCalls, nil
}

func (r *ContractWriteProcessor) executeMethodCall(
	ctx context.Context,
	parsedABI *abi.ABI,
	contractAddress common.Address,
	methodCall *avsproto.ContractWriteNode_MethodCall,
) *avsproto.ContractWriteNode_MethodResult {
	t0 := time.Now()

	// Substitute template variables in methodParams before generating calldata
	// Use preprocessTextWithVariableMapping for each parameter to support dot notation like {{value.address}}
	resolvedMethodParams := make([]string, len(methodCall.MethodParams))
	for i, param := range methodCall.MethodParams {
		resolvedMethodParams[i] = r.vm.preprocessTextWithVariableMapping(param)
	}

	// Use shared utility to generate or use existing calldata
	var existingCallData string
	if methodCall.CallData != nil {
		existingCallData = *methodCall.CallData
	}
	callData, err := GenerateOrUseCallData(methodCall.MethodName, existingCallData, resolvedMethodParams, parsedABI)
	if err != nil {
		if r.vm != nil && r.vm.logger != nil {
			r.vm.logger.Error("❌ Failed to get/generate calldata for contract write",
				"methodName", methodCall.MethodName,
				"providedCallData", methodCall.CallData,
				"rawMethodParams", methodCall.MethodParams,
				"resolvedMethodParams", resolvedMethodParams,
				"error", err)
		}
		return &avsproto.ContractWriteNode_MethodResult{
			Success:    false,
			Error:      fmt.Sprintf("failed to get/generate calldata: %v", err),
			MethodName: methodCall.MethodName,
		}
	}

	// Log successful calldata generation if needed
	if existingCallData == "" && callData != "" && r.vm != nil && r.vm.logger != nil {
		r.vm.logger.Debug("✅ Generated calldata from methodName and methodParams for contract write",
			"methodName", methodCall.MethodName,
			"rawMethodParams", methodCall.MethodParams,
			"resolvedMethodParams", resolvedMethodParams,
			"generatedCallData", callData)
	}

	calldata := common.FromHex(callData)

	// Resolve method name from ABI if not provided or if provided name is UnknownMethodName
	methodName := methodCall.MethodName
	if parsedABI != nil && (methodName == "" || methodName == UnknownMethodName) {
		if method, err := byte4.GetMethodFromCalldata(*parsedABI, calldata); err == nil {
			methodName = method.Name
		}
	}

	// 🔍 DEBUG: Log all configuration details
	r.vm.logger.Info("🔍 CONTRACT WRITE DEBUG - Configuration Analysis",
		"has_smart_wallet_config", r.smartWalletConfig != nil,
		"method_name", methodName,
		"contract_address", contractAddress.Hex())

	if r.smartWalletConfig != nil {
		r.vm.logger.Info("🔍 CONTRACT WRITE DEBUG - Smart Wallet Config Details",
			"enable_real_transactions", r.smartWalletConfig.EnableRealTransactions,
			"bundler_url", r.smartWalletConfig.BundlerURL,
			"factory_address", r.smartWalletConfig.FactoryAddress,
			"entrypoint_address", r.smartWalletConfig.EntrypointAddress)
	} else {
		r.vm.logger.Warn("⚠️ CONTRACT WRITE DEBUG - Smart wallet config is NIL!")
	}

	// Check if real transactions are enabled
	if r.smartWalletConfig != nil && r.smartWalletConfig.EnableRealTransactions {
		r.vm.logger.Info("🚀 CONTRACT WRITE DEBUG - Using real UserOp transaction path",
			"contract", contractAddress.Hex(),
			"method", methodName)

		return r.executeRealUserOpTransaction(ctx, contractAddress, callData, methodName, parsedABI, t0)
	}

	// FALLBACK TO TENDERLY SIMULATION FOR CONTRACT WRITES
	// This provides consistent behavior between run_node_immediately and simulateTask
	r.vm.logger.Info("🔮 CONTRACT WRITE DEBUG - Using Tenderly simulation path",
		"contract", contractAddress.Hex(),
		"method", methodName,
		"reason", func() string {
			if r.smartWalletConfig == nil {
				return "smart_wallet_config_is_nil"
			}
			return "enable_real_transactions_is_false"
		}())

	// Initialize Tenderly client
	tenderlyClient := NewTenderlyClient(r.vm.logger)

	// Get chain ID for simulation
	var chainID int64 = 11155111 // Default to Sepolia
	if r.smartWalletConfig != nil {
		// Try to extract chain ID from RPC URL or use default
		chainID = 11155111 // Sepolia default
	}

	// Get contract ABI as string
	var contractAbiStr string
	if parsedABI != nil {
		// Convert ABI back to JSON string for Tenderly
		// For now, we'll use an empty string and let Tenderly handle it
		contractAbiStr = ""
	}

	// Simulate the contract write using Tenderly
	simulationResult, err := tenderlyClient.SimulateContractWrite(
		ctx,
		contractAddress.Hex(),
		callData,
		contractAbiStr,
		methodName,
		chainID,
		r.owner.Hex(), // Pass the user's wallet address for simulation
	)

	if err != nil {
		r.vm.logger.Warn("🚫 Tenderly simulation failed, using mock result", "error", err)

		// Create a mock result when Tenderly fails
		return r.createMockContractWriteResult(methodName, contractAddress.Hex(), callData, parsedABI, t0, chainID)
	}

	// Convert Tenderly simulation result to legacy protobuf format
	return r.convertTenderlyResultToFlexibleFormat(simulationResult, parsedABI, callData)
}

// executeRealUserOpTransaction executes a real UserOp transaction for contract writes
func (r *ContractWriteProcessor) executeRealUserOpTransaction(ctx context.Context, contractAddress common.Address, callData string, methodName string, parsedABI *abi.ABI, startTime time.Time) *avsproto.ContractWriteNode_MethodResult {
	r.vm.logger.Info("🔍 REAL USEROP DEBUG - Starting real UserOp transaction execution",
		"contract_address", contractAddress.Hex(),
		"method_name", methodName,
		"calldata_length", len(callData),
		"calldata", callData)

	// Convert hex calldata to bytes
	callDataBytes := common.FromHex(callData)

	// Create smart wallet execute calldata: execute(target, value, data)
	smartWalletCallData, err := aa.PackExecute(
		contractAddress, // target contract
		big.NewInt(0),   // ETH value (0 for contract calls)
		callDataBytes,   // contract method calldata
	)
	if err != nil {
		r.vm.logger.Error("Failed to pack smart wallet execute calldata", "error", err)
		// Return error result - workflow execution FAILS (no fallback for deployed workflows)
		return &avsproto.ContractWriteNode_MethodResult{
			Success: false,
			Error:   fmt.Sprintf("Failed to pack smart wallet execute calldata: %v", err),
		}
	}

	// Set up factory address for AA operations
	aa.SetFactoryAddress(r.smartWalletConfig.FactoryAddress)
	aa.SetEntrypointAddress(r.smartWalletConfig.EntrypointAddress)

	// Optional runner validation: if workflowContext.runner is provided, ensure it matches
	// one of the owner EOA's known smart wallets (authoritative). If wallet list cannot be checked,
	// fall back to checking the derived salt:0 address as a best-effort sanity check.
	if wfCtxIface, ok := r.vm.vars[WorkflowContextVarName]; ok {
		if wfCtx, ok := wfCtxIface.(map[string]interface{}); ok {
			if runnerIface, ok := wfCtx["runner"]; ok {
				if runnerStr, ok := runnerIface.(string); ok && runnerStr != "" {
					client, err := ethclient.Dial(r.smartWalletConfig.EthRpcUrl)
					if err == nil {
						// derive sender at salt:0
						sender, derr := aa.GetSenderAddress(client, r.owner, big.NewInt(0))
						client.Close()
						if derr == nil && sender != nil {
							if !strings.EqualFold(sender.Hex(), runnerStr) {
								// Do not fail solely on derived salt:0 mismatch; authoritative check is the wallet list in run_node path
								r.vm.logger.Warn("runner does not match derived salt:0; proceeding (wallet list validation applies in run_node)", "expected", sender.Hex(), "runner", runnerStr)
							}
						}
					}
				}
			}
		}
	}

	// Determine if paymaster should be used based on transaction limits and whitelist
	var paymasterReq *preset.VerifyingPaymasterRequest
	if r.shouldUsePaymaster() {
		paymasterReq = preset.GetVerifyingPaymasterRequestForDuration(
			r.smartWalletConfig.PaymasterAddress,
			15*time.Minute, // 15 minute validity window
		)
		r.vm.logger.Info("🎫 Using paymaster for sponsored transaction",
			"paymaster", r.smartWalletConfig.PaymasterAddress.Hex(),
			"owner", r.owner.Hex())
	} else {
		r.vm.logger.Info("💰 Using regular transaction (no paymaster)",
			"owner", r.owner.Hex())
	}

	// Determine AA overrides from VM vars: prefer senderOverride; else saltOverride
	var senderOverride *common.Address
	r.vm.mu.Lock()
	if v, ok := r.vm.vars["aa_sender"]; ok {
		if s, ok2 := v.(string); ok2 && common.IsHexAddress(s) {
			addr := common.HexToAddress(s)
			senderOverride = &addr
		}
	}
	r.vm.mu.Unlock()

	// Send UserOp transaction with overrides
	userOp, receipt, err := r.sendUserOpFunc(
		r.smartWalletConfig,
		r.owner,
		smartWalletCallData,
		paymasterReq,
		senderOverride,
	)

	// Increment transaction counter for this address (regardless of success/failure)
	if r.vm.db != nil {
		counterKey := ContractWriteCounterKey(r.owner)
		if _, err := r.vm.db.IncCounter(counterKey, 0); err != nil {
			r.vm.logger.Warn("Failed to increment transaction counter", "error", err)
		}
	}

	if err != nil {
		r.vm.logger.Error("🚫 BUNDLER FAILED - UserOp transaction failed, workflow execution FAILED",
			"bundler_error", err,
			"bundler_url", r.smartWalletConfig.BundlerURL,
			"method", methodName,
			"contract", contractAddress.Hex())

		// Return error result - deployed workflows should FAIL when bundler is unavailable
		return &avsproto.ContractWriteNode_MethodResult{
			Success: false,
			Error:   fmt.Sprintf("Bundler failed - UserOp transaction could not be sent: %v", err),
		}
	}

	// Create result from real transaction
	return r.createRealTransactionResult(methodName, contractAddress.Hex(), callData, parsedABI, userOp, receipt)
}

// createRealTransactionResult creates a result from a real UserOp transaction
func (r *ContractWriteProcessor) createRealTransactionResult(methodName, contractAddress, callData string, parsedABI *abi.ABI, userOp *userop.UserOperation, receipt *types.Receipt) *avsproto.ContractWriteNode_MethodResult {
	// Extract methodABI from contract ABI if available
	var methodABI *structpb.Value
	if parsedABI != nil {
		if method, exists := parsedABI.Methods[methodName]; exists {
			if abiMap := r.extractMethodABI(&method); abiMap != nil {
				if abiValue, err := structpb.NewValue(abiMap); err == nil {
					methodABI = abiValue
				}
			}
		}
	}

	// Create receipt data from real transaction
	var receiptMap map[string]interface{}
	if receipt != nil {
		// Get transaction details for from/to fields
		var fromAddr, toAddr string
		if r.smartWalletConfig != nil {
			// For UserOp transactions, 'from' is the smart wallet address
			// Note: We don't have ethclient here, so we'll use the owner address as fallback
			fromAddr = r.owner.Hex()
			toAddr = contractAddress // contractAddress is already a string
		} else {
			// Fallback for regular transactions
			fromAddr = r.owner.Hex()
			toAddr = contractAddress // contractAddress is already a string
		}

		// Real transaction receipt with standard Ethereum fields
		receiptMap = map[string]interface{}{
			"transactionHash":   receipt.TxHash.Hex(),
			"blockNumber":       fmt.Sprintf("0x%x", receipt.BlockNumber.Uint64()),
			"blockHash":         receipt.BlockHash.Hex(),
			"transactionIndex":  fmt.Sprintf("0x%x", receipt.TransactionIndex),
			"from":              fromAddr,
			"to":                toAddr,
			"gasUsed":           fmt.Sprintf("0x%x", receipt.GasUsed),
			"cumulativeGasUsed": fmt.Sprintf("0x%x", receipt.CumulativeGasUsed),
			"effectiveGasPrice": fmt.Sprintf("0x%x", receipt.EffectiveGasPrice.Uint64()),
			"status":            fmt.Sprintf("0x%x", receipt.Status),
			"type":              fmt.Sprintf("0x%x", receipt.Type),
			"logsBloom":         fmt.Sprintf("0x%x", receipt.Bloom),
			"logs":              convertLogsToInterface(receipt.Logs),
		}
	} else {
		// UserOp submitted but receipt not available yet
		receiptMap = map[string]interface{}{
			"userOpHash":      userOp.GetUserOpHash(r.smartWalletConfig.EntrypointAddress, big.NewInt(11155111)).Hex(),
			"sender":          userOp.Sender.Hex(),
			"nonce":           fmt.Sprintf("0x%x", userOp.Nonce.Uint64()),
			"status":          "pending",
			"transactionHash": "pending", // Will be available once bundler processes the UserOp
		}
	}

	receiptValue, _ := structpb.NewValue(receiptMap)

	return &avsproto.ContractWriteNode_MethodResult{
		MethodName: methodName,
		MethodAbi:  methodABI,
		Success:    receipt != nil && receipt.Status == 1, // Success if receipt exists and status is 1
		Error:      "",
		Receipt:    receiptValue,
		Value:      nil, // Real transactions don't return values directly
	}
}

// convertLogsToInterface converts transaction logs to interface{} for protobuf compatibility
func convertLogsToInterface(logs []*types.Log) []interface{} {
	result := make([]interface{}, len(logs))
	for i, log := range logs {
		// Convert topics to []interface{} for protobuf compatibility
		topics := make([]interface{}, len(log.Topics))
		for j, topic := range log.Topics {
			topics[j] = topic.Hex()
		}

		result[i] = map[string]interface{}{
			"address":          log.Address.Hex(),
			"topics":           topics,
			"data":             common.Bytes2Hex(log.Data),
			"blockNumber":      fmt.Sprintf("0x%x", log.BlockNumber),
			"transactionHash":  log.TxHash.Hex(),
			"transactionIndex": fmt.Sprintf("0x%x", log.TxIndex),
			"blockHash":        log.BlockHash.Hex(),
			"logIndex":         fmt.Sprintf("0x%x", log.Index),
			"removed":          log.Removed,
		}
	}
	return result
}

// shouldUsePaymaster determines if paymaster should be used based on transaction limits and whitelist
func (r *ContractWriteProcessor) shouldUsePaymaster() bool {
	// Priority 1: If smart wallet already has ETH balance, do NOT use paymaster
	// This covers the case where the wallet can self-fund deployment and calls
	if r.client != nil {
		// Derive the smart wallet (sender) address using salt 0 (default path)
		senderAddr, err := aa.GetSenderAddress(r.client, r.owner, big.NewInt(0))
		if err == nil && senderAddr != nil {
			bal, balErr := r.client.BalanceAt(context.Background(), *senderAddr, nil)
			if balErr == nil && bal != nil && bal.Sign() > 0 {
				if r.vm.logger != nil {
					r.vm.logger.Debug("Smart wallet has ETH balance, not using paymaster",
						"owner", r.owner.Hex(), "wallet", senderAddr.Hex(), "balanceWei", bal.String())
				}
				return false
			}
		} else if r.vm.logger != nil {
			r.vm.logger.Debug("Could not derive smart wallet address for balance check; proceeding to consider paymaster",
				"owner", r.owner.Hex(), "error", err)
		}
	}

	// Priority 2: If no ETH on wallet, and a paymaster is configured, use paymaster as fallback for creation/sponsorship
	if (r.smartWalletConfig.PaymasterAddress != common.Address{}) {
		if r.vm.logger != nil {
			r.vm.logger.Debug("Using paymaster as fallback (wallet has no ETH or balance unknown)",
				"owner", r.owner.Hex(), "paymaster", r.smartWalletConfig.PaymasterAddress.Hex())
		}
		return true
	}

	// No ETH and no paymaster configured → do not force paymaster; bundler will fail (AA21) and we propagate failure
	if r.vm.logger != nil {
		r.vm.logger.Debug("No wallet ETH and no paymaster configured; proceeding without paymaster (may fail)",
			"owner", r.owner.Hex())
	}
	return false
}

// createMockContractWriteResult creates a mock result when Tenderly fails
func (r *ContractWriteProcessor) createMockContractWriteResult(methodName, contractAddress, callData string, parsedABI *abi.ABI, startTime time.Time, chainID int64) *avsproto.ContractWriteNode_MethodResult {
	// Extract methodABI from contract ABI if available
	var methodABI *structpb.Value
	if parsedABI != nil {
		if method, exists := parsedABI.Methods[methodName]; exists {
			if abiMap := r.extractMethodABI(&method); abiMap != nil {
				if abiValue, err := structpb.NewValue(abiMap); err == nil {
					methodABI = abiValue
				}
			}
		}
	}

	// Create flexible receipt as JSON object
	receiptMap := map[string]interface{}{
		"transactionHash": fmt.Sprintf("0x%064x", startTime.UnixNano()),
		"logs":            []interface{}{}, // Empty logs array
		// All other fields can be added dynamically as needed
	}

	receipt, _ := structpb.NewValue(receiptMap)

	return &avsproto.ContractWriteNode_MethodResult{
		MethodName: methodName,
		MethodAbi:  methodABI,
		Success:    true,
		Error:      "",
		Receipt:    receipt,
		Value:      nil, // Mock transactions don't have return values
	}
}

// convertTenderlyResultToLegacyFormat converts Tenderly result to new flexible format
func (r *ContractWriteProcessor) convertTenderlyResultToFlexibleFormat(result *ContractWriteSimulationResult, parsedABI *abi.ABI, callData string) *avsproto.ContractWriteNode_MethodResult {
	// Extract methodABI from contract ABI if available
	var methodABI *structpb.Value
	if parsedABI != nil {
		if method, exists := parsedABI.Methods[result.MethodName]; exists {
			if abiMap := r.extractMethodABI(&method); abiMap != nil {
				if abiValue, err := structpb.NewValue(abiMap); err == nil {
					methodABI = abiValue
				}
			}
		}
	}

	// Create flexible receipt as JSON object with Tenderly data and standard fields
	receiptStatus := "0x1" // Default to success
	if !result.Success {
		receiptStatus = "0x0" // Set to failure if transaction failed
	}

	receiptMap := map[string]interface{}{
		"transactionHash":   result.Transaction.Hash,                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              // ✅ From Tenderly
		"from":              result.Transaction.From,                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              // ✅ From Tenderly
		"to":                result.Transaction.To,                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                // ✅ From Tenderly
		"blockNumber":       "0x1",                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                // Mock value for simulation
		"blockHash":         "0x0000000000000000000000000000000000000000000000000000000000000001",                                                                                                                                                                                                                                                                                                                                                                                                                                                                 // Mock value
		"transactionIndex":  "0x0",                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                // Mock value for simulation
		"gasUsed":           StandardGasCostHex,                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   // Standard gas cost for simple transaction
		"cumulativeGasUsed": StandardGasCostHex,                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   // Standard gas cost for simple transaction
		"effectiveGasPrice": "0x3b9aca00",                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         // Mock value (1 gwei)
		"status":            receiptStatus,                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        // Success/failure status based on actual result
		"logsBloom":         "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000", // Empty logs bloom
		"logs":              []interface{}{},                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      // Empty logs array
		"type":              "0x2",                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                // EIP-1559 transaction type
	}

	receipt, _ := structpb.NewValue(receiptMap)

	// Extract return value from Tenderly response
	var returnValue *structpb.Value
	if result.ReturnData != nil {
		// Map returnData.value to our value field
		if valueProto, err := structpb.NewValue(result.ReturnData.Value); err == nil {
			returnValue = valueProto
		}
	}

	// Handle errors
	errorMsg := ""
	success := result.Success
	if result.Error != nil {
		errorMsg = result.Error.Message
		success = false
	}

	return &avsproto.ContractWriteNode_MethodResult{
		MethodName: result.MethodName,
		MethodAbi:  methodABI,
		Success:    success,
		Error:      errorMsg,
		Receipt:    receipt,
		Value:      returnValue,
	}
}

// extractMethodABI extracts ABI information for a specific method
func (r *ContractWriteProcessor) extractMethodABI(method *abi.Method) map[string]interface{} {
	if method == nil {
		return nil
	}

	// Convert inputs
	inputs := make([]interface{}, len(method.Inputs))
	for i, input := range method.Inputs {
		inputs[i] = map[string]interface{}{
			"name": input.Name,
			"type": input.Type.String(),
		}
	}

	// Convert outputs
	outputs := make([]interface{}, len(method.Outputs))
	for i, output := range method.Outputs {
		outputs[i] = map[string]interface{}{
			"name": output.Name,
			"type": output.Type.String(),
		}
	}

	return map[string]interface{}{
		"name":            method.Name,
		"type":            "function",
		"inputs":          inputs,
		"outputs":         outputs,
		"stateMutability": method.StateMutability,
		"constant":        method.Constant,
		"payable":         method.Payable,
	}
}

func (r *ContractWriteProcessor) Execute(stepID string, node *avsproto.ContractWriteNode) (*avsproto.Execution_Step, error) {
	// Use shared function to create execution step
	s := createNodeExecutionStep(stepID, avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE, r.vm)

	var log strings.Builder
	var err error

	defer func() {
		if err != nil {
			finalizeExecutionStep(s, false, err.Error(), log.String())
		}
	}()

	// Get input configuration
	contractAddress, _, methodCalls, inputErr := r.getInputData(node)
	if inputErr != nil {
		err = inputErr
		return s, err
	}

	log.WriteString(fmt.Sprintf("Contract Write Node: %s\n", contractAddress))
	log.WriteString(fmt.Sprintf("Number of method calls: %d\n", len(methodCalls)))

	// Parse ABI if provided - OPTIMIZED: Use protobuf Values directly
	var parsedABI *abi.ABI
	if node.Config != nil && len(node.Config.ContractAbi) > 0 {
		if optimizedParsedABI, parseErr := ParseABIOptimized(node.Config.ContractAbi); parseErr == nil {
			parsedABI = optimizedParsedABI
			log.WriteString("✅ ABI parsed successfully using optimized shared method (no string conversion)\n")
		} else {
			log.WriteString(fmt.Sprintf("Warning: Failed to parse ABI with optimized method: %v\n", parseErr))
		}
	}

	contractAddr := common.HexToAddress(contractAddress)
	var results []*avsproto.ContractWriteNode_MethodResult

	// Execute each method call
	ctx := context.Background()
	for i, methodCall := range methodCalls {
		log.WriteString(fmt.Sprintf("\nExecuting method %d: %s\n", i+1, methodCall.MethodName))

		result := r.executeMethodCall(ctx, parsedABI, contractAddr, methodCall)
		results = append(results, result)

		if result.Success {
			// Extract transaction hash from flexible receipt
			var txHash string
			if result.Receipt != nil {
				if receiptMap := result.Receipt.AsInterface().(map[string]interface{}); receiptMap != nil {
					if hash, ok := receiptMap["transactionHash"].(string); ok {
						txHash = hash
					}
				}
			}
			log.WriteString(fmt.Sprintf("✅ Success: %s (tx: %s)\n", result.MethodName, txHash))
		} else {
			log.WriteString(fmt.Sprintf("❌ Failed: %s - %s\n", result.MethodName, result.Error))
			// Don't fail the entire execution for individual method failures
		}
	}

	// Convert results to Go maps for JSON conversion using standardized format
	var resultsArray []interface{}
	for _, methodResult := range results {
		resultMap := map[string]interface{}{
			"methodName": methodResult.MethodName,
			"success":    methodResult.Success,
			"error":      methodResult.Error,
		}

		// Add methodABI if available
		if methodResult.MethodAbi != nil {
			resultMap["methodABI"] = methodResult.MethodAbi.AsInterface()
		}

		// Add flexible receipt - already in the correct JSON format
		if methodResult.Receipt != nil {
			resultMap["receipt"] = methodResult.Receipt.AsInterface()
		}

		// Add blockNumber for convenience if available in receipt
		if methodResult.Receipt != nil {
			if receiptMap := methodResult.Receipt.AsInterface().(map[string]interface{}); receiptMap != nil {
				if blockNumber, ok := receiptMap["blockNumber"]; ok {
					resultMap["blockNumber"] = blockNumber
				}
			}
		}

		// Add return value
		if methodResult.Value != nil {
			resultMap["value"] = methodResult.Value.AsInterface()
		} else {
			resultMap["value"] = nil
		}

		resultsArray = append(resultsArray, resultMap)
	}

	// Convert results to JSON for the new protobuf structure using shared helper
	resultsValue := ConvertResultsArrayToProtobufValue(resultsArray, &log)

	// 🚀 NEW: Create decoded events data organized by method name
	var decodedEventsData = make(map[string]interface{})

	// Parse events from each method's transaction receipt
	for _, methodResult := range results {
		methodEvents := make(map[string]interface{})

		// Extract logs from receipt if available
		if methodResult.Receipt != nil {
			if receiptMap := methodResult.Receipt.AsInterface().(map[string]interface{}); receiptMap != nil {
				if logs, hasLogs := receiptMap["logs"]; hasLogs {
					if logsArray, ok := logs.([]interface{}); ok && len(logsArray) > 0 {
						// Decode each event log using contract ABI
						for _, logInterface := range logsArray {
							if logMap, ok := logInterface.(map[string]interface{}); ok {
								if parsedABI != nil {
									// Convert log map to types.Log structure for parsing
									if eventLog := r.convertMapToEventLog(logMap); eventLog != nil {
										// Filter: only decode logs from the target contract address
										if !strings.EqualFold(eventLog.Address.Hex(), contractAddress) {
											continue
										}
										// Parse the log using shared event parsing function
										decodedEvent, _, err := parseEventWithABIShared(eventLog, parsedABI, nil, r.vm.logger)
										if err != nil {
											if r.vm != nil && r.vm.logger != nil {
												r.vm.logger.Warn("Failed to parse event from transaction receipt log",
													"contractAddress", eventLog.Address.Hex(),
													"blockNumber", eventLog.BlockNumber,
													"txHash", eventLog.TxHash.Hex(),
													"logIndex", eventLog.Index,
													"error", err)
											}
										} else {
											// Flatten event fields into methodEvents
											for key, value := range decodedEvent {
												if key != "eventName" { // Skip meta field
													methodEvents[key] = value
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}

		// Store events for this method (empty object if no events)
		decodedEventsData[methodResult.MethodName] = methodEvents
	}

	// Convert decoded events to protobuf Value
	var dataValue *structpb.Value
	if len(decodedEventsData) > 0 {
		if dv, err := structpb.NewValue(decodedEventsData); err == nil {
			dataValue = dv
		}
	} else {
		// Create empty object for consistency with contractRead format
		if dv, err := structpb.NewValue(map[string]interface{}{}); err == nil {
			dataValue = dv
		}
	}

	// Create output with flattened event data in Data field and method results in Metadata field
	s.OutputData = &avsproto.Execution_Step_ContractWrite{
		ContractWrite: &avsproto.ContractWriteNode_Output{
			Data:     dataValue,    // Flattened decoded events (empty object if no events)
			Metadata: resultsValue, // Method results array (detailed transaction info)
		},
	}

	// Set output variables for backward compatibility and chaining
	outputVars := make(map[string]any)
	if len(results) > 0 {
		// For single method calls, set the result as the main output
		if len(results) == 1 && results[0].Success {
			// Extract transaction hash from flexible receipt
			var txHash string
			if results[0].Receipt != nil {
				if receiptMap := results[0].Receipt.AsInterface().(map[string]interface{}); receiptMap != nil {
					if hash, ok := receiptMap["transactionHash"].(string); ok {
						txHash = hash
					}
				}
			}
			outputVars["receipt"] = results[0].Receipt
			outputVars["hash"] = txHash
			outputVars["success"] = results[0].Success
		}
		// Always provide results array for multi-method scenarios
		outputVars["results"] = results
	}
	// Use shared function to set output variable for this step
	setNodeOutputData(r.CommonProcessor, stepID, outputVars)

	// Use shared function to finalize execution step with success
	finalizeExecutionStep(s, true, "", log.String())

	return s, nil
}

// convertMapToEventLog converts a log map from receipt to types.Log structure for event parsing
func (r *ContractWriteProcessor) convertMapToEventLog(logMap map[string]interface{}) *types.Log {
	eventLog := &types.Log{}

	// Parse address
	if addr, hasAddr := logMap["address"]; hasAddr {
		if addrStr, ok := addr.(string); ok {
			eventLog.Address = common.HexToAddress(addrStr)
		}
	}

	// Parse topics
	if topics, hasTopics := logMap["topics"]; hasTopics {
		if topicsArray, ok := topics.([]interface{}); ok {
			for _, topic := range topicsArray {
				if topicStr, ok := topic.(string); ok {
					eventLog.Topics = append(eventLog.Topics, common.HexToHash(topicStr))
				}
			}
		}
	}

	// Parse data
	if data, hasData := logMap["data"]; hasData {
		if dataStr, ok := data.(string); ok {
			dataBytes := common.FromHex(dataStr)
			eventLog.Data = dataBytes
		}
	}

	// Parse other fields if needed
	if blockNumber, hasBN := logMap["blockNumber"]; hasBN {
		if bnStr, ok := blockNumber.(string); ok {
			bn, err := strconv.ParseUint(strings.TrimPrefix(bnStr, "0x"), 16, 64)
			if err != nil {
				if r.vm != nil && r.vm.logger != nil {
					r.vm.logger.Warn("Failed to parse blockNumber from transaction receipt log",
						"blockNumber", bnStr,
						"error", err)
				}
			} else {
				eventLog.BlockNumber = bn
			}
		}
	}

	if txHash, hasTxHash := logMap["transactionHash"]; hasTxHash {
		if txHashStr, ok := txHash.(string); ok {
			eventLog.TxHash = common.HexToHash(txHashStr)
		}
	}

	return eventLog
}
