package operator

import (
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
	}
}
