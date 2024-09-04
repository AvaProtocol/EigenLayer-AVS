package taskengine

import (
	"encoding/json"
	"fmt"
	"log"
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

	"github.com/AvaProtocol/ap-avs/core/apqueue"
	"github.com/AvaProtocol/ap-avs/storage"
)

type ContractProcessor struct {
	db                storage.Storage
	smartWalletConfig *config.SmartWalletConfig
}

func NewProcessor(db storage.Storage, smartWalletConfig *config.SmartWalletConfig) *ContractProcessor {
	return &ContractProcessor{
		db:                db,
		smartWalletConfig: smartWalletConfig,
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
		log.Println("task not found", task)
		return err
	}

	//calldata := common.FromHex(task.Body.ContractExecution.CallData)
	userOpCalldata, e := aa.PackExecute(
		common.HexToAddress(task.Body.ContractExecution.ContractAddress),
		big.NewInt(0),
		common.FromHex(task.Body.ContractExecution.CallData),
	)
	//calldata := common.FromHex("b61d27f600000000000000000000000069256ca54e6296e460dec7b29b7dcd97b81a3d55000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000044a9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a0000000000000000000000000000000000000000000000001bc16d674ec8000000000000000000000000000000000000000000000000000000000000")

	owner := common.HexToAddress(task.Owner)
	bundlerClient, e := bundler.NewBundlerClient(c.smartWalletConfig.BundlerURL)
	if e != nil {
		panic(e)
	}

	log.Println("push userops to bundle", string(job.Data), job.Name, job.Type, task, c.smartWalletConfig.BundlerURL)
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
	} else {
		task.AppendExecution(currentTime.Unix(), "", err)
		task.SetFailed()
	}

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

	if err != nil || txResult == "" {
		return fmt.Errorf("UseOp failed to send; error: %v", err)
	}

	//t.Logf("UserOp submit succesfully. UserOp hash: %v", txResult)
	return nil
}
