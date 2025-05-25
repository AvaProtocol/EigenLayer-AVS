// The core package that manage and distribute and execute task
package taskengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
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
	badger "github.com/dgraph-io/badger/v4"
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
	DefaultLimit        = 50
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
	if err := n.seq.Release(); err != nil {
		n.logger.Error("failed to release sequence", "error", err)
	}
	n.shutdown = true
}

func (n *Engine) MustStart() error {
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

	return nil
}

// ListWallets corresponds to the ListWallets RPC.
func (n *Engine) ListWallets(owner common.Address, payload *avsproto.ListWalletReq) (*avsproto.ListWalletResp, error) {
	walletsToReturnProto := []*avsproto.SmartWallet{}
	processedAddresses := make(map[string]bool)

	defaultSystemFactory := n.smartWalletConfig.FactoryAddress
	defaultDerivedAddress, deriveErr := aa.GetSenderAddressForFactory(rpcConn, owner, defaultSystemFactory, defaultSalt)

	if deriveErr != nil {
		n.logger.Warn("Failed to derive default system wallet address for ListWallets", "owner", owner.Hex(), "error", deriveErr)
	} else if defaultDerivedAddress == nil || *defaultDerivedAddress == (common.Address{}) {
		n.logger.Warn("Derived default system wallet address is nil or zero for ListWallets", "owner", owner.Hex())
	} else {
		includeThisDefault := true
		if payload != nil {
			if pfa := payload.GetFactoryAddress(); pfa != "" && !strings.EqualFold(defaultSystemFactory.Hex(), pfa) {
				includeThisDefault = false
			}
			if ps := payload.GetSalt(); ps != "" && defaultSalt.String() != ps {
				includeThisDefault = false
			}
		}

		if includeThisDefault {
			modelWallet, dbGetErr := GetWallet(n.db, owner, defaultDerivedAddress.Hex())

			isHidden := false
			actualSalt := defaultSalt.String()
			actualFactory := defaultSystemFactory.Hex()

			if dbGetErr == nil {
				isHidden = modelWallet.IsHidden
				actualSalt = modelWallet.Salt.String()
				if modelWallet.Factory != nil {
					actualFactory = modelWallet.Factory.Hex()
				}
			} else if dbGetErr != badger.ErrKeyNotFound {
				n.logger.Warn("DB error fetching default derived wallet for ListWallets", "address", defaultDerivedAddress.Hex(), "error", dbGetErr)
			}

			walletsToReturnProto = append(walletsToReturnProto, &avsproto.SmartWallet{
				Address:  defaultDerivedAddress.Hex(),
				Salt:     actualSalt,
				Factory:  actualFactory,
				IsHidden: isHidden,
			})
			processedAddresses[strings.ToLower(defaultDerivedAddress.Hex())] = true
		}
	}

	dbItems, listErr := n.db.GetByPrefix(WalletByOwnerPrefix(owner))
	if listErr != nil && listErr != badger.ErrKeyNotFound {
		n.logger.Error("Error fetching wallets by owner prefix for ListWallets", "owner", owner.Hex(), "error", listErr)
		if len(walletsToReturnProto) == 0 {
			return nil, status.Errorf(codes.Code(avsproto.Error_StorageUnavailable), "Error fetching wallets by owner: %v", listErr)
		}
	}

	for _, item := range dbItems {
		storedModelWallet := &model.SmartWallet{}
		if err := storedModelWallet.FromStorageData(item.Value); err != nil {
			n.logger.Error("Failed to parse stored wallet data for ListWallets", "key", string(item.Key), "error", err)
			continue
		}

		if processedAddresses[strings.ToLower(storedModelWallet.Address.Hex())] {
			continue
		}

		if pfa := payload.GetFactoryAddress(); pfa != "" {
			if storedModelWallet.Factory == nil || !strings.EqualFold(storedModelWallet.Factory.Hex(), pfa) {
				continue
			}
		}

		if ps := payload.GetSalt(); ps != "" {
			if storedModelWallet.Salt == nil || storedModelWallet.Salt.String() != ps {
				continue
			}
		}

		factoryString := ""
		if storedModelWallet.Factory != nil {
			factoryString = storedModelWallet.Factory.Hex()
		}
		walletsToReturnProto = append(walletsToReturnProto, &avsproto.SmartWallet{
			Address:  storedModelWallet.Address.Hex(),
			Salt:     storedModelWallet.Salt.String(),
			Factory:  factoryString,
			IsHidden: storedModelWallet.IsHidden,
		})
	}
	return &avsproto.ListWalletResp{Items: walletsToReturnProto}, nil
}

