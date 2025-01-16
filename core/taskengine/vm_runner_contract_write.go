package taskengine

//
// import (
// 	"context"
// 	"encoding/json"
// 	"fmt"
// 	"strings"
// 	"time"
//
// 	"github.com/ethereum/go-ethereum"
// 	"github.com/ethereum/go-ethereum/accounts/abi"
// 	"github.com/ethereum/go-ethereum/common"
// 	"github.com/ethereum/go-ethereum/ethclient"
//
// 	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
// )
//
// type ContractWriteProcessor struct {
// 	*CommonProcessor
// 	client *ethclient.Client
// }
//
// func NewContractWriteProcessor(vm *VM, client *ethclient.Client) *ContractWriteProcessor {
// 	return &ContractWriteProcessor{
// 		client: client,
// 		CommonProcessor: &CommonProcessor{
// 			vm: vm,
// 		},
// 	}
// }
//
// func (r *ContractWriteProcessor) Execute(stepID string, node *avsproto.ContractWriteNode) (*avsproto.Execution_Step, error) {
// 	ctx := context.Background()
// 	t0 := time.Now().Unix()
// 	s := &avsproto.Execution_Step{
// 		NodeId:     stepID,
// 		Log:        "",
// 		OutputData: "",
// 		Success:    true,
// 		Error:      "",
// 		StartAt:    t0,
// 	}
//
// 	var err error
// 	defer func() {
// 		s.EndAt = time.Now().Unix()
// 		s.Success = err == nil
// 		if err != nil {
// 			s.Error = err.Error()
// 		}
// 	}()
//
// 	var log strings.Builder
//
// 	// TODO: support load pre-define ABI
// 	parsedABI, err := abi.JSON(strings.NewReader(node.ContractAbi))
// 	if err != nil {
// 		return nil, fmt.Errorf("error parse abi: %w", err)
// 	}
//
// 	contractAddress := common.HexToAddress(node.ContractAddress)
// 	calldata := common.FromHex(node.CallData)
// 	msg := ethereum.CallMsg{
// 		To:   &contractAddress,
// 		Data: calldata,
// 	}
//
// 	output, err := r.client.CallContract(ctx, msg, nil)
//
// 	if err != nil {
// 		s.Success = false
// 		s.Error = fmt.Errorf("error invoke contract method: %w", err).Error()
// 		return s, err
// 	}
//
// 	// Unpack the output
// 	result, err := parsedABI.Unpack(node.Method, output)
// 	if err != nil {
// 		s.Success = false
// 		s.Error = fmt.Errorf("error decode result: %w", err).Error()
// 		return s, err
// 	}
//
// 	log.WriteString(fmt.Sprintf("Call %s on %s at %s", node.Method, node.ContractAddress, time.Now()))
// 	s.Log = log.String()
// 	outputData, err := json.Marshal(result)
// 	s.OutputData = string(outputData)
// 	r.SetOutputVarForStep(stepID, outputData)
// 	if err != nil {
// 		s.Success = false
// 		s.Error = err.Error()
// 		return s, err
// 	}
//
// 	return s, nil
// }
//
// //type ContractProcessor struct {
// //	db                storage.Storage
// //	smartWalletConfig *config.SmartWalletConfig
// //	logger            sdklogging.Logger
// //}
// //
// //func (c *ContractProcessor) GetTask(id string) (*model.Task, error) {
// //	var task model.Task
// //	item, err := c.db.GetKey([]byte(fmt.Sprintf("t:%s:%s", TaskStatusToStorageKey(avsproto.TaskStatus_Executing), id)))
// //
// //	if err != nil {
// //		return nil, err
// //	}
// //	err = json.Unmarshal(item, &task)
// //	if err != nil {
// //		return nil, err
// //	}
// //
// //	return &task, nil
// // }
// // func (c *ContractProcessor) ContractWrite(job *apqueue.Job) error {
// // 	//currentTime := time.Now()
// //
// // 	conn, _ := ethclient.Dial(c.smartWalletConfig.EthRpcUrl)
// // 	defer conn.Close()
// //
// // 	// Because we used the  master key to signed, the address cannot be
// // 	// calculate from that key, but need to be passed in instead
// // 	task, err := c.GetTask(string(job.Data))
// // 	if err != nil {
// // 		return err
// // 	}
// //
// // 	defer func() {
// // 		updates := map[string][]byte{}
// // 		updates[string(TaskStorageKey(task.Id, avsproto.TaskStatus_Executing))], err = task.ToJSON()
// // 		updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", task.Status))
// //
// // 		if err = c.db.BatchWrite(updates); err == nil {
// // 			c.db.Move(
// // 				[]byte(TaskStorageKey(task.Id, avsproto.TaskStatus_Executing)),
// // 				[]byte(TaskStorageKey(task.Id, task.Status)),
// // 			)
// // 		} else {
// // 			// TODO Gracefully handling of storage cleanup
// // 		}
// // 	}()
// //
// // 	// TODO: Implement the actualy nodes exeuction engine
// // 	// Process entrypoint node, then from the next pointer, and flow of the node, we will follow the chain of execution
// // 	action := task.Nodes[0]
// //
// // 	// TODO: move to vm.go
// // 	if action.GetContractWrite() == nil {
// // 		err := fmt.Errorf("invalid task action")
// // 		//task.AppendExecution(currentTime.Unix(), "", err)
// // 		task.SetFailed()
// // 		return err
// // 	}
// //
// // 	userOpCalldata, e := aa.PackExecute(
// // 		common.HexToAddress(action.GetContractWrite().ContractAddress),
// // 		big.NewInt(0),
// // 		common.FromHex(action.GetContractWrite().CallData),
// // 	)
// // 	//calldata := common.FromHex("b61d27f600000000000000000000000069256ca54e6296e460dec7b29b7dcd97b81a3d55000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000044a9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a0000000000000000000000000000000000000000000000001bc16d674ec8000000000000000000000000000000000000000000000000000000000000")
// //
// // 	owner := common.HexToAddress(task.Owner)
// // 	bundlerClient, e := bundler.NewBundlerClient(c.smartWalletConfig.BundlerURL)
// // 	if e != nil {
// // 		// TODO: maybe set retry?
// // 		err := fmt.Errorf("internal error, bundler not available")
// // 		//task.AppendExecution(currentTime.Unix(), "", err)
// // 		task.SetFailed()
// // 		return err
// // 	}
// //
// // 	c.logger.Info("send task to bundler rpc", "task_id", task.Id)
// // 	txResult, err := preset.SendUserOp(
// // 		conn,
// // 		bundlerClient,
// // 		c.smartWalletConfig.ControllerPrivateKey,
// // 		owner,
// // 		userOpCalldata,
// // 	)
// //
// // 	if txResult != "" {
// // 		// only set complete when the task is not reaching max
// // 		// task.SetCompleted()
// // 		c.logger.Info("succesfully perform userop", "task_id", task.Id, "userop", txResult)
// // 	} else {
// // 		task.SetFailed()
// // 		c.logger.Error("err perform userop", "task_id", task.Id, "error", err)
// // 	}
// //
// // 	if err != nil || txResult == "" {
// // 		return fmt.Errorf("UseOp failed to send; error: %v", err)
// // 	}
// //
// // 	return nil
// // }
