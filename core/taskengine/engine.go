// The core package that manage and distribute and execute task
package taskengine

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AvaProtocol/ap-avs/core/apqueue"
	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/AvaProtocol/ap-avs/model"
	"github.com/AvaProtocol/ap-avs/storage"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/allegro/bigcache/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/ethclient"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

const (
	ExecuteTask = "execute_task"
)

var (
	rpcConn *ethclient.Client
	// websocket client used for subscription
	wsEthClient *ethclient.Client
	wsRpcURL    string
	logger      sdklogging.Logger

	// a global variable that we expose to our tasks. User can use `{{name}}` to access them
	// These macro are define in our aggregator yaml config file under `macros`
	macroEnvs map[string]string
	cache     *bigcache.BigCache

	defaultSalt = big.NewInt(0)
)

// Set a global logger for task engine
func SetLogger(mylogger sdklogging.Logger) {
	logger = mylogger
}

// Set the global macro system. macros are static, immutable and available to  all tasks at runtime
func SetMacro(v map[string]string) {
	macroEnvs = v
}

func SetCache(c *bigcache.BigCache) {
	cache = c
}

// Initialize a shared rpc client instance
func SetRpc(rpcURL string) {
	if conn, err := ethclient.Dial(rpcURL); err == nil {
		rpcConn = conn
	} else {
		panic(err)
	}
}

// Initialize a shared websocket rpc client instance
func SetWsRpc(rpcURL string) {
	wsRpcURL = rpcURL
	if err := retryWsRpc(); err != nil {
		panic(err)
	}
}

func retryWsRpc() error {
	for {
		conn, err := ethclient.Dial(wsRpcURL)
		if err == nil {
			wsEthClient = conn
			return nil
		}
		logger.Errorf("cannot establish websocket client for RPC, retry in 15 seconds", "err", err)
		time.Sleep(15 * time.Second)
	}

	return nil
}

type operatorState struct {
	// list of task id that we had synced to this operator
	TaskID         map[string]bool
	MonotonicClock int64
}

// The core datastructure of the task engine
type Engine struct {
	db    storage.Storage
	queue *apqueue.Queue

	// maintain a list of active job that we have to synced to operators
	// only task triggers are sent to operator
	tasks            map[string]*model.Task
	lock             *sync.Mutex
	trackSyncedTasks map[string]*operatorState

	smartWalletConfig *config.SmartWalletConfig
	// when shutdown is true, our engine will perform the shutdown
	// pending execution will be pushed out before the shutdown completely
	// to force shutdown, one can type ctrl+c twice
	shutdown bool

	// seq is a monotonic number to keep track our task id
	seq storage.Sequence

	logger sdklogging.Logger
}

// create a new task engine using given storage, config and queueu
func New(db storage.Storage, config *config.Config, queue *apqueue.Queue, logger sdklogging.Logger) *Engine {
	e := Engine{
		db:    db,
		queue: queue,

		lock:              &sync.Mutex{},
		tasks:             make(map[string]*model.Task),
		trackSyncedTasks:  make(map[string]*operatorState),
		smartWalletConfig: config.SmartWallet,
		shutdown:          false,

		logger: logger,
	}

	SetRpc(config.SmartWallet.EthRpcUrl)
	//SetWsRpc(config.SmartWallet.EthWsUrl)

	return &e
}

func (n *Engine) Stop() {
	n.seq.Release()
	n.shutdown = true
}

func (n *Engine) MustStart() {
	var err error
	n.seq, err = n.db.GetSequence([]byte("t:seq"), 1000)
	if err != nil {
		panic(err)
	}

	// Upon booting we will get all the active tasks to sync to operator
	kvs, e := n.db.GetByPrefix(TaskByStatusStoragePrefix(avsproto.TaskStatus_Active))
	if e != nil {
		panic(e)
	}
	for _, item := range kvs {
		task := &model.Task{
			Task: &avsproto.Task{},
		}
		err := protojson.Unmarshal(item.Value, task)
		if err == nil {
			n.tasks[string(item.Key)] = task
		}
	}
}