func (n *Engine) GetWallet(user *model.User, payload *avsproto.GetWalletReq) (*avsproto.GetWalletResp, error) {
	if payload.GetFactoryAddress() != "" && !common.IsHexAddress(payload.GetFactoryAddress()) {
		return nil, status.Errorf(codes.InvalidArgument, InvalidFactoryAddressError)
	}

	saltBig := defaultSalt
	if payload.GetSalt() != "" {
		var ok bool
		saltBig, ok = math.ParseBig256(payload.GetSalt())
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "%s: %s", InvalidSmartAccountSaltError, payload.GetSalt())
		}
	}

	factoryAddr := n.smartWalletConfig.FactoryAddress
	if payload.GetFactoryAddress() != "" {
		factoryAddr = common.HexToAddress(payload.GetFactoryAddress())
	}

	derivedSenderAddress, err := aa.GetSenderAddressForFactory(rpcConn, user.Address, factoryAddr, saltBig)
	if err != nil || derivedSenderAddress == nil || *derivedSenderAddress == (common.Address{}) {
		n.logger.Warn("Failed to derive sender address or derived address is nil or zero for GetWallet", "owner", user.Address.Hex(), "factory", factoryAddr.Hex(), "salt", saltBig.String(), "derived", derivedSenderAddress, "error", err)
		return nil, status.Errorf(codes.InvalidArgument, "Failed to derive sender address or derived address is nil or zero. Error: %v", err)
	}

	dbModelWallet, err := GetWallet(n.db, user.Address, derivedSenderAddress.Hex())

	if err != nil && err != badger.ErrKeyNotFound {
		n.logger.Error("Error fetching wallet from DB for GetWallet", "owner", user.Address.Hex(), "wallet", derivedSenderAddress.Hex(), "error", err)
		return nil, status.Errorf(codes.Code(avsproto.Error_StorageUnavailable), "Error fetching wallet: %v", err)
	}

	if err == badger.ErrKeyNotFound {
		n.logger.Info("Wallet not found in DB for GetWallet, creating new entry", "owner", user.Address.Hex(), "walletAddress", derivedSenderAddress.Hex())
		newModelWallet := &model.SmartWallet{
			Owner:    &user.Address,
			Address:  derivedSenderAddress,
			Factory:  &factoryAddr,
			Salt:     saltBig,
			IsHidden: false,
		}
		if storeErr := StoreWallet(n.db, user.Address, newModelWallet); storeErr != nil {
			n.logger.Error("Error storing new wallet to DB for GetWallet", "owner", user.Address.Hex(), "walletAddress", derivedSenderAddress.Hex(), "error", storeErr)
			return nil, status.Errorf(codes.Code(avsproto.Error_StorageWriteError), "Error storing new wallet: %v", storeErr)
		}
		dbModelWallet = newModelWallet
	}

	resp := &avsproto.GetWalletResp{
		Address:        dbModelWallet.Address.Hex(),
		Salt:           dbModelWallet.Salt.String(),
		FactoryAddress: dbModelWallet.Factory.Hex(),
		IsHidden:       dbModelWallet.IsHidden,
	}

	statSvc := NewStatService(n.db)
	stat, statErr := statSvc.GetTaskCount(dbModelWallet)
	if statErr != nil {
		n.logger.Warn("Failed to get task count for GetWallet response", "walletAddress", dbModelWallet.Address.Hex(), "error", statErr)
	}
	resp.TotalTaskCount = stat.Total
	resp.ActiveTaskCount = stat.Active
	resp.CompletedTaskCount = stat.Completed
	resp.FailedTaskCount = stat.Failed
	resp.CanceledTaskCount = stat.Canceled

	return resp, nil
}

