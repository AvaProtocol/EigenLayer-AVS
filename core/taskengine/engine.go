// The core package that manage and distribute and execute task
package taskengine

import (
	"context"
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
	"github.com/ethereum/go-ethereum/core/types"
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

	// Token enrichment service for ERC20 transfers
	tokenEnrichmentService *TokenEnrichmentService
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

	// Initialize TokenEnrichmentService
	// Always try to initialize, even without RPC, so we can serve whitelist data
	tokenService, err := NewTokenEnrichmentService(rpcConn, logger)
	if err != nil {
		logger.Warn("Failed to initialize TokenEnrichmentService", "error", err)
		// Don't fail engine initialization, continue without token enrichment
	} else {
		e.tokenEnrichmentService = tokenService

		// Load token whitelist data into cache
		if err := tokenService.LoadWhitelist(); err != nil {
			logger.Warn("Failed to load token whitelist", "error", err)
			// Don't fail engine initialization, continue with RPC-only token enrichment
		} else {
			logger.Info("Token whitelist loaded successfully", "cacheSize", tokenService.GetCacheSize())
		}

		if rpcConn != nil {
			logger.Info("TokenEnrichmentService initialized successfully with RPC and whitelist support")
		} else {
			logger.Info("TokenEnrichmentService initialized successfully with whitelist-only support (no RPC)")
		}
	}

	return &e
}

