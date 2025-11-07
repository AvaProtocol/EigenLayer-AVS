package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"google.golang.org/protobuf/types/known/structpb"
)

// getRealisticBlockNumberForChain returns a realistic block number for simulation based on chain ID
// Only includes chains that the aggregator actually supports: Ethereum and Base
func getRealisticBlockNumberForChain(chainID int64) uint64 {
	switch chainID {
	case 1: // Ethereum mainnet
		return 19500000 + uint64(time.Now().Unix()%100000) // ~19.5M + small random offset
	case 11155111: // Sepolia testnet
		return 6500000 + uint64(time.Now().Unix()%100000) // ~6.5M + small random offset
	case 8453: // Base mainnet
		return 11500000 + uint64(time.Now().Unix()%100000) // ~11.5M + small random offset
	case 84532: // Base Sepolia testnet
		return 14400000 + uint64(time.Now().Unix()%100000) // ~14.4M + small random offset
	default:
		// Default to Sepolia-like numbers for unknown chains
		return 6500000 + uint64(time.Now().Unix()%100000)
	}
}

// RunNodeImmediately executes a single node immediately with authenticated user context.
// This is the primary entry point for running nodes that require user authentication.
// useSimulation defaults to true if not specified.
// RunNodeImmediately executes a node immediately with optional simulation mode
// useSimulation parameter (bool): true = simulation (default), false = real execution
func (n *Engine) RunNodeImmediately(nodeType string, nodeConfig map[string]interface{}, inputVariables map[string]interface{}, user *model.User, useSimulation ...interface{}) (map[string]interface{}, error) {
	// Default to simulation mode
	simulationMode := true

	// Parse optional simulation mode parameter
	if len(useSimulation) > 0 {
		if simMode, ok := useSimulation[0].(bool); ok {
			simulationMode = simMode
		}
	}

	if IsTriggerNodeType(nodeType) {
		return n.runTriggerImmediately(nodeType, nodeConfig, inputVariables)
	} else {
		return n.runProcessingNodeWithInputs(user, nodeType, nodeConfig, inputVariables, simulationMode)
	}
}

// runTriggerImmediately executes trigger nodes immediately, ignoring any scheduling configuration
func (n *Engine) runTriggerImmediately(triggerType string, triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	switch triggerType {
	case NodeTypeBlockTrigger:
		return n.runBlockTriggerImmediately(triggerConfig, inputVariables)
	case NodeTypeFixedTimeTrigger:
		return n.runFixedTimeTriggerImmediately(triggerConfig, inputVariables)
	case NodeTypeCronTrigger:
		return n.runCronTriggerImmediately(triggerConfig, inputVariables)
	case NodeTypeEventTrigger:
		return n.runEventTriggerImmediately(triggerConfig, inputVariables)
	case NodeTypeManualTrigger:
		return n.runManualTriggerImmediately(triggerConfig, inputVariables)
	default:
		return nil, fmt.Errorf("unsupported trigger type: %s", triggerType)
	}
}

// runBlockTriggerImmediately gets the latest block data immediately, ignoring any interval configuration
func (n *Engine) runBlockTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// For immediate execution, we ignore interval and always get the latest block
	// unless a specific blockNumber is provided
	var blockNumber uint64

	// Check if a specific block number is requested
	if configBlockNumber, ok := triggerConfig["blockNumber"]; ok {
		blockNum, err := n.parseUint64(configBlockNumber)
		if err != nil {
			if n.logger != nil {
				n.logger.Debug("Failed to parse blockNumber from trigger config, using latest block",
					"blockNumber", configBlockNumber,
					"error", err)
			}
		} else {
			blockNumber = blockNum
		}
	}

	// Ensure RPC connection is available
	if rpcConn == nil {
		return nil, fmt.Errorf("RPC connection not available for BlockTrigger execution")
	}

	// If no specific block number, get the latest block
	if blockNumber == 0 {
		currentBlock, err := rpcConn.BlockNumber(context.Background())
		if err != nil {
			// For simulations, use a mock block number to avoid RPC rate limiting
			if strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "429") {
				blockNumber = 8978000 // Use a recent mock block number for simulations
				if n.logger != nil {
					n.logger.Warn("BlockTrigger: Using mock block number due to RPC rate limiting", "mockBlockNumber", blockNumber, "rpcError", err.Error())
				}
			} else {
				return nil, fmt.Errorf("failed to get current block number from RPC: %w", err)
			}
		} else {
			blockNumber = currentBlock
			if n.logger != nil {
				n.logger.Info("BlockTrigger: Using latest block for immediate execution", "blockNumber", blockNumber)
			}
		}
	}

	// Get real block data from RPC
	header, err := rpcConn.HeaderByNumber(context.Background(), big.NewInt(int64(blockNumber)))
	if err != nil {
		// For simulations, use mock block data to avoid RPC rate limiting
		if strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "429") {
			if n.logger != nil {
				n.logger.Warn("BlockTrigger: Using mock block header due to RPC rate limiting", "blockNumber", blockNumber, "rpcError", err.Error())
			}
			// Create a mock header for simulation
			mockHash := common.HexToHash(fmt.Sprintf("0x%016x%016x%016x%016x", blockNumber, blockNumber+1, blockNumber+2, blockNumber+3))
			return map[string]interface{}{
				"blockNumber": blockNumber,
				"blockHash":   mockHash.Hex(),
				"timestamp":   time.Now().Unix(),
				"difficulty":  "0",
				"gasLimit":    uint64(30000000),
				"gasUsed":     uint64(15000000),
				"parentHash":  common.HexToHash(fmt.Sprintf("0x%016x%016x%016x%016x", blockNumber-1, blockNumber, blockNumber+1, blockNumber+2)).Hex(),
			}, nil
		}
		return nil, fmt.Errorf("failed to get block header for block %d from RPC: %w", blockNumber, err)
	}

	result := map[string]interface{}{
		"blockNumber": blockNumber,
		"blockHash":   header.Hash().Hex(),
		"timestamp":   header.Time,
		"parentHash":  header.ParentHash.Hex(),
		"difficulty":  header.Difficulty.String(),
		"gasLimit":    header.GasLimit,
		"gasUsed":     header.GasUsed,
	}

	if n.logger != nil {
		n.logger.Info("BlockTrigger executed immediately", "blockNumber", blockNumber, "blockHash", header.Hash().Hex())
	}
	return result, nil
}

// runFixedTimeTriggerImmediately returns the current timestamp immediately
func (n *Engine) runFixedTimeTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// For immediate execution, return current timestamp in milliseconds and ISO format
	currentTime := time.Now()
	currentTimestamp := uint64(currentTime.UnixMilli())
	currentTimestampISO := currentTime.UTC().Format("2006-01-02T15:04:05.000Z")

	result := map[string]interface{}{
		"timestamp":     currentTimestamp,
		"timestamp_iso": currentTimestampISO,
	}

	if n.logger != nil {
		n.logger.Info("FixedTimeTrigger executed immediately", "timestamp", currentTimestamp, "timestamp_iso", currentTimestampISO)
	}
	return result, nil
}

// runCronTriggerImmediately returns the current timestamp immediately
func (n *Engine) runCronTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// For immediate execution, return current timestamp in milliseconds and ISO format
	currentTime := time.Now()
	currentTimestamp := uint64(currentTime.UnixMilli())
	currentTimestampISO := currentTime.UTC().Format("2006-01-02T15:04:05.000Z")

	result := map[string]interface{}{
		"timestamp":     currentTimestamp,
		"timestamp_iso": currentTimestampISO,
	}

	if n.logger != nil {
		n.logger.Info("CronTrigger executed immediately", "timestamp", currentTimestamp, "timestamp_iso", currentTimestampISO)
	}
	return result, nil
}

// validateTopicHexFormat validates that a topic string is properly formatted as a hex value
// Returns error if the topic contains malformed hex (e.g., "0x...00x..." from bad template substitution)
func validateTopicHexFormat(topic string) error {
	if topic == "" {
		return nil // Empty/null topics are valid (wildcards)
	}

	// Check for double "0x" prefix (common template substitution error)
	if strings.Contains(strings.ToLower(topic), "0x") {
		firstIndex := strings.Index(strings.ToLower(topic), "0x")
		secondIndex := strings.Index(strings.ToLower(topic[firstIndex+1:]), "0x")
		if secondIndex != -1 {
			return fmt.Errorf("malformed topic hex value - contains multiple '0x' prefixes (likely from incorrect template substitution): %s", topic)
		}
	}

	// Validate hex format
	if !strings.HasPrefix(topic, "0x") {
		return fmt.Errorf("topic must start with '0x' prefix: %s", topic)
	}

	// Check that all characters after "0x" are valid hex
	hexPart := topic[2:]
	for _, c := range hexPart {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return fmt.Errorf("topic contains invalid hex character '%c': %s", c, topic)
		}
	}

	return nil
}

// runEventTriggerImmediately executes an event trigger immediately using the new queries-based system
func (n *Engine) runEventTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	if n.logger != nil {
		n.logger.Debug("🚀 TRACE: runEventTriggerImmediately CALLED", "configKeys", GetMapKeys(triggerConfig))
		n.logger.Info("🚀 runEventTriggerImmediately: Starting execution")
		n.logger.Debug("🚀 DEBUG: Function called with config", "configKeys", GetMapKeys(triggerConfig))
	}

	// Create a context with timeout to prevent hanging tests
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Create a temporary VM for template variable resolution
	// This allows eventTrigger to use {{settings.uniswapv3_pool.token0.id}} syntax in addresses
	tempVM := NewVM()
	tempVM.logger = n.logger
	if inputVariables != nil {
		for k, v := range inputVariables {
			tempVM.AddVar(k, v)
		}
	}

	// Parse the new queries-based configuration
	queriesInterface, ok := triggerConfig["queries"]
	if !ok {
		if n.logger != nil {
			n.logger.Info("🚀 runEventTriggerImmediately: No queries found, falling back to legacy mode")
		}
		return nil, NewMissingRequiredFieldError("queries")
	}

	queriesArray, ok := queriesInterface.([]interface{})
	if !ok || len(queriesArray) == 0 {
		return nil, NewInvalidNodeConfigError("queries must be a non-empty array")
	}

	// Resolve template variables in addresses and topics for all queries
	for queryIdx, queryInterface := range queriesArray {
		if queryMap, ok := queryInterface.(map[string]interface{}); ok {
			var queryAddresses []string

			// Resolve addresses
			addressesInterface, hasAddresses := queryMap["addresses"]
			if !hasAddresses {
				return nil, NewInvalidNodeConfigError("queries[].addresses must be a non-empty array")
			}
			if addressesArray, ok := addressesInterface.([]interface{}); ok {
				if len(addressesArray) == 0 {
					return nil, NewInvalidNodeConfigError("queries[].addresses must be a non-empty array")
				}
				resolvedAddresses := make([]interface{}, len(addressesArray))
				for i, addrInterface := range addressesArray {
					if addrStr, ok := addrInterface.(string); ok {
						// Resolve template variables like {{settings.uniswapv3_pool.token0.id}}
						resolvedAddr := tempVM.preprocessTextWithVariableMapping(addrStr)
						resolvedAddresses[i] = resolvedAddr
						queryAddresses = append(queryAddresses, resolvedAddr)

						if addrStr != resolvedAddr && n.logger != nil {
							n.logger.Info("EventTrigger: Resolved template in address",
								"original", addrStr,
								"resolved", resolvedAddr)
						}

						// Validate resolved address format
						if resolvedAddr == "" || !common.IsHexAddress(resolvedAddr) {
							return nil, fmt.Errorf("query[%d]: resolved address is not a valid Ethereum address: %s (original: %s)",
								queryIdx, resolvedAddr, addrStr)
						}
					} else {
						return nil, NewInvalidNodeConfigError("queries[].addresses must be a non-empty array of strings")
					}
				}
				queryMap["addresses"] = resolvedAddresses
			} else {
				return nil, NewInvalidNodeConfigError("queries[].addresses must be a non-empty array")
			}

			// Resolve topic values - flat array format
			if topicsInterface, exists := queryMap["topics"]; exists {
				if topicsArray, ok := topicsInterface.([]interface{}); ok {
					resolvedTopics := make([]interface{}, len(topicsArray))
					for i, topicInterface := range topicsArray {
						if topicStr, ok := topicInterface.(string); ok {
							// Resolve template variables in topic values
							resolvedValue := tempVM.preprocessTextWithVariableMapping(topicStr)
							resolvedTopics[i] = resolvedValue

							if topicStr != resolvedValue && n.logger != nil {
								n.logger.Info("EventTrigger: Resolved template in topic value",
									"original", topicStr,
									"resolved", resolvedValue)
							}

							// Validate resolved topic format
							if err := validateTopicHexFormat(resolvedValue); err != nil {
								if n.logger != nil {
									n.logger.Error("❌ EventTrigger: Invalid topic format after template resolution",
										"queryIndex", queryIdx,
										"topicIndex", i,
										"original", topicStr,
										"resolved", resolvedValue,
										"error", err.Error())
								}
								return nil, fmt.Errorf("query[%d].topics[%d]: %w (original: %s, resolved: %s)",
									queryIdx, i, err, topicStr, resolvedValue)
							}
						} else {
							// Keep non-string values (null) as-is
							resolvedTopics[i] = topicInterface
						}
					}
					queryMap["topics"] = resolvedTopics
				}
			}
		}
	}

	// Check if simulation mode is enabled (default: true, provides sample data for development)
	simulationMode := true
	if simModeInterface, exists := triggerConfig["simulationMode"]; exists {
		if simModeBool, ok := simModeInterface.(bool); ok {
			simulationMode = simModeBool
		}
	}

	if n.logger != nil {
		n.logger.Info("EventTrigger: Processing queries-based EventTrigger",
			"queriesCount", len(queriesArray),
			"simulationMode", simulationMode)
	}

	// 🔮 SMART DETECTION: Determine if this should use direct calls or simulation
	if simulationMode {
		// Check if this is a direct method call scenario (oracle reading)
		shouldUseDirect := n.shouldUseDirectCalls(queriesArray)

		if n.logger != nil {
			n.logger.Debug("🔍 TRACE: Path decision",
				"simulationMode", simulationMode,
				"shouldUseDirect", shouldUseDirect,
				"queriesCount", len(queriesArray))
		}

		if shouldUseDirect {
			if n.logger != nil {
				n.logger.Debug("🔍 EventTrigger: Using DIRECT CALLS path")
			}
			return n.runEventTriggerWithDirectCalls(ctx, queriesArray, inputVariables)
		} else {
			if n.logger != nil {
				n.logger.Debug("🔍 EventTrigger: Attempting TENDERLY SIMULATION path")
			}
			// Try Tenderly simulation first, but fallback to historical search if not supported
			result, err := n.runEventTriggerWithTenderlySimulation(ctx, queriesArray, inputVariables)
			if err != nil {
				// Check if this is a "not supported" error that should fallback to historical search
				if strings.Contains(err.Error(), "simulation not yet supported") {
					if n.logger != nil {
						n.logger.Info("⚠️ EventTrigger: Tenderly simulation not supported, falling back to historical search",
							"error", err.Error())
					}
					// Fallback to historical search with timeout check
					if rpcConn == nil {
						if n.logger != nil {
							n.logger.Warn("⚠️ EventTrigger: RPC connection not available for historical search fallback, returning simulation error")
						}
						// Return the original simulation error since RPC is not available
						return nil, fmt.Errorf("tenderly simulation not supported and RPC connection not available: %w", err)
					}
					// Create a timeout context for historical search (30 seconds max)
					historicalCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					defer cancel()

					historicalResult, histErr := n.runEventTriggerWithHistoricalSearch(historicalCtx, queriesArray, inputVariables)
					if histErr != nil {
						if n.logger != nil {
							n.logger.Warn("⚠️ EventTrigger: Historical search failed after simulation failure",
								"simulationError", err.Error(),
								"historicalError", histErr.Error())
						}
						// Return the simulation error as the primary error (more informative)
						return nil, fmt.Errorf("tenderly simulation failed: %w (historical search also failed: %v)", err, histErr)
					}
					return historicalResult, nil
				}
				// If addresses are missing for simulation, reject as invalid configuration (too broad)
				if strings.Contains(err.Error(), "no contract addresses provided") {
					return nil, NewInvalidNodeConfigError("queries[].addresses must be a non-empty array")
				}
				// For other errors, return them as-is
				return nil, err
			}
			return result, nil
		}
	}

	// 📊 HISTORICAL SEARCH MODE (use simulationMode: false for production)
	// Ensure RPC connection is available for historical on-chain queries
	if rpcConn == nil {
		return nil, fmt.Errorf("RPC connection not available for EventTrigger historical search")
	}
	return n.runEventTriggerWithHistoricalSearch(ctx, queriesArray, inputVariables)
}

// shouldUseDirectCalls determines if the query should use direct contract calls vs simulation
func (n *Engine) shouldUseDirectCalls(queriesArray []interface{}) bool {
	if n.logger != nil {
		n.logger.Info("🔍 shouldUseDirectCalls: Analyzing queries", "queryCount", len(queriesArray))
	}

	for i, queryInterface := range queriesArray {
		queryMap, ok := queryInterface.(map[string]interface{})
		if !ok {
			if n.logger != nil {
				n.logger.Info("🔍 shouldUseDirectCalls: Query not a map", "queryIndex", i)
			}
			continue
		}

		// Check if query has topics (indicates event-based query)
		if topicsInterface, exists := queryMap["topics"]; exists {
			if topicsArray, ok := topicsInterface.([]interface{}); ok && len(topicsArray) > 0 {
				// Has topics - this is event-based, use simulation
				if n.logger != nil {
					n.logger.Debug("🔍 TRACE: Found non-empty topics, using SIMULATION", "queryIndex", i, "topicsCount", len(topicsArray))
				}
				return false
			} else {
				if n.logger != nil {
					n.logger.Debug("🔍 TRACE: Found empty topics array", "queryIndex", i, "topicsExists", exists, "topicsType", fmt.Sprintf("%T", topicsInterface))
				}
			}
		} else {
			if n.logger != nil {
				n.logger.Debug("🔍 TRACE: No topics found", "queryIndex", i)
			}
		}

		// Check if query has methodCalls (indicates direct contract calls)
		if methodCallsInterface, exists := queryMap["methodCalls"]; exists {

			// Handle both []interface{} and []map[string]interface{} types
			var methodCallsCount int
			if methodCallsArray, ok := methodCallsInterface.([]interface{}); ok {
				methodCallsCount = len(methodCallsArray)
			} else if methodCallsMapArray, ok := methodCallsInterface.([]map[string]interface{}); ok {
				methodCallsCount = len(methodCallsMapArray)
			} else {
				methodCallsCount = 0
			}

			if methodCallsCount > 0 {
				// Has methodCalls but no topics - this is direct call scenario
				if n.logger != nil {
					n.logger.Debug("🔍 TRACE: Found methodCalls without topics, using DIRECT CALLS", "queryIndex", i, "methodCallsCount", methodCallsCount)
				}
				return true
			} else {
				if n.logger != nil {
					n.logger.Debug("🔍 TRACE: Found empty methodCalls", "queryIndex", i)
				}
			}
		}

		if n.logger != nil {
			n.logger.Info("🔍 shouldUseDirectCalls: Query has neither topics nor methodCalls", "queryIndex", i)
		}
	}

	// Default to simulation if unclear
	if n.logger != nil {
		n.logger.Info("🔍 shouldUseDirectCalls: No clear signal, defaulting to simulation")
	}
	return false
}

