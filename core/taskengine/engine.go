// The core package that manage and distribute and execute task
package taskengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	// Operator capabilities
	Capabilities *avsproto.SyncMessagesReq_Capabilities
}

type PendingNotification struct {
	TaskID    string
	Operation avsproto.MessageOp
	Timestamp time.Time
}

// The core datastructure of the task engine
type Engine struct {
	db     storage.Storage
	config *config.Config
	queue  *apqueue.Queue

	// maintain a list of active job that we have to synced to operators
	// only task triggers are sent to operator
	tasks            map[string]*model.Task
	lock             *sync.Mutex
	trackSyncedTasks map[string]*operatorState

	// operator stream management for real-time notifications
	operatorStreams map[string]avsproto.Node_SyncMessagesServer
	streamsMutex    *sync.RWMutex

	// Round-robin task assignment
	taskAssignments      map[string]string // taskID -> operatorAddress mapping
	assignmentRoundRobin int               // index for round-robin assignment
	assignmentMutex      *sync.RWMutex     // protects task assignments

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

	// Debouncing for operator approval logging
	lastApprovalLogTime map[string]time.Time
	approvalLogMutex    *sync.RWMutex

	// Batched operator notifications
	pendingNotifications map[string][]PendingNotification // operatorAddr -> list of notifications
	notificationMutex    *sync.RWMutex
	notificationTicker   *time.Ticker
}

// create a new task engine using given storage, config and queueu
func New(db storage.Storage, config *config.Config, queue *apqueue.Queue, logger sdklogging.Logger) *Engine {
	e := Engine{
		db:     db,
		config: config,
		queue:  queue,

		lock:                &sync.Mutex{},
		tasks:               make(map[string]*model.Task),
		trackSyncedTasks:    make(map[string]*operatorState),
		operatorStreams:     make(map[string]avsproto.Node_SyncMessagesServer),
		streamsMutex:        &sync.RWMutex{},
		taskAssignments:     make(map[string]string),
		assignmentMutex:     &sync.RWMutex{},
		lastApprovalLogTime: make(map[string]time.Time),
		approvalLogMutex:    &sync.RWMutex{},
		smartWalletConfig:   config.SmartWallet,
		shutdown:            false,

		// Initialize batched notifications
		pendingNotifications: make(map[string][]PendingNotification),
		notificationMutex:    &sync.RWMutex{},
		notificationTicker:   time.NewTicker(3 * time.Second), // Send batched notifications every 3 seconds

		logger: logger,
	}

	SetRpc(config.SmartWallet.EthRpcUrl)
	aa.SetFactoryAddress(config.SmartWallet.FactoryAddress)
	//SetWsRpc(config.SmartWallet.EthWsUrl)

	// Initialize TokenEnrichmentService
	// Always try to initialize, even without RPC, so we can serve whitelist data
	logger.Debug("initializing TokenEnrichmentService", "has_rpc", rpcConn != nil)
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

	// Send any remaining notifications before shutting down
	if n.notificationTicker != nil {
		n.sendBatchedNotifications()
		n.notificationTicker.Stop()
	}
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

	loadedCount := 0
	for _, item := range kvs {
		task := &model.Task{
			Task: &avsproto.Task{},
		}
		err := protojson.Unmarshal(item.Value, task)
		if err == nil {
			n.tasks[task.Id] = task
			loadedCount++
		} else {
			n.logger.Warn("Failed to unmarshal task during startup", "storage_key", string(item.Key), "error", err)
		}
	}

	n.logger.Info("🚀 Engine started successfully", "active_tasks_loaded", loadedCount)

	// Start the batch notification processor
	go n.processBatchedNotifications()

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

// validateNonZeroAddress validates that the factory address is not the zero address
// Returns an error if validation fails, nil if validation passes
func (n *Engine) validateNonZeroAddress(factoryAddr common.Address, methodName, ownerHex, salt string) error {
	if factoryAddr == (common.Address{}) {
		n.logger.Warn("Attempted to use zero address as factory for "+methodName, "owner", ownerHex, "salt", salt)
		return status.Errorf(codes.InvalidArgument, "Factory address cannot be the zero address")
	}
	return nil
}

// GetWallet is the gRPC handler for the GetWallet RPC.
// It uses the owner (from auth context), salt, and factory_address from payload to derive the wallet address.
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

	if err := n.validateNonZeroAddress(factoryAddr, "GetWallet", user.Address.Hex(), saltBig.String()); err != nil {
		return nil, err
	}

	derivedSenderAddress, err := aa.GetSenderAddressForFactory(rpcConn, user.Address, factoryAddr, saltBig)
	if err != nil || derivedSenderAddress == nil || *derivedSenderAddress == (common.Address{}) {
		var errMsg string
		if err != nil {
			errMsg = err.Error()
		}
		n.logger.Warn("Failed to derive sender address or derived address is nil or zero for GetWallet", "owner", user.Address.Hex(), "factory", factoryAddr.Hex(), "salt", saltBig.String(), "derived", derivedSenderAddress, "error", errMsg)
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

	if err := n.validateNonZeroAddress(factoryAddr, "SetWallet", owner.Hex(), payload.GetSalt()); err != nil {
		return nil, err
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
	n.tasks[task.Id] = task
	n.lock.Unlock()

	// Notify operators about the new task
	n.notifyOperatorsTaskOperation(task.Id, avsproto.MessageOp_MonitorTaskTrigger)

	return task, nil
}

func (n *Engine) StreamCheckToOperator(payload *avsproto.SyncMessagesReq, srv avsproto.Node_SyncMessagesServer) error {
	ticker := time.NewTicker(5 * time.Second)
	address := payload.Address

	n.logger.Info("open channel to stream check to operator", "operator", address)

	// Register this operator's stream for real-time notifications
	n.streamsMutex.Lock()
	n.operatorStreams[address] = srv
	n.streamsMutex.Unlock()

	if _, ok := n.trackSyncedTasks[address]; !ok {
		n.lock.Lock()
		n.trackSyncedTasks[address] = &operatorState{
			MonotonicClock: payload.MonotonicClock,
			TaskID:         map[string]bool{},
			Capabilities:   payload.Capabilities,
		}
		n.lock.Unlock()

		n.logger.Info("🔗 New operator connected with capabilities",
			"operator", address,
			"event_monitoring", payload.Capabilities.GetEventMonitoring(),
			"block_monitoring", payload.Capabilities.GetBlockMonitoring(),
			"time_monitoring", payload.Capabilities.GetTimeMonitoring())
	} else {
		// The operator has restated, but we haven't clean it state yet, reset now
		if payload.MonotonicClock > n.trackSyncedTasks[address].MonotonicClock {
			n.trackSyncedTasks[address].TaskID = map[string]bool{}
			n.trackSyncedTasks[address].MonotonicClock = payload.MonotonicClock
			n.trackSyncedTasks[address].Capabilities = payload.Capabilities

			n.logger.Info("🔄 Operator reconnected with updated capabilities",
				"operator", address,
				"event_monitoring", payload.Capabilities.GetEventMonitoring(),
				"block_monitoring", payload.Capabilities.GetBlockMonitoring(),
				"time_monitoring", payload.Capabilities.GetTimeMonitoring())
		}
	}

	// Reset the state if the operator disconnect
	defer func() {
		n.logger.Info("🔌 Operator disconnecting, cleaning up state", "operator", address)
		n.trackSyncedTasks[address].TaskID = map[string]bool{}
		n.trackSyncedTasks[address].MonotonicClock = 0

		// Unregister the operator's stream
		n.streamsMutex.Lock()
		delete(n.operatorStreams, address)
		n.streamsMutex.Unlock()

		// Reassign tasks that were assigned to this operator
		n.reassignOrphanedTasks()

		n.logger.Info("✅ Operator cleanup completed", "operator", address)
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

				// Use debounced logging to prevent spam (only log every 3 minutes per operator)
				if n.shouldLogApprovalMessage(address) {
					// Build dynamic approved operators list for logging
					var approvedList []string
					if len(n.config.ApprovedOperators) == 0 {
						// Use hardcoded list if no configuration
						approvedList = []string{"0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d", "0xc6b87cc9e85b07365b6abefff061f237f7cf7dc3", "0xa026265a0f01a6e1a19b04655519429df0a57c4e"}
					} else {
						// Use configured list
						for _, addr := range n.config.ApprovedOperators {
							approvedList = append(approvedList, addr.Hex())
						}
					}

					n.logger.Info("operator has not been approved to process task",
						"operator", address,
						"approved_operators", approvedList,
						"next_log_in", "3 minutes if still not approved")
				}
				continue
			}

			// Removed excessive debug logging that was happening every 5 seconds
			// Only log when there are actual tasks to send or state changes

			// Reassign orphaned tasks when operators connect/disconnect
			n.reassignOrphanedTasks()

			// Aggregate tasks for this operator to reduce logging and improve efficiency
			var tasksToStream []*model.Task
			var tasksByTriggerType = make(map[string]int) // Count tasks by trigger type
			var newAssignments []string                   // Track new task assignments for this operator

			for _, task := range n.tasks {
				if _, ok := n.trackSyncedTasks[address].TaskID[task.Id]; ok {
					continue
				}

				// Check if this operator is assigned to handle this task
				assignedOperator := n.assignTaskToOperator(task)
				if assignedOperator != address {
					// This task is assigned to a different operator
					continue
				}

				// Track this as a new assignment
				newAssignments = append(newAssignments, task.Id)

				// Check if operator supports this trigger type
				if !n.supportsTaskTrigger(address, task) {
					n.logger.Info("⚠️ Skipping task - operator doesn't support trigger type",
						"task_id", task.Id,
						"operator", address,
						"trigger_type", task.Trigger.String())
					continue
				}

				tasksToStream = append(tasksToStream, task)
				triggerTypeName := task.Trigger.String()
				tasksByTriggerType[triggerTypeName]++
			}

			// Log aggregated task assignments per operator
			if len(newAssignments) > 0 {
				n.logger.Info("🔄 Task assignments for operator",
					"operator", address,
					"operation", "MonitorTaskTrigger",
					"assigned_task_ids", newAssignments,
					"total_assignments", len(newAssignments))
			}

			// Stream all tasks and log aggregated results
			if len(tasksToStream) > 0 {
				successCount := 0
				for _, task := range tasksToStream {
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

					if err := srv.Send(&resp); err != nil {
						// return error to cause client to establish re-connect the connection
						n.logger.Error("error sending check to operator",
							"task_id", task.Id,
							"operator", payload.Address,
							"error", err,
							"error_type", fmt.Sprintf("%T", err),
							"grpc_status", grpcstatus.Code(err).String())
						return fmt.Errorf("cannot send data back to grpc channel: %w", err)
					}

					n.lock.Lock()
					n.trackSyncedTasks[address].TaskID[task.Id] = true
					n.lock.Unlock()
					successCount++
				}

				// Log aggregated results instead of individual tasks
				n.logger.Info("📤 Streamed tasks to operator",
					"operator", payload.Address,
					"total_tasks", len(tasksToStream),
					"successful", successCount,
					"task_breakdown", tasksByTriggerType)
			}
		}
	}
}

