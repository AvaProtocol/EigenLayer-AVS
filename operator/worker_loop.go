package operator

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
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
	if o.config.EnableMetrics {
		metricsErrChan = o.metrics.Start(ctx, o.metricsReg)
	} else {
		metricsErrChan = make(chan error, 1)
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

			if resp, err := o.nodeRpcClient.NotifyTriggers(context.Background(), &avspb.NotifyTriggersReq{
				Address:     o.config.OperatorAddress,
				Signature:   "pending",
				TaskId:      triggerItem.TaskID,
				TriggerType: avspb.TriggerType_TRIGGER_TYPE_CRON,
				TriggerOutput: &avspb.NotifyTriggersReq_CronTrigger{
					CronTrigger: &avspb.CronTrigger_Output{
						Timestamp:    uint64(triggerItem.Marker),
						TimestampIso: time.Unix(0, int64(triggerItem.Marker)*1000000).UTC().Format("2006-01-02T15:04:05.000Z"),
					},
				},
			}); err == nil {
				o.logger.Debug("Successfully notify aggregator for task hit",
					"taskid", triggerItem.TaskID,
					"remaining_executions", resp.RemainingExecutions,
					"task_still_active", resp.TaskStillActive,
					"status", resp.Status)

				// Check if task should stop being monitored
				if !resp.TaskStillActive || resp.RemainingExecutions == 0 {
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
			if taskCount == 1 || taskCount%5 == 0 {
				o.logger.Info("block trigger summary", "block", blockNum, "task_count", taskCount)
			}
			blockTasksMutex.Unlock()

			if resp, err := o.nodeRpcClient.NotifyTriggers(context.Background(), &avspb.NotifyTriggersReq{
				Address:     o.config.OperatorAddress,
				Signature:   "pending",
				TaskId:      triggerItem.TaskID,
				TriggerType: avspb.TriggerType_TRIGGER_TYPE_BLOCK,
				TriggerOutput: &avspb.NotifyTriggersReq_BlockTrigger{
					BlockTrigger: &avspb.BlockTrigger_Output{
						BlockNumber: uint64(triggerItem.Marker),
						// Other block fields would be populated here if available
						BlockHash:  "",
						Timestamp:  0,
						ParentHash: "",
						Difficulty: "",
						GasLimit:   0,
						GasUsed:    0,
					},
				},
			}); err == nil {
				o.logger.Debug("Successfully notify aggregator for task hit",
					"taskid", triggerItem.TaskID,
					"remaining_executions", resp.RemainingExecutions,
					"task_still_active", resp.TaskStillActive,
					"status", resp.Status)

				// Check if task should stop being monitored
				if !resp.TaskStillActive || resp.RemainingExecutions == 0 {
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
			o.logger.Info("event trigger", "task_id", triggerItem.TaskID, "marker", triggerItem.Marker)

			if resp, err := o.nodeRpcClient.NotifyTriggers(context.Background(), &avspb.NotifyTriggersReq{
				Address:     o.config.OperatorAddress,
				Signature:   "pending",
				TaskId:      triggerItem.TaskID,
				TriggerType: avspb.TriggerType_TRIGGER_TYPE_EVENT,
				TriggerOutput: &avspb.NotifyTriggersReq_EventTrigger{
					EventTrigger: &avspb.EventTrigger_Output{
						// Create an EVM log output with the event data using oneof
						OutputType: &avspb.EventTrigger_Output_EvmLog{
							EvmLog: &avspb.Evm_Log{
								BlockNumber:     uint64(triggerItem.Marker.BlockNumber),
								Index:           uint32(triggerItem.Marker.LogIndex),
								TransactionHash: triggerItem.Marker.TxHash,
								// Other fields would be populated if available
								Address: "",
								Topics:  []string{},
								Data:    "",
							},
						},
					},
				},
			}); err == nil {
				o.logger.Debug("Successfully notify aggregator for task hit",
					"taskid", triggerItem.TaskID,
					"remaining_executions", resp.RemainingExecutions,
					"task_still_active", resp.TaskStillActive,
					"status", resp.Status)

				// Check if task should stop being monitored
				if !resp.TaskStillActive || resp.RemainingExecutions == 0 {
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
	o.logger.Info("🚀 DEBUG: StreamMessages started - SEGFAULT_FIX_v2.0 - Enhanced logging",
		"timestamp", time.Now().Format(time.RFC3339),
		"fix_version", "v2.0")

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
						"retry_in", "15 seconds",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}
			} else if strings.Contains(err.Error(), "Unavailable") {
				errorType = "stream_service_unavailable"
				shouldLog = o.shouldLogError(errorType, true)
				if shouldLog {
					o.logger.Info("❌ Aggregator streaming service unavailable",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"solution", "Aggregator may be overloaded or experiencing issues",
						"retry_in", "15 seconds",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}
			} else if strings.Contains(err.Error(), "Canceled") || strings.Contains(err.Error(), "connection is closing") {
				errorType = "stream_connection_closing"
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
				return
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
						o.logger.Info("❌ Error receiving task data from aggregator",
							"aggregator_address", o.config.AggregatorServerIpPortAddress,
							"operator", o.config.OperatorAddress,
							"solution", "Will retry stream connection",
							"retry_in", "15 seconds",
							"next_log_in", "3 minutes if error persists",
							"raw_error", fmt.Sprintf("%v", err))
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
			o.metrics.IncNumTasksReceived(resp.Id)

			switch resp.Op {
			case avspb.MessageOp_CancelTask, avspb.MessageOp_DeleteTask:
				o.processMessage(resp)
			case avspb.MessageOp_MonitorTaskTrigger:
				o.logger.Info("🔍 DEBUG: Received MonitorTaskTrigger message",
					"task_id", resp.Id,
					"resp_nil", resp == nil,
					"task_metadata_nil", resp.TaskMetadata == nil)

				// Add nil check to prevent segmentation fault
				if resp.TaskMetadata == nil {
					o.logger.Warn("❌ Received MonitorTaskTrigger message with nil TaskMetadata",
						"task_id", resp.Id,
						"solution", "This may indicate a protocol mismatch or aggregator issue")
					continue
				}

				o.logger.Info("🔍 DEBUG: TaskMetadata is not nil, checking GetTrigger()",
					"task_id", resp.Id,
					"task_metadata_trigger_nil", resp.TaskMetadata.Trigger == nil)

				// Additional nil check for Trigger field
				triggerObj := resp.TaskMetadata.GetTrigger()
				o.logger.Info("🔍 DEBUG: Called GetTrigger()",
					"task_id", resp.Id,
					"trigger_obj_nil", triggerObj == nil)

				if triggerObj == nil {
					o.logger.Warn("❌ Received MonitorTaskTrigger message with nil Trigger",
						"task_id", resp.Id,
						"solution", "This may indicate a protocol mismatch or aggregator issue")
					continue
				}

				o.logger.Info("🔍 DEBUG: About to check trigger types",
					"task_id", resp.Id,
					"checking_event_trigger", true)

				if trigger := triggerObj.GetEvent(); trigger != nil {
					o.logger.Info("📥 Monitoring event trigger", "task_id", resp.Id)
					if err := o.eventTrigger.AddCheck(resp.TaskMetadata); err != nil {
						o.logger.Info("❌ Failed to add event trigger to monitoring", "error", err, "task_id", resp.Id, "solution", "Task may not be monitored for events")
					}
				} else {
					o.logger.Info("🔍 DEBUG: Event trigger is nil, checking block trigger",
						"task_id", resp.Id)

					if trigger := triggerObj.GetBlock(); trigger != nil {
						o.logger.Info("📦 Monitoring block trigger", "task_id", resp.Id, "interval", trigger.Config.Interval)
						if err := o.blockTrigger.AddCheck(resp.TaskMetadata); err != nil {
							o.logger.Info("❌ Failed to add block trigger to monitoring", "error", err, "task_id", resp.Id, "solution", "Task may not be monitored for blocks")
						}
					} else {
						o.logger.Info("🔍 DEBUG: Block trigger is nil, checking cron trigger",
							"task_id", resp.Id)

						if trigger := triggerObj.GetCron(); trigger != nil {
							scheduleStr := strings.Join(trigger.Config.Schedules, ", ")
							o.logger.Info("⏰ Monitoring cron trigger", "task_id", resp.Id, "schedule", scheduleStr)
							if err := o.timeTrigger.AddCheck(resp.TaskMetadata); err != nil {
								o.logger.Info("❌ Failed to add cron trigger to monitoring", "error", err, "task_id", resp.Id, "solution", "Task may not be monitored for scheduled execution")
							}
						} else {
							o.logger.Info("🔍 DEBUG: Cron trigger is nil, checking fixed time trigger",
								"task_id", resp.Id)

							if trigger := triggerObj.GetFixedTime(); trigger != nil {
								epochCount := len(trigger.Config.Epochs)
								var epochInfo string
								if epochCount == 1 {
									epochInfo = fmt.Sprintf("epoch: %d", trigger.Config.Epochs[0])
								} else {
									epochInfo = fmt.Sprintf("%d epochs", epochCount)
								}
								o.logger.Info("📅 Monitoring fixed time trigger", "task_id", resp.Id, "epoch_info", epochInfo)
								if err := o.timeTrigger.AddCheck(resp.TaskMetadata); err != nil {
									o.logger.Info("❌ Failed to add fixed time trigger to monitoring", "error", err, "task_id", resp.Id, "solution", "Task may not be monitored for fixed time execution")
								}
							} else {
								o.logger.Warn("❓ Unsupported or unrecognized trigger type",
									"task_id", resp.Id,
									"trigger_obj_nil", triggerObj == nil,
									"event_nil", triggerObj.GetEvent() == nil,
									"block_nil", triggerObj.GetBlock() == nil,
									"cron_nil", triggerObj.GetCron() == nil,
									"fixed_time_nil", triggerObj.GetFixedTime() == nil,
									"solution", "Task may not be monitored")
							}
						}
					}
				}

				o.logger.Info("🔍 DEBUG: Completed MonitorTaskTrigger processing",
					"task_id", resp.Id)
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

	o.metrics.IncWorkerLoop()
	elapse := o.elapsing.Report()
	o.metrics.AddUptime(float64(elapse.Milliseconds()))

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
			if errorType == "ping_connection_refused" {
				o.logger.Info("❌ Cannot connect to aggregator service - is the aggregator running?",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Please ensure the aggregator service is running and accessible",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))
			} else if errorType == "ping_timeout" {
				o.logger.Info("❌ Connection to aggregator timed out",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Check network connectivity and aggregator response time",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))
			} else if errorType == "ping_service_unavailable" {
				o.logger.Info("❌ Aggregator service is unavailable",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Check if the aggregator is overloaded or experiencing issues",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))
			} else if errorType == "ping_connection_closing" {
				o.logger.Info("❌ Connection to aggregator was closed",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Connection will be reestablished automatically",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))
			} else {
				o.logger.Info("❌ Failed to ping aggregator service",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Check aggregator configuration and network connectivity",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))
			}
		}
	} else {
		// Debounce successful ping messages - only log every 3 minutes
		now := time.Now()
		timeSinceLastSuccess := now.Sub(o.lastPingSuccessTime)
		if timeSinceLastSuccess >= errorLogDebounceInterval {
			o.logger.Debug("✅ Successfully pinged aggregator", "aggregator_address", o.config.AggregatorServerIpPortAddress)
			o.lastPingSuccessTime = now
		}
	}

	elapsed := time.Since(start)
	if err == nil {
		o.metrics.IncPing("success")
	} else {
		o.metrics.IncPing("error")
		// Don't log additional errors here - they were already logged above with proper debouncing
		// Duplicate logging here with different error types breaks the debounce logic
	}
	o.metrics.SetPingDuration(elapsed.Seconds())
}
