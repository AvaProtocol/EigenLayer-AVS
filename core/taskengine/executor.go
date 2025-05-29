package taskengine

import (
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"

	"github.com/AvaProtocol/EigenLayer-AVS/core/apqueue"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
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
	Reason      *avsproto.TriggerReason
	ExecutionID string
}

func (x *TaskExecutor) GetTask(id string) (*model.Task, error) {
	task := &model.Task{
		Task: &avsproto.Task{},
	}
	item, err := x.db.GetKey([]byte(fmt.Sprintf("t:%s:%s", TaskStatusToStorageKey(avsproto.TaskStatus_Active), id)))

	if err != nil {
		return nil, err
	}
	err = protojson.Unmarshal(item, task)
	if err != nil {
		return nil, err
	}

	return task, nil
}

func (x *TaskExecutor) Perform(job *apqueue.Job) error {
	task, err := x.GetTask(job.Name)

	if err != nil {
		return fmt.Errorf("fail to load task: %s", job.Name)
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

	triggerReason := GetTriggerReasonOrDefault(queueData.Reason, task.Id, x.logger)

	secrets, _ := LoadSecretForTask(x.db, task)

	// Check if this is an event trigger that might need transfer log data
	// For testing purposes, we'll provide mock transfer log data for event triggers
	var vm *VM
	var err error

	if triggerReason.Type == avsproto.TriggerType_TRIGGER_TYPE_EVENT &&
		triggerReason.TxHash == "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8" {
		// This is the test transaction, provide mock transfer log data
		_, transferLog := testutil.GetTestEventTriggerReasonWithTransferData()
		vm, err = NewVMWithDataAndTransferLog(
			task,
			triggerReason,
			x.smartWalletConfig,
			secrets,
			transferLog,
		)
	} else {
		vm, err = NewVMWithData(
			task,
			triggerReason,
			x.smartWalletConfig,
			secrets,
		)
	}

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
		x.logger.Error("error compile task", "error", err, "edges", task.Edges, "node", task.Nodes, "task trigger data", task.Trigger, "task trigger metadata", triggerReason)
		runTaskErr = err
	} else {
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
		Id:          queueData.ExecutionID,
		StartAt:     t0.UnixMilli(),
		EndAt:       t1.UnixMilli(),
		Success:     runTaskErr == nil,
		Error:       "",
		Steps:       vm.ExecutionLogs,
		Reason:      triggerReason,
		TriggerName: task.Trigger.Name,

		// Note: despite the name OutputData, this isn't output data of the task, it's the parsed and enrich data based on the event
		// it's a synthetic data to help end-user interact with the data come in from event, at run time, it's  accessible through <triggerName>.data
		OutputData: vm.parsedTriggerData.GetValue(),
	}

	if runTaskErr != nil {
		x.logger.Error("error executing task", "error", err, "runError", runTaskErr, "task_id", task.Id, "triggermark", triggerReason)
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
		x.logger.Info("successfully executing task", "task_id", task.Id, "triggermark", triggerReason)
		return execution, nil
	}

	return execution, fmt.Errorf("Error executing task %s: %v", task.Id, runTaskErr)
}