// SetWallet is the gRPC handler for the SetWallet RPC.
// It uses the owner (from auth context), salt, and factory_address from payload to identify/derive the wallet.
// It then sets the IsHidden status for that wallet.
func (n *Engine) SetWallet(owner common.Address, payload *avsproto.SetWalletReq) (*avsproto.GetWalletResp, error) {
	if payload.GetFactoryAddress() != "" && !common.IsHexAddress(payload.GetFactoryAddress()) {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid factory address format: %s", payload.GetFactoryAddress())
	}

	saltBig, ok := math.ParseBig256(payload.GetSalt())
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid salt format: %s", payload.GetSalt())
	}

	factoryAddr := n.smartWalletConfig.FactoryAddress // Default factory
	if payload.GetFactoryAddress() != "" {
		factoryAddr = common.HexToAddress(payload.GetFactoryAddress())
	}

	derivedWalletAddress, err := aa.GetSenderAddressForFactory(rpcConn, owner, factoryAddr, saltBig)
	if err != nil {
		n.logger.Error("Failed to derive wallet address for SetWallet", "owner", owner.Hex(), "salt", payload.GetSalt(), "factory", payload.GetFactoryAddress(), "error", err)
		return nil, status.Errorf(codes.Internal, "Failed to derive wallet address: %v", err)
	}
	if derivedWalletAddress == nil || *derivedWalletAddress == (common.Address{}) {
		n.logger.Error("Derived wallet address is nil or zero for SetWallet", "owner", owner.Hex(), "salt", payload.GetSalt(), "factory", payload.GetFactoryAddress())
		return nil, status.Errorf(codes.Internal, "Derived wallet address is nil or zero")
	}

	err = SetWalletHiddenStatus(n.db, owner, derivedWalletAddress.Hex(), payload.GetIsHidden())
	if err != nil {
		if errors.Is(err, badger.ErrKeyNotFound) {
			n.logger.Warn("Wallet not found for SetWallet", "owner", owner.Hex(), "derivedAddress", derivedWalletAddress.Hex(), "salt", payload.GetSalt(), "factory", payload.GetFactoryAddress())
			// If wallet doesn't exist, SetWalletHiddenStatus from schema returns a wrapped ErrKeyNotFound.
			// The SetWallet RPC is for existing wallets identified by salt/factory.
			// So, if not found by identifiers, it's a NotFound error.
			return nil, status.Errorf(codes.NotFound, "Wallet not found for the specified salt and factory.")
		}
		n.logger.Error("Failed to set wallet hidden status via schema.SetWalletHiddenStatus", "owner", owner.Hex(), "wallet", derivedWalletAddress.Hex(), "isHidden", payload.GetIsHidden(), "error", err)
		return nil, status.Errorf(codes.Internal, "Failed to update wallet hidden status: %v", err)
	}

	updatedModelWallet, getErr := GetWallet(n.db, owner, derivedWalletAddress.Hex())
	if getErr != nil {
		n.logger.Error("Failed to fetch wallet after SetWallet operation", "owner", owner.Hex(), "wallet", derivedWalletAddress.Hex(), "error", getErr)
		return nil, status.Errorf(codes.Internal, "Failed to retrieve wallet details after update: %v", getErr)
	}

	resp := &avsproto.GetWalletResp{
		Address:        updatedModelWallet.Address.Hex(),
		Salt:           updatedModelWallet.Salt.String(),
		FactoryAddress: updatedModelWallet.Factory.Hex(),
		IsHidden:       updatedModelWallet.IsHidden,
	}

	statSvc := NewStatService(n.db)
	stat, statErr := statSvc.GetTaskCount(updatedModelWallet)
	if statErr != nil {
		n.logger.Warn("Failed to get task count for SetWallet response", "walletAddress", updatedModelWallet.Address.Hex(), "error", statErr)
	}
	resp.TotalTaskCount = stat.Total
	resp.ActiveTaskCount = stat.Active
	resp.CompletedTaskCount = stat.Completed
	resp.FailedTaskCount = stat.Failed
	resp.CanceledTaskCount = stat.Canceled

	return resp, nil
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

	taskJSON, err := task.ToJSON()
	if err != nil {
		return nil, grpcstatus.Errorf(codes.Internal, "Failed to serialize task: %v", err)
	}

	updates[string(TaskStorageKey(task.Id, task.Status))] = taskJSON
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

	//nolint:S1000
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
				n.logger.Info("stream check to operator",
					"task_id", task.Id,
					"operator", payload.Address,
					"op", resp.Op.String(),
					"task_id_meta", resp.TaskMetadata.TaskId)

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

	reason := GetTriggerReasonOrDefault(payload.Reason, payload.TaskId, n.logger)

	queueTaskData := QueueExecutionData{
		Reason:      reason,
		ExecutionID: ulid.Make().String(),
	}

	data, err := json.Marshal(queueTaskData)
	if err != nil {
		n.logger.Error("error serialize trigger to json", err)
		return err
	}

	if _, err := n.queue.Enqueue(JobTypeExecuteTask, payload.TaskId, data); err != nil {
		n.logger.Error("failed to enqueue task", "error", err, "task_id", payload.TaskId)
		return err
	}
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

	taskKeys, err := n.db.ListKeysMulti(prefixes)
	if err != nil {
		return nil, grpcstatus.Errorf(codes.Code(avsproto.Error_StorageUnavailable), StorageUnavailableError)
	}

	// second, do the sort, this is key sorted by ordering of their insertion
	sort.Slice(taskKeys, func(i, j int) bool {
		id1 := ulid.MustParse(string(model.TaskKeyToId([]byte(taskKeys[i][2:]))))
		id2 := ulid.MustParse(string(model.TaskKeyToId([]byte(taskKeys[j][2:]))))
		return id1.Compare(id2) < 0
	})

	taskResp := &avsproto.ListTasksResp{
		Items:  []*avsproto.ListTasksResp_Item{},
		Cursor: "",
	}

	total := 0
	cursor, err := CursorFromString(payload.Cursor)
	limit := int(payload.Limit)
	if limit < 0 {
		return nil, status.Errorf(codes.InvalidArgument, InvalidPaginationParam)
	}
	if limit == 0 {
		limit = DefaultLimit
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
				ExecutionCount:     t.ExecutionCount,
				LastRanAt:          t.LastRanAt,
				Status:             t.Status,
				Trigger:            t.Trigger,
			})
			total += 1
		}

		if total >= limit {
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

	reason := GetTriggerReasonOrDefault(payload.Reason, payload.TaskId, n.logger)

	queueTaskData := QueueExecutionData{
		Reason:      reason,
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

	// Asynchronous execution path
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

	// This is where the TaskTriggerKey is set for asynchronous tasks
	if err := n.setExecutionStatusQueue(task, queueTaskData.ExecutionID); err != nil {
		n.logger.Error("failed to set execution status in queue storage", "error", err, "task_id", payload.TaskId, "execution_id", queueTaskData.ExecutionID)
		return nil, grpcstatus.Errorf(codes.Internal, "Failed to set execution status: %v", err)
	}

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

	// second, do the sort, this is key sorted by ordering of their insertion
	sort.Slice(executionKeys, func(i, j int) bool {
		id1 := ulid.MustParse(string(ExecutionIdFromStorageKey([]byte(executionKeys[i]))))
		id2 := ulid.MustParse(string(ExecutionIdFromStorageKey([]byte(executionKeys[j]))))
		return id1.Compare(id2) < 0
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

	limit := int(payload.Limit)
	if limit < 0 {
		return nil, status.Errorf(codes.InvalidArgument, InvalidPaginationParam)
	}

	if limit == 0 {
		limit = DefaultLimit
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
		if total >= limit {
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

func (n *Engine) getExecutionStatusFromQueue(task *model.Task, executionID string) (*avsproto.ExecutionStatus, error) {
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
		if status, err := n.getExecutionStatusFromQueue(task, payload.ExecutionId); err == nil {
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
		if len(id) != TaskIDLength {
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

	if err := n.db.Delete(TaskStorageKey(task.Id, task.Status)); err != nil {
		n.logger.Error("failed to delete task storage", "error", err, "task_id", task.Id)
		return false, fmt.Errorf("failed to delete task: %w", err)
	}

	if err := n.db.Delete(TaskUserKey(task)); err != nil {
		n.logger.Error("failed to delete task user key", "error", err, "task_id", task.Id)
		return false, fmt.Errorf("failed to delete task user key: %w", err)
	}

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
		if moveErr := n.db.Move(
			TaskStorageKey(task.Id, oldStatus),
			TaskStorageKey(task.Id, task.Status),
		); moveErr != nil {
			n.logger.Error("failed to move task", "error", moveErr, "task_id", task.Id)
		}

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
		return false, grpcstatus.Errorf(codes.InvalidArgument, "secret name length is invalid: should be 1-255 character")
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
		Items:   []*avsproto.ListSecretsResp_ResponseSecret{},
		Cursor:  "",
		HasMore: false,
	}

	secretKeys, err := n.db.ListKeysMulti(prefixes)
	if err != nil {
		return nil, err
	}

	sort.Strings(secretKeys)

	var before, after, legacyCursor string
	var limitVal int64

	if payload != nil {
		before = payload.Before
		after = payload.After
		legacyCursor = payload.Cursor
		limitVal = payload.Limit
	}

	cursor, limit, err := SetupPagination(before, after, legacyCursor, limitVal)
	if err != nil {
		return nil, err
	}

	total := 0
	var lastKey string

	for _, k := range secretKeys {
		if !cursor.IsZero() {
			if (cursor.Direction == CursorDirectionNext && k <= cursor.Position) ||
				(cursor.Direction == CursorDirectionPrevious && k >= cursor.Position) {
				continue
			}
		}

		secretWithNameOnly := SecretNameFromKey(k)
		item := &avsproto.ListSecretsResp_ResponseSecret{
			Name:       secretWithNameOnly.Name,
			OrgId:      secretWithNameOnly.OrgID,
			WorkflowId: secretWithNameOnly.WorkflowID,
		}

		result.Items = append(result.Items, item)
		lastKey = k
		total++

		if total >= limit {
			result.HasMore = true
			break
		}
	}

	if result.HasMore && lastKey != "" {
		result.Cursor = CreateNextCursor(lastKey)
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
		if len(id) != TaskIDLength {
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

func (n *Engine) RunNodeWithInputs(nodeType string, nodeConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	vm, err := NewVMWithData(nil, nil, n.smartWalletConfig, nil)
	if err != nil {
		return nil, err
	}

	vm.WithLogger(n.logger).WithDb(n.db)

	node, err := CreateNodeFromType(nodeType, nodeConfig, "")
	if err != nil {
		return nil, err
	}

	// For blockTrigger, add blockNumber to input variables
	if nodeType == "blockTrigger" {
		if blockNumber, ok := nodeConfig["blockNumber"]; ok {
			if inputVariables == nil {
				inputVariables = make(map[string]interface{})
			}
			inputVariables["blockNumber"] = blockNumber
		}
	}

	// Run the node with input variables
	executionStep, err := vm.RunNodeWithInputs(node, inputVariables)
	if err != nil {
		return nil, err
	}

	if !executionStep.Success {
		return nil, fmt.Errorf("execution failed: %s", executionStep.Error)
	}

	result := make(map[string]interface{})

	switch nodeType {
	case "blockTrigger":
		if codeOutput := executionStep.GetCustomCode(); codeOutput != nil && codeOutput.Data != nil {
			if valueInterface := codeOutput.Data.AsInterface(); valueInterface != nil {
				if dataMap, ok := valueInterface.(map[string]interface{}); ok {
					result = dataMap
				}
			}
		}
	case "restApi":
		if restOutput := executionStep.GetRestApi(); restOutput != nil && restOutput.Data != nil {
			// restOutput.Data is *anypb.Any, so we unmarshal the raw bytes as JSON
			var data map[string]interface{}
			if err := json.Unmarshal(restOutput.Data.Value, &data); err == nil {
				result["data"] = data
			} else {
				result["data"] = restOutput.Data
			}
		}
	case "contractRead":
		if readOutput := executionStep.GetContractRead(); readOutput != nil {
			result["data"] = readOutput.Data
		}
	case "customCode":
		if codeOutput := executionStep.GetCustomCode(); codeOutput != nil && codeOutput.Data != nil {
			if valueInterface := codeOutput.Data.AsInterface(); valueInterface != nil {
				if dataMap, ok := valueInterface.(map[string]interface{}); ok {
					result = dataMap
				}
			} else {
				result["data"] = codeOutput.Data
			}
		}
	case "branch":
		if branchOutput := executionStep.GetBranch(); branchOutput != nil {
			result["conditionId"] = branchOutput.ConditionId
		}
	case "filter":
		if filterOutput := executionStep.GetFilter(); filterOutput != nil && filterOutput.Data != nil {
			// filterOutput.Data is *anypb.Any, so we unmarshal the raw bytes as JSON
			var data interface{}
			if err := json.Unmarshal(filterOutput.Data.Value, &data); err == nil {
				result["data"] = data
			} else {
				result["data"] = filterOutput.Data
			}
		}
	}

	return result, nil
}
