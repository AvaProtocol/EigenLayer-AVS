package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"google.golang.org/protobuf/types/known/structpb"
)

// SharedEventEnrichmentParams contains all parameters needed for event enrichment
type SharedEventEnrichmentParams struct {
	EventLog               *types.Log
	ContractABI            []*structpb.Value              // For consistency with existing protobuf format
	Query                  *avsproto.EventTrigger_Query   // Primary query for ABI parsing
	AllQueries             []*avsproto.EventTrigger_Query // All queries in task for direction determination
	TokenEnrichmentService *TokenEnrichmentService
	RpcClient              *ethclient.Client
	Logger                 sdklogging.Logger
	ChainID                int64 // Chain ID for the event
}

// SharedEventEnrichmentResult contains the enriched event data
type SharedEventEnrichmentResult struct {
	ParsedData      map[string]interface{}
	IsTransferEvent bool
	EventName       string
}

// EnrichEventWithTokenMetadata is the shared function that enriches events with token metadata
// This replaces the duplicate logic in both simulation and real event processing
func EnrichEventWithTokenMetadata(params SharedEventEnrichmentParams) (*SharedEventEnrichmentResult, error) {
	result := &SharedEventEnrichmentResult{
		ParsedData:      make(map[string]interface{}),
		IsTransferEvent: false,
	}

	// Parse event data using ABI if provided
	if len(params.ContractABI) > 0 {
		if params.Logger != nil {
			params.Logger.Info("🔍 SharedEventEnrichment: Using ABI parsing",
				"hasABI", true,
				"abiElementCount", len(params.ContractABI))
		}

		// Use the existing shared ParseABIOptimized function from utils.go
		contractABI, err := ParseABIOptimized(params.ContractABI)
		if err != nil {
			if params.Logger != nil {
				params.Logger.Warn("Failed to parse ABI in shared enrichment", "error", err)
			}
			return result, err
		}

		// Use shared ABI parsing logic (extract from run_node_immediately.go)
		parsedData, eventName, err := parseEventWithABIShared(params.EventLog, contractABI, params.Query, params.Logger)
		if err != nil {
			if params.Logger != nil {
				params.Logger.Warn("Failed to parse event with ABI in shared enrichment", "error", err)
			}
			return result, err
		}

		result.ParsedData = parsedData
		result.EventName = eventName
		result.IsTransferEvent = (eventName == "Transfer")

		// If this is a Transfer event and we have token enrichment service, enrich it
		if result.IsTransferEvent && params.TokenEnrichmentService != nil {
			enrichedData, err := enrichTransferEventShared(
				params.EventLog,
				parsedData,
				params.Query,
				params.AllQueries, // Pass all queries for direction determination
				params.TokenEnrichmentService,
				params.RpcClient,
				params.Logger,
			)
			if err != nil {
				if params.Logger != nil {
					params.Logger.Warn("Failed to enrich Transfer event", "error", err)
				}
				// Continue with basic parsed data if enrichment fails
			} else {
				result.ParsedData = enrichedData
			}
		}
	} else {
		if params.Logger != nil {
			params.Logger.Info("🔍 SharedEventEnrichment: No ABI provided, using raw log data")
		}
		// Create basic metadata structure for events without ABI
		result.ParsedData = createBasicEventMetadata(params.EventLog)
	}

	return result, nil
}

