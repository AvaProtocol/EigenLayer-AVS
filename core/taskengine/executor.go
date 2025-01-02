package taskengine

import (
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/AvaProtocol/ap-avs/model"
	"github.com/AvaProtocol/ap-avs/pkg/erc4337/bundler"
	"github.com/AvaProtocol/ap-avs/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"

	"github.com/AvaProtocol/ap-avs/core/apqueue"
	"github.com/AvaProtocol/ap-avs/storage"
)

func NewExecutor(db storage.Storage, logger sdklogging.Logger) *TaskExecutor {
	return &TaskExecutor{
		db:     db,
		logger: logger,
	}
}

type TaskExecutor struct {
	db     storage.Storage
	logger sdklogging.Logger
}

type QueueExecutionData struct {
	TriggerMetadata *avsproto.TriggerMetadata
	ExecutionID     string
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
	triggerMetadata := queueData.TriggerMetadata

	vm, err := NewVMWithData(task.Id, triggerMetadata, task.Nodes, task.Edges)
	if err != nil {
		return nil, err
	}

	vm.WithLogger(x.logger)
	initialTaskStatus := task.Status

	if err != nil {
		return nil, fmt.Errorf("vm failed to initialize: %w", err)
	}

	t0 := time.Now()
	task.TotalExecution += 1
	task.LastRanAt = t0.Unix()

	vm.Compile()
	runTaskErr := vm.Run()

	t1 := time.Now()

	// when MaxExecution is 0, it means unlimited run until cancel
	if task.MaxExecution > 0 && task.TotalExecution >= task.MaxExecution {
		task.SetCompleted()
	}

	// If it rached the end, flag the task completed as well
	if t1.Unix() >= task.ExpiredAt {
		task.SetCompleted()
	}

	execution := &avsproto.Execution{
		Id:              queueData.ExecutionID,
		StartAt:         t0.Unix(),
		EndAt:           t1.Unix(),
		Success:         err == nil,
		Error:           "",
		Steps:           vm.ExecutionLogs,
		TriggerMetadata: triggerMetadata,
	}

	if runTaskErr != nil {
		x.logger.Error("error executing task", "error", err, "task_id", task.Id, "triggermark", triggerMetadata)
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
		x.logger.Info("succesfully executing task", "task_id", task.Id, "triggermark", triggerMetadata)
		return execution, nil
	}
	return execution, fmt.Errorf("Error executing task %s %v", task.Id, runTaskErr)
}

type ContractProcessor struct {
	db                storage.Storage
	smartWalletConfig *config.SmartWalletConfig
	logger            sdklogging.Logger
}

func (c *ContractProcessor) GetTask(id string) (*model.Task, error) {
	var task model.Task
	item, err := c.db.GetKey([]byte(fmt.Sprintf("t:%s:%s", TaskStatusToStorageKey(avsproto.TaskStatus_Executing), id)))

	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(item, &task)
	if err != nil {
		return nil, err
	}

	return &task, nil
}

func (c *ContractProcessor) ContractWrite(job *apqueue.Job) error {
	//currentTime := time.Now()

	conn, _ := ethclient.Dial(c.smartWalletConfig.EthRpcUrl)
	//defer conn.Close()

	// Because we used the  master key to signed, the address cannot be
	// calculate from that key, but need to be passed in instead
	task, err := c.GetTask(string(job.Data))
	if err != nil {
		return err
	}

	defer func() {
		updates := map[string][]byte{}
		updates[string(TaskStorageKey(task.Id, avsproto.TaskStatus_Executing))], err = task.ToJSON()
		updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", task.Status))

		if err = c.db.BatchWrite(updates); err == nil {
			c.db.Move(
				[]byte(TaskStorageKey(task.Id, avsproto.TaskStatus_Executing)),
				[]byte(TaskStorageKey(task.Id, task.Status)),
			)
		} else {
			// TODO Gracefully handling of storage cleanup
		}
	}()

	// TODO: Implement the actualy nodes exeuction engine
	// Process entrypoint node, then from the next pointer, and flow of the node, we will follow the chain of execution
	action := task.Nodes[0]

	// TODO: move to vm.go
	if action.GetContractWrite() == nil {
		err := fmt.Errorf("invalid task action")
		//task.AppendExecution(currentTime.Unix(), "", err)
		task.SetFailed()
		return err
	}

	userOpCalldata, e := aa.PackExecute(
		common.HexToAddress(action.GetContractWrite().ContractAddress),
		big.NewInt(0),
		common.FromHex(action.GetContractWrite().CallData),
	)
	//calldata := common.FromHex("b61d27f600000000000000000000000069256ca54e6296e460dec7b29b7dcd97b81a3d55000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000044a9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a0000000000000000000000000000000000000000000000001bc16d674ec8000000000000000000000000000000000000000000000000000000000000")

	owner := common.HexToAddress(task.Owner)
	bundlerClient, e := bundler.NewBundlerClient(c.smartWalletConfig.BundlerURL)
	if e != nil {
		// TODO: maybe set retry?
		err := fmt.Errorf("internal error, bundler not available")
		//task.AppendExecution(currentTime.Unix(), "", err)
		task.SetFailed()
		return err
	}

	c.logger.Info("send task to bundler rpc", "task_id", task.Id)
	txResult, err := preset.SendUserOp(
		conn,
		bundlerClient,
		c.smartWalletConfig.ControllerPrivateKey,
		owner,
		userOpCalldata,
	)

	if txResult != "" {
		// only set complete when the task is not reaching max
		// task.SetCompleted()
		c.logger.Info("succesfully perform userop", "task_id", task.Id, "userop", txResult)
	} else {
		task.SetFailed()
		c.logger.Error("err perform userop", "task_id", task.Id, "error", err)
	}

	if err != nil || txResult == "" {
		return fmt.Errorf("UseOp failed to send; error: %v", err)
	}

	return nil
}
