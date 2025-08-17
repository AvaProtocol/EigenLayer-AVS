package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	originalAbiString string,
	contractAddress common.Address,
	methodCall *avsproto.ContractWriteNode_MethodCall,
) *avsproto.ContractWriteNode_MethodResult {
	t0 := time.Now()

	// VERY OBVIOUS DEBUG - use Info to avoid noisy error-level logs in normal flow
	// Log method execution start at debug level for development/troubleshooting
	r.vm.logger.Debug("ContractWriteProcessor: executeMethodCall started",
		"method", methodCall.MethodName,
		"contract", contractAddress.Hex(),
		"timestamp", time.Now().Format("15:04:05.000"))

	// CRITICAL: We NEVER have eoaAddress's private key and can NEVER send transactions from it
	// eoaAddress (r.owner) is ONLY for ownership verification
	// For ALL transactions (simulations AND real), we MUST use runner (smart wallet) address as sender
	// The runner smart wallet corresponds to and is controlled by eoaAddress, but sender must ALWAYS be runner
	senderAddress := r.owner // Fallback only - should always be overridden by aa_sender

	// Get runner address (smart wallet) - this is the ONLY valid sender for transactions
	if aaSenderVar, ok := r.vm.vars["aa_sender"]; ok {
		if aaSenderStr, ok := aaSenderVar.(string); ok && aaSenderStr != "" {
			senderAddress = common.HexToAddress(aaSenderStr) // This is the runner (smart wallet)
			r.vm.logger.Error("🔄 CONTRACT WRITE CRITICAL DEBUG - Sender address resolved",
				"r_owner_eoaAddress", r.owner.Hex(),
				"sender_address_runner", senderAddress.Hex(),
				"aa_sender_var", aaSenderStr)
		}
	} else {
		r.vm.logger.Error("⚠️ CONTRACT WRITE CRITICAL DEBUG - aa_sender not found, using eoaAddress fallback",
			"r_owner_eoaAddress", r.owner.Hex(),
			"sender_address_fallback", senderAddress.Hex())
	}

	// Substitute template variables in methodParams before generating calldata
	// Use preprocessTextWithVariableMapping for each parameter to support dot notation like {{value.address}}
	resolvedMethodParams := make([]string, len(methodCall.MethodParams))
	for i, param := range methodCall.MethodParams {
		resolvedMethodParams[i] = r.vm.preprocessTextWithVariableMapping(param)
	}

	// Handle JSON objects/arrays: convert to appropriate format based on method signature
	// This supports struct/tuple parameters where the client returns objects or arrays from custom code
	if len(resolvedMethodParams) == 1 {
		param := resolvedMethodParams[0]

		// Check if this method expects a struct parameter by examining the ABI
		if parsedABI != nil {
			if method, exists := parsedABI.Methods[methodCall.MethodName]; exists {
				if len(method.Inputs) == 1 && method.Inputs[0].Type.T == abi.TupleTy {
					// Method expects a single struct/tuple parameter
					tupleType := method.Inputs[0].Type

					// Handle JSON object - convert to ordered array based on struct field order
					if strings.HasPrefix(param, "{") && strings.HasSuffix(param, "}") {
						var objData map[string]interface{}
						if err := json.Unmarshal([]byte(param), &objData); err == nil {
							// Convert object to ordered array based on ABI struct field order
							orderedArray := make([]interface{}, len(tupleType.TupleElems))
							for i := range tupleType.TupleElems {
								fieldName := tupleType.TupleRawNames[i]
								if value, exists := objData[fieldName]; exists {
									orderedArray[i] = value
								} else {
									// Field missing - this will cause an error in ABI parsing
									if r.vm != nil && r.vm.logger != nil {
										r.vm.logger.Error("❌ CONTRACT WRITE - Missing field in struct object",
											"method", methodCall.MethodName,
											"missing_field", fieldName,
											"available_fields", GetMapKeys(objData))
									}
								}
							}

							// Convert back to JSON array string for ABI processing
							if jsonBytes, err := json.Marshal(orderedArray); err == nil {
								resolvedMethodParams[0] = string(jsonBytes)
								if r.vm != nil && r.vm.logger != nil {
									r.vm.logger.Info("🔄 CONTRACT WRITE - Converted object to ordered array for struct",
										"method", methodCall.MethodName,
										"struct_fields", tupleType.TupleRawNames,
										"ordered_array", string(jsonBytes))
								}
							}
						}
					} else if strings.HasPrefix(param, "[") && strings.HasSuffix(param, "]") {
						// Handle JSON array - already in correct format for struct processing
						if r.vm != nil && r.vm.logger != nil {
							r.vm.logger.Info("🔄 CONTRACT WRITE - Detected struct parameter with JSON array",
								"method", methodCall.MethodName,
								"param_type", tupleType.String())
						}
					}
				} else if len(method.Inputs) > 1 {
					// Method expects multiple parameters - expand JSON array if provided
					if strings.HasPrefix(param, "[") && strings.HasSuffix(param, "]") {
						var arrayElements []interface{}
						if err := json.Unmarshal([]byte(param), &arrayElements); err == nil {
							expandedParams := make([]string, len(arrayElements))
							for j, element := range arrayElements {
								expandedParams[j] = fmt.Sprintf("%v", element)
							}
							resolvedMethodParams = expandedParams
							if r.vm != nil && r.vm.logger != nil {
								r.vm.logger.Info("🔄 CONTRACT WRITE - Expanded JSON array into individual parameters",
									"method", methodCall.MethodName,
									"expanded_count", len(expandedParams))
							}
						}
					}
				}
			}
		}
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
			"bundler_url", r.smartWalletConfig.BundlerURL,
			"factory_address", r.smartWalletConfig.FactoryAddress,
			"entrypoint_address", r.smartWalletConfig.EntrypointAddress)
	} else {
		r.vm.logger.Warn("⚠️ CONTRACT WRITE DEBUG - Smart wallet config is NIL!")
	}

	// Check if this is a runNodeWithInputs call (should always be simulation)
	isRunNodeWithInputs := false
	if taskTypeVar, ok := r.vm.vars["task_type"]; ok {
		if taskTypeStr, ok := taskTypeVar.(string); ok && taskTypeStr == "run_node_with_inputs" {
			isRunNodeWithInputs = true
		}
	}

	// Log VM mode (simulation flag only)
	r.vm.logger.Error("🔧 CONTRACT WRITE CRITICAL DEBUG - VM mode check",
		"is_simulation", r.vm.IsSimulation,
		"is_run_node_with_inputs", isRunNodeWithInputs,
		"should_simulate", r.vm.IsSimulation || isRunNodeWithInputs,
		"method", methodName,
		"contract", contractAddress.Hex())

	// If simulation flag is set OR forcing simulation, always use simulation path
	if r.vm.IsSimulation || isRunNodeWithInputs {
		r.vm.logger.Info("🔮 CONTRACT WRITE DEBUG - Using Tenderly simulation path",
			"contract", contractAddress.Hex(),
			"method", methodName,
			"reason", "vm_is_simulation")

		// Initialize Tenderly client
		tenderlyClient := NewTenderlyClient(r.vm.logger)

		// Get chain ID for simulation
		// STRICT: In runNode path, chainId must be provided via workflowContext.chainId
		var chainID int64
		foundChainID := false
		if wfCtxIface, ok := r.vm.vars[WorkflowContextVarName]; ok {
			if wfCtx, ok := wfCtxIface.(map[string]interface{}); ok {
				if cid, ok := wfCtx["chainId"]; ok {
					switch v := cid.(type) {
					case int64:
						chainID = v
						foundChainID = true
					case int:
						chainID = int64(v)
						foundChainID = true
					case float64:
						chainID = int64(v)
						foundChainID = true
					case string:
						if strings.HasPrefix(strings.ToLower(v), "0x") {
							if parsed, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(v), "0x"), 16, 64); err == nil {
								chainID = parsed
								foundChainID = true
							}
						} else if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
							chainID = parsed
							foundChainID = true
						}
					}
				}
			}
		}
		if !foundChainID {
			return &avsproto.ContractWriteNode_MethodResult{
				MethodName: methodName,
				Success:    false,
				Error:      "workflowContext.chainId is required for runNode contractWrite",
			}
		}
		r.vm.logger.Debug("ContractWrite: resolved chain id for simulation", "chain_id", chainID)

		// Get contract ABI as string
		var contractAbiStr string
		if parsedABI != nil && originalAbiString != "" {
			// Use the original ABI string that was successfully parsed
			// Don't re-marshal the parsed ABI as it changes the structure
			contractAbiStr = originalAbiString
			r.vm.logger.Debug("✅ CONTRACT WRITE - Using original ABI string for Tenderly",
				"method", methodName, "abi_length", len(contractAbiStr))
		}

		// Note: HTTP Simulation API automatically uses the latest block context

		// Extract transaction value from VM variables (passed from raw nodeConfig)
		var transactionValue string = "0" // Default to 0 if not specified
		r.vm.mu.Lock()
		if nodeConfigIface, exists := r.vm.vars["nodeConfig"]; exists {
			if nodeConfig, ok := nodeConfigIface.(map[string]interface{}); ok {
				if valueIface, exists := nodeConfig["value"]; exists {
					if valueStr, ok := valueIface.(string); ok && valueStr != "" {
						transactionValue = valueStr
						if r.vm.logger != nil {
							r.vm.logger.Info("Using transaction value from configuration",
								"value", transactionValue,
								"method", methodName,
								"contract", contractAddress.Hex())
						}
					}
				}
			}
		}
		r.vm.mu.Unlock()

		// Simulate the contract write using Tenderly

		simulationResult, err := tenderlyClient.SimulateContractWrite(
			ctx,
			contractAddress.Hex(),
			callData,
			contractAbiStr,
			methodName,
			chainID,
			senderAddress.Hex(), // Use runner (smart wallet) address for simulation
			transactionValue,    // Pass the transaction value
		)

		if err != nil {
			r.vm.logger.Warn("🚫 Tenderly simulation failed", "error", err)
			// Return failure result without mock data
			return &avsproto.ContractWriteNode_MethodResult{
				MethodName: methodName,
				Success:    false,
				Error:      fmt.Sprintf("tenderly simulation failed: %v", err),
			}
		}

		// Convert Tenderly simulation result to legacy protobuf format
		mr := r.convertTenderlyResultToFlexibleFormat(simulationResult, parsedABI, callData)
		// Try to stamp real latest block number/hash from our configured RPC
		if mr != nil && mr.Receipt != nil && r.client != nil {
			if header, herr := r.client.HeaderByNumber(ctx, nil); herr == nil && header != nil {
				if recMap, ok := mr.Receipt.AsInterface().(map[string]interface{}); ok {
					recMap["blockNumber"] = fmt.Sprintf("0x%x", header.Number.Uint64())
					recMap["blockHash"] = header.Hash().Hex()
					if newVal, err := structpb.NewValue(recMap); err == nil {
						mr.Receipt = newVal
					}
				}
			}
		}
		// Fallback: if Tenderly returned latest block number, override placeholder
		if simulationResult != nil && simulationResult.LatestBlockHex != "" && mr != nil && mr.Receipt != nil {
			if recMap, ok := mr.Receipt.AsInterface().(map[string]interface{}); ok {
				if _, has := recMap["blockNumber"]; !has || recMap["blockNumber"] == "0x1" {
					recMap["blockNumber"] = simulationResult.LatestBlockHex
					recMap["blockHash"] = fmt.Sprintf("0x%064s", strings.TrimPrefix(simulationResult.LatestBlockHex, "0x"))
					if newVal, err := structpb.NewValue(recMap); err == nil {
						mr.Receipt = newVal
					}
				}
			}
		}
		// Finally, attach Tenderly logs into the flexible receipt (do this last to avoid overwrites)
		r.vm.logger.Debug("LOG ATTACHMENT CHECK",
			"mr_nil", mr == nil,
			"receipt_nil", mr == nil || mr.Receipt == nil,
			"simulation_nil", simulationResult == nil,
			"receipt_logs_count", func() int {
				if simulationResult != nil {
					return len(simulationResult.ReceiptLogs)
				}
				return -1
			}())

		if mr != nil && mr.Receipt != nil && simulationResult != nil && len(simulationResult.ReceiptLogs) > 0 {
			r.vm.logger.Debug("About to attach Tenderly logs to receipt",
				"logs_count", len(simulationResult.ReceiptLogs))
			if recMap, ok := mr.Receipt.AsInterface().(map[string]interface{}); ok {
				logsIface := make([]interface{}, 0, len(simulationResult.ReceiptLogs))
				for _, m := range simulationResult.ReceiptLogs {
					logsIface = append(logsIface, m)
				}
				recMap["logs"] = logsIface
				r.vm.logger.Debug("Attached logs to receipt map",
					"logs_count", len(simulationResult.ReceiptLogs))
				if newVal, err := structpb.NewValue(recMap); err == nil {
					mr.Receipt = newVal
					r.vm.logger.Debug("Updated receipt with logs")
				} else {
					r.vm.logger.Error("Failed to create new receipt value", "error", err)
				}
			} else {
				r.vm.logger.Error("Failed to cast receipt to map")
			}
		} else {
			r.vm.logger.Debug("Not attaching logs",
				"mr_nil", mr == nil,
				"receipt_nil", mr == nil || mr.Receipt == nil,
				"simulation_nil", simulationResult == nil,
				"logs_count", func() int {
					if simulationResult != nil {
						return len(simulationResult.ReceiptLogs)
					}
					return -1
				}())
		}
		return mr
	}

	// Deployed workflows (simulation flag is false): require smartWalletConfig and use real UserOp path
	r.vm.logger.Error("🚀 CONTRACT WRITE CRITICAL DEBUG - Going down REAL transaction path",
		"is_simulation", r.vm.IsSimulation,
		"method", methodName,
		"contract", contractAddress.Hex())

	if r.smartWalletConfig == nil {
		r.vm.logger.Error("Contract write in deployed mode without smart wallet config")
		return &avsproto.ContractWriteNode_MethodResult{
			Success:    false,
			Error:      "smart wallet config is required for deployed contract write",
			MethodName: methodName,
		}
	}

	r.vm.logger.Info("🚀 CONTRACT WRITE DEBUG - Using real UserOp transaction path",
		"contract", contractAddress.Hex(),
		"method", methodName)

	return r.executeRealUserOpTransaction(ctx, contractAddress, callData, methodName, parsedABI, t0)
}