// notifyOperatorsTaskOperation queues notifications for batched sending to operators
// This method is non-blocking and batches notifications for efficiency
func (n *Engine) notifyOperatorsTaskOperation(taskID string, operation avsproto.MessageOp) {
	n.notificationMutex.Lock()
	defer n.notificationMutex.Unlock()

	// Find operators that were tracking this task
	for operatorAddr, operatorState := range n.trackSyncedTasks {
		if operatorState != nil {
			if _, wasTracked := operatorState.TaskID[taskID]; wasTracked {
				// Add to pending notifications for this operator
				notification := PendingNotification{
					TaskID:    taskID,
					Operation: operation,
					Timestamp: time.Now(),
				}
				n.pendingNotifications[operatorAddr] = append(n.pendingNotifications[operatorAddr], notification)
			}
		}
	}

	n.logger.Debug("📢 Queued notification for batching", "task_id", taskID, "operation", operation.String())
}

// processBatchedNotifications sends batched notifications to operators periodically
func (n *Engine) processBatchedNotifications() {
	defer n.notificationTicker.Stop()

	for {
		select {
		case <-n.notificationTicker.C:
			n.sendBatchedNotifications()
		case <-time.After(1 * time.Minute): // Safety check for shutdown
			if n.shutdown {
				n.logger.Info("🔄 Batch notification processor shutting down")
				return
			}
		}
	}
}

// sendBatchedNotifications sends accumulated notifications to operators in batches
func (n *Engine) sendBatchedNotifications() {
	n.notificationMutex.Lock()

	// Take a snapshot of pending notifications and clear the pending map
	currentBatch := make(map[string][]PendingNotification)
	for operatorAddr, notifications := range n.pendingNotifications {
		if len(notifications) > 0 {
			currentBatch[operatorAddr] = append([]PendingNotification{}, notifications...)
		}
	}
	// Clear pending notifications
	n.pendingNotifications = make(map[string][]PendingNotification)
	n.notificationMutex.Unlock()

	if len(currentBatch) == 0 {
		return // Nothing to send
	}

	// Get current operator streams
	n.streamsMutex.RLock()
	operatorStreams := make(map[string]avsproto.Node_SyncMessagesServer)
	for operatorAddr, stream := range n.operatorStreams {
		operatorStreams[operatorAddr] = stream
	}
	n.streamsMutex.RUnlock()

	// Send notifications in parallel to each operator
	var totalNotifications int
	var operatorsNotified int64
	operationCounts := make(map[string]int) // Track counts by operation type

	for operatorAddr, notifications := range currentBatch {
		totalNotifications += len(notifications)

		// Count operations by type
		for _, notification := range notifications {
			operationCounts[notification.Operation.String()]++
		}

		if stream, exists := operatorStreams[operatorAddr]; exists {
			go func(addr string, s avsproto.Node_SyncMessagesServer, notifs []PendingNotification) {
				successCount := 0
				operatorOperations := make(map[string][]string) // operation -> task_ids

				for _, notification := range notifs {
					resp := avsproto.SyncMessagesResp{
						Id: notification.TaskID,
						Op: notification.Operation,
						TaskMetadata: &avsproto.SyncMessagesResp_TaskMetadata{
							TaskId: notification.TaskID,
						},
					}

					// Use timeout for each individual notification
					done := make(chan error, 1)
					go func() {
						done <- s.Send(&resp)
					}()

					select {
					case err := <-done:
						if err != nil {
							n.logger.Debug("Failed to send batched notification",
								"operator", addr,
								"task_id", notification.TaskID,
								"operation", notification.Operation.String(),
								"error", err)
						} else {
							successCount++
							// Track successful operations by type
							operatorOperations[notification.Operation.String()] = append(
								operatorOperations[notification.Operation.String()],
								notification.TaskID)

							// Remove the task from the operator's tracking state for delete/cancel operations
							if notification.Operation == avsproto.MessageOp_CancelTask ||
								notification.Operation == avsproto.MessageOp_DeleteTask {
								if state, exists := n.trackSyncedTasks[addr]; exists {
									delete(state.TaskID, notification.TaskID)
								}
							}
						}
					case <-time.After(500 * time.Millisecond):
						n.logger.Debug("Timeout sending batched notification",
							"operator", addr,
							"task_id", notification.TaskID,
							"operation", notification.Operation.String())
					}
				}

				if successCount > 0 {
					atomic.AddInt64(&operatorsNotified, 1)
					// Log operator-centric notifications with operation breakdown
					for operation, taskIDs := range operatorOperations {
						n.logger.Info("📤 Batched notifications sent to operator",
							"operator", addr,
							"operation", operation,
							"task_ids", taskIDs,
							"total_notifications", len(taskIDs))
					}
				}
			}(operatorAddr, stream, notifications)
		} else {
			n.logger.Debug("Operator stream not available for batched notifications", "operator", operatorAddr)
		}
	}

	// Log aggregated results with operation breakdown
	if totalNotifications > 0 {
		// Give goroutines time to complete, then log summary
		go func() {
			time.Sleep(1 * time.Second)
			notifiedCount := atomic.LoadInt64(&operatorsNotified)
			if notifiedCount > 0 {
				n.logger.Info("📤 Batch notification summary",
					"total_notifications", totalNotifications,
					"operators_notified", notifiedCount,
					"operation_breakdown", operationCounts)
			}
		}()
	}
}

// TODO: Merge and verify from multiple operators
func (n *Engine) AggregateChecksResult(address string, payload *avsproto.NotifyTriggersReq) error {
	_, err := n.AggregateChecksResultWithState(address, payload)
	return err
}