func (n *Engine) Stop() {
	if n.seq != nil {
		if err := n.seq.Release(); err != nil {
			n.logger.Error("failed to release sequence", "error", err)
		}
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
	n.lock.Lock()
	n.logger.Info("processing aggregator check hit", "operator", address, "task_id", payload.TaskId)

	if state, exists := n.trackSyncedTasks[address]; exists {
		state.TaskID[payload.TaskId] = true
	}

	n.logger.Info("processed aggregator check hit", "operator", address, "task_id", payload.TaskId)
	n.lock.Unlock()

	// Create trigger data
	triggerData := &TriggerData{
		Type:   payload.TriggerType,
		Output: ExtractTriggerOutput(payload.TriggerOutput),
	}

	// Enrich EventTrigger output if TokenEnrichmentService is available and it's a Transfer event
	if payload.TriggerType == avsproto.TriggerType_TRIGGER_TYPE_EVENT && n.tokenEnrichmentService != nil {
		if eventOutput := triggerData.Output.(*avsproto.EventTrigger_Output); eventOutput != nil {
			if evmLog := eventOutput.GetEvmLog(); evmLog != nil {
				n.logger.Debug("enriching EventTrigger output from operator",
					"task_id", payload.TaskId,
					"tx_hash", evmLog.TransactionHash,
					"block_number", evmLog.BlockNumber)

				// Fetch full event data from the blockchain using the minimal data from operator
				if enrichedEventOutput, err := n.enrichEventTriggerFromOperatorData(evmLog); err == nil {
					// Replace the minimal event output with the enriched one
					triggerData.Output = enrichedEventOutput
					n.logger.Debug("successfully enriched EventTrigger output",
						"task_id", payload.TaskId,
						"has_transfer_log", enrichedEventOutput.TransferLog != nil)
				} else {
					n.logger.Warn("failed to enrich EventTrigger output, using minimal data",
						"task_id", payload.TaskId,
						"error", err)
				}
			}
		}
	}

	queueTaskData := QueueExecutionData{
		TriggerType:   triggerData.Type,
		TriggerOutput: triggerData.Output,
		ExecutionID:   ulid.Make().String(),
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
		Items: []*avsproto.Task{},
		PageInfo: &avsproto.PageInfo{
			StartCursor:     "",
			EndCursor:       "",
			HasPreviousPage: false,
			HasNextPage:     false,
		},
	}

	var before, after string
	var limitVal int64

	if payload != nil {
		before = payload.Before
		after = payload.After
		limitVal = payload.Limit
	}

	cursor, limit, err := SetupPagination(before, after, limitVal)
	if err != nil {
		return nil, err
	}

	total := 0
	var hasMoreItems bool

	for i := len(taskKeys) - 1; i >= 0; i-- {
		key := taskKeys[i]
		taskID := string(model.TaskKeyToId(([]byte(key[2:]))))
		statusValue, err := n.db.GetKey([]byte(key))
		if err != nil {
			return nil, grpcstatus.Errorf(codes.Code(avsproto.Error_StorageUnavailable), StorageUnavailableError)
		}
		status, _ := strconv.Atoi(string(statusValue))

		taskIDUlid := model.UlidFromTaskId(taskID)
		if !cursor.IsZero() {
			if (cursor.Direction == CursorDirectionNext && cursor.LessThanOrEqualUlid(taskIDUlid)) ||
				(cursor.Direction == CursorDirectionPrevious && !cursor.LessThanUlid(taskIDUlid)) {
				continue
			}
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
			// Apply field control - conditionally populate expensive fields
			taskItem := &avsproto.Task{
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
			}

			// Conditionally populate expensive fields based on request parameters
			if payload != nil {
				if payload.IncludeNodes {
					taskItem.Nodes = t.Nodes
				}
				if payload.IncludeEdges {
					taskItem.Edges = t.Edges
				}
			}

			taskResp.Items = append(taskResp.Items, taskItem)
			total += 1
		}

		// If we've processed more than the limit, we know there are more items
		if total >= limit {
			hasMoreItems = true
			break
		}
	}

	// Set pagination info
	if len(taskResp.Items) > 0 {
		firstItem := taskResp.Items[0]
		lastItem := taskResp.Items[len(taskResp.Items)-1]

		// Always set cursors for the current page (GraphQL PageInfo convention)
		taskResp.PageInfo.StartCursor = CreateNextCursor(firstItem.Id)
		taskResp.PageInfo.EndCursor = CreateNextCursor(lastItem.Id)

		// Check if there are more items after the current page
		taskResp.PageInfo.HasNextPage = hasMoreItems

		// Check if there are items before the current page
		// This is true if we have a cursor and we're not at the beginning
		taskResp.PageInfo.HasPreviousPage = !cursor.IsZero() && cursor.Direction == CursorDirectionNext

		// For backward pagination, we need to check if there are items after
		if cursor.Direction == CursorDirectionPrevious {
			taskResp.PageInfo.HasNextPage = true // There are items after since we're going backwards
			// Check if there are more items before
			taskResp.PageInfo.HasPreviousPage = hasMoreItems
		}
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

func (n *Engine) TriggerTask(user *model.User, payload *avsproto.TriggerTaskReq) (*avsproto.TriggerTaskResp, error) {
	// Validate task ID format first
	if !ValidateTaskId(payload.TaskId) {
		return nil, grpcstatus.Errorf(codes.InvalidArgument, InvalidTaskIdFormat)
	}

	task, err := n.GetTask(user, payload.TaskId)
	if err != nil {
		return nil, err
	}

	// Explicit ownership validation for security (even though GetTask already checks this)
	if !task.OwnedBy(user.Address) {
		return nil, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
	}

	// Important business logic validation: Check if task is runnable
	if !task.IsRunable() {
		return nil, grpcstatus.Errorf(codes.FailedPrecondition, TaskIsNotRunnable)
	}

	// Create trigger data
	triggerData := &TriggerData{
		Type:   payload.TriggerType,
		Output: ExtractTriggerOutput(payload.TriggerOutput),
	}

	queueTaskData := QueueExecutionData{
		TriggerType:   triggerData.Type,
		TriggerOutput: triggerData.Output,
		ExecutionID:   ulid.Make().String(),
	}

	// Store execution status as pending first
	err = n.setExecutionStatusQueue(task, queueTaskData.ExecutionID)
	if err != nil {
		return nil, err
	}

	if payload.IsBlocking {
		executor := NewExecutor(n.smartWalletConfig, n.db, n.logger)
		execution, runErr := executor.RunTask(task, &queueTaskData)
		if runErr != nil {
			n.logger.Error("failed to run blocking task", runErr)
			// For blocking execution, return the error to the caller
			return nil, runErr
		}

		// Clean up TaskTriggerKey after successful blocking execution
		if queueTaskData.ExecutionID != "" {
			triggerKeyToClean := TaskTriggerKey(task, queueTaskData.ExecutionID)
			if delErr := n.db.Delete(triggerKeyToClean); delErr != nil {
				n.logger.Error("TriggerTask: Failed to delete TaskTriggerKey after successful blocking execution",
					"key", string(triggerKeyToClean), "task_id", task.Id, "execution_id", queueTaskData.ExecutionID, "error", delErr)
			}
		}

		if execution != nil {
			return &avsproto.TriggerTaskResp{
				ExecutionId: queueTaskData.ExecutionID,
				Status:      avsproto.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			}, nil
		}
	}

	// Add async execution
	data, err := json.Marshal(queueTaskData)
	if err != nil {
		n.logger.Error("error serialize trigger to json", "error", err)
		return nil, err
	}

	if _, err := n.queue.Enqueue(JobTypeExecuteTask, payload.TaskId, data); err != nil {
		n.logger.Error("failed to enqueue task", "error", err, "task_id", payload.TaskId)
		return nil, err
	}
	n.logger.Info("enqueue task into the queue system", "task_id", payload.TaskId)

	return &avsproto.TriggerTaskResp{
		ExecutionId: queueTaskData.ExecutionID,
		Status:      avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING,
	}, nil
}

// SimulateTask executes a complete task simulation by first running the trigger immediately,
// then executing the task nodes in sequence just like regular task execution.
// This is useful for testing tasks without waiting for actual trigger conditions.
// The task definition is provided in the request, so no storage persistence is required.
func (n *Engine) SimulateTask(user *model.User, trigger *avsproto.TaskTrigger, nodes []*avsproto.TaskNode, edges []*avsproto.TaskEdge, inputVariables map[string]interface{}) (*avsproto.Execution, error) {
	// Create a temporary task structure for simulation (not saved to storage)
	simulationTaskID := ulid.Make().String()
	task := &model.Task{
		Task: &avsproto.Task{
			Id:      simulationTaskID,
			Owner:   user.Address.Hex(),
			Trigger: trigger,
			Nodes:   nodes,
			Edges:   edges,
			Status:  avsproto.TaskStatus_Active, // Set as active for simulation
		},
	}

	// Basic validation: Check if task structure is valid for execution
	if task.Trigger == nil {
		return nil, grpcstatus.Errorf(codes.InvalidArgument, "task trigger is required for simulation")
	}
	if len(task.Nodes) == 0 {
		return nil, grpcstatus.Errorf(codes.InvalidArgument, "task must have at least one node for simulation")
	}

	// Step 1: Simulate the trigger to get trigger output data
	// Extract trigger type and config from the TaskTrigger
	n.logger.Info("SimulateTask received trigger", "trigger_type_raw", trigger.GetType(), "trigger_type_int", int(trigger.GetType()), "trigger_id", trigger.GetId(), "trigger_name", trigger.GetName())

	// Debug: Check what oneof field is set
	n.logger.Info("SimulateTask trigger oneof debug",
		"manual", trigger.GetManual(),
		"fixed_time", trigger.GetFixedTime() != nil,
		"cron", trigger.GetCron() != nil,
		"block", trigger.GetBlock() != nil,
		"event", trigger.GetEvent() != nil,
		"oneof_type", fmt.Sprintf("%T", trigger.GetTriggerType()))

	// Use TaskTriggerToTriggerType to determine type from oneof field instead of just GetType()
	triggerType := TaskTriggerToTriggerType(trigger)
	n.logger.Info("SimulateTask trigger type conversion", "from_oneof", triggerType, "from_explicit", trigger.GetType())

	// Validate that the derived trigger type matches the expected type
	if triggerType != trigger.GetType() {
		n.logger.Error("Trigger type mismatch", "derived_type", triggerType, "expected_type", trigger.GetType(), "trigger_id", trigger.GetId(), "trigger_name", trigger.GetName())
		return nil, grpcstatus.Errorf(codes.InvalidArgument, "trigger type mismatch: derived=%v, expected=%v", triggerType, trigger.GetType())
	}

	triggerTypeStr := TriggerTypeToString(triggerType)
	if triggerTypeStr == "" {
		return nil, grpcstatus.Errorf(codes.InvalidArgument, "unsupported trigger type: %v (oneof type: %T)", trigger.GetType(), trigger.GetTriggerType())
	}

	// Extract trigger config using the shared utility function
	triggerConfig := TaskTriggerToConfig(trigger)

	triggerOutput, err := n.runTriggerImmediately(triggerTypeStr, triggerConfig, inputVariables)
	if err != nil {
		return nil, fmt.Errorf("failed to simulate trigger: %w", err)
	}

	// Step 2: Create QueueExecutionData similar to regular task execution
	simulationID := ulid.Make().String()

	// Convert trigger output to proper protobuf structure
	var triggerOutputProto interface{}
	switch trigger.Type {
	case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
		runAt := uint64(0)
		if runAtVal, ok := triggerOutput["runAt"]; ok {
			if runAtUint, ok := runAtVal.(uint64); ok {
				runAt = runAtUint
			}
		}
		triggerOutputProto = &avsproto.ManualTrigger_Output{
			RunAt: runAt,
		}
	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
		timestamp := uint64(0)
		timestampISO := ""
		if ts, ok := triggerOutput["timestamp"]; ok {
			if tsUint, ok := ts.(uint64); ok {
				timestamp = tsUint
			}
		}
		if tsISO, ok := triggerOutput["timestamp_iso"]; ok {
			if tsISOStr, ok := tsISO.(string); ok {
				timestampISO = tsISOStr
			}
		}
		triggerOutputProto = &avsproto.FixedTimeTrigger_Output{
			Timestamp:    timestamp,
			TimestampIso: timestampISO,
		}
	case avsproto.TriggerType_TRIGGER_TYPE_CRON:
		timestamp := uint64(0)
		timestampISO := ""
		if ts, ok := triggerOutput["timestamp"]; ok {
			if tsUint, ok := ts.(uint64); ok {
				timestamp = tsUint
			}
		}
		if tsISO, ok := triggerOutput["timestamp_iso"]; ok {
			if tsISOStr, ok := tsISO.(string); ok {
				timestampISO = tsISOStr
			}
		}
		triggerOutputProto = &avsproto.CronTrigger_Output{
			Timestamp:    timestamp,
			TimestampIso: timestampISO,
		}
	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		blockNumber := uint64(0)
		blockHash := ""
		timestamp := uint64(0)
		parentHash := ""
		difficulty := ""
		gasLimit := uint64(0)
		gasUsed := uint64(0)

		if bn, ok := triggerOutput["blockNumber"]; ok {
			if bnUint, ok := bn.(uint64); ok {
				blockNumber = bnUint
			}
		}
		if bh, ok := triggerOutput["blockHash"]; ok {
			if bhStr, ok := bh.(string); ok {
				blockHash = bhStr
			}
		}
		if ts, ok := triggerOutput["timestamp"]; ok {
			if tsUint, ok := ts.(uint64); ok {
				timestamp = tsUint
			}
		}
		if ph, ok := triggerOutput["parentHash"]; ok {
			if phStr, ok := ph.(string); ok {
				parentHash = phStr
			}
		}
		if diff, ok := triggerOutput["difficulty"]; ok {
			if diffStr, ok := diff.(string); ok {
				difficulty = diffStr
			}
		}
		if gl, ok := triggerOutput["gasLimit"]; ok {
			if glUint, ok := gl.(uint64); ok {
				gasLimit = glUint
			}
		}
		if gu, ok := triggerOutput["gasUsed"]; ok {
			if guUint, ok := gu.(uint64); ok {
				gasUsed = guUint
			}
		}

		triggerOutputProto = &avsproto.BlockTrigger_Output{
			BlockNumber: blockNumber,
			BlockHash:   blockHash,
			Timestamp:   timestamp,
			ParentHash:  parentHash,
			Difficulty:  difficulty,
			GasLimit:    gasLimit,
			GasUsed:     gasUsed,
		}
	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		// For event triggers, create a basic event output
		// In a real scenario, this would be populated with actual event data
		triggerOutputProto = &avsproto.EventTrigger_Output{}
	default:
		// For unknown trigger types, create a manual trigger as fallback
		triggerOutputProto = &avsproto.ManualTrigger_Output{
			RunAt: uint64(time.Now().Unix()),
		}
	}

	queueData := &QueueExecutionData{
		TriggerType:   trigger.Type,
		TriggerOutput: triggerOutputProto,
		ExecutionID:   simulationID,
	}

	// Step 3: Load secrets for the task
	secrets, err := LoadSecretForTask(n.db, task)
	if err != nil {
		n.logger.Warn("Failed to load secrets for workflow simulation", "error", err, "task_id", task.Id)
		// Don't fail the simulation, just use empty secrets
		secrets = make(map[string]string)
	}

	// Step 4: Create VM with simulated trigger data (similar to RunTask)
	triggerReason := GetTriggerReasonOrDefault(queueData, task.Id, n.logger)
	vm, err := NewVMWithData(task, triggerReason, n.smartWalletConfig, secrets)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM for simulation: %w", err)
	}

	vm.WithLogger(n.logger).WithDb(n.db)

	// Add input variables to VM for template processing
	for key, value := range inputVariables {
		vm.AddVar(key, value)
	}

	// Step 5: Add trigger data as "trigger" variable for convenient access in JavaScript
	// This ensures scripts can access trigger.data regardless of the trigger's name
	triggerDataMap := make(map[string]interface{})
	switch triggerReason.Type {
	case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
		triggerDataMap["triggered"] = true
		if runAt, ok := triggerOutput["runAt"]; ok {
			triggerDataMap["runAt"] = runAt
		}
	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
		if timestamp, ok := triggerOutput["timestamp"]; ok {
			triggerDataMap["timestamp"] = timestamp
		}
		if timestampISO, ok := triggerOutput["timestamp_iso"]; ok {
			triggerDataMap["timestamp_iso"] = timestampISO
		}
	case avsproto.TriggerType_TRIGGER_TYPE_CRON:
		if timestamp, ok := triggerOutput["timestamp"]; ok {
			triggerDataMap["timestamp"] = timestamp
		}
		if timestampISO, ok := triggerOutput["timestamp_iso"]; ok {
			triggerDataMap["timestamp_iso"] = timestampISO
		}
	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		if blockNumber, ok := triggerOutput["blockNumber"]; ok {
			triggerDataMap["blockNumber"] = blockNumber
		}
		if blockHash, ok := triggerOutput["blockHash"]; ok {
			triggerDataMap["blockHash"] = blockHash
		}
		if timestamp, ok := triggerOutput["timestamp"]; ok {
			triggerDataMap["timestamp"] = timestamp
		}
		if parentHash, ok := triggerOutput["parentHash"]; ok {
			triggerDataMap["parentHash"] = parentHash
		}
		if difficulty, ok := triggerOutput["difficulty"]; ok {
			triggerDataMap["difficulty"] = difficulty
		}
		if gasLimit, ok := triggerOutput["gasLimit"]; ok {
			triggerDataMap["gasLimit"] = gasLimit
		}
		if gasUsed, ok := triggerOutput["gasUsed"]; ok {
			triggerDataMap["gasUsed"] = gasUsed
		}
	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		// Copy all event trigger data
		for k, v := range triggerOutput {
			triggerDataMap[k] = v
		}
	default:
		// For unknown trigger types, copy all output data
		for k, v := range triggerOutput {
			triggerDataMap[k] = v
		}
	}

	// Add the universal "trigger" variable for JavaScript access
	vm.AddVar("trigger", map[string]any{"data": triggerDataMap})

	// Step 6: Compile the workflow
	t0 := time.Now()

	if err = vm.Compile(); err != nil {
		return nil, fmt.Errorf("failed to compile workflow for simulation: %w", err)
	}

	// Step 7: Create and add a trigger execution step manually before running nodes
	// Convert inputVariables keys to trigger inputs
	triggerInputs := make([]string, 0, len(inputVariables))
	for key := range inputVariables {
		triggerInputs = append(triggerInputs, key)
	}

	triggerStep := &avsproto.Execution_Step{
		Id:      task.Trigger.Id, // Use new 'id' field
		Success: true,
		Error:   "",
		StartAt: t0.UnixMilli(),
		EndAt:   t0.UnixMilli(),
		Log:     fmt.Sprintf("Simulated trigger: %s executed successfully", task.Trigger.Name),
		Inputs:  triggerInputs,                  // Use inputVariables keys as trigger inputs
		Type:    queueData.TriggerType.String(), // Use trigger type as string
		Name:    task.Trigger.Name,              // Use new 'name' field
	}

	// Set trigger output data in the step based on trigger type
	switch queueData.TriggerType {
	case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
		if output, ok := triggerOutputProto.(*avsproto.ManualTrigger_Output); ok {
			triggerStep.OutputData = &avsproto.Execution_Step_ManualTrigger{ManualTrigger: output}
		}
	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
		if output, ok := triggerOutputProto.(*avsproto.FixedTimeTrigger_Output); ok {
			triggerStep.OutputData = &avsproto.Execution_Step_FixedTimeTrigger{FixedTimeTrigger: output}
		}
	case avsproto.TriggerType_TRIGGER_TYPE_CRON:
		if output, ok := triggerOutputProto.(*avsproto.CronTrigger_Output); ok {
			triggerStep.OutputData = &avsproto.Execution_Step_CronTrigger{CronTrigger: output}
		}
	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		if output, ok := triggerOutputProto.(*avsproto.BlockTrigger_Output); ok {
			triggerStep.OutputData = &avsproto.Execution_Step_BlockTrigger{BlockTrigger: output}
		}
	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		if output, ok := triggerOutputProto.(*avsproto.EventTrigger_Output); ok {
			triggerStep.OutputData = &avsproto.Execution_Step_EventTrigger{EventTrigger: output}
		}
	}

	// Add trigger step to execution logs
	vm.ExecutionLogs = append(vm.ExecutionLogs, triggerStep)

	// Step 8: Run the workflow nodes
	runErr := vm.Run()
	t1 := time.Now()

	// Step 9: Create execution result with unified structure
	execution := &avsproto.Execution{
		Id:      simulationID,
		StartAt: t0.UnixMilli(),
		EndAt:   t1.UnixMilli(),
		Success: runErr == nil,
		Error:   "",
		Steps:   vm.ExecutionLogs, // Now contains both trigger and node steps
	}

	if runErr != nil {
		n.logger.Error("workflow simulation failed", "error", runErr, "task_id", task.Id, "simulation_id", simulationID)
		execution.Error = runErr.Error()
		return execution, fmt.Errorf("workflow simulation failed: %w", runErr)
	}

	n.logger.Info("workflow simulation completed successfully", "task_id", task.Id, "simulation_id", simulationID, "steps", len(execution.Steps))
	return execution, nil
}

// triggerTypeStringToEnum converts trigger type string to enum
func triggerTypeStringToEnum(triggerType string) avsproto.TriggerType {
	switch triggerType {
	case NodeTypeBlockTrigger:
		return avsproto.TriggerType_TRIGGER_TYPE_BLOCK
	case NodeTypeFixedTimeTrigger:
		return avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME
	case NodeTypeCronTrigger:
		return avsproto.TriggerType_TRIGGER_TYPE_CRON
	case NodeTypeEventTrigger:
		return avsproto.TriggerType_TRIGGER_TYPE_EVENT
	case NodeTypeManualTrigger:
		return avsproto.TriggerType_TRIGGER_TYPE_MANUAL
	default:
		return avsproto.TriggerType_TRIGGER_TYPE_UNSPECIFIED
	}
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
		Items: []*avsproto.Execution{},
		PageInfo: &avsproto.PageInfo{
			StartCursor:     "",
			EndCursor:       "",
			HasPreviousPage: false,
			HasNextPage:     false,
		},
	}

	var before, after string
	var limitVal int64

	if payload != nil {
		before = payload.Before
		after = payload.After
		limitVal = payload.Limit
	}

	cursor, limit, err := SetupPagination(before, after, limitVal)
	if err != nil {
		return nil, err
	}

	total := 0
	var firstExecutionId, lastExecutionId string
	var hasMoreItems bool
	for i := len(executionKeys) - 1; i >= 0; i-- {
		key := executionKeys[i]

		executionUlid := ulid.MustParse(ExecutionIdFromStorageKey([]byte(key)))
		if !cursor.IsZero() {
			if (cursor.Direction == CursorDirectionNext && cursor.LessThanOrEqualUlid(executionUlid)) ||
				(cursor.Direction == CursorDirectionPrevious && cursor.LessThanUlid(executionUlid)) {
				continue
			}
		}

		executionValue, err := n.db.GetKey([]byte(key))
		if err != nil {
			continue
		}

		exec := &avsproto.Execution{}
		if err := protojson.Unmarshal(executionValue, exec); err == nil {
			// No longer need trigger type at execution level - it's in the first step
			executioResp.Items = append(executioResp.Items, exec)

			if total == 0 {
				firstExecutionId = exec.Id
			}
			lastExecutionId = exec.Id
			total += 1
		}
		// If we've processed more than the limit, we know there are more items
		if total >= limit {
			hasMoreItems = true
			break
		}
	}

	// Set pagination info
	if len(executioResp.Items) > 0 {
		// Always set cursors for the current page (GraphQL PageInfo convention)
		executioResp.PageInfo.StartCursor = CreateNextCursor(firstExecutionId)
		executioResp.PageInfo.EndCursor = CreateNextCursor(lastExecutionId)

		// Check if there are more items after the current page
		executioResp.PageInfo.HasNextPage = hasMoreItems

		// Check if there are items before the current page
		// This is true if we have a cursor and we're not at the beginning
		executioResp.PageInfo.HasPreviousPage = !cursor.IsZero() && cursor.Direction == CursorDirectionNext

		// For backward pagination, we need to check if there are items after
		if cursor.Direction == CursorDirectionPrevious {
			executioResp.PageInfo.HasNextPage = true // There are items after since we're going backwards
			// Check if there are more items before
			executioResp.PageInfo.HasPreviousPage = hasMoreItems
		}
	}
	return executioResp, nil
}

func (n *Engine) setExecutionStatusQueue(task *model.Task, executionID string) error {
	status := strconv.Itoa(int(avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING))
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

// GetExecution for a given task id and execution id
func (n *Engine) GetExecution(user *model.User, payload *avsproto.ExecutionReq) (*avsproto.Execution, error) {
	task, err := n.GetTask(user, payload.TaskId)
	if err != nil {
		return nil, err
	}

	rawExecution, err := n.db.GetKey(TaskExecutionKey(task, payload.ExecutionId))
	if err != nil {
		return nil, grpcstatus.Errorf(codes.NotFound, ExecutionNotFoundError)
	}

	exec := &avsproto.Execution{}
	err = protojson.Unmarshal(rawExecution, exec)
	if err != nil {
		return nil, grpcstatus.Errorf(codes.Code(avsproto.Error_TaskDataCorrupted), TaskStorageCorruptedError)
	}

	// No longer need trigger type at execution level - it's in the first step
	return exec, nil
}

func (n *Engine) GetExecutionStatus(user *model.User, payload *avsproto.ExecutionReq) (*avsproto.ExecutionStatusResp, error) {
	task, err := n.GetTask(user, payload.TaskId)
	if err != nil {
		return nil, err
	}

	// First check if execution is completed and stored
	rawExecution, err := n.db.GetKey(TaskExecutionKey(task, payload.ExecutionId))
	if err == nil {
		exec := &avsproto.Execution{}
		err = protojson.Unmarshal(rawExecution, exec)
		if err != nil {
			return nil, grpcstatus.Errorf(codes.Code(avsproto.Error_TaskDataCorrupted), TaskStorageCorruptedError)
		}

		if exec.Success {
			return &avsproto.ExecutionStatusResp{Status: avsproto.ExecutionStatus_EXECUTION_STATUS_COMPLETED}, nil
		} else {
			return &avsproto.ExecutionStatusResp{Status: avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED}, nil
		}
	}

	// Check if it's pending in queue
	status, err := n.getExecutionStatusFromQueue(task, payload.ExecutionId)
	if err != nil {
		return nil, grpcstatus.Errorf(codes.NotFound, ExecutionNotFoundError)
	}

	return &avsproto.ExecutionStatusResp{Status: *status}, nil
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
	// TaskStorageKey now needs task.Status which is Canceled
	taskJSON, err := task.ToJSON() // Re-serialize task with new status
	if err != nil {
		return false, fmt.Errorf("failed to serialize canceled task: %w", err)
	}
	updates[string(TaskStorageKey(task.Id, task.Status))] = taskJSON // Use new status for the key where it's stored
	updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", task.Status))

	if err = n.db.BatchWrite(updates); err == nil {
		// Delete the old record, only if oldStatus is different from new Status
		if oldStatus != task.Status {
			if delErr := n.db.Delete(TaskStorageKey(task.Id, oldStatus)); delErr != nil {
				n.logger.Error("failed to delete old task status entry", "error", delErr, "task_id", task.Id, "old_status", oldStatus)
				// Not returning error here as the main update was successful
			}
		}

		n.lock.Lock()
		delete(n.tasks, task.Id) // Remove from active tasks map
		n.lock.Unlock()
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

	// In original code, it returned true, nil even on error. Preserving that.
	return true, nil
}

// ListSecrets
func (n *Engine) ListSecrets(user *model.User, payload *avsproto.ListSecretsReq) (*avsproto.ListSecretsResp, error) {
	prefixes := []string{
		SecretStoragePrefix(user),
	}

	result := &avsproto.ListSecretsResp{
		Items: []*avsproto.Secret{},
		PageInfo: &avsproto.PageInfo{
			StartCursor:     "",
			EndCursor:       "",
			HasPreviousPage: false,
			HasNextPage:     false,
		},
	}

	secretKeys, err := n.db.ListKeysMulti(prefixes)
	if err != nil {
		return nil, err
	}

	sort.Strings(secretKeys)

	var before, after string
	var limitVal int64

	if payload != nil {
		before = payload.Before
		after = payload.After
		limitVal = payload.Limit
	}

	cursor, limit, err := SetupPagination(before, after, limitVal)
	if err != nil {
		return nil, err
	}

	total := 0
	var firstKey, lastKey string
	var hasMoreItems bool
	var processedCount int

	// Process keys that match the cursor criteria and stop when limit+1 is reached
	// We fetch limit+1 to determine if there are more pages without loading everything
	for _, k := range secretKeys {
		if !cursor.IsZero() {
			if (cursor.Direction == CursorDirectionNext && k <= cursor.Position) ||
				(cursor.Direction == CursorDirectionPrevious && k >= cursor.Position) {
				continue
			}
		}

		processedCount++

		// If we've processed more than the limit, we know there are more items
		if processedCount > limit {
			hasMoreItems = true
			break
		}

		secretWithNameOnly := SecretNameFromKey(k)
		item := &avsproto.Secret{
			Name:       secretWithNameOnly.Name,
			OrgId:      secretWithNameOnly.OrgID,
			WorkflowId: secretWithNameOnly.WorkflowID,
			// Always include scope for basic functionality
			Scope: "user", // Default scope, could be enhanced to read from storage
		}

		// Conditionally populate additional fields based on request parameters
		if payload != nil {
			if payload.IncludeTimestamps {
				// In a real implementation, these would be fetched from storage
				// For now, we'll skip timestamps since we don't have real data
				// item.CreatedAt = time.Now().Unix() // Would fetch from storage
				// item.UpdatedAt = time.Now().Unix() // Would fetch from storage
			}
			if payload.IncludeCreatedBy {
				item.CreatedBy = user.Address.Hex() // Would fetch from storage
			}
			if payload.IncludeDescription {
				item.Description = "" // Would fetch from storage
			}
		}

		result.Items = append(result.Items, item)

		if total == 0 {
			firstKey = k
		}
		lastKey = k
		total++
	}

	// Set pagination info
	if len(result.Items) > 0 {
		// Always set cursors for the current page (GraphQL PageInfo convention)
		result.PageInfo.StartCursor = CreateNextCursor(firstKey)
		result.PageInfo.EndCursor = CreateNextCursor(lastKey)

		// Check if there are more items after the current page
		result.PageInfo.HasNextPage = hasMoreItems

		// Check if there are items before the current page
		// This is true if we have a cursor and we're not at the beginning
		result.PageInfo.HasPreviousPage = !cursor.IsZero() && cursor.Direction == CursorDirectionNext

		// For backward pagination, we need to check if there are items after
		if cursor.Direction == CursorDirectionPrevious {
			result.PageInfo.HasNextPage = true // There are items after since we're going backwards
			// Check if there are more items before
			result.PageInfo.HasPreviousPage = hasMoreItems
		}
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
			if e, ok := r.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("panic recovered: %v", r)
			}
		}
	}()

	num, err = n.seq.Next()
	if err != nil { // Check error after first Next() call
		return "", err
	}
	if num == 0 { // This case might indicate an issue with sequence or its initialization if it persists
		n.logger.Warn("Sequence returned 0, attempting Next() again.")
		num, err = n.seq.Next()
		if err != nil {
			return "", err
		}
	}

	return strconv.FormatInt(int64(num), 10), nil
}

func (n *Engine) CanStreamCheck(address string) bool {
	// Only enable for our own operator first, once it's stable we will roll out to all
	return strings.EqualFold(address, "0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d") || strings.EqualFold(address, "0xc6b87cc9e85b07365b6abefff061f237f7cf7dc3")
}

// GetExecutionStats returns the number of workflows for the given addresses of smart wallets, or if no addresses are provided, it returns the total number of workflows belongs to the requested user
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

// Helper function to get map keys for logging
func getStringMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// enrichEventTriggerFromOperatorData fetches full event data from blockchain and enriches it with token metadata
func (n *Engine) enrichEventTriggerFromOperatorData(minimalEvmLog *avsproto.Evm_Log) (*avsproto.EventTrigger_Output, error) {
	if minimalEvmLog.TransactionHash == "" {
		return nil, fmt.Errorf("transaction hash is required for enrichment")
	}

	// Get RPC client (using the global rpcConn variable)
	if rpcConn == nil {
		return nil, fmt.Errorf("RPC client not available")
	}

	// Fetch transaction receipt to get the full event logs
	ctx := context.Background()
	receipt, err := rpcConn.TransactionReceipt(ctx, common.HexToHash(minimalEvmLog.TransactionHash))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch transaction receipt: %w", err)
	}

	// Find the specific log that matches the operator's data
	var targetLog *types.Log
	for _, log := range receipt.Logs {
		if uint32(log.Index) == minimalEvmLog.Index {
			targetLog = log
			break
		}
	}

	if targetLog == nil {
		return nil, fmt.Errorf("log with index %d not found in transaction %s",
			minimalEvmLog.Index, minimalEvmLog.TransactionHash)
	}

	// Create enriched EVM log with full data
	enrichedEvmLog := &avsproto.Evm_Log{
		Address:          targetLog.Address.Hex(),
		Topics:           make([]string, len(targetLog.Topics)),
		Data:             "0x" + common.Bytes2Hex(targetLog.Data),
		BlockNumber:      targetLog.BlockNumber,
		TransactionHash:  targetLog.TxHash.Hex(),
		TransactionIndex: uint32(targetLog.TxIndex),
		BlockHash:        targetLog.BlockHash.Hex(),
		Index:            uint32(targetLog.Index),
		Removed:          targetLog.Removed,
	}

	// Convert topics to string array
	for i, topic := range targetLog.Topics {
		enrichedEvmLog.Topics[i] = topic.Hex()
	}

	enrichedOutput := &avsproto.EventTrigger_Output{
		EvmLog: enrichedEvmLog,
	}

	// Check if this is a Transfer event and enrich with token metadata
	isTransferEvent := len(targetLog.Topics) > 0 &&
		targetLog.Topics[0].Hex() == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

	if isTransferEvent && len(targetLog.Topics) >= 3 {
		// Get block timestamp for transfer_log
		header, err := rpcConn.HeaderByNumber(ctx, big.NewInt(int64(targetLog.BlockNumber)))
		var blockTimestamp uint64
		if err == nil {
			blockTimestamp = header.Time * 1000 // Convert to milliseconds
		}

		// Extract from and to addresses from topics
		fromAddr := common.HexToAddress(targetLog.Topics[1].Hex()).Hex()
		toAddr := common.HexToAddress(targetLog.Topics[2].Hex()).Hex()
		value := "0x" + common.Bytes2Hex(targetLog.Data)

		transferLog := &avsproto.EventTrigger_TransferLogOutput{
			TokenName:        "",
			TokenSymbol:      "",
			TokenDecimals:    0,
			TransactionHash:  targetLog.TxHash.Hex(),
			Address:          targetLog.Address.Hex(),
			BlockNumber:      targetLog.BlockNumber,
			BlockTimestamp:   blockTimestamp,
			FromAddress:      fromAddr,
			ToAddress:        toAddr,
			Value:            value,
			ValueFormatted:   "",
			TransactionIndex: uint32(targetLog.TxIndex),
			LogIndex:         uint32(targetLog.Index),
		}

		// Enrich with token metadata
		if err := n.tokenEnrichmentService.EnrichTransferLog(enrichedEvmLog, transferLog); err != nil {
			n.logger.Warn("failed to enrich transfer log with token metadata", "error", err)
			// Continue without enrichment - partial data is better than no data
		}

		enrichedOutput.TransferLog = transferLog
	}

	return enrichedOutput, nil
}

