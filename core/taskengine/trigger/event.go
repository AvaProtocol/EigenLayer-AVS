package trigger

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/samber/lo"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

var (
// Legacy whitelist removed - no longer needed since we use dynamic filtering
// The system now only subscribes to events that registered tasks actually need
)

type EventMark struct {
	BlockNumber uint64
	LogIndex    uint
	TxHash      string
}

type Check struct {
	TaskMetadata *avsproto.SyncMessagesResp_TaskMetadata

	Program string
	Matcher []*avsproto.EventTrigger_Matcher
}

type EventTrigger struct {
	*CommonTrigger

	checks sync.Map

	// channel that we will push the trigger information back
	triggerCh chan TriggerMetadata[EventMark]

	// Track multiple subscriptions for complex filtering
	subscriptions []SubscriptionInfo
	subsMutex     sync.RWMutex
}

type SubscriptionInfo struct {
	subscription ethereum.Subscription
	query        ethereum.FilterQuery
	description  string
}

func NewEventTrigger(o *RpcOption, triggerCh chan TriggerMetadata[EventMark], logger sdklogging.Logger) *EventTrigger {
	var err error

	b := EventTrigger{
		CommonTrigger: &CommonTrigger{
			done:      make(chan bool),
			shutdown:  false,
			rpcOption: o,
			logger:    logger,
		},

		triggerCh:     triggerCh,
		checks:        sync.Map{},
		subscriptions: make([]SubscriptionInfo, 0),
	}

	b.ethClient, err = ethclient.Dial(o.RpcURL)
	if err != nil {
		panic(err)
	}

	b.wsEthClient, err = ethclient.Dial(o.WsRpcURL)

	if err != nil {
		panic(err)
	}

	return &b
}

// TODO: track remainExecution and expriedAt before merge
func (t *EventTrigger) AddCheck(check *avsproto.SyncMessagesResp_TaskMetadata) error {
	evt := check.GetTrigger().GetEvent()

	t.checks.Store(check.TaskId, &Check{
		Program:      evt.GetConfig().GetExpression(),
		Matcher:      evt.GetConfig().GetMatcher(),
		TaskMetadata: check,
	})

	return nil
}

func (t *EventTrigger) RemoveCheck(id string) error {
	t.checks.Delete(id)

	return nil
}

