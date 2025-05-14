package taskengine

import (
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

func GetTriggerReasonOrDefault(reason *avsproto.TriggerReason, taskID string, logger sdklogging.Logger) *avsproto.TriggerReason {
	if reason != nil {
		return reason
	}

	logger.Debug("Creating default trigger reason", "task_id", taskID)
	return &avsproto.TriggerReason{
		Type: avsproto.TriggerReason_Unset,
	}
}