// parseEventWithABIShared contains the shared ABI parsing logic
// TODO: Extract and refactor from run_node_immediately.go parseEventWithParsedABI
func parseEventWithABIShared(eventLog *types.Log, contractABI *abi.ABI, query *avsproto.EventTrigger_Query, logger sdklogging.Logger) (map[string]interface{}, string, error) {
	// Find the matching event in ABI using the first topic (event signature)
	if len(eventLog.Topics) == 0 {
		return nil, "", fmt.Errorf("event log has no topics")
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
		return nil, "", fmt.Errorf("no matching event found in ABI for signature %s", eventSignature.Hex())
	}

	// Decode the event data
	decodedData, err := contractABI.Unpack(eventName, eventLog.Data)
	if err != nil {
		return nil, "", fmt.Errorf("failed to decode event data for %s: %w", eventName, err)
	}

	// Build parsed data map with both indexed and non-indexed fields
	parsedData := make(map[string]interface{})
	parsedData["eventName"] = eventName

	// Add indexed parameters from topics (skip topic[0] which is event signature)
	indexedCount := 0
	nonIndexedCount := 0

	for _, input := range matchingEvent.Inputs {
		if input.Indexed {
			// Get from topics (topic[0] is signature, so indexed params start from topic[1])
			topicIndex := indexedCount + 1
			if topicIndex < len(eventLog.Topics) {
				if input.Type.T == abi.AddressTy {
					// Convert hash to address for indexed address parameters
					parsedData[input.Name] = common.HexToAddress(eventLog.Topics[topicIndex].Hex()).Hex()
				} else {
					parsedData[input.Name] = eventLog.Topics[topicIndex]
				}
			}
			indexedCount++
		} else {
			// Get from decoded data
			if nonIndexedCount < len(decodedData) {
				parsedData[input.Name] = decodedData[nonIndexedCount]
			}
			nonIndexedCount++
		}
	}

	return parsedData, eventName, nil
}

// enrichTransferEventShared enriches Transfer events with token metadata
// This is extracted from the enrichment logic in run_node_immediately.go
func enrichTransferEventShared(eventLog *types.Log, parsedData map[string]interface{}, query *avsproto.EventTrigger_Query, allQueries []*avsproto.EventTrigger_Query, tokenService *TokenEnrichmentService, rpcClient *ethclient.Client, logger sdklogging.Logger) (map[string]interface{}, error) {
	if logger != nil {
		logger.Info("🎯 SharedEventEnrichment: Enriching Transfer event with token metadata",
			"contract", eventLog.Address.Hex(),
			"hasTokenService", tokenService != nil)
	}

	// Get token metadata from the enrichment service
	tokenMetadata, err := tokenService.GetTokenMetadata(eventLog.Address.Hex())
	if err != nil {
		if logger != nil {
			logger.Warn("Failed to get token metadata for Transfer event", "error", err, "contract", eventLog.Address.Hex())
		}
	}

	// Create standardized transfer event response structure using shared function
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

		// Format the value using token metadata
		if rawValue, ok := parsedData["value"]; ok {
			var valueStr string

			// Handle different value types (big.Int, string, etc.)
			switch v := rawValue.(type) {
			case *big.Int:
				valueStr = v.String()
			case string:
				valueStr = v
			default:
				valueStr = fmt.Sprintf("%v", v)
			}

			formattedValue := tokenService.FormatTokenValue(valueStr, tokenMetadata.Decimals)
			transferResponse.Value = formattedValue
		}

		if logger != nil {
			logger.Info("✅ SharedEventEnrichment: Transfer event enrichment completed",
				"tokenSymbol", tokenMetadata.Symbol,
				"tokenName", tokenMetadata.Name,
				"decimals", tokenMetadata.Decimals,
				"value", transferResponse.Value)
		}
	}

	// Add proper blockTimestamp for real events
	if eventLog.BlockNumber > 0 && rpcClient != nil {
		// Get actual block timestamp for real events
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		header, err := rpcClient.HeaderByNumber(ctx, big.NewInt(int64(eventLog.BlockNumber)))
		cancel()

		if err == nil {
			transferResponse.BlockTimestamp = int64(header.Time) * 1000 // Convert to milliseconds for JavaScript compatibility
		}
		// If error, keep the default timestamp from CreateStandardizedTransferResponse
	}

	// Determine user wallet and direction from all queries in the task
	userWalletAddress, direction := determineDirectionFromAllQueries(allQueries, transferResponse.FromAddress, transferResponse.ToAddress)

	// Convert standardized response to map for compatibility with existing code
	enrichedData := map[string]interface{}{

		"tokenName":       transferResponse.TokenName,
		"tokenSymbol":     transferResponse.TokenSymbol,
		"tokenDecimals":   transferResponse.TokenDecimals,
		"tokenContract":   transferResponse.Address, // Renamed from "address" for clarity
		"transactionHash": transferResponse.TransactionHash,
		"blockNumber":     transferResponse.BlockNumber,
		"blockTimestamp":  transferResponse.BlockTimestamp,
		"fromAddress":     transferResponse.FromAddress,
		"toAddress":       transferResponse.ToAddress,
		"value":           transferResponse.Value,
		"eventName":       "Transfer",
		"direction":       direction,                 // New field indicating "sent" or "received"
		"walletAddress":   userWalletAddress,         // New field with detected user wallet
		"chainId":         tokenService.GetChainID(), // Add chainId for chain name resolution
	}

	return enrichedData, nil
}