func (evtTrigger *EventTrigger) Run(ctx context.Context) error {
	// Build filter queries based on registered checks
	queries := evtTrigger.buildFilterQueries()

	if len(queries) == 0 {
		evtTrigger.logger.Info("no filter queries to subscribe to")
		return nil
	}

	// Create logs channel that all subscriptions will send to
	logs := make(chan types.Log, 1000) // Buffered to handle multiple subscriptions

	// Create subscriptions for each query
	evtTrigger.subsMutex.Lock()
	evtTrigger.subscriptions = make([]SubscriptionInfo, 0, len(queries))

	for i, queryInfo := range queries {
		sub, err := evtTrigger.wsEthClient.SubscribeFilterLogs(context.Background(), queryInfo.Query, logs)
		if err != nil {
			evtTrigger.logger.Error("failed to create subscription", "query_index", i, "description", queryInfo.Description, "error", err)
			// Clean up any successful subscriptions
			for _, subInfo := range evtTrigger.subscriptions {
				subInfo.subscription.Unsubscribe()
			}
			evtTrigger.subsMutex.Unlock()
			return err
		}

		evtTrigger.subscriptions = append(evtTrigger.subscriptions, SubscriptionInfo{
			subscription: sub,
			query:        queryInfo.Query,
			description:  queryInfo.Description,
		})

		evtTrigger.logger.Info("created subscription",
			"index", i,
			"description", queryInfo.Description,
			"addresses", queryInfo.Query.Addresses,
			"topics", queryInfo.Query.Topics)
	}
	evtTrigger.subsMutex.Unlock()

	// Create error channel that collects errors from all subscriptions
	errorCh := make(chan error, len(evtTrigger.subscriptions))

	// Start goroutines to monitor each subscription's error channel
	for i, subInfo := range evtTrigger.subscriptions {
		go func(index int, sub ethereum.Subscription, desc string) {
			err := <-sub.Err()
			if err != nil {
				evtTrigger.logger.Error("subscription error", "index", index, "description", desc, "error", err)
				errorCh <- err
			}
		}(i, subInfo.subscription, subInfo.description)
	}

	go func() {
		defer func() {
			// Clean up all subscriptions
			evtTrigger.subsMutex.RLock()
			for _, subInfo := range evtTrigger.subscriptions {
				subInfo.subscription.Unsubscribe()
			}
			evtTrigger.subsMutex.RUnlock()
		}()

		for {
			select {
			case <-ctx.Done():
				evtTrigger.logger.Info("context cancelled, stopping event trigger")
				return
			case <-evtTrigger.done:
				evtTrigger.logger.Info("event trigger shutdown signal received")
				return
			case err := <-errorCh:
				if err == nil {
					continue
				}
				evtTrigger.logger.Error("subscription error, attempting reconnection", "error", err)

				// Attempt to reconnect
				if err := evtTrigger.retryConnectToRpc(); err != nil {
					evtTrigger.logger.Error("failed to reconnect to RPC", "error", err)
					continue
				}

				// Rebuild and resubscribe
				evtTrigger.logger.Info("reconnected, rebuilding subscriptions")
				newQueries := evtTrigger.buildFilterQueries()

				evtTrigger.subsMutex.Lock()
				// Clean up old subscriptions
				for _, subInfo := range evtTrigger.subscriptions {
					subInfo.subscription.Unsubscribe()
				}
				evtTrigger.subscriptions = make([]SubscriptionInfo, 0, len(newQueries))

				// Create new subscriptions
				for i, queryInfo := range newQueries {
					sub, subErr := evtTrigger.wsEthClient.SubscribeFilterLogs(context.Background(), queryInfo.Query, logs)
					if subErr != nil {
						evtTrigger.logger.Error("failed to recreate subscription", "index", i, "error", subErr)
						continue
					}

					evtTrigger.subscriptions = append(evtTrigger.subscriptions, SubscriptionInfo{
						subscription: sub,
						query:        queryInfo.Query,
						description:  queryInfo.Description,
					})

					// Restart error monitoring for this subscription
					go func(idx int, s ethereum.Subscription, d string) {
						errSub := <-s.Err()
						if errSub != nil {
							errorCh <- errSub
						}
					}(i, sub, queryInfo.Description)
				}
				evtTrigger.subsMutex.Unlock()
			case event := <-logs:
				evtTrigger.logger.Debug("detect new event, evaluate checks", "event", event.Topics, "contract", event.Address, "tx", event.TxHash)
				// TODO: implement hint to avoid scan all checks
				toRemove := []string{}
				evtTrigger.progress += 1

				startTime := time.Now()
				checksCount := 0
				evtTrigger.checks.Range(func(key any, value any) bool {
					checksCount++
					if evtTrigger.shutdown {
						return false
					}

					check := value.(*Check)
					if hit, err := evtTrigger.Evaluate(&event, check); err == nil && hit {
						evtTrigger.logger.Info("check hit, notify aggregator", "task_id", key)
						evtTrigger.triggerCh <- TriggerMetadata[EventMark]{
							TaskID: key.(string),
							Marker: EventMark{
								BlockNumber: event.BlockNumber,
								LogIndex:    event.Index,
								TxHash:      event.TxHash.String(),
							},
						}

						evtTrigger.logger.Debug("check hit",
							"check", key,
							"tx_hash", event.TxHash,
						)

						// if check.metadata.Remain >= 0 {
						// 	if check.metadata.Remain == 1 {
						// 		toRemove = append(toRemove, key.(string))
						// 		check.metadata.Remain = -1
						// 	}
						// }
					}

					// We do want to continue other check no matter what outcome of previous one
					return true
				})

				duration := time.Since(startTime)
				evtTrigger.logger.Info("completed check evaluations",
					"checks_count", checksCount,
					"duration_ms", duration.Milliseconds(),
					"checks_per_second", float64(checksCount)/(float64(duration.Nanoseconds())/1e9))

				if len(toRemove) > 0 {
					for _, v := range toRemove {
						evtTrigger.checks.Delete(v)
					}
				}
			}
		}
	}()

	return nil
}

type QueryInfo struct {
	Query       ethereum.FilterQuery
	Description string
}

