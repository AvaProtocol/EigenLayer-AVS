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
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
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
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// getTaskStatusString safely converts a TaskStatus to string, handling edge cases
func getTaskStatusString(status avsproto.TaskStatus) string {
	// The crash was caused by calling .String() on an uninitialized enum
	// This function provides a safe wrapper that ensures we always get a valid string
	return status.String()
}

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

	// Context cancellation for managing ticker lifecycle
	TickerCancel context.CancelFunc
	TickerCtx    context.Context
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

// AddTaskForTesting adds a task directly to the engine's task map for testing purposes
// This bypasses database storage and validation - only use in tests
func (n *Engine) AddTaskForTesting(task *model.Task) {
	n.lock.Lock()
	defer n.lock.Unlock()
	n.tasks[task.Id] = task
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
			// Ensure task is properly initialized after loading from storage
			if initErr := task.EnsureInitialized(); initErr != nil {
				n.logger.Warn("Task failed initialization after loading from storage",
					"storage_key", string(item.Key),
					"task_id", task.Id,
					"error", initErr)
				continue // Skip this corrupt task
			}
			n.tasks[task.Id] = task
			loadedCount++
		} else {
			n.logger.Warn("Failed to unmarshal task during startup", "storage_key", string(item.Key), "error", err)
		}
	}

	n.logger.Info("🚀 Engine started successfully", "active_tasks_loaded", loadedCount)

	// Detect and handle any invalid tasks that may have been created before validation was fixed
	if err := n.DetectAndHandleInvalidTasks(); err != nil {
		n.logger.Error("Failed to handle invalid tasks during startup", "error", err)
		// Don't fail startup, but log the error
	}

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
			return nil, status.Errorf(codes.Code(avsproto.ErrorCode_STORAGE_UNAVAILABLE), "Error fetching wallets by owner: %v", listErr)
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

	// Enforce max smart wallet count per owner before deriving a new one
	// Count existing wallets stored for this owner
	if n.smartWalletConfig != nil {
		maxAllowed := n.smartWalletConfig.MaxWalletsPerOwner
		if maxAllowed <= 0 {
			maxAllowed = config.DefaultMaxWalletsPerOwner
		}
		if maxAllowed > config.HardMaxWalletsPerOwner {
			maxAllowed = config.HardMaxWalletsPerOwner
		}
		// Fetch all wallets for this owner and count unique addresses
		dbItems, listErr := n.db.GetByPrefix(WalletByOwnerPrefix(user.Address))
		if listErr == nil {
			unique := make(map[string]struct{})
			for _, item := range dbItems {
				storedModelWallet := &model.SmartWallet{}
				if err := storedModelWallet.FromStorageData(item.Value); err == nil && storedModelWallet.Address != nil {
					unique[strings.ToLower(storedModelWallet.Address.Hex())] = struct{}{}
				}
			}
			if len(unique) >= maxAllowed {
				return nil, status.Errorf(codes.ResourceExhausted, "max smart wallet count reached for owner (limit=%d)", maxAllowed)
			}
		}
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
		return nil, status.Errorf(codes.Code(avsproto.ErrorCode_STORAGE_UNAVAILABLE), "Error fetching wallet: %v", err)
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
			return nil, status.Errorf(codes.Code(avsproto.ErrorCode_STORAGE_WRITE_ERROR), "Error storing new wallet: %v", storeErr)
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
	userAddr := user.Address.Hex()

	// Log the create task request with detailed information
	n.logger.Info("📥 CreateTask request received",
		"user", userAddr,
		"smart_wallet_address", taskPayload.SmartWalletAddress,
		"trigger_type", func() string {
			if taskPayload.Trigger != nil {
				return taskPayload.Trigger.Type.String()
			}
			return "nil"
		}(),
		"nodes_count", len(taskPayload.Nodes),
		"edges_count", len(taskPayload.Edges),
		"start_at", taskPayload.StartAt,
		"expired_at", taskPayload.ExpiredAt,
		"max_execution", taskPayload.MaxExecution,
		"name", taskPayload.Name)

	// Log node details for debugging serialization issues
	if len(taskPayload.Nodes) > 0 {
		var nodeTypes []string
		for i, node := range taskPayload.Nodes {
			nodeType := "unknown"
			if node != nil {
				nodeType = node.Type.String()
			}
			nodeTypes = append(nodeTypes, fmt.Sprintf("%d:%s", i, nodeType))
		}
		n.logger.Debug("📋 CreateTask nodes breakdown",
			"user", userAddr,
			"node_details", nodeTypes)
	}

	// Log edge details
	if len(taskPayload.Edges) > 0 {
		var edgeDetails []string
		for i, edge := range taskPayload.Edges {
			if edge != nil {
				edgeDetails = append(edgeDetails, fmt.Sprintf("%d:%s->%s", i, edge.Source, edge.Target))
			}
		}
		n.logger.Debug("🔗 CreateTask edges breakdown",
			"user", userAddr,
			"edge_details", edgeDetails)
	}

	if taskPayload.SmartWalletAddress != "" {
		if !ValidWalletAddress(taskPayload.SmartWalletAddress) {
			return nil, status.Errorf(codes.InvalidArgument, InvalidSmartAccountAddressError)
		}

		if valid, _ := ValidWalletOwner(n.db, user, common.HexToAddress(taskPayload.SmartWalletAddress)); !valid {
			return nil, status.Errorf(codes.InvalidArgument, InvalidSmartAccountAddressError)
		}
	}

	// Validate task expiration date - must be at least 1 hour from now
	if taskPayload.ExpiredAt > 0 {
		now := time.Now().Unix() * 1000 // Convert to milliseconds for consistency with frontend
		expiredAtMs := taskPayload.ExpiredAt
		timeDifferenceMs := expiredAtMs - now
		minimumTimeMs := int64(60 * 60 * 1000) // 1 hour in milliseconds

		if timeDifferenceMs < minimumTimeMs {
			minutesRemaining := float64(timeDifferenceMs) / (60 * 1000)
			return nil, status.Errorf(codes.InvalidArgument,
				"task expiration date is too close to current time. The task must expire at least 1 hour from now. Current remaining time: %.1f minutes",
				minutesRemaining)
		}
	}

	task, err := model.NewTaskFromProtobuf(user, taskPayload)

	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s", err.Error())
	}

	// Validate all node names for JavaScript compatibility
	if err := validateAllNodeNamesForJavaScript(task); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "node name validation failed: %v", err)
	}

	updates := map[string][]byte{}

	taskJSON, err := task.ToJSON()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to serialize task: %v", err)
	}

	updates[string(TaskStorageKey(task.Id, task.Status))] = taskJSON
	updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", avsproto.TaskStatus_Active))

	if err = n.db.BatchWrite(updates); err != nil {
		return nil, err
	}

	n.lock.Lock()
	n.tasks[task.Id] = task
	n.lock.Unlock()

	// Note: MonitorTaskTrigger notifications are handled by StreamCheckToOperator
	// which sends complete task metadata. The batched notification system is only
	// for CancelTask/DeleteTask operations that don't need complete metadata.

	// Log successful task creation with final counts
	n.logger.Info("✅ CreateTask completed successfully",
		"user", userAddr,
		"task_id", task.Id,
		"smart_wallet_address", task.SmartWalletAddress,
		"trigger_type", task.Trigger.Type.String(),
		"final_nodes_count", len(task.Nodes),
		"final_edges_count", len(task.Edges),
		"status", getTaskStatusString(task.Status))

	return task, nil
}

