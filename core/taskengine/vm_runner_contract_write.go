package taskengine

import (
	"bytes"
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
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/byte4"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/eip1559"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/bundler"
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
		return "", "", nil, NewMissingRequiredFieldError("contractAddress")
	}

	// Validate contract address format
	if !common.IsHexAddress(contractAddress) {
		return "", "", nil, NewInvalidAddressError(contractAddress)
	}

	if len(methodCalls) == 0 {
		return "", "", nil, NewMissingRequiredFieldError("methodCalls or callData")
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
	var senderAddress common.Address
	if aaSenderVar, ok := r.vm.vars["aa_sender"]; ok {
		if aaSenderStr, ok := aaSenderVar.(string); ok && aaSenderStr != "" {
			senderAddress = common.HexToAddress(aaSenderStr) // This is the runner (smart wallet)
			r.vm.logger.Info("CONTRACT WRITE - Sender address resolved from aa_sender",
				"sender_address_runner", senderAddress.Hex(),
				"aa_sender_var", aaSenderStr)
		} else {
			return &avsproto.ContractWriteNode_MethodResult{
				MethodName: methodCall.MethodName,
				Success:    false,
				Error:      fmt.Sprintf("aa_sender variable is set but invalid - must be a non-empty hex address string, got: %v", aaSenderVar),
			}
		}
	} else {
		// This should never happen because RunNodeImmediately validates settings.runner and sets aa_sender
		// If we get here, it means validation was bypassed or there's a bug in the validation logic
		return &avsproto.ContractWriteNode_MethodResult{
			MethodName: methodCall.MethodName,
			Success:    false,
			Error:      "aa_sender variable not set - settings.runner is required for contractWrite",
		}
	}

	// Substitute template variables in methodParams before generating calldata
	// Use preprocessTextWithVariableMapping for each parameter to support dot notation like {{value.address}}
	resolvedMethodParams := make([]string, len(methodCall.MethodParams))
	for i, param := range methodCall.MethodParams {
		resolvedMethodParams[i] = r.vm.preprocessTextWithVariableMapping(param)

		// Validate that template resolution didn't produce "undefined" values
		if strings.Contains(resolvedMethodParams[i], "undefined") {
			if r.vm != nil && r.vm.logger != nil {
				r.vm.logger.Error("❌ CONTRACT WRITE - Template variable failed to resolve",
					"method", methodCall.MethodName,
					"original_param", param,
					"resolved_param", resolvedMethodParams[i],
					"explanation", "This may be due to an undefined variable, incorrect template syntax, or unsupported variable names (e.g., variables with hyphens are not supported; use snake_case such as 'recipient_address' instead of 'recipient-address').")
			}
			return &avsproto.ContractWriteNode_MethodResult{
				MethodName: methodCall.MethodName,
				Success:    false,
				Error:      fmt.Sprintf("template variable resolution failed in parameter %d: '%s' resolved to '%s'", i, param, resolvedMethodParams[i]),
			}
		}
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
									// Field missing - return error immediately
									if r.vm != nil && r.vm.logger != nil {
										r.vm.logger.Error("❌ CONTRACT WRITE - Missing field in struct object",
											"method", methodCall.MethodName,
											"missing_field", fieldName,
											"available_fields", GetMapKeys(objData))
									}
									return &avsproto.ContractWriteNode_MethodResult{
										MethodName: methodCall.MethodName,
										Success:    false,
										Error:      fmt.Sprintf("missing required field '%s' in struct parameter for method '%s'", fieldName, methodCall.MethodName),
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

		// Use shared Tenderly client from VM
		tenderlyClient := r.vm.tenderlyClient
		if tenderlyClient == nil {
			return &avsproto.ContractWriteNode_MethodResult{
				MethodName: methodCall.MethodName,
				Success:    false,
				Error:      "tenderlyClient is nil - cannot simulate contract write",
				Value:      nil,
			}
		}

		// Get chain ID for simulation from settings only
		var chainID int64
		foundChainID := false

		// Get chain_id from settings (snake_case only)
		if settingsIface, ok := r.vm.vars["settings"]; ok {
			if settings, ok := settingsIface.(map[string]interface{}); ok {
				if cid, ok := settings["chain_id"]; ok {
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
					r.vm.logger.Debug("ContractWrite: Found chainId in settings", "chain_id", chainID)
				} else {
					r.vm.logger.Debug("ContractWrite: chainId not found in settings")
				}
			} else {
				r.vm.logger.Debug("ContractWrite: settings is not a valid object")
			}
		} else {
			r.vm.logger.Debug("ContractWrite: settings not found in VM variables")
		}

		if !foundChainID {
			return &avsproto.ContractWriteNode_MethodResult{
				MethodName: methodName,
				Success:    false,
				Error:      "settings.chain_id is required for contractWrite",
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
		transactionValue := r.extractTransactionValue(methodName, contractAddress.Hex())

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

	// Initialize execution log builder to capture all details
	var executionLogBuilder strings.Builder
	executionLogBuilder.WriteString(fmt.Sprintf("UserOp Transaction Execution for %s\n", methodName))
	executionLogBuilder.WriteString(fmt.Sprintf("Contract: %s\n", contractAddress.Hex()))
	executionLogBuilder.WriteString(fmt.Sprintf("Owner EOA: %s\n", r.owner.Hex()))

	// Convert hex calldata to bytes
	callDataBytes := common.FromHex(callData)

	// 🔍 PRE-FLIGHT VALIDATION: Check for common failure scenarios before gas estimation
	if validationErr := r.validateTransactionBeforeGasEstimation(methodName, callData, callDataBytes, contractAddress); validationErr != nil {
		executionLogBuilder.WriteString(fmt.Sprintf("❌ PRE-FLIGHT VALIDATION FAILED: %v\n", validationErr))
		executionLogBuilder.WriteString("Skipped gas estimation to avoid bundler error\n")

		r.vm.logger.Error("🚫 PRE-FLIGHT VALIDATION FAILED - Skipping gas estimation to avoid bundler error",
			"validation_error", validationErr.Error(),
			"method", methodName,
			"contract", contractAddress.Hex())
		return &avsproto.ContractWriteNode_MethodResult{
			Success: false,
			Error:   fmt.Sprintf("Pre-flight validation failed: %v", validationErr),
		}
	}

	// Create smart wallet execute calldata: execute(target, value, data)
	executionLogBuilder.WriteString(fmt.Sprintf("Packing smart wallet execute calldata...\n"))
	executionLogBuilder.WriteString(fmt.Sprintf("  Target contract: %s\n", contractAddress.Hex()))
	executionLogBuilder.WriteString(fmt.Sprintf("  ETH value: 0\n"))
	executionLogBuilder.WriteString(fmt.Sprintf("  Method calldata: %d bytes\n", len(callDataBytes)))

	smartWalletCallData, err := aa.PackExecute(
		contractAddress, // target contract
		big.NewInt(0),   // ETH value (0 for contract calls)
		callDataBytes,   // contract method calldata
	)
	if err != nil {
		executionLogBuilder.WriteString(fmt.Sprintf("❌ CALLDATA PACKING FAILED: %v\n", err))

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

	executionLogBuilder.WriteString(fmt.Sprintf("✅ Smart wallet calldata packed: %d bytes\n", len(smartWalletCallData)))

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
		executionLogBuilder.WriteString("WARNING: aa_sender not found in VM vars\n")
		r.vm.logger.Error("🚨 DEPLOYED WORKFLOW ERROR: aa_sender not found in VM vars",
			"owner_eoaAddress", r.owner.Hex())
	} else {
		executionLogBuilder.WriteString(fmt.Sprintf("✅ Smart wallet sender: %s\n", senderOverride.Hex()))
	}

	// Add paymaster information to execution log
	if paymasterReq != nil {
		executionLogBuilder.WriteString(fmt.Sprintf("Using paymaster: %s\n", r.smartWalletConfig.PaymasterAddress.Hex()))
	} else {
		executionLogBuilder.WriteString("No paymaster (self-funded transaction)\n")
	}

	executionLogBuilder.WriteString(fmt.Sprintf("Bundler URL: %s\n", r.smartWalletConfig.BundlerURL))

	// Pre-send gas estimation to capture in logs
	executionLogBuilder.WriteString("Performing gas estimation...\n")

	// Create a temporary UserOp for gas estimation
	rpcClient, rpcErr := ethclient.Dial(r.smartWalletConfig.EthRpcUrl)
	if rpcErr != nil {
		executionLogBuilder.WriteString(fmt.Sprintf("❌ Failed to connect to RPC: %v\n", rpcErr))
	} else {
		defer rpcClient.Close()

		_, bundlerErr := bundler.NewBundlerClient(r.smartWalletConfig.BundlerURL)
		if bundlerErr != nil {
			executionLogBuilder.WriteString(fmt.Sprintf("❌ Failed to create bundler client: %v\n", bundlerErr))
		} else {
			// Check smart wallet balance
			smartWalletAddr := senderOverride
			if smartWalletAddr != nil {
				if balance, balErr := rpcClient.BalanceAt(ctx, *smartWalletAddr, nil); balErr == nil {
					executionLogBuilder.WriteString(fmt.Sprintf("Smart wallet balance: %s wei\n", balance.String()))
				} else {
					executionLogBuilder.WriteString(fmt.Sprintf("❌ Failed to check balance: %v\n", balErr))
				}
			}

			// Try to get current gas prices
			if maxFee, maxPriority, feeErr := eip1559.SuggestFee(rpcClient); feeErr == nil {
				executionLogBuilder.WriteString(fmt.Sprintf("Current gas prices:\n"))
				executionLogBuilder.WriteString(fmt.Sprintf("  MaxFeePerGas: %s wei\n", maxFee.String()))
				executionLogBuilder.WriteString(fmt.Sprintf("  MaxPriorityFeePerGas: %s wei\n", maxPriority.String()))
			} else {
				executionLogBuilder.WriteString(fmt.Sprintf("❌ Failed to get gas prices: %v\n", feeErr))
			}
		}
	}

	executionLogBuilder.WriteString("Sending UserOp to bundler...\n")

	// Send UserOp transaction with correct parameters:
	// - owner: EOA address (r.owner) for smart wallet derivation
	// - senderOverride: smart wallet address (aa_sender) for the actual transaction
	userOp, receipt, err := r.sendUserOpFunc(
		r.smartWalletConfig,
		r.owner, // Use EOA address (owner) for smart wallet derivation
		smartWalletCallData,
		// TODO: Make paymasterReq configurable if paymaster-sponsored contract writes are needed in the future.
		nil,            // No paymaster for contract writes (self-funded). See TODO above.
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
		// Add detailed error information to execution log
		executionLogBuilder.WriteString(fmt.Sprintf("❌ BUNDLER FAILED: UserOp transaction could not be sent\n"))
		executionLogBuilder.WriteString(fmt.Sprintf("Error: %v\n", err))

		// Check if this is specifically the AA21 prefund error and add detailed explanation
		if strings.Contains(err.Error(), "AA21") {
			executionLogBuilder.WriteString("AA21 PREFUND ERROR DETECTED\n")
			executionLogBuilder.WriteString("This indicates insufficient ETH balance for gas fees\n")
			executionLogBuilder.WriteString("Solution: Fund the smart wallet with ETH for gas fees\n")

			// Add gas estimation details if available from userOp
			if userOp != nil {
				executionLogBuilder.WriteString("Gas Requirements (if estimated):\n")
				// Only show gas limits if they were actually estimated (not default values)
				// Use the shared constant from preset package to avoid duplication
				defaultCallGasLimit := preset.DEFAULT_CALL_GAS_LIMIT
				defaultVerificationGasLimit := preset.DEFAULT_VERIFICATION_GAS_LIMIT
				defaultPreVerificationGas := preset.DEFAULT_PREVERIFICATION_GAS

				if userOp.CallGasLimit != nil && userOp.CallGasLimit.Cmp(defaultCallGasLimit) != 0 {
					executionLogBuilder.WriteString(fmt.Sprintf("  CallGasLimit: %s\n", userOp.CallGasLimit.String()))
				}
				if userOp.VerificationGasLimit != nil && userOp.VerificationGasLimit.Cmp(defaultVerificationGasLimit) != 0 {
					executionLogBuilder.WriteString(fmt.Sprintf("  VerificationGasLimit: %s\n", userOp.VerificationGasLimit.String()))
				}
				if userOp.PreVerificationGas != nil && userOp.PreVerificationGas.Cmp(defaultPreVerificationGas) != 0 {
					executionLogBuilder.WriteString(fmt.Sprintf("  PreVerificationGas: %s\n", userOp.PreVerificationGas.String()))
				}
				if userOp.MaxFeePerGas != nil {
					executionLogBuilder.WriteString(fmt.Sprintf("  MaxFeePerGas: %s wei\n", userOp.MaxFeePerGas.String()))
				}
			}
		}

		r.vm.logger.Error("🚫 BUNDLER FAILED - UserOp transaction failed, workflow execution FAILED",
			"bundler_error", err,
			"bundler_url", r.smartWalletConfig.BundlerURL,
			"method", methodName,
			"contract", contractAddress.Hex(),
			"sender_smart_wallet", func() string {
				if senderOverride != nil {
					return senderOverride.Hex()
				}
				return "not_set"
			}(),
			"owner_eoa", r.owner.Hex())

		// Check if this is specifically the AA21 prefund error
		if strings.Contains(err.Error(), "AA21") {
			r.vm.logger.Error("🚨 AA21 PREFUND ERROR DETECTED - This indicates insufficient ETH balance for gas fees",
				"error_code", "AA21",
				"meaning", "didn't pay prefund",
				"solution", "Fund the smart wallet with ETH for gas fees")
		}

		// Create comprehensive error message with all details
		var errorBuilder strings.Builder
		errorBuilder.WriteString(fmt.Sprintf("Bundler failed - UserOp transaction could not be sent: %v\n", err))
		errorBuilder.WriteString(executionLogBuilder.String())

		// Return error result with detailed execution log
		return &avsproto.ContractWriteNode_MethodResult{
			Success:    false,
			Error:      errorBuilder.String(),
			MethodName: methodName,
		}
	}

	// Add success information to execution log
	executionLogBuilder.WriteString("✅ BUNDLER SUCCESS: UserOp transaction sent successfully\n")
	if userOp != nil {
		executionLogBuilder.WriteString(fmt.Sprintf("UserOp Hash: %s\n", r.getUserOpHashOrPending(receipt)))
		executionLogBuilder.WriteString(fmt.Sprintf("Sender: %s\n", userOp.Sender.Hex()))
		executionLogBuilder.WriteString(fmt.Sprintf("Nonce: %s\n", userOp.Nonce.String()))
	}
	if receipt != nil {
		executionLogBuilder.WriteString(fmt.Sprintf("Transaction Hash: %s\n", receipt.TxHash.Hex()))
		executionLogBuilder.WriteString(fmt.Sprintf("Gas Used: %d\n", receipt.GasUsed))
		executionLogBuilder.WriteString(fmt.Sprintf("Block Number: %d\n", receipt.BlockNumber.Uint64()))
	}

	// Create result from real transaction
	result := r.createRealTransactionResult(methodName, contractAddress.Hex(), callData, parsedABI, userOp, receipt)

	// If result has additional error information, append our detailed logs
	if result != nil && !result.Success && result.Error != "" {
		result.Error = result.Error + "\n" + executionLogBuilder.String()
	}

	return result
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
		"transactionHash":   result.Transaction.Hash,                                              // ✅ From Tenderly
		"from":              result.Transaction.From,                                              // ✅ From Tenderly
		"to":                result.Transaction.To,                                                // ✅ From Tenderly
		"blockNumber":       "0x1",                                                                // Mock value for simulation
		"blockHash":         "0x0000000000000000000000000000000000000000000000000000000000000001", // Mock value
		"transactionIndex":  "0x0",                                                                // Mock value for simulation
		"gasUsed":           r.getGasUsedWithFallback(result, StandardGasCostHex),
		"cumulativeGasUsed": r.getGasUsedWithFallback(result, StandardGasCostHex),
		"effectiveGasPrice": r.getEffectiveGasPriceWithFallback(result),
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
// getGasUsedFromTenderly extracts gas used from Tenderly simulation result or returns empty string
func (r *ContractWriteProcessor) getGasUsedFromTenderly(result *ContractWriteSimulationResult) string {
	if result != nil && result.GasUsed != "" {
		// Convert decimal string to hex for receipt format
		if gasUsedBig, ok := new(big.Int).SetString(result.GasUsed, 10); ok {
			return fmt.Sprintf("0x%x", gasUsedBig)
		}
	}
	// Return empty string when real gas data is unavailable - don't show fake values
	return ""
}

// getGasUsedWithFallback returns gas used from Tenderly or fallback value if unavailable
func (r *ContractWriteProcessor) getGasUsedWithFallback(result *ContractWriteSimulationResult, fallbackValue string) string {
	if gasUsed := r.getGasUsedFromTenderly(result); gasUsed != "" {
		return gasUsed
	}
	// Use fallback gas cost for receipt compatibility
	return fallbackValue
}

// getGasPriceFromTenderly extracts gas price from Tenderly simulation result or returns empty string
func (r *ContractWriteProcessor) getGasPriceFromTenderly(result *ContractWriteSimulationResult) string {
	if result != nil && result.GasPrice != "" {
		// Convert decimal string to hex for receipt format
		if gasPriceBig, ok := new(big.Int).SetString(result.GasPrice, 10); ok {
			return fmt.Sprintf("0x%x", gasPriceBig)
		}
	}
	// Return empty string when real gas data is unavailable - don't show fake values
	return ""
}

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
			finalizeExecutionStepWithError(s, false, err, log.String())
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

	// Track decimal formatting information - collect methods that provide decimal values
	// and fields that need formatting with those decimal values
	var decimalProviders = make(map[string]*big.Int) // methodName -> decimal value

	// Execute each method call
	ctx := context.Background()
	for i, methodCall := range methodCalls {
		log.WriteString(fmt.Sprintf("Executing method %d: %s\n", i+1, methodCall.MethodName))

		// Add panic recovery to ensure individual method failures don't break the loop
		var result *avsproto.ContractWriteNode_MethodResult
		func() {
			defer func() {
				if rcv := recover(); rcv != nil {
					// Enhanced panic recovery with Sentry reporting
					if r.vm != nil && r.vm.logger != nil {
						r.vm.logger.Error("🚨 PANIC in executeMethodCall - capturing for Sentry",
							"panic", fmt.Sprintf("%v", rcv),
							"method", methodCall.MethodName,
							"contract", contractAddr.Hex())
					}

					// Report to Sentry with context
					enhancedPanicRecovery("contract_write", "executeMethodCall", map[string]interface{}{
						"method":     methodCall.MethodName,
						"contract":   contractAddr.Hex(),
						"panic_type": fmt.Sprintf("%T", rcv),
					})

					log.WriteString(fmt.Sprintf("🚨 PANIC in executeMethodCall: %v\n", rcv))
					result = &avsproto.ContractWriteNode_MethodResult{
						MethodName: methodCall.MethodName,
						Success:    false,
						Error:      fmt.Sprintf("panic during execution: %v", rcv),
					}

					// Clean up any partial state to prevent memory leaks
					if r.vm != nil {
						r.cleanupPartialExecutionState(methodCall.MethodName)
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

			// Add detailed failure information to execution log
			log.WriteString(fmt.Sprintf("❌ Failed: %s - %s\n", result.MethodName, result.Error))

			// If this is a bundler/AA error, add additional debugging information
			if strings.Contains(result.Error, "Bundler failed") || strings.Contains(result.Error, "AA21") {
				log.WriteString("BUNDLER FAILURE DETAILS:\n")
				log.WriteString(fmt.Sprintf("  Bundler URL: %s\n", r.smartWalletConfig.BundlerURL))
				log.WriteString(fmt.Sprintf("  Entry Point: %s\n", r.smartWalletConfig.EntrypointAddress.Hex()))
				log.WriteString(fmt.Sprintf("  Factory: %s\n", r.smartWalletConfig.FactoryAddress.Hex()))

				if strings.Contains(result.Error, "AA21") {
					log.WriteString("AA21 ERROR EXPLANATION:\n")
					log.WriteString("  - AA21 means 'didn't pay prefund'\n")
					log.WriteString("  - This indicates insufficient ETH balance for gas fees\n")
					log.WriteString("  - Solution: Fund the smart wallet with ETH\n")
					log.WriteString(fmt.Sprintf("  - Smart wallet address: %s\n", func() string {
						if v, ok := r.vm.vars["aa_sender"]; ok {
							if s, ok2 := v.(string); ok2 {
								return s
							}
						}
						return "not_available"
					}()))
				}
			}
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

	// First pass to collect decimal information from method results
	// This identifies methods that provide decimal values for formatting other methods
	for i, methodCall := range methodCalls {
		if len(methodCall.ApplyToFields) > 0 && i < len(results) {
			methodResult := results[i]
			methodName := methodResult.MethodName

			// Check if this method provides decimal formatting for other fields
			if methodResult.Success && methodResult.Value != nil {
				valueMap, isMap := methodResult.Value.AsInterface().(map[string]interface{})
				if isMap && len(valueMap) > 0 {
					// For methods that return objects, try to find a decimal value
					// Look for fields like "decimals" or "decimal"
					for fieldName, fieldValue := range valueMap {
						if fieldName == "decimals" || fieldName == "decimal" {
							if strValue, ok := fieldValue.(string); ok {
								if decimalsInt, err := strconv.ParseInt(strValue, 10, 64); err == nil {
									decimalValue := big.NewInt(decimalsInt)
									decimalProviders[methodName] = decimalValue
									log.WriteString(fmt.Sprintf("✅ Method %s provides decimal value: %s\n", methodName, decimalValue.String()))
									r.vm.logger.Info("Method provides decimal value",
										"methodName", methodName,
										"decimalValue", decimalValue.String(),
										"applyToFields", methodCall.ApplyToFields)
								}
							}
						}
					}
				} else if strValue, ok := methodResult.Value.AsInterface().(string); ok {
					// For methods that return a single value (like ERC20 decimals())
					if decimalsInt, err := strconv.ParseInt(strValue, 10, 64); err == nil {
						decimalValue := big.NewInt(decimalsInt)
						decimalProviders[methodName] = decimalValue
						log.WriteString(fmt.Sprintf("✅ Method %s provides decimal value: %s\n", methodName, decimalValue.String()))
						r.vm.logger.Info("Method provides decimal value",
							"methodName", methodName,
							"decimalValue", decimalValue.String(),
							"applyToFields", methodCall.ApplyToFields)
					}
				}
			}
		}
	}

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

										// Method 2: Dynamic pool discovery from transaction logs
										// First pass: collect all unique addresses from logs to build relevant address list
										relevantAddresses := []string{contractAddress} // Always include the target contract

										// Scan all logs in this transaction to find pool addresses
										if poolAddresses := r.discoverPoolAddressesFromLogs(logsArray); len(poolAddresses) > 0 {
											for _, poolAddr := range poolAddresses {
												relevantAddresses = append(relevantAddresses, poolAddr)
											}
											r.vm.logger.Info("🔍 DYNAMIC POOL DISCOVERY - Found pool addresses from transaction logs",
												"method", methodResult.MethodName,
												"pool_count", len(poolAddresses),
												"pools", poolAddresses)
										}

										// Check if current log is from any relevant address
										isRelevantAddress := false
										for _, addr := range relevantAddresses {
											if strings.EqualFold(eventLog.Address.Hex(), addr) {
												isRelevantAddress = true
												break
											}
										}

										if !isRelevantAddress {
											r.vm.logger.Debug("🔍 EVENT DEBUG - Address not relevant, skipping",
												"log_address", eventLog.Address.Hex(),
												"relevant_addresses", relevantAddresses)
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

			// Apply decimal formatting to event data if needed
			for decimalProviderMethod, decimalsValue := range decimalProviders {
				for _, methodCall := range methodCalls {
					// Find the method call that provides formatting details for this event
					if methodCall.MethodName == decimalProviderMethod && len(methodCall.ApplyToFields) > 0 {
						// Create decimal formatting context
						ctx := NewDecimalFormattingContext(decimalsValue, methodCall.ApplyToFields, decimalProviderMethod)
						// Apply formatting to the event data
						ctx.ApplyDecimalFormattingToEventData(methodEvents, methodName, r.vm.logger)
						break
					}
				}
			}
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

	// Use shared function to set output variable for this step
	setNodeOutputData(r.CommonProcessor, stepID, outputVars)

	// Determine step success: any failed method or receipt.status == 0x0 marks the step as failed
	stepSuccess, stepErrorMsg := computeWriteStepSuccess(results)

	// Calculate total gas costs from all method results
	totalGasUsed := "0"
	totalGasPrice := "0"
	totalGasCost := "0"
	hasGasInfo := false

	for _, methodResult := range results {
		if methodResult.Receipt != nil {
			if receiptMap := methodResult.Receipt.AsInterface().(map[string]interface{}); receiptMap != nil {
				// Extract gas information from receipt
				if gasUsedHex, ok := receiptMap["gasUsed"].(string); ok && gasUsedHex != "" {
					// Convert hex to decimal for aggregation
					gasUsedBig := new(big.Int)
					if _, ok := gasUsedBig.SetString(strings.TrimPrefix(gasUsedHex, "0x"), 16); ok {
						// Add to total gas used
						totalGasUsedBig := new(big.Int)
						if _, ok := totalGasUsedBig.SetString(totalGasUsed, 10); ok {
							totalGasUsedBig.Add(totalGasUsedBig, gasUsedBig)
							totalGasUsed = totalGasUsedBig.String()
							hasGasInfo = true
						}

						// Get gas price (prefer effectiveGasPrice over gasPrice)
						if effectiveGasPriceHex, ok := receiptMap["effectiveGasPrice"].(string); ok && effectiveGasPriceHex != "" {
							gasPriceBig := new(big.Int)
							if _, ok := gasPriceBig.SetString(strings.TrimPrefix(effectiveGasPriceHex, "0x"), 16); ok {
								totalGasPrice = gasPriceBig.String()
								// Calculate gas cost for this method: gasUsed * gasPrice
								methodGasCost := new(big.Int).Mul(gasUsedBig, gasPriceBig)
								totalGasCostBig := new(big.Int)
								if _, ok := totalGasCostBig.SetString(totalGasCost, 10); ok {
									totalGasCostBig.Add(totalGasCostBig, methodGasCost)
									totalGasCost = totalGasCostBig.String()
								}
							}
						}
					}
				}
			}
		}
	}

	// Check for gas information from Tenderly simulations
	if !hasGasInfo {
		// Try to extract gas info from simulation results stored in the results
		for _, methodResult := range results {
			if methodResult.Receipt != nil {
				if receiptMap := methodResult.Receipt.AsInterface().(map[string]interface{}); receiptMap != nil {
					// Use helper method to extract and validate gas data
					gasResult := r.extractValidGasDataFromReceipt(receiptMap)
					if gasResult.valid {
						totalGasUsed = gasResult.gasUsed.String()
						totalGasPrice = gasResult.gasPrice.String()
						totalGasCost = gasResult.gasCost.String()
						hasGasInfo = true
						break // Found valid gas info, no need to check other results
					}
				}
			}
		}
	}

	// Set gas cost information in the execution step if we found any
	if hasGasInfo && totalGasUsed != "0" {
		s.GasUsed = totalGasUsed
		s.GasPrice = totalGasPrice
		s.TotalGasCost = totalGasCost
		r.vm.logger.Info("✅ Set gas cost information in execution step",
			"step_id", stepID,
			"gas_used", totalGasUsed,
			"gas_price", totalGasPrice,
			"total_gas_cost", totalGasCost)
	} else {
		r.vm.logger.Debug("⚠️ No gas cost information available for execution step",
			"step_id", stepID,
			"results_count", len(results))
	}

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

// extractTransactionValue extracts the transaction value from nodeConfig with proper error handling
func (r *ContractWriteProcessor) extractTransactionValue(methodName, contractAddress string) string {
	transactionValue := "0" // Default to 0 if not specified

	r.vm.mu.Lock()
	defer r.vm.mu.Unlock()

	nodeConfigIface, exists := r.vm.vars["nodeConfig"]
	if !exists {
		return transactionValue
	}

	nodeConfig, ok := nodeConfigIface.(map[string]interface{})
	if !ok {
		return transactionValue
	}

	valueIface, exists := nodeConfig["value"]
	if !exists {
		return transactionValue
	}

	valueStr, ok := valueIface.(string)
	if !ok || valueStr == "" {
		return transactionValue
	}

	if r.vm.logger != nil {
		r.vm.logger.Info("Using transaction value from configuration",
			"value", valueStr,
			"method", methodName,
			"contract", contractAddress)
	}

	return valueStr
}

// calculatePoolAddresses calculates pool addresses from router method calldata
func (r *ContractWriteProcessor) calculatePoolAddresses(methodName, callData string, parsedABI *abi.ABI) []common.Address {
	var poolAddresses []common.Address

	if parsedABI == nil || callData == "" {
		return poolAddresses
	}

	// Handle Uniswap V3 exactInputSingle
	if methodName == "exactInputSingle" {
		if pools := r.calculateUniswapV3PoolFromExactInputSingle(callData, parsedABI); len(pools) > 0 {
			poolAddresses = append(poolAddresses, pools...)
		}
	}

	// Handle other DEX methods as needed
	// TODO: Add support for other methods like exactInput, swapExactTokensForTokens, etc.

	return poolAddresses
}

// calculateUniswapV3PoolFromExactInputSingle extracts pool address from exactInputSingle calldata
func (r *ContractWriteProcessor) calculateUniswapV3PoolFromExactInputSingle(callData string, parsedABI *abi.ABI) []common.Address {
	var poolAddresses []common.Address

	// Get the exactInputSingle method from ABI
	method, exists := parsedABI.Methods["exactInputSingle"]
	if !exists {
		return poolAddresses
	}

	// Decode the calldata
	callDataBytes := common.FromHex(callData)
	if len(callDataBytes) < 4 {
		return poolAddresses
	}

	// Remove the method selector (first 4 bytes)
	inputData := callDataBytes[4:]

	// Unpack the parameters
	values, err := method.Inputs.Unpack(inputData)
	if err != nil {
		r.vm.logger.Warn("Failed to unpack exactInputSingle parameters", "error", err)
		return poolAddresses
	}

	if len(values) == 0 {
		return poolAddresses
	}

	// ExactInputSingleParams struct should be the first parameter
	if paramsStruct, ok := values[0].(struct {
		TokenIn           common.Address
		TokenOut          common.Address
		Fee               *big.Int
		Recipient         common.Address
		Deadline          *big.Int
		AmountIn          *big.Int
		AmountOutMinimum  *big.Int
		SqrtPriceLimitX96 *big.Int
	}); ok {
		// Calculate pool address using Uniswap V3 formula
		poolAddr := r.computeUniswapV3PoolAddress(paramsStruct.TokenIn, paramsStruct.TokenOut, paramsStruct.Fee.Uint64())
		if poolAddr != (common.Address{}) {
			poolAddresses = append(poolAddresses, poolAddr)
			r.vm.logger.Info("🔍 POOL CALCULATION - Calculated Uniswap V3 pool address",
				"tokenIn", paramsStruct.TokenIn.Hex(),
				"tokenOut", paramsStruct.TokenOut.Hex(),
				"fee", paramsStruct.Fee.Uint64(),
				"poolAddress", poolAddr.Hex())
		}
	} else {
		// Try alternative unpacking approach for different struct layouts
		r.vm.logger.Warn("Failed to cast exactInputSingle params to expected struct, trying alternative approach")

		// Manual parameter extraction based on ABI structure
		if len(method.Inputs) >= 1 && method.Inputs[0].Type.String() == "tuple" {
			// The tuple should contain (tokenIn, tokenOut, fee, recipient, deadline, amountIn, amountOutMinimum, sqrtPriceLimitX96)
			if tupleValues, ok := values[0].([]interface{}); ok && len(tupleValues) >= 8 {
				if tokenIn, ok1 := tupleValues[0].(common.Address); ok1 {
					if tokenOut, ok2 := tupleValues[1].(common.Address); ok2 {
						if fee, ok3 := tupleValues[2].(*big.Int); ok3 {
							poolAddr := r.computeUniswapV3PoolAddress(tokenIn, tokenOut, fee.Uint64())
							if poolAddr != (common.Address{}) {
								poolAddresses = append(poolAddresses, poolAddr)
								r.vm.logger.Info("🔍 POOL CALCULATION - Calculated Uniswap V3 pool address (alternative method)",
									"tokenIn", tokenIn.Hex(),
									"tokenOut", tokenOut.Hex(),
									"fee", fee.Uint64(),
									"poolAddress", poolAddr.Hex())
							}
						}
					}
				}
			}
		}
	}

	return poolAddresses
}

// computeUniswapV3PoolAddress computes the deterministic pool address for Uniswap V3
func (r *ContractWriteProcessor) computeUniswapV3PoolAddress(tokenA, tokenB common.Address, fee uint64) common.Address {
	// Uniswap V3 Factory address on mainnet/testnets
	factory := common.HexToAddress("0x1F98431c8aD98523631AE4a59f267346ea31F984")

	// Ensure token0 < token1 (Uniswap V3 convention)
	token0, token1 := tokenA, tokenB
	if bytes.Compare(tokenA.Bytes(), tokenB.Bytes()) > 0 {
		token0, token1 = tokenB, tokenA
	}

	// Create the salt: keccak256(abi.encode(token0, token1, fee))
	feeBytes := make([]byte, 32)
	big.NewInt(int64(fee)).FillBytes(feeBytes[32-3:]) // fee is uint24, so last 3 bytes

	salt := crypto.Keccak256Hash(
		common.LeftPadBytes(token0.Bytes(), 32),
		common.LeftPadBytes(token1.Bytes(), 32),
		feeBytes,
	)

	// Uniswap V3 Pool bytecode hash (this is the init code hash for pool contracts)
	initCodeHash := common.HexToHash("0xe34f199b19b2b4f47f68442619d555527d244f78a3297ea89325f843f87b8b54")

	// CREATE2 address calculation: keccak256(0xff + factory + salt + initCodeHash)[12:]
	data := append([]byte{0xff}, factory.Bytes()...)
	data = append(data, salt.Bytes()...)
	data = append(data, initCodeHash.Bytes()...)

	hash := crypto.Keccak256Hash(data)

	// Take the last 20 bytes as the address
	var addr common.Address
	copy(addr[:], hash[12:])

	return addr
}

// discoverPoolAddressesFromLogs scans transaction logs to find pool addresses dynamically
func (r *ContractWriteProcessor) discoverPoolAddressesFromLogs(logsArray []interface{}) []string {
	var poolAddresses []string
	addressSet := make(map[string]bool) // Use map to avoid duplicates

	// Known DEX event signatures that indicate a pool/token contract
	dexEventSignatures := map[string]string{
		"0xc42079f94a6350d7e6235f29174924f928cc2ac818eb64fed8004e115fbcca67": "Uniswap V3 Swap",
		"0xd78ad95fa46c994b6551d0da85fc275fe613ce37657fb8d5e3d130840159d822": "Uniswap V2 Swap",
		"0x1c411e9a96e071241c2f21f7726b17ae89e3cab4c78be50e062b03a9fffbbad1": "SushiSwap Swap",
		"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef": "ERC20 Transfer",
	}

	// First pass: scan all logs to identify pool/token addresses
	for _, logInterface := range logsArray {
		if logMap, ok := logInterface.(map[string]interface{}); ok {
			// Extract address and topics
			if addressInterface, hasAddress := logMap["address"]; hasAddress {
				if addressStr, ok := addressInterface.(string); ok {
					if topicsInterface, hasTopics := logMap["topics"]; hasTopics {
						if topicsArray, ok := topicsInterface.([]interface{}); ok && len(topicsArray) > 0 {
							if topic0Interface, ok := topicsArray[0].(string); ok {
								// Check if this log has a DEX-related event signature
								if eventType, isDexEvent := dexEventSignatures[topic0Interface]; isDexEvent {
									if !addressSet[addressStr] {
										addressSet[addressStr] = true
										poolAddresses = append(poolAddresses, addressStr)
										r.vm.logger.Info("🔍 DYNAMIC POOL DISCOVERY - Found DEX-related address",
											"address", addressStr,
											"event_type", eventType,
											"topic0", topic0Interface)
									}
								}
							}
						}
					}
				}
			}
		}
	}

	return poolAddresses
}

// validateTransactionBeforeGasEstimation performs pre-flight checks to avoid common gas estimation failures
func (r *ContractWriteProcessor) validateTransactionBeforeGasEstimation(methodName, callData string, callDataBytes []byte, contractAddress common.Address) error {
	// 1. Check for empty or invalid calldata
	if len(callDataBytes) < 4 {
		return fmt.Errorf("invalid calldata: too short (length: %d)", len(callDataBytes))
	}

	// 2. Method-specific validations
	switch methodName {
	case "exactInputSingle":
		return r.validateUniswapExactInputSingle(callData, callDataBytes)
	case "quoteExactInputSingle":
		return r.validateUniswapQuoteExactInputSingle(callData, callDataBytes)
	case "approve":
		return r.validateERC20Approve(callData, callDataBytes)
	}

	// 3. General validations for all methods
	return r.validateGeneralTransactionParameters(methodName, callDataBytes, contractAddress)
}

// validateUniswapExactInputSingle validates Uniswap V3 exactInputSingle parameters
func (r *ContractWriteProcessor) validateUniswapExactInputSingle(callData string, callDataBytes []byte) error {
	// Try to decode the parameters to check for invalid values
	if len(callDataBytes) < 4 {
		return fmt.Errorf("exactInputSingle: calldata too short")
	}

	// Remove method selector (first 4 bytes)
	inputData := callDataBytes[4:]
	if len(inputData) < 32*7 { // Minimum size for ExactInputSingleParams struct (7 parameters)
		return fmt.Errorf("exactInputSingle: insufficient parameter data")
	}

	// Extract key parameters (basic validation without full ABI decoding)
	// Parameter layout: (tokenIn, tokenOut, fee, recipient, amountIn, amountOutMinimum, sqrtPriceLimitX96)

	// Check amountIn (5th parameter, at offset 32*4)
	amountInOffset := 32 * 4
	if len(inputData) > amountInOffset+32 {
		amountInBytes := inputData[amountInOffset : amountInOffset+32]
		amountIn := new(big.Int).SetBytes(amountInBytes)

		if amountIn.Cmp(big.NewInt(0)) == 0 {
			return fmt.Errorf("exactInputSingle: amountIn is zero - cannot swap zero tokens")
		}

		r.vm.logger.Info("🔍 PRE-FLIGHT VALIDATION - exactInputSingle parameters",
			"amountIn", amountIn.String(),
			"amountIn_hex", fmt.Sprintf("0x%x", amountIn))
	}

	// Note: ExactInputSingleParams doesn't have a deadline field - deadline is handled by multicall wrapper if needed
	// So we skip deadline validation for this method

	return nil
}

// gasDataResult represents the result of gas data extraction from a receipt
type gasDataResult struct {
	gasUsed  *big.Int
	gasPrice *big.Int
	gasCost  *big.Int
	valid    bool
}

// extractValidGasDataFromReceipt extracts and validates gas data from a transaction receipt
// Returns gasDataResult with valid=true only if both gas used and gas price are real values
// (not our fallback constants) and can be parsed successfully
func (r *ContractWriteProcessor) extractValidGasDataFromReceipt(receiptMap map[string]interface{}) gasDataResult {
	result := gasDataResult{valid: false}

	// Look for simulation-specific patterns
	txHash, ok := receiptMap["transactionHash"].(string)
	if !ok || !strings.HasPrefix(txHash, "0x") || len(txHash) != 66 {
		return result
	}

	// Check if this receipt has standard gas information (could be from Tenderly)
	gasUsedHex, ok := receiptMap["gasUsed"].(string)
	if !ok || gasUsedHex == "" || gasUsedHex == StandardGasCostHex {
		return result
	}

	// This looks like real gas data, not our fallback values
	gasUsedBig := new(big.Int)
	if _, ok := gasUsedBig.SetString(strings.TrimPrefix(gasUsedHex, "0x"), 16); !ok {
		return result
	}

	// Try to get gas price - only consider it complete gas info if we have both
	effectiveGasPriceHex, ok := receiptMap["effectiveGasPrice"].(string)
	if !ok || effectiveGasPriceHex == "" || effectiveGasPriceHex == DefaultGasPriceHex {
		return result
	}

	gasPriceBig := new(big.Int)
	if _, ok := gasPriceBig.SetString(strings.TrimPrefix(effectiveGasPriceHex, "0x"), 16); !ok {
		return result
	}

	// Only set gas info when we have BOTH real gas used AND real gas price
	result.gasUsed = gasUsedBig
	result.gasPrice = gasPriceBig
	result.gasCost = new(big.Int).Mul(gasUsedBig, gasPriceBig)
	result.valid = true

	return result
}

// validateUniswapQuoteExactInputSingle validates Uniswap V3 quoteExactInputSingle parameters
func (r *ContractWriteProcessor) validateUniswapQuoteExactInputSingle(callData string, callDataBytes []byte) error {
	if len(callDataBytes) < 4 {
		return fmt.Errorf("quoteExactInputSingle: calldata too short")
	}

	// Remove method selector (first 4 bytes)
	inputData := callDataBytes[4:]
	if len(inputData) < 32*5 { // Minimum size for QuoteExactInputSingleParams struct
		return fmt.Errorf("quoteExactInputSingle: insufficient parameter data")
	}

	// Check amountIn (3rd parameter, at offset 32*2)
	amountInOffset := 32 * 2
	if len(inputData) > amountInOffset+32 {
		amountInBytes := inputData[amountInOffset : amountInOffset+32]
		amountIn := new(big.Int).SetBytes(amountInBytes)

		if amountIn.Cmp(big.NewInt(0)) == 0 {
			return fmt.Errorf("quoteExactInputSingle: amountIn is zero - cannot quote zero tokens")
		}

		r.vm.logger.Info("🔍 PRE-FLIGHT VALIDATION - quoteExactInputSingle parameters",
			"amountIn", amountIn.String())
	}

	return nil
}

// validateERC20Approve validates ERC20 approve parameters
func (r *ContractWriteProcessor) validateERC20Approve(callData string, callDataBytes []byte) error {
	if len(callDataBytes) < 4 {
		return fmt.Errorf("approve: calldata too short")
	}

	// Remove method selector (first 4 bytes)
	inputData := callDataBytes[4:]
	if len(inputData) < 64 { // spender (32 bytes) + amount (32 bytes)
		return fmt.Errorf("approve: insufficient parameter data")
	}

	// Extract spender address (first 32 bytes, but address is in last 20 bytes)
	spenderBytes := inputData[12:32] // Skip first 12 bytes of padding
	spender := common.BytesToAddress(spenderBytes)

	if spender == (common.Address{}) {
		return fmt.Errorf("approve: spender address is zero")
	}

	r.vm.logger.Info("🔍 PRE-FLIGHT VALIDATION - approve parameters",
		"spender", spender.Hex())

	return nil
}

// validateGeneralTransactionParameters performs general validation for all transaction types
func (r *ContractWriteProcessor) validateGeneralTransactionParameters(methodName string, callDataBytes []byte, contractAddress common.Address) error {
	// Check contract address is not zero
	if contractAddress == (common.Address{}) {
		return fmt.Errorf("%s: contract address is zero", methodName)
	}

	// Check method selector is valid (first 4 bytes)
	if len(callDataBytes) >= 4 {
		methodSelector := fmt.Sprintf("0x%x", callDataBytes[:4])
		r.vm.logger.Debug("🔍 PRE-FLIGHT VALIDATION - Method selector",
			"method", methodName,
			"selector", methodSelector)
	}

	return nil
}

// getEffectiveGasPriceWithFallback returns effective gas price from Tenderly or fallback value
// Extracted from inline anonymous function for better maintainability
func (r *ContractWriteProcessor) getEffectiveGasPriceWithFallback(result *ContractWriteSimulationResult) string {
	if gasPrice := r.getGasPriceFromTenderly(result); gasPrice != "" {
		return gasPrice
	}
	// Use default gas price as fallback for receipt compatibility
	return DefaultGasPriceHex
}

// getUserOpHashOrPending returns transaction hash from receipt or "pending" if unavailable
// Extracted from inline anonymous function for better maintainability
func (r *ContractWriteProcessor) getUserOpHashOrPending(receipt *types.Receipt) string {
	if receipt != nil {
		return receipt.TxHash.Hex()
	}
	return "pending"
}
