package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"google.golang.org/protobuf/types/known/structpb"
)

// RunNodeImmediately executes a single node immediately for testing/simulation purposes.
// This is different from workflow execution - it runs the node right now, ignoring any scheduling.
func (n *Engine) RunNodeImmediately(nodeType string, nodeConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	if IsTriggerNodeType(nodeType) {
		return n.runTriggerImmediately(nodeType, nodeConfig, inputVariables)
	} else {
		return n.runProcessingNodeWithInputs(nodeType, nodeConfig, inputVariables)
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
		if blockNum, err := n.parseUint64(configBlockNumber); err == nil {
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
			return nil, fmt.Errorf("failed to get current block number from RPC: %w", err)
		}
		blockNumber = currentBlock

		if n.logger != nil {
			n.logger.Info("BlockTrigger: Using latest block for immediate execution", "blockNumber", blockNumber)
		}
	}

	// Get real block data from RPC
	header, err := rpcConn.HeaderByNumber(context.Background(), big.NewInt(int64(blockNumber)))
	if err != nil {
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

// runEventTriggerImmediately executes an event trigger immediately using the new queries-based system
func (n *Engine) runEventTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// Ensure RPC connection is available
	if rpcConn == nil {
		return nil, fmt.Errorf("RPC connection not available for EventTrigger execution")
	}

	// Create a context with timeout to prevent hanging tests
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Parse the new queries-based configuration
	queriesInterface, ok := triggerConfig["queries"]
	if !ok {
		return nil, fmt.Errorf("queries is required for EventTrigger")
	}

	queriesArray, ok := queriesInterface.([]interface{})
	if !ok || len(queriesArray) == 0 {
		return nil, fmt.Errorf("queries must be a non-empty array")
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

	// 🔮 TENDERLY SIMULATION MODE (default: provides sample data)
	if simulationMode {
		return n.runEventTriggerWithTenderlySimulation(ctx, queriesArray, inputVariables)
	}

	// 📊 HISTORICAL SEARCH MODE (use simulationMode: false for production)
	return n.runEventTriggerWithHistoricalSearch(ctx, queriesArray, inputVariables)
}

// runEventTriggerWithTenderlySimulation executes event trigger using Tenderly simulation
func (n *Engine) runEventTriggerWithTenderlySimulation(ctx context.Context, queriesArray []interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	if n.logger != nil {
		n.logger.Info("🔮 EventTrigger: Starting Tenderly simulation mode",
			"queriesCount", len(queriesArray))
	}

	// Initialize Tenderly client
	tenderlyClient := NewTenderlyClient(n.logger)

	// Get chain ID for simulation
	var chainID int64 = 11155111 // Default to Sepolia
	if n.tokenEnrichmentService != nil {
		chainID = int64(n.tokenEnrichmentService.GetChainID())
	}

	// Process the first query for simulation (Tenderly simulates one event at a time)
	if len(queriesArray) == 0 {
		return nil, fmt.Errorf("no queries provided for simulation")
	}

	queryMap, ok := queriesArray[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid query format")
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
		n.logger.Warn("🚫 Tenderly simulation failed, creating sample Transfer event for development", "error", err)

		// Instead of returning error, create a sample Transfer event log and process it through our enrichment pipeline
		// This ensures that the enrichment logic is tested even when Tenderly is not available

		// Create a sample Transfer event log with proper structure
		sampleAddress := common.HexToAddress("0x779877A7B0D9E8603169DdbD7836e478b4624789") // Sample token contract
		fromAddress := common.HexToAddress("0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788")
		toAddress := common.HexToAddress("0x1234567890123456789012345678901234567890")

		// Create sample topics for Transfer event
		transferSignature := common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
		fromTopic := common.BytesToHash(fromAddress.Bytes())
		toTopic := common.BytesToHash(toAddress.Bytes())

		// Use dynamic sample value based on decimals (default to 18 decimals like ETH)
		sampleValue := GetSampleTransferAmount(18)
		sampleData := common.LeftPadBytes(sampleValue.Bytes(), 32)

		// Create sample log with current timestamp-based block number for uniqueness
		currentTime := time.Now().Unix()
		sampleBlockNumber := uint64(1750000000 + currentTime) // Use timestamp to make it unique

		sampleLog := &types.Log{
			Address:     sampleAddress,
			Topics:      []common.Hash{transferSignature, fromTopic, toTopic},
			Data:        sampleData,
			BlockNumber: sampleBlockNumber,
			TxHash:      common.HexToHash(fmt.Sprintf("0x%064x", sampleBlockNumber-1750000000+0x184cd1e84b904808)),
			TxIndex:     0,
			BlockHash:   common.HexToHash(fmt.Sprintf("0x%064x", sampleBlockNumber+0x184cd1e84b904bf1)),
			Index:       0,
			Removed:     false,
		}

		if n.logger != nil {
			n.logger.Info("🎭 Created sample Transfer event for enrichment testing",
				"contract", sampleLog.Address.Hex(),
				"from", fromAddress.Hex(),
				"to", toAddress.Hex(),
				"blockNumber", sampleLog.BlockNumber)
		}

		// Now process this sample log through our normal enrichment pipeline
		simulatedLog = sampleLog
	}

	// Evaluate conditions against the real simulated data
	// If conditions don't match, return nil (same as runTask behavior)
	if len(query.GetConditions()) > 0 {
		conditionsMet := n.evaluateEventConditions(simulatedLog, query.GetConditions())
		if !conditionsMet {
			n.logger.Info("🚫 Conditions not satisfied by real data, no event returned",
				"contract", simulatedLog.Address.Hex(),
				"conditions_count", len(query.GetConditions()))

			// Return nil to indicate no event found (conditions not met)
			return nil, nil
		}
	}

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
		"address":          simulatedLog.Address.Hex(),
		"topics":           topicsMetadata, // Now protobuf-compatible
		"data":             "0x" + common.Bytes2Hex(simulatedLog.Data),
		"blockNumber":      simulatedLog.BlockNumber,
		"transactionHash":  simulatedLog.TxHash.Hex(),
		"transactionIndex": simulatedLog.TxIndex,
		"blockHash":        simulatedLog.BlockHash.Hex(),
		"logIndex":         simulatedLog.Index,
		"removed":          simulatedLog.Removed,
		"chainId":          chainID,
	}

	// Parse event data using ABI if provided
	var parsedData map[string]interface{}
	var isTransferEvent bool

	contractABI := query.GetContractAbi()
	// OPTIMIZED: Use protobuf Values directly without string conversion
	if len(contractABI) > 0 {
		if n.logger != nil {
			n.logger.Info("🔍 EventTrigger: Using optimized ABI parsing (no string conversion)",
				"hasABI", true,
				"abiElementCount", len(contractABI))
		}

		if parsedEventData, err := n.parseEventWithABIOptimized(simulatedLog, contractABI, query); err == nil {
			// Success with optimized parsing
			parsedData = parsedEventData

			// Check if this is enriched transfer data
			if eventName, ok := parsedEventData["eventName"].(string); ok && eventName == "Transfer" {
				isTransferEvent = true
			}
		} else {
			n.logger.Warn("Failed to parse event with optimized ABI method, using raw data", "error", err)
			// Fallback to raw data if optimized ABI parsing fails
			parsedData = metadata
		}
	} else {
		if n.logger != nil {
			n.logger.Info("🔍 EventTrigger: No ABI provided, using raw log data")
		}
		parsedData = metadata
	}

	// Build the result structure based on event type
	result := map[string]interface{}{
		"found":    true,
		"metadata": metadata, // Raw blockchain event data
	}

	// For Transfer events with enriched data, structure it properly
	if isTransferEvent {
		// parsedData contains the enriched transfer_log structure
		result["transfer_log"] = parsedData
		result["data"] = parsedData // Also provide as data for backward compatibility

		if n.logger != nil {
			n.logger.Info("✅ EventTrigger: Created enriched transfer_log structure",
				"tokenSymbol", parsedData["tokenSymbol"],
				"blockTimestamp", parsedData["blockTimestamp"])
		}
	} else {
		// For non-Transfer events, use standard data structure
		result["data"] = parsedData
	}

	if n.logger != nil {
		hasABI := len(contractABI) > 0
		n.logger.Info("✅ EventTrigger: Tenderly simulation completed successfully",
			"contract", simulatedLog.Address.Hex(),
			"block", simulatedLog.BlockNumber,
			"txHash", simulatedLog.TxHash.Hex(),
			"chainId", chainID,
			"hasABI", hasABI)
	}

	return result, nil
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

// parseEventWithABIOptimized efficiently parses an event using protobuf Values without string conversion
func (n *Engine) parseEventWithABIOptimized(eventLog *types.Log, abiValues []*structpb.Value, query *avsproto.EventTrigger_Query) (map[string]interface{}, error) {
	if len(abiValues) == 0 {
		return nil, fmt.Errorf("empty ABI provided")
	}

	// Use the optimized helper to get bytes.Reader directly
	reader, err := ConvertContractAbiToReader(abiValues)
	if err != nil {
		return nil, fmt.Errorf("failed to convert ABI to reader: %v", err)
	}

	// Parse ABI directly from bytes.Reader - no string conversion!
	contractABI, err := abi.JSON(reader)
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

	// Initialize the result map with only ABI-parsed event data
	parsedData := make(map[string]interface{})

	// Add only the event name from ABI
	parsedData["eventName"] = eventName

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

	// Create ABI value converter
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
						parsedData[input.Name] = topicValue.Hex()
					}
				case abi.BoolTy:
					// Convert boolean from topic
					boolVal := new(big.Int).SetBytes(topicValue.Bytes()).Cmp(big.NewInt(0)) != 0
					parsedData[input.Name] = boolVal
				case abi.AddressTy:
					// Keep addresses as hex
					parsedData[input.Name] = common.HexToAddress(topicValue.Hex()).Hex()
				case abi.HashTy, abi.FixedBytesTy:
					// Keep hashes and fixed bytes as hex
					parsedData[input.Name] = topicValue.Hex()
				default:
					// Default to hex for other types
					parsedData[input.Name] = topicValue.Hex()
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
				parsedData[input.Name] = convertedValue

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
		parsedData[key] = value
	}

	// Add decimals info if we retrieved it
	if decimalsValue != nil {
		parsedData["decimals"] = decimalsValue.Uint64() // Return as number, not string
	}

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
		if fromAddr, ok := parsedData["from"].(string); ok {
			transferResponse.FromAddress = fromAddr
		}
		if toAddr, ok := parsedData["to"].(string); ok {
			transferResponse.ToAddress = toAddr
		}

		// Populate token metadata if available
		if tokenMetadata != nil {
			transferResponse.TokenName = tokenMetadata.Name
			transferResponse.TokenSymbol = tokenMetadata.Symbol
			transferResponse.TokenDecimals = tokenMetadata.Decimals

			// Use the formatted value from ABI parsing if available, otherwise format using token metadata
			if formattedValue, hasFormatted := parsedData["value"]; hasFormatted {
				if valueStr, ok := formattedValue.(string); ok {
					transferResponse.Value = valueStr
				}
			} else if rawValue, ok := parsedData["valueRaw"].(string); ok {
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
				if formattedValue, hasFormatted := parsedData["value"]; hasFormatted {
					if valueStr, ok := formattedValue.(string); ok {
						transferResponse.Value = valueStr
					}
				} else if rawValue, ok := parsedData["valueRaw"].(string); ok {
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
				transferResponse.BlockTimestamp = int64(header.Time)
			}
			// If error, keep the default timestamp from CreateStandardizedTransferResponse
		}

		// Convert standardized response to map for compatibility with existing code
		// This provides all the standardized fields without deprecated or duplicate ones
		// Note: logIndex and transactionIndex are excluded as they're available in metadata
		transferMap := map[string]interface{}{
			"address":         transferResponse.Address,
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

		return transferMap, nil
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
			"found":         false,
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

	// Get chain ID for metadata
	var chainID int64 = 11155111 // Default to Sepolia
	if n.tokenEnrichmentService != nil {
		chainID = int64(n.tokenEnrichmentService.GetChainID())
	}

	// Build raw metadata (the original blockchain event data)
	metadata := map[string]interface{}{
		"address":          mostRecentEvent.Address.Hex(),
		"topics":           topics,
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
		"found":         true,
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
		return nil, fmt.Errorf("ManualTrigger data is required and cannot be null")
	}

	if n.logger != nil {
		n.logger.Info("ManualTrigger executed with valid JSON data", "dataType", fmt.Sprintf("%T", data))
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
func (n *Engine) runProcessingNodeWithInputs(nodeType string, nodeConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
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

	// Create a clean VM for isolated execution with proper secrets
	vm, err := NewVMWithData(nil, nil, n.smartWalletConfig, secrets)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM: %w", err)
	}

	vm.WithLogger(n.logger).WithDb(n.db)

	// Add input variables to VM for template processing and node access
	// Apply dual-access mapping to enable both camelCase and snake_case field access
	processedInputVariables := inputVariables
	for key, processedValue := range processedInputVariables {
		vm.AddVar(key, processedValue)
	}

	// Create node from type and config
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
		return nil, fmt.Errorf("node execution failed: %w", err)
	}

	if !executionStep.Success {
		return nil, fmt.Errorf("execution failed: %s", executionStep.Error)
	}

	// Extract and return the result data
	return n.extractExecutionResult(executionStep)
}

// LoadSecretsForImmediateExecution loads secrets for immediate node execution
// It loads global macroSecrets and user-level secrets (no workflow-level secrets since there's no workflow)
func (n *Engine) LoadSecretsForImmediateExecution(inputVariables map[string]interface{}) (map[string]string, error) {
	secrets := make(map[string]string)

	// Copy global static secrets from macroSecrets (equivalent to copyMap(secrets, macroSecrets) in LoadSecretForTask)
	copyMap(secrets, macroSecrets)

	// Try to get user from workflowContext if available
	if workflowContext, ok := inputVariables["workflowContext"]; ok {
		if wfCtx, ok := workflowContext.(map[string]interface{}); ok {
			if userIdStr, ok := wfCtx["userId"].(string); ok {
				// Load user-level secrets from database
				// Note: For immediate execution we don't have workflow-level secrets since there's no specific workflow
				// For now, we'll just use the global macroSecrets
				// In a full implementation, you'd want to resolve userId to user address and load user secrets
				// But the most important thing is that macroSecrets (global config secrets) are available
				if n.logger != nil {
					n.logger.Debug("LoadSecretsForImmediateExecution: Using global secrets", "userId", userIdStr, "secretCount", len(secrets))
				}
			}
		}
	}

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

// extractExecutionResult extracts the result data from an execution step (legacy version with success/error fields)
func (n *Engine) extractExecutionResult(executionStep *avsproto.Execution_Step) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	// Handle different output data types
	if ccode := executionStep.GetCustomCode(); ccode != nil && ccode.GetData() != nil {
		iface := ccode.GetData().AsInterface()
		if m, ok := iface.(map[string]interface{}); ok {
			result = m
		} else {
			result["data"] = iface
		}
	} else if restAPI := executionStep.GetRestApi(); restAPI != nil && restAPI.GetData() != nil {
		// REST API data is now stored as structpb.Value directly (no Any wrapper)
		iface := restAPI.GetData().AsInterface()
		if m, ok := iface.(map[string]interface{}); ok {
			// Use raw response format (same as simulateWorkflow) instead of ProcessRestAPIResponseRaw
			result = m
		} else {
			result = map[string]interface{}{"data": iface}
		}
	} else if contractRead := executionStep.GetContractRead(); contractRead != nil {
		// ContractRead now returns a single flattened object directly
		// Extract both data and metadata, following the same pattern as EventTrigger

		// Extract data if available
		if contractRead.GetData() != nil {
			iface := contractRead.GetData().AsInterface()
			if m, ok := iface.(map[string]interface{}); ok {
				// Create result map with data field
				result["data"] = m
			} else {
				result["data"] = iface
			}
		}

		// Extract metadata if available (independent of data)
		if contractRead.GetMetadata() != nil {
			if metadataArray := gow.ValueToSlice(contractRead.GetMetadata()); metadataArray != nil {
				result["metadata"] = metadataArray
			} else {
				result["metadata"] = contractRead.GetMetadata().AsInterface()
			}
		}
	} else if branch := executionStep.GetBranch(); branch != nil {
		// Extract conditionId from the new data field
		if branch.Data != nil {
			dataMap := gow.ValueToMap(branch.Data)
			if dataMap != nil {
				if conditionId, ok := dataMap["conditionId"]; ok {
					result["conditionId"] = conditionId
				}
			}
		}
		result["success"] = true
	} else if ethTransfer := executionStep.GetEthTransfer(); ethTransfer != nil {
		// EthTransfer output now contains enhanced results structure
		if ethTransfer.GetData() != nil {
			// Extract transaction hash from the data field
			if dataMap := gow.ValueToMap(ethTransfer.GetData()); dataMap != nil {
				if txHash, ok := dataMap["transactionHash"]; ok {
					result["txHash"] = txHash
				}
			}
		}
		result["success"] = true
	} else if contractWrite := executionStep.GetContractWrite(); contractWrite != nil {
		// ContractWrite output now contains enhanced results structure
		if contractWrite.GetData() != nil {
			// Extract results using helper function
			allResults := ExtractResultsFromProtobufValue(contractWrite.GetData())

			// Return results array directly without backward compatibility
			result["results"] = allResults

			return result, nil
		}
		return map[string]interface{}{"status": "success"}, nil
	} else if loop := executionStep.GetLoop(); loop != nil {
		// Loop output contains the array of iteration results
		if loop.GetData() != nil {
			iface := loop.GetData().AsInterface()
			// Store loop data in loopResult key for run_node_immediately.go to extract
			result["loopResult"] = iface
		}
	} else if filter := executionStep.GetFilter(); filter != nil {
		// Filter output contains the filtered array results
		if filter.GetData() != nil {
			iface := filter.GetData().AsInterface()
			// Store filter data directly in result for consistency with other nodes
			if filterArray, ok := iface.([]interface{}); ok {
				// Return the filtered array wrapped in a map
				return map[string]interface{}{"data": filterArray}, nil
			} else {
				// Fallback: store in data field
				result["data"] = iface
			}
		}
	}

	// If no specific data was extracted, include basic execution info
	// BUT preserve empty objects/arrays as-is for consistency
	if len(result) == 0 {
		// Check if this was a successful execution with explicitly empty result
		if executionStep.Success && executionStep.Error == "" {
			// Check the node type to determine appropriate empty structure
			// No special handling for CustomCode - it should return exactly what the code returns
			result["success"] = executionStep.Success
			result["nodeId"] = executionStep.Id
			if executionStep.Error != "" {
				result["error"] = executionStep.Error
			}
		} else {
			result["success"] = executionStep.Success
			result["nodeId"] = executionStep.Id
			if executionStep.Error != "" {
				result["error"] = executionStep.Error
			}
		}
	} else {
		result["success"] = executionStep.Success
		result["nodeId"] = executionStep.Id
		if executionStep.Error != "" {
			result["error"] = executionStep.Error
		}
	}

	return result, nil
}

// RunNodeImmediatelyRPC handles the RPC interface for immediate node execution
func (n *Engine) RunNodeImmediatelyRPC(user *model.User, req *avsproto.RunNodeWithInputsReq) (*avsproto.RunNodeWithInputsResp, error) {
	// Convert protobuf request to internal format
	nodeConfig := make(map[string]interface{})
	for k, v := range req.NodeConfig {
		nodeConfig[k] = v.AsInterface()
	}

	inputVariables := make(map[string]interface{})
	for k, v := range req.InputVariables {
		inputVariables[k] = v.AsInterface()
	}

	// Convert NodeType enum to string
	nodeTypeStr := NodeTypeToString(req.NodeType)
	if nodeTypeStr == "" {
		// For unsupported node types, return error but still set output data to avoid OUTPUT_DATA_NOT_SET
		resp := &avsproto.RunNodeWithInputsResp{
			Success: false,
			Error:   fmt.Sprintf("unsupported node type: %v", req.NodeType),
			NodeId:  fmt.Sprintf("node_immediate_%d_ERROR1", time.Now().UnixNano()),
		}
		// Set default RestAPI output structure to avoid OUTPUT_DATA_NOT_SET
		resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{
			RestApi: &avsproto.RestAPINode_Output{},
		}
		return resp, nil
	}

	// Execute the node immediately
	result, err := n.RunNodeImmediately(nodeTypeStr, nodeConfig, inputVariables)
	if err != nil {
		if n.logger != nil {
			// Categorize errors to avoid unnecessary stack traces for expected validation errors
			if isExpectedValidationError(err) {
				// Expected validation errors - log at WARN level without stack traces
				n.logger.Warn("RunNodeImmediatelyRPC: Validation failed", "nodeType", nodeTypeStr, "error", err.Error())
			} else {
				// Unexpected system errors - log at ERROR level without stack traces for cleaner output
				n.logger.Error("RunNodeImmediatelyRPC: System error during execution", "nodeType", nodeTypeStr, "error", err.Error())
			}
		}

		// Create response with failure status but still set appropriate output data structure
		// to avoid OUTPUT_DATA_NOT_SET errors on client side
		resp := &avsproto.RunNodeWithInputsResp{
			Success: false,
			Error:   err.Error(),
			NodeId:  fmt.Sprintf("node_immediate_%d_ERROR2", time.Now().UnixNano()),
		}

		// Set empty output data structure based on node type to avoid OUTPUT_DATA_NOT_SET
		switch nodeTypeStr {
		case NodeTypeRestAPI:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{
				RestApi: &avsproto.RestAPINode_Output{},
			}
		case NodeTypeCustomCode:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_CustomCode{
				CustomCode: &avsproto.CustomCodeNode_Output{},
			}
		case NodeTypeETHTransfer:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_EthTransfer{
				EthTransfer: &avsproto.ETHTransferNode_Output{},
			}
		case NodeTypeContractRead:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_ContractRead{
				ContractRead: &avsproto.ContractReadNode_Output{},
			}
		case NodeTypeContractWrite:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode_Output{},
			}
		case NodeTypeGraphQLQuery:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Graphql{
				Graphql: &avsproto.GraphQLQueryNode_Output{},
			}
		case NodeTypeBranch:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Branch{
				Branch: &avsproto.BranchNode_Output{},
			}
		case NodeTypeFilter:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Filter{
				Filter: &avsproto.FilterNode_Output{},
			}
		case NodeTypeLoop:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Loop{
				Loop: &avsproto.LoopNode_Output{},
			}
		default:
			// For unknown/invalid node types, set RestAPI as default to avoid OUTPUT_DATA_NOT_SET
			resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{
				RestApi: &avsproto.RestAPINode_Output{},
			}
		}

		return resp, nil
	}

	// Log successful execution
	if n.logger != nil {
		n.logger.Info("RunNodeImmediatelyRPC: Executed successfully", "nodeTypeStr", nodeTypeStr, "originalNodeType", req.NodeType)
	}

	// Convert result to the appropriate protobuf output type
	resp := &avsproto.RunNodeWithInputsResp{
		Success: true,
		NodeId:  fmt.Sprintf("node_immediate_%d_TEST_MARKER", time.Now().UnixNano()),
	}

	// Set the appropriate output data based on the node type
	// Always set output data structure even if result is empty to avoid OUTPUT_DATA_NOT_SET
	switch nodeTypeStr {
	case NodeTypeRestAPI:
		// Convert result directly to protobuf Value for REST API (no Any wrapping needed)
		valueData, err := structpb.NewValue(result)
		if err != nil {
			return &avsproto.RunNodeWithInputsResp{
				Success: false,
				Error:   fmt.Sprintf("failed to convert REST API output: %v", err),
				NodeId:  "",
			}, nil
		}
		restOutput := &avsproto.RestAPINode_Output{
			Data: valueData,
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{
			RestApi: restOutput,
		}
	case NodeTypeCustomCode:
		// For CustomCode immediate execution, return exactly what the JavaScript code returned
		// Extract the raw data from the wrapped result to avoid nested structures and metadata pollution
		var rawData interface{}

		if result != nil {
			// Check if this looks like an extractExecutionResult-processed object with metadata
			if hasMetadata := (result["success"] != nil || result["nodeId"] != nil); hasMetadata {
				// This result has been processed by extractExecutionResult, extract the original data
				if dataField, ok := result["data"]; ok {
					// Non-object return (like 42, "hello", null) - use the "data" field
					rawData = dataField
				} else {
					// Object return that got metadata added - extract the original object
					originalObject := make(map[string]interface{})
					for k, v := range result {
						// Skip metadata fields added by extractExecutionResult
						if k != "success" && k != "nodeId" && k != "error" {
							originalObject[k] = v
						}
					}
					// If nothing remains after removing metadata, it was an empty object
					rawData = originalObject
				}
			} else {
				// No metadata detected, use result as-is
				rawData = result
			}
		} else {
			rawData = nil
		}

		valueData, err := structpb.NewValue(rawData)
		if err != nil {
			return &avsproto.RunNodeWithInputsResp{
				Success: false,
				Error:   fmt.Sprintf("failed to convert CustomCode output: %v", err),
				NodeId:  "",
			}, nil
		}
		customOutput := &avsproto.CustomCodeNode_Output{
			Data: valueData,
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_CustomCode{
			CustomCode: customOutput,
		}
	case NodeTypeETHTransfer:
		// For ETH transfer nodes - set empty structure if no result or extract transaction hash
		// Create ETH transfer output with new data field
		ethData := map[string]interface{}{}
		if result != nil {
			if txHash, ok := result["txHash"].(string); ok {
				ethData["transactionHash"] = txHash
			}
		}

		// Convert to protobuf Value
		dataValue, err := structpb.NewValue(ethData)
		if err != nil {
			dataValue, _ = structpb.NewValue(map[string]interface{}{})
		}

		ethOutput := &avsproto.ETHTransferNode_Output{
			Data: dataValue,
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_EthTransfer{
			EthTransfer: ethOutput,
		}
	case NodeTypeContractRead:
		// For contract read nodes - always set output structure to avoid OUTPUT_DATA_NOT_SET
		contractReadOutput := &avsproto.ContractReadNode_Output{}
		if result != nil && len(result) > 0 {
			// Extract data field (flattened clean data)
			if dataInterface, hasData := result["data"]; hasData {
				if resultsValue, err := structpb.NewValue(dataInterface); err == nil {
					contractReadOutput.Data = resultsValue
				}
			} else {
				// If no explicit data field, use the flattened result object (excluding metadata)
				cleanResult := make(map[string]interface{})
				for k, v := range result {
					if k != "metadata" {
						cleanResult[k] = v
					}
				}
				if len(cleanResult) > 0 {
					if resultsValue, err := structpb.NewValue(cleanResult); err == nil {
						contractReadOutput.Data = resultsValue
					}
				}
			}

			// Extract metadata field (detailed method information)
			if metadataInterface, hasMetadata := result["metadata"]; hasMetadata {
				if metadataValue, err := structpb.NewValue(metadataInterface); err == nil {
					contractReadOutput.Metadata = metadataValue
				}
			}
		}

		resp.OutputData = &avsproto.RunNodeWithInputsResp_ContractRead{
			ContractRead: contractReadOutput,
		}
	case NodeTypeContractWrite:
		// For contract write nodes - always set output structure to avoid OUTPUT_DATA_NOT_SET
		contractWriteOutput := &avsproto.ContractWriteNode_Output{}
		if result != nil && len(result) > 0 {
			// Convert result to the new data structure
			var resultsArray []interface{}

			// Check if we have the new results array format (from VM execution)
			if resultsFromVM, ok := result["results"].([]interface{}); ok {
				// Process each result in the array
				for _, resultInterface := range resultsFromVM {
					if methodResult, ok := resultInterface.(*avsproto.ContractWriteNode_MethodResult); ok {
						// Convert protobuf result to map for consistency
						convertedResult := map[string]interface{}{
							"methodName": methodResult.MethodName,
							"success":    methodResult.Success,
							"error":      methodResult.Error,
						}

						// Add methodABI if available
						if methodResult.MethodAbi != nil {
							convertedResult["methodABI"] = methodResult.MethodAbi.AsInterface()
						}

						// Add flexible receipt
						if methodResult.Receipt != nil {
							convertedResult["receipt"] = methodResult.Receipt.AsInterface()
						}

						// Add return value
						if methodResult.Value != nil {
							convertedResult["value"] = methodResult.Value.AsInterface()
						} else {
							convertedResult["value"] = nil
						}

						resultsArray = append(resultsArray, convertedResult)
					} else if methodResultMap, ok := resultInterface.(map[string]interface{}); ok {
						// Already in map format
						resultsArray = append(resultsArray, methodResultMap)
					}
				}
			} else {
				// Fallback: Try to extract transaction hash from result (backward compatibility)
				if txHash, ok := result["txHash"].(string); ok {
					convertedResult := map[string]interface{}{
						"methodName": "unknown",
						"success":    true,
						"transaction": map[string]interface{}{
							"hash": txHash,
						},
					}
					resultsArray = append(resultsArray, convertedResult)
				} else if transactionHash, ok := result["transactionHash"].(string); ok {
					convertedResult := map[string]interface{}{
						"methodName": "unknown",
						"success":    true,
						"transaction": map[string]interface{}{
							"hash": transactionHash,
						},
					}
					resultsArray = append(resultsArray, convertedResult)
				}
			}

			// Convert to protobuf Value
			if resultsValue, err := structpb.NewValue(resultsArray); err == nil {
				contractWriteOutput.Data = resultsValue
			}
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_ContractWrite{
			ContractWrite: contractWriteOutput,
		}
	case NodeTypeGraphQLQuery:
		// For GraphQL query nodes - create output with new data field
		var dataValue *structpb.Value
		var err error
		if result != nil && len(result) > 0 {
			// Set actual GraphQL result data
			dataValue, err = structpb.NewValue(result)
			if err != nil {
				return &avsproto.RunNodeWithInputsResp{
					Success: false,
					Error:   fmt.Sprintf("failed to convert GraphQL output: %v", err),
					NodeId:  "",
				}, nil
			}
		} else {
			// Set empty object for no result
			dataValue, err = structpb.NewValue(map[string]interface{}{})
			if err != nil {
				return &avsproto.RunNodeWithInputsResp{
					Success: false,
					Error:   fmt.Sprintf("failed to create empty GraphQL output: %v", err),
					NodeId:  "",
				}, nil
			}
		}

		graphqlOutput := &avsproto.GraphQLQueryNode_Output{
			Data: dataValue,
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_Graphql{
			Graphql: graphqlOutput,
		}
	case NodeTypeBranch:
		// For branch nodes - create output with new data field
		branchData := map[string]interface{}{}
		if result != nil && len(result) > 0 {
			// Set actual branch result data
			if conditionId, ok := result["conditionId"].(string); ok {
				branchData["conditionId"] = conditionId
			}
		} else {
			// Set empty string for no result
			branchData["conditionId"] = ""
		}

		// Convert to protobuf Value
		dataValue, err := structpb.NewValue(branchData)
		if err != nil {
			dataValue, _ = structpb.NewValue(map[string]interface{}{})
		}

		branchOutput := &avsproto.BranchNode_Output{
			Data: dataValue,
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_Branch{
			Branch: branchOutput,
		}
	case NodeTypeFilter:
		// For filter nodes - create output with new data field
		var dataValue *structpb.Value
		var err error
		if result != nil && len(result) > 0 {
			// Set actual filter result data
			dataValue, err = structpb.NewValue(result)
			if err != nil {
				return &avsproto.RunNodeWithInputsResp{
					Success: false,
					Error:   fmt.Sprintf("failed to convert Filter output: %v", err),
					NodeId:  "",
				}, nil
			}
		} else {
			// Set empty array for no result
			emptyArray := []interface{}{}
			dataValue, err = structpb.NewValue(emptyArray)
			if err != nil {
				return &avsproto.RunNodeWithInputsResp{
					Success: false,
					Error:   fmt.Sprintf("failed to create empty Filter output: %v", err),
					NodeId:  "",
				}, nil
			}
		}

		filterOutput := &avsproto.FilterNode_Output{
			Data: dataValue,
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_Filter{
			Filter: filterOutput,
		}
	case NodeTypeLoop:
		if result != nil {
			// Extract the actual loop data from the loopResult key
			var loopData interface{}
			if loopResult, exists := result["loopResult"]; exists {
				loopData = loopResult
			} else {
				// Fallback to entire result if no loopResult key
				loopData = result
			}

			// Convert loop data to protobuf Value
			dataValue, err := structpb.NewValue(loopData)
			if err != nil {
				return &avsproto.RunNodeWithInputsResp{
					Success: false,
					Error:   fmt.Sprintf("failed to convert loop output: %v", err),
					NodeId:  "",
				}, nil
			}

			resp.OutputData = &avsproto.RunNodeWithInputsResp_Loop{
				Loop: &avsproto.LoopNode_Output{
					Data: dataValue,
				},
			}
		} else {
			// Empty loop result as empty array
			emptyArray := []interface{}{}
			dataValue, err := structpb.NewValue(emptyArray)
			if err != nil {
				dataValue, _ = structpb.NewValue([]interface{}{})
			}

			resp.OutputData = &avsproto.RunNodeWithInputsResp_Loop{
				Loop: &avsproto.LoopNode_Output{
					Data: dataValue,
				},
			}
		}
	default:
		// For unknown/invalid node types, set RestAPI as default to avoid OUTPUT_DATA_NOT_SET
		resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{
			RestApi: &avsproto.RestAPINode_Output{},
		}
	}

	// Extract top-level metadata for ContractRead nodes (following event trigger pattern)
	if nodeTypeStr == NodeTypeContractRead {
		// For ContractRead nodes, extract metadata directly from the contractReadOutput that was created
		if contractReadResp, ok := resp.OutputData.(*avsproto.RunNodeWithInputsResp_ContractRead); ok && contractReadResp.ContractRead != nil {
			if contractReadResp.ContractRead.Metadata != nil {
				resp.Metadata = contractReadResp.ContractRead.Metadata
			}
		}
	}

	return resp, nil
}

// RunTriggerRPC handles the RPC interface for immediate trigger execution
func (n *Engine) RunTriggerRPC(user *model.User, req *avsproto.RunTriggerReq) (*avsproto.RunTriggerResp, error) {
	// Convert protobuf request to internal format
	triggerConfig := make(map[string]interface{})
	for k, v := range req.TriggerConfig {
		triggerConfig[k] = v.AsInterface()
	}

	// Extract trigger input data from the request
	triggerInput := make(map[string]interface{})
	for k, v := range req.TriggerInput {
		triggerInput[k] = v.AsInterface()
	}

	// Convert TriggerType enum to string
	triggerTypeStr := TriggerTypeToString(req.TriggerType)
	if triggerTypeStr == "" {
		// For unsupported trigger types, return error but still set output data to avoid OUTPUT_DATA_NOT_SET
		resp := &avsproto.RunTriggerResp{
			Success:   false,
			Error:     fmt.Sprintf("unsupported trigger type: %v", req.TriggerType),
			TriggerId: fmt.Sprintf("trigger_immediate_%d", time.Now().UnixNano()),
		}
		// Set default ManualTrigger output structure to avoid OUTPUT_DATA_NOT_SET
		resp.OutputData = &avsproto.RunTriggerResp_ManualTrigger{
			ManualTrigger: &avsproto.ManualTrigger_Output{},
		}
		return resp, nil
	}

	// Execute the trigger immediately with trigger input data
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
			TriggerId: fmt.Sprintf("trigger_immediate_%d", time.Now().UnixNano()),
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
		n.logger.Info("RunTriggerRPC: Executed successfully", "triggerTypeStr", triggerTypeStr, "originalTriggerType", req.TriggerType)
	}

	// Convert result to the appropriate protobuf output type
	resp := &avsproto.RunTriggerResp{
		Success:   true,
		TriggerId: fmt.Sprintf("trigger_immediate_%d", time.Now().UnixNano()),
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

		// Add metadata for runTrigger (debugging/testing) - properly convert to protobuf Value
		if result != nil {
			if n.logger != nil {
				n.logger.Info("🔍 RunTriggerRPC: Checking for metadata in result",
					"hasResult", result != nil,
					"resultKeys", getMapKeys(result))
			}

			if metadata, hasMetadata := result["metadata"]; hasMetadata && metadata != nil {
				if n.logger != nil {
					n.logger.Info("🔍 RunTriggerRPC: Found metadata, converting to protobuf",
						"metadataType", fmt.Sprintf("%T", metadata),
						"metadataValue", metadata)
				}

				// Convert metadata to be compatible with protobuf
				compatibleMetadata := convertToProtobufCompatible(metadata)

				if metadataValue, err := structpb.NewValue(compatibleMetadata); err == nil {
					resp.Metadata = metadataValue
					if n.logger != nil {
						n.logger.Info("✅ RunTriggerRPC: Successfully converted metadata to protobuf")
					}
				} else {
					if n.logger != nil {
						n.logger.Error("❌ RunTriggerRPC: Failed to convert metadata to protobuf", "error", err)
					}
				}
			} else {
				if n.logger != nil {
					n.logger.Info("🔍 RunTriggerRPC: No metadata found in result")
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

	// Extract topics
	if topicsInterface, exists := queryMap["topics"]; exists {
		if topicsArray, ok := topicsInterface.([]interface{}); ok {
			for _, topicGroupInterface := range topicsArray {
				if topicGroupMap, ok := topicGroupInterface.(map[string]interface{}); ok {
					if valuesInterface, exists := topicGroupMap["values"]; exists {
						if valuesArray, ok := valuesInterface.([]interface{}); ok {
							topicGroup := &avsproto.EventTrigger_Topics{}
							values := make([]string, 0, len(valuesArray))
							for _, valueInterface := range valuesArray {
								if valueStr, ok := valueInterface.(string); ok {
									values = append(values, valueStr)
								}
							}
							topicGroup.Values = values
							query.Topics = append(query.Topics, topicGroup)
						}
					}
				}
			}
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

// getMapKeys returns the keys of a map for debugging purposes
func getMapKeys(m map[string]interface{}) []string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// convertToProtobufCompatible converts data structures to be compatible with structpb.NewValue()
// This handles cases like []string which structpb.NewValue() cannot handle directly
func convertToProtobufCompatible(data interface{}) interface{} {
	switch v := data.(type) {
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
	default:
		// Return as-is for basic types (string, int, float, bool, etc.)
		return v
	}
}