// AggregateChecksResultWithState processes operator trigger notifications and returns execution state info
func (n *Engine) AggregateChecksResultWithState(address string, payload *avsproto.NotifyTriggersReq) (*ExecutionState, error) {
	n.lock.Lock()
	n.logger.Debug("processing aggregator check hit", "operator", address, "task_id", payload.TaskId)

	if state, exists := n.trackSyncedTasks[address]; exists {
		state.TaskID[payload.TaskId] = true
	}

	n.logger.Debug("processed aggregator check hit", "operator", address, "task_id", payload.TaskId)
	n.lock.Unlock()

	// Get task information to determine execution state
	task, exists := n.tasks[payload.TaskId]
	if !exists {
		// Task not found in memory - this could indicate a synchronization issue
		// Try to load the task from database as a fallback
		n.logger.Warn("Task not found in memory, attempting database lookup",
			"task_id", payload.TaskId,
			"operator", address,
			"memory_task_count", len(n.tasks))

		dbTask, dbErr := n.GetTaskByID(payload.TaskId)
		if dbErr != nil {
			n.logger.Error("Task not found in database either",
				"task_id", payload.TaskId,
				"operator", address,
				"db_error", dbErr)
			return &ExecutionState{
				RemainingExecutions: 0,
				TaskStillActive:     false,
				Status:              "not_found",
				Message:             "Task not found",
			}, fmt.Errorf("task %s not found", payload.TaskId)
		}

		// Task found in database but not in memory - add it to memory and continue
		n.lock.Lock()
		n.tasks[dbTask.Id] = dbTask
		n.lock.Unlock()
		task = dbTask

		n.logger.Info("Task recovered from database and added to memory",
			"task_id", payload.TaskId,
			"operator", address,
			"task_status", task.Status)
	}

	// Check if task is still runnable
	if !task.IsRunable() {
		remainingExecutions := int64(0)
		status := "exhausted"
		message := "Task has reached execution limit or expired"

		if task.MaxExecution > 0 && task.ExecutionCount >= task.MaxExecution {
			status = "exhausted"
			message = fmt.Sprintf("Task has reached maximum executions (%d/%d)", task.ExecutionCount, task.MaxExecution)
		} else if task.ExpiredAt > 0 && time.Unix(task.ExpiredAt/1000, 0).Before(time.Now()) {
			status = "expired"
			message = "Task has expired"
		} else if task.StartAt > 0 && time.Now().UnixMilli() < task.StartAt {
			status = "not_started"
			message = "Task has not reached start time"
		}

		n.logger.Info("🛑 Task no longer runnable, will inform operator to stop monitoring",
			"task_id", payload.TaskId,
			"status", status,
			"execution_count", task.ExecutionCount,
			"max_execution", task.MaxExecution)

		return &ExecutionState{
			RemainingExecutions: remainingExecutions,
			TaskStillActive:     false,
			Status:              status,
			Message:             message,
		}, nil
	}

	// Task is still active, process the trigger
	// Create trigger data
	triggerData := &TriggerData{
		Type:   payload.TriggerType,
		Output: ExtractTriggerOutput(payload.TriggerOutput),
	}

	// Enrich EventTrigger output if TokenEnrichmentService is available and it's a Transfer event
	if payload.TriggerType == avsproto.TriggerType_TRIGGER_TYPE_EVENT {
		n.logger.Debug("processing event trigger",
			"task_id", payload.TaskId,
			"has_token_service", n.tokenEnrichmentService != nil)

		if n.tokenEnrichmentService != nil {
			if eventOutput := triggerData.Output.(*avsproto.EventTrigger_Output); eventOutput != nil {
				if evmLog := eventOutput.GetEvmLog(); evmLog != nil {
					n.logger.Debug("enriching EventTrigger output from operator",
						"task_id", payload.TaskId,
						"tx_hash", evmLog.TransactionHash,
						"block_number", evmLog.BlockNumber,
						"log_index", evmLog.Index,
						"address", evmLog.Address,
						"topics_count", len(evmLog.Topics),
						"data_length", len(evmLog.Data))

					// Fetch full event data from the blockchain using the minimal data from operator
					if enrichedEventOutput, err := n.enrichEventTriggerFromOperatorData(evmLog); err == nil {
						// Replace the minimal event output with the enriched one
						triggerData.Output = enrichedEventOutput
						n.logger.Debug("successfully enriched EventTrigger output",
							"task_id", payload.TaskId,
							"has_transfer_log", enrichedEventOutput.GetTransferLog() != nil,
							"has_evm_log", enrichedEventOutput.GetEvmLog() != nil)
					} else {
						n.logger.Warn("failed to enrich EventTrigger output, using minimal data",
							"task_id", payload.TaskId,
							"error", err)
					}
				} else {
					n.logger.Debug("EventTrigger output has no EvmLog data",
						"task_id", payload.TaskId,
						"has_transfer_log", eventOutput.GetTransferLog() != nil)
				}
			} else {
				n.logger.Debug("EventTrigger output is nil",
					"task_id", payload.TaskId)
			}
		} else {
			n.logger.Debug("TokenEnrichmentService not available for event enrichment",
				"task_id", payload.TaskId)
		}
	}

	queueTaskData := QueueExecutionData{
		TriggerType:   triggerData.Type,
		TriggerOutput: triggerData.Output,
		ExecutionID:   ulid.Make().String(),
	}

	// For event triggers, if we have enriched data, convert it to a map format that survives JSON serialization
	if triggerData.Type == avsproto.TriggerType_TRIGGER_TYPE_EVENT {
		if eventOutput, ok := triggerData.Output.(*avsproto.EventTrigger_Output); ok {
			// Convert the enriched protobuf data to a map that will survive JSON serialization
			enrichedDataMap := buildTriggerDataMapFromProtobuf(triggerData.Type, eventOutput, n.logger)

			// Store the enriched data as a map instead of protobuf structure
			// This ensures the enriched data survives JSON serialization/deserialization
			queueTaskData.TriggerOutput = map[string]interface{}{
				"enriched_data": enrichedDataMap,
				"trigger_type":  triggerData.Type.String(),
			}

			n.logger.Debug("stored enriched event trigger data for queue execution",
				"task_id", payload.TaskId,
				"has_token_symbol", enrichedDataMap["tokenSymbol"] != nil,
				"has_value_formatted", enrichedDataMap["valueFormatted"] != nil)
		}
	}

	data, err := json.Marshal(queueTaskData)
	if err != nil {
		n.logger.Error("error serialize trigger to json", err)
		return &ExecutionState{
			RemainingExecutions: 0,
			TaskStillActive:     false,
			Status:              "error",
			Message:             "Failed to process trigger",
		}, err
	}

	if _, err := n.queue.Enqueue(JobTypeExecuteTask, payload.TaskId, data); err != nil {
		n.logger.Error("failed to enqueue task", "error", err, "task_id", payload.TaskId)
		return &ExecutionState{
			RemainingExecutions: 0,
			TaskStillActive:     false,
			Status:              "error",
			Message:             "Failed to queue execution",
		}, err
	}

	// Calculate remaining executions
	var remainingExecutions int64
	if task.MaxExecution == 0 {
		remainingExecutions = -1 // Unlimited executions
	} else {
		remainingExecutions = int64(task.MaxExecution - task.ExecutionCount - 1) // -1 because we just queued one
		if remainingExecutions < 0 {
			remainingExecutions = 0
		}
	}

	n.logger.Debug("task queued for execution",
		"task_id", payload.TaskId,
		"remaining_executions", remainingExecutions)

	return &ExecutionState{
		RemainingExecutions: remainingExecutions,
		TaskStillActive:     true,
		Status:              "active",
		Message:             "Trigger processed successfully",
	}, nil
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
	n.logger.Debug("enqueue task into the queue system", "task_id", payload.TaskId)

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

	// Step 1: Start timing BEFORE trigger execution (consistent with node timing)
	triggerStartTime := time.Now()

	triggerOutput, err := n.runTriggerImmediately(triggerTypeStr, triggerConfig, inputVariables)
	if err != nil {
		return nil, fmt.Errorf("failed to simulate trigger: %w", err)
	}

	// Step 2: Capture trigger end time AFTER trigger execution completes
	triggerEndTime := time.Now()

	// Step 3: Create QueueExecutionData similar to regular task execution
	simulationID := ulid.Make().String()

	// Convert trigger output to proper protobuf structure using shared functions
	var triggerOutputProto interface{}
	switch trigger.Type {
	case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
		triggerOutputProto = buildManualTriggerOutput(triggerOutput)
	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
		triggerOutputProto = buildFixedTimeTriggerOutput(triggerOutput)
	case avsproto.TriggerType_TRIGGER_TYPE_CRON:
		triggerOutputProto = buildCronTriggerOutput(triggerOutput)
	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		triggerOutputProto = buildBlockTriggerOutput(triggerOutput)
	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		triggerOutputProto = buildEventTriggerOutput(triggerOutput)
	default:
		// For unknown trigger types, create a manual trigger as fallback
		triggerOutputProto = buildManualTriggerOutput(triggerOutput)
	}

	queueData := &QueueExecutionData{
		TriggerType:   trigger.Type,
		TriggerOutput: triggerOutputProto,
		ExecutionID:   simulationID,
	}

	// Step 4: Load secrets for the task
	secrets, err := LoadSecretForTask(n.db, task)
	if err != nil {
		n.logger.Warn("Failed to load secrets for workflow simulation", "error", err, "task_id", task.Id)
		// Don't fail the simulation, just use empty secrets
		secrets = make(map[string]string)
	}

	// Step 5: Create VM with simulated trigger data (similar to RunTask)
	triggerReason := GetTriggerReasonOrDefault(queueData, task.Id, n.logger)
	vm, err := NewVMWithData(task, triggerReason, n.smartWalletConfig, secrets)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM for simulation: %w", err)
	}

	vm.WithLogger(n.logger).WithDb(n.db)

	// Add input variables to VM for template processing
	// Apply dual-access mapping to enable both camelCase and snake_case field access
	for key, value := range inputVariables {
		// Apply dual-access mapping if the value is a map with a "data" field
		if valueMap, ok := value.(map[string]interface{}); ok {
			if dataField, hasData := valueMap["data"]; hasData {
				if dataMap, isDataMap := dataField.(map[string]interface{}); isDataMap {
					// Apply dual-access mapping to the data field
					dualAccessData := CreateDualAccessMap(dataMap)
					// Create a new map with the dual-access data
					processedValue := make(map[string]interface{})
					for k, v := range valueMap {
						if k == "data" {
							processedValue[k] = dualAccessData
						} else {
							processedValue[k] = v
						}
					}
					vm.AddVar(key, processedValue)
				} else {
					vm.AddVar(key, value)
				}
			} else {
				vm.AddVar(key, value)
			}
		} else {
			vm.AddVar(key, value)
		}
	}

	// Step 6: Add trigger data as "trigger" variable for convenient access in JavaScript
	// This ensures scripts can access trigger.data regardless of the trigger's name using shared function
	triggerDataMap := buildTriggerDataMap(triggerReason.Type, triggerOutput)

	// Add the trigger variable with the actual trigger name for JavaScript access
	vm.AddVar(sanitizeTriggerNameForJS(trigger.GetName()), map[string]any{"data": triggerDataMap})

	// Extract and add trigger input data if available
	triggerInputData := ExtractTriggerInputData(trigger)
	if triggerInputData != nil {
		// Get existing trigger variable and add input data
		triggerVarName := sanitizeTriggerNameForJS(trigger.GetName())
		vm.mu.Lock()
		existingTriggerVar := vm.vars[triggerVarName]
		if existingMap, ok := existingTriggerVar.(map[string]any); ok {
			// Apply dual-access mapping to trigger input data
			processedTriggerInput := CreateDualAccessMap(triggerInputData)
			existingMap["input"] = processedTriggerInput
			vm.vars[triggerVarName] = existingMap
		} else {
			// Create new trigger variable with both data and input
			processedTriggerInput := CreateDualAccessMap(triggerInputData)
			vm.vars[triggerVarName] = map[string]any{
				"data":  triggerDataMap,
				"input": processedTriggerInput,
			}
		}
		vm.mu.Unlock()
	}

	// Step 7: Compile the workflow
	if err = vm.Compile(); err != nil {
		return nil, fmt.Errorf("failed to compile workflow for simulation: %w", err)
	}

	// Step 8: Create and add a trigger execution step with ACTUAL timing
	// Convert inputVariables keys to trigger inputs
	triggerInputs := make([]string, 0, len(inputVariables))
	for key := range inputVariables {
		triggerInputs = append(triggerInputs, key)
	}

	triggerStep := &avsproto.Execution_Step{
		Id:      task.Trigger.Id, // Use new 'id' field
		Success: true,
		Error:   "",
		StartAt: triggerStartTime.UnixMilli(), // Use actual trigger start time
		EndAt:   triggerEndTime.UnixMilli(),   // Use actual trigger end time
		Log:     fmt.Sprintf("Simulated trigger: %s executed successfully", task.Trigger.Name),
		Inputs:  triggerInputs,                  // Use inputVariables keys as trigger inputs
		Type:    queueData.TriggerType.String(), // Use trigger type as string
		Name:    task.Trigger.Name,              // Use new 'name' field
		Input:   task.Trigger.Input,             // Include trigger input data for debugging
	}

	// Set trigger output data in the step using shared function
	triggerStep.OutputData = buildExecutionStepOutputData(queueData.TriggerType, triggerOutputProto)

	// Add trigger step to execution logs
	vm.ExecutionLogs = append(vm.ExecutionLogs, triggerStep)

	// Step 8.5: Update VM trigger variable with actual execution results
	// This ensures subsequent nodes can access the trigger's actual output via eventTrigger.data
	actualTriggerDataMap := buildTriggerDataMapFromProtobuf(queueData.TriggerType, triggerOutputProto, n.logger)
	vm.AddVar(sanitizeTriggerNameForJS(trigger.GetName()), map[string]any{"data": actualTriggerDataMap})

	// Step 9: Run the workflow nodes
	runErr := vm.Run()
	nodeEndTime := time.Now()

	// Step 10: Analyze execution results from all steps
	executionSuccess, executionError, failedStepCount := vm.AnalyzeExecutionResult()

	// Create execution result with proper success/error analysis
	execution := &avsproto.Execution{
		Id:      simulationID,
		StartAt: triggerStartTime.UnixMilli(), // Start with trigger start time
		EndAt:   nodeEndTime.UnixMilli(),      // End with node completion time
		Success: executionSuccess,             // Based on analysis of all steps
		Error:   executionError,               // Comprehensive error message from failed steps
		Steps:   vm.ExecutionLogs,             // Now contains both trigger and node steps (including failed ones)
	}

	if !executionSuccess {
		// Clean up error message to avoid stack traces in logs
		cleanErrorMsg := executionError
		// Use regex to remove stack-trace lines for cleaner logging (common in JS errors)
		stackTraceRegex := regexp.MustCompile(`(?m)^\s*at .*$`)
		cleanErrorMsg = stackTraceRegex.ReplaceAllString(cleanErrorMsg, "")
		// Clean up any extra whitespace left behind
		cleanErrorMsg = strings.TrimSpace(cleanErrorMsg)

		n.logger.Error("workflow simulation completed with failures",
			"error", cleanErrorMsg,
			"task_id", task.Id,
			"simulation_id", simulationID,
			"failed_steps", failedStepCount,
			"total_steps", len(vm.ExecutionLogs))
		// Don't return error here - we want to return the execution result with failed steps
		return execution, nil
	}

	if runErr != nil {
		// This should not happen if AnalyzeExecutionResult is working correctly,
		// but handle it as a fallback for VM-level errors
		n.logger.Error("workflow simulation had VM-level error", "vm_error", runErr, "task_id", task.Id, "simulation_id", simulationID)
		if execution.Error == "" {
			execution.Error = fmt.Sprintf("VM execution error: %s", runErr.Error())
			execution.Success = false
		}
		return execution, nil
	}

	n.logger.Info("workflow simulation completed successfully", "task_id", task.Id, "simulation_id", simulationID, "steps", len(execution.Steps))
	return execution, nil
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
	n.logger.Info("🔄 Starting delete task operation", "task_id", taskID, "user", user.Address.String())

	task, err := n.GetTask(user, taskID)
	if err != nil {
		n.logger.Info("❌ Task not found for deletion", "task_id", taskID, "error", err)
		return false, grpcstatus.Errorf(codes.NotFound, TaskNotFoundError)
	}

	n.logger.Info("✅ Retrieved task for deletion", "task_id", taskID, "status", task.Status)

	if task.Status == avsproto.TaskStatus_Executing {
		n.logger.Info("❌ Cannot delete executing task", "task_id", taskID, "status", task.Status)
		return false, fmt.Errorf("Only non executing task can be deleted")
	}

	n.logger.Info("🗑️ Deleting task storage", "task_id", taskID)
	if err := n.db.Delete(TaskStorageKey(task.Id, task.Status)); err != nil {
		n.logger.Error("failed to delete task storage", "error", err, "task_id", task.Id)
		return false, fmt.Errorf("failed to delete task: %w", err)
	}

	n.logger.Info("🗑️ Deleting task user key", "task_id", taskID)
	if err := n.db.Delete(TaskUserKey(task)); err != nil {
		n.logger.Error("failed to delete task user key", "error", err, "task_id", task.Id)
		return false, fmt.Errorf("failed to delete task user key: %w", err)
	}

	n.logger.Info("📢 Starting operator notifications", "task_id", taskID)
	n.notifyOperatorsTaskOperation(taskID, avsproto.MessageOp_DeleteTask)
	n.logger.Info("✅ Delete task operation completed", "task_id", taskID)

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

	n.notifyOperatorsTaskOperation(taskID, avsproto.MessageOp_CancelTask)

	return true, nil
}

