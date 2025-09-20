package operator

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"google.golang.org/protobuf/types/known/structpb"

	avspb "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func (o *Operator) processMessage(resp *avspb.SyncMessagesResp) {
	switch resp.Op {
	case avspb.MessageOp_CancelTask, avspb.MessageOp_DeleteTask:
		// Get task ID from either TaskMetadata or message ID
		var taskID string
		if resp.TaskMetadata != nil {
			taskID = resp.TaskMetadata.TaskId
		} else {
			// Fallback to using the message ID as task ID
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
			taskID = resp.Id
		}

		o.logger.Info("processing immediate trigger request", "task_id", taskID)

		// For immediate trigger, we need to know which trigger type to execute
		// Check the task metadata to determine trigger type
		if resp.TaskMetadata != nil && resp.TaskMetadata.Trigger != nil {
			o.logger.Info("executing immediate trigger", "task_id", taskID)

			// Execute the appropriate trigger type immediately
			// Check the trigger type from the task metadata using the oneof field
			if resp.TaskMetadata.Trigger.GetBlock() != nil {
				o.executeImmediateBlockTrigger(taskID)
			} else if resp.TaskMetadata.Trigger.GetCron() != nil || resp.TaskMetadata.Trigger.GetFixedTime() != nil {
				o.executeImmediateTimeTrigger(taskID)
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
	ctx := context.Background()

	// Get current block number from RPC
	ethClient, err := ethclient.Dial(o.config.TargetChain.EthRpcUrl)
	if err != nil {
		o.logger.Error("Failed to connect to RPC for immediate block trigger",
			"task_id", taskID,
			"rpc_url", o.config.TargetChain.EthRpcUrl,
			"error", err)
		return
	}
	defer ethClient.Close()

	// Get current block header
	header, err := ethClient.HeaderByNumber(ctx, nil) // nil means latest block
	if err != nil {
		o.logger.Error("Failed to get current block header for immediate trigger",
			"task_id", taskID,
			"error", err)
		return
	}

	blockNumber := header.Number.Int64()

	// Create block data similar to normal block trigger processing
	blockData := map[string]interface{}{
		"blockNumber": uint64(blockNumber),
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
func (o *Operator) executeImmediateTimeTrigger(taskID string) {
	ctx := context.Background()

	// Get current timestamp in nanoseconds
	currentTime := time.Now()
	timestampNanos := uint64(currentTime.UnixNano())

	cronData := map[string]interface{}{
		"timestamp":    timestampNanos,
		"timestampIso": currentTime.UTC().Format("2006-01-02T15:04:05.000Z"),
	}

	o.logger.Info("🚀 Executing immediate time trigger",
		"task_id", taskID,
		"timestamp", timestampNanos,
		"timestamp_iso", currentTime.UTC().Format("2006-01-02T15:04:05.000Z"))

	// Send trigger notification to aggregator
	if resp, err := o.nodeRpcClient.NotifyTriggers(ctx, &avspb.NotifyTriggersReq{
		Address:     o.config.OperatorAddress,
		Signature:   "pending",
		TaskId:      taskID,
		TriggerType: avspb.TriggerType_TRIGGER_TYPE_CRON,
		TriggerOutput: &avspb.NotifyTriggersReq_CronTrigger{
			CronTrigger: &avspb.CronTrigger_Output{
				Data: func() *structpb.Value {
					dataValue, _ := structpb.NewValue(cronData)
					return dataValue
				}(),
			},
		},
	}); err == nil {
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