func (n *Engine) StreamCheckToOperator(payload *avsproto.SyncMessagesReq, srv avsproto.Node_SyncMessagesServer) error {
	address := payload.Address
	connectionStartTime := time.Now()
	streamID := fmt.Sprintf("%s-%d", address[len(address)-6:], connectionStartTime.UnixNano()%10000)

	n.logger.Info("open channel to stream check to operator",
		"operator", address,
		"stream_id", streamID,
		"connection_start_time", connectionStartTime.Format("15:04:05.000"),
		"monotonic_clock", payload.MonotonicClock)

	// Register this operator's stream for real-time notifications
	n.streamsMutex.Lock()
	n.operatorStreams[address] = srv
	n.streamsMutex.Unlock()

	// Create context for this connection's ticker
	tickerCtx, tickerCancel := context.WithCancel(context.Background())

	if _, ok := n.trackSyncedTasks[address]; !ok {
		n.lock.Lock()
		n.trackSyncedTasks[address] = &operatorState{
			MonotonicClock: payload.MonotonicClock,
			TaskID:         map[string]bool{},
			Capabilities:   payload.Capabilities,
			TickerCtx:      tickerCtx,
			TickerCancel:   tickerCancel,
		}
		n.lock.Unlock()

		n.logger.Info("🔗 New operator connected with capabilities",
			"operator", address,
			"event_monitoring", payload.Capabilities.GetEventMonitoring(),
			"block_monitoring", payload.Capabilities.GetBlockMonitoring(),
			"time_monitoring", payload.Capabilities.GetTimeMonitoring())
	} else {
		// The operator has reconnected, cancel any existing ticker and reset state
		n.lock.Lock()

		// Cancel old ticker if it exists
		if n.trackSyncedTasks[address].TickerCancel != nil {
			n.logger.Info("🔄 Canceling old ticker for reconnected operator",
				"operator", address,
				"old_stream", "existing")
			n.trackSyncedTasks[address].TickerCancel()
		}

		if payload.MonotonicClock > n.trackSyncedTasks[address].MonotonicClock {
			n.logger.Info("🔄 Operator reconnected with newer MonotonicClock - resetting task tracking",
				"operator", address,
				"old_clock", n.trackSyncedTasks[address].MonotonicClock,
				"new_clock", payload.MonotonicClock,
				"old_task_count", len(n.trackSyncedTasks[address].TaskID))

			n.trackSyncedTasks[address].TaskID = map[string]bool{}
			n.trackSyncedTasks[address].MonotonicClock = payload.MonotonicClock
			n.trackSyncedTasks[address].Capabilities = payload.Capabilities

			// Set new ticker context for this connection
			n.trackSyncedTasks[address].TickerCtx = tickerCtx
			n.trackSyncedTasks[address].TickerCancel = tickerCancel

			n.logger.Info("🔄 Operator reconnected with updated capabilities",
				"operator", address,
				"event_monitoring", payload.Capabilities.GetEventMonitoring(),
				"block_monitoring", payload.Capabilities.GetBlockMonitoring(),
				"time_monitoring", payload.Capabilities.GetTimeMonitoring())
		} else {
			n.logger.Warn("⚠️ Operator reconnected with same/older MonotonicClock - force-resetting task tracking",
				"operator", address,
				"existing_clock", n.trackSyncedTasks[address].MonotonicClock,
				"new_clock", payload.MonotonicClock,
				"existing_task_count", len(n.trackSyncedTasks[address].TaskID),
				"tasks_will_be_skipped", false)

			// CRITICAL FIX: Always reset task tracking on reconnection, regardless of MonotonicClock
			// This ensures tasks are resent to reconnected operators
			n.logger.Info("🔧 Force-resetting task tracking for reconnected operator",
				"operator", address,
				"reason", "ensure_tasks_resent_on_reconnection")

			n.trackSyncedTasks[address].TaskID = map[string]bool{}
			n.trackSyncedTasks[address].Capabilities = payload.Capabilities

			// Set new ticker context for this connection
			n.trackSyncedTasks[address].TickerCtx = tickerCtx
			n.trackSyncedTasks[address].TickerCancel = tickerCancel
		}
		n.lock.Unlock()
	}

	// Create ticker for this connection
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Reset the state if the operator disconnect
	defer func() {
		n.logger.Info("🔌 Operator disconnecting, cleaning up state",
			"operator", address,
			"stream_id", streamID)

		n.lock.Lock()
		if n.trackSyncedTasks[address] != nil {
			// Only cancel the ticker context if it's OUR context (not a newer connection's context)
			if n.trackSyncedTasks[address].TickerCtx == tickerCtx {
				n.logger.Info("🔄 Canceling ticker context for this connection",
					"operator", address,
					"stream_id", streamID)
				if n.trackSyncedTasks[address].TickerCancel != nil {
					n.trackSyncedTasks[address].TickerCancel()
				}
				n.trackSyncedTasks[address].TaskID = map[string]bool{}
				n.trackSyncedTasks[address].MonotonicClock = 0
			} else {
				n.logger.Info("🔄 Skipping ticker cancellation - newer connection exists",
					"operator", address,
					"stream_id", streamID)
			}
		}
		n.lock.Unlock()

		// Unregister the operator's stream
		n.streamsMutex.Lock()
		delete(n.operatorStreams, address)
		n.streamsMutex.Unlock()

		// Reassign tasks that were assigned to this operator
		n.reassignOrphanedTasks()

		n.logger.Info("✅ Operator cleanup completed",
			"operator", address,
			"stream_id", streamID)
	}()

	//nolint:S1000
	for {
		select {
		case <-tickerCtx.Done():
			n.logger.Info("🛑 Ticker context canceled, stopping ticker loop",
				"operator", address,
				"stream_id", streamID)
			return nil
		case <-ticker.C:
			tickTime := time.Now()
			connectionAge := time.Since(connectionStartTime)

			n.logger.Info("📟 Ticker fired for operator",
				"operator", address,
				"stream_id", streamID,
				"tick_time", tickTime.Format("15:04:05.000"),
				"connection_start_time", connectionStartTime.Format("15:04:05.000"),
				"connection_age", connectionAge.String())

			if n.shutdown {
				return nil
			}

			if n.tasks == nil {
				n.logger.Debug("📭 No tasks available",
					"operator", address)
				continue
			}

			// Add connection stability grace period to prevent race conditions
			if connectionAge < 10*time.Second {
				n.logger.Info("⏳ Waiting for connection to stabilize before sending tasks",
					"operator", address,
					"stream_id", streamID,
					"connection_age", connectionAge.String(),
					"min_required", "10s",
					"remaining_wait", (10*time.Second - connectionAge).String(),
					"tick_time", tickTime.Format("15:04:05.000"))
				continue
			}

			n.logger.Info("✅ Connection stabilized, proceeding with task assignment",
				"operator", address,
				"stream_id", streamID,
				"connection_age", connectionAge.String(),
				"stabilization_complete", true,
				"total_tasks_in_memory", len(n.tasks))

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
			// IMPORTANT: Only after connection has stabilized to prevent premature assignment
			n.reassignOrphanedTasks()

			// Aggregate tasks for this operator to reduce logging and improve efficiency
			var tasksToStream []*model.Task
			var tasksByTriggerType = make(map[string]int) // Count tasks by trigger type
			var newAssignments []string                   // Track new task assignments for this operator
			var orphanedTasksReclaimed []string           // Track orphaned tasks being reclaimed

			n.logger.Debug("🔍 Processing tasks for operator assignment",
				"operator", address,
				"total_tasks_in_memory", len(n.tasks))

			// Iterate over a snapshot to avoid concurrent map iteration/write panics
			n.lock.Lock()
			snapshot := make([]*model.Task, 0, len(n.tasks))
			for _, t := range n.tasks {
				snapshot = append(snapshot, t)
			}
			n.lock.Unlock()

			for _, task := range snapshot {
				if _, ok := n.trackSyncedTasks[address].TaskID[task.Id]; ok {
					n.logger.Debug("⏭️ Skipping task - already synced to operator",
						"operator", address,
						"task_id", task.Id,
						"task_name", task.Name)
					continue
				}

				// CRITICAL FIX: Check for orphaned tasks (assigned to empty string) and reclaim them
				n.assignmentMutex.RLock()
				currentAssignment, isAssigned := n.taskAssignments[task.Id]
				n.assignmentMutex.RUnlock()

				n.logger.Debug("🔍 Checking task assignment status",
					"operator", address,
					"task_id", task.Id,
					"is_assigned", isAssigned,
					"current_assignment", currentAssignment)

				var wasReclaimed bool
				if (isAssigned && currentAssignment == "") || !isAssigned {
					// This task is orphaned (assigned to empty string) OR has no assignment at all
					// Both cases mean we should reclaim it for this reconnecting operator
					n.assignmentMutex.Lock()
					n.taskAssignments[task.Id] = address
					n.assignmentMutex.Unlock()

					orphanedTasksReclaimed = append(orphanedTasksReclaimed, task.Id)
					wasReclaimed = true

					previousAssignment := "empty_string"
					if !isAssigned {
						previousAssignment = "no_assignment"
					}

					n.logger.Info("🔄 Reclaimed orphaned task for operator",
						"task_id", task.Id,
						"operator", address,
						"previous_assignment", previousAssignment)
				}

				// Check if this operator is assigned to handle this task
				// CRITICAL FIX: Don't call assignTaskToOperator if we just reclaimed the task
				var assignedOperator string
				if wasReclaimed {
					assignedOperator = address // We just assigned it to this operator
				} else {
					assignedOperator = n.assignTaskToOperator(task)
				}

				if assignedOperator != address {
					// This task is assigned to a different operator
					n.logger.Debug("⏭️ Skipping task - assigned to different operator",
						"operator", address,
						"task_id", task.Id,
						"assigned_to", assignedOperator)
					continue
				}

				// Track this as a new assignment (unless it was a reclaimed orphan)
				isReclaimed := false
				for _, orphanedId := range orphanedTasksReclaimed {
					if orphanedId == task.Id {
						isReclaimed = true
						break
					}
				}
				if !isReclaimed {
					newAssignments = append(newAssignments, task.Id)
				}

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

			// Log task processing results
			n.logger.Debug("🔍 Task processing completed for operator",
				"operator", address,
				"tasks_to_stream", len(tasksToStream),
				"new_assignments", len(newAssignments),
				"orphaned_reclaimed", len(orphanedTasksReclaimed))

			// Log aggregated task assignments per operator
			if len(newAssignments) > 0 || len(orphanedTasksReclaimed) > 0 {
				n.logger.Info("🔄 Task assignments for operator",
					"operator", address,
					"stream_id", streamID,
					"operation", "MonitorTaskTrigger",
					"assigned_task_ids", newAssignments,
					"total_assignments", len(newAssignments),
					"orphaned_tasks_reclaimed", orphanedTasksReclaimed,
					"total_reclaimed", len(orphanedTasksReclaimed))
			}

			// Stream all tasks and log aggregated results
			if len(tasksToStream) > 0 {
				successCount := 0
				failedCount := 0
				var firstError error

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

					// Add timeout wrapper for send operation to prevent hanging
					sendError := make(chan error, 1)
					go func() {
						sendError <- srv.Send(&resp)
					}()

					select {
					case err := <-sendError:
						if err != nil {
							failedCount++
							if firstError == nil {
								firstError = err
							}
							n.logger.Warn("⚠️ Failed to send task to operator (will retry next cycle)",
								"task_id", task.Id,
								"operator", payload.Address,
								"error", err,
								"error_type", fmt.Sprintf("%T", err),
								"grpc_status", status.Code(err).String())

							// Check if this is a connection-level error that requires reconnection
							grpcCode := status.Code(err)
							if grpcCode == codes.Unavailable || grpcCode == codes.Canceled || grpcCode == codes.DeadlineExceeded {
								n.logger.Error("🔥 Connection-level error detected, operator needs to reconnect",
									"operator", payload.Address,
									"error", err,
									"grpc_code", grpcCode.String())
								return fmt.Errorf("connection-level error, operator must reconnect: %w", err)
							}
						} else {
							n.lock.Lock()
							n.trackSyncedTasks[address].TaskID[task.Id] = true
							n.lock.Unlock()
							successCount++
						}
					case <-time.After(2 * time.Second):
						failedCount++
						n.logger.Warn("⏰ Timeout sending task to operator (will retry next cycle)",
							"task_id", task.Id,
							"operator", payload.Address,
							"timeout", "2s")
					}
				}

				// Log aggregated results instead of individual tasks
				if successCount > 0 || failedCount > 0 {
					n.logger.Info("📤 Streamed tasks to operator",
						"operator", payload.Address,
						"total_tasks", len(tasksToStream),
						"successful", successCount,
						"failed", failedCount,
						"task_breakdown", tasksByTriggerType)
				}

				// If all tasks failed with connection errors, return error to trigger reconnection
				if failedCount > 0 && successCount == 0 && firstError != nil {
					grpcCode := status.Code(firstError)
					if grpcCode == codes.Unavailable || grpcCode == codes.Canceled || grpcCode == codes.DeadlineExceeded {
						return fmt.Errorf("all task sends failed with connection error: %w", firstError)
					}
				}
			}
		}
	}
}