// CancelTask cancels a task by ID without user authentication (for internal use like overload alerts)
func (n *Engine) CancelTask(taskID string) (bool, error) {
	n.lock.Lock()
	task, exists := n.tasks[taskID]
	n.lock.Unlock()

	if !exists {
		n.logger.Warn("Task not found for cancellation", "task_id", taskID)
		return false, nil
	}

	if task.Status != avsproto.TaskStatus_Active {
		n.logger.Info("Task is not active, cannot cancel", "task_id", taskID, "status", task.Status)
		return false, nil
	}

	updates := map[string][]byte{}
	oldStatus := task.Status
	task.SetCanceled()

	taskJSON, err := task.ToJSON()
	if err != nil {
		return false, fmt.Errorf("failed to serialize canceled task: %w", err)
	}

	updates[string(TaskStorageKey(task.Id, task.Status))] = taskJSON
	updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", task.Status))

	if err = n.db.BatchWrite(updates); err == nil {
		// Delete the old record
		if oldStatus != task.Status {
			if delErr := n.db.Delete(TaskStorageKey(task.Id, oldStatus)); delErr != nil {
				n.logger.Error("failed to delete old task status entry", "error", delErr, "task_id", task.Id, "old_status", oldStatus)
			}
		}

		n.lock.Lock()
		delete(n.tasks, task.Id) // Remove from active tasks map
		n.lock.Unlock()
	} else {
		return false, err
	}

	n.notifyOperatorsTaskOperation(taskID, avsproto.MessageOp_CancelTask)
	n.logger.Info("Task cancelled due to system alert", "task_id", taskID)

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
	// If no approved operators configured, use default hardcoded list for backward compatibility
	if len(n.config.ApprovedOperators) == 0 {
		n.logger.Debug("Using hardcoded operator approval list", "operator", address)
		return strings.EqualFold(address, "0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d") ||
			strings.EqualFold(address, "0xc6b87cc9e85b07365b6abefff061f237f7cf7dc3") ||
			strings.EqualFold(address, "0xa026265a0f01a6e1a19b04655519429df0a57c4e")
	}

	// Check against configured approved operators (case-insensitive)
	for _, approvedAddr := range n.config.ApprovedOperators {
		if strings.EqualFold(address, approvedAddr.Hex()) {
			n.logger.Debug("Operator approved via configuration", "operator", address)
			return true
		}
	}

	n.logger.Debug("Operator not found in approved list", "operator", address, "approved_count", len(n.config.ApprovedOperators))
	return false
}

