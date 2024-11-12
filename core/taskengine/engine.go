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
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/ethclient"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	grpcstatus "google.golang.org/grpc/status"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

var (
	rpcConn *ethclient.Client
	// websocket client used for subscription
	wsEthClient *ethclient.Client
	wsRpcURL    string
	logger      sdklogging.Logger
)

func SetLogger(mylogger sdklogging.Logger) {
	logger = mylogger
}

type operatorState struct {
	// list of task id that we had synced to this operator
	TaskID         map[string]bool
	MonotonicClock int64
}

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

func SetRpc(rpcURL string) {
	if conn, err := ethclient.Dial(rpcURL); err == nil {
		rpcConn = conn
	} else {
		panic(err)
	}
}

func SetWsRpc(rpcURL string) {
	wsRpcURL = rpcURL
	if err := retryWsRpc(); err != nil {
		panic(err)
	}
}

func retryWsRpc() error {
	conn, err := ethclient.Dial(wsRpcURL)
	if err == nil {
		wsEthClient = conn
		return nil
	}

	return err
}

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
		var task model.Task
		if err := json.Unmarshal(item.Value, &task); err == nil {
			n.tasks[string(item.Key)] = &task
		}
	}
}

func (n *Engine) GetSmartWallets(owner common.Address) ([]*avsproto.SmartWallet, error) {
	// This is the default wallet with our own factory
	salt := big.NewInt(0)
	sender, err := aa.GetSenderAddress(rpcConn, owner, salt)
	if err != nil {
		return nil, status.Errorf(codes.Code(avsproto.Error_SmartWalletNotFoundError), SmartAccountCreationError)
	}

	// now load the customize wallet with different salt or factory that was initialed and store in our db
	wallets := []*avsproto.SmartWallet{
		&avsproto.SmartWallet{
			Address: sender.String(),
			Factory: n.smartWalletConfig.FactoryAddress.String(),
			Salt:    salt.String(),
		},
	}

	items, err := n.db.GetByPrefix(WalletByOwnerPrefix(owner))

	if err != nil {
		return nil, status.Errorf(codes.Code(avsproto.Error_SmartWalletNotFoundError), SmartAccountCreationError)
	}

	for _, item := range items {
		w := &model.SmartWallet{}
		w.FromStorageData(item.Value)

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
		Address: sender.String(),
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
		return nil, err
	}

	updates := map[string][]byte{}

	updates[string(TaskStorageKey(task.ID, task.Status))], err = task.ToJSON()
	updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", avsproto.TaskStatus_Active))

	if err = n.db.BatchWrite(updates); err != nil {
		return nil, err
	}

	n.lock.Lock()
	defer n.lock.Unlock()
	n.tasks[task.ID] = task

	return task, nil
}

func (n *Engine) StreamCheckToOperator(payload *avsproto.SyncTasksReq, srv avsproto.Aggregator_SyncTasksServer) error {
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
				if _, ok := n.trackSyncedTasks[address].TaskID[task.ID]; ok {
					continue
				}

				n.logger.Info("stream check to operator", "taskID", task.ID, "operator", payload.Address)
				resp := avsproto.SyncTasksResp{
					Id:        task.ID,
					CheckType: "CheckTrigger",
					Trigger:   task.Trigger,
				}

				if err := srv.Send(&resp); err != nil {
					return err
				}

				n.lock.Lock()
				n.trackSyncedTasks[address].TaskID[task.ID] = true
				n.lock.Unlock()
			}
		}
	}
}

// TODO: Merge and verify from multiple operators
func (n *Engine) AggregateChecksResult(address string, ids []string) error {
	if len(ids) < 1 {
		return nil
	}

	n.logger.Debug("process aggregator check hits", "operator", address, "task_ids", ids)
	for _, id := range ids {
		n.lock.Lock()
		delete(n.tasks, id)
		delete(n.trackSyncedTasks[address].TaskID, id)
		n.logger.Info("processed aggregator check hit", "operator", address, "id", id)
		n.lock.Unlock()
	}

	// Now we will queue the job
	for _, id := range ids {
		n.logger.Debug("mark task in executing status", "task_id", id)

		if err := n.db.Move(
			[]byte(TaskStorageKey(id, avsproto.TaskStatus_Active)),
			[]byte(TaskStorageKey(id, avsproto.TaskStatus_Executing)),
		); err != nil {
			n.logger.Error("error moving the task storage from active to executing", "task", id, "error", err)
		}

		n.queue.Enqueue("contract_run", id, []byte(id))
		n.logger.Info("enqueue contract_run job", "taskid", id)
	}

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

		task := &model.Task{
			ID:    taskID,
			Owner: user.Address.Hex(),
		}
		if err := task.FromStorageData(taskRawByte); err != nil {
			continue
		}

		tasks[i], _ = task.ToProtoBuf()
	}

	return tasks, nil
}

func (n *Engine) GetTaskByID(taskID string) (*model.Task, error) {
	for status, _ := range avsproto.TaskStatus_name {
		if rawTaskData, err := n.db.GetKey(TaskStorageKey(taskID, avsproto.TaskStatus(status))); err == nil {
			task := &model.Task{
				ID: taskID,
			}
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

	n.db.Delete(TaskStorageKey(task.ID, task.Status))
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
	updates[string(TaskStorageKey(task.ID, oldStatus))], err = task.ToJSON()
	updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", task.Status))

	if err = n.db.BatchWrite(updates); err == nil {
		n.db.Move(
			TaskStorageKey(task.ID, oldStatus),
			TaskStorageKey(task.ID, task.Status),
		)

		delete(n.tasks, task.ID)
	} else {
		// TODO Gracefully handling of storage cleanup
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