// notifyOperatorsTaskOperation queues notifications for batched sending to operators
// This method is non-blocking and batches notifications for efficiency
func (n *Engine) notifyOperatorsTaskOperation(taskID string, operation avsproto.MessageOp) {
	// MonitorTaskTrigger should not use batched notifications as it requires complete task metadata
	if operation == avsproto.MessageOp_MonitorTaskTrigger {
		n.logger.Warn("❌ MonitorTaskTrigger should not be sent via batched notifications",
			"task_id", taskID,
			"operation", operation.String(),
			"solution", "MonitorTaskTrigger is handled by StreamCheckToOperator with complete metadata")
		return
	}

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
					}

					// Only include TaskMetadata for MonitorTaskTrigger operations
					// For other operations (CancelTask, DeleteTask), TaskMetadata is not needed
					// and sending incomplete TaskMetadata causes nil pointer issues in operators
					if notification.Operation == avsproto.MessageOp_MonitorTaskTrigger {
						// For MonitorTaskTrigger, we would need complete task data
						// But batched notifications are typically for Cancel/Delete operations
						// If we ever need to batch MonitorTaskTrigger, we should fetch full task data
						n.logger.Warn("MonitorTaskTrigger should not be sent via batched notifications",
							"task_id", notification.TaskID,
							"operation", notification.Operation.String())
					}
					// Note: TaskMetadata is intentionally nil for Cancel/Delete operations

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
	// Acquire lock once for all map operations to reduce lock contention
	n.lock.Lock()

	n.logger.Debug("processing aggregator check hit", "operator", address, "task_id", payload.TaskId)

	// Update operator task tracking
	if state, exists := n.trackSyncedTasks[address]; exists {
		state.TaskID[payload.TaskId] = true
	}

	// Get task information to determine execution state
	task, exists := n.tasks[payload.TaskId]

	if !exists {
		// Task not found in memory - try database lookup
		n.lock.Unlock() // Release lock for database operation

		dbTask, dbErr := n.GetTaskByID(payload.TaskId)
		if dbErr != nil {
			// Task not found in database either - this is likely a stale operator notification
			// Log at DEBUG level to reduce noise in production
			n.logger.Debug("Operator notified about non-existent task (likely stale data)",
				"task_id", payload.TaskId,
				"operator", address,
				"memory_task_count", len(n.tasks))

			// Clean up stale task tracking for this operator
			n.lock.Lock()
			if state, exists := n.trackSyncedTasks[address]; exists {
				delete(state.TaskID, payload.TaskId)
				n.logger.Debug("Cleaned up stale task from operator tracking",
					"task_id", payload.TaskId,
					"operator", address)
			}
			n.lock.Unlock()

			return &ExecutionState{
				RemainingExecutions: 0,
				TaskStillActive:     false,
				Status:              "not_found",
				Message:             "Task no longer exists - operator should stop monitoring",
			}, nil // Return nil error to avoid spam
		}

		// Task found in database but not in memory - add it to memory and continue
		n.lock.Lock()
		n.tasks[dbTask.Id] = dbTask
		task = dbTask
		n.lock.Unlock()

		n.logger.Info("Task recovered from database and added to memory",
			"task_id", payload.TaskId,
			"operator", address,
			"task_status", task.Status,
			"memory_task_count_after", len(n.tasks))
	} else {
		n.lock.Unlock() // Release lock after getting task
	}

	n.logger.Debug("processed aggregator check hit", "operator", address, "task_id", payload.TaskId)

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
			if eventOutput, ok := triggerData.Output.(*avsproto.EventTrigger_Output); ok && eventOutput != nil {
				// With new structured data, we just log what we have
				hasData := eventOutput.Data != nil
				dataLength := 0
				if hasData {
					// Convert to string for logging purposes
					if dataStr, err := eventOutput.Data.MarshalJSON(); err == nil {
						dataLength = len(dataStr)
					}
				}

				n.logger.Debug("EventTrigger output with structured data",
					"task_id", payload.TaskId,
					"has_data", hasData,
					"data_length", dataLength)

				// Token enrichment is now handled during event parsing, not here
				// The structured data should already include all necessary enriched fields
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
		return nil, status.Errorf(codes.Code(avsproto.ErrorCode_STORAGE_UNAVAILABLE), StorageUnavailableError)
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
			return nil, status.Errorf(codes.Code(avsproto.ErrorCode_STORAGE_UNAVAILABLE), StorageUnavailableError)
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
	for statusInt := range avsproto.TaskStatus_name {
		if rawTaskData, err := n.db.GetKey(TaskStorageKey(taskID, avsproto.TaskStatus(statusInt))); err == nil {
			task := model.NewTask()
			err = task.FromStorageData(rawTaskData)

			if err == nil {
				return task, nil
			}

			return nil, status.Errorf(codes.Code(avsproto.ErrorCode_TASK_DATA_CORRUPTED), TaskStorageCorruptedError)
		}
	}

	return nil, status.Errorf(codes.NotFound, TaskNotFoundError)
}

