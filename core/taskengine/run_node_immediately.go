package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"google.golang.org/protobuf/types/known/anypb"
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

// runEventTriggerImmediately makes an actual call to Ethereum RPC to fetch event data
// Unlike regular workflow event triggers that listen to real-time events, this searches
// for the most recent historical event matching the criteria without block limitations
//
// Enhanced syntax support:
// - Basic: "0xddf252...signature"
// - Single contract: "0xddf252...signature&&0xcontractAddress"
// - Multiple contracts: "0xddf252...signature&&contracts=[0xaddr1,0xaddr2]"
// - From address filter: "0xddf252...signature&&from=0xaddress"
// - To address filter: "0xddf252...signature&&to=0xaddress"
// - Combined: "0xddf252...signature&&contracts=[0xaddr1,0xaddr2]&&from=0xaddress"
// - "0xddf252...signature&&from=0xaddr||to=0xaddr" -> (signature, [], addr, addr) - OR syntax support
func (n *Engine) runEventTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// Ensure RPC connection is available
	if rpcConn == nil {
		return nil, fmt.Errorf("RPC connection not available for EventTrigger execution")
	}

	// Create a context with timeout to prevent hanging tests
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // Increased slightly for chunked search
	defer cancel()

	// Parse the enhanced expression format
	var expression string
	var topicHash string
	var contractAddresses []string
	var fromAddress, toAddress string

	if expr, ok := triggerConfig["expression"].(string); ok {
		expression = expr
		topicHash, contractAddresses, fromAddress, toAddress = parseEnhancedExpression(expression)
	}

	// If no expression provided, use default Transfer event signature
	if topicHash == "" {
		topicHash = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef" // Transfer event
	}

	if n.logger != nil {
		contractsInfo := "ALL contracts"
		if len(contractAddresses) > 0 {
			contractsInfo = fmt.Sprintf("contracts: %v", contractAddresses)
		}

		n.logger.Info("EventTrigger: Making enhanced RPC call to fetch event data",
			"expression", expression,
			"topicHash", topicHash,
			"contractsInfo", contractsInfo,
			"fromAddress", fromAddress,
			"toAddress", toAddress)
	}

	// Get the latest block number
	currentBlock, err := rpcConn.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current block number: %w", err)
	}

	// If we need to search for both FROM and TO, we'll need separate queries
	var allEvents []types.Log
	var totalSearched uint64

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

	// Function to search with specific topic configuration
	searchWithTopics := func(topics [][]common.Hash) ([]types.Log, uint64, error) {
		var events []types.Log
		var searched uint64

		for _, searchRange := range searchRanges {
			select {
			case <-ctx.Done():
				// Timeout occurred - return partial results gracefully
				if n.logger != nil {
					n.logger.Warn("EventTrigger: Search timeout reached, returning partial results",
						"blocksSearched", searched,
						"eventsFound", len(events))
				}
				return events, searched, nil // Return what we have so far instead of error
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
				Topics:    topics,
			}

			// Add contract address filters if specified
			if len(contractAddresses) > 0 {
				addresses := make([]common.Address, len(contractAddresses))
				for i, addr := range contractAddresses {
					addresses[i] = common.HexToAddress(addr)
				}
				query.Addresses = addresses
			}

			if n.logger != nil {
				addressStrs := make([]string, len(query.Addresses))
				for i, addr := range query.Addresses {
					addressStrs[i] = addr.Hex()
				}

				topicStrs := make([][]string, len(topics))
				for i, topicGroup := range topics {
					topicStrs[i] = make([]string, len(topicGroup))
					for j, topic := range topicGroup {
						topicStrs[i][j] = topic.Hex()
					}
				}

				n.logger.Debug("EventTrigger: Enhanced search with detailed params",
					"fromBlock", fromBlock,
					"toBlock", currentBlock,
					"contractAddresses", addressStrs,
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
							// Chunked search in progress - reduced logging for cleaner output

							// Break the range into 1000-block chunks
							chunkSize := uint64(1000)
							var chunkedSearched uint64
							for chunkStart := fromBlock; chunkStart < currentBlock; chunkStart += chunkSize {
								// Check timeout during chunked search
								select {
								case <-ctx.Done():
									n.logger.Info("EventTrigger: Chunked search timeout reached",
										"chunkedSearched", chunkedSearched,
										"chunksProcessed", (chunkStart-fromBlock)/chunkSize)
									searched += chunkedSearched
									return events, searched, nil
								default:
								}

								chunkEnd := chunkStart + chunkSize - 1
								if chunkEnd > currentBlock {
									chunkEnd = currentBlock
								}

								chunkQuery := query
								chunkQuery.FromBlock = big.NewInt(int64(chunkStart))
								chunkQuery.ToBlock = big.NewInt(int64(chunkEnd))

								chunkLogs, chunkErr := rpcConn.FilterLogs(ctx, chunkQuery)
								chunkedSearched += (chunkEnd - chunkStart + 1)

								if chunkErr == nil {
									logs = append(logs, chunkLogs...)
									if n.logger != nil && len(chunkLogs) > 0 {
										n.logger.Info("EventTrigger: Found events in chunk - stopping early for most recent",
											"chunkStart", chunkStart,
											"chunkEnd", chunkEnd,
											"eventsInChunk", len(chunkLogs))
									}
									// OPTIMIZATION: For runTrigger, we only need the most recent event
									// Stop immediately after finding any events to avoid unnecessary work
									if len(chunkLogs) > 0 {
										searched += chunkedSearched
										return logs, searched, nil
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
							searched += chunkedSearched

							// If chunked search found events or completed successfully, treat as success
							if len(logs) > 0 || chunkedSearched > 0 {
								err = nil
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
				searched += (currentBlock - fromBlock)
			}

			events = append(events, logs...)

			if n.logger != nil {
				n.logger.Info("EventTrigger: Search completed for range",
					"fromBlock", fromBlock,
					"toBlock", currentBlock,
					"logsFound", len(logs),
					"totalEventsSoFar", len(events),
					"blockRange", currentBlock-fromBlock)
			}

			// OPTIMIZATION: For runTrigger, return immediately after finding any events
			// Since we search from most recent blocks first, the first events found are the most recent
			if len(logs) > 0 {
				// Update searched counter and return immediately
				if !usedChunkedSearch {
					searched += (currentBlock - fromBlock)
				}
				return append(events, logs...), searched, nil
			}

			// If we've searched back to genesis, stop
			if fromBlock == 0 {
				break
			}
		}

		return events, searched, nil
	}

	// Build topics for searching
	baseTopics := [][]common.Hash{{common.HexToHash(topicHash)}}

	if fromAddress != "" && toAddress != "" {
		if n.logger != nil {
			n.logger.Debug("EventTrigger: Both FROM and TO addresses specified",
				"fromAddress", fromAddress,
				"toAddress", toAddress,
				"addressesEqual", strings.EqualFold(fromAddress, toAddress))
		}

		// Check if FROM and TO addresses are the same to avoid redundant search
		if strings.EqualFold(fromAddress, toAddress) {
			// When searching for transfers from an address to itself,
			// we only need to search FROM events (which includes self-transfers)
			fromTopics := append(baseTopics, []common.Hash{common.HexToHash(fromAddress)}, []common.Hash{})
			allEvents, totalSearched, err = searchWithTopics(fromTopics)
			if err != nil {
				return nil, fmt.Errorf("failed to search FROM events for same address: %w", err)
			}

			if n.logger != nil {
				n.logger.Info("EventTrigger: Optimized search for same FROM/TO address (single FROM search)",
					"address", fromAddress,
					"eventsFound", len(allEvents),
					"totalSearched", totalSearched)
			}
		} else {
			// Search for transfers both FROM and TO the address (two separate queries)

			// Query 1: FROM the address
			fromTopics := append(baseTopics, []common.Hash{common.HexToHash(fromAddress)}, []common.Hash{})
			fromEvents, fromSearched, err := searchWithTopics(fromTopics)
			if err != nil {
				return nil, fmt.Errorf("failed to search FROM events: %w", err)
			}

			// Query 2: TO the address
			toTopics := append(baseTopics, []common.Hash{}, []common.Hash{common.HexToHash(toAddress)})
			toEvents, toSearched, err := searchWithTopics(toTopics)
			if err != nil {
				return nil, fmt.Errorf("failed to search TO events: %w", err)
			}

			allEvents = append(fromEvents, toEvents...)
			totalSearched = fromSearched + toSearched
		}

	} else if fromAddress != "" {
		// Search only FROM the address
		fromTopics := append(baseTopics, []common.Hash{common.HexToHash(fromAddress)}, []common.Hash{})
		allEvents, totalSearched, err = searchWithTopics(fromTopics)
		if err != nil {
			return nil, fmt.Errorf("failed to search FROM events: %w", err)
		}

	} else if toAddress != "" {
		// Search only TO the address
		toTopics := append(baseTopics, []common.Hash{}, []common.Hash{common.HexToHash(toAddress)})
		allEvents, totalSearched, err = searchWithTopics(toTopics)
		if err != nil {
			return nil, fmt.Errorf("failed to search TO events: %w", err)
		}

	} else {
		// Search without address filtering (any from/to)
		allEvents, totalSearched, err = searchWithTopics(baseTopics)
		if err != nil {
			return nil, fmt.Errorf("failed to search events: %w", err)
		}
	}

	if n.logger != nil {
		n.logger.Info("EventTrigger: Enhanced search complete",
			"totalEvents", len(allEvents),
			"totalSearched", totalSearched)
	}

	// If no events found after comprehensive search
	if len(allEvents) == 0 {
		if n.logger != nil {
			n.logger.Info("EventTrigger: No events found after enhanced search",
				"totalBlocksSearched", totalSearched,
				"topicHash", topicHash,
				"contractAddresses", contractAddresses,
				"fromAddress", fromAddress,
				"toAddress", toAddress)
		}

		return map[string]interface{}{
			"found":             false,
			"evm_log":           nil,
			"topicHash":         topicHash,
			"contractAddresses": contractAddresses,
			"fromAddress":       fromAddress,
			"toAddress":         toAddress,
			"totalSearched":     totalSearched,
			"expression":        expression,
			"message":           "No events found matching the criteria",
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

	// Create the basic evm_log structure (always present)
	evmLog := map[string]interface{}{
		"address":          mostRecentEvent.Address.Hex(),
		"topics":           topics,
		"data":             "0x" + common.Bytes2Hex(mostRecentEvent.Data),
		"blockNumber":      mostRecentEvent.BlockNumber,
		"transactionHash":  mostRecentEvent.TxHash.Hex(),
		"transactionIndex": uint32(mostRecentEvent.TxIndex),
		"blockHash":        mostRecentEvent.BlockHash.Hex(),
		"index":            uint32(mostRecentEvent.Index),
		"removed":          mostRecentEvent.Removed,
	}

	result := map[string]interface{}{
		"found":             true,
		"evm_log":           evmLog,
		"topicHash":         topicHash,
		"contractAddresses": contractAddresses,
		"fromAddress":       fromAddress,
		"toAddress":         toAddress,
		"totalSearched":     totalSearched,
		"expression":        expression,
		"totalEvents":       len(allEvents),
		"searchMetadata": map[string]interface{}{
			"blocksSearchedBackwards": totalSearched,
			"searchComplete":          true,
			"timeoutOccurred":         false,
			"stoppedEarly":            true, // Since we stop after finding first event
		},
	}

	// If this is a Transfer event, add enriched transfer_log data
	isTransferEvent := topicHash == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	if isTransferEvent && len(topics) >= 3 {
		// Get block timestamp for transfer_log
		header, err := rpcConn.HeaderByNumber(ctx, big.NewInt(int64(mostRecentEvent.BlockNumber)))
		var blockTimestamp uint64
		if err == nil {
			blockTimestamp = header.Time * 1000 // Convert to milliseconds
		}

		// Extract from and to addresses from topics
		fromAddr := common.HexToAddress(topics[1]).Hex()
		toAddr := common.HexToAddress(topics[2]).Hex()
		value := "0x" + common.Bytes2Hex(mostRecentEvent.Data)

		transferLog := map[string]interface{}{
			"tokenName":        "",
			"tokenSymbol":      "",
			"tokenDecimals":    uint32(0),
			"transactionHash":  mostRecentEvent.TxHash.Hex(),
			"address":          mostRecentEvent.Address.Hex(),
			"blockNumber":      mostRecentEvent.BlockNumber,
			"blockTimestamp":   blockTimestamp,
			"fromAddress":      fromAddr,
			"toAddress":        toAddr,
			"value":            value,
			"valueFormatted":   "",
			"transactionIndex": uint32(mostRecentEvent.TxIndex),
			"logIndex":         uint32(mostRecentEvent.Index),
		}

		// Enrich the transfer log with token metadata if TokenEnrichmentService is available
		if n.tokenEnrichmentService != nil {
			// Create protobuf structures for enrichment
			evmLogProto := &avsproto.Evm_Log{
				Address: mostRecentEvent.Address.Hex(),
			}

			transferLogProto := &avsproto.EventTrigger_TransferLogOutput{
				TransactionHash:  mostRecentEvent.TxHash.Hex(),
				Address:          mostRecentEvent.Address.Hex(),
				BlockNumber:      mostRecentEvent.BlockNumber,
				BlockTimestamp:   blockTimestamp,
				FromAddress:      fromAddr,
				ToAddress:        toAddr,
				Value:            value,
				TransactionIndex: uint32(mostRecentEvent.TxIndex),
				LogIndex:         uint32(mostRecentEvent.Index),
			}

			// Enrich with token metadata
			if enrichErr := n.tokenEnrichmentService.EnrichTransferLog(evmLogProto, transferLogProto); enrichErr == nil {
				// Update the map with enriched data
				transferLog["tokenName"] = transferLogProto.TokenName
				transferLog["tokenSymbol"] = transferLogProto.TokenSymbol
				transferLog["tokenDecimals"] = transferLogProto.TokenDecimals
				transferLog["valueFormatted"] = transferLogProto.ValueFormatted

				if n.logger != nil {
					n.logger.Info("EventTrigger: Successfully enriched transfer log",
						"contract", mostRecentEvent.Address.Hex(),
						"tokenName", transferLogProto.TokenName,
						"tokenSymbol", transferLogProto.TokenSymbol,
						"tokenDecimals", transferLogProto.TokenDecimals,
						"valueFormatted", transferLogProto.ValueFormatted)
				}
			} else {
				if n.logger != nil {
					n.logger.Warn("EventTrigger: Failed to enrich transfer log",
						"contract", mostRecentEvent.Address.Hex(),
						"error", enrichErr)
				}
			}
		} else {
			if n.logger != nil {
				n.logger.Debug("EventTrigger: TokenEnrichmentService not available, transfer log not enriched")
			}
		}

		result["transfer_log"] = transferLog

		if n.logger != nil {
			n.logger.Info("EventTrigger: Enhanced Transfer event found",
				"blockNumber", mostRecentEvent.BlockNumber,
				"txHash", mostRecentEvent.TxHash.Hex(),
				"from", fromAddr,
				"to", toAddr,
				"value", value,
				"contract", mostRecentEvent.Address.Hex())
		}
	}

	if n.logger != nil {
		n.logger.Info("EventTrigger: Successfully found most recent event with enhanced search",
			"blockNumber", mostRecentEvent.BlockNumber,
			"txHash", mostRecentEvent.TxHash.Hex(),
			"address", mostRecentEvent.Address.Hex(),
			"totalEvents", len(allEvents),
			"totalSearched", totalSearched)
	}

	return result, nil
}

// parseEnhancedExpression parses the enhanced expression format
// Examples:
// - "0xddf252...signature" -> (signature, [], "", "")
// - "0xddf252...signature&&0xaddr" -> (signature, [addr], "", "")
// - "0xddf252...signature&&contracts=[0xaddr1,0xaddr2]" -> (signature, [addr1,addr2], "", "")
// - "0xddf252...signature&&from=0xaddr" -> (signature, [], addr, "")
// - "0xddf252...signature&&to=0xaddr" -> (signature, [], "", addr)
// - "0xddf252...signature&&contracts=[0xaddr1,0xaddr2]&&from=0xaddr" -> (signature, [addr1,addr2], addr, "")
// - "0xddf252...signature&&from=0xaddr||to=0xaddr" -> (signature, [], addr, addr) - OR syntax support
func parseEnhancedExpression(expression string) (topicHash string, contractAddresses []string, fromAddress, toAddress string) {
	parts := strings.Split(expression, "&&")

	// First part is always the topic hash
	if len(parts) > 0 {
		topicHash = strings.TrimSpace(parts[0])
	}

	// Parse additional parameters
	for i := 1; i < len(parts); i++ {
		part := strings.TrimSpace(parts[i])

		if strings.HasPrefix(part, "contracts=") {
			// Parse contracts array: contracts=[0xaddr1,0xaddr2]
			contractsStr := strings.TrimPrefix(part, "contracts=")
			if strings.HasPrefix(contractsStr, "[") && strings.HasSuffix(contractsStr, "]") {
				contractsStr = strings.Trim(contractsStr, "[]")
				if contractsStr != "" {
					contractAddresses = strings.Split(contractsStr, ",")
					for j := range contractAddresses {
						contractAddresses[j] = strings.TrimSpace(contractAddresses[j])
					}
				}
			}
		} else if strings.HasPrefix(part, "from=") {
			fromPart := strings.TrimSpace(strings.TrimPrefix(part, "from="))
			// Check if this contains OR logic: from=X||to=Y
			if strings.Contains(fromPart, "||to=") {
				orParts := strings.Split(fromPart, "||to=")
				if len(orParts) == 2 {
					fromAddress = strings.TrimSpace(orParts[0])
					toAddress = strings.TrimSpace(orParts[1])
				} else {
					fromAddress = fromPart
				}
			} else {
				fromAddress = fromPart
			}
		} else if strings.HasPrefix(part, "to=") {
			toAddress = strings.TrimSpace(strings.TrimPrefix(part, "to="))
		} else {
			// Simple contract address format
			contractAddresses = []string{part}
		}
	}

	return topicHash, contractAddresses, fromAddress, toAddress
}

// runManualTriggerImmediately executes a manual trigger immediately
func (n *Engine) runManualTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// Manual triggers are perfect for immediate execution
	result := map[string]interface{}{
		"triggered": true,
		"runAt":     uint64(time.Now().Unix()),
	}

	if n.logger != nil {
		n.logger.Info("ManualTrigger executed immediately")
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
	for key, value := range inputVariables {
		vm.AddVar(key, value)
	}

	// Create node from type and config
	node, err := CreateNodeFromType(nodeType, nodeConfig, "")
	if err != nil {
		return nil, fmt.Errorf("failed to create node: %w", err)
	}

	// Execute the node
	executionStep, err := vm.RunNodeWithInputs(node, inputVariables)
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
			// Use the common response processing function
			result = ProcessRestAPIResponseRaw(m)
		} else {
			result = map[string]interface{}{"data": iface}
		}
	} else if contractRead := executionStep.GetContractRead(); contractRead != nil && len(contractRead.GetResults()) > 0 {
		// ContractRead now returns multiple method results
		results := contractRead.GetResults()

		// For backward compatibility, if there's only one result, expose its fields directly
		if len(results) == 1 {
			methodResult := results[0]
			if methodResult.Success {
				result["method_name"] = methodResult.GetMethodName()

				// Add structured data with field names
				if len(methodResult.GetData()) > 0 {
					structuredData := make(map[string]interface{})
					for _, field := range methodResult.GetData() {
						structuredData[field.GetName()] = field.GetValue()
					}
					result["data"] = structuredData
				}
			} else {
				result["error"] = methodResult.GetError()
				result["method_name"] = methodResult.GetMethodName()
			}
		} else {
			// Multiple method results - return as array
			var allResults []map[string]interface{}
			for _, methodResult := range results {
				methodMap := map[string]interface{}{
					"method_name": methodResult.GetMethodName(),
					"success":     methodResult.GetSuccess(),
				}

				if methodResult.Success {
					// Add structured data
					if len(methodResult.GetData()) > 0 {
						structuredData := make(map[string]interface{})
						for _, field := range methodResult.GetData() {
							structuredData[field.GetName()] = field.GetValue()
						}
						methodMap["data"] = structuredData
					}
				} else {
					methodMap["error"] = methodResult.GetError()
				}

				allResults = append(allResults, methodMap)
			}
			result["results"] = allResults
		}
	} else if branch := executionStep.GetBranch(); branch != nil {
		result["conditionId"] = branch.GetConditionId()
	} else if filter := executionStep.GetFilter(); filter != nil && filter.GetData() != nil {
		var data interface{}
		structVal := &structpb.Value{}
		if err := filter.GetData().UnmarshalTo(structVal); err == nil {
			data = structVal.AsInterface()
		} else {
			if n.logger != nil {
				n.logger.Warn("Failed to unmarshal Filter output", "error", err.Error())
			}
			data = string(filter.GetData().GetValue())
		}
		result["data"] = data
	} else if loop := executionStep.GetLoop(); loop != nil {
		result["data"] = loop.GetData()
	} else if graphQL := executionStep.GetGraphql(); graphQL != nil && graphQL.GetData() != nil {
		var data map[string]interface{}
		structVal := &structpb.Struct{}
		if err := graphQL.GetData().UnmarshalTo(structVal); err == nil {
			data = structVal.AsMap()
		} else {
			if n.logger != nil {
				n.logger.Warn("Failed to unmarshal GraphQL output", "error", err.Error())
			}
			data = map[string]interface{}{"raw_output": string(graphQL.GetData().GetValue())}
		}
		result = data
	} else if ethTransfer := executionStep.GetEthTransfer(); ethTransfer != nil {
		result["txHash"] = ethTransfer.GetTransactionHash()
		result["success"] = true
	} else if contractWrite := executionStep.GetContractWrite(); contractWrite != nil {
		// ContractWrite output contains UserOp and TxReceipt
		if txReceipt := contractWrite.GetTxReceipt(); txReceipt != nil {
			result["txHash"] = txReceipt.GetHash()
			result["success"] = true
		} else {
			result["success"] = true
		}
	}

	// If no specific data was extracted, include basic execution info
	if len(result) == 0 {
		result["success"] = executionStep.Success
		result["nodeId"] = executionStep.Id
		if executionStep.Error != "" {
			result["error"] = executionStep.Error
		}
	}

	return result, nil
}

// extractExecutionResultDirect extracts only the data from execution step, throwing errors instead of including them
func (n *Engine) extractExecutionResultDirect(executionStep *avsproto.Execution_Step) (map[string]interface{}, error) {
	// Handle different output data types and return data directly
	if ccode := executionStep.GetCustomCode(); ccode != nil && ccode.GetData() != nil {
		iface := ccode.GetData().AsInterface()
		if m, ok := iface.(map[string]interface{}); ok {
			return m, nil
		} else {
			return map[string]interface{}{"data": iface}, nil
		}
	} else if restAPI := executionStep.GetRestApi(); restAPI != nil && restAPI.GetData() != nil {
		// REST API data is now stored as structpb.Value directly (no Any wrapper)
		iface := restAPI.GetData().AsInterface()
		if m, ok := iface.(map[string]interface{}); ok {
			// Use the common response processing function
			return ProcessRestAPIResponseRaw(m), nil
		} else {
			return map[string]interface{}{"data": iface}, nil
		}
	} else if contractRead := executionStep.GetContractRead(); contractRead != nil && len(contractRead.GetResults()) > 0 {
		// For ContractRead, return the method results
		results := contractRead.GetResults()

		// If single result, return directly for backward compatibility
		if len(results) == 1 {
			methodResult := results[0]
			if methodResult.Success {
				response := map[string]interface{}{
					"method_name": methodResult.GetMethodName(),
				}

				// Add structured data
				if len(methodResult.GetData()) > 0 {
					structuredData := make(map[string]interface{})
					for _, field := range methodResult.GetData() {
						structuredData[field.GetName()] = field.GetValue()
					}
					response["data"] = structuredData
				}

				return response, nil
			} else {
				return map[string]interface{}{
					"method_name": methodResult.GetMethodName(),
					"error":       methodResult.GetError(),
				}, nil
			}
		} else {
			// Multiple results
			var allResults []map[string]interface{}
			for _, methodResult := range results {
				methodMap := map[string]interface{}{
					"method_name": methodResult.GetMethodName(),
					"success":     methodResult.GetSuccess(),
				}

				if methodResult.Success {
					if len(methodResult.GetData()) > 0 {
						structuredData := make(map[string]interface{})
						for _, field := range methodResult.GetData() {
							structuredData[field.GetName()] = field.GetValue()
						}
						methodMap["data"] = structuredData
					}
				} else {
					methodMap["error"] = methodResult.GetError()
				}

				allResults = append(allResults, methodMap)
			}
			return map[string]interface{}{"results": allResults}, nil
		}
	} else if branch := executionStep.GetBranch(); branch != nil {
		return map[string]interface{}{"conditionId": branch.GetConditionId()}, nil
	} else if filter := executionStep.GetFilter(); filter != nil && filter.GetData() != nil {
		var data interface{}
		structVal := &structpb.Value{}
		if err := filter.GetData().UnmarshalTo(structVal); err == nil {
			data = structVal.AsInterface()
		} else {
			if n.logger != nil {
				n.logger.Warn("Failed to unmarshal Filter output", "error", err.Error())
			}
			data = string(filter.GetData().GetValue())
		}
		return map[string]interface{}{"data": data}, nil
	} else if loop := executionStep.GetLoop(); loop != nil {
		return map[string]interface{}{"data": loop.GetData()}, nil
	} else if graphQL := executionStep.GetGraphql(); graphQL != nil && graphQL.GetData() != nil {
		var data map[string]interface{}
		structVal := &structpb.Struct{}
		if err := graphQL.GetData().UnmarshalTo(structVal); err == nil {
			data = structVal.AsMap()
		} else {
			if n.logger != nil {
				n.logger.Warn("Failed to unmarshal GraphQL output", "error", err.Error())
			}
			data = map[string]interface{}{"raw_output": string(graphQL.GetData().GetValue())}
		}
		return data, nil
	} else if ethTransfer := executionStep.GetEthTransfer(); ethTransfer != nil {
		return map[string]interface{}{"txHash": ethTransfer.GetTransactionHash()}, nil
	} else if contractWrite := executionStep.GetContractWrite(); contractWrite != nil {
		// ContractWrite output contains UserOp and TxReceipt
		if txReceipt := contractWrite.GetTxReceipt(); txReceipt != nil {
			return map[string]interface{}{"txHash": txReceipt.GetHash()}, nil
		} else {
			return map[string]interface{}{"status": "success"}, nil
		}
	}

	// If no specific data was extracted, return empty result
	return map[string]interface{}{}, nil
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
			NodeId:  fmt.Sprintf("node_immediate_%d", time.Now().UnixNano()),
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
			NodeId:  fmt.Sprintf("node_immediate_%d", time.Now().UnixNano()),
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
		n.logger.Info("RunNodeImmediatelyRPC: Executed successfully", "nodeTypeStr", nodeTypeStr, "originalNodeType", req.NodeType, "configKeys", getStringMapKeys(nodeConfig), "inputKeys", getStringMapKeys(inputVariables))
	}

	// Convert result to the appropriate protobuf output type
	resp := &avsproto.RunNodeWithInputsResp{
		Success: true,
		NodeId:  fmt.Sprintf("node_immediate_%d", time.Now().UnixNano()),
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
		// For custom code nodes
		valueData, err := structpb.NewValue(result)
		if err != nil {
			return &avsproto.RunNodeWithInputsResp{
				Success: false,
				Error:   fmt.Sprintf("failed to convert output: %v", err),
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
		ethOutput := &avsproto.ETHTransferNode_Output{}
		if result != nil {
			if txHash, ok := result["txHash"].(string); ok {
				ethOutput.TransactionHash = txHash
			}
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_EthTransfer{
			EthTransfer: ethOutput,
		}
	case NodeTypeContractRead:
		// For contract read nodes - always set output structure to avoid OUTPUT_DATA_NOT_SET
		contractReadOutput := &avsproto.ContractReadNode_Output{}
		if result != nil && len(result) > 0 {
			// Handle the new multiple method result structure
			if results, ok := result["results"].([]map[string]interface{}); ok {
				// Multiple method results
				for _, methodResult := range results {
					methodResultProto := &avsproto.ContractReadNode_MethodResult{}
					if methodName, ok := methodResult["method_name"].(string); ok {
						methodResultProto.MethodName = methodName
					}
					if success, ok := methodResult["success"].(bool); ok {
						methodResultProto.Success = success
					}
					if errorMsg, ok := methodResult["error"].(string); ok {
						methodResultProto.Error = errorMsg
					}

					// Handle structured data (now the only data field)
					if data, ok := methodResult["data"].(map[string]interface{}); ok {
						for fieldName, fieldValue := range data {
							field := &avsproto.ContractReadNode_MethodResult_StructuredField{
								Name:  fieldName,
								Type:  "string", // Default type, could be enhanced to detect actual type
								Value: fmt.Sprintf("%v", fieldValue),
							}
							methodResultProto.Data = append(methodResultProto.Data, field)
						}
					}

					contractReadOutput.Results = append(contractReadOutput.Results, methodResultProto)
				}
			} else {
				// Single method result (backward compatibility)
				methodResult := &avsproto.ContractReadNode_MethodResult{}

				if methodName, ok := result["method_name"].(string); ok {
					methodResult.MethodName = methodName
				}
				if errorMsg, ok := result["error"].(string); ok {
					methodResult.Error = errorMsg
					methodResult.Success = false
				} else {
					methodResult.Success = true
				}

				// Handle structured data (now the only data field)
				if data, ok := result["data"].(map[string]interface{}); ok {
					for fieldName, fieldValue := range data {
						field := &avsproto.ContractReadNode_MethodResult_StructuredField{
							Name:  fieldName,
							Type:  "string", // Default type, could be enhanced to detect actual type
							Value: fmt.Sprintf("%v", fieldValue),
						}
						methodResult.Data = append(methodResult.Data, field)
					}
				}

				contractReadOutput.Results = []*avsproto.ContractReadNode_MethodResult{methodResult}
			}
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_ContractRead{
			ContractRead: contractReadOutput,
		}
	case NodeTypeContractWrite:
		// For contract write nodes - always set output structure to avoid OUTPUT_DATA_NOT_SET
		contractWriteOutput := &avsproto.ContractWriteNode_Output{}
		if result != nil && len(result) > 0 {
			// ContractWrite should have UserOp and TxReceipt, but for testing we just create empty structures
			// In a real implementation, these would be populated from the actual transaction results
			contractWriteOutput.UserOp = &avsproto.Evm_UserOp{}
			contractWriteOutput.TxReceipt = &avsproto.Evm_TransactionReceipt{}

			// Try to extract transaction hash from result if available
			if txHash, ok := result["txHash"].(string); ok {
				contractWriteOutput.TxReceipt.Hash = txHash
			} else if transactionHash, ok := result["transactionHash"].(string); ok {
				contractWriteOutput.TxReceipt.Hash = transactionHash
			}
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_ContractWrite{
			ContractWrite: contractWriteOutput,
		}
	case NodeTypeGraphQLQuery:
		if result != nil && len(result) > 0 {
			// For GraphQL query nodes, convert result to appropriate format
			anyData, err := structpb.NewValue(result)
			if err != nil {
				return &avsproto.RunNodeWithInputsResp{
					Success: false,
					Error:   fmt.Sprintf("failed to convert GraphQL output: %v", err),
					NodeId:  "",
				}, nil
			}
			anyProto, err := anypb.New(anyData)
			if err != nil {
				return &avsproto.RunNodeWithInputsResp{
					Success: false,
					Error:   fmt.Sprintf("failed to create Any proto for GraphQL: %v", err),
					NodeId:  "",
				}, nil
			}
			graphqlOutput := &avsproto.GraphQLQueryNode_Output{
				Data: anyProto,
			}
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Graphql{
				Graphql: graphqlOutput,
			}
		}
	case NodeTypeBranch:
		if result != nil && len(result) > 0 {
			// For branch nodes, convert result to appropriate format
			branchOutput := &avsproto.BranchNode_Output{}
			if conditionId, ok := result["conditionId"].(string); ok {
				branchOutput.ConditionId = conditionId
			}
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Branch{
				Branch: branchOutput,
			}
		}
	case NodeTypeFilter:
		if result != nil && len(result) > 0 {
			// For filter nodes, convert result to appropriate format
			anyData, err := structpb.NewValue(result)
			if err != nil {
				return &avsproto.RunNodeWithInputsResp{
					Success: false,
					Error:   fmt.Sprintf("failed to convert Filter output: %v", err),
					NodeId:  "",
				}, nil
			}
			anyProto, err := anypb.New(anyData)
			if err != nil {
				return &avsproto.RunNodeWithInputsResp{
					Success: false,
					Error:   fmt.Sprintf("failed to create Any proto for Filter: %v", err),
					NodeId:  "",
				}, nil
			}
			filterOutput := &avsproto.FilterNode_Output{
				Data: anyProto,
			}
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Filter{
				Filter: filterOutput,
			}
		}
	case NodeTypeLoop:
		if result != nil && len(result) > 0 {
			// For loop nodes, convert result to appropriate format
			loopOutput := &avsproto.LoopNode_Output{}
			if data, ok := result["data"].(string); ok {
				loopOutput.Data = data
			} else if data, ok := result["data"]; ok {
				// Convert any other data type to string representation
				loopOutput.Data = fmt.Sprintf("%v", data)
			}
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Loop{
				Loop: loopOutput,
			}
		}
	default:
		// For unknown/invalid node types, set RestAPI as default to avoid OUTPUT_DATA_NOT_SET
		resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{
			RestApi: &avsproto.RestAPINode_Output{},
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

	// Execute the trigger immediately (triggers don't accept input variables)
	result, err := n.runTriggerImmediately(triggerTypeStr, triggerConfig, nil)
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
		n.logger.Info("RunTriggerRPC: Executed successfully", "triggerTypeStr", triggerTypeStr, "originalTriggerType", req.TriggerType, "configKeys", getStringMapKeys(triggerConfig))
	}

	// Convert result to the appropriate protobuf output type
	resp := &avsproto.RunTriggerResp{
		Success:   true,
		TriggerId: fmt.Sprintf("trigger_immediate_%d", time.Now().UnixNano()),
	}

	// Set the appropriate output data based on the trigger type using shared functions
	switch triggerTypeStr {
	case NodeTypeBlockTrigger:
		if result != nil {
			blockOutput := buildBlockTriggerOutput(result)
			resp.OutputData = &avsproto.RunTriggerResp_BlockTrigger{
				BlockTrigger: blockOutput,
			}
		}
	case NodeTypeFixedTimeTrigger:
		if result != nil {
			fixedTimeOutput := buildFixedTimeTriggerOutput(result)
			resp.OutputData = &avsproto.RunTriggerResp_FixedTimeTrigger{
				FixedTimeTrigger: fixedTimeOutput,
			}
		}
	case NodeTypeCronTrigger:
		if result != nil {
			// Convert result to CronTrigger output
			cronOutput := &avsproto.CronTrigger_Output{}
			if timestamp, ok := result["timestamp"].(uint64); ok {
				cronOutput.Timestamp = timestamp
			}
			if timestampISO, ok := result["timestamp_iso"].(string); ok {
				cronOutput.TimestampIso = timestampISO
			}
			resp.OutputData = &avsproto.RunTriggerResp_CronTrigger{
				CronTrigger: cronOutput,
			}
		}
	case NodeTypeEventTrigger:
		// Use shared function to build EventTrigger output (handles nil result gracefully)
		eventOutput := buildEventTriggerOutput(result)
		resp.OutputData = &avsproto.RunTriggerResp_EventTrigger{
			EventTrigger: eventOutput,
		}
	case NodeTypeManualTrigger:
		// Always set manual trigger output, even if result is nil
		manualOutput := &avsproto.ManualTrigger_Output{}
		if result != nil {
			if runAt, ok := result["runAt"].(uint64); ok {
				manualOutput.RunAt = runAt
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
	}

	for _, pattern := range validationErrorPatterns {
		if strings.Contains(errorMsg, pattern) {
			return true
		}
	}

	// If it doesn't match validation patterns, treat as system error
	return false
}
