package taskengine

import (
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

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

type ContractProcessor struct {
	db                storage.Storage
	smartWalletConfig *config.SmartWalletConfig
	logger            sdklogging.Logger
}

func NewProcessor(db storage.Storage, smartWalletConfig *config.SmartWalletConfig, logger sdklogging.Logger) *ContractProcessor {
	return &ContractProcessor{
		db:                db,
		smartWalletConfig: smartWalletConfig,
		logger:            logger,
	}
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

func (c *ContractProcessor) Perform(job *apqueue.Job) error {
	currentTime := time.Now()

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
		updates[TaskStorageKey(task.ID, avsproto.TaskStatus_Executing)], err = task.ToJSON()
		updates[TaskUserKey(task)] = []byte(fmt.Sprintf("%d", task.Status))

		if err = c.db.BatchWrite(updates); err == nil {
			c.db.Move(
				[]byte(TaskStorageKey(task.ID, avsproto.TaskStatus_Executing)),
				[]byte(TaskStorageKey(task.ID, task.Status)),
			)
		} else {
			// TODO Gracefully handling of storage cleanup
		}
	}()

	// TODO: Implement the actualy nodes exeuction engine
	// Process entrypoint node, then from the next pointer, and flow of the node, we will follow the chain of execution
	action := task.Nodes[0]

	if action.ContractExecution == nil {
		err := fmt.Errorf("invalid task action")
		task.AppendExecution(currentTime.Unix(), "", err)
		task.SetFailed()
		return err
	}

	userOpCalldata, e := aa.PackExecute(
		common.HexToAddress(action.ContractExecution.ContractAddress),
		big.NewInt(0),
		common.FromHex(action.ContractExecution.CallData),
	)
	//calldata := common.FromHex("b61d27f600000000000000000000000069256ca54e6296e460dec7b29b7dcd97b81a3d55000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000044a9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a0000000000000000000000000000000000000000000000001bc16d674ec8000000000000000000000000000000000000000000000000000000000000")

	owner := common.HexToAddress(task.Owner)
	bundlerClient, e := bundler.NewBundlerClient(c.smartWalletConfig.BundlerURL)
	if e != nil {
		// TODO: maybe set retry?
		err := fmt.Errorf("internal error, bundler not available")
		task.AppendExecution(currentTime.Unix(), "", err)
		task.SetFailed()
		return err
	}

	c.logger.Info("send task to bundler rpc", "taskid", task.ID)
	txResult, err := preset.SendUserOp(
		conn,
		bundlerClient,
		c.smartWalletConfig.ControllerPrivateKey,
		owner,
		userOpCalldata,
	)

	if txResult != "" {
		task.AppendExecution(currentTime.Unix(), txResult, nil)
		task.SetCompleted()
		c.logger.Info("succesfully perform userop", "taskid", task.ID, "userop", txResult)
	} else {
		task.AppendExecution(currentTime.Unix(), "", err)
		task.SetFailed()
		c.logger.Error("err perform userop", "taskid", task.ID, "error", err)
	}

	if err != nil || txResult == "" {
		return fmt.Errorf("UseOp failed to send; error: %v", err)
	}

	return nil
}