// buildFilterQueries creates multiple filter queries for efficient FROM-OR-TO filtering
func (evtTrigger *EventTrigger) buildFilterQueries() []QueryInfo {
	addressesMap := make(map[common.Address]bool)
	topicsMap := make(map[common.Hash]bool)
	fromAddressesMap := make(map[common.Address]bool)
	toAddressesMap := make(map[common.Address]bool)

	// Analyze all registered checks to collect filter requirements
	evtTrigger.checks.Range(func(key any, value any) bool {
		check := value.(*Check)

		// Parse different trigger types
		if len(check.Matcher) > 0 {
			// Handle simple matcher-based triggers
			for _, matcher := range check.Matcher {
				switch matcher.Type {
				case "address":
					if len(matcher.Value) > 0 && matcher.Value[0] != "" {
						if addr := common.HexToAddress(matcher.Value[0]); addr != (common.Address{}) {
							addressesMap[addr] = true
						}
					}
				case "topics":
					if len(matcher.Value) > 0 && matcher.Value[0] != "" {
						if topic := common.HexToHash(matcher.Value[0]); topic != (common.Hash{}) {
							topicsMap[topic] = true
						}
					}
				}
			}
		} else if check.Program != "" {
			// Parse advanced expression-based triggers
			triggerName := check.TaskMetadata.GetTrigger().GetName()

			// Try to extract information from the JavaScript expression
			contracts, topics, fromAddrs, toAddrs := evtTrigger.parseExpressionForFilters(check.Program, triggerName)

			for _, addr := range contracts {
				if addr != (common.Address{}) {
					addressesMap[addr] = true
				}
			}

			for _, topic := range topics {
				if topic != (common.Hash{}) {
					topicsMap[topic] = true
				}
			}

			for _, addr := range fromAddrs {
				if addr != (common.Address{}) {
					fromAddressesMap[addr] = true
				}
			}

			for _, addr := range toAddrs {
				if addr != (common.Address{}) {
					toAddressesMap[addr] = true
				}
			}
		}

		return true
	})

	// Convert maps to slices
	var addresses []common.Address
	for addr := range addressesMap {
		addresses = append(addresses, addr)
	}

	var queries []QueryInfo

	// Check if we have Transfer events and from/to filtering
	transferTopic := common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	hasTransferEvent := false
	for topic := range topicsMap {
		if topic == transferTopic {
			hasTransferEvent = true
			break
		}
	}

	hasFromFilter := len(fromAddressesMap) > 0
	hasToFilter := len(toAddressesMap) > 0

	// VALIDATION: Disallow overly broad Transfer event filters (similar to buildFilterQuery validation)
	if hasTransferEvent {
		// Case 1: Transfer events from any contract without proper filtering
		if len(addresses) == 0 && !hasFromFilter && !hasToFilter {
			evtTrigger.logger.Error("DISALLOWED in buildFilterQueries: Transfer events from any contract without from/to filtering")
			return []QueryInfo{} // Return empty to prevent any subscriptions
		}

		// Case 2: Transfer events from specific contracts but no from/to filtering
		if len(addresses) > 0 && !hasFromFilter && !hasToFilter {
			evtTrigger.logger.Error("DISALLOWED in buildFilterQueries: Transfer events from specific contracts without from/to filtering")
			evtTrigger.logger.Error("Contracts that would be affected:", "addresses", addresses)
			return []QueryInfo{} // Return empty to prevent any subscriptions
		}
	}

	if hasTransferEvent && hasFromFilter && hasToFilter {
		// Check if it's FROM-OR-TO for the same address
		sameAddressInBoth := false
		for fromAddr := range fromAddressesMap {
			if _, exists := toAddressesMap[fromAddr]; exists {
				sameAddressInBoth = true
				break
			}
		}

		if sameAddressInBoth {
			// Create two separate subscriptions: one for FROM, one for TO
			evtTrigger.logger.Info("Creating dual subscriptions for FROM-OR-TO filtering")

			// FROM subscription
			fromTopics := [][]common.Hash{
				{transferTopic}, // Transfer signature
				{},              // from addresses
			}
			for addr := range fromAddressesMap {
				fromTopics[1] = append(fromTopics[1], common.BytesToHash(addr.Bytes()))
			}

			queries = append(queries, QueryInfo{
				Query: ethereum.FilterQuery{
					Addresses: addresses,
					Topics:    fromTopics,
				},
				Description: fmt.Sprintf("Transfer FROM addresses (count: %d)", len(fromAddressesMap)),
			})

			// TO subscription
			toTopics := [][]common.Hash{
				{transferTopic}, // Transfer signature
				nil,             // any from address
				{},              // to addresses
			}
			for addr := range toAddressesMap {
				toTopics[2] = append(toTopics[2], common.BytesToHash(addr.Bytes()))
			}

			queries = append(queries, QueryInfo{
				Query: ethereum.FilterQuery{
					Addresses: addresses,
					Topics:    toTopics,
				},
				Description: fmt.Sprintf("Transfer TO addresses (count: %d)", len(toAddressesMap)),
			})

			evtTrigger.logger.Info("=== DUAL SUBSCRIPTION STRATEGY ===")
			evtTrigger.logger.Info("Subscription 1: Transfer FROM specific addresses")
			evtTrigger.logger.Info("Subscription 2: Transfer TO specific addresses")
			evtTrigger.logger.Info("Both will trigger the same task - much more efficient!")
			evtTrigger.logger.Info("=== END DUAL SUBSCRIPTION ===")

			return queries
		}
	}

	// Fallback to single subscription
	evtTrigger.logger.Info("Using single subscription strategy")
	return []QueryInfo{evtTrigger.convertToQueryInfo(evtTrigger.buildFilterQuery())}
}

