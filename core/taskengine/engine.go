// The core package that manage and distribute and execute task
package taskengine

import (
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/apqueue"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/allegro/bigcache/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/oklog/ulid/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

const (
	JobTypeExecuteTask  = "execute_task"
	DefaultItemPerPage  = 50
	MaxSecretNameLength = 255

	EvmErc20TransferTopic0 = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
)

var (
	rpcConn *ethclient.Client
	// websocket client used for subscription
	wsEthClient *ethclient.Client
	wsRpcURL    string
	logger      sdklogging.Logger

	// a global variable that we expose to our tasks. User can use `{{name}}` to access them
	// These macro are define in our aggregator yaml config file under `macros`
	macroVars    map[string]string
	macroSecrets map[string]string
	cache        *bigcache.BigCache

	defaultSalt = big.NewInt(0)
)

// Set a global logger for task engine
func SetLogger(mylogger sdklogging.Logger) {
	logger = mylogger
}

// Set the global macro system. macros are static, immutable and available to  all tasks at runtime
func SetMacroVars(v map[string]string) {
	macroVars = v
}

func SetMacroSecrets(v map[string]string) {
	macroSecrets = v
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
	aa.SetFactoryAddress(config.SmartWallet.FactoryAddress)
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

func (n *Engine) GetSmartWallets(owner common.Address, payload *avsproto.ListWalletReq) ([]*avsproto.SmartWallet, error) {
	sender, err := aa.GetSenderAddress(rpcConn, owner, defaultSalt)
	if err != nil {
		return nil, status.Errorf(codes.Code(avsproto.Error_SmartWalletNotFoundError), SmartAccountCreationError)
	}

	wallets := []*avsproto.SmartWallet{}

	if payload == nil || payload.FactoryAddress == "" || strings.EqualFold(payload.FactoryAddress, n.smartWalletConfig.FactoryAddress.Hex()) {
		// This is the default wallet with our own factory
		wallets = append(wallets, &avsproto.SmartWallet{
			Address: sender.String(),
			Factory: n.smartWalletConfig.FactoryAddress.String(),
			Salt:    defaultSalt.String(),
		})
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

		if payload != nil && payload.FactoryAddress != "" && !strings.EqualFold(w.Factory.String(), payload.FactoryAddress) {
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

func (n *Engine) GetWallet(user *model.User, payload *avsproto.GetWalletReq) (*avsproto.GetWalletResp, error) {
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
	if err != nil || sender.Hex() == "0x0000000000000000000000000000000000000000" {
		return nil, status.Errorf(codes.InvalidArgument, InvalidFactoryAddressError)
	}
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

	statSvc := NewStatService(n.db)
	stat, _ := statSvc.GetTaskCount(wallet)

	return &avsproto.GetWalletResp{
		Address:        sender.Hex(),
		Salt:           salt.String(),
		FactoryAddress: factoryAddress.Hex(),

		TotalTaskCount:     stat.Total,
		ActiveTaskCount:    stat.Active,
		CompletedTaskCount: stat.Completed,
		FailedTaskCount:    stat.Failed,
		CanceledTaskCount:  stat.Canceled,
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
		return nil, grpcstatus.Errorf(codes.InvalidArgument, "%s", err.Error())
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

			if !n.CanStreamCheck(address) {
				// This isn't a consensus approval. It's a feature flag we control server side whether to stream data to the operator or not.
				// TODO: Remove this flag when we measure performance impact on all operator
				n.logger.Info("operator has not been approved to process task", address)
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
				n.logger.Info("stream check to operator", "task_id", task.Id, "operator", payload.Address, "resp", resp)

				if err := srv.Send(&resp); err != nil {
					// return error to cause client to establish re-connect the connection
					n.logger.Info("error sending check to operator", "task_id", task.Id, "operator", payload.Address)
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

	queueTaskData := QueueExecutionData{
		Reason:      payload.Reason,
		ExecutionID: ulid.Make().String(),
	}

	data, err := json.Marshal(queueTaskData)
	if err != nil {
		n.logger.Error("error serialize trigger to json", err)
		return err
	}

	n.queue.Enqueue(JobTypeExecuteTask, payload.TaskId, data)
	n.logger.Info("enqueue task into the queue system", "task_id", payload.TaskId)

	// if the task can still run, add it back
	return nil
}

func (n *Engine) ListTasksByUser(user *model.User, payload *avsproto.ListTasksReq) (*avsproto.ListTasksResp, error) {
	if len(payload.SmartWalletAddress) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, MissingSmartWalletAddressError)
	}

	prefixes := make([]string, len(payload.SmartWalletAddress))
	for i, smartWalletAddress := range payload.SmartWalletAddress {
		if smartWalletAddress == "" {
			return nil, status.Errorf(codes.InvalidArgument, MissingSmartWalletAddressError)
		}

		if !ValidWalletAddress(smartWalletAddress) {
			return nil, status.Errorf(codes.InvalidArgument, InvalidSmartAccountAddressError)
		}

		if valid, _ := ValidWalletOwner(n.db, user, common.HexToAddress(smartWalletAddress)); !valid {
			return nil, status.Errorf(codes.InvalidArgument, InvalidSmartAccountAddressError)
		}

		smartWallet := common.HexToAddress(smartWalletAddress)
		prefixes[i] = string(SmartWalletTaskStoragePrefix(user.Address, smartWallet))
	}

	//taskIDs, err := n.db.GetByPrefix(SmartWalletTaskStoragePrefix(user.Address, *owner))
	taskKeys, err := n.db.ListKeysMulti(prefixes)
	if err != nil {
		return nil, grpcstatus.Errorf(codes.Code(avsproto.Error_StorageUnavailable), StorageUnavailableError)
	}

	// second, do the sort, this is key sorted by ordering of ther insertion
	slices.SortFunc(taskKeys, func(a, b string) int {
		id1 := ulid.MustParse(string(model.TaskKeyToId([]byte(a[2:]))))
		id2 := ulid.MustParse(string(model.TaskKeyToId([]byte(b[2:]))))
		return id1.Compare(id2)
	})

	taskResp := &avsproto.ListTasksResp{
		Items:  []*avsproto.ListTasksResp_Item{},
		Cursor: "",
	}

	total := 0
	cursor, err := CursorFromString(payload.Cursor)
	itemPerPage := int(payload.ItemPerPage)
	if itemPerPage < 0 {
	}
	if itemPerPage == 0 {
		itemPerPage = DefaultItemPerPage
	}

	visited := 0
	for i := len(taskKeys) - 1; i >= 0; i-- {
		key := taskKeys[i]
		visited = i
		taskID := string(model.TaskKeyToId(([]byte(key[2:]))))
		statusValue, err := n.db.GetKey([]byte(key))
		if err != nil {
			return nil, grpcstatus.Errorf(codes.Code(avsproto.Error_StorageUnavailable), StorageUnavailableError)
		}
		status, _ := strconv.Atoi(string(statusValue))

		taskIDUlid := model.UlidFromTaskId(taskID)
		if !cursor.IsZero() && cursor.LessThanOrEqualUlid(taskIDUlid) {
			continue
		}

		taskRawByte, err := n.db.GetKey(TaskStorageKey(taskID, avsproto.TaskStatus(status)))
		if err != nil {
			continue
		}

		task := model.NewTask()
		if err := task.FromStorageData(taskRawByte); err != nil {
			continue
		}
		task.Id = taskID

		if t, err := task.ToProtoBuf(); err == nil {
			taskResp.Items = append(taskResp.Items, &avsproto.ListTasksResp_Item{
				Id:                 t.Id,
				Owner:              t.Owner,
				SmartWalletAddress: t.SmartWalletAddress,
				StartAt:            t.StartAt,
				ExpiredAt:          t.ExpiredAt,
				Name:               t.Name,
				CompletedAt:        t.CompletedAt,
				MaxExecution:       t.MaxExecution,
				TotalExecution:     t.TotalExecution,
				LastRanAt:          t.LastRanAt,
				Status:             t.Status,
				Trigger:            t.Trigger,
			})
			total += 1
		}

		if total >= itemPerPage {
			break
		}
	}

	taskResp.HasMore = visited > 0
	if taskResp.HasMore {
		taskResp.Cursor = NewCursor(CursorDirectionNext, taskResp.Items[total-1].Id).String()
	}

	return taskResp, nil
}

func (n *Engine) GetTaskByID(taskID string) (*model.Task, error) {
	for status := range avsproto.TaskStatus_name {
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

	if !task.OwnedBy(user.Address) {
		return nil, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
	}

	return task, nil
}

func (n *Engine) TriggerTask(user *model.User, payload *avsproto.UserTriggerTaskReq) (*avsproto.UserTriggerTaskResp, error) {
	if !ValidateTaskId(payload.TaskId) {
		return nil, status.Errorf(codes.InvalidArgument, InvalidTaskIdFormat)
	}

	n.logger.Info("processed manually trigger", "user", user.Address, "task_id", payload.TaskId)

	task, err := n.GetTaskByID(payload.TaskId)
	if err != nil {
		n.logger.Error("task not found", "user", user.Address, "task_id", payload.TaskId)
		return nil, err
	}

	if !task.IsRunable() {
		return nil, grpcstatus.Errorf(codes.FailedPrecondition, TaskIsNotRunable)
	}
	if !task.OwnedBy(user.Address) {
		// only the owner of a task can trigger it
		n.logger.Error("task not own by user", "owner", user.Address, "task_id", payload.TaskId)
		return nil, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
	}

	queueTaskData := QueueExecutionData{
		Reason:      payload.Reason,
		ExecutionID: ulid.Make().String(),
	}

	if payload.IsBlocking {
		// Run the task inline, by pass the queue system
		executor := NewExecutor(n.smartWalletConfig, n.db, n.logger)
		execution, err := executor.RunTask(task, &queueTaskData)
		if err == nil {
			return &avsproto.UserTriggerTaskResp{
				ExecutionId: execution.Id,
			}, nil
		}

		return nil, grpcstatus.Errorf(codes.Code(avsproto.Error_TaskTriggerError), "Error trigger task: %v", err)
	}

	data, err := json.Marshal(queueTaskData)
	if err != nil {
		n.logger.Error("error serialize trigger to json", err)
		return nil, grpcstatus.Errorf(codes.InvalidArgument, "%s", err.Error())
	}

	jid, err := n.queue.Enqueue(JobTypeExecuteTask, payload.TaskId, data)
	if err != nil {
		n.logger.Error("error enqueue job %s %s %w", payload.TaskId, string(data), err)
		return nil, grpcstatus.Errorf(codes.Code(avsproto.Error_StorageUnavailable), StorageQueueUnavailableError)
	}

	n.setExecutionStatusQueue(task, queueTaskData.ExecutionID)
	n.logger.Info("enqueue task into the queue system", "task_id", payload.TaskId, "jid", jid, "execution_id", queueTaskData.ExecutionID)
	return &avsproto.UserTriggerTaskResp{
		ExecutionId: queueTaskData.ExecutionID,
	}, nil
}

// List Execution for a given task id
func (n *Engine) ListExecutions(user *model.User, payload *avsproto.ListExecutionsReq) (*avsproto.ListExecutionsResp, error) {
	// Validate all tasks own by the caller, if there are any tasks won't be owned by caller, we return permission error
	tasks := make(map[string]*model.Task)

	for _, id := range payload.TaskIds {
		task, err := n.GetTaskByID(id)
		if err != nil {
			return nil, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
		}

		if !task.OwnedBy(user.Address) {
			return nil, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
		}
		tasks[id] = task
	}

	prefixes := make([]string, len(payload.TaskIds))
	for _, id := range payload.TaskIds {
		prefixes = append(prefixes, string(TaskExecutionPrefix(id)))
	}

	executionKeys, err := n.db.ListKeysMulti(prefixes)

	// second, do the sort, this is key sorted by ordering of ther insertion
	slices.SortFunc(executionKeys, func(a, b string) int {
		id1 := ulid.MustParse(string(ExecutionIdFromStorageKey([]byte(a))))
		id2 := ulid.MustParse(string(ExecutionIdFromStorageKey([]byte(b))))
		return id1.Compare(id2)
	})

	if err != nil {
		return nil, grpcstatus.Errorf(codes.Code(avsproto.Error_StorageUnavailable), StorageUnavailableError)
	}

	executioResp := &avsproto.ListExecutionsResp{
		Items:  []*avsproto.Execution{},
		Cursor: "",
	}

	total := 0
	cursor, err := CursorFromString(payload.Cursor)

	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, InvalidCursor)
	}

	itemPerPage := int(payload.ItemPerPage)
	if itemPerPage < 0 {
		return nil, status.Errorf(codes.InvalidArgument, InvalidPaginationParam)
	}

	if itemPerPage == 0 {
		itemPerPage = DefaultItemPerPage
	}
	visited := 0
	for i := len(executionKeys) - 1; i >= 0; i-- {
		key := executionKeys[i]
		visited = i

		executionUlid := ulid.MustParse(ExecutionIdFromStorageKey([]byte(key)))
		if !cursor.IsZero() && cursor.LessThanOrEqualUlid(executionUlid) {
			continue
		}

		executionValue, err := n.db.GetKey([]byte(key))
		if err != nil {
			continue
		}

		exec := avsproto.Execution{}
		if err := protojson.Unmarshal(executionValue, &exec); err == nil {
			taskId := TaskIdFromExecutionStorageKey([]byte(key))
			task := tasks[taskId]
			if task == nil {
				// This cannot be happen, if it had corrupted storage
				panic("program corrupted")
			}
			switch task.GetTrigger().GetTriggerType().(type) {
			case *avsproto.TaskTrigger_Manual:
				exec.Reason.Type = avsproto.TriggerReason_Manual
			case *avsproto.TaskTrigger_FixedTime:
				exec.Reason.Type = avsproto.TriggerReason_FixedTime
			case *avsproto.TaskTrigger_Cron:
				exec.Reason.Type = avsproto.TriggerReason_Cron
			case *avsproto.TaskTrigger_Block:
				exec.Reason.Type = avsproto.TriggerReason_Block
			case *avsproto.TaskTrigger_Event:
				exec.Reason.Type = avsproto.TriggerReason_Event
			}
			executioResp.Items = append(executioResp.Items, &exec)
			total += 1
		}
		if total >= itemPerPage {
			break
		}
	}

	executioResp.HasMore = visited > 0
	if executioResp.HasMore {
		executioResp.Cursor = NewCursor(CursorDirectionNext, executioResp.Items[total-1].Id).String()
	}
	return executioResp, nil
}

func (n *Engine) setExecutionStatusQueue(task *model.Task, executionID string) error {
	status := strconv.Itoa(int(avsproto.ExecutionStatus_Queued))
	return n.db.Set(TaskTriggerKey(task, executionID), []byte(status))
}

func (n *Engine) getExecutonStatusFromQueue(task *model.Task, executionID string) (*avsproto.ExecutionStatus, error) {
	status, err := n.db.GetKey(TaskTriggerKey(task, executionID))
	if err != nil {
		return nil, err
	}

	value, err := strconv.Atoi(string(status))
	if err != nil {
		return nil, err
	}
	statusValue := avsproto.ExecutionStatus(value)
	return &statusValue, nil
}

// Get xecution for a given task id and execution id
func (n *Engine) GetExecution(user *model.User, payload *avsproto.ExecutionReq) (*avsproto.Execution, error) {
	// Validate all tasks own by the caller, if there are any tasks won't be owned by caller, we return permission error
	task, err := n.GetTaskByID(payload.TaskId)

	if err != nil {
		return nil, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
	}

	if !task.OwnedBy(user.Address) {
		return nil, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
	}

	executionValue, err := n.db.GetKey(TaskExecutionKey(task, payload.ExecutionId))
	if err != nil {
		return nil, grpcstatus.Errorf(codes.NotFound, ExecutionNotFoundError)
	}
	exec := avsproto.Execution{}
	if err := protojson.Unmarshal(executionValue, &exec); err == nil {
		switch task.GetTrigger().GetTriggerType().(type) {
		case *avsproto.TaskTrigger_Manual:
			exec.Reason.Type = avsproto.TriggerReason_Manual
		case *avsproto.TaskTrigger_FixedTime:
			exec.Reason.Type = avsproto.TriggerReason_FixedTime
		case *avsproto.TaskTrigger_Cron:
			exec.Reason.Type = avsproto.TriggerReason_Cron
		case *avsproto.TaskTrigger_Block:
			exec.Reason.Type = avsproto.TriggerReason_Block
		case *avsproto.TaskTrigger_Event:
			exec.Reason.Type = avsproto.TriggerReason_Event
		}
	}

	return &exec, nil
}

func (n *Engine) GetExecutionStatus(user *model.User, payload *avsproto.ExecutionReq) (*avsproto.ExecutionStatusResp, error) {
	task, err := n.GetTaskByID(payload.TaskId)

	if err != nil {
		return nil, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
	}

	if !task.OwnedBy(user.Address) {
		return nil, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
	}

	// First look into execution first
	if _, err = n.db.GetKey(TaskExecutionKey(task, payload.ExecutionId)); err != nil {
		// When execution not found, it could be in pending status, we will check that storage
		if status, err := n.getExecutonStatusFromQueue(task, payload.ExecutionId); err == nil {
			return &avsproto.ExecutionStatusResp{
				Status: *status,
			}, nil
		}
		return nil, fmt.Errorf("invalid ")
	}

	// if the key existed, the execution has finished, no need to decode the whole storage, we just return the status in this call
	return &avsproto.ExecutionStatusResp{
		Status: avsproto.ExecutionStatus_Finished,
	}, nil
}

func (n *Engine) GetExecutionCount(user *model.User, payload *avsproto.GetExecutionCountReq) (*avsproto.GetExecutionCountResp, error) {
	workflowIds := payload.WorkflowIds

	total := int64(0)
	var err error

	if len(workflowIds) == 0 {
		workflowIds = []string{}
		// count all executions of the owner by finding all their task idds
		taskIds, err := n.db.GetKeyHasPrefix(UserTaskStoragePrefix(user.Address))

		if err != nil {
			return nil, grpcstatus.Errorf(codes.Internal, "Internal error counting execution")
		}
		for _, id := range taskIds {
			taskId := TaskIdFromTaskStatusStorageKey(id)
			workflowIds = append(workflowIds, string(taskId))
		}
	}

	prefixes := [][]byte{}
	for _, id := range workflowIds {
		if len(id) != 26 {
			continue
		}
		prefixes = append(prefixes, TaskExecutionPrefix(id))
	}
	total, err = n.db.CountKeysByPrefixes(prefixes)

	if err != nil {
		n.logger.Error("error counting execution for", "user", user.Address, "error", err)
		return nil, grpcstatus.Errorf(codes.Internal, "Internal error counting execution")
	}

	return &avsproto.GetExecutionCountResp{
		Total: total,
	}, nil
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

func (n *Engine) CreateSecret(user *model.User, payload *avsproto.CreateOrUpdateSecretReq) (bool, error) {
	secret := &model.Secret{
		User:       user,
		Name:       payload.Name,
		Value:      payload.Secret,
		OrgID:      payload.OrgId,
		WorkflowID: payload.WorkflowId,
	}

	updates := map[string][]byte{}
	if strings.HasPrefix(strings.ToLower(payload.Name), "ap_") {
		return false, grpcstatus.Errorf(codes.InvalidArgument, "secret name cannot start with ap_")
	}

	if len(payload.Name) == 0 || len(payload.Name) > MaxSecretNameLength {
		return false, grpcstatus.Errorf(codes.InvalidArgument, "secret name lengh is invalid: should be 1-255 character")
	}

	key, _ := SecretStorageKey(secret)
	updates[key] = []byte(payload.Secret)
	err := n.db.BatchWrite(updates)
	if err == nil {
		return true, nil
	}

	return false, grpcstatus.Errorf(codes.Internal, "Cannot save data")
}

func (n *Engine) UpdateSecret(user *model.User, payload *avsproto.CreateOrUpdateSecretReq) (bool, error) {
	updates := map[string][]byte{}
	secret := &model.Secret{
		User:       user,
		Name:       payload.Name,
		Value:      payload.Secret,
		OrgID:      payload.OrgId,
		WorkflowID: payload.WorkflowId,
	}
	key, _ := SecretStorageKey(secret)
	if ok, err := n.db.Exist([]byte(key)); !ok || err != nil {
		return false, grpcstatus.Errorf(codes.NotFound, "Secret not found")
	}

	updates[key] = []byte(payload.Secret)

	err := n.db.BatchWrite(updates)
	if err == nil {
		return true, nil
	}

	return true, nil
}

// ListSecrets
func (n *Engine) ListSecrets(user *model.User, payload *avsproto.ListSecretsReq) (*avsproto.ListSecretsResp, error) {
	prefixes := []string{
		SecretStoragePrefix(user),
	}

	result := &avsproto.ListSecretsResp{
		Items: []*avsproto.ListSecretsResp_ResponseSecret{},
	}

	secretKeys, err := n.db.ListKeysMulti(prefixes)
	if err != nil {
		return nil, err
	}
	for _, k := range secretKeys {
		secretWithNameOnly := SecretNameFromKey(k)
		item := &avsproto.ListSecretsResp_ResponseSecret{
			Name:       secretWithNameOnly.Name,
			OrgId:      secretWithNameOnly.OrgID,
			WorkflowId: secretWithNameOnly.WorkflowID,
		}

		result.Items = append(result.Items, item)
	}

	return result, nil
}

func (n *Engine) DeleteSecret(user *model.User, payload *avsproto.DeleteSecretReq) (bool, error) {
	// No need to check permission, the key is prefixed by user eoa already
	secret := &model.Secret{
		Name:       payload.Name,
		User:       user,
		OrgID:      payload.OrgId,
		WorkflowID: payload.WorkflowId,
	}
	key, _ := SecretStorageKey(secret)
	err := n.db.Delete([]byte(key))

	return err == nil, err
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

func (n *Engine) CanStreamCheck(address string) bool {
	// Only enable for our own operator first, once it's stable we will roll out to all
	return strings.EqualFold(address, "0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d") || strings.EqualFold(address, "0xc6b87cc9e85b07365b6abefff061f237f7cf7dc3")
}

// GetWorkflowCount returns the number of workflows for the given addresses of smart wallets, or if no addresses are provided, it returns the total number of workflows belongs to the requested user
func (n *Engine) GetExecutionStats(user *model.User, payload *avsproto.GetExecutionStatsReq) (*avsproto.GetExecutionStatsResp, error) {
	workflowIds := payload.WorkflowIds
	days := payload.Days
	if days <= 0 {
		days = 7 // Default to 7 days if not specified
	}

	cutoffTime := time.Now().AddDate(0, 0, -int(days)).UnixMilli()

	// Initialize counters
	total := int64(0)
	succeeded := int64(0)
	failed := int64(0)
	var totalExecutionTime int64 = 0

	if len(workflowIds) == 0 {
		workflowIds = []string{}
		taskIds, err := n.db.GetKeyHasPrefix(UserTaskStoragePrefix(user.Address))
		if err != nil {
			return nil, grpcstatus.Errorf(codes.Internal, "Internal error retrieving tasks")
		}
		for _, id := range taskIds {
			taskId := TaskIdFromTaskStatusStorageKey(id)
			workflowIds = append(workflowIds, string(taskId))
		}
	}

	for _, id := range workflowIds {
		if len(id) != 26 {
			continue
		}

		items, err := n.db.GetByPrefix(TaskExecutionPrefix(id))
		if err != nil {
			n.logger.Error("error getting executions", "workflow", id, "error", err)
			continue
		}

		for _, item := range items {
			execution := &avsproto.Execution{}
			if err := protojson.Unmarshal(item.Value, execution); err != nil {
				n.logger.Error("error unmarshalling execution", "error", err)
				continue
			}

			if execution.StartAt < cutoffTime {
				continue
			}

			total++
			if execution.Success {
				succeeded++
			} else {
				failed++
			}

			if execution.EndAt > execution.StartAt {
				executionTime := execution.EndAt - execution.StartAt
				totalExecutionTime += executionTime
			}
		}
	}

	var avgExecutionTime float64 = 0
	if total > 0 {
		avgExecutionTime = float64(totalExecutionTime) / float64(total)
	}

	return &avsproto.GetExecutionStatsResp{
		Total:            total,
		Succeeded:        succeeded,
		Failed:           failed,
		AvgExecutionTime: avgExecutionTime,
	}, nil
}

func (n *Engine) GetWorkflowCount(user *model.User, payload *avsproto.GetWorkflowCountReq) (*avsproto.GetWorkflowCountResp, error) {
	smartWalletAddresses := payload.Addresses

	total := int64(0)
	var err error

	// Example logic to count workflows
	// This should be replaced with actual logic to count workflows based on addresses
	if len(smartWalletAddresses) == 0 {
		// Default logic if no addresses are provided we count all tasks belongs to the user
		total, err = n.db.CountKeysByPrefix(UserTaskStoragePrefix(user.Address))
	} else {
		prefixes := [][]byte{}

		for _, address := range smartWalletAddresses {
			smartWalletAddress := common.HexToAddress(address)
			if ok, err := ValidWalletOwner(n.db, user, smartWalletAddress); !ok || err != nil {
				// skip if the address is not a valid smart wallet address or it isn't belong to this user
				continue
			}

			prefixes = append(prefixes, SmartWalletTaskStoragePrefix(user.Address, smartWalletAddress))
		}

		total, err = n.db.CountKeysByPrefixes(prefixes)
	}

	if err != nil {
		n.logger.Error("error counting task for", "user", user.Address, "error", err)
		return nil, grpcstatus.Errorf(codes.Internal, "Internal error counting workflow")
	}

	return &avsproto.GetWorkflowCountResp{
		Total: total,
	}, nil
}