// determineDirectionFromAllQueries analyzes all EventTrigger queries to determine user wallet and transaction direction
func determineDirectionFromAllQueries(allQueries []*avsproto.EventTrigger_Query, fromAddress, toAddress string) (string, string) {
	if len(allQueries) == 0 {
		return "", "unknown"
	}

	// Check all queries to find the user's wallet address
	for _, query := range allQueries {
		userWallet, direction := determineDirectionFromQueries(query, fromAddress, toAddress)
		if userWallet != "" && direction != "unknown" {
			return userWallet, direction
		}
	}

	return "", "unknown"
}

// determineDirectionFromQueries analyzes the EventTrigger queries to determine user wallet and transaction direction
func determineDirectionFromQueries(query *avsproto.EventTrigger_Query, fromAddress, toAddress string) (string, string) {
	if query == nil || len(query.Topics) == 0 {
		return "", "unknown"
	}

	// Extract the user wallet address from the query topics
	// Look for non-empty addresses in topic[1] (from) or topic[2] (to) positions
	var userWallet string

	for _, topic := range query.Topics {
		if len(topic.Values) >= 3 {
			// Topic[1] = from address, Topic[2] = to address in Transfer events
			fromAddr := topic.Values[1]
			toAddr := topic.Values[2]

			// Check if fromAddr is specified (not empty/null) - this means user is sender
			if fromAddr != "" && fromAddr != "0x" && !strings.Contains(strings.ToLower(fromAddr), "null") {
				cleanFromAddr := extractAddressFromPaddedHex(fromAddr)
				if cleanFromAddr != "" {
					userWallet = cleanFromAddr
					break
				}
			}

			// Check if toAddr is specified (not empty/null) - this means user is receiver
			if toAddr != "" && toAddr != "0x" && !strings.Contains(strings.ToLower(toAddr), "null") {
				cleanToAddr := extractAddressFromPaddedHex(toAddr)
				if cleanToAddr != "" {
					userWallet = cleanToAddr
					break
				}
			}
		}
	}

	if userWallet == "" {
		return "", "unknown"
	}

	// Determine direction based on user wallet position in the actual event
	if strings.EqualFold(fromAddress, userWallet) {
		return userWallet, "sent"
	} else if strings.EqualFold(toAddress, userWallet) {
		return userWallet, "received"
	}

	return userWallet, "unknown"
}

// extractAddressFromPaddedHex converts padded hex (0x000...address) to clean address format
func extractAddressFromPaddedHex(paddedHex string) string {
	if len(paddedHex) != 66 { // 0x + 64 hex chars
		return ""
	}

	// Extract last 40 hex characters (20 bytes = address)
	addressPart := paddedHex[26:] // Skip 0x + 24 padding chars
	return "0x" + addressPart
}

// findCommonAddress finds addresses that appear in both fromAddresses and toAddresses lists
func findCommonAddress(fromAddresses, toAddresses []string) string {
	for _, fromAddr := range fromAddresses {
		for _, toAddr := range toAddresses {
			if strings.EqualFold(fromAddr, toAddr) {
				return fromAddr // Found common address (user wallet)
			}
		}
	}
	return ""
}

// createBasicEventMetadata creates basic metadata structure for events without ABI
func createBasicEventMetadata(eventLog *types.Log) map[string]interface{} {
	return map[string]interface{}{
		"tokenContract":   eventLog.Address.Hex(), // Renamed from "address" for clarity
		"blockNumber":     eventLog.BlockNumber,
		"transactionHash": eventLog.TxHash.Hex(),
		"logIndex":        eventLog.Index,
		"blockHash":       eventLog.BlockHash.Hex(),
		"topics":          eventLog.Topics,
		"data":            common.Bytes2Hex(eventLog.Data),
		"removed":         eventLog.Removed,
	}
}
