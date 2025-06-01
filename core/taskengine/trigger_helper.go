package taskengine

import (
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

// TriggerData represents the flattened trigger information
type TriggerData struct {
	Type   avsproto.TriggerType
	Output interface{} // Will hold the specific trigger output (BlockTrigger.Output, etc.)
}

// ExtractTriggerOutput extracts trigger output from any oneof field (TriggerTaskReq or NotifyTriggersReq)
func ExtractTriggerOutput(triggerOutput interface{}) interface{} {
	switch output := triggerOutput.(type) {
	// TriggerTaskReq oneof cases
	case *avsproto.TriggerTaskReq_BlockTrigger:
		return output.BlockTrigger
	case *avsproto.TriggerTaskReq_FixedTimeTrigger:
		return output.FixedTimeTrigger
	case *avsproto.TriggerTaskReq_CronTrigger:
		return output.CronTrigger
	case *avsproto.TriggerTaskReq_EventTrigger:
		return output.EventTrigger
	case *avsproto.TriggerTaskReq_ManualTrigger:
		return output.ManualTrigger

	// NotifyTriggersReq oneof cases
	case *avsproto.NotifyTriggersReq_BlockTrigger:
		return output.BlockTrigger
	case *avsproto.NotifyTriggersReq_FixedTimeTrigger:
		return output.FixedTimeTrigger
	case *avsproto.NotifyTriggersReq_CronTrigger:
		return output.CronTrigger
	case *avsproto.NotifyTriggersReq_EventTrigger:
		return output.EventTrigger
	case *avsproto.NotifyTriggersReq_ManualTrigger:
		return output.ManualTrigger
	}
	return nil
}

func GetTriggerReasonOrDefault(queueData *QueueExecutionData, taskID string, logger sdklogging.Logger) *TriggerData {
	if queueData != nil && queueData.TriggerType != avsproto.TriggerType_TRIGGER_TYPE_UNSPECIFIED {
		return &TriggerData{
			Type:   queueData.TriggerType,
			Output: queueData.TriggerOutput,
		}
	}

	logger.Debug("Creating default trigger data", "task_id", taskID)
	return &TriggerData{
		Type:   avsproto.TriggerType_TRIGGER_TYPE_UNSPECIFIED,
		Output: nil,
	}
}
