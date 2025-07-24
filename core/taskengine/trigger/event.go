package trigger

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

// parseABIOptimized efficiently parses ABI from protobuf Values without string conversion
func parseABIOptimized(abiValues []*structpb.Value) (*abi.ABI, error) {
	if len(abiValues) == 0 {
		return nil, fmt.Errorf("empty ABI")
	}

	// Convert protobuf Values to JSON bytes directly
	abiArray := make([]interface{}, len(abiValues))
	for i, value := range abiValues {
		abiArray[i] = value.AsInterface()
	}

	// Marshal to JSON bytes
	jsonBytes, err := protojson.Marshal(&structpb.ListValue{Values: abiValues})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ABI to JSON: %v", err)
	}

	// Parse ABI directly from bytes using bytes.NewReader
	reader := bytes.NewReader(jsonBytes)
	parsedABI, err := abi.JSON(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ABI: %v", err)
	}

	return &parsedABI, nil
}

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
	Queries      []*avsproto.EventTrigger_Query
	// Cache parsed ABIs for conditional filtering to avoid repeated JSON parsing
	ParsedABIs map[int]*abi.ABI // queryIndex -> parsed ABI
}

// QueryInfo contains a filter query with safety and debugging information
type QueryInfo struct {
	Query             ethereum.FilterQuery
	Description       string
	TaskID            string
	QueryIndex        int
	MaxEventsPerBlock uint32
}

type EventTrigger struct {
	*CommonTrigger

	// New unified registry (preferred)
	registry *TaskRegistry

	// Legacy sync.Map (for backward compatibility)
	checks     sync.Map
	legacyMode bool

	// channel that we will push the trigger information back
	triggerCh chan TriggerMetadata[EventMark]

	// Track multiple subscriptions for query-based filtering
	subscriptions []SubscriptionInfo
	subsMutex     sync.RWMutex

	// Channel to signal subscription updates
	updateSubsCh chan struct{}

	// Event counting for safety monitoring
	eventCountsMutex sync.RWMutex
	eventCounts      map[string]map[uint64]uint32 // taskID -> blockNumber -> eventCount

	// Safety limits
	defaultMaxEventsPerQuery uint32
	defaultMaxTotalEvents    uint32

	// Overload alert callback (for operator to notify aggregator)
	onOverloadAlert func(alert *avsproto.EventOverloadAlert)

	// Add deduplication tracking
	processedEventsMutex sync.RWMutex
	processedEvents      map[string]bool // key: "blockNumber-txHash-logIndex"
}

type SubscriptionInfo struct {
	subscription ethereum.Subscription
	query        ethereum.FilterQuery
	description  string
	taskID       string
	queryIndex   int
}