// Helper function to convert FilterQuery to QueryInfo
func (evtTrigger *EventTrigger) convertToQueryInfo(query ethereum.FilterQuery) QueryInfo {
	description := "Single subscription"
	if len(query.Addresses) > 0 {
		description += fmt.Sprintf(" - contracts: %v", query.Addresses)
	}
	if len(query.Topics) > 0 {
		description += fmt.Sprintf(" - %d topic levels", len(query.Topics))
	}

	return QueryInfo{
		Query:       query,
		Description: description,
	}
}

// buildFilterQuery creates a dynamic filter query based on registered checks
//
// EXAMPLE FILTER QUERY STRUCTURES:
//
//  1. Basic Transfer event from any contract: ** DISALLOWED **
//     FilterQuery{
//     Addresses: nil,  // any contract
//     Topics: [
//     [0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef], // Transfer signature
//     ]
//     }
//     NOTE: This would capture ALL Transfer events from ALL contracts - too broad and inefficient.
//
//  2. Transfer from specific contract (no from/to filtering): ** DISALLOWED **
//     FilterQuery{
//     Addresses: [0x08210F9170F89Ab7658F0B5E3fF39b0E03C594D4],
//     Topics: [
//     [0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef], // Transfer signature
//     ]
//     }
//     NOTE: This would capture ALL Transfer events from specific contracts - still too broad.
//
//  3. Transfer FROM specific address (from any contract):
//     FilterQuery{
//     Addresses: nil,
//     Topics: [
//     [0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef], // Transfer signature
//     [0x000000000000000000000000fE66125343Aabda4A330DA667431eC1Acb7BbDA9], // from address (padded)
//     ]
//     }
//
//  4. Transfer TO specific address (from any contract):
//     FilterQuery{
//     Addresses: nil,
//     Topics: [
//     [0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef], // Transfer signature
//     nil, // any from address
//     [0x000000000000000000000000fE66125343Aabda4A330DA667431eC1Acb7BbDA9], // to address
//     ]
//     }
//
// NOTE: For "FROM or TO" scenarios, RPC filtering cannot do OR across topic positions!
// We need to use a more permissive filter and do the OR logic locally, or use multiple subscriptions.
// Current strategy: Use permissive filter (just event signature + contract) and filter locally.
//
// This dramatically reduces network traffic compared to subscribing to ALL events!
func (evtTrigger *EventTrigger) buildFilterQuery() ethereum.FilterQuery {
	addressesMap := make(map[common.Address]bool)
	topicsMap := make(map[common.Hash]bool)
	fromAddressesMap := make(map[common.Address]bool)
	toAddressesMap := make(map[common.Address]bool)

	// Analyze all registered checks to build comprehensive filter
	evtTrigger.checks.Range(func(key any, value any) bool {
		check := value.(*Check)

		// Parse different trigger types
		if len(check.Matcher) > 0 {
			// Handle simple matcher-based triggers
			for _, matcher := range check.Matcher {
				switch matcher.Type {
				case "address":
					if len(matcher.Value) > 0 && matcher.Value[0] != "" {
						if addr := common.HexToAddress(matcher.Value[0]); addr != (common.Address{}) {
							addressesMap[addr] = true
						}
					}
				case "topics":
					if len(matcher.Value) > 0 && matcher.Value[0] != "" {
						if topic := common.HexToHash(matcher.Value[0]); topic != (common.Hash{}) {
							topicsMap[topic] = true
						}
					}
				}
			}
		} else if check.Program != "" {
			// Parse advanced expression-based triggers
			triggerName := check.TaskMetadata.GetTrigger().GetName()

			// Try to extract information from the JavaScript expression
			contracts, topics, fromAddrs, toAddrs := evtTrigger.parseExpressionForFilters(check.Program, triggerName)

			for _, addr := range contracts {
				if addr != (common.Address{}) {
					addressesMap[addr] = true
				}
			}

			for _, topic := range topics {
				if topic != (common.Hash{}) {
					topicsMap[topic] = true
				}
			}

			for _, addr := range fromAddrs {
				if addr != (common.Address{}) {
					fromAddressesMap[addr] = true
				}
			}

			for _, addr := range toAddrs {
				if addr != (common.Address{}) {
					toAddressesMap[addr] = true
				}
			}
		}

		return true
	})

	// Convert maps to slices
	var addresses []common.Address
	for addr := range addressesMap {
		addresses = append(addresses, addr)
	}

	var topics [][]common.Hash
	if len(topicsMap) > 0 {
		var topicList []common.Hash
		for topic := range topicsMap {
			topicList = append(topicList, topic)
		}
		// First position is event signature/topic0
		topics = append(topics, topicList)

		// Handle from/to filtering for Transfer events
		if evtTrigger.hasTransferEvent(topicList) {
			hasFromFilter := len(fromAddressesMap) > 0
			hasToFilter := len(toAddressesMap) > 0

			if hasFromFilter && hasToFilter {
				// Check if it's the same address in both from and to
				sameAddressInBoth := false
				for fromAddr := range fromAddressesMap {
					if _, exists := toAddressesMap[fromAddr]; exists {
						sameAddressInBoth = true
						break
					}
				}

				if sameAddressInBoth {
					// For "from=X||to=X" (same address), use permissive filter and do OR logic locally
					evtTrigger.logger.Info("FROM-OR-TO filtering detected: using permissive filter + local evaluation")
					// Don't add topic filters - let all Transfer events through and filter locally
				} else {
					// Different addresses in from and to - this is likely an error in configuration
					evtTrigger.logger.Warn("FROM and TO filters have different addresses - using permissive filter")
				}
			} else if hasFromFilter {
				// Only FROM filter
				var fromTopics []common.Hash
				for addr := range fromAddressesMap {
					fromTopics = append(fromTopics, common.BytesToHash(addr.Bytes()))
				}
				topics = append(topics, fromTopics)
				evtTrigger.logger.Info("FROM-only filter applied", "from_addresses", fromTopics)
			} else if hasToFilter {
				// Only TO filter
				topics = append(topics, nil) // any from address
				var toTopics []common.Hash
				for addr := range toAddressesMap {
					toTopics = append(toTopics, common.BytesToHash(addr.Bytes()))
				}
				topics = append(topics, toTopics)
				evtTrigger.logger.Info("TO-only filter applied", "to_addresses", toTopics)
			}
		}
	}

	// If no specific filters detected, don't create any subscriptions
	// This means no tasks are registered, so no events need to be monitored
	if len(addresses) == 0 && len(topics) == 0 {
		evtTrigger.logger.Info("no registered tasks require event monitoring - no subscriptions needed")
		return ethereum.FilterQuery{} // Return empty filter - no subscriptions will be created
	}

	evtTrigger.logger.Info("built dynamic filter",
		"addresses_count", len(addresses),
		"topic_levels", len(topics),
		"from_addresses_count", len(fromAddressesMap),
		"to_addresses_count", len(toAddressesMap))

	// Log detailed filter structure for debugging
	if len(addresses) > 0 || len(topics) > 0 {
		evtTrigger.logger.Info("=== FILTER QUERY STRUCTURE ===")

		if len(addresses) > 0 {
			evtTrigger.logger.Info("Contract addresses filter:", "addresses", addresses)
			evtTrigger.logger.Info("  Example: Only events from these specific contracts will be received")
		}

		if len(topics) > 0 {
			for i, topicLevel := range topics {
				switch i {
				case 0:
					evtTrigger.logger.Info("Topic[0] - Event signatures:", "topics", topicLevel)
					evtTrigger.logger.Info("  Example: 0xddf252ad... = Transfer event signature")
				case 1:
					if len(topicLevel) > 0 {
						evtTrigger.logger.Info("Topic[1] - FROM addresses:", "topics", topicLevel)
						evtTrigger.logger.Info("  Example: Only Transfer events FROM these addresses")
					} else {
						evtTrigger.logger.Info("Topic[1] - FROM addresses: ANY (no filter)")
					}
				case 2:
					if len(topicLevel) > 0 {
						evtTrigger.logger.Info("Topic[2] - TO addresses:", "topics", topicLevel)
						evtTrigger.logger.Info("  Example: Only Transfer events TO these addresses")
					} else {
						evtTrigger.logger.Info("Topic[2] - TO addresses: ANY (no filter)")
					}
				default:
					evtTrigger.logger.Info("Topic[%d]:", i, "topics", topicLevel)
				}
			}
		}

		// Show complete example
		evtTrigger.logger.Info("=== COMPLETE FILTER EXAMPLE ===")
		if len(addresses) > 0 && len(topics) > 0 {
			if len(fromAddressesMap) > 0 && len(toAddressesMap) > 0 {
				evtTrigger.logger.Info("FROM-OR-TO scenario: Permissive RPC filter + local evaluation")
				evtTrigger.logger.Info("  - RPC receives: Transfer events from contracts", "contracts", addresses)
				fromAddrs := make([]common.Address, 0, len(fromAddressesMap))
				for addr := range fromAddressesMap {
					fromAddrs = append(fromAddrs, addr)
				}
				toAddrs := make([]common.Address, 0, len(toAddressesMap))
				for addr := range toAddressesMap {
					toAddrs = append(toAddrs, addr)
				}
				evtTrigger.logger.Info("  - Local filter: from OR to", "from_addresses", fromAddrs, "to_addresses", toAddrs)
			} else {
				evtTrigger.logger.Info("FilterQuery will receive:")
				evtTrigger.logger.Info("  - Events from contracts", "contracts", addresses)
				evtTrigger.logger.Info("  - With event signatures", "signatures", topics[0])
				if len(topics) > 1 && len(topics[1]) > 0 {
					evtTrigger.logger.Info("  - FROM addresses", "from_addresses", topics[1])
				}
				if len(topics) > 2 && len(topics[2]) > 0 {
					evtTrigger.logger.Info("  - TO addresses", "to_addresses", topics[2])
				}
			}
		} else if len(addresses) > 0 {
			evtTrigger.logger.Info("FilterQuery: Events from contracts (any event type)", "addresses", addresses)
		} else if len(topics) > 0 {
			evtTrigger.logger.Info("FilterQuery: Events with signatures (any contract)", "topics", topics[0])
		}
		evtTrigger.logger.Info("=== END FILTER STRUCTURE ===")
	}

	// VALIDATION: Disallow overly broad Transfer event filters to prevent network overload
	transferTopic := common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	hasTransferInTopics := false
	if len(topics) > 0 {
		for _, topic := range topics[0] {
			if topic == transferTopic {
				hasTransferInTopics = true
				break
			}
		}
	}

	if hasTransferInTopics {
		// Case 1: Transfer events from any contract (no address filter AND no from/to filters)
		if len(addresses) == 0 && (len(topics) == 1 || (len(topics) > 1 && len(topics[1]) == 0 && (len(topics) <= 2 || len(topics[2]) == 0))) {
			evtTrigger.logger.Error("DISALLOWED: Transfer events from any contract without from/to filtering")
			evtTrigger.logger.Error("This would capture ALL Transfer events from ALL contracts - too broad and inefficient")

			// Debug: Log which checks are causing this
			evtTrigger.checks.Range(func(key any, value any) bool {
				check := value.(*Check)
				evtTrigger.logger.Error("Problematic check causing broad filter",
					"task_id", key,
					"program", check.Program,
					"matchers_count", len(check.Matcher))
				return true
			})

			return ethereum.FilterQuery{} // Return empty filter to prevent subscription
		}

		// Case 2: Transfer events from specific contracts but no from/to filtering
		if len(addresses) > 0 && (len(topics) == 1 || (len(topics) > 1 && len(topics[1]) == 0 && (len(topics) <= 2 || len(topics[2]) == 0))) {
			evtTrigger.logger.Error("DISALLOWED: Transfer events from specific contracts without from/to filtering")
			evtTrigger.logger.Error("This would capture ALL Transfer events from specified contracts - still too broad")
			evtTrigger.logger.Error("Contracts that would be affected:", "addresses", addresses)
			return ethereum.FilterQuery{} // Return empty filter to prevent subscription
		}

		evtTrigger.logger.Info("Transfer filter validation passed - has proper from/to filtering")
	}

	return ethereum.FilterQuery{
		Addresses: addresses,
		Topics:    topics,
	}
}

