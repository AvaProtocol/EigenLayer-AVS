package taskengine

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"

	"github.com/AvaProtocol/EigenLayer-AVS/core/apqueue"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func NewExecutor(config *config.SmartWalletConfig, db storage.Storage, logger sdklogging.Logger) *TaskExecutor {
	return &TaskExecutor{
		db:                db,
		logger:            logger,
		smartWalletConfig: config,
	}
}

type TaskExecutor struct {
	db                storage.Storage
	logger            sdklogging.Logger
	smartWalletConfig *config.SmartWalletConfig
}

type QueueExecutionData struct {
	TriggerType   avsproto.TriggerType
	TriggerOutput interface{} // Will hold the specific trigger output (BlockTrigger.Output, etc.)
	ExecutionID   string
}

func (x *TaskExecutor) GetTask(id string) (*model.Task, error) {
	task := &model.Task{
		Task: &avsproto.Task{},
	}
	storageKey := []byte(fmt.Sprintf("t:%s:%s", TaskStatusToStorageKey(avsproto.TaskStatus_Active), id))
	item, err := x.db.GetKey(storageKey)

	if err != nil {
		return nil, fmt.Errorf("storage access failed for key 't:%s:%s': %w", TaskStatusToStorageKey(avsproto.TaskStatus_Active), id, err)
	}
	err = protojson.Unmarshal(item, task)
	if err != nil {
		return nil, fmt.Errorf("failed to parse task data from storage (data may be corrupted): %w", err)
	}

	return task, nil
}

func (x *TaskExecutor) Perform(job *apqueue.Job) error {
	task, err := x.GetTask(job.Name)

	if err != nil {
		// Provide more specific error information
		if strings.Contains(err.Error(), "key not found") || strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("task not found in storage: %s (task may have been deleted or storage key is incorrect)", job.Name)
		} else if strings.Contains(err.Error(), "unmarshal") || strings.Contains(err.Error(), "json") {
			return fmt.Errorf("task data corruption in storage: %s (stored data is invalid JSON)", job.Name)
		} else {
			return fmt.Errorf("storage error loading task %s: %v", job.Name, err)
		}
	}

	queueData := &QueueExecutionData{}
	// A task executor data is the trigger mark
	// ref: AggregateChecksResult
	err = json.Unmarshal(job.Data, queueData)
	if err != nil {
		return fmt.Errorf("error decode job payload when executing task: %s with job id %d", task.Id, job.ID)
	}

	// Execute the task logic
	_, runErr := x.RunTask(task, queueData)

	if runErr == nil {
		// Task logic executed successfully. Clean up the TaskTriggerKey for this async execution.
		if queueData != nil && queueData.ExecutionID != "" { // Assumes `ExecutionID` is always set for queued jobs. Verify this assumption if the logic changes.
			triggerKeyToClean := TaskTriggerKey(task, queueData.ExecutionID)
			if delErr := x.db.Delete(triggerKeyToClean); delErr != nil {
				x.logger.Error("Perform: Failed to delete TaskTriggerKey after successful async execution",
					"key", string(triggerKeyToClean), "task_id", task.Id, "execution_id", queueData.ExecutionID, "error", delErr)
			} else {
				// Successfully deleted, no need for a verbose log here unless for specific debug scenarios
				// x.logger.Info("Perform: Successfully deleted TaskTriggerKey after async execution",
				// 	"key", string(triggerKeyToClean), "task_id", task.Id, "execution_id", queueData.ExecutionID)
			}
		}
		return nil // Job processed successfully
	}

	// If runErr is not nil, the task logic failed.
	// x.logger.Error("Perform: Task execution failed, not deleting TaskTriggerKey.", "task_id", task.Id, "execution_id", queueData.ExecutionID, "error", runErr)
	return runErr // Propagate the error from task execution
}

