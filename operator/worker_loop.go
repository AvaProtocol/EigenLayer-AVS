package operator

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	gocron "github.com/go-co-op/gocron/v2"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	triggerengine "github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/trigger"
	avspb "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/version"
	"github.com/ethereum/go-ethereum/core/types"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	retryIntervalSecond      = 60
	errorLogDebounceInterval = 3 * time.Minute // Only log same error type every 3 minutes
)

// shouldLogError determines if we should log an error based on debouncing rules
// Returns true if we should log, false if we should skip to reduce spam
func (o *Operator) shouldLogError(errorType string, isStreamError bool) bool {
	now := time.Now()

	if isStreamError {
		// Check if this is a different error type or enough time has passed
		timeSinceLastLog := now.Sub(o.lastStreamErrorTime)
		isDifferentError := o.lastStreamErrorType != errorType
		shouldLog := isDifferentError || timeSinceLastLog >= errorLogDebounceInterval

		if shouldLog {
			o.lastStreamErrorType = errorType
			o.lastStreamErrorTime = now
			return true
		}
	} else {
		// Check if this is a different error type or enough time has passed for ping errors
		timeSinceLastLog := now.Sub(o.lastPingErrorTime)
		isDifferentError := o.lastPingErrorType != errorType
		shouldLog := isDifferentError || timeSinceLastLog >= errorLogDebounceInterval

		if shouldLog {
			o.lastPingErrorType = errorType
			o.lastPingErrorTime = now
			return true
		}
	}

	return false
}