func NewEventTrigger(o *RpcOption, triggerCh chan TriggerMetadata[EventMark], logger sdklogging.Logger, maxEventsPerQuery uint32, maxTotalEvents uint32) *EventTrigger {
	var err error

	b := EventTrigger{
		CommonTrigger: &CommonTrigger{
			done:      make(chan bool),
			shutdown:  false,
			rpcOption: o,
			logger:    logger,
		},

		registry:                 NewTaskRegistry(),
		triggerCh:                triggerCh,
		checks:                   sync.Map{},
		legacyMode:               false, // Start in new mode
		subscriptions:            make([]SubscriptionInfo, 0),
		updateSubsCh:             make(chan struct{}, 1),
		eventCounts:              make(map[string]map[uint64]uint32),
		defaultMaxEventsPerQuery: maxEventsPerQuery,
		defaultMaxTotalEvents:    maxTotalEvents,
		processedEvents:          make(map[string]bool),
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

// SetOverloadAlertCallback sets the callback function for when event overload is detected
func (t *EventTrigger) SetOverloadAlertCallback(callback func(alert *avsproto.EventOverloadAlert)) {
	t.onOverloadAlert = callback
}

// ensureNewFormat ensures we're using the new TaskRegistry format
// This provides automatic conversion from legacy sync.Map format
func (t *EventTrigger) ensureNewFormat() {
	if t.legacyMode {
		t.logger.Info("🔄 Converting from legacy sync.Map format to new TaskRegistry format")

		// Convert existing data
		t.registry.ConvertFromSyncMap(&t.checks)

		// Clear old data
		t.checks = sync.Map{}
		t.legacyMode = false

		t.logger.Info("✅ Successfully converted to new TaskRegistry format",
			"task_count", t.registry.GetEventTaskCount())
	}
}

// ensureLegacyConversion consolidates legacy data detection and conversion
// This helper function eliminates code duplication across methods
func (t *EventTrigger) ensureLegacyConversion() {
	t.detectLegacyData()
	t.ensureNewFormat()
}

// detectLegacyData checks if we have data in the old format
func (t *EventTrigger) detectLegacyData() {
	hasLegacyData := false
	t.checks.Range(func(key, value interface{}) bool {
		hasLegacyData = true
		return false // Stop after finding first item
	})

	if hasLegacyData && t.registry.GetEventTaskCount() == 0 {
		t.legacyMode = true
		t.logger.Info("🔍 Detected legacy sync.Map data - will convert on next operation")
	}
}

func (t *EventTrigger) AddCheck(check *avsproto.SyncMessagesResp_TaskMetadata) error {
	// Auto-convert from legacy format if needed
	t.ensureLegacyConversion()

	sTrigger := check.GetTrigger()
	if sTrigger == nil {
		return fmt.Errorf("trigger not found from sync message")
	}

	evt := sTrigger.GetEvent()
	if evt == nil {
		return fmt.Errorf("event trigger not found from sync message")
	}

	taskID := check.TaskId
	queries := evt.GetConfig().GetQueries()

	if len(queries) == 0 {
		return fmt.Errorf("no queries found in event trigger config for task %s", taskID)
	}

	// Create EventTaskData for the new registry
	eventData := &EventTaskData{
		Queries:    queries,
		ParsedABIs: make(map[int]*abi.ABI),
	}

	// Pre-parse ABIs for queries that have conditions to avoid repeated parsing
	for i, query := range queries {
		conditions := query.GetConditions()
		if len(conditions) > 0 {
			abiValues := query.GetContractAbi()
			// Convert contractAbi from protobuf Value array using optimized shared function
			if len(abiValues) > 0 {
				if parsedABI, err := parseABIOptimized(abiValues); err != nil {
					t.logger.Warn("🚫 Failed to pre-parse ABI for conditional filtering - will skip conditions",
						"task_id", taskID,
						"query_index", i,
						"error", err)
				} else {
					eventData.ParsedABIs[i] = parsedABI
					t.logger.Debug("✅ Pre-parsed ABI for conditional filtering using shared optimized function",
						"task_id", taskID,
						"query_index", i,
						"method_count", len(parsedABI.Methods))
				}
			}
		}
	}

	// Add to new registry
	t.registry.AddTask(taskID, check, eventData, nil, nil)

	// Initialize event counts for this task
	t.eventCountsMutex.Lock()
	t.eventCounts[taskID] = make(map[uint64]uint32)
	t.eventCountsMutex.Unlock()

	t.logger.Info("🔍 Task added with queries-based EventTrigger",
		"task_id", taskID,
		"queries_count", len(queries))

	// Log query details
	for i, query := range queries {
		t.logger.Info("📋 Query details",
			"task_id", taskID,
			"query_index", i,
			"addresses_count", len(query.GetAddresses()),
			"topics_count", len(query.GetTopics()),
			"max_events_per_block", query.GetMaxEventsPerBlock())
	}

	// Trigger subscription update (non-blocking)
	select {
	case t.updateSubsCh <- struct{}{}:
	default:
		// Channel already has a pending update, no need to queue another
	}

	return nil
}

func (t *EventTrigger) RemoveCheck(id string) error {
	// Auto-convert from legacy format if needed
	t.ensureLegacyConversion()

	// Remove from new registry (handles cleanup automatically)
	removed := t.registry.RemoveTask(id)

	if !removed {
		t.logger.Debug("🤷 Task not found for removal", "task_id", id)
	}

	// Clean up event counts for this task
	t.eventCountsMutex.Lock()
	delete(t.eventCounts, id)
	t.eventCountsMutex.Unlock()

	t.logger.Info("🗑️ Task removed - updating subscriptions dynamically", "task_id", id)

	// Trigger subscription update (non-blocking)
	select {
	case t.updateSubsCh <- struct{}{}:
	default:
		// Channel already has a pending update, no need to queue another
	}

	return nil
}

func (t *EventTrigger) Run(ctx context.Context) error {
	// Create logs channel that all subscriptions will send to
	logs := make(chan types.Log, 1000) // Buffered to handle multiple subscriptions

	// Build initial filter queries based on registered checks
	queries := t.buildFilterQueries()

	// Create initial subscriptions if we have queries
	t.subsMutex.Lock()
	t.subscriptions = make([]SubscriptionInfo, 0, len(queries))

	if len(queries) == 0 {
		t.logger.Debug("🚫 No initial filter queries to subscribe to - will wait for tasks to be added")
	} else {
		// Create subscriptions for each query
		for i, queryInfo := range queries {
			// Use timeout context to prevent indefinite blocking
			timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			sub, err := t.wsEthClient.SubscribeFilterLogs(timeoutCtx, queryInfo.Query, logs)
			cancel() // Clean up timeout context
			if err != nil {
				t.logger.Error("❌ Failed to create subscription",
					"query_index", i,
					"description", queryInfo.Description,
					"error", err)
				// Clean up any successful subscriptions
				for _, subInfo := range t.subscriptions {
					subInfo.subscription.Unsubscribe()
				}
				t.subsMutex.Unlock()
				return err
			}

			t.subscriptions = append(t.subscriptions, SubscriptionInfo{
				subscription: sub,
				query:        queryInfo.Query,
				description:  queryInfo.Description,
				taskID:       queryInfo.TaskID,
				queryIndex:   queryInfo.QueryIndex,
			})

			t.logger.Info("✅ Created initial subscription",
				"index", i,
				"task_id", queryInfo.TaskID,
				"description", queryInfo.Description,
				"addresses", queryInfo.Query.Addresses,
				"topics", queryInfo.Query.Topics)
		}
	}
	t.subsMutex.Unlock()

	// Create error channel that collects errors from all subscriptions
	errorCh := make(chan error, 100) // Buffered to handle multiple subscription errors

	// Start goroutines to monitor each subscription's error channel
	t.subsMutex.RLock()
	for i, subInfo := range t.subscriptions {
		go func(index int, sub ethereum.Subscription, desc string) {
			err := <-sub.Err()
			if err != nil {
				t.logger.Error("🔥 Subscription error", "index", index, "description", desc, "error", err)
				errorCh <- err
			}
		}(i, subInfo.subscription, subInfo.description)
	}
	t.subsMutex.RUnlock()

	// Start cleanup goroutine to prevent memory leaks
	go func() {
		ticker := time.NewTicker(30 * time.Minute) // Clean up every 30 minutes
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-t.done:
				return
			case <-ticker.C:
				t.cleanupOldProcessedEvents(1000) // Keep last 1000 processed events
				t.cleanupOldEventCounts(100)      // Keep last 100 blocks of event counts
			}
		}
	}()

	// Start the main event loop - this should ALWAYS run, even with no initial subscriptions
	go func() {
		defer func() {
			// Clean up all subscriptions
			t.subsMutex.RLock()
			for _, subInfo := range t.subscriptions {
				subInfo.subscription.Unsubscribe()
			}
			t.subsMutex.RUnlock()
		}()

		for {
			select {
			case <-ctx.Done():
				t.logger.Info("⏹️ Context cancelled, stopping event trigger")
				return
			case <-t.done:
				t.logger.Info("🛑 Event trigger shutdown signal received")
				return
			case log := <-logs:
				t.logger.Debug("📨 Received log",
					"block", log.BlockNumber,
					"tx", log.TxHash.Hex(),
					"log_index", log.Index)

				// CRITICAL: Check for duplicates FIRST before any processing
				// Use txHash-logIndex as unique key (txHash is unique, logIndex distinguishes events within same tx)
				eventKey := fmt.Sprintf("%s-%d", log.TxHash.Hex(), log.Index)

				t.processedEventsMutex.Lock()
				if t.processedEvents[eventKey] {
					t.processedEventsMutex.Unlock()
					t.logger.Debug("🔄 Skipping duplicate event from multiple subscriptions",
						"block", log.BlockNumber,
						"tx", log.TxHash.Hex(),
						"log_index", log.Index,
						"event_key", eventKey)
					continue
				}

				// Mark as processed immediately to prevent race conditions
				t.processedEvents[eventKey] = true
				t.processedEventsMutex.Unlock()

				// Safety check: count events per block per task
				if !t.checkEventSafety(log) {
					continue // Skip processing this event due to safety limits
				}

				// Process the log and match it to tasks
				if err := t.processLogInternal(log); err != nil {
					t.logger.Error("❌ Error processing log", "error", err)
				}

			case <-t.updateSubsCh:
				t.logger.Info("🔄 Subscription update requested")

				// Rebuild queries
				newQueries := t.buildFilterQueries()

				// Stop all existing subscriptions
				t.subsMutex.Lock()
				for _, subInfo := range t.subscriptions {
					subInfo.subscription.Unsubscribe()
				}
				t.subscriptions = make([]SubscriptionInfo, 0, len(newQueries))

				if len(newQueries) == 0 {
					t.logger.Info("🚫 No tasks require monitoring - all subscriptions stopped")
				} else {
					// Create new subscriptions
					for i, queryInfo := range newQueries {
						// Use timeout context to prevent indefinite blocking during updates
						timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						sub, subErr := t.wsEthClient.SubscribeFilterLogs(timeoutCtx, queryInfo.Query, logs)
						cancel() // Clean up timeout context
						if subErr != nil {
							t.logger.Error("❌ Failed to create new subscription during update", "index", i, "error", subErr)
							continue
						}

						t.subscriptions = append(t.subscriptions, SubscriptionInfo{
							subscription: sub,
							query:        queryInfo.Query,
							description:  queryInfo.Description,
							taskID:       queryInfo.TaskID,
							queryIndex:   queryInfo.QueryIndex,
						})

						t.logger.Info("✅ Created new subscription during update",
							"index", i,
							"task_id", queryInfo.TaskID,
							"description", queryInfo.Description,
							"addresses", queryInfo.Query.Addresses,
							"topics", queryInfo.Query.Topics,
							"warning", "Multiple subscriptions for same task may cause duplicate events")

						// Start error monitoring for new subscription
						go func(idx int, s ethereum.Subscription, d string) {
							errSub := <-s.Err()
							if errSub != nil {
								errorCh <- errSub
							}
						}(i, sub, queryInfo.Description)
					}
				}
				t.subsMutex.Unlock()

				t.logger.Info("🔄 Subscription update completed", "active_subscriptions", len(newQueries))

			case err := <-errorCh:
				if err == nil {
					continue
				}
				t.logger.Error("🔥 Subscription error, attempting reconnection", "error", err)

				// Attempt to reconnect
				if err := t.retryConnectToRpc(); err != nil {
					t.logger.Error("❌ Failed to reconnect to RPC", "error", err)
					continue
				}

				// Rebuild and resubscribe
				t.logger.Info("🔌 Reconnected, rebuilding subscriptions")
				newQueries := t.buildFilterQueries()

				t.subsMutex.Lock()
				// Clean up old subscriptions
				for _, subInfo := range t.subscriptions {
					subInfo.subscription.Unsubscribe()
				}
				t.subscriptions = make([]SubscriptionInfo, 0, len(newQueries))

				// Create new subscriptions
				for i, queryInfo := range newQueries {
					// Use timeout context to prevent indefinite blocking during reconnection
					timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					sub, subErr := t.wsEthClient.SubscribeFilterLogs(timeoutCtx, queryInfo.Query, logs)
					cancel() // Clean up timeout context
					if subErr != nil {
						t.logger.Error("❌ Failed to recreate subscription", "index", i, "error", subErr)
						continue
					}

					t.subscriptions = append(t.subscriptions, SubscriptionInfo{
						subscription: sub,
						query:        queryInfo.Query,
						description:  queryInfo.Description,
						taskID:       queryInfo.TaskID,
						queryIndex:   queryInfo.QueryIndex,
					})

					// Restart error monitoring for this subscription
					go func(idx int, s ethereum.Subscription, d string) {
						errSub := <-s.Err()
						if errSub != nil {
							errorCh <- errSub
						}
					}(i, sub, queryInfo.Description)
				}
				t.subsMutex.Unlock()

				t.logger.Info("🔌 Reconnection completed", "active_subscriptions", len(newQueries))
			}
		}
	}()

	return nil
}