func (n *Engine) GetTask(user *model.User, taskID string) (*model.Task, error) {
	task, err := n.GetTaskByID(taskID)
	if err != nil {
		return nil, err
	}

	if !task.OwnedBy(user.Address) {
		return nil, status.Errorf(codes.NotFound, TaskNotFoundError)
	}

	return task, nil
}

func (n *Engine) TriggerTask(user *model.User, payload *avsproto.TriggerTaskReq) (*avsproto.TriggerTaskResp, error) {
	// Validate task ID format first
	if !ValidateTaskId(payload.TaskId) {
		return nil, status.Errorf(codes.InvalidArgument, InvalidTaskIdFormat)
	}

	task, err := n.GetTask(user, payload.TaskId)
	if err != nil {
		return nil, err
	}

	// Explicit ownership validation for security (even though GetTask already checks this)
	if !task.OwnedBy(user.Address) {
		return nil, status.Errorf(codes.NotFound, TaskNotFoundError)
	}

	// Important business logic validation: Check if task is runnable
	if !task.IsRunable() {
		return nil, status.Errorf(codes.FailedPrecondition, TaskIsNotRunnable)
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

	// Create base response with always-available fields
	response := &avsproto.TriggerTaskResp{
		ExecutionId: queueTaskData.ExecutionID,
		WorkflowId:  payload.TaskId, // taskId and workflowId refer to the same entity. Consider renaming for consistency.
	}

	if payload.IsBlocking {
		executor := NewExecutor(n.smartWalletConfig, n.db, n.logger, n.tokenEnrichmentService)
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
			// For blocking mode, populate all execution fields like getExecution response
			response.Status = avsproto.ExecutionStatus_EXECUTION_STATUS_COMPLETED
			response.StartAt = &execution.StartAt
			response.EndAt = &execution.EndAt
			response.Success = &execution.Success
			if execution.Error != "" {
				response.Error = &execution.Error
			}
			response.Steps = execution.Steps
			return response, nil
		}
	} else {
		// For non-blocking mode, only set startAt (execution has started)
		startTime := time.Now().UnixMilli()
		response.StartAt = &startTime
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

	response.Status = avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING
	return response, nil
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
		return nil, status.Errorf(codes.InvalidArgument, "task trigger is required for simulation")
	}
	if len(task.Nodes) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "task must have at least one node for simulation")
	}

	// Validate all node names for JavaScript compatibility
	if err := validateAllNodeNamesForJavaScript(task); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "node name validation failed: %v", err)
	}

	// Step 1: Simulate the trigger to get trigger output data
	// Extract trigger type and config from the TaskTrigger
	n.logger.Info("SimulateTask received trigger", "trigger_type_raw", trigger.GetType(), "trigger_type_int", int(trigger.GetType()), "trigger_id", trigger.GetId(), "trigger_name", trigger.GetName())

	// Debug: Check what oneof field is set
	n.logger.Info("SimulateTask trigger oneof debug",
		"fixed_time", trigger.GetFixedTime() != nil,
		"cron", trigger.GetCron() != nil,
		"block", trigger.GetBlock() != nil,
		"event", trigger.GetEvent() != nil,
		"manual", TaskTriggerToTriggerType(trigger) == avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		"oneof_type", fmt.Sprintf("%T", trigger.GetTriggerType()))

	// Use TaskTriggerToTriggerType to determine type from oneof field instead of just GetType()
	triggerType := TaskTriggerToTriggerType(trigger)
	n.logger.Info("SimulateTask trigger type conversion", "from_oneof", triggerType, "from_explicit", trigger.GetType())

	// Validate that the derived trigger type matches the expected type
	if triggerType != trigger.GetType() {
		n.logger.Error("Trigger type mismatch", "derived_type", triggerType, "expected_type", trigger.GetType(), "trigger_id", trigger.GetId(), "trigger_name", trigger.GetName())
		return nil, status.Errorf(codes.InvalidArgument, "trigger type mismatch: derived=%v, expected=%v", triggerType, trigger.GetType())
	}

	triggerTypeStr := TriggerTypeToString(triggerType)
	if triggerTypeStr == "" {
		return nil, status.Errorf(codes.InvalidArgument, "unsupported trigger type: %v (oneof type: %T)", trigger.GetType(), trigger.GetTriggerType())
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
	switch triggerType {
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
		return nil, fmt.Errorf("unsupported trigger type for simulation: %v", triggerType)
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

	vm.WithLogger(n.logger).WithDb(n.db).SetSimulation(true)
	// Resolve AA sender for simulation ONLY if the workflow contains AA-relevant nodes
	// (contractWrite or ethTransfer). For non-AA workflows (e.g., CustomCode), skip this requirement.
	{
		requiresAA := false
		for _, tn := range task.Nodes {
			if tn.GetContractWrite() != nil || tn.GetEthTransfer() != nil {
				requiresAA = true
				break
			}
		}

		if requiresAA {
			owner := user.Address
			var chosenSender common.Address
			if wfCtxIface, ok := inputVariables["workflowContext"]; ok {
				if wfCtx, ok := wfCtxIface.(map[string]interface{}); ok {
					if runnerIface, ok := wfCtx["runner"]; ok {
						if runnerStr, ok := runnerIface.(string); ok && runnerStr != "" {
							resp, err := n.ListWallets(owner, &avsproto.ListWalletReq{})
							if err == nil {
								for _, w := range resp.GetItems() {
									if strings.EqualFold(w.GetAddress(), runnerStr) {
										chosenSender = common.HexToAddress(w.GetAddress())
										break
									}
								}
							}
						}
					}
				}
			}
			if (chosenSender == common.Address{}) {
				return nil, fmt.Errorf("runner does not match any existing smart wallet for owner %s", owner.Hex())
			}
			vm.AddVar("aa_sender", chosenSender.Hex())
			if n.logger != nil {
				n.logger.Info("SimulateTask: AA sender resolved", "sender", chosenSender.Hex())
			}
		} else if n.logger != nil {
			n.logger.Info("SimulateTask: Skipping AA sender resolution (no AA-relevant nodes in workflow)")
		}
	}

	// Add chain name to workflowContext if token enrichment service is available
	if n.tokenEnrichmentService != nil {
		chainId := n.tokenEnrichmentService.GetChainID()
		n.logger.Info("🔗 Engine: Adding chain name to VM", "chainId", chainId)
		vm.WithChainName(chainId)
	} else {
		n.logger.Warn("⚠️ Engine: No token enrichment service available for chain name")
	}

	// Add input variables to VM for template processing
	// Apply dual-access mapping to enable both camelCase and snake_case field access
	processedInputVariables := inputVariables
	for key, processedValue := range processedInputVariables {
		vm.AddVar(key, processedValue)
	}

	// Step 6: Add trigger data and input data together for JavaScript access
	// This ensures scripts can access both trigger.data and trigger.input
	// The buildTriggerDataMap function will handle EventTrigger data extraction internally
	triggerDataMap := buildTriggerDataMap(triggerReason.Type, triggerOutput)

	// Debug logging for EventTriggers to track data extraction
	if triggerReason.Type == avsproto.TriggerType_TRIGGER_TYPE_EVENT {
		n.logger.Debug("🔍 SimulateTask: EventTrigger data extraction", "inputKeys", getMapKeys(triggerOutput), "outputKeys", getMapKeys(triggerDataMap))
	}

	// Extract trigger config data if available
	triggerInputData := TaskTriggerToConfig(task.Trigger)

	// Build complete trigger variable data using shared function
	triggerVarData := buildTriggerVariableData(task.Trigger, triggerDataMap, triggerInputData)

	// Add the complete trigger variable with the actual trigger name for JavaScript access
	vm.AddVar(sanitizeTriggerNameForJS(trigger.GetName()), triggerVarData)

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

	// Use the trigger config data for the execution step's Config field (includes data, headers, pathParams for ManualTrigger)
	// The Config field should show the configuration data used to execute the trigger
	var triggerConfigProto *structpb.Value
	triggerInputData = TaskTriggerToConfig(task.Trigger)
	if len(triggerInputData) > 0 {
		var err error
		triggerConfigProto, err = structpb.NewValue(triggerInputData)
		if err != nil {
			n.logger.Warn("Failed to convert trigger input to protobuf", "error", err)
			// Try a fallback approach: convert to JSON and back to ensure proper formatting
			jsonBytes, jsonErr := json.Marshal(triggerInputData)
			if jsonErr == nil {
				var cleanData interface{}
				if unmarshalErr := json.Unmarshal(jsonBytes, &cleanData); unmarshalErr == nil {
					if inputProto, err := structpb.NewValue(cleanData); err == nil {
						triggerConfigProto = inputProto
						n.logger.Info("✅ Successfully converted trigger input using JSON fallback")
					}
				}
			}
		}
	} else {
		n.logger.Info("🔍 SimulateTask: No trigger input found", "trigger_id", task.Trigger.Id, "trigger_type", task.Trigger.GetType())
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
		Config:  triggerConfigProto,             // Include trigger configuration data for debugging
	}

	// Attach execution_context on trigger step
	if vm != nil {
		provider := "real"
		if vm.IsSimulation {
			provider = "tenderly"
		}
		ctxMap := map[string]interface{}{
			"is_simulated": vm.IsSimulation,
			"provider":     provider,
		}
		if vm.smartWalletConfig != nil && vm.smartWalletConfig.ChainID != 0 {
			ctxMap["chain_id"] = vm.smartWalletConfig.ChainID
		}
		if ctxVal, err := structpb.NewValue(ctxMap); err == nil {
			triggerStep.ExecutionContext = ctxVal
		}
	}

	// Set trigger output data in the step using shared function
	triggerStep.OutputData = buildExecutionStepOutputData(queueData.TriggerType, triggerOutputProto)

	// Add trigger step to execution logs
	vm.ExecutionLogs = append(vm.ExecutionLogs, triggerStep)

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
			return nil, status.Errorf(codes.NotFound, TaskNotFoundError)
		}

		if !task.OwnedBy(user.Address) {
			return nil, status.Errorf(codes.NotFound, TaskNotFoundError)
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
		return nil, status.Errorf(codes.Code(avsproto.ErrorCode_STORAGE_UNAVAILABLE), StorageUnavailableError)
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
		return nil, status.Errorf(codes.NotFound, ExecutionNotFoundError)
	}

	exec := &avsproto.Execution{}
	err = protojson.Unmarshal(rawExecution, exec)
	if err != nil {
		return nil, status.Errorf(codes.Code(avsproto.ErrorCode_TASK_DATA_CORRUPTED), TaskStorageCorruptedError)
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
			return nil, status.Errorf(codes.Code(avsproto.ErrorCode_TASK_DATA_CORRUPTED), TaskStorageCorruptedError)
		}

		if exec.Success {
			return &avsproto.ExecutionStatusResp{Status: avsproto.ExecutionStatus_EXECUTION_STATUS_COMPLETED}, nil
		} else {
			return &avsproto.ExecutionStatusResp{Status: avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED}, nil
		}
	}

	// Check if it's pending in queue
	execStatus, err := n.getExecutionStatusFromQueue(task, payload.ExecutionId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, ExecutionNotFoundError)
	}

	return &avsproto.ExecutionStatusResp{Status: *execStatus}, nil
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
			return nil, status.Errorf(codes.Internal, "Internal error counting execution")
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
		return nil, status.Errorf(codes.Internal, "Internal error counting execution")
	}

	return &avsproto.GetExecutionCountResp{
		Total: total,
	}, nil
}