// runWorkLoop is main entrypoint where we sync data with aggregator. It performs these op
//   - subscribe to server to receive update. act on these update to update local storage
//   - spawn a loop to check triggering condition
func (o *Operator) runWorkLoop(ctx context.Context) error {
	// Completely disable stack traces globally
	debug.SetTraceback("none")
	os.Setenv("GOTRACEBACK", "none")

	blockTasksMap := make(map[int64][]string)
	blockTasksMutex := &sync.Mutex{}

	// Initialize the scheduler for managing periodic tasks
	var schedulerErr error

	// Configure scheduler to suppress excessive logging
	o.scheduler, schedulerErr = gocron.NewScheduler()
	if schedulerErr != nil {
		return fmt.Errorf("failed to initialize scheduler: %w", schedulerErr)
	}
	o.scheduler.Start()

	_, err := o.scheduler.NewJob(
		gocron.DurationJob(time.Minute*10),
		gocron.NewTask(func() {
			blockTasksMutex.Lock()
			defer blockTasksMutex.Unlock()

			if len(blockTasksMap) > 10 {
				var blocks []int64
				for block := range blockTasksMap {
					blocks = append(blocks, block)
				}

				sort.Slice(blocks, func(i, j int) bool {
					return blocks[i] < blocks[j]
				})

				for i := 0; i < len(blocks)-10; i++ {
					delete(blockTasksMap, blocks[i])
				}
			}
		}),
	)
	if err != nil {
		o.logger.Error("Failed to create cleanup job for block tasks map", "error", err)
	}

	// Wrap PingServer to handle errors gracefully without stack traces
	_, err = o.scheduler.NewJob(
		gocron.DurationJob(time.Second*5),
		gocron.NewTask(func() {
			// Simple wrapper that just calls PingServer - all error handling is internal
			o.PingServer()
		}),
	)
	if err != nil {
		o.logger.Error("Failed to create ping job", "error", err)
	}

	macros.SetRpc(o.config.TargetChain.EthWsUrl)
	taskengine.SetRpc(o.config.TargetChain.EthRpcUrl)
	taskengine.SetWsRpc(o.config.TargetChain.EthWsUrl)
	taskengine.SetLogger(o.logger)

	var metricsErrChan <-chan error
	if o.config.EnableMetrics && o.metrics != nil {
		metricsErrChan = o.metrics.Start(ctx, o.metricsReg)
	} else {
		metricsErrChan = make(chan error, 1)
		if o.config.EnableMetrics && o.metrics == nil {
			o.logger.Error("🚨 CRITICAL: metrics is nil but EnableMetrics is true - this should never happen in production",
				"solution", "This indicates a serious initialization problem that needs investigation")
		}
	}

	rpcConfig := triggerengine.RpcOption{
		RpcURL:   o.config.TargetChain.EthRpcUrl,
		WsRpcURL: o.config.TargetChain.EthWsUrl,
	}

	blockTriggerCh := make(chan triggerengine.TriggerMetadata[int64], 1000)
	o.blockTrigger = triggerengine.NewBlockTrigger(&rpcConfig, blockTriggerCh, o.logger)

	eventTriggerCh := make(chan triggerengine.TriggerMetadata[triggerengine.EventMark], 1000)
	o.eventTrigger = triggerengine.NewEventTrigger(&rpcConfig, eventTriggerCh, o.logger,
		o.config.GetMaxEventsPerQueryPerBlock(), o.config.GetMaxTotalEventsPerBlock())

	// Set up overload alert callback to notify aggregator
	o.eventTrigger.SetOverloadAlertCallback(func(alert *avspb.EventOverloadAlert) {
		o.logger.Warn("🚨 Sending event overload alert to aggregator",
			"task_id", alert.TaskId,
			"events_detected", alert.EventsDetected,
			"safety_limit", alert.SafetyLimit)

		// Use existing node client for internal overload alerts
		if _, err := o.nodeRpcClient.ReportEventOverload(ctx, alert); err != nil {
			o.logger.Error("❌ Failed to send overload alert to aggregator",
				"task_id", alert.TaskId,
				"error", err)
		} else {
			o.logger.Info("✅ Successfully sent overload alert to aggregator",
				"task_id", alert.TaskId)
		}
	})

	timeTriggerCh := make(chan triggerengine.TriggerMetadata[uint64], 1000)
	o.timeTrigger = triggerengine.NewTimeTrigger(timeTriggerCh, o.logger)

	// Log consolidated monitoring status
	eventStatus := "disabled"
	if o.config.EnabledFeatures.EventTrigger {
		eventStatus = "enabled"
	}
	o.logger.Infof("📊 Monitoring Status: Block ✅ | Time ✅ | Event %s", eventStatus)

	o.blockTrigger.Run(ctx)
	o.timeTrigger.Run(ctx)

	// Event trigger can be costly, so we require an opt-in
	if o.config.EnabledFeatures.EventTrigger {
		o.eventTrigger.Run(ctx)
	}

	// Establish a connection with gRPC server where new task will be pushed automatically
	o.logger.Info("open channel to grpc to receive check")
	go o.StreamMessages()

	for {
		select {
		case <-ctx.Done():
			return nil
		case triggerItem := <-timeTriggerCh:
			o.logger.Info("time trigger", "task_id", triggerItem.TaskID, "marker", triggerItem.Marker)

			if resp, err := o.nodeRpcClient.NotifyTriggers(ctx, &avspb.NotifyTriggersReq{
				Address:     o.config.OperatorAddress,
				Signature:   "pending",
				TaskId:      triggerItem.TaskID,
				TriggerType: avspb.TriggerType_TRIGGER_TYPE_CRON,
				TriggerOutput: &avspb.NotifyTriggersReq_CronTrigger{
					CronTrigger: &avspb.CronTrigger_Output{
						Data: func() *structpb.Value {
							cronData := map[string]interface{}{
								"timestamp":    uint64(triggerItem.Marker),
								"timestampIso": time.Unix(0, int64(triggerItem.Marker)*1000000).UTC().Format("2006-01-02T15:04:05.000Z"),
							}
							dataValue, _ := structpb.NewValue(cronData)
							return dataValue
						}(),
					},
				},
			}); err == nil {
				o.logger.Debug("Successfully notify aggregator for task hit",
					"taskid", triggerItem.TaskID,
					"remaining_executions", resp.RemainingExecutions,
					"task_still_enabled", resp.TaskStillEnabled,
					"status", resp.Status)

				// Check if task should stop being monitored
				if !resp.TaskStillEnabled || resp.RemainingExecutions == 0 {
					o.logger.Info("🛑 Task exhausted, requesting stop monitoring",
						"task_id", triggerItem.TaskID,
						"remaining_executions", resp.RemainingExecutions,
						"status", resp.Status,
						"message", resp.Message)

					// Remove task from time trigger monitoring
					if o.timeTrigger != nil {
						if err := o.timeTrigger.RemoveCheck(triggerItem.TaskID); err != nil {
							o.logger.Warn("Failed to remove exhausted task from time monitoring",
								"task_id", triggerItem.TaskID,
								"error", err)
						} else {
							o.logger.Info("✅ Removed exhausted task from time monitoring",
								"task_id", triggerItem.TaskID)
						}
					}
				}
			} else {
				// Use debounced logging for trigger notification errors to prevent stack traces
				var errorType string
				if strings.Contains(err.Error(), "connection refused") {
					errorType = "trigger_notify_connection_refused"
				} else if strings.Contains(err.Error(), "Unavailable") {
					errorType = "trigger_notify_unavailable"
				} else {
					errorType = "trigger_notify_other_error"
				}

				if o.shouldLogError(errorType, false) {
					o.logger.Info("❌ Failed to notify aggregator of task trigger",
						"task_id", triggerItem.TaskID,
						"trigger_type", "time",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"solution", "Check aggregator connectivity - task will be retried",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}
			}
		case triggerItem := <-blockTriggerCh:
			o.logger.Debug("block trigger details", "task_id", triggerItem.TaskID, "marker", triggerItem.Marker)

			blockTasksMutex.Lock()
			blockNum := triggerItem.Marker
			blockTasksMap[blockNum] = append(blockTasksMap[blockNum], triggerItem.TaskID)

			taskCount := len(blockTasksMap[blockNum])
			// Log at 10, 20, 40, 80, 160...
			threshold := 10
			for threshold <= taskCount {
				if taskCount == threshold {
					o.logger.Info("block trigger summary", "block", blockNum, "task_count", taskCount)
					break
				}
				threshold *= 2
			}
			blockTasksMutex.Unlock()

			// Fetch real block data from RPC to match runBlockTriggerImmediately behavior
			blockData := map[string]interface{}{
				"blockNumber": uint64(triggerItem.Marker),
				"blockHash":   "",
				"timestamp":   uint64(0),
				"parentHash":  "",
				"difficulty":  "",
				"gasLimit":    uint64(0),
				"gasUsed":     uint64(0),
			}

			// Try to fetch full block data from RPC using shared targetEthClient
			if o.targetEthClient != nil {
				var header *types.Header
				var fetchErr error
				const blockFetchRetries = 3
				const blockFetchRetryDelay = 150 * time.Millisecond
				for attempt := 0; attempt < blockFetchRetries; attempt++ {
					h, err := o.targetEthClient.HeaderByNumber(ctx, big.NewInt(triggerItem.Marker))
					if err == nil && h != nil {
						header = h
						break
					}
					if err != nil {
						fetchErr = err
					} else {
						fetchErr = fmt.Errorf("header is nil for block %d", triggerItem.Marker)
					}
					if attempt < blockFetchRetries-1 {
						time.Sleep(blockFetchRetryDelay)
					}
				}
				if header != nil {
					// Populate with real blockchain data
					blockData["blockHash"] = header.Hash().Hex()
					blockData["timestamp"] = header.Time
					blockData["parentHash"] = header.ParentHash.Hex()
					blockData["difficulty"] = header.Difficulty.String()
					blockData["gasLimit"] = header.GasLimit
					blockData["gasUsed"] = header.GasUsed

					o.logger.Debug("✅ Fetched real block data for trigger",
						"task_id", triggerItem.TaskID,
						"block_number", triggerItem.Marker,
						"block_hash", header.Hash().Hex(),
						"timestamp", header.Time)
				} else {
					// RPC propagation lag: block not yet available - expected, handled with fallback
					errStr := "nil"
					if fetchErr != nil {
						errStr = fetchErr.Error()
					}
					o.logger.Debug("Block header not available from RPC (propagation lag), using minimal block data",
						"task_id", triggerItem.TaskID,
						"block_number", triggerItem.Marker,
						"error", errStr)
				}
			} else {
				o.logger.Warn("⚠️ Target RPC client not available, using minimal block data",
					"task_id", triggerItem.TaskID,
					"block_number", triggerItem.Marker)
			}

			if resp, err := o.nodeRpcClient.NotifyTriggers(ctx, &avspb.NotifyTriggersReq{
				Address:     o.config.OperatorAddress,
				Signature:   "pending",
				TaskId:      triggerItem.TaskID,
				TriggerType: avspb.TriggerType_TRIGGER_TYPE_BLOCK,
				TriggerOutput: &avspb.NotifyTriggersReq_BlockTrigger{
					BlockTrigger: &avspb.BlockTrigger_Output{
						Data: func() *structpb.Value {
							dataValue, _ := structpb.NewValue(blockData)
							return dataValue
						}(),
					},
				},
			}); err == nil {
				o.logger.Debug("Successfully notify aggregator for task hit",
					"taskid", triggerItem.TaskID,
					"remaining_executions", resp.RemainingExecutions,
					"task_still_enabled", resp.TaskStillEnabled,
					"status", resp.Status)

				// Check if task should stop being monitored
				if !resp.TaskStillEnabled || resp.RemainingExecutions == 0 {
					o.logger.Info("🛑 Task exhausted, requesting stop monitoring",
						"task_id", triggerItem.TaskID,
						"remaining_executions", resp.RemainingExecutions,
						"status", resp.Status,
						"message", resp.Message)

					// Remove task from block trigger monitoring
					if o.blockTrigger != nil {
						if err := o.blockTrigger.RemoveCheck(triggerItem.TaskID); err != nil {
							o.logger.Warn("Failed to remove exhausted task from block monitoring",
								"task_id", triggerItem.TaskID,
								"error", err)
						} else {
							o.logger.Info("✅ Removed exhausted task from block monitoring",
								"task_id", triggerItem.TaskID)
						}
					}
				}
			} else {
				// Use debounced logging for trigger notification errors to prevent stack traces
				var errorType string
				if strings.Contains(err.Error(), "connection refused") {
					errorType = "trigger_notify_connection_refused"
				} else if strings.Contains(err.Error(), "Unavailable") {
					errorType = "trigger_notify_unavailable"
				} else {
					errorType = "trigger_notify_other_error"
				}

				if o.shouldLogError(errorType, false) {
					o.logger.Info("❌ Failed to notify aggregator of task trigger",
						"task_id", triggerItem.TaskID,
						"trigger_type", "block",
						"block_number", triggerItem.Marker,
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"solution", "Check aggregator connectivity - task will be retried",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}
			}

		case triggerItem := <-eventTriggerCh:
			hasEnrichedData := triggerItem.Marker.EnrichedData != nil
			o.logger.Info("event trigger",
				"task_id", triggerItem.TaskID,
				"marker", triggerItem.Marker,
				"has_enriched_data", hasEnrichedData)

			// Create structured data for the event trigger
			var eventDataMap map[string]interface{}

			if triggerItem.Marker.EnrichedData != nil {
				// Use enriched data if available (includes token metadata, addresses, etc.)
				eventDataMap = triggerItem.Marker.EnrichedData

				// Ensure basic fields are still available for backward compatibility
				if _, exists := eventDataMap["blockNumber"]; !exists {
					eventDataMap["blockNumber"] = triggerItem.Marker.BlockNumber
				}
				if _, exists := eventDataMap["logIndex"]; !exists {
					eventDataMap["logIndex"] = triggerItem.Marker.LogIndex
				}
				if _, exists := eventDataMap["transactionHash"]; !exists {
					eventDataMap["transactionHash"] = triggerItem.Marker.TxHash
				}

				o.logger.Debug("✅ Using enriched event data for task execution",
					"task_id", triggerItem.TaskID,
					"enriched_fields", len(eventDataMap))
			} else {
				// Fall back to basic metadata if no enriched data
				eventDataMap = map[string]interface{}{
					"blockNumber":     triggerItem.Marker.BlockNumber,
					"logIndex":        triggerItem.Marker.LogIndex,
					"transactionHash": triggerItem.Marker.TxHash,
				}

				o.logger.Debug("📋 Using basic event metadata (no enrichment available)",
					"task_id", triggerItem.TaskID)
			}

			eventData, err := structpb.NewStruct(eventDataMap)
			if err != nil {
				o.logger.Error("Failed to create structured event data", "error", err)
				continue
			}

			if resp, err := o.nodeRpcClient.NotifyTriggers(ctx, &avspb.NotifyTriggersReq{
				Address:     o.config.OperatorAddress,
				Signature:   "pending",
				TaskId:      triggerItem.TaskID,
				TriggerType: avspb.TriggerType_TRIGGER_TYPE_EVENT,
				TriggerOutput: &avspb.NotifyTriggersReq_EventTrigger{
					EventTrigger: &avspb.EventTrigger_Output{
						Data: structpb.NewStructValue(eventData),
					},
				},
			}); err == nil {
				o.logger.Debug("Successfully notify aggregator for task hit",
					"taskid", triggerItem.TaskID,
					"remaining_executions", resp.RemainingExecutions,
					"task_still_enabled", resp.TaskStillEnabled,
					"status", resp.Status)

				// Check if task should stop being monitored
				if !resp.TaskStillEnabled || resp.RemainingExecutions == 0 {
					o.logger.Info("🛑 Task exhausted, requesting stop monitoring",
						"task_id", triggerItem.TaskID,
						"remaining_executions", resp.RemainingExecutions,
						"status", resp.Status,
						"message", resp.Message)

					// Remove task from event trigger monitoring
					if o.eventTrigger != nil {
						if err := o.eventTrigger.RemoveCheck(triggerItem.TaskID); err != nil {
							o.logger.Warn("Failed to remove exhausted task from event monitoring",
								"task_id", triggerItem.TaskID,
								"error", err)
						} else {
							o.logger.Info("✅ Removed exhausted task from event monitoring",
								"task_id", triggerItem.TaskID)
						}
					}
				}
			} else {
				// Use debounced logging for trigger notification errors to prevent stack traces
				var errorType string
				if strings.Contains(err.Error(), "connection refused") {
					errorType = "trigger_notify_connection_refused"
				} else if strings.Contains(err.Error(), "Unavailable") {
					errorType = "trigger_notify_unavailable"
				} else {
					errorType = "trigger_notify_other_error"
				}

				if o.shouldLogError(errorType, false) {
					o.logger.Info("❌ Failed to notify aggregator of task trigger",
						"task_id", triggerItem.TaskID,
						"trigger_type", "event",
						"block_number", triggerItem.Marker.BlockNumber,
						"log_index", triggerItem.Marker.LogIndex,
						"tx_hash", triggerItem.Marker.TxHash,
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"solution", "Check aggregator connectivity - task will be retried",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}
			}
		case err := <-metricsErrChan:
			// Handle metrics server errors gracefully without crashing the operator
			if strings.Contains(err.Error(), "address already in use") {
				o.logger.Info("❌ Metrics server port conflict - another service is using the port",
					"port", o.config.GetPublicMetricPort(),
					"solution", "Either stop the conflicting service or change the metrics port in config",
					"impact", "Metrics collection disabled, but operator will continue running",
					"raw_error", fmt.Sprintf("%v", err))
			} else {
				o.logger.Info("❌ Metrics server encountered an error",
					"port", o.config.GetPublicMetricPort(),
					"solution", "Check metrics server configuration and port availability",
					"impact", "Metrics collection disabled, but operator will continue running",
					"raw_error", fmt.Sprintf("%v", err))
			}
			// Continue operation without metrics instead of crashing
		}
	}
}

// StreamMessages setup a streaming connection to receive task from server
func (o *Operator) StreamMessages() {
	id := hex.EncodeToString(o.operatorId[:])
	ctx := context.Background()
	o.logger.Info("Subscribe to aggregator to get check")

	for {
		epoch := time.Now().Unix()
		blsSignature, err := o.GetSignature(ctx, []byte(fmt.Sprintf("operator connection: %s %s %d", o.config.OperatorAddress, id, epoch)))
		if err != nil {
			o.logger.Info("❌ Failed to generate BLS signature for stream connection",
				"operator", o.config.OperatorAddress,
				"solution", "Check BLS key configuration and permissions - will retry in 15 seconds",
				"raw_error", fmt.Sprintf("%v", err))
			time.Sleep(time.Duration(retryIntervalSecond) * time.Second)
			continue
		}

		req := &avspb.SyncMessagesReq{
			Address: o.config.OperatorAddress,
			Id:      id,

			MonotonicClock: epoch,
			Signature:      blsSignature.Serialize(),
			Capabilities: &avspb.SyncMessagesReq_Capabilities{
				EventMonitoring: true, // This operator supports event monitoring
				BlockMonitoring: true, // This operator supports block monitoring
				TimeMonitoring:  true, // This operator supports time/cron monitoring
			},
		}

		stream, err := o.nodeRpcClient.SyncMessages(ctx, req)
		if err != nil {
			var errorType string
			var shouldLog bool

			// Categorize and debounce stream error logging - check more specific patterns first
			if strings.Contains(err.Error(), "connection refused") {
				errorType = "stream_connection_refused"
				shouldLog = o.shouldLogError(errorType, true)
				if shouldLog {
					o.logger.Info("❌ Cannot establish stream to aggregator - service not reachable",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"solution", "Ensure aggregator service is running and accessible",
						"retry_in", "60 seconds",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}

				// Try to recreate connection even for connection refused - aggregator might have restarted
				if recreateErr := o.recreateGrpcConnection(); recreateErr != nil {
					o.logger.Debug("🔄 Connection recreation failed (aggregator likely still down)",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"raw_error", recreateErr)
					// Continue to sleep and retry
				} else {
					o.logger.Info("✅ Connection recreated successfully after connection refused - retrying immediately",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress)
					continue
				}
			} else if strings.Contains(err.Error(), "Unavailable") {
				errorType = "stream_service_unavailable"
				shouldLog = o.shouldLogError(errorType, true)
				if shouldLog {
					o.logger.Info("❌ Aggregator streaming service unavailable",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"solution", "Aggregator may be overloaded or experiencing issues",
						"retry_in", "60 seconds",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}

				// Try to recreate connection for unavailable service - aggregator might have restarted
				if recreateErr := o.recreateGrpcConnection(); recreateErr != nil {
					o.logger.Debug("🔄 Connection recreation failed (aggregator likely still unavailable)",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"raw_error", recreateErr)
					// Continue to sleep and retry
				} else {
					o.logger.Info("✅ Connection recreated successfully after service unavailable - retrying immediately",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress)
					continue
				}
			} else if strings.Contains(err.Error(), "Canceled") || strings.Contains(err.Error(), "connection is closing") {
				errorType = "stream_connection_closing"
				shouldLog = o.shouldLogError(errorType, true)
				if shouldLog {
					o.logger.Info("❌ gRPC connection is closing - will recreate connection",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"solution", "Recreating gRPC client to establish fresh connection",
						"retry_in", "15 seconds",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}

				// Recreate the gRPC connection when it's in closing state
				if recreateErr := o.recreateGrpcConnection(); recreateErr != nil {
					o.logger.Error("❌ Failed to recreate gRPC connection",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"raw_error", recreateErr)
					// Still sleep and retry even if recreation failed
				} else {
					o.logger.Info("✅ gRPC connection recreated successfully - attempting to reconnect immediately",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress)
					// Skip the sleep and try immediately with the new connection
					continue
				}
			} else {
				errorType = "stream_other_error"
				shouldLog = o.shouldLogError(errorType, true)
				if shouldLog {
					o.logger.Info("❌ Failed to open task stream to aggregator",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"solution", "Check network connectivity and aggregator service status",
						"retry_in", "15 seconds",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}
			}
			time.Sleep(time.Duration(retryIntervalSecond) * time.Second)
			continue
		}

		defer stream.CloseSend()
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				o.logger.Info("📡 Stream closed by aggregator (EOF) - will reconnect",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Stream closed normally, retrying connection")
				break // Break out of inner loop to retry connection in outer loop
			}
			if err != nil {
				var errorType string
				var shouldLog bool

				// Categorize and debounce stream receive error logging
				if strings.Contains(err.Error(), "connection") {
					errorType = "stream_lost_connection"
					shouldLog = o.shouldLogError(errorType, true)
					if shouldLog {
						o.logger.Info("❌ Lost connection to aggregator stream",
							"aggregator_address", o.config.AggregatorServerIpPortAddress,
							"operator", o.config.OperatorAddress,
							"solution", "Will attempt to reconnect to aggregator",
							"retry_in", "15 seconds",
							"next_log_in", "3 minutes if error persists",
							"raw_error", fmt.Sprintf("%v", err))
					}
				} else if strings.Contains(err.Error(), "Canceled") || strings.Contains(err.Error(), "connection is closing") {
					errorType = "stream_receive_connection_closing"
					shouldLog = o.shouldLogError(errorType, true)
					if shouldLog {
						o.logger.Info("❌ Stream connection is closing - will recreate gRPC client",
							"aggregator_address", o.config.AggregatorServerIpPortAddress,
							"operator", o.config.OperatorAddress,
							"solution", "Recreating gRPC client to establish fresh connection",
							"retry_in", "15 seconds",
							"next_log_in", "3 minutes if error persists",
							"raw_error", fmt.Sprintf("%v", err))
					}

					// Recreate the gRPC connection when stream receive detects closing state
					if recreateErr := o.recreateGrpcConnection(); recreateErr != nil {
						o.logger.Error("❌ Failed to recreate gRPC connection during stream receive",
							"aggregator_address", o.config.AggregatorServerIpPortAddress,
							"operator", o.config.OperatorAddress,
							"raw_error", recreateErr)
					}
				} else {
					errorType = "stream_receive_error"
					shouldLog = o.shouldLogError(errorType, true)
					if shouldLog {
						o.logger.Info("❌ Error receiving task data from aggregator",
							"aggregator_address", o.config.AggregatorServerIpPortAddress,
							"operator", o.config.OperatorAddress,
							"solution", "Will retry stream connection",
							"retry_in", "15 seconds",
							"next_log_in", "3 minutes if error persists",
							"raw_error", fmt.Sprintf("%v", err))
					}
				}
				time.Sleep(time.Duration(retryIntervalSecond) * time.Second)
				break
			}
			// Add nil check for resp to prevent segmentation fault
			if resp == nil {
				o.logger.Warn("❌ Received nil response from aggregator stream",
					"solution", "This may indicate a protocol mismatch or aggregator issue")
				continue
			}
			// Add nil check for metrics to prevent segmentation fault
			if o.metrics != nil {
				o.metrics.IncNumTasksReceived(resp.Id)
			} else {
				o.logger.Error("🚨 CRITICAL: metrics is nil in StreamMessages - this should never happen in production",
					"task_id", resp.Id,
					"solution", "This indicates a serious initialization problem that needs investigation")
			}

			switch resp.Op {
			case avspb.MessageOp_DisableTask, avspb.MessageOp_DeleteTask:
				o.processMessage(resp)
			case avspb.MessageOp_MonitorTaskTrigger:
				// Add nil check to prevent segmentation fault
				if resp.TaskMetadata == nil {
					o.logger.Warn("❌ Received MonitorTaskTrigger message with nil TaskMetadata",
						"task_id", resp.Id,
						"solution", "This may indicate a protocol mismatch or aggregator issue")
					continue
				}

				// Additional nil check for Trigger field
				triggerObj := resp.TaskMetadata.GetTrigger()
				if triggerObj == nil {
					o.logger.Warn("❌ Received MonitorTaskTrigger message with nil Trigger",
						"task_id", resp.Id,
						"solution", "This may indicate a protocol mismatch or aggregator issue")
					continue
				}

				if trigger := triggerObj.GetEvent(); trigger != nil {
					o.logger.Info("📥 Monitoring event trigger", "task_id", resp.Id)

					// Safely call AddCheck with panic recovery
					func() {
						defer func() {
							if r := recover(); r != nil {
								o.logger.Error("🚨 CRITICAL: eventTrigger.AddCheck() caused segmentation fault",
									"task_id", resp.Id,
									"panic", r,
									"solution", "resp.TaskMetadata is corrupted - cannot add event trigger")
							}
						}()
						if err := o.eventTrigger.AddCheck(resp.TaskMetadata); err != nil {
							o.logger.Info("❌ Failed to add event trigger to monitoring", "error", err, "task_id", resp.Id, "solution", "Task may not be monitored for events")
						}
					}()
				} else if trigger := triggerObj.GetBlock(); trigger != nil {
					o.logger.Info("📦 Monitoring block trigger", "task_id", resp.Id, "interval", trigger.Config.GetInterval())

					// Safely call AddCheck with panic recovery
					func() {
						defer func() {
							if r := recover(); r != nil {
								o.logger.Error("🚨 CRITICAL: blockTrigger.AddCheck() caused segmentation fault",
									"task_id", resp.Id,
									"panic", r,
									"solution", "resp.TaskMetadata is corrupted - cannot add block trigger")
							}
						}()
						if err := o.blockTrigger.AddCheck(resp.TaskMetadata); err != nil {
							o.logger.Info("❌ Failed to add block trigger to monitoring", "error", err, "task_id", resp.Id, "solution", "Task may not be monitored for blocks")
						}
					}()
				} else if trigger := triggerObj.GetCron(); trigger != nil {
					scheduleInfo := "unknown"
					if trigger.Config != nil && trigger.Config.Schedules != nil {
						scheduleInfo = strings.Join(trigger.Config.Schedules, ", ")
					}
					o.logger.Info("⏰ Monitoring cron trigger", "task_id", resp.Id, "schedule", scheduleInfo)

					// Safely call AddCheck with panic recovery
					func() {
						defer func() {
							if r := recover(); r != nil {
								o.logger.Error("🚨 CRITICAL: timeTrigger.AddCheck() caused segmentation fault",
									"task_id", resp.Id,
									"panic", r,
									"solution", "resp.TaskMetadata is corrupted - cannot add cron trigger")
							}
						}()
						if err := o.timeTrigger.AddCheck(resp.TaskMetadata); err != nil {
							o.logger.Info("❌ Failed to add cron trigger to monitoring", "error", err, "task_id", resp.Id, "solution", "Task may not be monitored for scheduled execution")
						}
					}()
				} else if trigger := triggerObj.GetFixedTime(); trigger != nil {
					epochInfo := "unknown"
					if trigger.Config != nil && trigger.Config.Epochs != nil {
						epochCount := len(trigger.Config.Epochs)
						if epochCount == 1 {
							epochInfo = fmt.Sprintf("epoch: %d", trigger.Config.Epochs[0])
						} else {
							epochInfo = fmt.Sprintf("%d epochs", epochCount)
						}
					}
					o.logger.Info("📅 Monitoring fixed time trigger", "task_id", resp.Id, "epoch_info", epochInfo)

					// Safely call AddCheck with panic recovery
					func() {
						defer func() {
							if r := recover(); r != nil {
								o.logger.Error("🚨 CRITICAL: timeTrigger.AddCheck() caused segmentation fault",
									"task_id", resp.Id,
									"panic", r,
									"solution", "resp.TaskMetadata is corrupted - cannot add fixed time trigger")
							}
						}()
						if err := o.timeTrigger.AddCheck(resp.TaskMetadata); err != nil {
							o.logger.Info("❌ Failed to add fixed time trigger to monitoring", "error", err, "task_id", resp.Id, "solution", "Task may not be monitored for fixed time execution")
						}
					}()
				} else {
					o.logger.Warn("❓ Unsupported or unrecognized trigger type",
						"task_id", resp.Id,
						"solution", "Task may not be monitored")
				}
			}
		}
	}
}

func (o *Operator) PingServer() {
	// Ensure this function never panics or appears to fail to gocron
	defer func() {
		if r := recover(); r != nil {
			// Silent recovery - don't even log to avoid triggering gocron stack traces
		}
	}()

	// Add nil check for metrics to prevent segmentation fault
	if o.metrics != nil {
		o.metrics.IncWorkerLoop()
		elapse := o.elapsing.Report()
		o.metrics.AddUptime(float64(elapse.Milliseconds()))
	} else {
		o.logger.Error("🚨 CRITICAL: metrics is nil in PingServer - this should never happen in production",
			"solution", "This indicates a serious initialization problem that needs investigation")
	}

	id := hex.EncodeToString(o.operatorId[:])
	start := time.Now()

	blsSignature, err := o.GetSignature(context.Background(), []byte(fmt.Sprintf("ping from %s ip %s", o.config.OperatorAddress, o.GetPublicIP())))

	if blsSignature == nil {
		// Use debounced logging for BLS signature errors
		if o.shouldLogError("bls_signature_error", false) {
			o.logger.Info("❌ Failed to generate BLS signature for ping",
				"operator", o.config.OperatorAddress,
				"solution", "Check BLS key configuration and permissions",
				"next_log_in", "3 minutes if error persists",
				"raw_error", fmt.Sprintf("%v", err))
		}
		return
	}

	str := base64.StdEncoding.EncodeToString(blsSignature.Serialize())

	_, err = o.nodeRpcClient.Ping(context.Background(), &avspb.Checkin{
		Address:     o.config.OperatorAddress,
		Id:          id,
		Signature:   str,
		Version:     version.Get(),
		RemoteIP:    o.GetPublicIP(),
		MetricsPort: o.config.GetPublicMetricPort(),
		BlockNumber: o.blockTrigger.GetProgress(),
		EventCount:  o.eventTrigger.GetProgress(),
	})

	if err != nil {
		var errorType string
		var shouldLog bool

		// Categorize and debounce error logging - check more specific patterns first
		if strings.Contains(err.Error(), "connection refused") {
			errorType = "ping_connection_refused"
		} else if strings.Contains(err.Error(), "context deadline exceeded") || strings.Contains(err.Error(), "timeout") {
			errorType = "ping_timeout"
		} else if strings.Contains(err.Error(), "Unavailable") {
			errorType = "ping_service_unavailable"
		} else if strings.Contains(err.Error(), "Canceled") || strings.Contains(err.Error(), "connection is closing") {
			errorType = "ping_connection_closing"
		} else {
			errorType = "ping_other_error"
		}

		shouldLog = o.shouldLogError(errorType, false)
		if shouldLog {
			switch errorType {
			case "ping_connection_refused":
				o.logger.Info("❌ Cannot connect to aggregator service - is the aggregator running?",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Please ensure the aggregator service is running and accessible",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))
			case "ping_timeout":
				o.logger.Info("❌ Connection to aggregator timed out",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Check network connectivity and aggregator response time",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))
			case "ping_service_unavailable":
				o.logger.Info("❌ Aggregator service is unavailable",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Check if the aggregator is overloaded or experiencing issues",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))
			case "ping_connection_closing":
				o.logger.Info("❌ Connection to aggregator was closed - recreating gRPC client",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Recreating gRPC client to establish fresh connection",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))

				// Recreate the gRPC connection when ping detects closing state
				if recreateErr := o.recreateGrpcConnection(); recreateErr != nil {
					o.logger.Error("❌ Failed to recreate gRPC connection during ping",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"raw_error", recreateErr)
				}
			default:
				o.logger.Info("❌ Failed to ping aggregator service",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Check aggregator configuration and network connectivity",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))
			}
		}
	} else {
		// Log successful ping status with tiered intervals
		now := time.Now()
		timeSinceLastSuccess := now.Sub(o.lastPingSuccessTime)

		// Always log the first success after any error (status change)
		wasInErrorState := o.lastPingSuccessTime.IsZero() || timeSinceLastSuccess >= errorLogDebounceInterval

		// Debug logging every 5 minutes, Info logging every 30 minutes
		debugInterval := 5 * time.Minute
		infoInterval := 30 * time.Minute
		timeSinceLastLog := now.Sub(o.lastPingSuccessTime)
		shouldLogDebug := timeSinceLastLog >= debugInterval
		shouldLogInfo := timeSinceLastLog >= infoInterval

		if wasInErrorState {
			// Status change: error -> success (always INFO level)
			o.logger.Info("Successfully pinged aggregator", "aggregator_address", o.config.AggregatorServerIpPortAddress)
			o.lastPingSuccessTime = now
		} else if shouldLogInfo {
			// Periodic health confirmation at INFO level (every 30 minutes)
			o.logger.Info("Successfully pinged aggregator", "aggregator_address", o.config.AggregatorServerIpPortAddress)
			o.lastPingSuccessTime = now
		} else if shouldLogDebug {
			// More frequent logging at DEBUG level (every 5 minutes)
			o.logger.Debug("Successfully pinged aggregator", "aggregator_address", o.config.AggregatorServerIpPortAddress)
			o.lastPingSuccessTime = now
		}
	}

	elapsed := time.Since(start)
	// Add nil check for metrics to prevent segmentation fault
	if o.metrics != nil {
		if err == nil {
			o.metrics.IncPing("success")
		} else {
			o.metrics.IncPing("error")
			// Don't log additional errors here - they were already logged above with proper debouncing
			// Duplicate logging here with different error types breaks the debounce logic
		}
		o.metrics.SetPingDuration(elapsed.Seconds())
	} else {
		o.logger.Error("🚨 CRITICAL: metrics is nil in PingServer end - this should never happen in production",
			"solution", "This indicates a serious initialization problem that needs investigation")
	}
}