// hasTransferEvent checks if the topic list contains Transfer event signature
func (evtTrigger *EventTrigger) hasTransferEvent(topics []common.Hash) bool {
	transferTopic := common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	for _, topic := range topics {
		if topic == transferTopic {
			return true
		}
	}
	return false
}

// parseExpressionForFilters extracts contract addresses and topics from JavaScript expressions
func (evtTrigger *EventTrigger) parseExpressionForFilters(program, triggerName string) ([]common.Address, []common.Hash, []common.Address, []common.Address) {
	var contracts []common.Address
	var topics []common.Hash
	var fromAddresses []common.Address
	var toAddresses []common.Address

	// First check for enhanced expression format (used in immediate execution)
	// Format: "0xddf252...&&contracts=[0xaddr1,0xaddr2]&&from=0xaddr&&to=0xaddr"
	if strings.Contains(program, "&&") {
		topicHash, contractAddresses, fromAddress, toAddress := evtTrigger.parseEnhancedExpression(program)

		// Add topic hash if found
		if topicHash != "" {
			if topic := common.HexToHash(topicHash); topic != (common.Hash{}) {
				topics = append(topics, topic)
			}
		}

		// Add contract addresses if found
		for _, addrStr := range contractAddresses {
			if addr := common.HexToAddress(addrStr); addr != (common.Address{}) {
				contracts = append(contracts, addr)
			}
		}

		if fromAddress != "" {
			if addr := common.HexToAddress(fromAddress); addr != (common.Address{}) {
				fromAddresses = append(fromAddresses, addr)
			}
		}

		if toAddress != "" {
			if addr := common.HexToAddress(toAddress); addr != (common.Address{}) {
				toAddresses = append(toAddresses, addr)
			}
		}

		return contracts, topics, fromAddresses, toAddresses
	}

	// Look for common patterns in JavaScript expressions:
	// - triggerName.data.address == "0x..."
	// - triggerName.data.topics[0] == "0x..."

	// Extract contract addresses from JS expressions
	addressPattern := regexp.MustCompile(triggerName + `\.data\.address\s*==\s*"(0x[a-fA-F0-9]{40})"`)
	addressMatches := addressPattern.FindAllStringSubmatch(program, -1)
	for _, match := range addressMatches {
		if len(match) > 1 {
			if addr := common.HexToAddress(match[1]); addr != (common.Address{}) {
				contracts = append(contracts, addr)
			}
		}
	}

	// Extract topic hashes (usually topic[0] for event signatures)
	topicPattern := regexp.MustCompile(triggerName + `\.data\.topics\[0\]\s*==\s*"(0x[a-fA-F0-9]{64})"`)
	topicMatches := topicPattern.FindAllStringSubmatch(program, -1)
	for _, match := range topicMatches {
		if len(match) > 1 {
			if topic := common.HexToHash(match[1]); topic != (common.Hash{}) {
				topics = append(topics, topic)
			}
		}
	}

	// Extract Transfer event signature (common case)
	transferPattern := regexp.MustCompile(`0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef`)
	if transferPattern.MatchString(program) {
		transferTopic := common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
		topics = append(topics, transferTopic)
	}

	return contracts, topics, fromAddresses, toAddresses
}