func (n *Engine) DeleteTaskByUser(user *model.User, taskID string) (*avsproto.DeleteTaskResp, error) {
	n.logger.Info("🔄 Starting delete task operation", "task_id", taskID, "user", user.Address.String())

	task, err := n.GetTask(user, taskID)
	if err != nil {
		n.logger.Warn("❌ Task not found for deletion", "task_id", taskID, "error", err)
		return &avsproto.DeleteTaskResp{
			Success: false,
			Status:  "not_found",
			Message: "Task not found",
			Id:      taskID,
		}, nil
	}

	n.logger.Info("✅ Retrieved task for deletion", "task_id", taskID, "status", task.Status)

	if task.Status == avsproto.TaskStatus_Executing {
		n.logger.Warn("❌ Cannot delete executing task", "task_id", taskID, "status", task.Status)
		return &avsproto.DeleteTaskResp{
			Success:        false,
			Status:         "cannot_delete",
			Message:        "Only non executing task can be deleted",
			Id:             taskID,
			PreviousStatus: getTaskStatusString(task.Status),
		}, nil
	}

	previousStatus := getTaskStatusString(task.Status)
	deletedAt := time.Now().UnixMilli()

	n.logger.Info("🗑️ Deleting task storage", "task_id", taskID)
	if err := n.db.Delete(TaskStorageKey(task.Id, task.Status)); err != nil {
		n.logger.Error("failed to delete task storage", "error", err, "task_id", task.Id)
		return &avsproto.DeleteTaskResp{
			Success: false,
			Status:  "error",
			Message: fmt.Sprintf("Failed to delete task: %v", err),
			Id:      taskID,
		}, nil
	}

	n.logger.Info("🗑️ Deleting task user key", "task_id", taskID)
	if err := n.db.Delete(TaskUserKey(task)); err != nil {
		n.logger.Error("failed to delete task user key", "error", err, "task_id", task.Id)
		return &avsproto.DeleteTaskResp{
			Success: false,
			Status:  "error",
			Message: fmt.Sprintf("Failed to delete task user key: %v", err),
			Id:      taskID,
		}, nil
	}

	n.logger.Info("📢 Starting operator notifications", "task_id", taskID)
	n.notifyOperatorsTaskOperation(taskID, avsproto.MessageOp_DeleteTask)
	n.logger.Info("✅ Delete task operation completed", "task_id", taskID)

	return &avsproto.DeleteTaskResp{
		Success:        true,
		Status:         "deleted",
		Message:        "Task deleted successfully",
		DeletedAt:      deletedAt,
		Id:             taskID,
		PreviousStatus: previousStatus,
	}, nil
}