func (n *Engine) GetSmartWallets(owner common.Address) ([]*avsproto.SmartWallet, error) {
	sender, err := aa.GetSenderAddress(rpcConn, owner, defaultSalt)
	if err != nil {
		return nil, status.Errorf(codes.Code(avsproto.Error_SmartWalletNotFoundError), SmartAccountCreationError)
	}

	// This is the default wallet with our own factory
	wallets := []*avsproto.SmartWallet{
		&avsproto.SmartWallet{
			Address: sender.String(),
			Factory: n.smartWalletConfig.FactoryAddress.String(),
			Salt:    defaultSalt.String(),
		},
	}

	items, err := n.db.GetByPrefix(WalletByOwnerPrefix(owner))

	if err != nil {
		return nil, status.Errorf(codes.Code(avsproto.Error_SmartWalletNotFoundError), SmartAccountCreationError)
	}

	// now load the customize wallet with different salt or factory that was initialed and store in our db
	for _, item := range items {
		w := &model.SmartWallet{}
		w.FromStorageData(item.Value)

		if w.Salt.Cmp(defaultSalt) == 0 {
			continue
		}

		wallets = append(wallets, &avsproto.SmartWallet{
			Address: w.Address.String(),
			Factory: w.Factory.String(),
			Salt:    w.Salt.String(),
		})
	}

	return wallets, nil
}

func (n *Engine) CreateSmartWallet(user *model.User, payload *avsproto.CreateWalletReq) (*avsproto.CreateWalletResp, error) {
	// Verify data
	// when user passing a custom factory address, we want to validate it
	if payload.FactoryAddress != "" && !common.IsHexAddress(payload.FactoryAddress) {
		return nil, status.Errorf(codes.InvalidArgument, InvalidFactoryAddressError)
	}

	salt := big.NewInt(0)
	if payload.Salt != "" {
		var ok bool
		salt, ok = math.ParseBig256(payload.Salt)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, InvalidSmartAccountSaltError)
		}
	}

	factoryAddress := n.smartWalletConfig.FactoryAddress
	if payload.FactoryAddress != "" {
		factoryAddress = common.HexToAddress(payload.FactoryAddress)

	}

	sender, err := aa.GetSenderAddressForFactory(rpcConn, user.Address, factoryAddress, salt)

	wallet := &model.SmartWallet{
		Owner:   &user.Address,
		Address: sender,
		Factory: &factoryAddress,
		Salt:    salt,
	}

	updates := map[string][]byte{}

	updates[string(WalletStorageKey(user.Address, sender.Hex()))], err = wallet.ToJSON()

	if err = n.db.BatchWrite(updates); err != nil {
		return nil, status.Errorf(codes.Code(avsproto.Error_StorageWriteError), StorageWriteError)
	}

	return &avsproto.CreateWalletResp{
		Address:        sender.Hex(),
		Salt:           salt.String(),
		FactoryAddress: factoryAddress.Hex(),
	}, nil
}

// CreateTask records submission data
func (n *Engine) CreateTask(user *model.User, taskPayload *avsproto.CreateTaskReq) (*model.Task, error) {
	var err error

	if taskPayload.SmartWalletAddress != "" {
		if !ValidWalletAddress(taskPayload.SmartWalletAddress) {
			return nil, status.Errorf(codes.InvalidArgument, InvalidSmartAccountAddressError)
		}

		if valid, _ := ValidWalletOwner(n.db, user, common.HexToAddress(taskPayload.SmartWalletAddress)); !valid {
			return nil, status.Errorf(codes.InvalidArgument, InvalidSmartAccountAddressError)
		}
	}
	task, err := model.NewTaskFromProtobuf(user, taskPayload)

	if err != nil {
		return nil, status.Errorf(codes.Code(avsproto.Error_TaskDataMissingError), err.Error())
	}

	updates := map[string][]byte{}

	updates[string(TaskStorageKey(task.Id, task.Status))], err = task.ToJSON()
	updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", avsproto.TaskStatus_Active))

	if err = n.db.BatchWrite(updates); err != nil {
		return nil, err
	}

	n.lock.Lock()
	defer n.lock.Unlock()
	n.tasks[task.Id] = task

	return task, nil
}