// checkEventSafety monitors event counts per block and triggers alerts if limits are exceeded
func (t *EventTrigger) checkEventSafety(log types.Log) bool {
	blockNumber := log.BlockNumber

	// Find which task this log belongs to by checking all subscriptions
	t.subsMutex.RLock()
	var matchingTaskID string
	var queryIndex int
	var maxEventsPerBlock uint32

	for _, subInfo := range t.subscriptions {
		if t.logMatchesQuery(log, subInfo.query) {
			matchingTaskID = subInfo.taskID
			queryIndex = subInfo.queryIndex

			// Get safety limits for this task
			if entry, exists := t.registry.GetTask(matchingTaskID); exists && entry.EventData != nil {
				if queryIndex < len(entry.EventData.Queries) {
					maxEventsPerBlock = entry.EventData.Queries[queryIndex].GetMaxEventsPerBlock()
				}
			}
			break
		}
	}
	t.subsMutex.RUnlock()

	if matchingTaskID == "" {
		return true // No matching task found, allow processing
	}

	// Apply default if not specified
	if maxEventsPerBlock == 0 {
		maxEventsPerBlock = t.defaultMaxEventsPerQuery
	}

	// Update event count for this task and block
	t.eventCountsMutex.Lock()
	if t.eventCounts[matchingTaskID] == nil {
		t.eventCounts[matchingTaskID] = make(map[uint64]uint32)
	}
	t.eventCounts[matchingTaskID][blockNumber]++
	currentCount := t.eventCounts[matchingTaskID][blockNumber]
	t.eventCountsMutex.Unlock()

	// Check if limit is exceeded
	if currentCount > maxEventsPerBlock {
		t.logger.Warn("🚨 Event safety limit exceeded",
			"task_id", matchingTaskID,
			"block_number", blockNumber,
			"events_detected", currentCount,
			"safety_limit", maxEventsPerBlock,
			"query_index", queryIndex)

		// Trigger overload alert if callback is set
		if t.onOverloadAlert != nil {
			alert := &avsproto.EventOverloadAlert{
				TaskId:          matchingTaskID,
				OperatorAddress: "operator", // TODO: Get from config
				BlockNumber:     blockNumber,
				EventsDetected:  currentCount,
				SafetyLimit:     maxEventsPerBlock,
				QueryIndex:      fmt.Sprintf("%d", queryIndex),
				Timestamp:       uint64(time.Now().UnixMilli()),
				Details:         fmt.Sprintf("Query %d exceeded %d events per block limit with %d events at block %d", queryIndex, maxEventsPerBlock, currentCount, blockNumber),
			}

			go t.onOverloadAlert(alert) // Non-blocking call
		}

		return false // Reject this event
	}

	return true // Allow processing
}