func (n *Engine) CancelTaskByUser(user *model.User, taskID string) (*avsproto.CancelTaskResp, error) {
	task, err := n.GetTask(user, taskID)
	if err != nil {
		return &avsproto.CancelTaskResp{
			Success: false,
			Status:  "not_found",
			Message: "Task not found",
			Id:      taskID,
		}, nil
	}

	if task.Status != avsproto.TaskStatus_Active {
		statusMsg := "already_cancelled"
		if task.Status == avsproto.TaskStatus_Completed {
			statusMsg = "cannot_cancel"
		} else if task.Status == avsproto.TaskStatus_Failed {
			statusMsg = "cannot_cancel"
		}

		return &avsproto.CancelTaskResp{
			Success:        false,
			Status:         statusMsg,
			Message:        fmt.Sprintf("Only active task can be cancelled, current status: %s", getTaskStatusString(task.Status)),
			Id:             taskID,
			PreviousStatus: getTaskStatusString(task.Status),
		}, nil
	}

	updates := map[string][]byte{}
	oldStatus := task.Status
	task.SetCanceled()
	// TaskStorageKey now needs task.Status which is Canceled
	taskJSON, err := task.ToJSON() // Re-serialize task with new status
	if err != nil {
		return &avsproto.CancelTaskResp{
			Success:        false,
			Status:         "error",
			Message:        fmt.Sprintf("Failed to serialize canceled task: %v", err),
			Id:             taskID,
			PreviousStatus: getTaskStatusString(oldStatus),
		}, nil
	}
	updates[string(TaskStorageKey(task.Id, task.Status))] = taskJSON // Use new status for the key where it's stored
	updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", task.Status))

	cancelledAt := time.Now().UnixMilli()

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
		return &avsproto.CancelTaskResp{
			Success:        false,
			Status:         "error",
			Message:        fmt.Sprintf("Failed to update task status: %v", err),
			Id:             taskID,
			PreviousStatus: getTaskStatusString(oldStatus),
		}, nil
	}

	n.notifyOperatorsTaskOperation(taskID, avsproto.MessageOp_CancelTask)

	return &avsproto.CancelTaskResp{
		Success:        true,
		Status:         "cancelled",
		Message:        "Task cancelled successfully",
		CancelledAt:    cancelledAt,
		Id:             taskID,
		PreviousStatus: getTaskStatusString(oldStatus),
	}, nil
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
		return false, status.Errorf(codes.InvalidArgument, "secret name cannot start with ap_")
	}

	if len(payload.Name) == 0 || len(payload.Name) > MaxSecretNameLength {
		return false, status.Errorf(codes.InvalidArgument, "secret name length is invalid: should be 1-255 character")
	}

	key, _ := SecretStorageKey(secret)
	updates[key] = []byte(payload.Secret)
	err := n.db.BatchWrite(updates)
	if err == nil {
		return true, nil
	}

	return false, status.Errorf(codes.Internal, "Cannot save data")
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
		return false, status.Errorf(codes.NotFound, "Secret not found")
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

