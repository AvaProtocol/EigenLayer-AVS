package operator

import (
	"context"
	"regexp"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	avspb "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// isValidTaskIDFormat validates that a string looks like a valid task ID
// Task IDs are typically ULIDs or UUIDs with specific format constraints
func isValidTaskIDFormat(id string) bool {
	if id == "" {
		return false
	}

	// Check for common task ID patterns:
	// 1. ULID format (26 characters, base32)
	// 2. UUID format (36 characters with hyphens)
	// 3. Simple alphanumeric format (at least 8 characters)

	// ULID pattern: 26 characters, base32 encoded
	ulidPattern := regexp.MustCompile(`^[0123456789ABCDEFGHJKMNPQRSTVWXYZ]{26}$`)
	if ulidPattern.MatchString(id) {
		return true
	}

	// UUID pattern: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
	uuidPattern := regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	if uuidPattern.MatchString(id) {
		return true
	}

	// Alphanumeric pattern: at least 8 characters, alphanumeric with optional hyphens/underscores
	alphanumericPattern := regexp.MustCompile(`^[a-zA-Z0-9_-]{8,}$`)
	if alphanumericPattern.MatchString(id) {
		return true
	}

	return false
}

func (o *Operator) processMessage(resp *avspb.SyncMessagesResp) {
	switch resp.Op {
	case avspb.MessageOp_CancelTask, avspb.MessageOp_DeleteTask:
		// Get task ID from either TaskMetadata or message ID
		var taskID string
		if resp.TaskMetadata != nil {
			taskID = resp.TaskMetadata.TaskId
		} else {
			// Fallback to using the message ID as task ID
			o.logger.Warn("TaskMetadata missing, falling back to message ID as task ID",
				"message_id", resp.Id,
				"operation", resp.Op)

			// Validate that the message ID looks like a valid task ID
			if !isValidTaskIDFormat(resp.Id) {
				o.logger.Error("Message ID does not appear to be a valid task ID format",
					"message_id", resp.Id,
					"operation", resp.Op)
				return
			}
			taskID = resp.Id
		}

		o.logger.Info("removing task from all triggers", "task_id", taskID, "operation", resp.Op)
		o.eventTrigger.RemoveCheck(taskID)
		o.blockTrigger.RemoveCheck(taskID)
		o.timeTrigger.RemoveCheck(taskID)
	case avspb.MessageOp_ImmediateTrigger:
		// Get task ID from either TaskMetadata or message ID
		var taskID string
		if resp.TaskMetadata != nil {
			taskID = resp.TaskMetadata.TaskId
		} else {
			// Fallback to using the message ID as task ID
			o.logger.Warn("TaskMetadata missing, falling back to message ID as task ID",
				"message_id", resp.Id,
				"operation", resp.Op)

			// Validate that the message ID looks like a valid task ID
			if !isValidTaskIDFormat(resp.Id) {
				o.logger.Error("Message ID does not appear to be a valid task ID format",
					"message_id", resp.Id,
					"operation", resp.Op)
				return
			}
			taskID = resp.Id
		}

		o.logger.Debug("📨 Received immediate trigger instruction from aggregator",
			"task_id", taskID,
			"operator", o.config.OperatorAddress,
			"has_task_metadata", resp.TaskMetadata != nil)

		// For immediate trigger, we need to know which trigger type to execute
		// Check the task metadata to determine trigger type
		if resp.TaskMetadata != nil && resp.TaskMetadata.Trigger != nil {
			o.logger.Debug("🔥 Executing immediate trigger",
				"task_id", taskID,
				"trigger_type", resp.TaskMetadata.Trigger.Type.String())

			// Execute the appropriate trigger type immediately
			// Check the trigger type from the task metadata using the oneof field
			if resp.TaskMetadata.Trigger.GetBlock() != nil {
				o.executeImmediateBlockTrigger(taskID)
			} else if resp.TaskMetadata.Trigger.GetCron() != nil {
				o.executeImmediateTimeTrigger(taskID, avspb.TriggerType_TRIGGER_TYPE_CRON)
			} else if resp.TaskMetadata.Trigger.GetFixedTime() != nil {
				o.executeImmediateTimeTrigger(taskID, avspb.TriggerType_TRIGGER_TYPE_FIXED_TIME)
			} else {
				o.logger.Warn("unsupported immediate trigger type for task", "task_id", taskID)
			}
		} else {
			o.logger.Warn("immediate trigger request missing metadata", "task_id", taskID)
		}
	}
}

// executeImmediateBlockTrigger executes a block trigger immediately with current block data
func (o *Operator) executeImmediateBlockTrigger(taskID string) {
	// Create context with timeout to avoid hanging on slow RPC endpoints
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use shared targetEthClient instead of creating new connection
	if o.targetEthClient == nil {
		o.logger.Error("Target RPC client not available for immediate block trigger",
			"task_id", taskID)
		return
	}

	// Get current block header with timeout context
	header, err := o.targetEthClient.HeaderByNumber(ctx, nil) // nil means latest block
	if err != nil {
		o.logger.Error("Failed to get current block header for immediate trigger",
			"task_id", taskID,
			"error", err)
		return
	}

	blockNumber := header.Number.Uint64()

	// Create block data similar to normal block trigger processing
	blockData := map[string]interface{}{
		"blockNumber": blockNumber,
		"blockHash":   header.Hash().Hex(),
		"timestamp":   header.Time,
		"parentHash":  header.ParentHash.Hex(),
		"difficulty":  header.Difficulty.String(),
		"gasLimit":    header.GasLimit,
		"gasUsed":     header.GasUsed,
	}

	o.logger.Info("🚀 Executing immediate block trigger",
		"task_id", taskID,
		"block_number", blockNumber,
		"block_hash", header.Hash().Hex(),
		"timestamp", header.Time)

	// Send trigger notification to aggregator
	if resp, err := o.nodeRpcClient.NotifyTriggers(ctx, &avspb.NotifyTriggersReq{
		Address:     o.config.OperatorAddress,
		Signature:   "pending",
		TaskId:      taskID,
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
		o.logger.Info("✅ Successfully executed immediate block trigger",
			"task_id", taskID,
			"block_number", blockNumber,
			"remaining_executions", resp.RemainingExecutions,
			"task_still_active", resp.TaskStillActive,
			"status", resp.Status)
	} else {
		o.logger.Error("❌ Failed to execute immediate block trigger",
			"task_id", taskID,
			"block_number", blockNumber,
			"error", err)
	}
}

// executeImmediateTimeTrigger executes a time/cron trigger immediately
func (o *Operator) executeImmediateTimeTrigger(taskID string, triggerType avspb.TriggerType) {
	// Create context with timeout to avoid hanging on slow aggregator endpoints
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get current timestamp in nanoseconds
	currentTime := time.Now()
	timestampNanos := uint64(currentTime.UnixNano())

	timeData := map[string]interface{}{
		"timestamp":    timestampNanos,
		"timestampIso": currentTime.UTC().Format("2006-01-02T15:04:05.000Z"),
	}

	o.logger.Info("🚀 Executing immediate time trigger",
		"task_id", taskID,
		"trigger_type", triggerType.String(),
		"timestamp", timestampNanos,
		"timestamp_iso", currentTime.UTC().Format("2006-01-02T15:04:05.000Z"))

	// Create the NotifyTriggersReq with appropriate trigger output based on the trigger type
	var notifyReq *avspb.NotifyTriggersReq
	if triggerType == avspb.TriggerType_TRIGGER_TYPE_CRON {
		notifyReq = &avspb.NotifyTriggersReq{
			Address:     o.config.OperatorAddress,
			Signature:   "pending",
			TaskId:      taskID,
			TriggerType: triggerType,
			TriggerOutput: &avspb.NotifyTriggersReq_CronTrigger{
				CronTrigger: &avspb.CronTrigger_Output{
					Data: func() *structpb.Value {
						dataValue, _ := structpb.NewValue(timeData)
						return dataValue
					}(),
				},
			},
		}
	} else {
		notifyReq = &avspb.NotifyTriggersReq{
			Address:     o.config.OperatorAddress,
			Signature:   "pending",
			TaskId:      taskID,
			TriggerType: triggerType,
			TriggerOutput: &avspb.NotifyTriggersReq_FixedTimeTrigger{
				FixedTimeTrigger: &avspb.FixedTimeTrigger_Output{
					Data: func() *structpb.Value {
						dataValue, _ := structpb.NewValue(timeData)
						return dataValue
					}(),
				},
			},
		}
	}

	// Send trigger notification to aggregator
	if resp, err := o.nodeRpcClient.NotifyTriggers(ctx, notifyReq); err == nil {
		o.logger.Info("✅ Successfully executed immediate time trigger",
			"task_id", taskID,
			"timestamp", timestampNanos,
			"remaining_executions", resp.RemainingExecutions,
			"task_still_active", resp.TaskStillActive,
			"status", resp.Status)
	} else {
		o.logger.Error("❌ Failed to execute immediate time trigger",
			"task_id", taskID,
			"timestamp", timestampNanos,
			"error", err)
	}
}