// logMatchesQuery checks if a log matches a specific ethereum.FilterQuery
func (t *EventTrigger) logMatchesQuery(log types.Log, query ethereum.FilterQuery) bool {
	// Check addresses
	if len(query.Addresses) > 0 {
		found := false
		for _, addr := range query.Addresses {
			if addr == log.Address {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check topics
	if len(query.Topics) > 0 && len(log.Topics) > 0 {
		for i, topicGroup := range query.Topics {
			if i >= len(log.Topics) {
				break
			}

			if len(topicGroup) > 0 {
				found := false
				for _, expectedTopic := range topicGroup {
					if log.Topics[i] == expectedTopic {
						found = true
						break
					}
				}
				if !found {

					return false
				}
			}
		}
	}

	return true
}

// processLog processes an individual log and triggers matching tasks (public interface)
func (t *EventTrigger) processLog(log types.Log) error {
	// Create unique key for this event (txHash is unique, logIndex distinguishes events within same tx)
	eventKey := fmt.Sprintf("%s-%d", log.TxHash.Hex(), log.Index)

	// Check if we've already processed this event
	t.processedEventsMutex.Lock()
	if t.processedEvents[eventKey] {
		t.processedEventsMutex.Unlock()
		t.logger.Debug("🔄 Skipping duplicate event",
			"block", log.BlockNumber,
			"tx", log.TxHash.Hex(),
			"log_index", log.Index)
		return nil
	}

	// Mark this event as processed
	t.processedEvents[eventKey] = true
	t.processedEventsMutex.Unlock()

	return t.processLogInternal(log)
}

// processLogInternal processes an individual log and triggers matching tasks
// Note: Deduplication should be handled by caller before calling this function
func (t *EventTrigger) processLogInternal(log types.Log) error {
	var triggeredTasks []string

	// Check all registered tasks to see which ones match this log
	t.registry.RangeEventTasks(func(taskID string, entry *TaskEntry) bool {
		if t.logMatchesTaskEntry(log, entry) {
			triggeredTasks = append(triggeredTasks, taskID)

			// Send trigger notification
			marker := EventMark{
				BlockNumber: log.BlockNumber,
				LogIndex:    uint(log.Index),
				TxHash:      log.TxHash.Hex(),
			}

			triggerMeta := TriggerMetadata[EventMark]{
				TaskID: taskID,
				Marker: marker,
			}

			select {
			case t.triggerCh <- triggerMeta:
				t.logger.Info("🎯 Task triggered",
					"task_id", taskID,
					"block", log.BlockNumber,
					"tx", log.TxHash.Hex(),
					"log_index", log.Index)
			default:
				t.logger.Warn("⚠️ Trigger channel full, dropping trigger", "task_id", taskID)
			}
		}
		return true
	})

	if len(triggeredTasks) > 0 {
		t.logger.Debug("📤 Log processed successfully",
			"triggered_tasks", len(triggeredTasks),
			"task_ids", triggeredTasks)
	}

	return nil
}

// logMatchesTask checks if a log matches any of the queries for a specific task
// logMatchesTaskEntry checks if a log matches a task entry (new format)
func (t *EventTrigger) logMatchesTaskEntry(log types.Log, entry *TaskEntry) bool {
	if entry.EventData == nil {
		return false
	}

	for queryIndex, query := range entry.EventData.Queries {
		if t.logMatchesEventQuery(log, query, entry.EventData, queryIndex) {
			return true
		}
	}
	return false
}

// logMatchesTask checks if a log matches a task check (legacy format - kept for compatibility)
func (t *EventTrigger) logMatchesTask(log types.Log, check *Check) bool {
	// Convert to EventTaskData for compatibility
	eventData := &EventTaskData{
		Queries:    check.Queries,
		ParsedABIs: check.ParsedABIs,
	}

	for i, query := range check.Queries {
		if t.logMatchesEventQuery(log, query, eventData, i) {
			return true
		}
	}
	return false
}

// logMatchesEventQuery checks if a log matches a specific EventTrigger_Query
// logMatchesEventQuery checks if a log matches a specific event query (works with both old and new formats)
func (t *EventTrigger) logMatchesEventQuery(log types.Log, query *avsproto.EventTrigger_Query, eventData *EventTaskData, queryIndex int) bool {
	// Check addresses
	addresses := query.GetAddresses()
	if len(addresses) > 0 {
		found := false
		for _, addrStr := range addresses {
			if addr := common.HexToAddress(addrStr); addr == log.Address {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check topics
	topics := query.GetTopics()
	if len(topics) > 0 && len(log.Topics) > 0 {
		for i, topicFilter := range topics {
			if i >= len(log.Topics) {
				break
			}

			topicValues := topicFilter.GetValues()
			if len(topicValues) > 0 {
				found := false
				for _, expectedTopicStr := range topicValues {
					// Apply same address padding as in filter query building
					paddedTopicStr := t.padAddressIfNeeded(expectedTopicStr)
					if expectedTopic := common.HexToHash(paddedTopicStr); log.Topics[i] == expectedTopic {
						found = true
						break
					}
				}
				if !found {
					return false
				}
			}
		}
	}

	// NEW: Evaluate conditional filtering if conditions are provided
	conditions := query.GetConditions()
	if len(conditions) > 0 {
		return t.evaluateEventConditionsWithEventData(log, query, conditions, eventData, queryIndex)
	}

	return true
}

// evaluateEventConditionsWithEventData checks if a log matches the provided ABI-based conditions (new format)
func (t *EventTrigger) evaluateEventConditionsWithEventData(log types.Log, query *avsproto.EventTrigger_Query, conditions []*avsproto.EventCondition, eventData *EventTaskData, queryIndex int) bool {
	// Use cached ABI if available, otherwise parse it (fallback for backward compatibility)
	var contractABI *abi.ABI
	if cachedABI, exists := eventData.ParsedABIs[queryIndex]; exists && cachedABI != nil {
		contractABI = cachedABI
		t.logger.Debug("🚀 Using cached ABI for conditional filtering", "query_index", queryIndex)
	} else {
		// Fallback: parse ABI on-demand (this should rarely happen with the new caching)
		abiValues := query.GetContractAbi()
		if len(abiValues) > 0 {
			if parsedABI, err := parseABIOptimized(abiValues); err != nil {
				t.logger.Error("❌ Failed to parse contract ABI for conditional filtering", "error", err)
				return false
			} else {
				contractABI = parsedABI
				t.logger.Debug("⚠️ Parsed ABI on-demand using shared optimized function (consider pre-parsing for better performance)", "query_index", queryIndex)
			}
		} else {
			t.logger.Warn("🚫 Conditional filtering requires contract ABI but none provided")
			return false
		}
	}

	return t.evaluateEventConditionsCommon(log, query, conditions, contractABI, queryIndex)
}

// evaluateEventConditions checks if a log matches the provided ABI-based conditions (legacy format)
func (t *EventTrigger) evaluateEventConditions(log types.Log, query *avsproto.EventTrigger_Query, conditions []*avsproto.EventCondition, check *Check, queryIndex int) bool {
	// Use cached ABI if available, otherwise parse it (fallback for backward compatibility)
	var contractABI *abi.ABI
	if cachedABI, exists := check.ParsedABIs[queryIndex]; exists && cachedABI != nil {
		contractABI = cachedABI
		t.logger.Debug("🚀 Using cached ABI for conditional filtering", "query_index", queryIndex)
	} else {
		// Fallback: parse ABI on-demand (this should rarely happen with the new caching)
		abiValues := query.GetContractAbi()
		if len(abiValues) > 0 {
			if parsedABI, err := parseABIOptimized(abiValues); err != nil {
				t.logger.Error("❌ Failed to parse contract ABI for conditional filtering", "error", err)
				return false
			} else {
				contractABI = parsedABI
				t.logger.Debug("⚠️ Parsed ABI on-demand using shared optimized function (consider pre-parsing for better performance)", "query_index", queryIndex)
			}
		} else {
			t.logger.Warn("🚫 Conditional filtering requires contract ABI but none provided")
			return false
		}
	}

	return t.evaluateEventConditionsCommon(log, query, conditions, contractABI, queryIndex)
}

// evaluateEventConditionsCommon contains the shared logic for both legacy and new formats
func (t *EventTrigger) evaluateEventConditionsCommon(log types.Log, query *avsproto.EventTrigger_Query, conditions []*avsproto.EventCondition, contractABI *abi.ABI, queryIndex int) bool {
	// Find the matching event in ABI using the first topic (event signature)
	if len(log.Topics) == 0 {
		t.logger.Debug("🚫 Log has no topics, cannot match event signature")
		return false
	}

	eventSignature := log.Topics[0]
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
		t.logger.Debug("🚫 No matching event found in ABI for signature",
			"signature", eventSignature.Hex())
		return false
	}

	// Decode the event data
	decodedData, err := contractABI.Unpack(eventName, log.Data)
	if err != nil {
		t.logger.Error("❌ Failed to decode event data",
			"event", eventName,
			"error", err)
		return false
	}

	// Create field map for condition evaluation (includes both indexed and non-indexed fields)
	fieldMap := make(map[string]interface{})

	// Add indexed parameters from topics (skip topic[0] which is event signature)
	indexedCount := 0
	nonIndexedCount := 0

	for _, input := range matchingEvent.Inputs {
		if input.Indexed {
			// Get from topics (topic[0] is signature, so indexed params start from topic[1])
			topicIndex := indexedCount + 1
			if topicIndex < len(log.Topics) {
				fieldMap[input.Name] = log.Topics[topicIndex]
				t.logger.Debug("🔍 Added indexed field from topic",
					"field", input.Name,
					"value", log.Topics[topicIndex].Hex())
			}
			indexedCount++
		} else {
			// Get from decoded data
			if nonIndexedCount < len(decodedData) {
				fieldMap[input.Name] = decodedData[nonIndexedCount]
				t.logger.Debug("🔍 Added non-indexed field from data",
					"field", input.Name,
					"value", decodedData[nonIndexedCount])
			}
			nonIndexedCount++
		}
	}

	// Evaluate all conditions (AND logic - all must pass)
	for i, condition := range conditions {
		if !t.evaluateCondition(fieldMap, condition, eventName) {
			t.logger.Debug("🚫 Condition failed",
				"condition_index", i,
				"field", condition.GetFieldName(),
				"operator", condition.GetOperator(),
				"expected", condition.GetValue())
			return false
		}
	}

	t.logger.Info("✅ All conditions passed for event",
		"event", eventName,
		"conditions_count", len(conditions))
	return true
}

// evaluateCondition evaluates a single condition against the decoded field data
func (t *EventTrigger) evaluateCondition(fieldMap map[string]interface{}, condition *avsproto.EventCondition, eventName string) bool {
	fieldName := condition.GetFieldName()
	fieldValue, exists := fieldMap[fieldName]
	if !exists {
		t.logger.Warn("🚫 Field not found in decoded event data",
			"field", fieldName,
			"event", eventName,
			"available_fields", getMapKeys(fieldMap))
		return false
	}

	fieldType := condition.GetFieldType()
	operator := condition.GetOperator()
	expectedValue := condition.GetValue()

	t.logger.Debug("🔍 Evaluating condition",
		"field", fieldName,
		"type", fieldType,
		"operator", operator,
		"field_value", fieldValue,
		"expected", expectedValue)

	switch fieldType {
	case "uint256", "uint128", "uint64", "uint32", "uint16", "uint8":
		return t.evaluateUintCondition(fieldValue, operator, expectedValue)
	case "int256", "int128", "int64", "int32", "int16", "int8":
		return t.evaluateIntCondition(fieldValue, operator, expectedValue)
	case "address":
		return t.evaluateAddressCondition(fieldValue, operator, expectedValue)
	case "bool":
		return t.evaluateBoolCondition(fieldValue, operator, expectedValue)
	case "bytes32", "bytes":
		return t.evaluateBytesCondition(fieldValue, operator, expectedValue)
	default:
		t.logger.Warn("🚫 Unsupported field type for condition evaluation",
			"type", fieldType,
			"field", fieldName)
		return false
	}
}

// evaluateUintCondition handles unsigned integer comparisons
func (t *EventTrigger) evaluateUintCondition(fieldValue interface{}, operator, expectedValue string) bool {
	// Convert field value to *big.Int
	var fieldBigInt *big.Int
	switch v := fieldValue.(type) {
	case *big.Int:
		fieldBigInt = v
	case uint64:
		fieldBigInt = new(big.Int).SetUint64(v)
	case uint32:
		fieldBigInt = new(big.Int).SetUint64(uint64(v))
	case common.Hash:
		fieldBigInt = new(big.Int).SetBytes(v.Bytes())
	default:
		t.logger.Error("❌ Cannot convert field value to big.Int",
			"value", fieldValue,
			"type", fmt.Sprintf("%T", fieldValue))
		return false
	}

	// Parse expected value as big.Int
	expectedBigInt, ok := new(big.Int).SetString(expectedValue, 10)
	if !ok {
		t.logger.Error("❌ Cannot parse expected value as big.Int", "value", expectedValue)
		return false
	}

	// Perform comparison
	switch operator {
	case "gt":
		return fieldBigInt.Cmp(expectedBigInt) > 0
	case "gte":
		return fieldBigInt.Cmp(expectedBigInt) >= 0
	case "lt":
		return fieldBigInt.Cmp(expectedBigInt) < 0
	case "lte":
		return fieldBigInt.Cmp(expectedBigInt) <= 0
	case "eq":
		return fieldBigInt.Cmp(expectedBigInt) == 0
	case "ne":
		return fieldBigInt.Cmp(expectedBigInt) != 0
	default:
		t.logger.Error("❌ Unsupported operator for uint condition", "operator", operator)
		return false
	}
}

// evaluateIntCondition handles signed integer comparisons with proper two's complement handling
func (t *EventTrigger) evaluateIntCondition(fieldValue interface{}, operator, expectedValue string) bool {
	// Convert field value to *big.Int with proper signed interpretation
	var fieldBigInt *big.Int
	switch v := fieldValue.(type) {
	case *big.Int:
		fieldBigInt = v
	case int64:
		fieldBigInt = big.NewInt(v)
	case int32:
		fieldBigInt = big.NewInt(int64(v))
	case common.Hash:
		// CRITICAL: Handle two's complement for signed integers
		// For signed integers, we need to interpret the bytes as two's complement
		fieldBigInt = new(big.Int).SetBytes(v.Bytes())

		// Check if this should be interpreted as negative (two's complement)
		// For int256, if the most significant bit is set, it's negative
		if len(v.Bytes()) == 32 && v.Bytes()[0]&0x80 != 0 {
			// This is a negative number in two's complement
			// Convert from unsigned interpretation to signed by subtracting 2^256
			maxInt256 := new(big.Int)
			maxInt256.Exp(big.NewInt(2), big.NewInt(256), nil) // 2^256
			fieldBigInt.Sub(fieldBigInt, maxInt256)

			t.logger.Debug("🔄 Converted two's complement negative value",
				"original_unsigned", new(big.Int).SetBytes(v.Bytes()).String(),
				"corrected_signed", fieldBigInt.String(),
				"hex", v.Hex())
		}
	default:
		t.logger.Error("❌ Cannot convert field value to signed big.Int",
			"value", fieldValue,
			"type", fmt.Sprintf("%T", fieldValue))
		return false
	}

	// Parse expected value as signed big.Int (supports negative values)
	expectedBigInt, ok := new(big.Int).SetString(expectedValue, 10)
	if !ok {
		t.logger.Error("❌ Cannot parse expected value as signed big.Int", "value", expectedValue)
		return false
	}

	t.logger.Debug("🔍 Signed integer comparison",
		"field_value", fieldBigInt.String(),
		"expected_value", expectedBigInt.String(),
		"operator", operator)

	// Perform comparison (same logic as unsigned, but with proper signed values)
	switch operator {
	case "gt":
		return fieldBigInt.Cmp(expectedBigInt) > 0
	case "gte":
		return fieldBigInt.Cmp(expectedBigInt) >= 0
	case "lt":
		return fieldBigInt.Cmp(expectedBigInt) < 0
	case "lte":
		return fieldBigInt.Cmp(expectedBigInt) <= 0
	case "eq":
		return fieldBigInt.Cmp(expectedBigInt) == 0
	case "ne":
		return fieldBigInt.Cmp(expectedBigInt) != 0
	default:
		t.logger.Error("❌ Unsupported operator for signed int condition", "operator", operator)
		return false
	}
}

// evaluateAddressCondition handles address comparisons
func (t *EventTrigger) evaluateAddressCondition(fieldValue interface{}, operator, expectedValue string) bool {
	var fieldAddr common.Address
	switch v := fieldValue.(type) {
	case common.Address:
		fieldAddr = v
	case common.Hash:
		fieldAddr = common.HexToAddress(v.Hex())
	case string:
		fieldAddr = common.HexToAddress(v)
	default:
		t.logger.Error("❌ Cannot convert field value to address",
			"value", fieldValue,
			"type", fmt.Sprintf("%T", fieldValue))
		return false
	}

	expectedAddr := common.HexToAddress(expectedValue)

	switch operator {
	case "eq":
		return fieldAddr == expectedAddr
	case "ne":
		return fieldAddr != expectedAddr
	default:
		t.logger.Error("❌ Unsupported operator for address condition", "operator", operator)
		return false
	}
}

// evaluateBoolCondition handles boolean comparisons
func (t *EventTrigger) evaluateBoolCondition(fieldValue interface{}, operator, expectedValue string) bool {
	fieldBool, ok := fieldValue.(bool)
	if !ok {
		t.logger.Error("❌ Field value is not boolean",
			"value", fieldValue,
			"type", fmt.Sprintf("%T", fieldValue))
		return false
	}

	expectedBool := expectedValue == "true"

	switch operator {
	case "eq":
		return fieldBool == expectedBool
	case "ne":
		return fieldBool != expectedBool
	default:
		t.logger.Error("❌ Unsupported operator for bool condition", "operator", operator)
		return false
	}
}

// evaluateBytesCondition handles bytes comparisons
func (t *EventTrigger) evaluateBytesCondition(fieldValue interface{}, operator, expectedValue string) bool {
	var fieldHex string
	switch v := fieldValue.(type) {
	case common.Hash:
		fieldHex = v.Hex()
	case []byte:
		fieldHex = common.Bytes2Hex(v)
	case string:
		fieldHex = v
	default:
		t.logger.Error("❌ Cannot convert field value to hex string",
			"value", fieldValue,
			"type", fmt.Sprintf("%T", fieldValue))
		return false
	}

	// Normalize hex strings (ensure they start with 0x)
	if !strings.HasPrefix(fieldHex, "0x") {
		fieldHex = "0x" + fieldHex
	}
	if !strings.HasPrefix(expectedValue, "0x") {
		expectedValue = "0x" + expectedValue
	}

	switch operator {
	case "eq":
		return strings.EqualFold(fieldHex, expectedValue)
	case "ne":
		return !strings.EqualFold(fieldHex, expectedValue)
	default:
		t.logger.Error("❌ Unsupported operator for bytes condition", "operator", operator)
		return false
	}
}

// getMapKeys is a helper function to get map keys for debugging
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// buildFilterQueries converts all registered tasks into ethereum.FilterQuery objects
// Optimized to combine identical or overlapping queries from the same task
func (t *EventTrigger) buildFilterQueries() []QueryInfo {
	var allQueries []QueryInfo

	// Auto-convert from legacy format if needed
	t.ensureLegacyConversion()

	t.registry.RangeEventTasks(func(taskID string, entry *TaskEntry) bool {

		// Group queries by their filter criteria to identify duplicates/overlaps
		queryGroups := make(map[string][]int) // queryKey -> []queryIndex

		// Convert each EventTrigger_Query to ethereum.FilterQuery and group by criteria
		for i, query := range entry.EventData.Queries {
			ethQuery := t.convertToFilterQuery(query)

			// Create a unique key for this query's filter criteria
			queryKey := t.createQueryKey(ethQuery)
			queryGroups[queryKey] = append(queryGroups[queryKey], i)
		}

		// For each unique query criteria, create one optimized query
		for _, queryIndices := range queryGroups {
			// Use the first query as the base (they should all be identical)
			baseQueryIndex := queryIndices[0]
			baseQuery := entry.EventData.Queries[baseQueryIndex]
			ethQuery := t.convertToFilterQuery(baseQuery)

			// Find the maximum maxEventsPerBlock across all identical queries
			maxEventsPerBlock := baseQuery.GetMaxEventsPerBlock()
			for _, idx := range queryIndices[1:] {
				if entry.EventData.Queries[idx].GetMaxEventsPerBlock() > maxEventsPerBlock {
					maxEventsPerBlock = entry.EventData.Queries[idx].GetMaxEventsPerBlock()
				}
			}

			description := fmt.Sprintf("Task[%s] Query[%d-%d]", taskID, queryIndices[0], queryIndices[len(queryIndices)-1])
			if len(ethQuery.Addresses) > 0 {
				description += fmt.Sprintf(" - contracts:%v", ethQuery.Addresses)
			}
			if len(ethQuery.Topics) > 0 {
				description += fmt.Sprintf(" - %d topic levels", len(ethQuery.Topics))
			}

			// If we combined multiple queries, add a note
			if len(queryIndices) > 1 {
				description += fmt.Sprintf(" (combined %d identical queries)", len(queryIndices))
			}

			queryInfo := QueryInfo{
				Query:             ethQuery,
				Description:       description,
				TaskID:            taskID,
				QueryIndex:        baseQueryIndex, // Use the first query index as representative
				MaxEventsPerBlock: maxEventsPerBlock,
			}

			allQueries = append(allQueries, queryInfo)
		}

		return true
	})

	if len(allQueries) > 0 {
		t.logger.Info("🔧 Built optimized filter queries",
			"total_queries", len(allQueries))

		// Check for potential overlapping queries from same task
		taskQueryCounts := make(map[string]int)
		for _, q := range allQueries {
			taskQueryCounts[q.TaskID]++
		}

		for taskID, count := range taskQueryCounts {
			if count > 1 {
				t.logger.Info("⚠️ Task has multiple unique queries - may receive duplicate events",
					"task_id", taskID,
					"unique_query_count", count,
					"recommendation", "Consider combining queries to reduce duplicates")
			}
		}

		for i, q := range allQueries {
			t.logger.Info("📋 Query details",
				"index", i,
				"task_id", q.TaskID,
				"description", q.Description,
				"addresses", len(q.Query.Addresses),
				"topic_levels", len(q.Query.Topics))
		}
	}

	return allQueries
}

// createQueryKey creates a unique string key for a filter query's criteria
func (t *EventTrigger) createQueryKey(query ethereum.FilterQuery) string {
	var keyParts []string

	// Add addresses (sorted for consistency)
	if len(query.Addresses) > 0 {
		addresses := make([]string, len(query.Addresses))
		for i, addr := range query.Addresses {
			addresses[i] = addr.Hex()
		}
		// Sort addresses for consistent key generation
		sort.Strings(addresses)
		keyParts = append(keyParts, fmt.Sprintf("addrs:%v", addresses))
	}

	// Add topics (preserving wildcard vs specific value distinction AND position)
	if len(query.Topics) > 0 {
		for i, topicGroup := range query.Topics {
			if len(topicGroup) > 0 {
				// Check if this topic group contains any wildcards (empty hashes)
				hasWildcard := false
				specificTopics := make([]string, 0)

				for _, topic := range topicGroup {
					if topic == (common.Hash{}) {
						hasWildcard = true
					} else {
						specificTopics = append(specificTopics, topic.Hex())
					}
				}

				if hasWildcard {
					// Include wildcard indicator in the key with position
					// Sort specific topics for consistency when wildcards are present
					sort.Strings(specificTopics)
					keyParts = append(keyParts, fmt.Sprintf("topic[%d]:wildcard+%v", i, specificTopics))
				} else {
					// Sort specific topics for consistent key generation
					sort.Strings(specificTopics)
					keyParts = append(keyParts, fmt.Sprintf("topic[%d]:%v", i, specificTopics))
				}
			} else {
				// Empty topic group means "any value" for this topic position
				// This is different from having wildcards mixed with specific values
				keyParts = append(keyParts, fmt.Sprintf("topic[%d]:any", i))
			}
		}
	}

	return strings.Join(keyParts, "|")
}

// convertToFilterQuery converts a protobuf EventTrigger_Query to ethereum.FilterQuery
func (t *EventTrigger) convertToFilterQuery(query *avsproto.EventTrigger_Query) ethereum.FilterQuery {
	var addresses []common.Address
	for _, addrStr := range query.GetAddresses() {
		if addr := common.HexToAddress(addrStr); addr != (common.Address{}) {
			addresses = append(addresses, addr)
		}
	}

	var topics [][]common.Hash

	// Handle the case where client sends all topic values in a single topic array
	// This is the format: topics: [{ values: [Transfer signature, FROM address, null] }]
	if len(query.GetTopics()) == 1 && len(query.GetTopics()[0].GetValues()) > 1 {
		// Client sent all topic values in a single array, need to split them by position
		allValues := query.GetTopics()[0].GetValues()

		// Process each topic position
		for i := 0; i < len(allValues); i++ {
			if i < len(allValues) {
				topicStr := allValues[i]
				if topicStr == "" {
					// Empty string represents null/wildcard for this topic position
					topics = append(topics, nil)
				} else {
					// Check if this looks like an Ethereum address and pad it properly
					paddedTopicStr := t.padAddressIfNeeded(topicStr)
					if hash := common.HexToHash(paddedTopicStr); hash != (common.Hash{}) {
						topics = append(topics, []common.Hash{hash})
					} else {
						topics = append(topics, nil)
					}
				}
			}
		}
	} else {
		// Original format: each topicFilter represents a separate topic position
		for _, topicFilter := range query.GetTopics() {
			allWildcard := true
			var topicHashes []common.Hash
			for _, topicStr := range topicFilter.GetValues() {
				if topicStr == "" {
					// Empty string represents null/wildcard
					continue
				} else {
					// Check if this looks like an Ethereum address and pad it properly
					paddedTopicStr := t.padAddressIfNeeded(topicStr)
					if hash := common.HexToHash(paddedTopicStr); hash != (common.Hash{}) {
						topicHashes = append(topicHashes, hash)
						allWildcard = false
					}
				}
			}
			if allWildcard {
				topics = append(topics, nil) // nil means wildcard for this topic position
			} else {
				topics = append(topics, topicHashes)
			}
		}
	}

	return ethereum.FilterQuery{
		Addresses: addresses,
		Topics:    topics,
	}
}

// Stop stops the event trigger
func (t *EventTrigger) Stop() {
	t.logger.Info("🛑 Stopping EventTrigger")
	t.shutdown = true
	close(t.done)
}

// Cleanup for old event counts (call periodically to prevent memory leaks)
func (t *EventTrigger) cleanupOldEventCounts(blocksToKeep uint64) {
	t.eventCountsMutex.Lock()
	defer t.eventCountsMutex.Unlock()

	// Get current block number (you might want to track this)
	// For now, just keep recent blocks based on the parameter

	for taskID, blockCounts := range t.eventCounts {
		// This is a simplified cleanup - in practice you'd want to know the current block
		// For now, just limit the number of blocks we keep in memory
		if len(blockCounts) > int(blocksToKeep) {
			// Find and remove oldest blocks
			var oldestBlock uint64 = ^uint64(0)
			for blockNumber := range blockCounts {
				if blockNumber < oldestBlock {
					oldestBlock = blockNumber
				}
			}
			delete(blockCounts, oldestBlock)
			t.logger.Debug("🧹 Cleaned up old event counts", "task_id", taskID, "removed_block", oldestBlock)
		}
	}
}

// Add cleanup for processed events to prevent memory leaks
func (t *EventTrigger) cleanupOldProcessedEvents(maxEvents uint64) {
	t.processedEventsMutex.Lock()
	defer t.processedEventsMutex.Unlock()

	// Remove old events if we have too many
	currentCount := len(t.processedEvents)
	if currentCount > int(maxEvents) {
		// Simple approach: clear a portion of the map to keep memory usage bounded
		// Since we can't easily determine "oldest" without block numbers in the key,
		// we'll just clear about half of the events
		toRemove := currentCount - int(maxEvents)
		removedCount := 0

		for key := range t.processedEvents {
			delete(t.processedEvents, key)
			removedCount++
			if removedCount >= toRemove {
				break
			}
		}

		t.logger.Debug("🧹 Cleaned up old processed events",
			"removed", removedCount,
			"remaining", len(t.processedEvents),
			"note", "Using simple cleanup since txHash-logIndex keys don't contain timestamps")
	}
}

// padAddressIfNeeded checks if a topic string looks like an Ethereum address and pads it to 32 bytes if needed
func (t *EventTrigger) padAddressIfNeeded(topicStr string) string {
	// Remove 0x prefix for length check
	cleanHex := strings.TrimPrefix(strings.ToLower(topicStr), "0x")

	// Check if it looks like an Ethereum address (40 hex characters = 20 bytes)
	if len(cleanHex) == 40 {
		// This looks like an Ethereum address, pad it to 32 bytes (64 hex characters)
		paddedAddr := "0x" + strings.Repeat("0", 24) + cleanHex
		t.logger.Debug("🔧 Padded address for topic filtering",
			"original", topicStr,
			"padded", paddedAddr,
			"reason", "Ethereum addresses in topics must be 32 bytes")
		return paddedAddr
	}

	// If it's already 64 hex characters (32 bytes) or not an address, return as-is
	return topicStr
}