func (x *TaskExecutor) RunTask(task *model.Task, queueData *QueueExecutionData) (*avsproto.Execution, error) {
	if queueData == nil || queueData.ExecutionID == "" {
		return nil, fmt.Errorf("internal error: invalid execution id")
	}

	// Convert queue data back to the format expected by the VM
	triggerReason := GetTriggerReasonOrDefault(queueData, task.Id, x.logger)

	secrets, _ := LoadSecretForTask(x.db, task)

	// Create VM with trigger reason data
	vm, err := NewVMWithData(task, triggerReason, x.smartWalletConfig, secrets)

	if err != nil {
		return nil, err
	}

	vm.WithLogger(x.logger).WithDb(x.db)
	initialTaskStatus := task.Status

	if err != nil {
		return nil, fmt.Errorf("vm failed to initialize: %w", err)
	}

	t0 := time.Now()
	task.ExecutionCount += 1
	task.LastRanAt = t0.UnixMilli()

	var runTaskErr error = nil
	if err = vm.Compile(); err != nil {
		x.logger.Error("error compile task", "error", err, "edges", task.Edges, "node", task.Nodes, "task trigger data", task.Trigger, "task trigger metadata", queueData)
		runTaskErr = err
	} else {
		// Create and add a trigger execution step before running nodes (matching SimulateTask behavior)
		triggerStep := &avsproto.Execution_Step{
			Id:      task.Trigger.Id,
			Success: true,
			Error:   "",
			StartAt: t0.UnixMilli(),
			EndAt:   t0.UnixMilli(),
			Log:     fmt.Sprintf("Executed trigger: %s", task.Trigger.Name),
			Inputs:  []string{}, // No inputs for trigger steps
			Type:    queueData.TriggerType.String(),
			Name:    task.Trigger.Name,
		}

		// Set trigger output data in the step based on trigger type
		switch queueData.TriggerType {
		case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
			if output, ok := queueData.TriggerOutput.(*avsproto.ManualTrigger_Output); ok {
				triggerStep.OutputData = &avsproto.Execution_Step_ManualTrigger{ManualTrigger: output}
			}
		case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
			if output, ok := queueData.TriggerOutput.(*avsproto.FixedTimeTrigger_Output); ok {
				triggerStep.OutputData = &avsproto.Execution_Step_FixedTimeTrigger{FixedTimeTrigger: output}
			}
		case avsproto.TriggerType_TRIGGER_TYPE_CRON:
			if output, ok := queueData.TriggerOutput.(*avsproto.CronTrigger_Output); ok {
				triggerStep.OutputData = &avsproto.Execution_Step_CronTrigger{CronTrigger: output}
			}
		case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
			if output, ok := queueData.TriggerOutput.(*avsproto.BlockTrigger_Output); ok {
				triggerStep.OutputData = &avsproto.Execution_Step_BlockTrigger{BlockTrigger: output}
			}
		case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
			if output, ok := queueData.TriggerOutput.(*avsproto.EventTrigger_Output); ok {
				triggerStep.OutputData = &avsproto.Execution_Step_EventTrigger{EventTrigger: output}
			}
		}

		// Add trigger step to execution logs before running nodes
		vm.ExecutionLogs = append(vm.ExecutionLogs, triggerStep)

		runTaskErr = vm.Run()
	}

	t1 := time.Now()

	// when MaxExecution is 0, it means unlimited run until cancel
	if task.MaxExecution > 0 && task.ExecutionCount >= task.MaxExecution {
		task.SetCompleted()
	}

	if task.ExpiredAt > 0 && t1.UnixMilli() >= task.ExpiredAt {
		task.SetCompleted()
	}

	execution := &avsproto.Execution{
		Id:      queueData.ExecutionID,
		StartAt: t0.UnixMilli(),
		EndAt:   t1.UnixMilli(),
		Success: runTaskErr == nil,
		Error:   "",
		Steps:   vm.ExecutionLogs,
	}

	if runTaskErr != nil {
		x.logger.Error("error executing task", "error", err, "runError", runTaskErr, "task_id", task.Id, "triggermark", queueData)
		execution.Error = runTaskErr.Error()
	}

	// batch update storage for task + execution log
	updates := map[string][]byte{}
	updates[string(TaskStorageKey(task.Id, task.Status))], err = task.ToJSON()
	updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", task.Status))

	// update execution log
	executionByte, err := protojson.Marshal(execution)
	if err == nil {
		updates[string(TaskExecutionKey(task, execution.Id))] = executionByte
	}

	if err = x.db.BatchWrite(updates); err != nil {
		// TODO Monitor to see how often this happen
		x.logger.Errorf("error updating task status. %w", err, "task_id", task.Id)
	}

	// whenever a task change its status, we moved it, therefore we will need to clean up the old storage
	if task.Status != initialTaskStatus {
		if err = x.db.Delete(TaskStorageKey(task.Id, initialTaskStatus)); err != nil {
			x.logger.Errorf("error updating task status. %w", err, "task_id", task.Id)
		}
	}

	if runTaskErr == nil {
		x.logger.Info("successfully executing task", "task_id", task.Id, "triggermark", queueData)
		return execution, nil
	}

	return execution, fmt.Errorf("Error executing task %s: %v", task.Id, runTaskErr)
}