func (n *Engine) StreamCheckToOperator(payload *avsproto.SyncMessagesReq, srv avsproto.Node_SyncMessagesServer) error {
	ticker := time.NewTicker(5 * time.Second)
	address := payload.Address

	n.logger.Info("open channel to stream check to operator", "operator", address)
	if _, ok := n.trackSyncedTasks[address]; !ok {
		n.lock.Lock()
		n.trackSyncedTasks[address] = &operatorState{
			MonotonicClock: payload.MonotonicClock,
			TaskID:         map[string]bool{},
		}
		n.lock.Unlock()
	} else {
		// The operator has restated, but we haven't clean it state yet, reset now
		if payload.MonotonicClock > n.trackSyncedTasks[address].MonotonicClock {
			n.trackSyncedTasks[address].TaskID = map[string]bool{}
			n.trackSyncedTasks[address].MonotonicClock = payload.MonotonicClock
		}
	}

	// Reset the state if the operator disconnect
	defer func() {
		n.logger.Info("operator disconnect, cleanup state", "operator", address)
		n.trackSyncedTasks[address].TaskID = map[string]bool{}
		n.trackSyncedTasks[address].MonotonicClock = 0
	}()

	for {
		select {
		case <-ticker.C:
			if n.shutdown {
				return nil
			}

			if n.tasks == nil {
				continue
			}

			for _, task := range n.tasks {
				if _, ok := n.trackSyncedTasks[address].TaskID[task.Id]; ok {
					continue
				}

				resp := avsproto.SyncMessagesResp{
					Id: task.Id,
					Op: avsproto.MessageOp_MonitorTaskTrigger,

					TaskMetadata: &avsproto.SyncMessagesResp_TaskMetadata{
						TaskId:    task.Id,
						Remain:    task.MaxExecution,
						ExpiredAt: task.ExpiredAt,
						Trigger:   task.Trigger,
					},
				}
				n.logger.Info("stream check to operator", "taskID", task.Id, "operator", payload.Address, "resp", resp)

				if err := srv.Send(&resp); err != nil {
					// return error to cause client to establish re-connect the connection
					n.logger.Info("error sending check to operator", "taskID", task.Id, "operator", payload.Address)
					return fmt.Errorf("cannot send data back to grpc channel")
				}

				n.lock.Lock()
				n.trackSyncedTasks[address].TaskID[task.Id] = true
				n.lock.Unlock()
			}
		}
	}
}

// TODO: Merge and verify from multiple operators
func (n *Engine) AggregateChecksResult(address string, payload *avsproto.NotifyTriggersReq) error {
	if len(payload.TaskId) < 1 {
		return nil
	}

	n.lock.Lock()
	// delete(n.tasks, payload.TaskId)
	// delete(n.trackSyncedTasks[address].TaskID, payload.TaskId)
	// uncomment later

	n.logger.Info("processed aggregator check hit", "operator", address, "task_id", payload.TaskId)
	n.lock.Unlock()

	data, err := json.Marshal(payload.TriggerMarker)
	if err != nil {
		n.logger.Error("error serialize trigger to json", err)
		return err
	}

	n.queue.Enqueue(ExecuteTask, payload.TaskId, data)
	n.logger.Info("enqueue task into the queue system", "task_id", payload.TaskId)

	// if the task can still run, add it back
	return nil
}

