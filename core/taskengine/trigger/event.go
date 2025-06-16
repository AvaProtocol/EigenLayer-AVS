package trigger

import (
	"context"
	"fmt"
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
	Queries      []*avsproto.EventTrigger_Query
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

	checks sync.Map

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

		triggerCh:                triggerCh,
		checks:                   sync.Map{},
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

func (t *EventTrigger) AddCheck(check *avsproto.SyncMessagesResp_TaskMetadata) error {
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

	c := &Check{
		TaskMetadata: check,
		Queries:      queries,
	}

	t.checks.Store(taskID, c)

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
	t.checks.Delete(id)

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
			if check, exists := t.checks.Load(matchingTaskID); exists {
				checkObj := check.(*Check)
				if queryIndex < len(checkObj.Queries) {
					maxEventsPerBlock = checkObj.Queries[queryIndex].GetMaxEventsPerBlock()
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
	t.checks.Range(func(key any, value any) bool {
		taskID := key.(string)
		check := value.(*Check)

		if t.logMatchesTask(log, check) {
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
func (t *EventTrigger) logMatchesTask(log types.Log, check *Check) bool {
	for _, query := range check.Queries {
		if t.logMatchesEventQuery(log, query) {
			return true
		}
	}
	return false
}

// logMatchesEventQuery checks if a log matches a specific EventTrigger_Query
func (t *EventTrigger) logMatchesEventQuery(log types.Log, query *avsproto.EventTrigger_Query) bool {
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
					if expectedTopic := common.HexToHash(expectedTopicStr); log.Topics[i] == expectedTopic {
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

// buildFilterQueries converts all registered tasks into ethereum.FilterQuery objects
// Optimized to combine identical or overlapping queries from the same task
func (t *EventTrigger) buildFilterQueries() []QueryInfo {
	var allQueries []QueryInfo

	t.checks.Range(func(key any, value any) bool {
		taskID := key.(string)
		check := value.(*Check)

		// Group queries by their filter criteria to identify duplicates/overlaps
		queryGroups := make(map[string][]int) // queryKey -> []queryIndex

		// Convert each EventTrigger_Query to ethereum.FilterQuery and group by criteria
		for i, query := range check.Queries {
			ethQuery := t.convertToFilterQuery(query)

			// Create a unique key for this query's filter criteria
			queryKey := t.createQueryKey(ethQuery)
			queryGroups[queryKey] = append(queryGroups[queryKey], i)
		}

		// For each unique query criteria, create one optimized query
		for _, queryIndices := range queryGroups {
			// Use the first query as the base (they should all be identical)
			baseQueryIndex := queryIndices[0]
			baseQuery := check.Queries[baseQueryIndex]
			ethQuery := t.convertToFilterQuery(baseQuery)

			// Find the maximum maxEventsPerBlock across all identical queries
			maxEventsPerBlock := baseQuery.GetMaxEventsPerBlock()
			for _, idx := range queryIndices[1:] {
				if check.Queries[idx].GetMaxEventsPerBlock() > maxEventsPerBlock {
					maxEventsPerBlock = check.Queries[idx].GetMaxEventsPerBlock()
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
				t.logger.Warn("⚠️ Task has multiple unique queries - may receive duplicate events",
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
					if hash := common.HexToHash(topicStr); hash != (common.Hash{}) {
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
					if hash := common.HexToHash(topicStr); hash != (common.Hash{}) {
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