// runEventTriggerWithDirectCalls executes eventTrigger by creating simulated AnswerUpdated events for oracle reading
// This ensures consistency with deployed tasks and simulate workflow by returning event data instead of method call results
func (n *Engine) runEventTriggerWithDirectCalls(ctx context.Context, queriesArray []interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	if n.logger != nil {
		n.logger.Debug("🔍 TRACE: DIRECT CALLS path executing")
		n.logger.Info("🎯 EventTrigger: Creating simulated AnswerUpdated events for consistency",
			"queriesCount", len(queriesArray))
	}

	// Process the first query (for now, handle single query)
	if len(queriesArray) == 0 {
		return nil, fmt.Errorf("no queries provided for simulated events")
	}

	queryMap, ok := queriesArray[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid query format")
	}

	// Extract contract address
	addressesInterface, exists := queryMap["addresses"]
	if !exists {
		return nil, NewMissingRequiredFieldError("addresses")
	}

	addressesArray, ok := addressesInterface.([]interface{})
	if !ok || len(addressesArray) == 0 {
		return nil, NewInvalidNodeConfigError("invalid addresses format")
	}

	contractAddressStr, ok := addressesArray[0].(string)
	if !ok {
		return nil, NewInvalidAddressError("invalid contract address format")
	}

	// Get chain ID for response context
	var chainID int64 = 11155111 // Default to Sepolia
	if n.tokenEnrichmentService != nil {
		chainID = int64(n.tokenEnrichmentService.GetChainID())
	}

	// Create simulated AnswerUpdated event using the same logic as TenderlyClient
	simulatedLog := n.createSimulatedAnswerUpdatedLog(contractAddressStr, chainID)

	// Parse contract ABI for enrichment (if available)
	var contractABI []interface{}
	if contractAbiInterface, exists := queryMap["contractAbi"]; exists {
		if abiArray, ok := contractAbiInterface.([]interface{}); ok {
			// Validate total ABI size with short-circuit optimization
			totalABISize := 0
			for i, abiItem := range abiArray {
				var itemSize int
				if abiStr, ok := abiItem.(string); ok {
					itemSize = len(abiStr)
					// Validate individual ABI item size
					if itemSize > MaxEventTriggerABIItemSize {
						return nil, NewStructuredError(
							avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
							fmt.Sprintf("%s at index %d: %d bytes (max: %d bytes)", ValidationErrorMessages.EventTriggerABIItemTooLarge, i, itemSize, MaxEventTriggerABIItemSize),
							map[string]interface{}{
								"field":   "contractAbi",
								"issue":   "ABI item size limit exceeded",
								"index":   i,
								"size":    itemSize,
								"maxSize": MaxEventTriggerABIItemSize,
							},
						)
					}
				} else if abiMap, ok := abiItem.(map[string]interface{}); ok {
					// Estimate size for map by marshaling to JSON
					if jsonBytes, err := json.Marshal(abiMap); err == nil {
						itemSize = len(jsonBytes)
					}
				}
				totalABISize += itemSize

				// Short-circuit: stop processing once total exceeds limit
				if totalABISize > MaxContractABISize {
					return nil, NewStructuredError(
						avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
						fmt.Sprintf("%s: %d bytes (max: %d bytes)", ValidationErrorMessages.ContractABITooLarge, totalABISize, MaxContractABISize),
						map[string]interface{}{
							"field":   "contractAbi",
							"issue":   "total ABI size limit exceeded",
							"size":    totalABISize,
							"maxSize": MaxContractABISize,
							"items":   len(abiArray),
						},
					)
				}
			}

			contractABI = abiArray
		}
	}

	// Use the shared enrichment function to process the simulated log
	enrichmentParams := SharedEventEnrichmentParams{
		EventLog:               simulatedLog,
		ContractABI:            nil, // Will be converted from contractABI below
		TokenEnrichmentService: n.tokenEnrichmentService,
		RpcClient:              rpcConn, // Use global RPC connection
		Logger:                 n.logger,
		ChainID:                chainID,
	}

	// Convert contractABI to protobuf Values for enrichment
	if len(contractABI) > 0 {
		abiValues := make([]*structpb.Value, len(contractABI))
		for i, abiItem := range contractABI {
			if abiStr, ok := abiItem.(string); ok {
				// ABI item is a JSON string, parse it to map first
				var abiMap map[string]interface{}
				if err := json.Unmarshal([]byte(abiStr), &abiMap); err == nil {
					if val, err := structpb.NewValue(abiMap); err == nil {
						abiValues[i] = val
					}
				}
			} else if abiMap, ok := abiItem.(map[string]interface{}); ok {
				// ABI item is already a map, convert directly to protobuf Value
				if val, err := structpb.NewValue(abiMap); err == nil {
					abiValues[i] = val
				}
			}
		}
		enrichmentParams.ContractABI = abiValues
	}

	// Enrich the simulated event data
	enrichmentResult, err := EnrichEventWithTokenMetadata(enrichmentParams)
	if err != nil {
		n.logger.Warn("Failed to enrich simulated event data, using basic metadata",
			"error", err)
		// Create basic event response without enrichment
		return n.createBasicSimulatedEventResponse(simulatedLog, chainID), nil
	}

	// Process method calls to extract decimal formatting information
	var formattingContext *DecimalFormattingContext
	if methodCallsInterface, hasMethodCalls := queryMap["methodCalls"]; hasMethodCalls {
		if methodCallsArray, ok := methodCallsInterface.([]interface{}); ok {
			// Look for decimals method with apply_to_fields
			for _, methodCallInterface := range methodCallsArray {
				if methodCallMap, ok := methodCallInterface.(map[string]interface{}); ok {
					if methodName, ok := methodCallMap["methodName"].(string); ok && methodName == "decimals" {
						// Extract applyToFields
						if applyToFieldsInterface, hasApplyToFields := methodCallMap["applyToFields"]; hasApplyToFields {
							if applyToFieldsArray, ok := applyToFieldsInterface.([]interface{}); ok {
								fieldsToFormat := make([]string, 0, len(applyToFieldsArray))
								for _, fieldInterface := range applyToFieldsArray {
									if fieldStr, ok := fieldInterface.(string); ok {
										fieldsToFormat = append(fieldsToFormat, fieldStr)
									}
								}

								if len(fieldsToFormat) > 0 {
									// Simulate decimals() call - for ETH/USD price feeds, typically 8 decimals
									decimalsValue := big.NewInt(8)
									formattingContext = NewDecimalFormattingContext(decimalsValue, fieldsToFormat, "decimals")

									if n.logger != nil {
										n.logger.Info("✅ Created decimal formatting context for direct calls",
											"decimalsValue", decimalsValue.String(),
											"fieldsToFormat", fieldsToFormat)
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Apply decimal formatting to enriched event data if needed
	parsedData := enrichmentResult.ParsedData
	if formattingContext != nil {
		// Apply decimal formatting to each event in the parsed data
		for eventName, eventFields := range parsedData {
			if eventFieldsMap, ok := eventFields.(map[string]interface{}); ok {
				// Apply formatting using shared utility
				formattingContext.ApplyDecimalFormattingToEventData(eventFieldsMap, eventName, n.logger)
			}
		}
	}

	// Create response with enriched event data (same format as deployed tasks and simulate workflow)
	response := make(map[string]interface{})

	// Add the parsed ABI fields as flattened data (like Transfer event format)
	response["data"] = parsedData

	// Add raw event log fields as metadata (no execution context in metadata)
	response["metadata"] = enrichmentResult.RawEventData

	// Evaluate conditions if present
	conditionsMet := true
	errorMessage := ""
	if conditionsInterface, hasConditions := queryMap["conditions"]; hasConditions {
		// Handle both []interface{} and []map[string]interface{} types
		var conditionsArray []interface{}
		if conditionsInterfaceArray, ok := conditionsInterface.([]interface{}); ok {
			conditionsArray = conditionsInterfaceArray
		} else if conditionsMapArray, ok := conditionsInterface.([]map[string]interface{}); ok {
			// Convert []map[string]interface{} to []interface{}
			conditionsArray = make([]interface{}, len(conditionsMapArray))
			for i, condMap := range conditionsMapArray {
				conditionsArray[i] = condMap
			}
		}

		if len(conditionsArray) > 0 {
			// Use decimal formatting context for consistent condition evaluation
			if formattingContext != nil {
				conditionsMet = n.evaluateConditionsAgainstEventDataWithDecimalContext(parsedData, conditionsArray, formattingContext)
			} else {
				conditionsMet = n.evaluateConditionsAgainstEventData(parsedData, conditionsArray)
			}
			if !conditionsMet {
				errorMessage = "Conditions not met for simulated event"
			}
		}
	}

	// Set success based on condition evaluation
	response["success"] = conditionsMet
	response["error"] = errorMessage

	// Add execution context (chainId, isSimulated, provider)
	response["executionContext"] = GetExecutionContext(chainID, true) // true = isSimulation

	if conditionsMet && n.logger != nil {
		n.logger.Info("✅ Simulated AnswerUpdated event created successfully",
			"contract", contractAddressStr,
			"chainId", chainID,
			"eventName", enrichmentResult.EventName,
			"hasEnrichedData", enrichmentResult.ParsedData != nil)
	}

	return response, nil
}

// Simulation constants for AnswerUpdated events
const (
	// DefaultSimulatedETHPrice represents $2500 with 8 decimals (ETH/USD)
	DefaultSimulatedETHPrice = 250000000000
	// DefaultSimulatedRoundID represents a realistic round ID for Chainlink price feeds
	DefaultSimulatedRoundID = 92233720368547758
)

// createSimulatedAnswerUpdatedLog creates a simulated Chainlink AnswerUpdated event log for consistency
func (n *Engine) createSimulatedAnswerUpdatedLog(contractAddress string, chainID int64) *types.Log {
	// Use realistic mock data for simulation
	currentPrice := big.NewInt(DefaultSimulatedETHPrice)
	roundId := big.NewInt(DefaultSimulatedRoundID)
	updatedAt := big.NewInt(time.Now().Unix())

	// AnswerUpdated event signature: AnswerUpdated(int256 current, uint256 roundId, uint256 updatedAt)
	eventSignature := common.HexToHash("0x0559884fd3a460db3073b7fc896cc77986f16e378210ded43186175bf646fc5f")

	// Convert price to 32-byte hash (indexed parameter)
	priceHash := common.BytesToHash(common.LeftPadBytes(currentPrice.Bytes(), 32))

	// Convert roundId to 32-byte hash (indexed parameter)
	roundIdHash := common.BytesToHash(common.LeftPadBytes(roundId.Bytes(), 32))

	// updatedAt is non-indexed, so it goes in the data field
	updatedAtBytes := common.LeftPadBytes(updatedAt.Bytes(), 32)

	// Create a realistic transaction hash
	txHash := common.HexToHash(fmt.Sprintf("0x%064x", time.Now().UnixNano()))

	// Use realistic block number based on chain ID
	blockNumber := getRealisticBlockNumberForChain(chainID)

	return &types.Log{
		Address: common.HexToAddress(contractAddress),
		Topics: []common.Hash{
			eventSignature, // Event signature
			priceHash,      // current (indexed)
			roundIdHash,    // roundId (indexed)
		},
		Data:        updatedAtBytes, // updatedAt (non-indexed)
		BlockNumber: blockNumber,
		TxHash:      txHash,
		Index:       0,
		TxIndex:     0,
		BlockHash:   common.HexToHash(fmt.Sprintf("0x%064x", time.Now().UnixNano()+1)),
		Removed:     false,
	}
}

// createBasicSimulatedEventResponse creates a basic event response when enrichment fails
func (n *Engine) createBasicSimulatedEventResponse(log *types.Log, chainID int64) map[string]interface{} {
	response := make(map[string]interface{})

	// Create basic parsed data (without ABI enrichment, just basic fields)
	parsedData := map[string]interface{}{
		"eventName": "AnswerUpdated",
		// Add basic unparsed event data when ABI parsing fails
		"raw_data":   "0x" + common.Bytes2Hex(log.Data),
		"raw_topics": make([]string, len(log.Topics)),
	}

	// Convert topics to string array
	for i, topic := range log.Topics {
		parsedData["raw_topics"].([]string)[i] = topic.Hex()
	}

	// Create raw event log metadata (no execution context in metadata)
	topics := make([]string, len(log.Topics))
	for i, topic := range log.Topics {
		topics[i] = topic.Hex()
	}

	metadata := map[string]interface{}{
		"address":          log.Address.Hex(),
		"topics":           topics,
		"data":             "0x" + common.Bytes2Hex(log.Data),
		"blockNumber":      log.BlockNumber,
		"transactionHash":  log.TxHash.Hex(),
		"transactionIndex": log.TxIndex,
		"blockHash":        log.BlockHash.Hex(),
		"logIndex":         log.Index,
		"removed":          log.Removed,
		"chainId":          chainID,
	}

	response["data"] = parsedData
	response["metadata"] = metadata
	response["success"] = true
	response["error"] = ""

	// Add execution context (chainId, isSimulated, provider)
	response["executionContext"] = GetExecutionContext(chainID, true) // true = isSimulation

	return response
}

// buildEventTriggerResponseWithSimulation builds an event trigger response with optional simulation flag
func (n *Engine) buildEventTriggerResponseWithSimulation(methodCallData map[string]interface{}, allConditionsMet bool, chainID int64, rawContractMetadata []interface{}, queryMap map[string]interface{}, isSimulation bool) map[string]interface{} {
	response := make(map[string]interface{})

	// Always include the contract read data in the data field
	response["data"] = methodCallData

	// Set metadata as array of method objects without the value field
	if len(rawContractMetadata) > 0 {
		// Remove only the "value" field from each metadata entry and add simulation flag
		cleanedMetadata := make([]interface{}, len(rawContractMetadata))
		for i, metadataEntry := range rawContractMetadata {
			if entryMap, ok := metadataEntry.(map[string]interface{}); ok {
				// Create a copy without the "value" field
				cleanEntry := make(map[string]interface{})
				for k, v := range entryMap {
					if k != "value" {
						cleanEntry[k] = v
					}
				}
				// Add simulation flag to metadata
				if isSimulation {
					cleanEntry["isSimulation"] = true
				}
				cleanedMetadata[i] = cleanEntry
			}
		}
		response["metadata"] = cleanedMetadata
	} else {
		// Fallback to empty array if no raw metadata available
		metadata := []interface{}{}
		if isSimulation {
			metadata = append(metadata, map[string]interface{}{
				"isSimulation": true,
				"source":       "simulation",
			})
		}
		response["metadata"] = metadata
	}

	if allConditionsMet {
		// Success case: conditions met
		response["success"] = true
		response["error"] = ""
	} else {
		// Failure case: conditions not met - need to evaluate conditions to get detailed error
		response["success"] = false

		// Get the actual condition evaluation to build a detailed error message
		conditionResults, _ := n.evaluateConditionsWithDetails(methodCallData, queryMap)

		// Build detailed error message
		var failedReasons []string
		for _, condition := range conditionResults {
			if !condition.Passed {
				failedReasons = append(failedReasons, condition.Reason)
			}
		}

		if len(failedReasons) > 0 {
			// Only include failed reasons in error message to avoid leaking sensitive data
			response["error"] = fmt.Sprintf("Conditions not met: %s",
				strings.Join(failedReasons, "; "))
		} else {
			response["error"] = "Conditions not met"
		}
	}

	// Add executionContext with simulation flag
	response["executionContext"] = GetExecutionContext(chainID, isSimulation)

	return response
}

// ConditionResult represents the result of evaluating a single condition
type ConditionResult struct {
	FieldName     string      `json:"fieldName"`
	Operator      string      `json:"operator"`
	ExpectedValue string      `json:"expectedValue"`
	ActualValue   interface{} `json:"actualValue"`
	Passed        bool        `json:"passed"`
	Reason        string      `json:"reason,omitempty"`
}

// evaluateConditionsWithDetailsAndDecimalContext evaluates conditions with decimal formatting context
func (n *Engine) evaluateConditionsWithDetailsAndDecimalContext(data map[string]interface{}, queryMap map[string]interface{}, formattingContext *DecimalFormattingContext) ([]ConditionResult, bool) {
	conditionsInterface, exists := queryMap["conditions"]
	if !exists {
		// No conditions to evaluate - all conditions met by default
		return []ConditionResult{}, true
	}

	// Handle both []interface{} and []map[string]interface{} types
	var conditionsArray []interface{}
	if directArray, ok := conditionsInterface.([]interface{}); ok {
		conditionsArray = directArray
	} else if mapArray, ok := conditionsInterface.([]map[string]interface{}); ok {
		// Convert []map[string]interface{} to []interface{}
		conditionsArray = make([]interface{}, len(mapArray))
		for i, condMap := range mapArray {
			conditionsArray[i] = condMap
		}
	} else {
		// Unsupported type - no valid conditions
		return []ConditionResult{}, true
	}

	if len(conditionsArray) == 0 {
		// No conditions to evaluate - all conditions met by default
		return []ConditionResult{}, true
	}

	results := make([]ConditionResult, len(conditionsArray))
	allConditionsMet := true

	for i, conditionInterface := range conditionsArray {
		conditionMap, ok := conditionInterface.(map[string]interface{})
		if !ok {
			results[i] = ConditionResult{
				FieldName: "unknown",
				Passed:    false,
				Reason:    "Invalid condition format",
			}
			allConditionsMet = false
			continue
		}

		fieldName, _ := conditionMap["fieldName"].(string)
		operator, _ := conditionMap["operator"].(string)
		expectedValue, _ := conditionMap["value"].(string)
		fieldType, _ := conditionMap["fieldType"].(string)

		// Get actual value from data - support nested field access
		actualValue, exists := n.getNestedFieldValue(data, fieldName)
		if !exists {
			results[i] = ConditionResult{
				FieldName:     fieldName,
				Operator:      operator,
				ExpectedValue: expectedValue,
				ActualValue:   nil,
				Passed:        false,
				Reason:        fmt.Sprintf("Field '%s' not found in method call results", fieldName),
			}
			allConditionsMet = false
			continue
		}

		// FIXED: Handle decimal formatting context for consistent comparison
		// When fieldType is "decimal" and we have a formatting context, ensure consistent comparison
		var processedExpectedValue string = expectedValue
		if fieldType == "decimal" && formattingContext != nil {
			// Format the expected value to match the actual value's formatting
			processedExpectedValue = formattingContext.FormatConditionValueForComparison(fieldName, expectedValue)
			if n.logger != nil {
				n.logger.Info("✅ DECIMAL CONDITION FIX: Formatted condition value for consistent comparison",
					"fieldName", fieldName,
					"originalExpectedValue", expectedValue,
					"formattedExpectedValue", processedExpectedValue,
					"actualValue", actualValue,
					"fieldType", fieldType)
			}
		}

		// Evaluate the condition using processed expected value
		passed := n.evaluateCondition(actualValue, operator, processedExpectedValue, fieldType)
		reason := n.buildConditionReason(actualValue, operator, processedExpectedValue, passed)

		// Convert actualValue to display format for JSON serialization
		displayActualValue := n.formatValueForDisplay(actualValue)

		results[i] = ConditionResult{
			FieldName:     fieldName,
			Operator:      operator,
			ExpectedValue: expectedValue, // Show original expected value in result
			ActualValue:   displayActualValue,
			Passed:        passed,
			Reason:        reason,
		}

		if !passed {
			allConditionsMet = false
		}
	}

	return results, allConditionsMet
}

// evaluateConditionsWithDetails evaluates conditions and returns detailed results
func (n *Engine) evaluateConditionsWithDetails(data map[string]interface{}, queryMap map[string]interface{}) ([]ConditionResult, bool) {
	conditionsInterface, exists := queryMap["conditions"]
	if !exists {
		// No conditions to evaluate - all conditions met by default
		return []ConditionResult{}, true
	}

	// Handle both []interface{} and []map[string]interface{} types
	var conditionsArray []interface{}
	if directArray, ok := conditionsInterface.([]interface{}); ok {
		conditionsArray = directArray
	} else if mapArray, ok := conditionsInterface.([]map[string]interface{}); ok {
		// Convert []map[string]interface{} to []interface{}
		conditionsArray = make([]interface{}, len(mapArray))
		for i, condMap := range mapArray {
			conditionsArray[i] = condMap
		}
	} else {
		// Unsupported type - no valid conditions
		return []ConditionResult{}, true
	}

	if len(conditionsArray) == 0 {
		// No conditions to evaluate - all conditions met by default
		return []ConditionResult{}, true
	}

	results := make([]ConditionResult, len(conditionsArray))
	allConditionsMet := true

	for i, conditionInterface := range conditionsArray {
		conditionMap, ok := conditionInterface.(map[string]interface{})
		if !ok {
			results[i] = ConditionResult{
				FieldName: "unknown",
				Passed:    false,
				Reason:    "Invalid condition format",
			}
			allConditionsMet = false
			continue
		}

		fieldName, _ := conditionMap["fieldName"].(string)
		operator, _ := conditionMap["operator"].(string)
		expectedValue, _ := conditionMap["value"].(string)
		fieldType, _ := conditionMap["fieldType"].(string)

		// Get actual value from data - support nested field access
		actualValue, exists := n.getNestedFieldValue(data, fieldName)
		if !exists {
			results[i] = ConditionResult{
				FieldName:     fieldName,
				Operator:      operator,
				ExpectedValue: expectedValue,
				ActualValue:   nil,
				Passed:        false,
				Reason:        fmt.Sprintf("Field '%s' not found in method call results", fieldName),
			}
			allConditionsMet = false
			continue
		}

		// Evaluate the condition
		passed := n.evaluateCondition(actualValue, operator, expectedValue, fieldType)
		reason := n.buildConditionReason(actualValue, operator, expectedValue, passed)

		// Convert actualValue to display format for JSON serialization
		displayActualValue := n.formatValueForDisplay(actualValue)

		results[i] = ConditionResult{
			FieldName:     fieldName,
			Operator:      operator,
			ExpectedValue: expectedValue,
			ActualValue:   displayActualValue, // Use formatted display value
			Passed:        passed,
			Reason:        reason,
		}

		if !passed {
			allConditionsMet = false
		}
	}

	return results, allConditionsMet
}

// getNestedFieldValue retrieves a value from nested data structure
// Supports both direct field access ("answer") and nested access ("latestRoundData.answer")
func (n *Engine) getNestedFieldValue(data map[string]interface{}, fieldName string) (interface{}, bool) {
	// Handle dot notation for nested field access
	parts := strings.Split(fieldName, ".")

	if len(parts) == 1 {
		// Direct field access
		value, exists := data[fieldName]
		return value, exists
	}

	// For eventName.fieldName format (e.g., "AnswerUpdated.current"), try just the field name
	if len(parts) == 2 {
		fieldNameOnly := parts[1]
		if value, exists := data[fieldNameOnly]; exists {
			return value, true
		}
	}

	// Nested field access
	current := data
	for i, part := range parts {
		if current == nil {
			return nil, false
		}

		value, exists := current[part]
		if !exists {
			return nil, false
		}

		// If this is the last part, return the value
		if i == len(parts)-1 {
			return value, true
		}

		// Otherwise, continue traversing - value must be a map
		if nextMap, ok := value.(map[string]interface{}); ok {
			current = nextMap
		} else {
			return nil, false
		}
	}

	return nil, false
}

// evaluateCondition evaluates a single condition
func (n *Engine) evaluateCondition(actualValue interface{}, operator, expectedValue, fieldType string) bool {
	switch operator {
	case "lt", "less_than":
		return n.evaluateLessThan(actualValue, expectedValue, fieldType)
	case "gt", "greater_than":
		return n.evaluateGreaterThan(actualValue, expectedValue, fieldType)
	case "eq", "equals":
		return n.evaluateEquals(actualValue, expectedValue, fieldType)
	case "ne", "not_equals":
		return !n.evaluateEquals(actualValue, expectedValue, fieldType)
	case "lte", "less_than_or_equal":
		return n.evaluateLessThan(actualValue, expectedValue, fieldType) || n.evaluateEquals(actualValue, expectedValue, fieldType)
	case "gte", "greater_than_or_equal":
		return n.evaluateGreaterThan(actualValue, expectedValue, fieldType) || n.evaluateEquals(actualValue, expectedValue, fieldType)
	default:
		n.logger.Warn("🚫 Unsupported condition operator", "operator", operator)
		return false
	}
}

// evaluateLessThan evaluates less than comparison
func (n *Engine) evaluateLessThan(actualValue interface{}, expectedValue, fieldType string) bool {
	switch fieldType {
	case "decimal", "int256", "uint256", "uint128", "uint64", "uint32", "uint16", "uint8", "int128", "int64", "int32", "int16", "int8":
		actualFloat, err1 := n.convertToFloat(actualValue)
		expectedFloat, err2 := n.convertToFloat(expectedValue)
		if err1 != nil || err2 != nil {
			return false
		}
		return actualFloat < expectedFloat
	default:
		return false
	}
}

// evaluateGreaterThan evaluates greater than comparison
func (n *Engine) evaluateGreaterThan(actualValue interface{}, expectedValue, fieldType string) bool {
	switch fieldType {
	case "decimal", "int256", "uint256", "uint128", "uint64", "uint32", "uint16", "uint8", "int128", "int64", "int32", "int16", "int8":
		actualFloat, err1 := n.convertToFloat(actualValue)
		expectedFloat, err2 := n.convertToFloat(expectedValue)
		if err1 != nil || err2 != nil {
			return false
		}
		return actualFloat > expectedFloat
	default:
		return false
	}
}

// evaluateEquals evaluates equality comparison
func (n *Engine) evaluateEquals(actualValue interface{}, expectedValue, fieldType string) bool {
	switch fieldType {
	case "decimal", "int256", "uint256", "uint128", "uint64", "uint32", "uint16", "uint8", "int128", "int64", "int32", "int16", "int8":
		actualFloat, err1 := n.convertToFloat(actualValue)
		expectedFloat, err2 := n.convertToFloat(expectedValue)
		if err1 != nil || err2 != nil {
			return false
		}
		return actualFloat == expectedFloat
	case "string", "address":
		actualStr := fmt.Sprintf("%v", actualValue)
		return actualStr == expectedValue
	case "bool":
		actualBool, ok := actualValue.(bool)
		if !ok {
			return false
		}
		expectedBool := expectedValue == "true"
		return actualBool == expectedBool
	default:
		return fmt.Sprintf("%v", actualValue) == expectedValue
	}
}

// convertToFloat converts various types to float64 for numeric comparison
func (n *Engine) convertToFloat(value interface{}) (float64, error) {
	switch v := value.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case string:
		return strconv.ParseFloat(v, 64)
	default:
		// Try to convert to string then parse
		str := fmt.Sprintf("%v", value)
		return strconv.ParseFloat(str, 64)
	}
}

// buildConditionReason builds a human-readable reason for condition result
func (n *Engine) buildConditionReason(actualValue interface{}, operator, expectedValue string, passed bool) string {
	// Convert hex values to human-readable decimal format for display
	displayValue := n.formatValueForDisplay(actualValue)

	if passed {
		switch operator {
		case "lt", "less_than":
			return fmt.Sprintf("Value %s is less than %s", displayValue, expectedValue)
		case "gt", "greater_than":
			return fmt.Sprintf("Value %s is greater than %s", displayValue, expectedValue)
		case "eq", "equals":
			return fmt.Sprintf("Value %s equals %s", displayValue, expectedValue)
		case "ne", "not_equals":
			return fmt.Sprintf("Value %s does not equal %s", displayValue, expectedValue)
		case "lte", "less_than_or_equal":
			return fmt.Sprintf("Value %s is less than or equal to %s", displayValue, expectedValue)
		case "gte", "greater_than_or_equal":
			return fmt.Sprintf("Value %s is greater than or equal to %s", displayValue, expectedValue)
		default:
			return "Condition passed"
		}
	} else {
		switch operator {
		case "lt", "less_than":
			return fmt.Sprintf("Value %s is not less than %s", displayValue, expectedValue)
		case "gt", "greater_than":
			return fmt.Sprintf("Value %s is not greater than %s", displayValue, expectedValue)
		case "eq", "equals":
			return fmt.Sprintf("Value %s does not equal %s", displayValue, expectedValue)
		case "ne", "not_equals":
			return fmt.Sprintf("Value %s equals %s (expected not equal)", displayValue, expectedValue)
		case "lte", "less_than_or_equal":
			return fmt.Sprintf("Value %s is not less than or equal to %s", displayValue, expectedValue)
		case "gte", "greater_than_or_equal":
			return fmt.Sprintf("Value %s is not greater than or equal to %s", displayValue, expectedValue)
		default:
			return "Condition failed"
		}
	}
}

// formatValueForDisplay converts hex values to human-readable decimal format
func (n *Engine) formatValueForDisplay(value interface{}) string {
	switch v := value.(type) {
	case string:
		// Check if it's a hex string
		if strings.HasPrefix(v, "0x") && len(v) > 2 {
			// Try to parse as big int
			if bigInt, success := new(big.Int).SetString(v, 0); success {
				return bigInt.String() // Return decimal representation
			}
		}
		return v
	case *big.Int:
		return v.String()
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%d", v)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v)
	case float32, float64:
		return fmt.Sprintf("%.2f", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// buildEventTriggerResponse builds the enhanced response structure
func (n *Engine) buildEventTriggerResponse(methodCallData map[string]interface{}, allConditionsMet bool, chainID int64, rawContractMetadata []interface{}, queryMap map[string]interface{}) map[string]interface{} {
	response := make(map[string]interface{})

	// Always include the contract read data in the data field
	response["data"] = methodCallData

	// Set metadata as array of method objects without the value field
	if len(rawContractMetadata) > 0 {
		// Remove only the "value" field from each metadata entry
		cleanedMetadata := make([]interface{}, len(rawContractMetadata))
		for i, metadataEntry := range rawContractMetadata {
			if entryMap, ok := metadataEntry.(map[string]interface{}); ok {
				// Create a copy without the "value" field
				cleanEntry := make(map[string]interface{})
				for k, v := range entryMap {
					if k != "value" {
						cleanEntry[k] = v
					}
				}
				cleanedMetadata[i] = cleanEntry
			}
		}
		response["metadata"] = cleanedMetadata
	} else {
		// Fallback to empty array if no raw metadata available
		response["metadata"] = []interface{}{}
	}

	if allConditionsMet {
		// Success case: conditions met
		response["success"] = true
		response["error"] = ""
	} else {
		// Failure case: conditions not met - need to evaluate conditions to get detailed error
		response["success"] = false

		// Get the actual condition evaluation to build a detailed error message
		conditionResults, _ := n.evaluateConditionsWithDetails(methodCallData, queryMap)

		// Build detailed error message
		var failedReasons []string
		for _, condition := range conditionResults {
			if !condition.Passed {
				failedReasons = append(failedReasons, condition.Reason)
			}
		}

		if len(failedReasons) > 0 {
			// Only include failed reasons in error message to avoid leaking sensitive data
			response["error"] = fmt.Sprintf("Conditions not met: %s",
				strings.Join(failedReasons, "; "))
		} else {
			response["error"] = "Conditions not met"
		}
	}

	// Add executionContext for EventTrigger direct calls (real RPC calls, not simulated)
	response["executionContext"] = GetExecutionContext(chainID, false)

	// Add debug trace info
	debugResponse := map[string]interface{}{
		"debug_trace":     "runEventTriggerWithDirectCalls_COMPLETED",
		"debug_timestamp": time.Now().Unix(),
	}
	for key, value := range debugResponse {
		response[key] = value
	}
	response["debug_path"] = "DIRECT_CALLS"

	return response
}

// executeMethodCallForSimulation executes a single method call for simulation path
func (n *Engine) executeMethodCallForSimulation(ctx context.Context, methodCall *avsproto.EventTrigger_MethodCall, queryMap map[string]interface{}, chainID int64) (map[string]interface{}, error) {
	// Extract contract address and ABI from queryMap
	contractAddressInterface, exists := queryMap["addresses"]
	if !exists {
		return nil, NewMissingRequiredFieldError("addresses")
	}

	addressesArray, ok := contractAddressInterface.([]interface{})
	if !ok || len(addressesArray) == 0 {
		return nil, NewInvalidNodeConfigError("addresses must be a non-empty array")
	}

	contractAddressStr, ok := addressesArray[0].(string)
	if !ok {
		return nil, NewInvalidAddressError("contract address must be a string")
	}

	// Extract ABI
	contractAbiInterface, exists := queryMap["contractAbi"]
	if !exists {
		return nil, NewMissingRequiredFieldError("contractAbi")
	}

	abiArray, ok := contractAbiInterface.([]interface{})
	if !ok {
		return nil, NewInvalidNodeConfigError("invalid contractAbi format")
	}

	// Convert ABI items directly to protobuf Values (same as direct calls path)
	abiValues := make([]*structpb.Value, len(abiArray))
	for i, abiItem := range abiArray {
		if abiStr, ok := abiItem.(string); ok {
			// ABI item is a JSON string, parse it to map first
			var abiMap map[string]interface{}
			if err := json.Unmarshal([]byte(abiStr), &abiMap); err != nil {
				return nil, fmt.Errorf("failed to parse ABI JSON string at index %d: %v", i, err)
			}
			val, err := structpb.NewValue(abiMap)
			if err != nil {
				return nil, fmt.Errorf("failed to convert parsed ABI to protobuf value at index %d: %v", i, err)
			}
			abiValues[i] = val
		} else if abiMap, ok := abiItem.(map[string]interface{}); ok {
			// ABI item is already a map, convert directly to protobuf Value
			val, err := structpb.NewValue(abiMap)
			if err != nil {
				return nil, fmt.Errorf("failed to convert ABI map to protobuf value at index %d: %v", i, err)
			}
			abiValues[i] = val
		} else {
			return nil, NewInvalidNodeConfigError(fmt.Sprintf("invalid ABI item format at index %d (expected string or map, got %T)", i, abiItem))
		}
	}

	// Get method params as strings (ContractReadNode expects []string)
	methodParams := methodCall.GetMethodParams()

	// Create a temporary contractRead node for execution (same as direct calls)
	contractReadNode := &avsproto.ContractReadNode{
		Config: &avsproto.ContractReadNode_Config{
			ContractAddress: contractAddressStr,
			ContractAbi:     abiValues,
			MethodCalls: []*avsproto.ContractReadNode_MethodCall{
				{
					MethodName:   methodCall.GetMethodName(),
					MethodParams: methodParams,
				},
			},
		},
	}

	// Create a temporary VM for contract read execution using proper initialization
	tempVM := NewVM()
	tempVM.logger = n.logger
	tempVM.smartWalletConfig = n.smartWalletConfig // Use the engine's smart wallet config
	tempVM.SetSimulation(true)                     // Use simulation mode for reads

	// Wrap into a temporary TaskNode and execute using VM's runContractRead
	// Use the same ID style as real nodes for consistency
	tempStepID := model.GenerateID()
	tempTaskNode := &avsproto.TaskNode{Id: tempStepID, Type: avsproto.NodeType_NODE_TYPE_CONTRACT_READ, TaskType: &avsproto.TaskNode_ContractRead{ContractRead: contractReadNode}}
	executionStep, err := tempVM.runContractRead(tempTaskNode)
	if err != nil {
		return nil, fmt.Errorf("contract read failed: %v", err)
	}

	// Extract result from execution step
	results := make(map[string]interface{})
	if executionStep != nil && executionStep.Success {
		contractReadOutput := executionStep.GetContractRead()
		if contractReadOutput != nil && contractReadOutput.Data != nil {
			dataInterface := contractReadOutput.Data.AsInterface()
			if resultData, ok := dataInterface.(map[string]interface{}); ok {
				// Merge method results
				for key, value := range resultData {
					results[key] = value
				}
			}
		}
	}

	return results, nil
}

// runEventTriggerWithTenderlySimulation executes event trigger using Tenderly simulation
func (n *Engine) runEventTriggerWithTenderlySimulation(ctx context.Context, queriesArray []interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	if n.logger != nil {
		n.logger.Debug("🔍 TRACE: runEventTriggerWithTenderlySimulation called")
		n.logger.Info("🔮 EventTrigger: Starting Tenderly simulation mode",
			"queriesCount", len(queriesArray))
	}

	// Use shared Tenderly client from engine
	tenderlyClient := n.tenderlyClient

	// Get chain ID for simulation
	var chainID int64 = 11155111 // Default to Sepolia
	if n.tokenEnrichmentService != nil {
		chainID = int64(n.tokenEnrichmentService.GetChainID())
	}

	// Process the first query for simulation (Tenderly simulates one event at a time)
	if len(queriesArray) == 0 {
		return nil, NewMissingRequiredFieldError("queries")
	}

	queryMap, ok := queriesArray[0].(map[string]interface{})
	if !ok {
		return nil, NewInvalidNodeConfigError("invalid query format")
	}

	// Convert query map to protobuf format for simulation
	if n.logger != nil {
		n.logger.Info("🔍 Converting query map to protobuf for Tenderly simulation",
			"hasMethodCalls", queryMap["methodCalls"] != nil)
		if methodCallsInterface, exists := queryMap["methodCalls"]; exists {
			if methodCallsArray, ok := methodCallsInterface.([]interface{}); ok {
				n.logger.Info("🔍 Found method calls in query map",
					"methodCallsCount", len(methodCallsArray))
				for i, methodCallInterface := range methodCallsArray {
					if methodCallMap, ok := methodCallInterface.(map[string]interface{}); ok {
						n.logger.Info("🔍 Method call details",
							"index", i,
							"methodName", methodCallMap["methodName"],
							"callData", methodCallMap["callData"],
							"applyToFields", methodCallMap["applyToFields"])
					}
				}
			}
		}
	}

	query, err := n.convertMapToEventQuery(queryMap)
	if err != nil {
		return nil, fmt.Errorf("failed to convert query: %w", err)
	}

	// Convert all queries for direction determination
	var allQueries []*avsproto.EventTrigger_Query
	for i, queryInterface := range queriesArray {
		if queryMapItem, ok := queryInterface.(map[string]interface{}); ok {
			convertedQuery, err := n.convertMapToEventQuery(queryMapItem)
			if err != nil {
				if n.logger != nil {
					n.logger.Warn("Failed to convert query for direction determination", "queryIndex", i, "error", err)
				}
				continue
			}
			allQueries = append(allQueries, convertedQuery)
		}
	}

	if n.logger != nil {
		methodCallsCount := 0
		if query != nil && query.GetMethodCalls() != nil {
			methodCallsCount = len(query.GetMethodCalls())
		}
		n.logger.Info("✅ Query conversion completed for Tenderly simulation",
			"hasQuery", query != nil,
			"methodCallsCount", methodCallsCount)
	}

	// Simulate the event using Tenderly (gets real current data)
	simulatedLog, err := tenderlyClient.SimulateEventTrigger(ctx, query, chainID)
	if err != nil {
		n.logger.Error("🚫 Tenderly simulation failed", "error", err)
		return nil, fmt.Errorf("tenderly event simulation failed: %w", err)
	}

	// Check if conditions exist - if so, we need to evaluate them after enrichment
	hasConditions := len(query.GetConditions()) > 0

	// Build raw metadata (the original blockchain event data)
	topics := make([]string, len(simulatedLog.Topics))
	for i, topic := range simulatedLog.Topics {
		topics[i] = topic.Hex()
	}

	// Convert topics to protobuf-compatible format for metadata
	topicsMetadata := make([]interface{}, len(topics))
	for i, topic := range topics {
		topicsMetadata[i] = topic
	}

	metadata := map[string]interface{}{
		"address":          simulatedLog.Address.Hex(), // Original contract address
		"topics":           topicsMetadata,             // Now protobuf-compatible
		"data":             "0x" + common.Bytes2Hex(simulatedLog.Data),
		"blockNumber":      simulatedLog.BlockNumber,
		"transactionHash":  simulatedLog.TxHash.Hex(),
		"transactionIndex": simulatedLog.TxIndex,
		"blockHash":        simulatedLog.BlockHash.Hex(),
		"logIndex":         simulatedLog.Index,
		"removed":          simulatedLog.Removed,
		"chainId":          chainID,
	}

	// Use shared enrichment logic instead of duplicate ABI parsing and token enrichment
	enrichmentParams := SharedEventEnrichmentParams{
		EventLog:               simulatedLog,
		ContractABI:            query.GetContractAbi(),
		Query:                  query,
		AllQueries:             allQueries, // Pass all queries for direction determination
		TokenEnrichmentService: n.tokenEnrichmentService,
		RpcClient:              nil, // Explicitly nil in simulation to avoid RPC dependency
		Logger:                 n.logger,
		ChainID:                chainID, // Add chainID for chain name resolution
	}

	// Enrich the event data using shared logic
	enrichmentResult, err := EnrichEventWithTokenMetadata(enrichmentParams)
	if err != nil {
		if n.logger != nil {
			n.logger.Warn("Failed to enrich simulated event data, using basic metadata", "error", err)
		}
		// Fallback to basic metadata if shared enrichment fails
		enrichmentResult = &SharedEventEnrichmentResult{
			ParsedData:      metadata,
			IsTransferEvent: false,
			EventName:       "Unknown",
		}
	}

	// Extract results from shared enrichment
	parsedData := enrichmentResult.ParsedData
	isTransferEvent := enrichmentResult.IsTransferEvent

	if n.logger != nil {
		n.logger.Info("✅ SharedEventEnrichment: Simulation completed successfully",
			"contract", simulatedLog.Address.Hex(),
			"block", simulatedLog.BlockNumber,
			"txHash", simulatedLog.TxHash.Hex(),
			"chainId", chainID,
			"is_transfer", isTransferEvent,
			"event_name", enrichmentResult.EventName)
	}

	// Execute method calls to get additional data for enhanced response
	methodCallResults := make(map[string]interface{})
	n.logger.Info("🔧 EventTrigger simulation: Checking method calls",
		"hasMethodCalls", query.GetMethodCalls() != nil,
		"methodCallsCount", len(query.GetMethodCalls()))
	if query.GetMethodCalls() != nil && len(query.GetMethodCalls()) > 0 {
		n.logger.Info("🔧 EventTrigger simulation: Executing method calls",
			"methodCallsCount", len(query.GetMethodCalls()))
		for _, methodCall := range query.GetMethodCalls() {
			if methodCall.GetMethodName() != "" {
				n.logger.Info("🔧 Executing method call",
					"methodName", methodCall.GetMethodName(),
					"methodParams", methodCall.GetMethodParams())
				result, err := n.executeMethodCallForSimulation(ctx, methodCall, queryMap, chainID)
				if err != nil {
					n.logger.Warn("Failed to execute method call for enhanced response",
						"method", methodCall.GetMethodName(),
						"error", err)
					continue
				}
				n.logger.Info("✅ Method call result",
					"methodName", methodCall.GetMethodName(),
					"resultKeys", GetMapKeys(result))
				// Merge results
				for key, value := range result {
					methodCallResults[key] = value
				}
			}
		}
	}

	// Add enriched event data to method call results
	for key, value := range parsedData {
		methodCallResults[key] = value
	}

	// Apply decimal formatting to event fields if decimals method was called
	// Store the formatting context for use in condition evaluation
	var formattingContext *DecimalFormattingContext
	if query.GetMethodCalls() != nil {
		var decimalsValue *big.Int

		if n.logger != nil {
			n.logger.Error("🔍 FORCE DEBUG: Decimal formatting logic executing",
				"methodCallResultsKeys", GetMapKeys(methodCallResults),
				"methodCallsCount", len(query.GetMethodCalls()))
			// Debug: Print all method call results
			for key, value := range methodCallResults {
				n.logger.Info("🔍 DEBUG: Method call result",
					"key", key,
					"value", value,
					"type", fmt.Sprintf("%T", value))
			}
		}

		// Find decimals method call and extract applyToFields
		for _, methodCall := range query.GetMethodCalls() {
			if n.logger != nil {
				n.logger.Info("🔍 DEBUG: Processing method call",
					"methodName", methodCall.GetMethodName(),
					"hasApplyToFields", len(methodCall.GetApplyToFields()) > 0,
					"applyToFields", methodCall.GetApplyToFields())
			}

			if methodCall.GetMethodName() == "decimals" && len(methodCall.GetApplyToFields()) > 0 {
				// Check if we got decimals result from method calls
				if decimalsResult, exists := methodCallResults["decimals"]; exists {
					if decimalsFloat, ok := decimalsResult.(float64); ok {
						decimalsValue = big.NewInt(int64(decimalsFloat))
					} else if decimalsInt, ok := decimalsResult.(int64); ok {
						decimalsValue = big.NewInt(decimalsInt)
					} else if decimalsStr, ok := decimalsResult.(string); ok {
						if parsed, success := new(big.Int).SetString(decimalsStr, 10); success {
							decimalsValue = parsed
						}
					}
				}

				if decimalsValue != nil {
					// Create list of fields to format from applyToFields
					var fieldsToFormat []string
					for _, applyToField := range methodCall.GetApplyToFields() {
						parts := strings.Split(applyToField, ".")
						if len(parts) == 2 {
							targetEventName := parts[0]
							// Use the full field name for mapping (eventName.fieldName)
							fieldsToFormat = append(fieldsToFormat, fmt.Sprintf("%s.%s", targetEventName, parts[1]))
						}
					}

					// Create formatting context for consistent decimal handling
					formattingContext = NewDecimalFormattingContext(decimalsValue, fieldsToFormat, "decimals")

					// Apply formatting to each target event field using shared utility
					for _, applyToField := range methodCall.GetApplyToFields() {
						parts := strings.Split(applyToField, ".")
						if len(parts) == 2 {
							targetEventName := parts[0]

							// Check if we have this event in our parsed data
							if eventData, exists := parsedData[targetEventName].(map[string]interface{}); exists {
								// Apply decimal formatting using shared utility
								formattingContext.ApplyDecimalFormattingToEventData(eventData, targetEventName, n.logger)
							}
						}
					}
				}
				break
			}
		}
	}

	// Always include chainId in the response data for consistency
	methodCallResults["chainId"] = chainID

	// Evaluate conditions with details for enhanced response
	var allConditionsMet bool = true
	if hasConditions {
		// Use the structured parsedData for condition evaluation with decimal formatting context
		_, allConditionsMet = n.evaluateConditionsWithDetailsAndDecimalContext(parsedData, queryMap, formattingContext)
	}

	// Create response following the same pattern as runEventTriggerWithDirectCalls
	response := make(map[string]interface{})

	// Add the parsed ABI fields in structured format
	response["data"] = parsedData

	// DEBUG: Log the final parsedData structure
	if n.logger != nil {
		n.logger.Info("🔍 DEBUG: Final response data structure",
			"parsedDataKeys", GetMapKeys(parsedData))
		for eventName, eventFields := range parsedData {
			if eventFieldsMap, ok := eventFields.(map[string]interface{}); ok {
				n.logger.Info("🔍 DEBUG: Event fields",
					"eventName", eventName,
					"fields", GetMapKeys(eventFieldsMap))
				if current, exists := eventFieldsMap["current"]; exists {
					n.logger.Info("🔍 DEBUG: Current field value",
						"eventName", eventName,
						"current", current,
						"type", fmt.Sprintf("%T", current))
				}
			}
		}
	}

	// Add raw event log fields as metadata (direct format for backward compatibility)
	response["metadata"] = enrichmentResult.RawEventData

	// Set success based on condition evaluation
	response["success"] = allConditionsMet
	if !allConditionsMet {
		response["error"] = "Conditions not met for simulated event"
	} else {
		response["error"] = ""
	}

	// Add execution context (chainId, isSimulated, provider)
	response["executionContext"] = GetExecutionContext(chainID, true) // true = isSimulation

	// Add debug trace info
	debugResponse := map[string]interface{}{
		"debug_trace":     "runEventTriggerWithTenderlySimulation_COMPLETED",
		"debug_timestamp": time.Now().Unix(),
	}
	for key, value := range debugResponse {
		response[key] = value
	}
	response["debug_path"] = "TENDERLY_SIMULATION"

	n.logger.Info("✅ EventTrigger simulation: Returning enhanced response format",
		"contract", simulatedLog.Address.Hex(),
		"hasConditions", hasConditions,
		"allConditionsMet", allConditionsMet,
		"dataKeys", GetMapKeys(methodCallResults))

	return response, nil
}

// parseEventWithABI parses an event log using the provided contract ABI and applies method calls for enhanced formatting
func (n *Engine) parseEventWithABI(eventLog *types.Log, contractABIString string, query *avsproto.EventTrigger_Query) (map[string]interface{}, error) {
	// Parse the ABI
	contractABI, err := abi.JSON(strings.NewReader(contractABIString))
	if err != nil {
		return nil, fmt.Errorf("failed to parse contract ABI: %w", err)
	}

	// Find the matching event in ABI using the first topic (event signature)
	return n.parseEventWithParsedABI(eventLog, &contractABI, query)
}

// parseEventWithParsedABI contains the shared logic for both optimized and legacy methods
func (n *Engine) parseEventWithParsedABI(eventLog *types.Log, contractABI *abi.ABI, query *avsproto.EventTrigger_Query) (map[string]interface{}, error) {
	// Find the matching event in ABI using the first topic (event signature)
	if len(eventLog.Topics) == 0 {
		return nil, fmt.Errorf("event log has no topics")
	}

	eventSignature := eventLog.Topics[0]
	var matchingEvent *abi.Event
	var eventName string

	for name, event := range contractABI.Events {
		if event.ID == eventSignature {
			matchingEvent = &event
			eventName = name
			break
		}
	}

	if matchingEvent == nil {
		return nil, fmt.Errorf("no matching event found in ABI for signature %s", eventSignature.Hex())
	}

	// Decode the event data
	decodedData, err := contractABI.Unpack(eventName, eventLog.Data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode event data for %s: %w", eventName, err)
	}

	// Initialize the result map with structured event data
	parsedData := make(map[string]interface{})

	// Create structured format: eventName as key, fields as nested object
	eventFields := make(map[string]interface{})

	// Process method calls for decimal formatting
	var decimalsValue *big.Int
	var fieldsToFormat []string

	if query != nil {
		methodCalls := query.GetMethodCalls()
		if n.logger != nil {
			n.logger.Info("🔍 Processing method calls for event formatting",
				"methodCallsCount", len(methodCalls),
				"hasRpcConn", rpcConn != nil)
		}

		for _, methodCall := range methodCalls {
			if n.logger != nil {
				n.logger.Info("📞 Processing method call",
					"methodName", methodCall.GetMethodName(),
					"callData", methodCall.GetCallData(),
					"applyToFields", methodCall.GetApplyToFields())
			}

			if methodCall.GetMethodName() == "decimals" {
				// Generate callData from methodName and methodParams if callData is empty
				var existingCallData string
				if methodCall.CallData != nil {
					existingCallData = *methodCall.CallData
				}
				callData, err := GenerateOrUseCallData(methodCall.GetMethodName(), existingCallData, methodCall.GetMethodParams(), contractABI)
				if err != nil {
					if n.logger != nil {
						n.logger.Error("❌ Failed to generate callData for decimals method",
							"methodName", methodCall.GetMethodName(),
							"providedCallData", methodCall.GetCallData(),
							"methodParams", methodCall.GetMethodParams(),
							"error", err)
					}
					continue // Skip this method call
				}

				// Make the decimals() call to the contract
				if decimals, err := n.callContractMethod(eventLog.Address, callData); err == nil {
					if decimalsInt, ok := decimals.(*big.Int); ok {
						decimalsValue = decimalsInt

						// Process applyToFields to extract field names for the current event
						// Format: "eventName.fieldName" or just "eventName" for single field
						var processedFields []string
						for _, applyToField := range methodCall.GetApplyToFields() {
							if n.logger != nil {
								n.logger.Debug("Processing applyToField", "applyToField", applyToField, "eventName", eventName)
							}

							// Parse the eventName.fieldName format or just eventName for single values
							parts := strings.Split(applyToField, ".")
							var targetEventName, targetFieldName string

							if len(parts) == 1 {
								// Simple format: just eventName (for single field events or when applying to all fields)
								targetEventName = parts[0]
								targetFieldName = parts[0] // Use event name as field name fallback
							} else if len(parts) == 2 {
								// Dot notation format: eventName.fieldName
								targetEventName = parts[0]
								targetFieldName = parts[1]
							} else {
								if n.logger != nil {
									n.logger.Debug("Invalid applyToFields format", "applyToField", applyToField, "expected", "eventName or eventName.fieldName", "parts", parts)
								}
								continue
							}

							// Check if this applyToField targets the current event
							if targetEventName == eventName {
								processedFields = append(processedFields, targetFieldName)
								if n.logger != nil {
									n.logger.Debug("Added field for decimal formatting",
										"targetEventName", targetEventName,
										"targetFieldName", targetFieldName,
										"currentEventName", eventName)
								}
							} else {
								if n.logger != nil {
									n.logger.Debug("Skipping applyToField for different event",
										"targetEventName", targetEventName,
										"currentEventName", eventName)
								}
							}
						}

						fieldsToFormat = processedFields
						if n.logger != nil {
							n.logger.Info("📞 Retrieved decimals from contract",
								"contract", eventLog.Address.Hex(),
								"decimals", decimalsValue.String(),
								"originalApplyToFields", methodCall.GetApplyToFields(),
								"processedFieldsToFormat", fieldsToFormat,
								"eventName", eventName)
						}
					}
				} else {
					if n.logger != nil {
						n.logger.Warn("Failed to call decimals() method", "error", err)
					}
				}
				break
			}
		}
	} else {
		if n.logger != nil {
			n.logger.Info("🔍 No query provided for method calls processing")
		}
	}

	// Create ABI value converter AFTER processing method calls to ensure decimalsValue and fieldsToFormat are set
	converter := NewABIValueConverter(decimalsValue, fieldsToFormat)

	// Process event inputs (both indexed and non-indexed)
	indexedCount := 0
	nonIndexedCount := 0

	for _, input := range matchingEvent.Inputs {
		if input.Indexed {
			// Get from topics (topic[0] is signature, so indexed params start from topic[1])
			topicIndex := indexedCount + 1
			if topicIndex < len(eventLog.Topics) {
				// Convert indexed topic values based on ABI type
				topicValue := eventLog.Topics[topicIndex]

				switch input.Type.T {
				case abi.UintTy, abi.IntTy:
					// Convert numeric types to proper types
					if bigInt := new(big.Int).SetBytes(topicValue.Bytes()); bigInt != nil {
						convertedValue := converter.ConvertABIValueToInterface(bigInt, input.Type, input.Name)
						parsedData[input.Name] = convertedValue

						if n.logger != nil {
							n.logger.Debug("Added indexed numeric field",
								"field", input.Name,
								"type", input.Type.String(),
								"rawValue", bigInt.String(),
								"convertedValue", convertedValue)
						}
					} else {
						eventFields[input.Name] = topicValue.Hex()
					}
				case abi.BoolTy:
					// Convert boolean from topic
					boolVal := new(big.Int).SetBytes(topicValue.Bytes()).Cmp(big.NewInt(0)) != 0
					eventFields[input.Name] = boolVal
				case abi.AddressTy:
					// Keep addresses as hex
					eventFields[input.Name] = common.HexToAddress(topicValue.Hex()).Hex()
				case abi.HashTy, abi.FixedBytesTy:
					// Keep hashes and fixed bytes as hex
					eventFields[input.Name] = topicValue.Hex()
				default:
					// Default to hex for other types
					eventFields[input.Name] = topicValue.Hex()
				}

				if n.logger != nil {
					n.logger.Debug("Added indexed field from topic",
						"field", input.Name,
						"type", input.Type.String(),
						"value", parsedData[input.Name])
				}
			}
			indexedCount++
		} else {
			// Get from decoded data
			if nonIndexedCount < len(decodedData) {
				// Convert the value using ABI type information
				value := decodedData[nonIndexedCount]

				convertedValue := converter.ConvertABIValueToInterface(value, input.Type, input.Name)
				eventFields[input.Name] = convertedValue

				if n.logger != nil {
					n.logger.Debug("Added non-indexed field from data",
						"field", input.Name,
						"type", input.Type.String(),
						"rawValue", fmt.Sprintf("%v", value),
						"convertedValue", convertedValue)
				}
			}
			nonIndexedCount++
		}
	}

	// Add any raw fields metadata from decimal formatting
	rawFieldsMetadata := converter.GetRawFieldsMetadata()
	for key, value := range rawFieldsMetadata {
		eventFields[key] = value
	}

	// Add decimals info if we retrieved it
	if decimalsValue != nil {
		eventFields["decimals"] = decimalsValue.Uint64() // Return as number, not string
	}

	// Apply decimal formatting to specified fields
	if decimalsValue != nil && len(fieldsToFormat) > 0 {
		if n.logger != nil {
			n.logger.Debug("Applying decimal formatting to event fields",
				"decimals", decimalsValue.String(),
				"fieldsToFormat", fieldsToFormat,
				"eventName", eventName)
		}

		for _, fieldName := range fieldsToFormat {
			if rawValue, exists := eventFields[fieldName]; exists {
				if rawValueStr, ok := rawValue.(string); ok {
					if _, success := new(big.Int).SetString(rawValueStr, 10); success {
						// Apply decimal formatting using the token enrichment service
						if n.tokenEnrichmentService != nil {
							formattedValue := n.tokenEnrichmentService.FormatTokenValue(rawValueStr, uint32(decimalsValue.Uint64()))
							eventFields[fieldName] = formattedValue

							// Also store the raw value for reference
							eventFields[fieldName+"Raw"] = rawValueStr

							if n.logger != nil {
								n.logger.Debug("Applied decimal formatting to event field",
									"fieldName", fieldName,
									"rawValue", rawValueStr,
									"formattedValue", formattedValue,
									"decimals", decimalsValue.String())
							}
						}
					}
				}
			}
		}
	}

	// Create the structured format: eventName as key, fields as nested object
	parsedData[eventName] = eventFields

	// 🔥 ENHANCED TRANSFER EVENT ENRICHMENT
	// If this is a Transfer event, create enriched transfer_log data

	if eventName == "Transfer" && n.tokenEnrichmentService != nil {
		if n.logger != nil {
			n.logger.Info("🎯 Detected Transfer event - enriching with token metadata",
				"contract", eventLog.Address.Hex(),
				"hasTokenService", n.tokenEnrichmentService != nil)
		}

		// Get token metadata from the enrichment service
		tokenMetadata, err := n.tokenEnrichmentService.GetTokenMetadata(eventLog.Address.Hex())
		if err != nil {
			if n.logger != nil {
				n.logger.Warn("Failed to get token metadata for Transfer event", "error", err, "contract", eventLog.Address.Hex())
			}
		}

		// Create standardized transfer event response structure
		// Initialize with default values (logIndex and transactionIndex excluded - available in metadata)
		transferResponse := CreateStandardizedTransferResponse(
			eventLog.Address.Hex(), // contractAddress
			eventLog.TxHash.Hex(),  // txHash
			eventLog.BlockNumber,   // blockNumber
			"",                     // fromAddr (will be populated below)
			"",                     // toAddr (will be populated below)
			"Unknown Token",        // tokenName (default)
			"UNKNOWN",              // tokenSymbol (default)
			18,                     // tokenDecimals (default)
			"0",                    // value (will be populated below)
		)

		// Populate transfer-specific fields from ABI parsing
		if fromAddr, ok := eventFields["from"].(string); ok {
			transferResponse.FromAddress = fromAddr
		}
		if toAddr, ok := eventFields["to"].(string); ok {
			transferResponse.ToAddress = toAddr
		}

		// Populate token metadata if available
		if tokenMetadata != nil {
			transferResponse.TokenName = tokenMetadata.Name
			transferResponse.TokenSymbol = tokenMetadata.Symbol
			transferResponse.TokenDecimals = tokenMetadata.Decimals

			// Use the formatted value from ABI parsing if available, otherwise format using token metadata
			if formattedValue, hasFormatted := eventFields["value"]; hasFormatted {
				if valueStr, ok := formattedValue.(string); ok {
					transferResponse.Value = valueStr
				}
			} else if rawValue, ok := eventFields["valueRaw"].(string); ok {
				// Fallback: format using token metadata if ABI didn't format it
				formattedValue := n.tokenEnrichmentService.FormatTokenValue(rawValue, tokenMetadata.Decimals)
				transferResponse.Value = formattedValue
			}

			if n.logger != nil {
				n.logger.Info("✅ Transfer event enrichment completed",
					"tokenSymbol", tokenMetadata.Symbol,
					"tokenName", tokenMetadata.Name,
					"decimals", tokenMetadata.Decimals,
					"value", transferResponse.Value)
			}
		} else {
			// Even without token metadata, try to format using decimals from method call
			if decimalsValue != nil && len(fieldsToFormat) > 0 {
				decimalsUint32 := uint32(decimalsValue.Uint64())
				transferResponse.TokenDecimals = decimalsUint32

				// Use the formatted value from ABI parsing if available, otherwise format using method call decimals
				if formattedValue, hasFormatted := eventFields["value"]; hasFormatted {
					if valueStr, ok := formattedValue.(string); ok {
						transferResponse.Value = valueStr
					}
				} else if rawValue, ok := eventFields["valueRaw"].(string); ok {
					// Fallback: format using method call decimals if ABI didn't format it
					if n.tokenEnrichmentService != nil {
						formattedValue := n.tokenEnrichmentService.FormatTokenValue(rawValue, decimalsUint32)
						transferResponse.Value = formattedValue
					}
				}
			}

			if n.logger != nil {
				n.logger.Info("⚠️ Transfer event enrichment with limited metadata",
					"tokenMetadataAvailable", false,
					"decimalsFromMethodCall", decimalsValue != nil)
			}
		}

		// Add proper blockTimestamp for real events (not simulated)
		// For real events, we can get the actual block timestamp if needed
		if eventLog.BlockNumber > 0 && rpcConn != nil {
			// Get actual block timestamp for real events
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			header, err := rpcConn.HeaderByNumber(ctx, big.NewInt(int64(eventLog.BlockNumber)))
			cancel()

			if err == nil {
				transferResponse.BlockTimestamp = int64(header.Time) * 1000 // Convert to milliseconds for JavaScript compatibility
			}
			// If error, keep the default timestamp from CreateStandardizedTransferResponse
		}

		// Convert standardized response to map for structured format
		// This provides all the standardized fields without deprecated or duplicate ones
		// Note: logIndex and transactionIndex are excluded as they're available in metadata
		transferFields := map[string]interface{}{
			"contractAddress": transferResponse.ContractAddress,
			"tokenName":       transferResponse.TokenName,
			"tokenSymbol":     transferResponse.TokenSymbol,
			"tokenDecimals":   transferResponse.TokenDecimals,
			"transactionHash": transferResponse.TransactionHash,
			"blockNumber":     transferResponse.BlockNumber,
			"blockTimestamp":  transferResponse.BlockTimestamp,
			"fromAddress":     transferResponse.FromAddress,
			"toAddress":       transferResponse.ToAddress,
			"value":           transferResponse.Value,
		}

		// Return structured format for Transfer events too
		transferData := make(map[string]interface{})
		transferData[eventName] = transferFields
		return transferData, nil
	}

	// For non-Transfer events, return basic parsed data
	return parsedData, nil
}

// callContractMethod makes a contract method call to retrieve additional data
func (n *Engine) callContractMethod(contractAddress common.Address, callData string) (interface{}, error) {
	// Ensure RPC connection is available
	if rpcConn == nil {
		return nil, fmt.Errorf("RPC connection not available for contract method call")
	}

	// Remove 0x prefix if present
	callDataHex := strings.TrimPrefix(callData, "0x")

	// Convert hex string to bytes
	callDataBytes := common.FromHex("0x" + callDataHex)

	// Create the call message
	msg := ethereum.CallMsg{
		To:   &contractAddress,
		Data: callDataBytes,
	}

	// Make the contract call
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := rpcConn.CallContract(ctx, msg, nil)
	if err != nil {
		return nil, fmt.Errorf("contract call failed: %w", err)
	}

	// For decimals() method, we expect a uint8 return value
	// The result is 32 bytes, but we only need the last byte for uint8
	if len(result) >= 32 {
		// Convert the last byte to big.Int (decimals is typically uint8)
		decimals := new(big.Int).SetBytes(result[31:32])
		return decimals, nil
	}

	return nil, fmt.Errorf("unexpected result length: %d", len(result))
}

// runEventTriggerWithHistoricalSearch executes event trigger using historical blockchain search
func (n *Engine) runEventTriggerWithHistoricalSearch(ctx context.Context, queriesArray []interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// Get the latest block number
	currentBlock, err := rpcConn.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current block number: %w", err)
	}

	// Chain-specific search strategy: Use 3-month and 6-month ranges based on chain block times
	var searchRanges []uint64
	if n.tokenEnrichmentService != nil {
		chainID := n.tokenEnrichmentService.GetChainID()
		searchRanges = GetChainSearchRanges(chainID)

		if n.logger != nil {
			ranges := GetBlockSearchRanges(chainID)
			n.logger.Info("EventTrigger: Using chain-specific search ranges",
				"chainID", chainID,
				"oneMonth", ranges.OneMonth,
				"twoMonths", ranges.TwoMonths,
				"fourMonths", ranges.FourMonths)
		}
	} else {
		// Fallback to default Ethereum-like ranges if no token service available
		searchRanges = GetChainSearchRanges(1) // Default to Ethereum mainnet

		if n.logger != nil {
			n.logger.Warn("EventTrigger: Using default Ethereum search ranges (no token service available)")
		}
	}

	var allEvents []types.Log
	var totalSearched uint64

	// Process each query in the queries
	for queryIndex, queryInterface := range queriesArray {
		queryMap, ok := queryInterface.(map[string]interface{})
		if !ok {
			continue
		}

		// Parse query configuration
		var addresses []common.Address
		var topics [][]common.Hash
		var maxEventsPerBlock uint32

		// Extract addresses list
		if addressesInterface, exists := queryMap["addresses"]; exists {
			if addressesArray, ok := addressesInterface.([]interface{}); ok {
				for _, addrInterface := range addressesArray {
					if addrStr, ok := addrInterface.(string); ok && addrStr != "" {
						addresses = append(addresses, common.HexToAddress(addrStr))
					}
				}
			}
		}

		// Extract topics list and build proper Ethereum log filter structure
		if topicsInterface, exists := queryMap["topics"]; exists {
			if topicsArray, ok := topicsInterface.([]interface{}); ok {
				// For each query, we expect one topic group that defines the topic filter structure
				// Multiple topic groups within one query is not supported in Ethereum filtering
				if len(topicsArray) > 0 {
					if topicGroupMap, ok := topicsArray[0].(map[string]interface{}); ok {
						if valuesInterface, exists := topicGroupMap["values"]; exists {
							if valuesArray, ok := valuesInterface.([]interface{}); ok {
								// Build topics array where each position corresponds to topic[0], topic[1], topic[2], etc.
								// For Ethereum log filtering: topics[i] = []common.Hash{possible values for topic position i}
								// If a position should be wildcard (any value), use nil for that position

								// Initialize topics array with the correct size
								topics = make([][]common.Hash, len(valuesArray))

								// Process each value and assign to the corresponding topic position
								for i, valueInterface := range valuesArray {
									if valueStr, ok := valueInterface.(string); ok && valueStr != "" {
										// Non-null value: create a topic filter for this position
										topics[i] = []common.Hash{common.HexToHash(valueStr)}
									} else {
										// null value: this topic position should be wildcard (nil)
										topics[i] = nil
									}
								}
							}
						}
					}
				}
			}
		}

		// Extract maxEventsPerBlock
		if maxEventsInterface, exists := queryMap["maxEventsPerBlock"]; exists {
			if maxEventsFloat, ok := maxEventsInterface.(float64); ok {
				maxEventsPerBlock = uint32(maxEventsFloat)
			}
		}

		if n.logger != nil {
			n.logger.Info("EventTrigger: Processing query",
				"queryIndex", queryIndex,
				"addressesCount", len(addresses),
				"topicGroupsCount", len(topics),
				"maxEventsPerBlock", maxEventsPerBlock)
		}

		// Execute search for this query
		queryEvents, querySearched, err := n.searchEventsForQuery(ctx, addresses, topics, currentBlock, searchRanges)
		if err != nil {
			if n.logger != nil {
				n.logger.Warn("EventTrigger: Query failed, continuing with other queries",
					"queryIndex", queryIndex,
					"error", err)
			}
			continue
		}

		allEvents = append(allEvents, queryEvents...)
		totalSearched += querySearched

		if n.logger != nil {
			n.logger.Info("EventTrigger: Query completed",
				"queryIndex", queryIndex,
				"eventsFound", len(queryEvents),
				"blocksSearched", querySearched)
		}

		// Continue processing all queries to find the most recent event across all queries
		// Don't stop early - we need to check all queries to find the truly most recent event
	}

	if n.logger != nil {
		n.logger.Info("EventTrigger: All queries processed",
			"queriesProcessed", len(queriesArray),
			"totalEvents", len(allEvents),
			"totalSearched", totalSearched)
	}

	// If no events found after processing all queries
	if len(allEvents) == 0 {
		if n.logger != nil {
			n.logger.Info("EventTrigger: No events found after processing all queries",
				"totalBlocksSearched", totalSearched,
				"queriesCount", len(queriesArray))
		}

		return map[string]interface{}{
			"success":       false,
			"evm_log":       nil,
			"queriesCount":  len(queriesArray),
			"totalSearched": totalSearched,
			"message":       "No events found matching any query criteria",
			"searchMetadata": map[string]interface{}{
				"blocksSearchedBackwards": totalSearched,
				"searchComplete":          true,
				"timeoutOccurred":         false,
			},
		}, nil
	}

	// Find the most recent event (highest block number + log index)
	mostRecentEvent := &allEvents[0]
	for i := 1; i < len(allEvents); i++ {
		current := &allEvents[i]
		if current.BlockNumber > mostRecentEvent.BlockNumber ||
			(current.BlockNumber == mostRecentEvent.BlockNumber && current.Index > mostRecentEvent.Index) {
			mostRecentEvent = current
		}
	}

	// Build the result with proper EventTrigger.Output structure
	topics := make([]string, len(mostRecentEvent.Topics))
	for i, topic := range mostRecentEvent.Topics {
		topics[i] = topic.Hex()
	}

	// Convert topics to protobuf-compatible format for metadata
	topicsMetadata := make([]interface{}, len(topics))
	for i, topic := range topics {
		topicsMetadata[i] = topic
	}

	// Get chain ID for metadata
	var chainID int64 = 11155111 // Default to Sepolia
	if n.tokenEnrichmentService != nil {
		chainID = int64(n.tokenEnrichmentService.GetChainID())
	}

	// Build raw metadata (the original blockchain event data)
	metadata := map[string]interface{}{
		"address":          mostRecentEvent.Address.Hex(), // Original contract address
		"topics":           topicsMetadata,
		"data":             "0x" + common.Bytes2Hex(mostRecentEvent.Data),
		"blockNumber":      mostRecentEvent.BlockNumber,
		"transactionHash":  mostRecentEvent.TxHash.Hex(),
		"transactionIndex": mostRecentEvent.TxIndex,
		"blockHash":        mostRecentEvent.BlockHash.Hex(),
		"logIndex":         mostRecentEvent.Index,
		"removed":          mostRecentEvent.Removed,
		"chainId":          chainID,
	}

	// Parse event data using ABI if provided in any query
	var parsedData map[string]interface{}
	var isTransferEvent bool
	var contractABI string
	var queryWithABI map[string]interface{}

	// Find the first query that has a contract ABI
	for _, queryInterface := range queriesArray {
		if queryMap, ok := queryInterface.(map[string]interface{}); ok {
			if abiInterface, exists := queryMap["contractAbi"]; exists {
				if abiStr, ok := abiInterface.(string); ok && abiStr != "" {
					contractABI = abiStr
					queryWithABI = queryMap
					break
				}
			}
		}
	}

	if contractABI != "" {
		// Convert the query map to protobuf query for method calls support
		protobufQuery, err := n.convertMapToEventQuery(queryWithABI)
		if err != nil {
			n.logger.Warn("Failed to convert query map to protobuf, using ABI without method calls", "error", err)
			protobufQuery = nil
		} else {
			if n.logger != nil {
				methodCallsCount := 0
				if protobufQuery != nil && protobufQuery.GetMethodCalls() != nil {
					methodCallsCount = len(protobufQuery.GetMethodCalls())
				}
				n.logger.Info("✅ Successfully converted query map to protobuf",
					"hasProtobufQuery", protobufQuery != nil,
					"methodCallsCount", methodCallsCount)
			}
		}

		// Parse using the provided ABI
		parsedEventData, err := n.parseEventWithABI(mostRecentEvent, contractABI, protobufQuery)
		if err != nil {
			n.logger.Warn("Failed to parse event with provided ABI, using raw data", "error", err)
			// Fallback to raw data if ABI parsing fails
			parsedData = metadata
		} else {
			parsedData = parsedEventData

			// Check if this is enriched transfer data
			if eventName, ok := parsedEventData["eventName"].(string); ok && eventName == "Transfer" {
				isTransferEvent = true
			}
		}
	} else {
		// No ABI provided, use raw event data
		parsedData = metadata
	}

	// Build the result structure based on event type
	result := map[string]interface{}{
		"success":       true,
		"metadata":      metadata, // Raw blockchain event data
		"queriesCount":  len(queriesArray),
		"totalSearched": totalSearched,
		"totalEvents":   len(allEvents),
		"searchMetadata": map[string]interface{}{
			"blocksSearchedBackwards": totalSearched,
			"searchComplete":          true,
			"timeoutOccurred":         false,
			"stoppedEarly":            true, // Since we stop after finding first event
		},
	}

	// For Transfer events with enriched data, structure it properly
	if isTransferEvent {
		// parsedData contains the enriched transfer_log structure
		result["transfer_log"] = parsedData
		result["data"] = parsedData // Also provide as data for backward compatibility

		if n.logger != nil {
			n.logger.Info("✅ EventTrigger: Created enriched transfer_log structure in historical search",
				"tokenSymbol", parsedData["tokenSymbol"],
				"blockTimestamp", parsedData["blockTimestamp"])
		}
	} else {
		// For non-Transfer events, use standard data structure
		result["data"] = parsedData
	}

	if n.logger != nil {
		hasABI := contractABI != ""
		n.logger.Info("EventTrigger: Successfully found most recent event with queries-based search",
			"blockNumber", mostRecentEvent.BlockNumber,
			"txHash", mostRecentEvent.TxHash.Hex(),
			"address", mostRecentEvent.Address.Hex(),
			"totalEvents", len(allEvents),
			"totalSearched", totalSearched,
			"hasABI", hasABI)
	}

	return result, nil
}

// searchEventsForQuery executes a single query search with recent-first strategy
func (n *Engine) searchEventsForQuery(ctx context.Context, addresses []common.Address, topics [][]common.Hash, currentBlock uint64, searchRanges []uint64) ([]types.Log, uint64, error) {
	var allEvents []types.Log
	var totalSearched uint64

	// PRIORITY SEARCH: Search very recent blocks first to find the most recent events
	// Use a single comprehensive search of recent blocks instead of stopping early
	recentRange := uint64(5000) // Search last 5000 blocks for most recent events

	var fromBlock uint64
	if currentBlock < recentRange {
		fromBlock = 0
	} else {
		fromBlock = currentBlock - recentRange
	}

	// Prepare filter query for recent comprehensive search
	query := ethereum.FilterQuery{
		FromBlock: big.NewInt(int64(fromBlock)),
		ToBlock:   big.NewInt(int64(currentBlock)),
		Addresses: addresses,
		Topics:    topics,
	}

	// Search recent blocks comprehensively
	logs, err := rpcConn.FilterLogs(ctx, query)
	if err == nil {
		totalSearched += (currentBlock - fromBlock)
		allEvents = append(allEvents, logs...)

		if len(logs) > 0 {
			// Return immediately since we found events in the most recent range
			return allEvents, totalSearched, nil
		}
	}

	// FALLBACK SEARCH: If no recent events found, search larger ranges
	for _, searchRange := range searchRanges {
		select {
		case <-ctx.Done():
			if n.logger != nil {
				n.logger.Warn("EventTrigger: Search timeout reached, returning partial results",
					"blocksSearched", totalSearched,
					"eventsFound", len(allEvents))
			}
			return allEvents, totalSearched, nil
		default:
		}

		var fromBlock uint64
		if currentBlock < searchRange {
			fromBlock = 0
		} else {
			fromBlock = currentBlock - searchRange
		}

		// Prepare filter query
		query := ethereum.FilterQuery{
			FromBlock: big.NewInt(int64(fromBlock)),
			ToBlock:   big.NewInt(int64(currentBlock)),
			Addresses: addresses,
			Topics:    topics,
		}

		if n.logger != nil {
			addressStrs := make([]string, len(addresses))
			for i, addr := range addresses {
				addressStrs[i] = addr.Hex()
			}

			topicStrs := make([][]string, len(topics))
			for i, topicGroup := range topics {
				if topicGroup == nil {
					topicStrs[i] = []string{"<wildcard>"}
				} else {
					topicStrs[i] = make([]string, len(topicGroup))
					for j, topic := range topicGroup {
						topicStrs[i][j] = topic.Hex()
					}
				}
			}

			n.logger.Debug("EventTrigger: Fallback search with larger range",
				"fromBlock", fromBlock,
				"toBlock", currentBlock,
				"addresses", addressStrs,
				"topics", topicStrs,
				"blockRange", currentBlock-fromBlock)
		}

		// Fetch logs from Ethereum with timeout context
		logs, err := rpcConn.FilterLogs(ctx, query)
		var usedChunkedSearch bool

		if err != nil {
			if n.logger != nil {
				// Check if it's a known RPC limit error to avoid stack traces
				errorMsg := err.Error()
				if strings.Contains(errorMsg, "Block range limit exceeded") ||
					strings.Contains(errorMsg, "range limit") ||
					strings.Contains(errorMsg, "too many blocks") {
					n.logger.Debug("EventTrigger: Block range limit hit, using chunked search",
						"blockRange", currentBlock-fromBlock)

					// Try chunked search for large ranges
					if currentBlock-fromBlock > 1000 {
						usedChunkedSearch = true

						// Break the range into 1000-block chunks, searching from MOST RECENT first
						chunkSize := uint64(1000)
						var chunkedSearched uint64

						// Search chunks in reverse order (most recent first)
						for chunkStart := currentBlock; chunkStart > fromBlock; {
							// Check timeout during chunked search
							select {
							case <-ctx.Done():
								n.logger.Info("EventTrigger: Chunked search timeout reached",
									"chunkedSearched", chunkedSearched)
								totalSearched += chunkedSearched
								return allEvents, totalSearched, nil
							default:
							}

							chunkEnd := chunkStart
							if chunkStart < fromBlock+chunkSize {
								chunkStart = fromBlock
							} else {
								chunkStart = chunkStart - chunkSize
							}

							chunkQuery := query
							chunkQuery.FromBlock = big.NewInt(int64(chunkStart))
							chunkQuery.ToBlock = big.NewInt(int64(chunkEnd))

							chunkLogs, chunkErr := rpcConn.FilterLogs(ctx, chunkQuery)
							chunkedSearched += (chunkEnd - chunkStart)

							if chunkErr == nil {
								allEvents = append(allEvents, chunkLogs...)
								if n.logger != nil && len(chunkLogs) > 0 {
									n.logger.Info("EventTrigger: Found events in chunk - returning most recent",
										"chunkStart", chunkStart,
										"chunkEnd", chunkEnd,
										"eventsInChunk", len(chunkLogs))
								}
								// Return immediately when we find events (these are most recent due to reverse search)
								if len(chunkLogs) > 0 {
									totalSearched += chunkedSearched
									return allEvents, totalSearched, nil
								}
							} else {
								if n.logger != nil {
									n.logger.Debug("EventTrigger: Chunk failed, continuing",
										"chunkStart", chunkStart,
										"chunkEnd", chunkEnd,
										"error", chunkErr)
								}
							}
						}

						// Update searched counter with chunked progress
						totalSearched += chunkedSearched

						// If chunked search found events, return them
						if len(allEvents) > 0 {
							return allEvents, totalSearched, nil
						}
					}
				} else {
					n.logger.Warn("EventTrigger: Failed to fetch logs, continuing search",
						"fromBlock", fromBlock,
						"toBlock", currentBlock,
						"error", err)
				}
			}

			// If still error after chunked retry, continue to next range
			if err != nil {
				continue
			}
		}

		// Only update searched counter if chunked search wasn't used (it handles its own counting)
		if !usedChunkedSearch {
			totalSearched += (currentBlock - fromBlock)
		}

		allEvents = append(allEvents, logs...)

		if n.logger != nil {
			n.logger.Info("EventTrigger: Fallback search completed for range",
				"fromBlock", fromBlock,
				"toBlock", currentBlock,
				"logsFound", len(logs),
				"totalEventsSoFar", len(allEvents),
				"blockRange", currentBlock-fromBlock)
		}

		// Continue searching even if we found events to ensure we get the most recent ones
		// Only return early if we're searching a very recent range (< 5000 blocks)
		if len(logs) > 0 && (currentBlock-fromBlock) < 5000 {
			return allEvents, totalSearched, nil
		}

		// If we've searched back to genesis, stop
		if fromBlock == 0 {
			break
		}
	}

	return allEvents, totalSearched, nil
}

// runManualTriggerImmediately executes a manual trigger immediately
func (n *Engine) runManualTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// Validate that data is provided and not null
	data, exists := triggerConfig["data"]
	if !exists || data == nil {
		return nil, NewStructuredError(
			avsproto.ErrorCode_INVALID_TRIGGER_CONFIG,
			"ManualTrigger data is required and cannot be null",
			map[string]interface{}{
				"field": "data",
				"issue": "missing or null",
			},
		)
	}

	// Parse language from config (strict requirement - no default)
	lang, err := ParseLanguageFromConfig(triggerConfig)
	if err != nil {
		return nil, err
	}

	// Validate based on language using universal validator
	if err := ValidateInputByLanguage(data, lang); err != nil {
		return nil, err
	}

	if n.logger != nil {
		n.logger.Info("ManualTrigger executed with valid data", "dataType", fmt.Sprintf("%T", data))
	}

	// Process headers
	headers := make(map[string]interface{})
	if headersInterface, exists := triggerConfig["headers"]; exists {
		if headersArray, ok := headersInterface.([]interface{}); ok {
			// Convert array format to map format
			convertedHeaders := make(map[string]interface{})
			for _, item := range headersArray {
				if itemMap, ok := item.(map[string]interface{}); ok {
					if key, keyOk := itemMap["key"].(string); keyOk {
						if value, valueOk := itemMap["value"]; valueOk {
							convertedHeaders[key] = value
						}
					}
				}
			}
			headers = convertedHeaders
		} else if headersMap, ok := headersInterface.(map[string]interface{}); ok {
			headers = headersMap
		}
	}

	if n.logger != nil {
		n.logger.Info("ManualTrigger executed with headers", "headersType", fmt.Sprintf("%T", headers))
	}

	// Process path parameters
	pathParams := make(map[string]interface{})
	if pathParamsInterface, exists := triggerConfig["pathParams"]; exists {
		if pathParamsArray, ok := pathParamsInterface.([]interface{}); ok {
			// Convert array format to map format
			convertedPathParams := make(map[string]interface{})
			for _, item := range pathParamsArray {
				if itemMap, ok := item.(map[string]interface{}); ok {
					if key, keyOk := itemMap["key"].(string); keyOk {
						if value, valueOk := itemMap["value"]; valueOk {
							convertedPathParams[key] = value
						}
					}
				}
			}
			pathParams = convertedPathParams
		} else if pathParamsMap, ok := pathParamsInterface.(map[string]interface{}); ok {
			pathParams = pathParamsMap
		}
	}

	if n.logger != nil {
		n.logger.Info("ManualTrigger executed with pathParams", "pathParamsType", fmt.Sprintf("%T", pathParams))
	}

	// Return result with proper nested structure
	// The main data should be at the top level for direct access
	// The input structure should contain data, headers, and pathParams for template access
	result := map[string]interface{}{
		"data": triggerConfig["data"],
		"input": map[string]interface{}{
			"data":       triggerConfig["data"],
			"headers":    headers,
			"pathParams": pathParams,
		},
	}

	return result, nil
}

// runProcessingNodeWithInputs handles execution of processing node types
func (n *Engine) runProcessingNodeWithInputs(user *model.User, nodeType string, nodeConfig map[string]interface{}, inputVariables map[string]interface{}, useSimulation bool) (map[string]interface{}, error) {
	// Check if this is actually a trigger type that was misrouted
	if IsTriggerNodeType(nodeType) {
		return n.runTriggerImmediately(nodeType, nodeConfig, inputVariables)
	}

	// Load secrets for immediate execution (global macroSecrets + user-level secrets)
	secrets, err := n.LoadSecretsForImmediateExecution(inputVariables)
	if err != nil {
		if n.logger != nil {
			n.logger.Warn("Failed to load secrets for immediate execution", "error", err.Error())
		}
		// Don't fail the request, just use empty secrets
		secrets = make(map[string]string)
	}

	// Create a clean VM for isolated execution with proper secrets (no task needed for immediate execution)
	vm, err := NewVMWithData(nil, nil, n.smartWalletConfig, secrets)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM: %w", err)
	}

	vm.tenderlyClient = n.tenderlyClient

	// Use the simulation mode parameter by default
	vm.WithLogger(n.logger).WithDb(n.db).SetSimulation(useSimulation)

	if n.logger != nil {
		n.logger.Info("RunNodeImmediately: Execution mode set",
			"simulation_mode", useSimulation,
			"node_type", nodeType)
	}

	// Set TaskOwner from authenticated user (extracted from signed API key)
	if user != nil {
		vm.TaskOwner = user.Address
		if n.logger != nil {
			n.logger.Info("RunNodeImmediately: Set TaskOwner from authenticated user", "taskOwner", vm.TaskOwner.Hex())
		}
	}

	// For contractWrite nodes, handle runner validation from settings only; simulation is resolved in the processor from typed config
	if strings.EqualFold(nodeType, "contractWrite") {

		// Require authenticated user (TaskOwner)
		if (vm.TaskOwner == common.Address{}) {
			if n.logger != nil {
				n.logger.Warn("RunNodeImmediately: No authenticated user for contractWrite - refusing to simulate")
			}
			return nil, fmt.Errorf("authentication required for contractWrite")
		}

		// Look for runner in settings instead of workflowContext
		if settingsIface, ok := inputVariables["settings"]; ok {
			if settings, ok := settingsIface.(map[string]interface{}); ok {
				if n.logger != nil {
					n.logger.Info("RunNodeImmediately: Found settings for contractWrite validation", "keys", GetMapKeys(settings))
				}

				// Require runner
				runnerIface, ok := settings["runner"]
				if !ok {
					return nil, fmt.Errorf("settings.runner is required for contractWrite")
				}
				runnerStr, ok := runnerIface.(string)
				if !ok || !common.IsHexAddress(runnerStr) {
					return nil, fmt.Errorf("settings.runner must be a valid hex address for contractWrite")
				}

				// Validate runner belongs to owner
				// For runNodeImmediately, we allow smart wallets that haven't been created yet
				// The paymaster will sponsor their creation if needed (same as deployed workflows)
				resp, err := n.ListWallets(vm.TaskOwner, &avsproto.ListWalletReq{})
				if err != nil {
					return nil, fmt.Errorf("failed to list wallets for owner %s: %w", vm.TaskOwner.Hex(), err)
				}
				var chosenSender common.Address
				walletExists := false
				for _, w := range resp.GetItems() {
					if strings.EqualFold(w.GetAddress(), runnerStr) {
						chosenSender = common.HexToAddress(w.GetAddress())
						walletExists = true
						break
					}
				}

				// If wallet doesn't exist in database, verify it's a valid derived address for this owner
				// We support up to 5 smart wallets per EOA (salt:0 through salt:4)
				if !walletExists {
					if n.logger != nil {
						n.logger.Info("RunNodeImmediately: Smart wallet not found in database, verifying it's a valid derived address for this owner",
							"runner", runnerStr,
							"owner", vm.TaskOwner.Hex())
					}

					// Connect to RPC to derive addresses
					client, err := ethclient.Dial(n.smartWalletConfig.EthRpcUrl)
					if err != nil {
						return nil, fmt.Errorf("failed to connect to RPC for address derivation: %w", err)
					}

					// Check salts 0-4 (we allow up to 5 smart wallets per EOA)
					var matchedSalt *big.Int
					for salt := int64(0); salt < 5; salt++ {
						derivedAddr, err := aa.GetSenderAddress(client, vm.TaskOwner, big.NewInt(salt))
						if err != nil {
							if n.logger != nil {
								n.logger.Debug("Failed to derive address for salt", "salt", salt, "error", err)
							}
							continue
						}

						if strings.EqualFold(derivedAddr.Hex(), runnerStr) {
							matchedSalt = big.NewInt(salt)
							chosenSender = *derivedAddr
							break
						}
					}
					client.Close()

					// If no match found in salts 0-4, reject the runner
					if matchedSalt == nil {
						return nil, NewStructuredError(
							avsproto.ErrorCode_SMART_WALLET_NOT_FOUND,
							fmt.Sprintf("runner %s does not match any derived address (salt:0 through salt:4) for owner %s", runnerStr, vm.TaskOwner.Hex()),
							map[string]interface{}{
								"runner": runnerStr,
								"owner":  vm.TaskOwner.Hex(),
							},
						)
					}

					if n.logger != nil {
						n.logger.Info("RunNodeImmediately: Smart wallet will be created on first use with paymaster sponsorship",
							"sender", chosenSender.Hex(),
							"owner", vm.TaskOwner.Hex(),
							"salt", matchedSalt.Int64(),
							"paymaster", n.smartWalletConfig.PaymasterAddress.Hex())
					}
				}

				vm.AddVar("aa_sender", chosenSender.Hex())
				if n.logger != nil {
					n.logger.Info("RunNodeImmediately: AA sender resolved from settings", "sender", chosenSender.Hex())
				}
			} else {
				return nil, fmt.Errorf("settings must be an object for contractWrite")
			}
		} else {
			return nil, fmt.Errorf("settings is required for contractWrite")
		}

		// Final validation: ensure aa_sender was set
		vm.mu.Lock()
		_, hasSender := vm.vars["aa_sender"]
		vm.mu.Unlock()
		if !hasSender {
			if n.logger != nil {
				n.logger.Warn("RunNodeImmediately: Missing settings.runner for contractWrite - refusing to simulate without aa_sender")
			}
			return nil, fmt.Errorf("settings.runner is required for contractWrite")
		}
	}

	// Add input variables to VM for template processing and node access
	// Apply dual-access mapping to enable both camelCase and snake_case field access
	processedInputVariables := inputVariables
	for key, processedValue := range processedInputVariables {
		vm.AddVar(key, processedValue)
	}

	// Create node from type and config
	// NOTE: For CustomCode nodes, lang field can be either:
	// - avsproto.Lang enum (preferred in tests)
	// - lowercase strings like "javascript" (from SDK, handled by ParseLanguageFromConfig)
	node, err := CreateNodeFromType(nodeType, nodeConfig, "")
	if err != nil {
		return nil, fmt.Errorf("failed to create node: %w", err)
	}

	// Validate node name for JavaScript compatibility
	if err := model.ValidateNodeNameForJavaScript(node.Name); err != nil {
		return nil, fmt.Errorf("node name validation failed: %w", err)
	}

	// Execute the node with processed input variables
	executionStep, err := vm.RunNodeWithInputs(node, processedInputVariables)
	if err != nil {
		// Return the original error to preserve structured error codes
		return nil, err
	}

	// ALWAYS extract result (even for failed steps) to ensure consistent success/error fields
	result, extractErr := n.extractExecutionResult(executionStep)
	if extractErr != nil {
		return nil, extractErr
	}

	// For failed steps, still return the result (which now contains success=false, error=message)
	// but also return the original error for structured error codes
	if !executionStep.Success {
		return result, err
	}

	return result, nil
}

// LoadSecretsForImmediateExecution loads secrets for immediate node execution
// It loads global macroSecrets (no workflow-level secrets since there's no workflow)
// Note: User-level secrets are not currently supported, as this function does not accept a user parameter.
// Support for user-level secrets could be added in the future if needed.
func (n *Engine) LoadSecretsForImmediateExecution(inputVariables map[string]interface{}) (map[string]string, error) {
	secrets := make(map[string]string)

	// Copy global static secrets from macroSecrets (equivalent to copyMap(secrets, macroSecrets) in LoadSecretForTask)
	copyMap(secrets, macroSecrets)

	return secrets, nil
}

func (n *Engine) parseUint64(value interface{}) (uint64, error) {
	switch v := value.(type) {
	case uint64:
		return v, nil
	case int64:
		if v < 0 {
			return 0, fmt.Errorf("negative value cannot be converted to uint64: %d", v)
		}
		return uint64(v), nil
	case int:
		if v < 0 {
			return 0, fmt.Errorf("negative value cannot be converted to uint64: %d", v)
		}
		return uint64(v), nil
	case float64:
		if v < 0 {
			return 0, fmt.Errorf("negative value cannot be converted to uint64: %f", v)
		}
		return uint64(v), nil
	case string:
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("failed to parse string to uint64: %w", err)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported type for uint64 conversion: %T", value)
	}
}

// assignOutputData is a helper function that converts interface{} output from handlers
// to the correct protobuf oneof type using a type switch.
// This helper eliminates code duplication where the same type switch pattern appears multiple times.
func assignOutputData(resp *avsproto.RunNodeWithInputsResp, outputData interface{}) {
	switch v := outputData.(type) {
	case *avsproto.RunNodeWithInputsResp_RestApi:
		resp.OutputData = v
	case *avsproto.RunNodeWithInputsResp_CustomCode:
		resp.OutputData = v
	case *avsproto.RunNodeWithInputsResp_Balance:
		resp.OutputData = v
	case *avsproto.RunNodeWithInputsResp_ContractRead:
		resp.OutputData = v
	case *avsproto.RunNodeWithInputsResp_ContractWrite:
		resp.OutputData = v
	case *avsproto.RunNodeWithInputsResp_EthTransfer:
		resp.OutputData = v
	case *avsproto.RunNodeWithInputsResp_Graphql:
		resp.OutputData = v
	case *avsproto.RunNodeWithInputsResp_Branch:
		resp.OutputData = v
	case *avsproto.RunNodeWithInputsResp_Filter:
		resp.OutputData = v
	case *avsproto.RunNodeWithInputsResp_Loop:
		resp.OutputData = v
	}
}

// extractExecutionResult extracts the result data from an execution step using node-specific handlers
// This provides a clean, object-oriented approach that eliminates code duplication
func (n *Engine) extractExecutionResult(executionStep *avsproto.Execution_Step) (map[string]interface{}, error) {
	// Get node type from execution step
	nodeType := executionStep.Type
	if nodeType != "" {
		// Normal case: Convert protobuf enum string to internal node type constant
		// e.g., "NODE_TYPE_CONTRACT_WRITE" -> "contractWrite"
		nodeType = n.convertProtobufNodeTypeToInternal(nodeType)
	} else {
		// Fallback case: try to determine from output data when Type field is empty
		nodeType = n.detectNodeTypeFromStep(executionStep)
	}

	// Get the appropriate handler for this node type
	factory := NewNodeOutputHandlerFactory(n)
	handler, err := factory.GetHandler(nodeType)
	if err != nil {
		// Fallback to empty result for unknown node types
		result := make(map[string]interface{})
		result["success"] = executionStep.Success
		if executionStep.Error != "" {
			result["error"] = executionStep.Error
		}
		return result, nil
	}

	// Use the handler to extract data
	result, err := handler.ExtractFromExecutionStep(executionStep)
	if err != nil {
		return nil, fmt.Errorf("failed to extract execution result for node type %q: %w", nodeType, err)
	}

	// ALWAYS add step success/error fields for ALL node types (general approach)
	// This ensures consistent behavior across runNodeImmediately, runTrigger, and simulateTask
	result["success"] = executionStep.Success
	if executionStep.Error != "" {
		result["error"] = executionStep.Error
	}

	// Preserve executionContext from step if available
	if executionStep.ExecutionContext != nil {
		result["executionContext"] = executionStep.ExecutionContext
	}

	return result, nil
}

// convertProtobufNodeTypeToInternal converts protobuf enum string names to internal node type constants
// e.g., "NODE_TYPE_CONTRACT_WRITE" -> "contractWrite"
func (n *Engine) convertProtobufNodeTypeToInternal(protobufNodeType string) string {
	switch protobufNodeType {
	case "NODE_TYPE_CONTRACT_WRITE":
		return NodeTypeContractWrite
	case "NODE_TYPE_CONTRACT_READ":
		return NodeTypeContractRead
	case "NODE_TYPE_REST_API":
		return NodeTypeRestAPI
	case "NODE_TYPE_CUSTOM_CODE":
		return NodeTypeCustomCode
	case "NODE_TYPE_ETH_TRANSFER":
		return NodeTypeETHTransfer
	case "NODE_TYPE_GRAPHQL_QUERY":
		return NodeTypeGraphQLQuery
	case "NODE_TYPE_BRANCH":
		return NodeTypeBranch
	case "NODE_TYPE_FILTER":
		return NodeTypeFilter
	case "NODE_TYPE_LOOP":
		return NodeTypeLoop
	case "NODE_TYPE_BALANCE":
		return NodeTypeBalance
	default:
		// If it's not a protobuf enum string, return as-is (it might already be the internal constant)
		return protobufNodeType
	}
}

// detectNodeTypeFromStep detects the node type from the execution step's output data
// This is a fallback for cases where the Type field is not set
func (n *Engine) detectNodeTypeFromStep(step *avsproto.Execution_Step) string {
	switch {
	case step.GetCustomCode() != nil:
		return NodeTypeCustomCode
	case step.GetRestApi() != nil:
		return NodeTypeRestAPI
	case step.GetContractRead() != nil:
		return NodeTypeContractRead
	case step.GetContractWrite() != nil:
		return NodeTypeContractWrite
	case step.GetEthTransfer() != nil:
		return NodeTypeETHTransfer
	case step.GetGraphql() != nil:
		return NodeTypeGraphQLQuery
	case step.GetBranch() != nil:
		return NodeTypeBranch
	case step.GetFilter() != nil:
		return NodeTypeFilter
	case step.GetLoop() != nil:
		return NodeTypeLoop
	case step.GetBalance() != nil:
		return NodeTypeBalance
	default:
		return ""
	}
}

// RunNodeImmediatelyRPC handles the RPC interface for immediate node execution
func (n *Engine) RunNodeImmediatelyRPC(user *model.User, req *avsproto.RunNodeWithInputsReq) (*avsproto.RunNodeWithInputsResp, error) {
	// The request now contains a complete TaskNode, consistent with SimulateTask
	node := req.Node
	if node == nil {
		return &avsproto.RunNodeWithInputsResp{
			Success:   false,
			Error:     "node is required",
			ErrorCode: avsproto.ErrorCode_INVALID_REQUEST,
			OutputData: &avsproto.RunNodeWithInputsResp_RestApi{
				RestApi: &avsproto.RestAPINode_Output{},
			},
		}, nil
	}

	// Convert input variables from protobuf to Go map
	inputVariables := make(map[string]interface{})
	for k, v := range req.InputVariables {
		inputVariables[k] = v.AsInterface()
	}

	// Get node type string from the node's Type field
	nodeTypeStr := NodeTypeToString(node.Type)
	if nodeTypeStr == "" {
		// For unsupported node types, return error but still set output data to avoid OUTPUT_DATA_NOT_SET
		resp := &avsproto.RunNodeWithInputsResp{
			Success: false,
			Error:   fmt.Sprintf("unsupported node type: %v", node.Type),
		}
		// Set default RestAPI output structure to avoid OUTPUT_DATA_NOT_SET
		resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{
			RestApi: &avsproto.RestAPINode_Output{},
		}
		return resp, nil
	}

	// Extract node configuration from the TaskNode protobuf
	// This uses ExtractNodeConfiguration to get a properly typed config map with all fields
	// (including value and gasLimit for ContractWrite nodes)
	nodeConfig := ExtractNodeConfiguration(node)
	if nodeConfig == nil {
		return &avsproto.RunNodeWithInputsResp{
			Success:   false,
			Error:     "failed to extract node configuration",
			ErrorCode: avsproto.ErrorCode_INVALID_REQUEST,
			OutputData: &avsproto.RunNodeWithInputsResp_RestApi{
				RestApi: &avsproto.RestAPINode_Output{},
			},
		}, nil
	}

	// Extract isSimulated from node config (defaults to true if not specified)
	// The runNodeImmediately simulation mode should solely depend on the node input
	useSimulation := true // Default to simulation mode
	if isSimulatedVal, ok := nodeConfig["isSimulated"]; ok {
		if isSimBool, ok := isSimulatedVal.(bool); ok {
			useSimulation = isSimBool
		}
	}

	// Execute the node immediately with authenticated user
	// NOTE: lang field conversion for CustomCode is handled by ParseLanguageFromConfig
	// Pass the isSimulated flag from node config to control simulation/real execution
	result, err := n.RunNodeImmediately(nodeTypeStr, nodeConfig, inputVariables, user, useSimulation)
	if err != nil {
		if n.logger != nil {
			if isExpectedValidationError(err) {
				n.logger.Warn("RunNodeImmediatelyRPC: Validation failed", "nodeType", nodeTypeStr, "error", err.Error())
			} else {
				n.logger.Error("RunNodeImmediatelyRPC: System error during execution", "nodeType", nodeTypeStr, "error", err.Error())
			}
		}

		// Build structured failure metadata for method-level nodes (contract read/write)
		resp := &avsproto.RunNodeWithInputsResp{
			Success:   false,
			Error:     err.Error(),
			ErrorCode: GetErrorCodeForProtobuf(err),
		}

		// Use handler to create empty output structure
		factory := NewNodeOutputHandlerFactory(n)
		handler, handlerErr := factory.GetHandler(nodeTypeStr)
		if handlerErr != nil {
			// Fallback to RestAPI for unknown node types
			resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{RestApi: &avsproto.RestAPINode_Output{}}
		} else {
			// Use helper to convert handler output to correct protobuf oneof type
			assignOutputData(resp, handler.CreateEmptyOutput())
		}

		return resp, nil
	}

	// Log successful execution (success determined by node execution)
	if n.logger != nil {
		n.logger.Info("RunNodeImmediatelyRPC: Executed successfully", "nodeTypeStr", nodeTypeStr, "nodeType", node.Type)
	}

	// Convert result to the appropriate protobuf output type
	// Success/Error are already encoded inside 'result' for immediate execution path

	// Extract success/error from result (now consistently populated by extractExecutionResult for all node types)
	var responseSuccess bool = true
	var responseError string = ""

	if result != nil {
		if successVal, ok := result["success"]; ok {
			if successBool, ok := successVal.(bool); ok {
				responseSuccess = successBool
			}
		}
		if errorVal, hasError := result["error"]; hasError {
			if errorStr, ok := errorVal.(string); ok && errorStr != "" {
				responseError = errorStr
			}
		}

	}

	resp := &avsproto.RunNodeWithInputsResp{
		Success:   responseSuccess,
		Error:     responseError,
		ErrorCode: avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED, // Default for successful operations
	}

	// Use handler to convert result to protobuf output
	factory := NewNodeOutputHandlerFactory(n)
	handler, err := factory.GetHandler(nodeTypeStr)
	if err != nil {
		// Fallback to RestAPI for unknown node types
		resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{RestApi: &avsproto.RestAPINode_Output{}}
	} else {
		outputData, metadata, err := handler.ConvertToProtobuf(result)
		if err != nil {
			return &avsproto.RunNodeWithInputsResp{
				Success: false,
				Error:   err.Error(),
			}, nil
		}
		// Use helper to convert handler output to correct protobuf oneof type
		assignOutputData(resp, outputData)
		if metadata != nil {
			resp.Metadata = metadata
		}
	}

	// Special handling for ContractWrite: align top-level success with method outcomes
	if nodeTypeStr == NodeTypeContractWrite && resp.Metadata != nil {
		meta := resp.Metadata.AsInterface()
		if metaArr, ok := meta.([]interface{}); ok {
			for _, item := range metaArr {
				if m, ok := item.(map[string]interface{}); ok {
					if succ, ok := m["success"].(bool); ok && !succ {
						resp.Success = false
						break
					}
					if rec, ok := m["receipt"].(map[string]interface{}); ok {
						if status, ok := rec["status"].(string); ok && strings.EqualFold(status, "0x0") {
							resp.Success = false
							break
						}
					}
				}
			}
		}
	}

	// Attach execution_context from step if available, otherwise use defaults
	// Skip for EventTrigger since it provides its own executionContext in metadata
	if nodeTypeStr != NodeTypeEventTrigger {
		// Check if result already has executionContext from step
		var ctxFromStep *structpb.Value
		if result != nil {
			if ctxVal, ok := result["executionContext"]; ok {
				if ctxProto, ok := ctxVal.(*structpb.Value); ok {
					ctxFromStep = ctxProto
				}
			}
		}

		// Use step's execution context if available; otherwise create default
		if ctxFromStep != nil {
			resp.ExecutionContext = ctxFromStep
		} else {
			// Fallback to default context
			ctxMap := map[string]interface{}{
				"is_simulated": nil,
				"provider":     string(ProviderChainRPC),
			}
			if n.smartWalletConfig != nil && n.smartWalletConfig.ChainID != 0 {
				ctxMap["chain_id"] = n.smartWalletConfig.ChainID
			}
			if ctxVal, err := structpb.NewValue(ctxMap); err == nil {
				resp.ExecutionContext = ctxVal
			}
		}
	}

	// For failed operations, set error code based on failure type
	if !resp.Success {
		// Check if error indicates bundler/RPC connectivity issues
		if strings.Contains(resp.Error, "connection refused") ||
			strings.Contains(resp.Error, "dial tcp") ||
			strings.Contains(resp.Error, "Bundler service unavailable") {
			resp.ErrorCode = avsproto.ErrorCode_RPC_NODE_ERROR // 5000: External service unavailable
		} else if resp.ErrorCode == avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED {
			// Fallback for generic failures
			resp.ErrorCode = avsproto.ErrorCode_INVALID_REQUEST // 3000: Generic validation failure
		}
		// Otherwise keep the error code set by the step/handler
	}

	return resp, nil
}

// RunTriggerRPC handles the RPC interface for immediate trigger execution
func (n *Engine) RunTriggerRPC(user *model.User, req *avsproto.RunTriggerReq) (*avsproto.RunTriggerResp, error) {
	// Validate that trigger is provided
	if req.Trigger == nil {
		resp := &avsproto.RunTriggerResp{
			Success: false,
			Error:   "trigger is required",
		}
		resp.OutputData = &avsproto.RunTriggerResp_ManualTrigger{
			ManualTrigger: &avsproto.ManualTrigger_Output{},
		}
		return resp, nil
	}

	// Extract trigger configuration from TaskTrigger using existing helper
	triggerConfig := ExtractTriggerConfigData(req.Trigger)
	if triggerConfig == nil {
		resp := &avsproto.RunTriggerResp{
			Success: false,
			Error:   "failed to extract trigger configuration",
		}
		resp.OutputData = &avsproto.RunTriggerResp_ManualTrigger{
			ManualTrigger: &avsproto.ManualTrigger_Output{},
		}
		return resp, nil
	}

	// Extract trigger input data from the request
	triggerInput := make(map[string]interface{})
	for k, v := range req.TriggerInput {
		triggerInput[k] = v.AsInterface()
	}

	// Convert TriggerType enum to string
	triggerTypeStr := TriggerTypeToString(req.Trigger.Type)
	if triggerTypeStr == "" {
		// For unsupported trigger types, return error but still set output data to avoid OUTPUT_DATA_NOT_SET
		resp := &avsproto.RunTriggerResp{
			Success: false,
			Error:   fmt.Sprintf("unsupported trigger type: %v", req.Trigger.Type),
		}
		// Set default ManualTrigger output structure to avoid OUTPUT_DATA_NOT_SET
		resp.OutputData = &avsproto.RunTriggerResp_ManualTrigger{
			ManualTrigger: &avsproto.ManualTrigger_Output{},
		}
		return resp, nil
	}

	// Execute the trigger immediately with trigger input data
	// NOTE: lang field conversion for ManualTrigger is handled by ParseLanguageFromConfig
	result, err := n.runTriggerImmediately(triggerTypeStr, triggerConfig, triggerInput)
	if err != nil {
		if n.logger != nil {
			// Categorize errors to avoid unnecessary stack traces for expected validation errors
			if isExpectedValidationError(err) {
				// Expected validation errors - log at WARN level without stack traces
				n.logger.Warn("RunTriggerRPC: Validation failed", "triggerType", triggerTypeStr, "error", err.Error())
			} else {
				// Unexpected system errors - log at ERROR level without stack traces for cleaner output
				n.logger.Error("RunTriggerRPC: System error during execution", "triggerType", triggerTypeStr, "error", err.Error())
			}
		}

		// Create response with failure status but still set appropriate output data structure
		// to avoid OUTPUT_DATA_NOT_SET errors on client side
		resp := &avsproto.RunTriggerResp{
			Success:   false,
			Error:     err.Error(),
			ErrorCode: GetErrorCodeForProtobuf(err),
		}

		// Set empty output data structure based on trigger type to avoid OUTPUT_DATA_NOT_SET
		switch triggerTypeStr {
		case NodeTypeBlockTrigger:
			resp.OutputData = &avsproto.RunTriggerResp_BlockTrigger{
				BlockTrigger: &avsproto.BlockTrigger_Output{},
			}
		case NodeTypeFixedTimeTrigger:
			resp.OutputData = &avsproto.RunTriggerResp_FixedTimeTrigger{
				FixedTimeTrigger: &avsproto.FixedTimeTrigger_Output{},
			}
		case NodeTypeCronTrigger:
			resp.OutputData = &avsproto.RunTriggerResp_CronTrigger{
				CronTrigger: &avsproto.CronTrigger_Output{},
			}
		case NodeTypeEventTrigger:
			resp.OutputData = &avsproto.RunTriggerResp_EventTrigger{
				EventTrigger: &avsproto.EventTrigger_Output{},
			}
		case NodeTypeManualTrigger:
			resp.OutputData = &avsproto.RunTriggerResp_ManualTrigger{
				ManualTrigger: &avsproto.ManualTrigger_Output{},
			}
		default:
			// For unknown trigger types, set ManualTrigger as default to avoid OUTPUT_DATA_NOT_SET
			resp.OutputData = &avsproto.RunTriggerResp_ManualTrigger{
				ManualTrigger: &avsproto.ManualTrigger_Output{},
			}
		}

		return resp, nil
	}

	// Log successful execution
	if n.logger != nil {
		n.logger.Info("RunTriggerRPC: Executed successfully", "triggerTypeStr", triggerTypeStr, "originalTriggerType", req.Trigger.Type)
	}

	// Convert result to the appropriate protobuf output type
	// Extract success status from trigger result (default to true if not specified)
	triggerSuccess := true
	if result != nil {
		if successValue, hasSuccess := result["success"]; hasSuccess {
			if successBool, ok := successValue.(bool); ok {
				triggerSuccess = successBool
			}
		}
	}

	resp := &avsproto.RunTriggerResp{
		Success: triggerSuccess,
	}

	// Extract error message from trigger result if success is false
	if !triggerSuccess && result != nil {
		if errorValue, hasError := result["error"]; hasError {
			if errorStr, ok := errorValue.(string); ok && errorStr != "" {
				resp.Error = errorStr
			}
		}
	}

	// Set the appropriate output data based on the trigger type using shared functions
	switch triggerTypeStr {
	case NodeTypeBlockTrigger:
		// For block triggers - always set output structure to avoid OUTPUT_DATA_NOT_SET
		if result != nil {
			blockOutput := buildBlockTriggerOutput(result)
			resp.OutputData = &avsproto.RunTriggerResp_BlockTrigger{
				BlockTrigger: blockOutput,
			}
		} else {
			// Set empty block trigger output for no result
			resp.OutputData = &avsproto.RunTriggerResp_BlockTrigger{
				BlockTrigger: &avsproto.BlockTrigger_Output{},
			}
		}
	case NodeTypeFixedTimeTrigger:
		// For fixed time triggers - always set output structure to avoid OUTPUT_DATA_NOT_SET
		if result != nil {
			fixedTimeOutput := buildFixedTimeTriggerOutput(result)
			resp.OutputData = &avsproto.RunTriggerResp_FixedTimeTrigger{
				FixedTimeTrigger: fixedTimeOutput,
			}
		} else {
			// Set empty fixed time trigger output for no result
			resp.OutputData = &avsproto.RunTriggerResp_FixedTimeTrigger{
				FixedTimeTrigger: &avsproto.FixedTimeTrigger_Output{},
			}
		}
	case NodeTypeCronTrigger:
		// For cron triggers - always set output structure to avoid OUTPUT_DATA_NOT_SET
		cronData := map[string]interface{}{}
		if result != nil {
			// Set actual cron trigger result data
			if timestamp, ok := result["timestamp"].(uint64); ok {
				cronData["timestamp"] = timestamp
			}
			if timestampISO, ok := result["timestamp_iso"].(string); ok {
				cronData["timestampIso"] = timestampISO
			}
		} else {
			// Set empty values for no result
			cronData["timestamp"] = uint64(0)
			cronData["timestampIso"] = ""
		}

		// Convert to protobuf Value
		dataValue, err := structpb.NewValue(cronData)
		if err != nil {
			// Fallback to empty data on error
			dataValue, _ = structpb.NewValue(map[string]interface{}{})
		}

		cronOutput := &avsproto.CronTrigger_Output{
			Data: dataValue,
		}
		resp.OutputData = &avsproto.RunTriggerResp_CronTrigger{
			CronTrigger: cronOutput,
		}
	case NodeTypeEventTrigger:
		// Use shared function to build EventTrigger output (handles nil result gracefully)
		eventOutput := buildEventTriggerOutput(result)
		resp.OutputData = &avsproto.RunTriggerResp_EventTrigger{
			EventTrigger: eventOutput,
		}

		// Add metadata for runTrigger (debugging/testing) - use shared function
		if result != nil {
			// Use shared function to extract metadata
			if metadata := extractTriggerMetadata(result); metadata != nil {

				// Convert metadata to be compatible with protobuf
				compatibleMetadata := convertToProtobufCompatible(metadata)

				if metadataValue, err := structpb.NewValue(compatibleMetadata); err == nil {
					resp.Metadata = metadataValue
				} else if n.logger != nil {
					n.logger.Error("Failed to convert metadata to protobuf", "error", err)
				}
			}
		}
	case NodeTypeManualTrigger:
		// Always set manual trigger output, even if result is nil
		manualOutput := &avsproto.ManualTrigger_Output{}
		if result != nil {
			// Include ONLY the user-defined data - this is the main payload for manual triggers
			// Headers and pathParams are config-only fields, not output fields
			if dataValue, exists := result["data"]; exists {
				if pbValue, err := structpb.NewValue(dataValue); err == nil {
					manualOutput.Data = pbValue
				}
			}
		}
		resp.OutputData = &avsproto.RunTriggerResp_ManualTrigger{
			ManualTrigger: manualOutput,
		}
	}

	// Attach execution_context
	// For EventTrigger, use the trigger's own executionContext if available
	// For other triggers, use the generic RPC wrapper context
	var ctxMap map[string]interface{}
	if triggerTypeStr == "eventTrigger" && result != nil {
		if execCtx, hasExecCtx := result["executionContext"]; hasExecCtx {
			if execCtxMap, ok := execCtx.(map[string]interface{}); ok {
				ctxMap = execCtxMap
			}
		}
	}

	// Fallback to generic RPC wrapper context if no specific context found
	if ctxMap == nil {
		// Trigger operations themselves are not simulated, so isSimulated should be false
		ctxMap = GetExecutionContext(n.smartWalletConfig.ChainID, false)
	}

	if ctxVal, err := structpb.NewValue(ctxMap); err == nil {
		resp.ExecutionContext = ctxVal
	}

	return resp, nil
}

// isExpectedValidationError determines if an error is an expected validation error
// that should be logged at WARN level without stack traces, vs an unexpected system error
// that should be logged at ERROR level with stack traces
func isExpectedValidationError(err error) bool {
	if err == nil {
		return false
	}

	errorMsg := err.Error()

	// Common validation error patterns that should be logged as WARN (no stack traces)
	validationErrorPatterns := []string{
		"missing required input variables:",
		"destination address is required",
		"unsupported node type for CreateNodeFromType:",
		"invalid node type:",
		"missing required configuration:",
		"invalid configuration:",
		"missing required field:",
		"validation failed:",
		"invalid input:",
		"parameter validation failed:",
		"node configuration error:",
		"template processing failed:",
		"unknown node type for node ID",                     // Filter node execution errors
		"branch node requires conditionsList configuration", // Branch node configuration errors
		"failed to create node:",                            // Node creation errors
		"ManualTrigger data is required",                    // ManualTrigger data validation errors
		"methodCalls[].methodName is required",              // Contract read method name validation
		"contractAddress is required",                       // Contract address validation
		"contractAbi is required",                           // Contract ABI validation
		"methodCalls is required",                           // Method calls validation
		"inputNodeName is required",                         // Loop node input validation
		"iterVal is required",                               // Loop node iteration variable validation
		"url is required",                                   // REST API/GraphQL URL validation
		"query is required",                                 // GraphQL query validation
		"source is required",                                // CustomCode source validation
		"does not match any existing smart wallet",          // Smart wallet validation
		"queries is required",                               // EventTrigger queries validation
		"addresses is required",                             // EventTrigger addresses validation
		"contractAbi is required",                           // EventTrigger contractAbi validation
		"invalid query format",                              // EventTrigger query format validation
		"invalid addresses format",                          // EventTrigger addresses format validation
		"invalid contractAbi format",                        // EventTrigger ABI format validation
		"settings is required for contractWrite",            // ContractWrite settings validation
		"settings.chain_id is required for contractWrite",   // ContractWrite chain_id validation
	}

	for _, pattern := range validationErrorPatterns {
		if strings.Contains(errorMsg, pattern) {
			return true
		}
	}

	// If it doesn't match validation patterns, treat as system error
	return false
}

// convertMapToEventQuery converts a map-based query to protobuf EventTrigger_Query
func (n *Engine) convertMapToEventQuery(queryMap map[string]interface{}) (*avsproto.EventTrigger_Query, error) {
	query := &avsproto.EventTrigger_Query{}

	// Extract addresses
	if addressesInterface, exists := queryMap["addresses"]; exists {
		if addressesArray, ok := addressesInterface.([]interface{}); ok {
			addresses := make([]string, 0, len(addressesArray))
			for _, addrInterface := range addressesArray {
				if addrStr, ok := addrInterface.(string); ok && addrStr != "" {
					addresses = append(addresses, addrStr)
				}
			}
			query.Addresses = addresses
		}
	}

	// Extract topics - flat array format
	// Client sends: topics: ['sig', 'from', 'to']
	if topicsInterface, exists := queryMap["topics"]; exists {
		if topicsArray, ok := topicsInterface.([]interface{}); ok {
			topics := make([]string, 0, len(topicsArray))
			for _, valueInterface := range topicsArray {
				if valueStr, ok := valueInterface.(string); ok {
					topics = append(topics, valueStr)
				} else if valueInterface == nil {
					// Handle null values as empty strings
					topics = append(topics, "")
				}
			}
			query.Topics = topics
		}
	}

	// Extract contract ABI if present - must be an array like ContractRead
	if abiInterface, exists := queryMap["contractAbi"]; exists {
		if abiArray, ok := abiInterface.([]interface{}); ok {
			// Convert array directly to protobuf Values (same as ContractRead)
			if abiValues, err := ConvertInterfaceArrayToProtobufValues(abiArray); err == nil {
				query.ContractAbi = abiValues
			} else {
				return nil, fmt.Errorf("failed to convert contractAbi array to protobuf values: %v", err)
			}
		} else {
			// Strictly reject non-array contractAbi
			return nil, fmt.Errorf("contractAbi must be an array of ABI elements, got %T", abiInterface)
		}
	}

	// Extract conditions if present
	if conditionsInterface, exists := queryMap["conditions"]; exists {
		if conditionsArray, ok := conditionsInterface.([]interface{}); ok {
			for _, conditionInterface := range conditionsArray {
				if conditionMap, ok := conditionInterface.(map[string]interface{}); ok {
					condition := &avsproto.EventCondition{}
					if fieldName, ok := conditionMap["fieldName"].(string); ok {
						condition.FieldName = fieldName
					}
					if operator, ok := conditionMap["operator"].(string); ok {
						condition.Operator = operator
					}
					if value, ok := conditionMap["value"].(string); ok {
						condition.Value = value
					}
					if fieldType, ok := conditionMap["fieldType"].(string); ok {
						condition.FieldType = fieldType
					}
					query.Conditions = append(query.Conditions, condition)
				}
			}
		}
	}

	// Extract method calls if present
	if methodCallsInterface, exists := queryMap["methodCalls"]; exists {
		if methodCallsArray, ok := methodCallsInterface.([]interface{}); ok {
			for _, methodCallInterface := range methodCallsArray {
				if methodCallMap, ok := methodCallInterface.(map[string]interface{}); ok {
					methodCall := &avsproto.EventTrigger_MethodCall{}

					if methodName, ok := methodCallMap["methodName"].(string); ok {
						methodCall.MethodName = methodName
					}
					if callData, ok := methodCallMap["callData"].(string); ok {
						methodCall.CallData = &callData
					}
					// Handle methodParams field as string array
					if methodParamsInterface, ok := methodCallMap["methodParams"]; ok {
						if methodParamsArray, ok := methodParamsInterface.([]interface{}); ok {
							methodParams := make([]string, len(methodParamsArray))
							for i, param := range methodParamsArray {
								if paramStr, ok := param.(string); ok {
									methodParams[i] = paramStr
								}
							}
							methodCall.MethodParams = methodParams
						}
					}
					if applyToFieldsInterface, exists := methodCallMap["applyToFields"]; exists {
						if applyToFieldsArray, ok := applyToFieldsInterface.([]interface{}); ok {
							applyToFields := make([]string, 0, len(applyToFieldsArray))
							for _, fieldInterface := range applyToFieldsArray {
								if fieldStr, ok := fieldInterface.(string); ok {
									applyToFields = append(applyToFields, fieldStr)
								}
							}
							methodCall.ApplyToFields = applyToFields
						}
					}
					query.MethodCalls = append(query.MethodCalls, methodCall)
				}
			}
		}
	}

	// Extract maxEventsPerBlock if present
	if maxEventsInterface, exists := queryMap["maxEventsPerBlock"]; exists {
		if maxEventsFloat, ok := maxEventsInterface.(float64); ok {
			maxEventsPerBlock := uint32(maxEventsFloat)
			query.MaxEventsPerBlock = &maxEventsPerBlock
		}
	}

	return query, nil
}

// evaluateConditionsAgainstEventDataWithDecimalContext evaluates conditions with decimal formatting context
func (n *Engine) evaluateConditionsAgainstEventDataWithDecimalContext(eventData map[string]interface{}, conditionsArray []interface{}, formattingContext *DecimalFormattingContext) bool {
	for _, conditionInterface := range conditionsArray {
		conditionMap, ok := conditionInterface.(map[string]interface{})
		if !ok {
			continue
		}

		fieldName, _ := conditionMap["fieldName"].(string)
		operator, _ := conditionMap["operator"].(string)
		expectedValue, _ := conditionMap["value"].(string)
		fieldType, _ := conditionMap["fieldType"].(string)

		// Get the actual field value from structured event data
		var actualValue interface{}
		var exists bool

		if strings.Contains(fieldName, ".") {
			parts := strings.Split(fieldName, ".")
			if len(parts) == 2 {
				eventName := parts[0]
				fieldNameOnly := parts[1]

				// Look for the event in the structured data
				if eventMap, eventExists := eventData[eventName].(map[string]interface{}); eventExists {
					actualValue, exists = eventMap[fieldNameOnly]
				}
			}
		} else {
			// For simple field names, check all events for the field
			for _, eventFields := range eventData {
				if eventFieldsMap, ok := eventFields.(map[string]interface{}); ok {
					if value, fieldExists := eventFieldsMap[fieldName]; fieldExists {
						actualValue = value
						exists = true
						break
					}
				}
			}
		}

		if !exists {
			if n.logger != nil {
				n.logger.Debug("Condition field not found in structured event data",
					"fieldName", fieldName,
					"availableEvents", GetMapKeys(eventData))
			}
			return false
		}

		// DECIMAL FORMATTING FIX: Apply consistent formatting for condition comparison
		var processedExpectedValue string = expectedValue
		if fieldType == "decimal" && formattingContext != nil {
			// Format the expected value to match the actual value's formatting
			processedExpectedValue = formattingContext.FormatConditionValueForComparison(fieldName, expectedValue)
			if n.logger != nil {
				n.logger.Info("✅ DECIMAL CONDITION FIX: Formatted condition value for consistent comparison (direct calls)",
					"fieldName", fieldName,
					"originalExpectedValue", expectedValue,
					"formattedExpectedValue", processedExpectedValue,
					"actualValue", actualValue,
					"fieldType", fieldType)
			}
		}

		// Evaluate condition based on field type using processed expected value
		conditionMet := false
		switch fieldType {
		case "int256":
			conditionMet = n.evaluateInt256Condition(actualValue, operator, processedExpectedValue)
		case "uint256":
			conditionMet = n.evaluateUint256Condition(actualValue, operator, processedExpectedValue)
		case "decimal":
			// Treat decimal as int256 for comparison with formatted expected value
			conditionMet = n.evaluateInt256Condition(actualValue, operator, processedExpectedValue)
		default:
			if n.logger != nil {
				n.logger.Warn("Unsupported field type for condition evaluation",
					"fieldType", fieldType,
					"fieldName", fieldName)
			}
			return false
		}

		if !conditionMet {
			return false
		}
	}
	return true
}

// evaluateConditionsAgainstEventData evaluates conditions against parsed event data
func (n *Engine) evaluateConditionsAgainstEventData(eventData map[string]interface{}, conditionsArray []interface{}) bool {
	for _, conditionInterface := range conditionsArray {
		conditionMap, ok := conditionInterface.(map[string]interface{})
		if !ok {
			continue
		}

		fieldName, _ := conditionMap["fieldName"].(string)
		operator, _ := conditionMap["operator"].(string)
		expectedValue, _ := conditionMap["value"].(string)
		fieldType, _ := conditionMap["fieldType"].(string)

		// Get the actual field value from structured event data
		// Handle eventName.fieldName format for structured data
		var actualValue interface{}
		var exists bool

		if strings.Contains(fieldName, ".") {
			parts := strings.Split(fieldName, ".")
			if len(parts) == 2 {
				eventName := parts[0]
				fieldNameOnly := parts[1]

				// Look for the event in the structured data
				if eventMap, eventExists := eventData[eventName].(map[string]interface{}); eventExists {
					actualValue, exists = eventMap[fieldNameOnly]
				}
			}
		} else {
			// For simple field names, check all events for the field
			for _, eventFields := range eventData {
				if eventFieldsMap, ok := eventFields.(map[string]interface{}); ok {
					if value, fieldExists := eventFieldsMap[fieldName]; fieldExists {
						actualValue = value
						exists = true
						break
					}
				}
			}
		}

		if !exists {
			if n.logger != nil {
				n.logger.Debug("Condition field not found in structured event data",
					"fieldName", fieldName,
					"availableEvents", GetMapKeys(eventData))
			}
			return false
		}

		// Evaluate condition based on field type
		conditionMet := false
		switch fieldType {
		case "int256":
			conditionMet = n.evaluateInt256Condition(actualValue, operator, expectedValue)
		case "uint256":
			conditionMet = n.evaluateUint256Condition(actualValue, operator, expectedValue)
		case "decimal":
			// Treat decimal as int256 for comparison
			conditionMet = n.evaluateInt256Condition(actualValue, operator, expectedValue)
		default:
			if n.logger != nil {
				n.logger.Warn("Unsupported field type for condition evaluation",
					"fieldType", fieldType,
					"fieldName", fieldName)
			}
			return false
		}

		if !conditionMet {
			return false
		}
	}
	return true
}

// evaluateInt256Condition evaluates int256 field conditions
func (n *Engine) evaluateInt256Condition(actualValue interface{}, operator, expectedValue string) bool {
	// Convert actual value to big.Int
	var actualBig *big.Int
	switch v := actualValue.(type) {
	case string:
		actualBig, _ = new(big.Int).SetString(v, 10)
	case int64:
		actualBig = big.NewInt(v)
	case *big.Int:
		actualBig = v
	default:
		return false
	}

	if actualBig == nil {
		return false
	}

	// Convert expected value to big.Int
	expectedBig, ok := new(big.Int).SetString(expectedValue, 10)
	if !ok {
		return false
	}

	// Compare based on operator
	var result bool
	switch operator {
	case "lt":
		result = actualBig.Cmp(expectedBig) < 0
	case "gt":
		result = actualBig.Cmp(expectedBig) > 0
	case "eq":
		result = actualBig.Cmp(expectedBig) == 0
	case "lte":
		result = actualBig.Cmp(expectedBig) <= 0
	case "gte":
		result = actualBig.Cmp(expectedBig) >= 0
	case "ne":
		result = actualBig.Cmp(expectedBig) != 0
	default:
		return false
	}

	return result
}

// evaluateUint256Condition evaluates uint256 field conditions
func (n *Engine) evaluateUint256Condition(actualValue interface{}, operator, expectedValue string) bool {
	// Reuse int256 logic since big.Int handles both
	return n.evaluateInt256Condition(actualValue, operator, expectedValue)
}

// evaluateEventConditions checks if event log data satisfies the provided conditions
// This function uses the ABI-based condition evaluation from the trigger package
func (n *Engine) evaluateEventConditions(eventLog *types.Log, conditions []*avsproto.EventCondition) bool {
	// For now, use a simple implementation that works with the existing condition format
	// This can be enhanced to use the full ABI-based evaluation later
	for _, condition := range conditions {
		if condition.GetFieldName() == "current" {
			// For AnswerUpdated events, current price is in Topics[1]
			if len(eventLog.Topics) >= 2 {
				currentPrice := eventLog.Topics[1].Big()
				expectedValue, ok := new(big.Int).SetString(condition.GetValue(), 10)
				if !ok {
					continue
				}

				conditionMet := false
				switch condition.GetOperator() {
				case "gt":
					conditionMet = currentPrice.Cmp(expectedValue) > 0
				case "lt":
					conditionMet = currentPrice.Cmp(expectedValue) < 0
				case "eq":
					conditionMet = currentPrice.Cmp(expectedValue) == 0
				case "gte":
					conditionMet = currentPrice.Cmp(expectedValue) >= 0
				case "lte":
					conditionMet = currentPrice.Cmp(expectedValue) <= 0
				case "ne":
					conditionMet = currentPrice.Cmp(expectedValue) != 0
				}

				if !conditionMet {
					if n.logger != nil {
						n.logger.Debug("EventTrigger condition not met",
							"field", condition.GetFieldName(),
							"operator", condition.GetOperator(),
							"expected", condition.GetValue(),
							"actual", currentPrice.String())
					}
					return false
				}
			}
		}
		// Add more field types here as needed (roundId, updatedAt, etc.)
	}
	return true
}

// convertToProtobufCompatible converts data structures to be compatible with structpb.NewValue()
// This handles cases like []string which structpb.NewValue() cannot handle directly
func convertToProtobufCompatible(data interface{}) interface{} {
	switch v := data.(type) {
	case []common.Hash:
		// Convert []common.Hash to []interface{} of strings
		result := make([]interface{}, len(v))
		for i, h := range v {
			result[i] = h.Hex()
		}
		return result
	case []string:
		// Convert []string to []interface{}
		result := make([]interface{}, len(v))
		for i, s := range v {
			result[i] = s
		}
		return result
	case map[string]interface{}:
		// Recursively convert map values
		result := make(map[string]interface{})
		for k, val := range v {
			result[k] = convertToProtobufCompatible(val)
		}
		return result
	case []interface{}:
		// Recursively convert slice elements
		result := make([]interface{}, len(v))
		for i, val := range v {
			result[i] = convertToProtobufCompatible(val)
		}
		return result
	case []ConditionResult:
		// Convert []ConditionResult to []interface{} of maps
		result := make([]interface{}, len(v))
		for i, cr := range v {
			result[i] = map[string]interface{}{
				"fieldName":     cr.FieldName,
				"operator":      cr.Operator,
				"expectedValue": cr.ExpectedValue,
				"actualValue":   cr.ActualValue,
				"passed":        cr.Passed,
				"reason":        cr.Reason,
			}
		}
		return result
	default:
		// Return as-is for basic types (string, int, float, bool, etc.)
		return v
	}
}