func (n *Engine) ListTasksByUser(user *model.User, payload *avsproto.ListTasksReq) ([]*avsproto.Task, error) {
	// by default show the task from the default smart wallet, if proving we look into that wallet specifically
	owner := user.SmartAccountAddress
	if payload.SmartWalletAddress == "" {
		return nil, status.Errorf(codes.InvalidArgument, MissingSmartWalletAddressError)
	}

	if !ValidWalletAddress(payload.SmartWalletAddress) {
		return nil, status.Errorf(codes.InvalidArgument, InvalidSmartAccountAddressError)
	}

	if valid, _ := ValidWalletOwner(n.db, user, common.HexToAddress(payload.SmartWalletAddress)); !valid {
		return nil, status.Errorf(codes.InvalidArgument, InvalidSmartAccountAddressError)
	}

	smartWallet := common.HexToAddress(payload.SmartWalletAddress)
	owner = &smartWallet

	taskIDs, err := n.db.GetByPrefix(SmartWalletTaskStoragePrefix(user.Address, *owner))

	if err != nil {
		return nil, grpcstatus.Errorf(codes.Code(avsproto.Error_StorageUnavailable), StorageUnavailableError)
	}

	tasks := make([]*avsproto.Task, len(taskIDs))
	for i, kv := range taskIDs {
		status, _ := strconv.Atoi(string(kv.Value))
		taskID := string(model.TaskKeyToId(kv.Key[2:]))
		taskRawByte, err := n.db.GetKey(TaskStorageKey(taskID, avsproto.TaskStatus(status)))
		if err != nil {
			continue
		}

		task := model.NewTask()
		if err := task.FromStorageData(taskRawByte); err != nil {
			continue
		}
		task.Id = taskID

		tasks[i], _ = task.ToProtoBuf()
	}

	return tasks, nil
}

func (n *Engine) GetTaskByID(taskID string) (*model.Task, error) {
	for status, _ := range avsproto.TaskStatus_name {
		if rawTaskData, err := n.db.GetKey(TaskStorageKey(taskID, avsproto.TaskStatus(status))); err == nil {
			task := model.NewTask()
			err = task.FromStorageData(rawTaskData)

			if err == nil {
				return task, nil
			}

			return nil, grpcstatus.Errorf(codes.Code(avsproto.Error_TaskDataCorrupted), TaskStorageCorruptedError)
		}
	}

	return nil, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
}

func (n *Engine) GetTask(user *model.User, taskID string) (*model.Task, error) {
	task, err := n.GetTaskByID(taskID)
	if err != nil {
		return nil, err
	}

	if strings.ToLower(task.Owner) != strings.ToLower(user.Address.Hex()) {
		return nil, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
	}

	return task, nil
}

func (n *Engine) DeleteTaskByUser(user *model.User, taskID string) (bool, error) {
	task, err := n.GetTask(user, taskID)

	if err != nil {
		return false, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
	}

	if task.Status == avsproto.TaskStatus_Executing {
		return false, fmt.Errorf("Only non executing task can be deleted")
	}

	n.db.Delete(TaskStorageKey(task.Id, task.Status))
	n.db.Delete(TaskUserKey(task))

	return true, nil
}

func (n *Engine) CancelTaskByUser(user *model.User, taskID string) (bool, error) {
	task, err := n.GetTask(user, taskID)

	if err != nil {
		return false, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
	}

	if task.Status != avsproto.TaskStatus_Active {
		return false, fmt.Errorf("Only active task can be cancelled")
	}

	updates := map[string][]byte{}
	oldStatus := task.Status
	task.SetCanceled()
	updates[string(TaskStorageKey(task.Id, oldStatus))], err = task.ToJSON()
	updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", task.Status))

	if err = n.db.BatchWrite(updates); err == nil {
		n.db.Move(
			TaskStorageKey(task.Id, oldStatus),
			TaskStorageKey(task.Id, task.Status),
		)

		delete(n.tasks, task.Id)
	} else {
		return false, err
	}

	return true, nil
}

// A global counter for the task engine
func (n *Engine) NewSeqID() (string, error) {
	num := uint64(0)
	var err error

	defer func() {
		r := recover()
		if r != nil {
			// recover from panic and send err instead
			err = r.(error)
		}
	}()

	num, err = n.seq.Next()
	if num == 0 {
		num, err = n.seq.Next()
	}

	if err != nil {
		return "", err
	}
	return strconv.FormatInt(int64(num), 10), nil
}