// shouldLogApprovalMessage checks if we should log the approval message for this operator
// Returns true if more than 3 minutes have passed since the last log for this operator
func (n *Engine) shouldLogApprovalMessage(address string) bool {
	n.approvalLogMutex.Lock()
	defer n.approvalLogMutex.Unlock()

	lastLogTime, exists := n.lastApprovalLogTime[address]
	now := time.Now()

	// Log if no previous log or more than 3 minutes have passed
	if !exists || now.Sub(lastLogTime) >= 3*time.Minute {
		n.lastApprovalLogTime[address] = now
		return true
	}

	return false
}

// supportsTaskTrigger checks if an operator supports a specific trigger type
func (n *Engine) supportsTaskTrigger(operatorAddr string, task *model.Task) bool {
	n.lock.Lock()
	defer n.lock.Unlock()

	operatorState, exists := n.trackSyncedTasks[operatorAddr]
	if !exists || operatorState.Capabilities == nil {
		// If no capabilities specified, assume operator supports all trigger types (backward compatibility)
		return true
	}

	capabilities := operatorState.Capabilities

	// Check trigger type support
	if task.Trigger.GetEvent() != nil {
		return capabilities.EventMonitoring
	}
	if task.Trigger.GetBlock() != nil {
		return capabilities.BlockMonitoring
	}
	if task.Trigger.GetCron() != nil || task.Trigger.GetFixedTime() != nil {
		return capabilities.TimeMonitoring
	}

	// Default to true for unknown trigger types
	return true
}

// getEligibleOperators returns operators that support the given task's trigger type
func (n *Engine) getEligibleOperators(task *model.Task) []string {
	n.streamsMutex.RLock()
	defer n.streamsMutex.RUnlock()

	var eligible []string
	for operatorAddr := range n.operatorStreams {
		if n.CanStreamCheck(operatorAddr) && n.supportsTaskTrigger(operatorAddr, task) {
			eligible = append(eligible, operatorAddr)
		}
	}

	return eligible
}

// assignTaskToOperator assigns a task to an operator using round-robin
func (n *Engine) assignTaskToOperator(task *model.Task) string {
	eligible := n.getEligibleOperators(task)
	if len(eligible) == 0 {
		return ""
	}

	n.assignmentMutex.Lock()
	defer n.assignmentMutex.Unlock()

	// Check if task is already assigned
	if assignedOperator, exists := n.taskAssignments[task.Id]; exists {
		// Verify the assigned operator is still eligible and online
		for _, op := range eligible {
			if op == assignedOperator {
				return assignedOperator
			}
		}
		// Assigned operator is no longer eligible, remove assignment
		delete(n.taskAssignments, task.Id)
	}

	// Round-robin assignment
	selectedOperator := eligible[n.assignmentRoundRobin%len(eligible)]
	n.assignmentRoundRobin++

	// Store assignment
	n.taskAssignments[task.Id] = selectedOperator

	n.logger.Debug("🔄 Round-robin task assignment",
		"task_id", task.Id,
		"assigned_operator", selectedOperator,
		"eligible_operators", len(eligible),
		"round_robin_index", n.assignmentRoundRobin-1)

	return selectedOperator
}

