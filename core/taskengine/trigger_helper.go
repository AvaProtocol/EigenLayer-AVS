package taskengine

import (
	"log/slog"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func GetTriggerReasonOrDefault(reason *avsproto.TriggerReason, taskID string, logger *slog.Logger) *avsproto.TriggerReason {
	if reason != nil {
		return reason
	}

	logger.Debug("Creating default trigger reason", "task_id", taskID)
	return &avsproto.TriggerReason{
		Type: avsproto.TriggerReason_Default,
	}
}