// parseEnhancedExpression parses the enhanced expression format used in immediate execution
// Examples:
// - "0xddf252...signature" -> (signature, [], "", "")
// - "0xddf252...signature&&0xaddr" -> (signature, [addr], "", "")
// - "0xddf252...signature&&contracts=[0xaddr1,0xaddr2]" -> (signature, [addr1,addr2], "", "")
// - "0xddf252...signature&&from=0xaddr" -> (signature, [], addr, "")
// - "0xddf252...signature&&to=0xaddr" -> (signature, [], "", addr)
func (evtTrigger *EventTrigger) parseEnhancedExpression(expression string) (topicHash string, contractAddresses []string, fromAddress, toAddress string) {
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
			fromAddress = strings.TrimSpace(strings.TrimPrefix(part, "from="))
		} else if strings.HasPrefix(part, "to=") {
			toAddress = strings.TrimSpace(strings.TrimPrefix(part, "to="))
		} else {
			// Simple contract address format
			contractAddresses = []string{part}
		}
	}

	return topicHash, contractAddresses, fromAddress, toAddress
}

func (evt *EventTrigger) Evaluate(event *types.Log, check *Check) (bool, error) {
	if event == nil {
		return false, fmt.Errorf("event is nil")
	}

	var err error = nil

	if len(check.Matcher) > 0 {
		// This is the simpler trigger. It's essentially an anyof
		return lo.SomeBy(check.Matcher, func(x *avsproto.EventTrigger_Matcher) bool {
			if len(x.Value) == 0 {
				err = fmt.Errorf("matcher value is empty")
				return false
			}

			switch x.Type {
			case "topics":
				// Matching based on topic of transaction
				topics := lo.Map[common.Hash, string](event.Topics, func(topic common.Hash, _ int) string {
					return "0x" + strings.ToLower(strings.TrimLeft(topic.String(), "0x"))
				})

				match := true
				// In Topics matching, this will be the array of topics. an element that is empty is skip
				for i, v := range x.Value {
					if v == "" || v == "0x" {
						continue
					}

					match = match && strings.EqualFold(topics[i], v)
				}
				return match
			case "address":
				// Matching base on token contract that emit the event
				return strings.EqualFold(event.Address.String(), x.Value[0])
			}

			// Unsupport type
			err = fmt.Errorf("unsupport matcher type: %s", x.Type)
			return false
		}), err
	}

	if check.Program != "" {
		// This is the advance trigger with js evaluation based on trigger data
		triggerVarName := check.TaskMetadata.GetTrigger().GetName()

		jsvm := taskengine.NewGojaVM()

		envs := macros.GetEnvs(map[string]interface{}{})
		for k, v := range envs {
			if err := jsvm.Set(k, v); err != nil {
				return false, fmt.Errorf("failed to set macro env in JS VM: %w", err)
			}
		}

		triggerData := map[string]interface{}{
			"data": map[string]interface{}{
				"address": strings.ToLower(event.Address.Hex()),
				"topics": lo.Map[common.Hash, string](event.Topics, func(topic common.Hash, _ int) string {
					return "0x" + strings.ToLower(strings.TrimLeft(topic.String(), "0x"))
				}),
				"data":    "0x" + common.Bytes2Hex(event.Data),
				"tx_hash": event.TxHash,
			},
		}
		if err := jsvm.Set(triggerVarName, triggerData); err != nil {
			return false, fmt.Errorf("failed to set trigger data in JS VM: %w", err)
		}

		result, err := jsvm.RunString(check.Program)

		if err != nil {
			return false, err
		}

		evalutationResult, ok := result.Export().(bool)
		if !ok {
			return false, fmt.Errorf("the expression `%s` didn't return a boolean but %v", check.Program, result.Export())
		}

		return evalutationResult, err
	}

	err = fmt.Errorf("invalid event trigger check: both matcher or expression are missing or empty")
	return false, err
}