// reassignOrphanedTasks reassigns tasks from disconnected operators
func (n *Engine) reassignOrphanedTasks() {
	n.assignmentMutex.Lock()
	defer n.assignmentMutex.Unlock()

	n.streamsMutex.RLock()
	activeOperators := make(map[string]bool)
	for operatorAddr := range n.operatorStreams {
		activeOperators[operatorAddr] = true
	}
	n.streamsMutex.RUnlock()

	var orphanedTasks []string
	for taskID, operatorAddr := range n.taskAssignments {
		if !activeOperators[operatorAddr] {
			orphanedTasks = append(orphanedTasks, taskID)
			delete(n.taskAssignments, taskID)
		}
	}

	if len(orphanedTasks) > 0 {
		n.logger.Info("🔄 Reassigning orphaned tasks",
			"orphaned_count", len(orphanedTasks),
			"active_operators", len(activeOperators))

		// Track reassignments by operator
		reassignmentsByOperator := make(map[string][]string)

		// Reassign orphaned tasks
		for _, taskID := range orphanedTasks {
			if task, exists := n.tasks[taskID]; exists {
				assignedOperator := n.assignTaskToOperator(task)
				reassignmentsByOperator[assignedOperator] = append(reassignmentsByOperator[assignedOperator], taskID)
			}
		}

		// Log aggregated reassignments per operator
		for operatorAddr, taskIDs := range reassignmentsByOperator {
			n.logger.Info("🔄 Reassigned tasks to operator",
				"operator", operatorAddr,
				"operation", "MonitorTaskTrigger",
				"reassigned_task_ids", taskIDs,
				"total_reassignments", len(taskIDs))
		}
	}
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
		n.logger.Debug("enrichment failed: transaction hash is empty")
		return nil, fmt.Errorf("transaction hash is required for enrichment")
	}

	// Get RPC client (using the global rpcConn variable)
	if rpcConn == nil {
		n.logger.Debug("enrichment failed: RPC client not available")
		return nil, fmt.Errorf("RPC client not available")
	}

	n.logger.Debug("starting event enrichment",
		"tx_hash", minimalEvmLog.TransactionHash,
		"log_index", minimalEvmLog.Index,
		"block_number", minimalEvmLog.BlockNumber)

	// Fetch transaction receipt to get the full event logs
	ctx := context.Background()
	receipt, err := rpcConn.TransactionReceipt(ctx, common.HexToHash(minimalEvmLog.TransactionHash))
	if err != nil {
		n.logger.Debug("enrichment failed: could not fetch transaction receipt",
			"tx_hash", minimalEvmLog.TransactionHash,
			"error", err)
		return nil, fmt.Errorf("failed to fetch transaction receipt: %w", err)
	}

	n.logger.Debug("fetched transaction receipt",
		"tx_hash", minimalEvmLog.TransactionHash,
		"logs_count", len(receipt.Logs))

	// Find the specific log that matches the operator's data
	var targetLog *types.Log
	for _, log := range receipt.Logs {
		if uint32(log.Index) == minimalEvmLog.Index {
			targetLog = log
			break
		}
	}

	if targetLog == nil {
		n.logger.Debug("enrichment failed: log not found in transaction",
			"tx_hash", minimalEvmLog.TransactionHash,
			"expected_log_index", minimalEvmLog.Index,
			"available_log_indices", func() []uint32 {
				indices := make([]uint32, len(receipt.Logs))
				for i, log := range receipt.Logs {
					indices[i] = uint32(log.Index)
				}
				return indices
			}())
		return nil, fmt.Errorf("log with index %d not found in transaction %s",
			minimalEvmLog.Index, minimalEvmLog.TransactionHash)
	}

	n.logger.Debug("found target log",
		"tx_hash", minimalEvmLog.TransactionHash,
		"log_index", targetLog.Index,
		"address", targetLog.Address.Hex(),
		"topics_count", len(targetLog.Topics),
		"data_length", len(targetLog.Data))

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

	enrichedOutput := &avsproto.EventTrigger_Output{}

	// Check if this is a Transfer event and enrich with token metadata
	isTransferEvent := len(targetLog.Topics) > 0 &&
		targetLog.Topics[0].Hex() == "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

	n.logger.Debug("checking if transfer event",
		"is_transfer", isTransferEvent,
		"topics_count", len(targetLog.Topics),
		"first_topic", func() string {
			if len(targetLog.Topics) > 0 {
				return targetLog.Topics[0].Hex()
			}
			return "none"
		}())

	if isTransferEvent && len(targetLog.Topics) >= 3 {
		n.logger.Debug("processing as transfer event")

		// Get block timestamp for transfer_log
		header, err := rpcConn.HeaderByNumber(ctx, big.NewInt(int64(targetLog.BlockNumber)))
		var blockTimestamp uint64
		if err == nil {
			blockTimestamp = header.Time * 1000 // Convert to milliseconds
		} else {
			n.logger.Debug("could not fetch block header for timestamp", "error", err)
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

		n.logger.Debug("created transfer log",
			"from", fromAddr,
			"to", toAddr,
			"value", value,
			"token_address", targetLog.Address.Hex())

		// Enrich with token metadata
		if err := n.tokenEnrichmentService.EnrichTransferLog(enrichedEvmLog, transferLog); err != nil {
			n.logger.Warn("failed to enrich transfer log with token metadata", "error", err)
			// Continue without enrichment - partial data is better than no data
		} else {
			n.logger.Debug("successfully enriched transfer log",
				"token_name", transferLog.TokenName,
				"token_symbol", transferLog.TokenSymbol,
				"token_decimals", transferLog.TokenDecimals,
				"value_formatted", transferLog.ValueFormatted)
		}

		// Use the oneof TransferLog field
		enrichedOutput.OutputType = &avsproto.EventTrigger_Output_TransferLog{
			TransferLog: transferLog,
		}
	} else {
		n.logger.Debug("processing as regular EVM log event")
		// Regular event (not a transfer) - use the oneof EvmLog field
		enrichedOutput.OutputType = &avsproto.EventTrigger_Output_EvmLog{
			EvmLog: enrichedEvmLog,
		}
	}

	n.logger.Debug("enrichment completed successfully",
		"has_transfer_log", enrichedOutput.GetTransferLog() != nil,
		"has_evm_log", enrichedOutput.GetEvmLog() != nil)

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

// sanitizeTriggerNameForJS converts trigger names to valid JavaScript variable identifiers
// by replacing spaces and special characters with underscores.
// This ensures trigger names like "my event trigger" or "trigger-1" become valid JS variables
// like "my_event_trigger" and "trigger_1" respectively.
func sanitizeTriggerNameForJS(triggerName string) string {
	if triggerName == "" {
		return "unnamed_trigger"
	}

	// Replace any sequence of non-alphanumeric characters (except underscore) with a single underscore
	reg := regexp.MustCompile(`[^a-zA-Z0-9_]+`)
	sanitized := reg.ReplaceAllString(triggerName, "_")

	// Remove leading/trailing underscores
	sanitized = strings.Trim(sanitized, "_")

	// Ensure it doesn't start with a number (JS variable naming rule)
	if len(sanitized) > 0 && sanitized[0] >= '0' && sanitized[0] <= '9' {
		sanitized = "trigger_" + sanitized
	}

	// Fallback if somehow we end up with empty string
	if sanitized == "" {
		sanitized = "unnamed_trigger"
	}

	return sanitized
}

// buildEventTriggerOutput creates an EventTrigger_Output from raw trigger output data.
// This shared function eliminates code duplication between SimulateTask, RunTriggerRPC,
// and regular task execution flows.
//
// Parameters:
//   - triggerOutput: map containing raw trigger output data from runEventTriggerImmediately
//
// Returns:
//   - *avsproto.EventTrigger_Output: properly structured protobuf output with oneof fields
//
// The function handles both TransferLog (for Transfer events with enriched token metadata)
// and EvmLog (for general Ethereum events with raw log data).
func buildEventTriggerOutput(triggerOutput map[string]interface{}) *avsproto.EventTrigger_Output {
	eventOutput := &avsproto.EventTrigger_Output{}

	// Check if we have event data and populate appropriately
	if triggerOutput != nil {
		// Check if we found events
		if found, ok := triggerOutput["found"].(bool); ok && found {
			// We found events - check if we have transfer_log data (for Transfer events)
			if transferLogData, hasTransferLog := triggerOutput["transfer_log"].(map[string]interface{}); hasTransferLog {
				// Create TransferLog structure
				transferLog := &avsproto.EventTrigger_TransferLogOutput{}

				if tokenName, ok := transferLogData["tokenName"].(string); ok {
					transferLog.TokenName = tokenName
				}
				if tokenSymbol, ok := transferLogData["tokenSymbol"].(string); ok {
					transferLog.TokenSymbol = tokenSymbol
				}
				if tokenDecimals, ok := transferLogData["tokenDecimals"].(uint32); ok {
					transferLog.TokenDecimals = tokenDecimals
				}
				if txHash, ok := transferLogData["transactionHash"].(string); ok {
					transferLog.TransactionHash = txHash
				}
				if address, ok := transferLogData["address"].(string); ok {
					transferLog.Address = address
				}
				if blockNumber, ok := transferLogData["blockNumber"].(uint64); ok {
					transferLog.BlockNumber = blockNumber
				}
				if blockTimestamp, ok := transferLogData["blockTimestamp"].(uint64); ok {
					transferLog.BlockTimestamp = blockTimestamp
				}
				if fromAddress, ok := transferLogData["fromAddress"].(string); ok {
					transferLog.FromAddress = fromAddress
				}
				if toAddress, ok := transferLogData["toAddress"].(string); ok {
					transferLog.ToAddress = toAddress
				}
				if value, ok := transferLogData["value"].(string); ok {
					transferLog.Value = value
				}
				if valueFormatted, ok := transferLogData["valueFormatted"].(string); ok {
					transferLog.ValueFormatted = valueFormatted
				}
				if txIndex, ok := transferLogData["transactionIndex"].(uint32); ok {
					transferLog.TransactionIndex = txIndex
				}
				if logIndex, ok := transferLogData["logIndex"].(uint32); ok {
					transferLog.LogIndex = logIndex
				}

				// Set the TransferLog in the oneof field
				eventOutput.OutputType = &avsproto.EventTrigger_Output_TransferLog{
					TransferLog: transferLog,
				}
			} else if evmLogData, hasEvmLog := triggerOutput["evm_log"].(map[string]interface{}); hasEvmLog {
				// Create EvmLog structure for general Ethereum events
				evmLog := &avsproto.Evm_Log{}

				if address, ok := evmLogData["address"].(string); ok {
					evmLog.Address = address
				}
				if topics, ok := evmLogData["topics"].([]string); ok {
					evmLog.Topics = topics
				}
				if data, ok := evmLogData["data"].(string); ok {
					evmLog.Data = data
				}
				if blockNumber, ok := evmLogData["blockNumber"].(uint64); ok {
					evmLog.BlockNumber = blockNumber
				}
				if txHash, ok := evmLogData["transactionHash"].(string); ok {
					evmLog.TransactionHash = txHash
				}
				if txIndex, ok := evmLogData["transactionIndex"].(uint32); ok {
					evmLog.TransactionIndex = txIndex
				}
				if blockHash, ok := evmLogData["blockHash"].(string); ok {
					evmLog.BlockHash = blockHash
				}
				if index, ok := evmLogData["index"].(uint32); ok {
					evmLog.Index = index
				}
				if removed, ok := evmLogData["removed"].(bool); ok {
					evmLog.Removed = removed
				}

				// Set the EvmLog in the oneof field
				eventOutput.OutputType = &avsproto.EventTrigger_Output_EvmLog{
					EvmLog: evmLog,
				}
			}
		}
		// If no events found or no event data, eventOutput remains with default empty oneof
	}

	return eventOutput
}

// buildBlockTriggerOutput creates a BlockTrigger_Output from raw trigger output data.
// This shared function eliminates code duplication between SimulateTask, RunTriggerRPC,
// and regular task execution flows.
//
// Parameters:
//   - triggerOutput: map containing raw trigger output data from runBlockTriggerImmediately
//
// Returns:
//   - *avsproto.BlockTrigger_Output: properly structured protobuf output with block data
func buildBlockTriggerOutput(triggerOutput map[string]interface{}) *avsproto.BlockTrigger_Output {
	blockNumber := uint64(0)
	blockHash := ""
	timestamp := uint64(0)
	parentHash := ""
	difficulty := ""
	gasLimit := uint64(0)
	gasUsed := uint64(0)

	if triggerOutput != nil {
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
	}

	return &avsproto.BlockTrigger_Output{
		BlockNumber: blockNumber,
		BlockHash:   blockHash,
		Timestamp:   timestamp,
		ParentHash:  parentHash,
		Difficulty:  difficulty,
		GasLimit:    gasLimit,
		GasUsed:     gasUsed,
	}
}

// buildFixedTimeTriggerOutput creates a FixedTimeTrigger_Output from raw trigger output data.
// This shared function eliminates code duplication between SimulateTask, RunTriggerRPC,
// and regular task execution flows.
//
// Parameters:
//   - triggerOutput: map containing raw trigger output data from runFixedTimeTriggerImmediately
//
// Returns:
//   - *avsproto.FixedTimeTrigger_Output: properly structured protobuf output with timestamp data
func buildFixedTimeTriggerOutput(triggerOutput map[string]interface{}) *avsproto.FixedTimeTrigger_Output {
	timestamp := uint64(0)
	timestampISO := ""

	if triggerOutput != nil {
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
	}

	return &avsproto.FixedTimeTrigger_Output{
		Timestamp:    timestamp,
		TimestampIso: timestampISO,
	}
}

// buildCronTriggerOutput creates a CronTrigger_Output from raw trigger output data.
// This shared function eliminates code duplication between SimulateTask, RunTriggerRPC,
// and regular task execution flows.
//
// Parameters:
//   - triggerOutput: map containing raw trigger output data from runCronTriggerImmediately
//
// Returns:
//   - *avsproto.CronTrigger_Output: properly structured protobuf output with timestamp data
func buildCronTriggerOutput(triggerOutput map[string]interface{}) *avsproto.CronTrigger_Output {
	timestamp := uint64(0)
	timestampISO := ""

	if triggerOutput != nil {
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
	}

	return &avsproto.CronTrigger_Output{
		Timestamp:    timestamp,
		TimestampIso: timestampISO,
	}
}

// buildManualTriggerOutput creates a ManualTrigger_Output from raw trigger output data.
// This shared function eliminates code duplication between SimulateTask, RunTriggerRPC,
// and regular task execution flows.
//
// Parameters:
//   - triggerOutput: map containing raw trigger output data from runManualTriggerImmediately
//
// Returns:
//   - *avsproto.ManualTrigger_Output: properly structured protobuf output with run timestamp
func buildManualTriggerOutput(triggerOutput map[string]interface{}) *avsproto.ManualTrigger_Output {
	runAt := uint64(time.Now().Unix()) // Default to current time

	if triggerOutput != nil {
		if ra, ok := triggerOutput["runAt"]; ok {
			if raUint, ok := ra.(uint64); ok {
				runAt = raUint
			}
		}
	}

	return &avsproto.ManualTrigger_Output{
		RunAt: runAt,
	}
}

// buildTriggerDataMap creates a map for JavaScript trigger variable access.
// This shared function eliminates code duplication between SimulateTask and VM initialization.
//
// Parameters:
//   - triggerType: the type of trigger being processed
//   - triggerOutput: map containing raw trigger output data
//
// Returns:
//   - map[string]interface{}: JavaScript-accessible trigger data map
func buildTriggerDataMap(triggerType avsproto.TriggerType, triggerOutput map[string]interface{}) map[string]interface{} {
	triggerDataMap := make(map[string]interface{})

	if triggerOutput == nil {
		return triggerDataMap
	}

	switch triggerType {
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
		// Handle event trigger data with special processing for transfer_log
		if transferLogData, hasTransferLog := triggerOutput["transfer_log"].(map[string]interface{}); hasTransferLog {
			// Flatten transfer_log data to top level for JavaScript access
			for k, v := range transferLogData {
				triggerDataMap[k] = v
			}
		} else {
			// For non-transfer events, copy all event trigger data
			for k, v := range triggerOutput {
				triggerDataMap[k] = v
			}
		}
	default:
		// For unknown trigger types, copy all output data
		for k, v := range triggerOutput {
			triggerDataMap[k] = v
		}
	}

	return triggerDataMap
}

// buildExecutionStepOutputData creates the appropriate OutputData oneof field for execution steps.
// This shared function eliminates code duplication in execution step creation.
//
// Parameters:
//   - triggerType: the type of trigger being processed
//   - triggerOutputProto: the protobuf trigger output structure
//
// Returns:
//   - avsproto.IsExecution_Step_OutputData: the appropriate oneof field for the execution step
func buildExecutionStepOutputData(triggerType avsproto.TriggerType, triggerOutputProto interface{}) avsproto.IsExecution_Step_OutputData {
	if triggerOutputProto == nil {
		// Create empty output structure based on trigger type to avoid nil output
		switch triggerType {
		case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
			return &avsproto.Execution_Step_ManualTrigger{ManualTrigger: &avsproto.ManualTrigger_Output{}}
		case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
			return &avsproto.Execution_Step_FixedTimeTrigger{FixedTimeTrigger: &avsproto.FixedTimeTrigger_Output{}}
		case avsproto.TriggerType_TRIGGER_TYPE_CRON:
			return &avsproto.Execution_Step_CronTrigger{CronTrigger: &avsproto.CronTrigger_Output{}}
		case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
			return &avsproto.Execution_Step_BlockTrigger{BlockTrigger: &avsproto.BlockTrigger_Output{}}
		case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
			return &avsproto.Execution_Step_EventTrigger{EventTrigger: &avsproto.EventTrigger_Output{
				// No oneof field set, so GetTransferLog() and GetEvmLog() return nil
			}}
		}
		return nil
	}

	switch triggerType {
	case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
		if output, ok := triggerOutputProto.(*avsproto.ManualTrigger_Output); ok {
			return &avsproto.Execution_Step_ManualTrigger{ManualTrigger: output}
		}
	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
		if output, ok := triggerOutputProto.(*avsproto.FixedTimeTrigger_Output); ok {
			return &avsproto.Execution_Step_FixedTimeTrigger{FixedTimeTrigger: output}
		}
	case avsproto.TriggerType_TRIGGER_TYPE_CRON:
		if output, ok := triggerOutputProto.(*avsproto.CronTrigger_Output); ok {
			return &avsproto.Execution_Step_CronTrigger{CronTrigger: output}
		}
	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		if output, ok := triggerOutputProto.(*avsproto.BlockTrigger_Output); ok {
			return &avsproto.Execution_Step_BlockTrigger{BlockTrigger: output}
		}
	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		if output, ok := triggerOutputProto.(*avsproto.EventTrigger_Output); ok {
			return &avsproto.Execution_Step_EventTrigger{EventTrigger: output}
		}
		// If type assertion failed, log the actual type for debugging
		if triggerOutputProto != nil {
			// Create empty EventTrigger output as fallback to avoid nil
			return &avsproto.Execution_Step_EventTrigger{EventTrigger: &avsproto.EventTrigger_Output{
				// No oneof field set, so GetTransferLog() and GetEvmLog() return nil
			}}
		}
	}

	// Fallback: create empty output structure based on trigger type to avoid nil output
	switch triggerType {
	case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
		return &avsproto.Execution_Step_ManualTrigger{ManualTrigger: &avsproto.ManualTrigger_Output{}}
	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
		return &avsproto.Execution_Step_FixedTimeTrigger{FixedTimeTrigger: &avsproto.FixedTimeTrigger_Output{}}
	case avsproto.TriggerType_TRIGGER_TYPE_CRON:
		return &avsproto.Execution_Step_CronTrigger{CronTrigger: &avsproto.CronTrigger_Output{}}
	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		return &avsproto.Execution_Step_BlockTrigger{BlockTrigger: &avsproto.BlockTrigger_Output{}}
	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		return &avsproto.Execution_Step_EventTrigger{EventTrigger: &avsproto.EventTrigger_Output{
			// No oneof field set, so GetTransferLog() and GetEvmLog() return nil
		}}
	}

	return nil
}

// buildTriggerDataMapFromProtobuf creates a map for JavaScript trigger variable access from protobuf structures.
// This shared function eliminates code duplication in VM initialization where protobuf trigger outputs
// need to be converted to JavaScript-accessible data maps.
//
// Parameters:
//   - triggerType: the type of trigger being processed
//   - triggerOutputProto: the protobuf trigger output structure (e.g., *avsproto.BlockTrigger_Output)
//   - logger: optional logger for debugging (can be nil)
//
// Returns:
//   - map[string]interface{}: JavaScript-accessible trigger data map
//
// This function differs from buildTriggerDataMap in that it works with structured protobuf data
// rather than raw trigger output maps, making it suitable for VM initialization.
func buildTriggerDataMapFromProtobuf(triggerType avsproto.TriggerType, triggerOutputProto interface{}, logger sdklogging.Logger) map[string]interface{} {
	triggerDataMap := make(map[string]interface{})

	if triggerOutputProto == nil {
		// Always add trigger type for reference if available
		if triggerType != avsproto.TriggerType_TRIGGER_TYPE_UNSPECIFIED {
			triggerDataMap["type"] = triggerType.String()
		}
		return triggerDataMap
	}

	switch triggerType {
	case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
		if manualOutput, ok := triggerOutputProto.(*avsproto.ManualTrigger_Output); ok {
			triggerDataMap["run_at"] = manualOutput.RunAt
		}
	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
		if timeOutput, ok := triggerOutputProto.(*avsproto.FixedTimeTrigger_Output); ok {
			triggerDataMap["timestamp"] = timeOutput.Timestamp
			triggerDataMap["timestamp_iso"] = timeOutput.TimestampIso
		}
	case avsproto.TriggerType_TRIGGER_TYPE_CRON:
		if cronOutput, ok := triggerOutputProto.(*avsproto.CronTrigger_Output); ok {
			triggerDataMap["timestamp"] = cronOutput.Timestamp
			triggerDataMap["timestamp_iso"] = cronOutput.TimestampIso
		}
	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		if blockOutput, ok := triggerOutputProto.(*avsproto.BlockTrigger_Output); ok {
			triggerDataMap["block_number"] = blockOutput.BlockNumber
			triggerDataMap["block_hash"] = blockOutput.BlockHash
			triggerDataMap["timestamp"] = blockOutput.Timestamp
			triggerDataMap["parent_hash"] = blockOutput.ParentHash
			triggerDataMap["difficulty"] = blockOutput.Difficulty
			triggerDataMap["gas_limit"] = blockOutput.GasLimit
			triggerDataMap["gas_used"] = blockOutput.GasUsed
		}
	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		if eventOutput, ok := triggerOutputProto.(*avsproto.EventTrigger_Output); ok {
			// Check if we have transfer log data in the event output
			if transferLogData := eventOutput.GetTransferLog(); transferLogData != nil {
				// Use transfer log data to populate rich trigger data matching runTrigger format
				// Use camelCase field names for JavaScript compatibility
				triggerDataMap["tokenName"] = transferLogData.TokenName
				triggerDataMap["tokenSymbol"] = transferLogData.TokenSymbol
				triggerDataMap["tokenDecimals"] = transferLogData.TokenDecimals
				triggerDataMap["transactionHash"] = transferLogData.TransactionHash
				triggerDataMap["address"] = transferLogData.Address
				triggerDataMap["blockNumber"] = transferLogData.BlockNumber
				triggerDataMap["blockTimestamp"] = transferLogData.BlockTimestamp
				triggerDataMap["fromAddress"] = transferLogData.FromAddress
				triggerDataMap["toAddress"] = transferLogData.ToAddress
				triggerDataMap["value"] = transferLogData.Value
				triggerDataMap["valueFormatted"] = transferLogData.ValueFormatted
				triggerDataMap["transactionIndex"] = transferLogData.TransactionIndex
				triggerDataMap["logIndex"] = transferLogData.LogIndex
			} else if evmLogData := eventOutput.GetEvmLog(); evmLogData != nil {
				// Use EVM log data for regular events
				triggerDataMap["address"] = evmLogData.Address
				triggerDataMap["topics"] = evmLogData.Topics
				triggerDataMap["data"] = evmLogData.Data
				triggerDataMap["blockNumber"] = evmLogData.BlockNumber
				triggerDataMap["transactionHash"] = evmLogData.TransactionHash
				triggerDataMap["transactionIndex"] = evmLogData.TransactionIndex
				triggerDataMap["blockHash"] = evmLogData.BlockHash
				triggerDataMap["logIndex"] = evmLogData.Index
				triggerDataMap["removed"] = evmLogData.Removed
			}
		} else if enrichedDataMap, ok := triggerOutputProto.(map[string]interface{}); ok {
			// Handle the new enriched data format that survives JSON serialization
			if enrichedData, hasEnrichedData := enrichedDataMap["enriched_data"].(map[string]interface{}); hasEnrichedData {
				// Copy all enriched data to the trigger data map
				for k, v := range enrichedData {
					triggerDataMap[k] = v
				}
				if logger != nil {
					logger.Debug("loaded enriched event trigger data from queue",
						"has_token_symbol", triggerDataMap["tokenSymbol"] != nil,
						"has_value_formatted", triggerDataMap["valueFormatted"] != nil)
				}
			} else {
				// Fallback: copy all data from the map
				for k, v := range enrichedDataMap {
					if k != "trigger_type" { // Skip metadata
						triggerDataMap[k] = v
					}
				}
			}
		}
	default:
		// For unknown trigger types, return empty map
	}

	// Always add trigger type for reference
	if triggerType != avsproto.TriggerType_TRIGGER_TYPE_UNSPECIFIED {
		triggerDataMap["type"] = triggerType.String()
	}

	return triggerDataMap
}