// GetTokenMetadata handles the RPC for token metadata lookup
func (n *Engine) GetTokenMetadata(user *model.User, payload *avsproto.GetTokenMetadataReq) (*avsproto.GetTokenMetadataResp, error) {
	// Validate the address parameter
	if payload.Address == "" {
		return &avsproto.GetTokenMetadataResp{
			Found: false,
		}, grpcstatus.Errorf(codes.InvalidArgument, "token address is required")
	}

	// Check if address is a valid hex address
	if !common.IsHexAddress(payload.Address) {
		return &avsproto.GetTokenMetadataResp{
			Found: false,
		}, grpcstatus.Errorf(codes.InvalidArgument, "invalid token address format")
	}

	// Check if TokenEnrichmentService is available
	if n.tokenEnrichmentService == nil {
		return &avsproto.GetTokenMetadataResp{
			Found: false,
		}, grpcstatus.Errorf(codes.Unavailable, "token enrichment service not available")
	}

	// Try to get token metadata using the enrichment service
	metadata, err := n.tokenEnrichmentService.GetTokenMetadata(payload.Address)
	if err != nil {
		n.logger.Warn("Failed to get token metadata",
			"address", payload.Address,
			"user", user.Address.Hex(),
			"error", err)

		return &avsproto.GetTokenMetadataResp{
			Found: false,
		}, nil // Return not found instead of error for better UX
	}

	// Check if token was not found (nil metadata but no error)
	if metadata == nil {
		n.logger.Info("Token not found in whitelist or RPC",
			"address", payload.Address,
			"user", user.Address.Hex())

		return &avsproto.GetTokenMetadataResp{
			Found: false,
		}, nil
	}

	// Determine the source of the data
	source := metadata.Source // Use the source from the metadata

	// Return successful response with token metadata
	response := &avsproto.GetTokenMetadataResp{
		Found:  true,
		Source: source,
		Token: &avsproto.TokenMetadata{
			Address:  metadata.Address,
			Name:     metadata.Name,
			Symbol:   metadata.Symbol,
			Decimals: metadata.Decimals,
		},
	}

	n.logger.Info("Token metadata lookup successful",
		"address", payload.Address,
		"user", user.Address.Hex(),
		"tokenName", metadata.Name,
		"tokenSymbol", metadata.Symbol,
		"source", source)

	return response, nil
}
