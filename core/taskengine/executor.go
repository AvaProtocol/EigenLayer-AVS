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

	_, err = x.RunTask(task, queueData)
	return err
}

func (x *TaskExecutor) RunTask(task *model.Task, queueData *QueueExecutionData) (*avsproto.Execution, error) {
	defer func() {
		// Delete the task trigger queue when we're done, the execution log is available in main task storage at this point
		x.db.GetKey(TaskTriggerKey(task, queueData.ExecutionID))
	}()

	if queueData == nil || queueData.ExecutionID == "" {
		return nil, fmt.Errorf("internal error: invalid execution id")
	}

	triggerReason := GetTriggerReasonOrDefault(queueData.Reason, task.Id, x.logger)

	secrets, _ := LoadSecretForTask(x.db, task)
	vm, err := NewVMWithData(
		task,
		triggerReason,
		x.smartWalletConfig,
		secrets,
	)

	if err != nil {
		return nil, err
	}

	vm.WithLogger(x.logger).WithDb(x.db)
	initialTaskStatus := task.Status

	if err != nil {
		return nil, fmt.Errorf("vm failed to initialize: %w", err)
	}

	t0 := time.Now()
	task.TotalExecution += 1
	task.LastRanAt = t0.UnixMilli()

	var runTaskErr error = nil
	if err = vm.Compile(); err != nil {
		x.logger.Error("error compile task", "error", err, "edges", task.Edges, "node", task.Nodes, "task trigger data", task.Trigger, "task trigger metadata", triggerMetadata)
		runTaskErr = err
	} else {
		runTaskErr = vm.Run()
	}

	t1 := time.Now()

	// when MaxExecution is 0, it means unlimited run until cancel
	if task.MaxExecution > 0 && task.TotalExecution >= task.MaxExecution {
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
		x.logger.Info("succesfully executing task", "task_id", task.Id, "triggermark", triggerReason)
		return execution, nil
	}
	return execution, fmt.Errorf("Error executing task %s: %v", task.Id, runTaskErr)
}