func (n *Engine) DeleteSecret(user *model.User, payload *avsproto.DeleteSecretReq) (*avsproto.DeleteSecretResp, error) {
	// No need to check permission, the key is prefixed by user eoa already
	secret := &model.Secret{
		Name:       payload.Name,
		User:       user,
		OrgID:      payload.OrgId,
		WorkflowID: payload.WorkflowId,
	}
	key, _ := SecretStorageKey(secret)

	// Check if secret exists before attempting to delete
	exists, err := n.db.Exist([]byte(key))
	if err != nil {
		return &avsproto.DeleteSecretResp{
			Success:    false,
			Status:     "error",
			Message:    fmt.Sprintf("Error checking secret existence: %v", err),
			SecretName: payload.Name,
		}, err
	}

	if !exists {
		return &avsproto.DeleteSecretResp{
			Success:    true,
			Status:     "not_found",
			Message:    "Secret not found",
			SecretName: payload.Name,
		}, nil
	}

	// Attempt to delete the secret
	err = n.db.Delete([]byte(key))
	if err != nil {
		return &avsproto.DeleteSecretResp{
			Success:    false,
			Status:     "error",
			Message:    fmt.Sprintf("Error deleting secret: %v", err),
			SecretName: payload.Name,
		}, err
	}

	// Determine scope for response
	scope := "user"
	if payload.OrgId != "" {
		scope = "org"
	} else if payload.WorkflowId != "" {
		scope = "workflow"
	}

	return &avsproto.DeleteSecretResp{
		Success:    true,
		Status:     "deleted",
		Message:    "Secret successfully deleted",
		DeletedAt:  time.Now().UnixMilli(),
		SecretName: payload.Name,
		Scope:      scope,
	}, nil
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

		if len(activeOperators) == 0 {
			// No active operators to reassign to - leave tasks unassigned for now
			// They will be reclaimed when operators reconnect
			n.logger.Info("⏸️ No active operators available - orphaned tasks will be reclaimed on operator reconnection",
				"orphaned_count", len(orphanedTasks))
			return
		}

		// Track reassignments by operator
		reassignmentsByOperator := make(map[string][]string)

		// Reassign orphaned tasks
		for _, taskID := range orphanedTasks {
			if task, exists := n.tasks[taskID]; exists {
				assignedOperator := n.assignTaskToOperator(task)
				if assignedOperator != "" {
					reassignmentsByOperator[assignedOperator] = append(reassignmentsByOperator[assignedOperator], taskID)
				}
			}
		}

		// Log aggregated reassignments per operator
		for operatorAddr, taskIDs := range reassignmentsByOperator {
			n.logger.Info("🔄 Reassigned tasks to operator",
				"operator", operatorAddr,
				"operation", "MonitorTaskTrigger",
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
			return nil, status.Errorf(codes.Internal, "Internal error retrieving tasks")
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
		return nil, status.Errorf(codes.Internal, "Internal error counting workflow")
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

// GetTokenMetadata handles the RPC for token metadata lookup
func (n *Engine) GetTokenMetadata(user *model.User, payload *avsproto.GetTokenMetadataReq) (*avsproto.GetTokenMetadataResp, error) {
	// Validate the address parameter
	if payload.Address == "" {
		return &avsproto.GetTokenMetadataResp{
			Found: false,
		}, status.Errorf(codes.InvalidArgument, "token address is required")
	}

	// Check if address is a valid hex address
	if !common.IsHexAddress(payload.Address) {
		return &avsproto.GetTokenMetadataResp{
			Found: false,
		}, status.Errorf(codes.InvalidArgument, "invalid token address format")
	}

	// Check if TokenEnrichmentService is available
	if n.tokenEnrichmentService == nil {
		return &avsproto.GetTokenMetadataResp{
			Found: false,
		}, status.Errorf(codes.Unavailable, "token enrichment service not available")
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
//   - *avsproto.EventTrigger_Output: properly structured protobuf output with structured data
func buildEventTriggerOutput(triggerOutput map[string]interface{}) *avsproto.EventTrigger_Output {
	eventOutput := &avsproto.EventTrigger_Output{}

	// Check if we have event data and populate appropriately
	if triggerOutput != nil {
		// Check if we found events
		if found, ok := triggerOutput["found"].(bool); ok && found {
			// Extract the data from the trigger output
			if data, ok := triggerOutput["data"]; ok {
				var dataToConvert interface{}
				var shouldConvert bool

				// Handle different data types: JSON string, map, or other types
				switch d := data.(type) {
				case string:
					// Try to parse as JSON string
					var parsedData interface{}
					if err := json.Unmarshal([]byte(d), &parsedData); err == nil {
						dataToConvert = parsedData
						shouldConvert = true
					} else {
						// If not valid JSON, treat as plain string (but only if non-empty)
						if d != "" {
							dataToConvert = d
							shouldConvert = true
						}
					}
				case map[string]interface{}:
					// Direct map data - always valid
					dataToConvert = d
					shouldConvert = true
				default:
					// Other types (int, bool, etc.) are considered invalid for event data
					// in the defensive programming context - skip conversion
					shouldConvert = false
				}

				// Convert to google.protobuf.Value only if we have valid data
				if shouldConvert {
					// Convert data to protobuf-compatible format before serialization
					compatibleData := convertToProtobufCompatible(dataToConvert)
					if protoValue, err := structpb.NewValue(compatibleData); err == nil {
						eventOutput.Data = protoValue
					}
				}
			}
		}
		// If no events found, eventOutput remains with empty data field
	}

	return eventOutput
}

// buildBlockTriggerOutput creates a BlockTrigger_Output from raw trigger output data.
// This shared function eliminates code duplication between SimulateTask, RunTriggerRPC,
// and regular task execution flows.
//
// IMPORTANT: Type Conversion Limitation
// =====================================
// This function converts uint64 values to protobuf Value structures using structpb.NewValue().
// Due to protobuf's internal use of JSON, all numeric types get converted to float64.
// This means:
//   - Input:  blockNumber: uint64(12345)
//   - Output: blockNumber: float64(12345) (via protobuf)
//
// This type conversion happens because:
// 1. structpb.NewValue() uses JSON internally
// 2. JSON only has one numeric type (float64 in Go)
// 3. All integers get converted to float64
//
// Impact:
// - buildTriggerDataMap() preserves uint64 types (works with raw data)
// - buildTriggerDataMapFromProtobuf() returns float64 types (works with protobuf data)
// - This creates inconsistency between different data paths
//
// Client Consistency Requirement:
// The key requirement is that client input should match execution step output.
// As long as users get back the same values they provided, the internal conversion is acceptable.
//
// Potential Solutions:
// 1. Avoid protobuf conversion when not needed (preserve raw data) - requires major refactoring
// 2. Use custom protobuf types that preserve integer types - complex implementation
// 3. Accept the conversion and ensure client consistency - current approach
//
// Currently using solution #3: clients typically send JSON with float64 numbers anyway,
// so the protobuf conversion maintains consistency from the client perspective.
// Tests verify that user input matches execution step output values and types.
//
// Parameters:
//   - triggerOutput: map containing raw trigger output data from runBlockTriggerImmediately
//
// Returns:
//   - *avsproto.BlockTrigger_Output: properly structured protobuf output with block data
//     (note: numeric values will be float64 due to protobuf conversion)
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

	// Create the data structure with all block information
	blockData := map[string]interface{}{
		"blockNumber": blockNumber,
		"blockHash":   blockHash,
		"timestamp":   timestamp,
		"parentHash":  parentHash,
		"difficulty":  difficulty,
		"gasLimit":    gasLimit,
		"gasUsed":     gasUsed,
	}

	// Convert to protobuf Value
	dataValue, err := structpb.NewValue(blockData)
	if err != nil {
		// Fallback to empty data on error
		dataValue, _ = structpb.NewValue(map[string]interface{}{})
	}

	return &avsproto.BlockTrigger_Output{
		Data: dataValue,
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

	// Create the data structure with timestamp information
	timeData := map[string]interface{}{
		"timestamp":    timestamp,
		"timestampIso": timestampISO,
	}

	// Convert to protobuf Value
	dataValue, err := structpb.NewValue(timeData)
	if err != nil {
		// Fallback to empty data on error
		dataValue, _ = structpb.NewValue(map[string]interface{}{})
	}

	return &avsproto.FixedTimeTrigger_Output{
		Data: dataValue,
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

	// Create the data structure with timestamp information
	cronData := map[string]interface{}{
		"timestamp":    timestamp,
		"timestampIso": timestampISO,
	}

	// Convert to protobuf Value
	dataValue, err := structpb.NewValue(cronData)
	if err != nil {
		// Fallback to empty data on error
		dataValue, _ = structpb.NewValue(map[string]interface{}{})
	}

	return &avsproto.CronTrigger_Output{
		Data: dataValue,
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
//   - *avsproto.ManualTrigger_Output: properly structured protobuf output with only the parsed data
func buildManualTriggerOutput(triggerOutput map[string]interface{}) *avsproto.ManualTrigger_Output {
	var data *structpb.Value

	if triggerOutput != nil {
		// Include ONLY the user-defined JSON data - this is the main payload for manual triggers
		// Headers and pathParams are only used for configuration, not output
		if dataValue, exists := triggerOutput["data"]; exists && dataValue != nil {
			// Convert any valid JSON data (objects, arrays, etc.) to protobuf Value
			if pbValue, err := structpb.NewValue(dataValue); err == nil {
				data = pbValue
			}
		}
	}

	result := &avsproto.ManualTrigger_Output{
		Data: data,
		// Headers and PathParams are removed from output - they're config-only fields
	}
	return result
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
		// For manual triggers, include all fields (data, headers, pathParams) for template access
		// This allows templates to access ManualTrigger.data.field, ManualTrigger.headers.field, etc.
		for k, v := range triggerOutput {
			triggerDataMap[k] = v
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
			// For EventTriggers, check if this is a simulation result structure
			// Simulation results should have "found", "metadata", and "data" fields
			if _, hasFound := triggerOutput["found"]; hasFound {
				if _, hasMetadata := triggerOutput["metadata"]; hasMetadata {
					if eventData, hasEventData := triggerOutput["data"].(map[string]interface{}); hasEventData {
						// Extract the actual event data from the nested "data" field
						for k, v := range eventData {
							triggerDataMap[k] = v
						}
					} else {
						// No valid data field in simulation result - copy all data as-is
						for k, v := range triggerOutput {
							triggerDataMap[k] = v
						}
					}
				} else {
					// Not a complete simulation result structure - copy all data as-is
					for k, v := range triggerOutput {
						triggerDataMap[k] = v
					}
				}
			} else {
				// Not a simulation result structure - this should be actual event data
				// Copy all event trigger data directly
				for k, v := range triggerOutput {
					triggerDataMap[k] = v
				}
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
				// Empty EventTrigger output with no data
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
				// Empty EventTrigger output with no data
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
			// Empty EventTrigger output with no data
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
		if manualOutput, ok := triggerOutputProto.(*avsproto.ManualTrigger_Output); ok && manualOutput.Data != nil {
			triggerDataMap["data"] = manualOutput.Data.AsInterface()
		} else if mapOutput, ok := triggerOutputProto.(map[string]interface{}); ok {
			if dataField, exists := mapOutput["data"]; exists {
				triggerDataMap["data"] = dataField
			}
		}
	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
		if timeOutput, ok := triggerOutputProto.(*avsproto.FixedTimeTrigger_Output); ok {
			// Extract data from the new standardized data field
			if timeOutput.Data != nil {
				dataMap := gow.ValueToMap(timeOutput.Data)
				if dataMap != nil {
					if timestamp, ok := dataMap["timestamp"]; ok {
						triggerDataMap["timestamp"] = timestamp
					}
					if timestampIso, ok := dataMap["timestampIso"]; ok {
						triggerDataMap["timestamp_iso"] = timestampIso
					}
				}
			}
		}
	case avsproto.TriggerType_TRIGGER_TYPE_CRON:
		if cronOutput, ok := triggerOutputProto.(*avsproto.CronTrigger_Output); ok {
			// Extract data from the new standardized data field
			if cronOutput.Data != nil {
				dataMap := gow.ValueToMap(cronOutput.Data)
				if dataMap != nil {
					if timestamp, ok := dataMap["timestamp"]; ok {
						triggerDataMap["timestamp"] = timestamp
					}
					if timestampIso, ok := dataMap["timestampIso"]; ok {
						triggerDataMap["timestamp_iso"] = timestampIso
					}
				}
			}
		}
	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		if blockOutput, ok := triggerOutputProto.(*avsproto.BlockTrigger_Output); ok {
			// Extract data from the new standardized data field
			if blockOutput.Data != nil {
				dataMap := gow.ValueToMap(blockOutput.Data)
				if dataMap != nil {
					if blockNumber, ok := dataMap["blockNumber"]; ok {
						triggerDataMap["blockNumber"] = blockNumber
					}
					if blockHash, ok := dataMap["blockHash"]; ok {
						triggerDataMap["blockHash"] = blockHash
					}
					if timestamp, ok := dataMap["timestamp"]; ok {
						triggerDataMap["timestamp"] = timestamp
					}
					if parentHash, ok := dataMap["parentHash"]; ok {
						triggerDataMap["parentHash"] = parentHash
					}
					if difficulty, ok := dataMap["difficulty"]; ok {
						triggerDataMap["difficulty"] = difficulty
					}
					if gasLimit, ok := dataMap["gasLimit"]; ok {
						triggerDataMap["gasLimit"] = gasLimit
					}
					if gasUsed, ok := dataMap["gasUsed"]; ok {
						triggerDataMap["gasUsed"] = gasUsed
					}
				}
			}
		} else if rawMap, ok := triggerOutputProto.(map[string]interface{}); ok {
			// Handle raw map data (from queue/storage) - convert snake_case to camelCase
			if blockNumber, exists := rawMap["block_number"]; exists {
				triggerDataMap["blockNumber"] = blockNumber
			}
			if blockHash, exists := rawMap["block_hash"]; exists {
				triggerDataMap["blockHash"] = blockHash
			}
			if timestamp, exists := rawMap["timestamp"]; exists {
				triggerDataMap["timestamp"] = timestamp
			}
			if parentHash, exists := rawMap["parent_hash"]; exists {
				triggerDataMap["parentHash"] = parentHash
			}
			if difficulty, exists := rawMap["difficulty"]; exists {
				triggerDataMap["difficulty"] = difficulty
			}
			if gasLimit, exists := rawMap["gas_limit"]; exists {
				triggerDataMap["gasLimit"] = gasLimit
			}
			if gasUsed, exists := rawMap["gas_used"]; exists {
				triggerDataMap["gasUsed"] = gasUsed
			}
		}
	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		if eventOutput, ok := triggerOutputProto.(*avsproto.EventTrigger_Output); ok {
			// With new structured data, convert the protobuf value to map
			if eventOutput.Data != nil {
				// Convert google.protobuf.Value to map[string]interface{}
				if eventData, ok := eventOutput.Data.AsInterface().(map[string]interface{}); ok {
					// Copy all parsed event data to the trigger data map
					for k, v := range eventData {
						triggerDataMap[k] = v
					}
				} else if logger != nil {
					logger.Warn("Failed to convert event trigger data to map", "data_type", fmt.Sprintf("%T", eventOutput.Data.AsInterface()))
				}
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

// DetectAndHandleInvalidTasks scans for tasks with invalid configurations
// and either marks them as failed or removes them based on the strategy
func (n *Engine) DetectAndHandleInvalidTasks() error {
	n.logger.Info("🔍 Scanning for tasks with invalid configurations...")

	invalidTasks := []string{}
	updates := make(map[string][]byte)

	// Acquire lock once for the entire operation to reduce lock contention
	n.lock.Lock()

	// Scan through all tasks in memory and prepare updates
	for taskID, task := range n.tasks {
		if err := task.ValidateWithError(); err != nil {
			invalidTasks = append(invalidTasks, taskID)
			n.logger.Warn("🚨 Found invalid task configuration",
				"task_id", taskID,
				"error", err.Error())

			// Mark task as failed and prepare storage updates
			task.SetFailed()

			taskJSON, err := task.ToJSON()
			if err != nil {
				n.logger.Error("Failed to serialize invalid task for cleanup",
					"task_id", taskID,
					"error", err)
				continue
			}

			// Prepare the task status update in storage
			updates[string(TaskStorageKey(task.Id, task.Status))] = taskJSON
			updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", avsproto.TaskStatus_Failed))
		}
	}

	n.lock.Unlock()

	if len(invalidTasks) == 0 {
		n.logger.Info("✅ No invalid tasks found")
		return nil
	}

	n.logger.Warn("🚨 Found invalid tasks, marking as failed",
		"count", len(invalidTasks),
		"task_ids", invalidTasks)

	// Batch write the updates
	if len(updates) > 0 {
		if err := n.db.BatchWrite(updates); err != nil {
			n.logger.Error("Failed to update invalid tasks in storage",
				"error", err)
			return err
		}
	}

	n.logger.Info("✅ Successfully marked invalid tasks as failed",
		"count", len(invalidTasks))

	return nil
}
