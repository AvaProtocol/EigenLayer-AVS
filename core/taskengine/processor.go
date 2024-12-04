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

func (x *TaskExecutor) GetTask(id string) (*model.Task, error) {
	task := &model.Task{
		Task: &avsproto.Task{},
	}
	item, err := x.db.GetKey([]byte(fmt.Sprintf("t:%s:%s", TaskStatusToStorageKey(avsproto.TaskStatus_Executing), id)))

	if err != nil {
		// Fallback, TODO: track this and see how often we fall to this
		item, err = x.db.GetKey([]byte(fmt.Sprintf("t:%s:%s", TaskStatusToStorageKey(avsproto.TaskStatus_Active), id)))
		if err != nil {
			return nil, err
		}
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

	// A task executor data is the trigger mark
	// ref: AggregateChecksResult
	triggerMark := &avsproto.TriggerMark{}
	err = json.Unmarshal(job.Data, triggerMark)
	if err != nil {
		return fmt.Errorf("error decode job payload when executing task: %s with job id %d", task.Id, job.ID)
	}

	vm, err := NewVMWithData(job.Name, triggerMark, task.Nodes, task.Edges)

	if err != nil {
		return fmt.Errorf("vm failed to initialize: %w", err)
	}

	vm.Compile()
	err = vm.Run()

	defer func() {
		updates := map[string][]byte{}
		updates[string(TaskStorageKey(task.Id, avsproto.TaskStatus_Executing))], err = task.ToJSON()
		updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", task.Status))

		if err = x.db.BatchWrite(updates); err == nil {
			x.db.Move(
				[]byte(TaskStorageKey(task.Id, avsproto.TaskStatus_Executing)),
				[]byte(TaskStorageKey(task.Id, task.Status)),
			)
		} else {
			// TODO Gracefully handling of storage cleanup
		}
	}()

	currentTime := time.Now()
	if err == nil {
		x.logger.Info("succesfully executing task", "taskid", job.Name, "triggermark", string(job.Data))
		task.AppendExecution(currentTime.Unix(), vm.ExecutionLogs, nil)
		task.SetCompleted()
	} else {
		x.logger.Error("error executing task", "taskid", job.Name, "triggermark", string(job.Data), err)
		task.AppendExecution(currentTime.Unix(), vm.ExecutionLogs, err)
		task.SetFailed()
		return fmt.Errorf("Error executing program: %v", err)
	}

	return nil
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

	c.logger.Info("send task to bundler rpc", "taskid", task.Id)
	txResult, err := preset.SendUserOp(
		conn,
		bundlerClient,
		c.smartWalletConfig.ControllerPrivateKey,
		owner,
		userOpCalldata,
	)

	if txResult != "" {
		//task.AppendExecution(currentTime.Unix(), txResult, nil)
		task.SetCompleted()
		c.logger.Info("succesfully perform userop", "taskid", task.Id, "userop", txResult)
	} else {
		//task.AppendExecution(currentTime.Unix(), "", err)
		task.SetFailed()
		c.logger.Error("err perform userop", "taskid", task.Id, "error", err)
	}

	if err != nil || txResult == "" {
		return fmt.Errorf("UseOp failed to send; error: %v", err)
	}

	return nil
}