// executeRealUserOpTransaction executes a real UserOp transaction for contract writes
func (r *ContractWriteProcessor) executeRealUserOpTransaction(ctx context.Context, contractAddress common.Address, callData string, methodName string, parsedABI *abi.ABI, startTime time.Time) *avsproto.ContractWriteNode_MethodResult {
	r.vm.logger.Info("🔍 REAL USEROP DEBUG - Starting real UserOp transaction execution",
		"contract_address", contractAddress.Hex(),
		"method_name", methodName,
		"calldata_length", len(callData),
		"calldata", callData,
		"owner_eoaAddress", r.owner.Hex())

	// Convert hex calldata to bytes
	callDataBytes := common.FromHex(callData)

	// Create smart wallet execute calldata: execute(target, value, data)
	smartWalletCallData, err := aa.PackExecute(
		contractAddress, // target contract
		big.NewInt(0),   // ETH value (0 for contract calls)
		callDataBytes,   // contract method calldata
	)
	if err != nil {
		r.vm.logger.Error("🚨 DEPLOYED WORKFLOW ERROR: Failed to pack smart wallet execute calldata",
			"error", err,
			"contract_address", contractAddress.Hex(),
			"method_name", methodName,
			"calldata_bytes_length", len(callDataBytes))
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
		r.vm.logger.Info("Using paymaster for sponsored transaction",
			"paymaster", r.smartWalletConfig.PaymasterAddress.Hex(),
			"owner", r.owner.Hex())
	} else {
		r.vm.logger.Info("Using regular transaction (no paymaster)",
			"owner", r.owner.Hex())
	}

	// Determine AA overrides from VM vars: get smart wallet address for senderOverride
	var senderOverride *common.Address
	r.vm.mu.Lock()
	if v, ok := r.vm.vars["aa_sender"]; ok {
		if s, ok2 := v.(string); ok2 && common.IsHexAddress(s) {
			addr := common.HexToAddress(s)
			senderOverride = &addr
			r.vm.logger.Info("🔍 DEPLOYED WORKFLOW: UserOp sender configuration",
				"owner_eoaAddress", r.owner.Hex(),
				"senderOverride_smartWallet", addr.Hex(),
				"aa_sender_var", s)
		}
	}
	r.vm.mu.Unlock()

	if senderOverride == nil {
		r.vm.logger.Error("🚨 DEPLOYED WORKFLOW ERROR: aa_sender not found in VM vars",
			"owner_eoaAddress", r.owner.Hex())
	}

	// Send UserOp transaction with correct parameters:
	// - owner: EOA address (r.owner) for smart wallet derivation
	// - senderOverride: smart wallet address (aa_sender) for the actual transaction
	userOp, receipt, err := r.sendUserOpFunc(
		r.smartWalletConfig,
		r.owner, // Use EOA address (owner) for smart wallet derivation
		smartWalletCallData,
		paymasterReq,
		senderOverride, // Smart wallet address from aa_sender
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
	r.vm.logger.Info("🔍 DEPLOYED WORKFLOW: Creating real transaction result",
		"method_name", methodName,
		"contract_address", contractAddress,
		"has_receipt", receipt != nil,
		"has_userop", userOp != nil)

	if receipt != nil {
		r.vm.logger.Info("📋 DEPLOYED WORKFLOW: Transaction receipt details",
			"tx_hash", receipt.TxHash.Hex(),
			"status", receipt.Status,
			"gas_used", receipt.GasUsed,
			"block_number", receipt.BlockNumber.Uint64(),
			"logs_count", len(receipt.Logs))
	} else {
		r.vm.logger.Error("🚨 DEPLOYED WORKFLOW ERROR: No receipt available for transaction result")
	}

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

		// Get the actual sender (runner) from VM variables
		actualSender := r.owner // Default to owner (eoaAddress)
		if aaSenderVar, ok := r.vm.vars["aa_sender"]; ok {
			if aaSenderStr, ok := aaSenderVar.(string); ok && aaSenderStr != "" {
				actualSender = common.HexToAddress(aaSenderStr)
			}
		}

		if r.smartWalletConfig != nil {
			// For UserOp transactions, 'from' is the smart wallet address (runner)
			fromAddr = actualSender.Hex()
			toAddr = contractAddress // contractAddress is already a string
		} else {
			// Fallback for regular transactions
			fromAddr = actualSender.Hex()
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
			"userOpHash":      userOp.GetUserOpHash(r.smartWalletConfig.EntrypointAddress, big.NewInt(r.smartWalletConfig.ChainID)).Hex(),
			"sender":          userOp.Sender.Hex(),
			"nonce":           fmt.Sprintf("0x%x", userOp.Nonce.Uint64()),
			"status":          "pending",
			"transactionHash": "pending", // Will be available once bundler processes the UserOp
		}
	}

	receiptValue, _ := structpb.NewValue(receiptMap)

	// Debug real transaction receipt
	if receipt != nil {
		r.vm.logger.Error("🔍 REAL TRANSACTION DEBUG - Receipt analysis",
			"tx_hash", receipt.TxHash.Hex(),
			"block_number", receipt.BlockNumber.Uint64(),
			"status", receipt.Status,
			"logs_count", len(receipt.Logs),
			"method", methodName)

		// Log each individual log entry
		for i, log := range receipt.Logs {
			r.vm.logger.Error("🔍 REAL TRANSACTION DEBUG - Log entry",
				"log_index", i,
				"address", log.Address.Hex(),
				"topics_count", len(log.Topics),
				"data_length", len(log.Data))
		}
	} else {
		r.vm.logger.Error("🔍 REAL TRANSACTION DEBUG - No receipt available")
	}

	success := receipt != nil && receipt.Status == 1
	errorMsg := ""
	if !success {
		if receipt == nil {
			errorMsg = "No transaction receipt received"
		} else if receipt.Status != 1 {
			errorMsg = fmt.Sprintf("Transaction failed with status %d", receipt.Status)
		}
	}

	r.vm.logger.Info("🎯 DEPLOYED WORKFLOW: Final transaction result",
		"method_name", methodName,
		"success", success,
		"error_msg", errorMsg,
		"has_receipt_value", receiptValue != nil)

	return &avsproto.ContractWriteNode_MethodResult{
		MethodName: methodName,
		MethodAbi:  methodABI,
		Success:    success,
		Error:      errorMsg,
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
	// Priority 1: If the intended sender (aa_sender override or derived) has ETH balance, do NOT use paymaster
	if r.client != nil {
		// Prefer aa_sender override when present
		var checkAddr *common.Address
		if r.vm != nil {
			r.vm.mu.Lock()
			if v, ok := r.vm.vars["aa_sender"]; ok {
				if s, ok2 := v.(string); ok2 && common.IsHexAddress(s) {
					addr := common.HexToAddress(s)
					checkAddr = &addr
				}
			}
			r.vm.mu.Unlock()
		}
		if checkAddr == nil {
			// Fallback to derived sender at salt 0
			if derived, err := aa.GetSenderAddress(r.client, r.owner, big.NewInt(0)); err == nil {
				checkAddr = derived
			} else if r.vm.logger != nil {
				r.vm.logger.Debug("Could not derive smart wallet address for balance check; proceeding to consider paymaster",
					"owner", r.owner.Hex(), "error", err)
			}
		}
		if checkAddr != nil {
			if bal, balErr := r.client.BalanceAt(context.Background(), *checkAddr, nil); balErr == nil && bal != nil && bal.Sign() > 0 {
				if r.vm.logger != nil {
					r.vm.logger.Debug("Sender has ETH balance, not using paymaster",
						"owner", r.owner.Hex(), "wallet", checkAddr.Hex(), "balanceWei", bal.String())
				}
				return false
			}
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

// convertTenderlyResultToFlexibleFormat maps a Tenderly simulation result into our
// ContractWriteNode_MethodResult shape using a flexible receipt wrapper.
//
// Behavior:
//   - Success is derived from the Tenderly result (true/false)
//   - Receipt is a minimal shell that includes standard fields and placeholders
//     for blockNumber/blockHash/indices, since eth_call-style simulations do not
//     produce a real transaction receipt.
//   - Logs are NOT fabricated here. If the upstream simulation provider returns
//     decoded logs, they should be copied by the caller before or after this call.
//   - Callers may patch blockNumber and blockHash afterwards with real chain
//     context (e.g., latest block header) to avoid placeholder values.
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

	// Create a flexible receipt shell with Tenderly data and standard fields.
	// Note: blockNumber/blockHash default to placeholders here and can be
	// overridden by the caller with real chain context when available.
	receiptStatus := "0x1" // Default to success
	if !result.Success {
		receiptStatus = "0x0" // Set to failure if transaction failed
	}

	// Prepare logs from Tenderly simulation result
	var receiptLogs []interface{}
	if len(result.ReceiptLogs) > 0 {
		receiptLogs = make([]interface{}, len(result.ReceiptLogs))
		for i, log := range result.ReceiptLogs {
			receiptLogs[i] = log
		}
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
		"logs":              receiptLogs,                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          // Logs from Tenderly simulation
		"type":              "0x2",                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                // EIP-1559 transaction type
	}

	// Logs are now populated from real simulation provider (Tenderly)

	receipt, _ := structpb.NewValue(receiptMap)

	// Extract return value from Tenderly response
	var returnValue *structpb.Value
	if result.ReturnData != nil {
		r.vm.logger.Info("🔍 CRITICAL DEBUG - ReturnData found",
			"method", result.MethodName,
			"returnData_name", result.ReturnData.Name,
			"returnData_type", result.ReturnData.Type,
			"returnData_value", result.ReturnData.Value)

		// Parse the JSON value from ReturnData and convert to protobuf
		var parsedValue interface{}
		if err := json.Unmarshal([]byte(result.ReturnData.Value), &parsedValue); err == nil {
			// Successfully parsed JSON, convert to protobuf
			if valueProto, err := structpb.NewValue(parsedValue); err == nil {
				returnValue = valueProto
				r.vm.logger.Info("✅ CRITICAL DEBUG - Successfully created returnValue protobuf",
					"method", result.MethodName,
					"parsedValue", parsedValue)
			} else {
				r.vm.logger.Error("❌ CRITICAL DEBUG - Failed to create protobuf from parsedValue",
					"method", result.MethodName,
					"error", err)
			}
		} else {
			r.vm.logger.Error("❌ CRITICAL DEBUG - Failed to unmarshal JSON from ReturnData.Value",
				"method", result.MethodName,
				"error", err,
				"raw_value", result.ReturnData.Value)

			// Fallback: treat as raw string if JSON parsing fails
			if valueProto, err := structpb.NewValue(result.ReturnData.Value); err == nil {
				returnValue = valueProto
			}
		}
	} else {
		r.vm.logger.Error("❌ CRITICAL DEBUG - ReturnData is nil",
			"method", result.MethodName)
	}

	// No fallback default value. If provider does not return output data, Value remains nil

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
	var originalAbiString string
	if node.Config != nil && len(node.Config.ContractAbi) > 0 {
		// Get the original ABI string for Tenderly decoding
		if abiReader, readerErr := ConvertContractAbiToReader(node.Config.ContractAbi); readerErr == nil {
			if abiBytes, readErr := io.ReadAll(abiReader); readErr == nil {
				originalAbiString = string(abiBytes)
			}
		}

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

		// Add panic recovery to ensure individual method failures don't break the loop
		var result *avsproto.ContractWriteNode_MethodResult
		func() {
			defer func() {
				if rcv := recover(); rcv != nil {
					log.WriteString(fmt.Sprintf("🚨 PANIC in executeMethodCall: %v\n", rcv))
					result = &avsproto.ContractWriteNode_MethodResult{
						MethodName: methodCall.MethodName,
						Success:    false,
						Error:      fmt.Sprintf("panic during execution: %v", rcv),
					}
				}
			}()
			result = r.executeMethodCall(ctx, parsedABI, originalAbiString, contractAddr, methodCall)
		}()
		// Ensure MethodName is populated to avoid empty keys downstream
		if result.MethodName == "" {
			result.MethodName = methodCall.MethodName
			if result.MethodName == "" {
				result.MethodName = fmt.Sprintf("method_%d", i+1)
			}
		}
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
			r.vm.logger.Error("🚨 DEPLOYED WORKFLOW: Method execution failed",
				"method_name", result.MethodName,
				"error_message", result.Error,
				"error_length", len(result.Error),
				"success", result.Success)
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

	// PRIORITY LOGIC: Add return values first, events will override if present
	// Rule: Events take priority (more descriptive) over return values when both exist
	for _, methodResult := range results {
		r.vm.logger.Info("🔍 CRITICAL DEBUG - Processing methodResult for decodedEventsData",
			"method", methodResult.MethodName,
			"value_nil", methodResult.Value == nil,
			"value_content", func() interface{} {
				if methodResult.Value != nil {
					return methodResult.Value.AsInterface()
				}
				return "nil"
			}())
		if methodResult.Value != nil {
			// Add return values under method name
			decodedEventsData[methodResult.MethodName] = methodResult.Value.AsInterface()
			r.vm.logger.Info("✅ Added return value to decodedEventsData (will be overridden by events if present)",
				"method", methodResult.MethodName,
				"data", methodResult.Value.AsInterface())
		} else {
			// For methods with no return value, create empty object to maintain structure
			decodedEventsData[methodResult.MethodName] = map[string]interface{}{}
			r.vm.logger.Info("✅ Added empty object for method with no return value",
				"method", methodResult.MethodName)
		}
	}

	// Parse events from each method's transaction receipt
	for idx, methodResult := range results {
		// Defensive: ensure method name is non-empty
		methodName := methodResult.MethodName
		if methodName == "" {
			if idx < len(methodCalls) && methodCalls[idx].MethodName != "" {
				methodName = methodCalls[idx].MethodName
			} else {
				methodName = fmt.Sprintf("method_%d", idx+1)
			}
		}
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
										r.vm.logger.Error("🔍 EVENT DEBUG - Converted log",
											"address", eventLog.Address.Hex(),
											"topics_count", len(eventLog.Topics),
											"data_length", len(eventLog.Data),
											"contract_address", contractAddress)

										// Filter: only decode logs from the target contract address
										if !strings.EqualFold(eventLog.Address.Hex(), contractAddress) {
											r.vm.logger.Error("🔍 EVENT DEBUG - Address mismatch, skipping",
												"log_address", eventLog.Address.Hex(),
												"expected_address", contractAddress)
											continue
										}

										// Parse the log using shared event parsing function
										decodedEvent, eventName, err := parseEventWithABIShared(eventLog, parsedABI, nil, r.vm.logger)
										if err != nil {
											r.vm.logger.Error("🔍 EVENT DEBUG - Failed to parse event",
												"contractAddress", eventLog.Address.Hex(),
												"error", err)
										} else {
											r.vm.logger.Error("🔍 EVENT DEBUG - Successfully parsed event",
												"event_name", eventName,
												"decoded_data", decodedEvent)

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

		// PRIORITY LOGIC: Events take priority over return values
		if len(methodEvents) > 0 {
			// If we have events, use ONLY event data (events are more descriptive)
			decodedEventsData[methodName] = methodEvents
			r.vm.logger.Info("✅ EVENT PRIORITY - Using event data only (overriding return values)",
				"method", methodName,
				"event_fields", len(methodEvents),
				"event_data", methodEvents)
		}
		// If no events, preserve existing return values (return values only when no events)
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

	// Create output with flattened event data in Data field and attach method results to step-level metadata
	s.OutputData = &avsproto.Execution_Step_ContractWrite{
		ContractWrite: &avsproto.ContractWriteNode_Output{
			Data: dataValue, // Flattened decoded events (empty object if no events)
		},
	}
	s.Metadata = resultsValue

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
	// Also expose flattened decoded events under "data" for callers that read from VM vars
	outputVars["data"] = decodedEventsData

	// 🔍 DEBUG: Log what we're storing in outputVars
	r.vm.logger.Debug("🔍 CONTRACT WRITE DEBUG - Setting outputVars",
		"stepID", stepID,
		"outputVars_keys", getOutputVarKeys(outputVars),
		"decodedEventsData", decodedEventsData,
		"decodedEventsData_size", len(decodedEventsData))

	// Use shared function to set output variable for this step
	setNodeOutputData(r.CommonProcessor, stepID, outputVars)

	// Determine step success: any failed method or receipt.status == 0x0 marks the step as failed
	stepSuccess, stepErrorMsg := computeWriteStepSuccess(results)

	// Finalize step with computed success and error message
	finalizeExecutionStep(s, stepSuccess, stepErrorMsg, log.String())

	return s, nil
}

// getOutputVarKeys returns the keys of a map as a slice of strings for debugging
func getOutputVarKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// convertMapToEventLog converts a log map from receipt to types.Log structure for event parsing
func (r *ContractWriteProcessor) convertMapToEventLog(logMap map[string]interface{}) *types.Log {
	eventLog := &types.Log{}

	// Tenderly HTTP/RPC may nest raw EVM log fields under "raw"
	// If present, promote nested raw fields for uniform parsing
	if rawAny, hasRaw := logMap["raw"]; hasRaw {
		if rawMap, ok := rawAny.(map[string]interface{}); ok {
			// Merge raw fields on top-level for our parser expectations
			// Do not overwrite existing top-level keys if already present
			if _, ok := logMap["address"]; !ok {
				if v, ok2 := rawMap["address"].(string); ok2 {
					logMap["address"] = v
				}
			}
			if _, ok := logMap["topics"]; !ok {
				if v, ok2 := rawMap["topics"].([]interface{}); ok2 {
					logMap["topics"] = v
				}
			}
			if _, ok := logMap["data"]; !ok {
				if v, ok2 := rawMap["data"].(string); ok2 {
					logMap["data"] = v
				}
			}
		}
	}

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
