package operator

import (
	avspb "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func (o *Operator) processMessage(resp *avspb.SyncMessagesResp) {
	switch resp.Op {
	case avspb.MessageOp_CancelTask, avspb.MessageOp_DeleteTask:
		o.logger.Info("removing task from all triggers", "task_id", resp.TaskMetadata.TaskId, "operation", resp.Op)
		o.eventTrigger.RemoveCheck(resp.TaskMetadata.TaskId)
		o.blockTrigger.RemoveCheck(resp.TaskMetadata.TaskId)
		o.timeTrigger.RemoveCheck(resp.TaskMetadata.TaskId)
	}
}
