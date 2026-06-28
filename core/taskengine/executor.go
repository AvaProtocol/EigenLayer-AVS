package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/getsentry/sentry-go"

	"github.com/AvaProtocol/EigenLayer-AVS/core/apqueue"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	storageschema "github.com/AvaProtocol/EigenLayer-AVS/storage/schema"
	"github.com/oklog/ulid/v2"
)

// reconstructTriggerOutputData is a helper function to reconstruct trigger output data
// from JSON-decoded maps back to protobuf Values. This reduces duplication across
// different trigger types that all follow the same pattern.
func reconstructTriggerOutputData(m map[string]interface{}, logger sdklogging.Logger) *structpb.Value {
	if m == nil {
		return nil
	}

	if raw, exists := m["data"]; exists && raw != nil {
		if pb, err := structpb.NewValue(raw); err == nil {
			return pb
		} else {
			// Log the error to help with debugging malformed trigger output data
			if logger != nil {
				logger.Debug("Failed to reconstruct trigger output data from JSON-decoded map",
					"error", err,
					"raw_data_type", fmt.Sprintf("%T", raw))
			}
		}
	}

	// Fallback: try to convert the entire map if "data" key doesn't exist or is nil
	if pb, err := structpb.NewValue(m); err == nil {
		return pb
	} else {
		if logger != nil {
			logger.Debug("Failed to reconstruct trigger output data from entire map",
				"error", err,
				"map_type", fmt.Sprintf("%T", m))
		}
	}

	return nil
}

func NewExecutor(swConfig *config.SmartWalletConfig, db storage.Storage, logger sdklogging.Logger, engine *Engine, priceService PriceService) *WorkflowExecutor {
	executor := &WorkflowExecutor{
		db:                     db,
		logger:                 logger,
		smartWalletConfig:      swConfig,
		tokenEnrichmentService: GetTokenEnrichmentService(),
		engine:                 engine,
		priceService:           priceService,
	}

	// Initialize fee ledger for value fee tracking
	executor.feeLedger = NewFeeLedger(db, logger)

	return executor
}

// Creates a WorkflowExecutor with nil engine for testing purposes
// Tests should set up a mock engine or use this when atomic indexing is not needed
func NewExecutorForTesting(swCfg *config.SmartWalletConfig, db storage.Storage, logger sdklogging.Logger) *WorkflowExecutor {
	// Initialize a minimal Engine instance so tests exercise production code paths
	// without requiring separate mocks or test-only fallbacks.
	minimalCfg := &config.Config{SmartWallet: swCfg}
	eng := New(db, minimalCfg, nil, logger)
	return &WorkflowExecutor{
		db:                     db,
		logger:                 logger,
		smartWalletConfig:      swCfg,
		tokenEnrichmentService: GetTokenEnrichmentService(),
		engine:                 eng,
	}
}

type WorkflowExecutor struct {
	db                     storage.Storage
	logger                 sdklogging.Logger
	smartWalletConfig      *config.SmartWalletConfig
	tokenEnrichmentService *TokenEnrichmentService
	engine                 *Engine
	feeLedger              *FeeLedger
	priceService           PriceService
}

type QueueExecutionData struct {
	TriggerType    avsproto.TriggerType
	TriggerOutput  interface{} // Will hold the specific trigger output (BlockTrigger.Output, etc.)
	ExecutionID    string
	InputVariables map[string]interface{} // Input variables for template resolution (e.g., settings: {runner, chain_id})
}

func (x *WorkflowExecutor) GetTask(id string) (*model.Workflow, error) {
	task := &model.Workflow{
		Task: &avsproto.Task{},
	}
	// Lookup by task ID alone — search every known chain bucket. The engine
	// reference is always set in production; tests that build a standalone
	// WorkflowExecutor still fall back to the legacy single-chain key.
	var storageKey []byte
	if x.engine != nil {
		storageKey = x.engine.findTaskKey(id, avsproto.TaskStatus_Enabled)
	} else {
		storageKey = storageschema.WorkflowStorageKey(id, avsproto.TaskStatus_Enabled)
	}
	item, err := x.db.GetKey(storageKey)

	if err != nil {
		return nil, fmt.Errorf("storage access failed for key '%s': %w", string(storageKey), err)
	}
	// DiscardUnknown: tolerate proto fields renamed/removed since the
	// task was written. Without this, any task whose body carries
	// old fields like `expression`/`epochs`/`totalExecution` would
	// fail strict decode here and the executor would falsely report
	// the task as "data may be corrupted".
	err = (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(item, task)
	if err != nil {
		return nil, fmt.Errorf("failed to parse task data from storage (data may be corrupted): %w", err)
	}

	// Ensure task is properly initialized after loading from storage
	if initErr := task.EnsureInitialized(); initErr != nil {
		return nil, fmt.Errorf("task failed initialization after loading from storage (ID: %s): %w", task.Id, initErr)
	}

	// Debug: Log FilterNode expressions after task retrieval from storage
	if x.logger != nil {
		for _, node := range task.Nodes {
			if filterNode := node.GetFilter(); filterNode != nil && filterNode.Config != nil {
				x.logger.Info("DEPLOY DEBUG: FilterNode expression after storage retrieval",
					"task_id", task.Id,
					"node_id", node.Id,
					"node_name", node.Name,
					"expression", filterNode.Config.Expression)
			}
		}
	}

	return task, nil
}

func (x *WorkflowExecutor) Perform(job *apqueue.Job) error {
	task, err := x.GetTask(job.Name)

	if err != nil {
		// Provide more specific error information
		if strings.Contains(err.Error(), "key not found") || strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("task not found in storage: %s (task may have been deleted or storage key is incorrect)", job.Name)
		} else if strings.Contains(err.Error(), "unmarshal") || strings.Contains(err.Error(), "json") {
			return fmt.Errorf("task data corruption in storage: %s (stored data is invalid JSON)", job.Name)
		} else {
			return fmt.Errorf("storage error loading task %s: %v", job.Name, err)
		}
	}

	queueData := &QueueExecutionData{}
	// A task executor data is the trigger mark
	// ref: AggregateChecksResult
	err = json.Unmarshal(job.Data, queueData)
	if err != nil {
		return fmt.Errorf("error decode job payload when executing task: %s with job id %d", task.Id, job.ID)
	}

	// Execute the task logic
	_, runErr := x.RunTask(task, queueData)

	if runErr == nil {
		// Task logic executed successfully. Clean up the TaskTriggerKey for this async execution.
		if queueData.ExecutionID != "" { // Assumes `ExecutionID` is always set for queued jobs. Verify this assumption if the logic changes.
			triggerKeyToClean := TaskTriggerKey(task, queueData.ExecutionID)
			if delErr := x.db.Delete(triggerKeyToClean); delErr != nil {
				x.logger.Error("Perform: Failed to delete TaskTriggerKey after successful async execution",
					"key", string(triggerKeyToClean), "task_id", task.Id, "execution_id", queueData.ExecutionID, "error", delErr)
			} else {
				// Successfully deleted, no need for a verbose log here unless for specific debug scenarios
				// x.logger.Info("Perform: Successfully deleted TaskTriggerKey after async execution",
				// 	"key", string(triggerKeyToClean), "task_id", task.Id, "execution_id", queueData.ExecutionID)
			}
		}
		return nil // Job processed successfully
	}

	// If runErr is not nil, the task logic failed.
	// x.logger.Error("Perform: Task execution failed, not deleting TaskTriggerKey.", "task_id", task.Id, "execution_id", queueData.ExecutionID, "error", runErr)
	return runErr // Propagate the error from task execution
}

// RunTask executes a task using a background context. It is retained for the
// async queue path (Perform) and tests, which have no request-scoped context.
func (x *WorkflowExecutor) RunTask(task *model.Workflow, queueData *QueueExecutionData) (*avsproto.Execution, error) {
	return x.RunTaskWithContext(context.Background(), task, queueData)
}

// RunTaskWithContext is RunTask with a caller-supplied context. The ctx is
// propagated to wallet-ownership validation so a cancelled request interrupts
// in-flight chain-worker derivations.
func (x *WorkflowExecutor) RunTaskWithContext(ctx context.Context, task *model.Workflow, queueData *QueueExecutionData) (*avsproto.Execution, error) {
	// Load secrets for the task
	secrets, err := LoadSecretForTask(x.db, task)
	if err != nil {
		x.logger.Warn("Failed to load secrets for task", "error", err, "task_id", task.Id)
		// Don't fail the task, just use empty secrets
		secrets = make(map[string]string)
	}

	if queueData == nil || queueData.ExecutionID == "" {
		return nil, fmt.Errorf("internal error: invalid execution id")
	}

	// Validate all node names for JavaScript compatibility
	if err := validateAllNodeNamesForJavaScript(task); err != nil {
		return nil, fmt.Errorf("node name validation failed: %w", err)
	}

	// Convert queue data back to the format expected by the VM
	triggerReason := GetTriggerReasonOrDefault(queueData, task.Id, x.logger)

	// Debug: Log FilterNode expressions in task before VM creation
	if x.logger != nil {
		for _, node := range task.Nodes {
			if filterNode := node.GetFilter(); filterNode != nil && filterNode.Config != nil {
				x.logger.Info("DEPLOY DEBUG: FilterNode expression before VM creation",
					"task_id", task.Id,
					"node_id", node.Id,
					"node_name", node.Name,
					"expression", filterNode.Config.Expression)
			}
		}
	}

	// Create VM with trigger reason data.
	// In gateway mode, resolve the SmartWalletConfig for the task's target chain.
	// A task carries no chain (G5); the VM's default config is the aggregator
	// default. Each chain-aware node resolves its OWN chain via the
	// chainConfigResolver wired into the VM, so this default is only a fallback.
	swConfig := x.smartWalletConfig
	if x.engine != nil {
		swConfig = x.engine.ResolveSmartWalletConfig(x.engine.defaultChainID())
	}
	vm, err := NewVMWithData(task, triggerReason, swConfig, secrets)
	if err != nil {
		return nil, err
	}
	if x.engine != nil {
		vm.WithChainConfigResolver(x.engine.ResolveSmartWalletConfig)
	}

	// Merge input variables from trigger execution (overrides task-level input variables)
	if queueData != nil && len(queueData.InputVariables) > 0 {
		if x.logger != nil {
			x.logger.Debug("Adding input variables from trigger execution",
				"task_id", task.Id,
				"input_variables_count", len(queueData.InputVariables))
		}
		for key, value := range queueData.InputVariables {
			vm.AddVar(key, value)
		}
	}

	if err != nil {
		return nil, err
	}

	// Check for paymaster override in settings (inputVariables.settings.shouldUsePaymaster)
	// This allows tests and workflows to explicitly control paymaster usage
	if triggerReason != nil && triggerReason.Output != nil {
		if outputData, ok := triggerReason.Output.(*avsproto.ManualTrigger_Output); ok && outputData.Data != nil {
			dataInterface := outputData.Data.AsInterface()
			if dataMap, ok := dataInterface.(map[string]interface{}); ok {
				if settingsIface, hasSettings := dataMap["settings"]; hasSettings {
					if settings, isMap := settingsIface.(map[string]interface{}); isMap {
						// Note: shouldUsePaymaster override has been removed - always use paymaster if configured
						if _, hasLegacyOverride := settings["shouldUsePaymaster"]; hasLegacyOverride {
							x.logger.Warn("Deployed workflow: shouldUsePaymaster override is deprecated and ignored",
								"reason", "paymaster is always used if configured",
								"task_id", task.Id)
						}
					}
				}
			}
		}
	}

	// Extract chainId from enriched event data first, fallback to tokenEnrichmentService
	var chainId uint64
	var chainIdSource string

	// Try to extract chainId from enriched event data in the trigger reason
	if triggerReason != nil && triggerReason.Output != nil {
		// Handle the enriched data format that survives JSON serialization
		if enrichedDataMap, ok := triggerReason.Output.(map[string]interface{}); ok {
			if enrichedData, hasEnrichedData := enrichedDataMap["enriched_data"].(map[string]interface{}); hasEnrichedData {
				// Try different numeric types for chainId
				if chainIdFloat, ok := enrichedData["chainId"].(float64); ok {
					chainId = uint64(chainIdFloat)
					chainIdSource = "enriched_event_data"
				} else if chainIdInt, ok := enrichedData["chainId"].(int64); ok {
					chainId = uint64(chainIdInt)
					chainIdSource = "enriched_event_data"
				} else if chainIdUint, ok := enrichedData["chainId"].(uint64); ok {
					chainId = chainIdUint
					chainIdSource = "enriched_event_data"
				}
			}
		}
	}

	// Fallback to tokenEnrichmentService if chainId not found in enriched data
	if chainId == 0 && x.tokenEnrichmentService != nil {
		chainId = x.tokenEnrichmentService.GetChainID()
		chainIdSource = "tokenEnrichmentService"
	}

	// chainId is used by execution context (set in vm.smartWalletConfig.ChainID)
	if chainId > 0 && x.logger != nil {
		x.logger.Debug("Executor: Chain ID resolved", "chainId", chainId, "source", chainIdSource)
	}

	// Debug: Log FilterNode expressions in VM after creation
	if x.logger != nil {
		for nodeID, taskNode := range vm.TaskNodes {
			if filterNode := taskNode.GetFilter(); filterNode != nil && filterNode.Config != nil {
				x.logger.Info("DEPLOY DEBUG: FilterNode expression after VM creation",
					"task_id", task.Id,
					"node_id", nodeID,
					"node_name", taskNode.Name,
					"expression", filterNode.Config.Expression)
			}
		}
	}

	vm.WithLogger(x.logger).WithDb(x.db)

	// Convert execution fee (USD) → Wei and set on VM for UserOp injection
	if x.priceService != nil && x.engine.config.FeeRates != nil {
		chainID := int64(0)
		if x.smartWalletConfig != nil {
			chainID = x.smartWalletConfig.ChainID
		}
		if feeWei, convErr := ConvertUSDToWei(x.engine.config.FeeRates.ExecutionFeeUSD, x.priceService, chainID); convErr == nil {
			vm.executionFeeWei = feeWei
			x.logger.Debug("Execution fee set on VM", "fee_wei", feeWei.String(), "fee_usd", x.engine.config.FeeRates.ExecutionFeeUSD)
		} else {
			x.logger.Warn("Failed to convert execution fee to Wei, proceeding without fee", "error", convErr)
		}
	}

	// Initialize timing and execution record BEFORE validation to ensure we always have a record
	t0 := time.Now()
	task.ExecutionCount += 1
	task.LastRanAt = t0.UnixMilli()
	initialTaskStatus := task.Status

	// Check if there's a pre-assigned execution index from non-blocking trigger first
	var executionIndex int64
	pendingKey := PendingExecutionKey(task, queueData.ExecutionID)
	if pendingData, err := x.db.GetKey(pendingKey); err == nil {
		// Try to parse the pre-assigned index from pending storage
		if storedIndex, parseErr := strconv.ParseInt(string(pendingData), 10, 64); parseErr == nil {
			executionIndex = storedIndex
			x.logger.Info("Using pre-assigned execution index from pending storage",
				"task_id", task.Id, "execution_id", queueData.ExecutionID, "index", executionIndex)

			// Now that we've used the pre-assigned index, clean up the pending key
			if deleteErr := x.db.Delete(pendingKey); deleteErr != nil {
				x.logger.Warn("Failed to delete pending key after using pre-assigned index",
					"task_id", task.Id, "execution_id", queueData.ExecutionID, "error", deleteErr)
			}
		} else {
			// Pending data exists but not a valid index; require engine to assign atomically
			if x.engine == nil {
				return nil, fmt.Errorf("engine is not initialized; cannot assign execution index")
			}
			newIndex, indexErr := x.engine.AssignNextExecutionIndex(task)
			if indexErr != nil {
				x.logger.Error("Failed to assign execution index", "task_id", task.Id, "execution_id", queueData.ExecutionID, "error", indexErr)
				return nil, fmt.Errorf("failed to assign execution index: %w", indexErr)
			}
			executionIndex = newIndex
		}
	} else {
		// No pending data found, assign new atomic index for blocking executions
		if x.engine == nil {
			return nil, fmt.Errorf("engine is not initialized; cannot assign execution index")
		}
		newIndex, indexErr := x.engine.AssignNextExecutionIndex(task)
		if indexErr != nil {
			x.logger.Error("Failed to assign execution index", "task_id", task.Id, "execution_id", queueData.ExecutionID, "error", indexErr)
			return nil, fmt.Errorf("failed to assign execution index: %w", indexErr)
		}
		executionIndex = newIndex
	}

	// Set execution index on VM directly (used by email subject formatting "Run #X:").
	vm.ExecutionIndex = executionIndex

	// Update the context var with the post-increment executionCount.
	// executionCount was already incremented above (task.ExecutionCount += 1) but the VM's
	// context still has the pre-increment value from vm.go.
	vm.mu.Lock()
	if ctx, ok := vm.vars[ContextVarName].(map[string]interface{}); ok {
		ctx["executionCount"] = task.ExecutionCount
	}
	vm.mu.Unlock()

	// Create execution record immediately - this ensures we have a record even if validation fails
	execution := &avsproto.Execution{
		Id:      queueData.ExecutionID,
		StartAt: t0.UnixMilli(),
		EndAt:   0,                                                 // Will be set when execution completes or fails
		Status:  avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING, // Default to pending, will be updated based on results
		Error:   "",                                                // Will be populated if there are errors
		Steps:   []*avsproto.Execution_Step{},                      // Will be populated during execution
		Index:   executionIndex,                                    // Atomic execution index assignment
	}

	// Wallet validation - if this fails, we'll record the failure and return the execution record
	if task != nil && task.Owner != "" {
		if task.SmartWalletAddress == "" || !common.IsHexAddress(task.SmartWalletAddress) {
			execution.EndAt = time.Now().UnixMilli()
			execution.Error = "invalid or missing task smart wallet address for deployed run"
			execution.Status = avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED
			x.persistFailedExecution(task, execution, initialTaskStatus)
			return execution, nil // Return execution record with failure details
		}
		owner := common.HexToAddress(task.Owner)
		user := &model.User{Address: owner}
		smartWalletAddr := common.HexToAddress(task.SmartWalletAddress)

		// Enhanced wallet validation that handles any legitimately derived wallet
		isValid, err := x.validateWalletOwnership(ctx, x.engine.defaultChainID(), user, smartWalletAddr)
		if err == nil && !isValid {
			// The smart-wallet runner address is chain-invariant across configured
			// chains, but its ownership record (w:<chain>:...) may have been written
			// under a different chain than the gateway default. Mirror the create-time
			// cross-chain check (userOwnsWalletOnAnyChain) so a wallet registered on a
			// non-default chain isn't spuriously rejected at execution → auto-disabled.
			if ok, ownErr := x.engine.userOwnsWalletOnAnyChain(user, smartWalletAddr); ownErr == nil && ok {
				isValid = true
			}
		}
		if err != nil {
			execution.EndAt = time.Now().UnixMilli()
			execution.Error = fmt.Sprintf("failed to validate wallet ownership for owner %s: %v", owner.Hex(), err)
			execution.Status = avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED
			x.persistFailedExecution(task, execution, initialTaskStatus)
			return execution, nil // Return execution record with failure details
		}
		if !isValid {
			execution.EndAt = time.Now().UnixMilli()
			execution.Error = "task smart wallet address does not belong to owner"
			execution.Status = avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED
			x.persistFailedExecution(task, execution, initialTaskStatus)
			return execution, nil // Return execution record with failure details
		}
		vm.AddVar("aa_sender", common.HexToAddress(task.SmartWalletAddress).Hex())
		if x.logger != nil {
			x.logger.Info("Executor: AA sender resolved", "sender", task.SmartWalletAddress)
		}

		// Look up the wallet's salt from DB for auto-deployment of non-salt-0 wallets.
		// The salt is chain-invariant (same salt derives the same address on every
		// configured chain), but the record (w:<chain>:...) is keyed by the chain it
		// was registered on, which may differ from the gateway default. Scan all
		// configured chains so the salt isn't missed → wallet auto-deployed wrong.
		if x.db != nil {
			for _, walletChainID := range x.engine.knownChainIDs() {
				if wallet, walletErr := GetWallet(x.db, walletChainID, owner, task.SmartWalletAddress); walletErr == nil && wallet != nil && wallet.Salt != nil {
					vm.AddVar("aa_salt", wallet.Salt)
					if x.logger != nil {
						x.logger.Info("Executor: AA salt resolved from wallet DB", "salt", wallet.Salt.String(), "wallet", task.SmartWalletAddress, "chain_id", walletChainID)
					}
					break
				}
			}
		}
	}

	// Extract and add trigger config data if available using shared functions
	triggerInputData := TaskTriggerToConfig(task.Trigger)
	if triggerInputData != nil && task.Trigger != nil {
		// Get the trigger variable name and update trigger variable using shared function
		triggerVarName := sanitizeTriggerNameForJS(task.Trigger.GetName())

		// Build trigger data map from the actual trigger output data
		triggerDataMap := buildTriggerDataMapFromProtobuf(queueData.TriggerType, queueData.TriggerOutput, x.logger)

		// Build trigger variable data using shared function with ACTUAL trigger output data
		triggerVarData := buildTriggerVariableData(task.Trigger, triggerDataMap, triggerInputData)

		// Update trigger variable in VM using shared function
		updateTriggerVariableInVM(vm, triggerVarName, triggerVarData)
	}

	// Check for error after VM creation
	if err != nil {
		execution.EndAt = time.Now().UnixMilli()
		execution.Error = fmt.Sprintf("failed to create VM: %v", err)
		execution.Status = avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED
		x.persistFailedExecution(task, execution, initialTaskStatus)
		return execution, nil // Return execution record with failure details
	}

	// Pre-execution credit check: block if outstanding value fees exceed credit limit.
	// NOTE: Known TOCTOU gap — no lock is held between reading the outstanding balance
	// and recording a new fee after execution. Two concurrent executions for the same
	// owner can both pass this check before either records its fee. Acceptable for V1
	// where value fees are not auto-recorded; needs per-owner locking when V2 lands.
	if x.feeLedger != nil && x.priceService != nil && x.engine.config.FeeRates != nil {
		creditLimitUSD := x.engine.config.FeeRates.CreditLimitUSD
		chainID := int64(0)
		if x.smartWalletConfig != nil {
			chainID = x.smartWalletConfig.ChainID
		}

		// Convert credit limit to Wei. If 0 (default), creditLimitWei = 0 → block on any outstanding.
		var creditLimitWei *big.Int
		if creditLimitUSD > 0 {
			var convErr error
			creditLimitWei, convErr = ConvertUSDToWei(creditLimitUSD, x.priceService, chainID)
			if convErr != nil {
				x.logger.Warn("Failed to convert credit limit to Wei, skipping check", "error", convErr)
				creditLimitWei = nil
			}
		} else {
			creditLimitWei = big.NewInt(0) // Zero tolerance: block on any outstanding balance
		}

		if creditLimitWei != nil {
			taskOwner := common.HexToAddress(task.Owner)
			// Value fees accrue per execution chain (fl:<chain>:<owner>) and a task's
			// nodes can act on different chains, so an outstanding balance may sit on a
			// non-default chain. Gate against every configured chain rather than just
			// the gateway default, so credit limits can't be bypassed cross-chain.
			for _, feeChainID := range x.engine.knownChainIDs() {
				withinLimit, outstanding, checkErr := x.feeLedger.CheckCreditLimit(feeChainID, taskOwner, creditLimitWei)
				if checkErr != nil {
					x.logger.Warn("Fee ledger check failed, proceeding with execution", "chain_id", feeChainID, "error", checkErr)
					continue
				}
				if !withinLimit {
					execution.EndAt = time.Now().UnixMilli()
					execution.Error = fmt.Sprintf("[INSUFFICIENT_CREDIT] outstanding value fees (%s wei) exceed credit limit", outstanding.String())
					execution.Status = avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED
					x.persistFailedExecution(task, execution, initialTaskStatus)
					return execution, nil
				}
			}
		}
	}

	// Reaching this point means every validation gate above passed. Clear any
	// prior consecutive-failure state so a future transient blip doesn't keep
	// counting from a stale baseline. Persisted by the task write at the end
	// of RunTask.
	if task.ConsecutiveValidationFailures != 0 || task.LastValidationError != "" {
		task.ConsecutiveValidationFailures = 0
		task.LastValidationError = ""
	}

	var runTaskErr error = nil
	if err = vm.Compile(); err != nil {
		x.logger.Error("error compile task", "error", err, "edges", task.Edges, "node", task.Nodes, "task trigger data", task.Trigger, "task trigger metadata", queueData)
		runTaskErr = err
	} else {
		// Debug: Log FilterNode expressions after VM compilation
		if x.logger != nil {
			for nodeID, taskNode := range vm.TaskNodes {
				if filterNode := taskNode.GetFilter(); filterNode != nil && filterNode.Config != nil {
					x.logger.Info("DEPLOY DEBUG: FilterNode expression after VM compilation",
						"task_id", task.Id,
						"node_id", nodeID,
						"node_name", taskNode.Name,
						"expression", filterNode.Config.Expression)
				}
			}
		}
		// Create and add a trigger execution step before running nodes
		// This ensures regular workflows have complete execution history (trigger + nodes)

		// Create trigger step similar to SimulateTask
		// Use trigger config data for the execution step's Config field (includes data, headers, pathParams for ManualTrigger)
		var triggerConfigProto *structpb.Value
		triggerInputData := TaskTriggerToConfig(task.Trigger)

		if len(triggerInputData) > 0 {
			if inputProto, err := structpb.NewValue(triggerInputData); err != nil {
				x.logger.Warn("Failed to convert trigger input to protobuf", "error", err)
			} else {
				triggerConfigProto = inputProto
			}
		}

		triggerStep := &avsproto.Execution_Step{
			Id:      task.Trigger.Id,
			Success: true,
			Error:   "",
			StartAt: t0.UnixMilli(),
			EndAt:   t0.UnixMilli(),
			Log:     fmt.Sprintf("Trigger: %s executed successfully", task.Trigger.Name),
			Inputs:  []string{}, // Empty inputs for trigger steps
			Type:    queueData.TriggerType.String(),
			Name:    task.Trigger.Name,
			Config:  triggerConfigProto, // Include trigger configuration data for debugging
		}

		// Set trigger output data in the step based on trigger type
		switch queueData.TriggerType {
		case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
			if output, ok := queueData.TriggerOutput.(*avsproto.ManualTrigger_Output); ok && output != nil {
				triggerStep.OutputData = &avsproto.Execution_Step_ManualTrigger{ManualTrigger: output}
			} else if m, ok := queueData.TriggerOutput.(map[string]interface{}); ok && m != nil {
				// Convert JSON-decoded map back to ManualTrigger_Output using helper
				dataVal := reconstructTriggerOutputData(m, x.logger)
				triggerStep.OutputData = &avsproto.Execution_Step_ManualTrigger{ManualTrigger: &avsproto.ManualTrigger_Output{Data: dataVal}}
			}
		case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
			if output, ok := queueData.TriggerOutput.(*avsproto.FixedTimeTrigger_Output); ok && output != nil {
				triggerStep.OutputData = &avsproto.Execution_Step_FixedTimeTrigger{FixedTimeTrigger: output}
			} else if m, ok := queueData.TriggerOutput.(map[string]interface{}); ok && m != nil {
				// Convert JSON-decoded map back to FixedTimeTrigger_Output using helper
				dataVal := reconstructTriggerOutputData(m, x.logger)
				triggerStep.OutputData = &avsproto.Execution_Step_FixedTimeTrigger{FixedTimeTrigger: &avsproto.FixedTimeTrigger_Output{Data: dataVal}}
			}
		case avsproto.TriggerType_TRIGGER_TYPE_CRON:
			if output, ok := queueData.TriggerOutput.(*avsproto.CronTrigger_Output); ok && output != nil {
				triggerStep.OutputData = &avsproto.Execution_Step_CronTrigger{CronTrigger: output}
			} else if m, ok := queueData.TriggerOutput.(map[string]interface{}); ok && m != nil {
				// Convert JSON-decoded map back to CronTrigger_Output using helper
				dataVal := reconstructTriggerOutputData(m, x.logger)
				triggerStep.OutputData = &avsproto.Execution_Step_CronTrigger{CronTrigger: &avsproto.CronTrigger_Output{Data: dataVal}}
			}
		case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
			if output, ok := queueData.TriggerOutput.(*avsproto.BlockTrigger_Output); ok && output != nil {
				triggerStep.OutputData = &avsproto.Execution_Step_BlockTrigger{BlockTrigger: output}
			} else if m, ok := queueData.TriggerOutput.(map[string]interface{}); ok && m != nil {
				// Convert JSON-decoded map back to BlockTrigger_Output using helper
				dataVal := reconstructTriggerOutputData(m, x.logger)
				triggerStep.OutputData = &avsproto.Execution_Step_BlockTrigger{BlockTrigger: &avsproto.BlockTrigger_Output{Data: dataVal}}
			}
		case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
			if output, ok := queueData.TriggerOutput.(*avsproto.EventTrigger_Output); ok && output != nil {
				triggerStep.OutputData = &avsproto.Execution_Step_EventTrigger{EventTrigger: output}
			} else if m, ok := queueData.TriggerOutput.(map[string]interface{}); ok && m != nil {
				// Convert JSON-decoded map back to EventTrigger_Output using helper
				dataVal := reconstructTriggerOutputData(m, x.logger)
				triggerStep.OutputData = &avsproto.Execution_Step_EventTrigger{EventTrigger: &avsproto.EventTrigger_Output{Data: dataVal}}
			}
		}

		// Add trigger step to execution logs before running nodes
		vm.ExecutionLogs = append(vm.ExecutionLogs, triggerStep)

		runTaskErr = vm.Run()
	}

	// Durable execution: an Await node may have paused the run via requestSuspend.
	// Checkpoint and return WAITING instead of finalizing; the execution resumes later
	// via DeliverSignal/Advance (human approval) or an operator chain-event wake.
	if susp := vm.PendingSuspend(); susp != nil {
		return x.checkpointSuspendedExecution(task, execution, vm, susp)
	}

	t1 := time.Now()

	// when MaxExecution is 0, it means unlimited run until cancel
	if task.MaxExecution > 0 && task.ExecutionCount >= task.MaxExecution {
		task.SetCompleted()
	}

	if task.ExpiredAt > 0 && t1.UnixMilli() >= task.ExpiredAt {
		task.SetCompleted()
	}

	// Analyze execution results from all steps (including failed ones)
	executionError, failedStepCount, resultStatus := vm.AnalyzeExecutionResult()

	// Calculate total gas cost for the workflow

	// Update the execution record we created earlier with the final results
	execution.EndAt = t1.UnixMilli()
	execution.Status = convertToExecutionStatus(resultStatus) // Based on analysis of all steps
	execution.Error = executionError                          // Comprehensive error message from failed steps
	execution.Steps = vm.ExecutionLogs                        // Contains all steps including failed ones
	execution.ExecutionFee = buildExecutionFee(x.engine.config.FeeRates)
	execution.Cogs = buildCOGSFromSteps(vm.ExecutionLogs)
	execution.ValueFee = buildValueFee(task.Nodes, x.engine.config.FeeRates)

	// Value fee recording placeholder.
	// V1: value fees are not recorded because actual transaction value (the base for
	// percentage calculation) is not available from step/receipt data yet.
	// V2 will extract on-chain transferred value from execution receipts and compute
	// value_fee = tier_percentage × tx_value, then record via FeeLedger.RecordValueFee().
	if x.feeLedger != nil && resultStatus == ExecutionSuccess && execution.ValueFee != nil &&
		execution.ValueFee.Fee != nil && execution.ValueFee.Fee.Amount != "0" {
		x.logger.Info("Value fee applicable but not recorded (V1: tx value extraction not implemented)",
			"execution_id", execution.Id,
			"task_id", task.Id,
			"tier", execution.ValueFee.Tier.String(),
			"tier_percentage", execution.ValueFee.Fee.Amount)
	}

	// Ensure no NaN/Inf sneak into protobuf Values (which reject them)
	sanitizeExecutionForPersistence(execution)

	// Log execution status based on result type
	switch resultStatus {
	case ExecutionSuccess:
		x.logger.Info("task execution completed successfully", "task_id", task.Id, "execution_id", queueData.ExecutionID, "total_steps", len(vm.ExecutionLogs))
	case ExecutionFailed:
		// User-workflow failure: per-step errors are already logged at their sites
		// and the ExecutionStatus_EXECUTION_STATUS_FAILED is persisted below. Log
		// the summary at Warn so it stays out of Sentry error alerts.
		x.logger.Warn("task execution completed with failures",
			"error", executionError,
			"task_id", task.Id,
			"execution_id", queueData.ExecutionID,
			"failed_steps", failedStepCount,
			"total_steps", len(vm.ExecutionLogs))
	}

	if runTaskErr != nil {
		x.logger.Error("task execution had VM-level error", "vm_error", runTaskErr, "task_id", task.Id, "execution_id", queueData.ExecutionID)
		if execution.Error == "" {
			execution.Error = fmt.Sprintf("VM execution error: %s", runTaskErr.Error())
		} else {
			execution.Error = fmt.Sprintf("VM execution error: %s (step analysis: %s)", runTaskErr.Error(), execution.Error)
		}
		execution.Status = avsproto.ExecutionStatus_EXECUTION_STATUS_ERROR
	}

	// batch update storage for task + execution log
	updates := map[string][]byte{}
	updates[string(WorkflowStorageKey(task.Id, task.Status))], err = task.ToJSON()
	updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", task.Status))

	// update execution log
	var executionByte []byte
	{
		// Prefer deterministic marshal with unpopulated fields to avoid nil-related issues
		mo := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}
		b, mErr := mo.Marshal(execution)
		if mErr != nil {
			// Fallback to default marshal
			if x.logger != nil {
				x.logger.Error("Executor: protojson.MarshalOptions failed, falling back", "error", mErr)
			}
			b, mErr = protojson.Marshal(execution)
		}
		if mErr == nil {
			executionByte = b
			key := string(TaskExecutionKey(task, execution.Id))
			updates[key] = executionByte
			if x.logger != nil {
				x.logger.Info("Executor: persisting execution", "task_id", task.Id, "execution_id", execution.Id, "key", key)
			}
		} else if x.logger != nil {
			x.logger.Error("Executor: failed to serialize execution for persistence", "task_id", task.Id, "execution_id", execution.Id, "error", mErr)
		}
	}

	if err = x.db.BatchWrite(updates); err != nil {
		// TODO Monitor to see how often this happen
		x.logger.Errorf("error updating task status. %w", err, "task_id", task.Id)
	}

	// whenever a task change its status, we moved it, therefore we will need to clean up the old storage
	if task.Status != initialTaskStatus {
		if err = x.db.Delete(WorkflowStorageKey(task.Id, initialTaskStatus)); err != nil {
			x.logger.Errorf("error updating task status. %w", err, "task_id", task.Id)
		}
	}

	// Always return the execution result (whether successful or failed)
	// Only return an error for true system-level failures, not node execution failures
	if runTaskErr != nil && execution.Error == "" {
		// This is a true system-level error (compilation, etc.) not handled by AnalyzeExecutionResult
		x.logger.Error("critical system error during task execution", "error", runTaskErr, "task_id", task.Id)
		return execution, fmt.Errorf("System error executing task %s: %v", task.Id, runTaskErr)
	}

	// Final execution status logging based on result type
	switch resultStatus {
	case ExecutionSuccess:
		x.logger.Info("successfully executing task", "task_id", task.Id, "triggermark", queueData)
	case ExecutionFailed:
		x.logger.Warn("task execution completed with step failures", "task_id", task.Id, "failed_steps", failedStepCount, "triggermark", queueData)
	}

	return execution, nil
}

// checkpointSuspendedExecution persists a paused execution so it can resume later
// (PLAN_DURABLE_EXECUTION.md). It writes, in crash-safe order (Appendix C.3): the
// resume DATA (vars snapshot) first, then the armed markers (wake subscription +
// the WAITING execution record whose `steps` carry the completed-node outputs).
// The task's own status row is untouched — the task stays Enabled; only this
// execution is WAITING.
func (x *WorkflowExecutor) checkpointSuspendedExecution(task *model.Workflow, execution *avsproto.Execution, vm *VM, susp *SuspendRequest) (*avsproto.Execution, error) {
	execution.Steps = vm.ExecutionLogs
	execution.Status = avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING
	execution.ResumeNodeId = susp.AwaitNodeID
	if susp.Wake != nil {
		execution.WaitReason = "waiting on " + susp.Wake.Kind.String()
	}
	// EndAt intentionally left unset — the execution has not finished.
	sanitizeExecutionForPersistence(execution)

	// 1. Resume data first.
	snapshot, err := vm.snapshotNodeVars()
	if err != nil {
		return nil, fmt.Errorf("checkpoint snapshot vars: %w", err)
	}
	if err := persistCheckpoint(x.db, execution.Id, snapshot); err != nil {
		return nil, fmt.Errorf("persist checkpoint: %w", err)
	}
	// 2. Armed markers — the wake subscription AND the WAITING execution record are
	//    written ATOMICALLY in one batch. As separate writes, a crash between them
	//    could leave a wake pointing at an execution that was never persisted: the
	//    internal-trigger sync would then watch and fire a wake that can never resume.
	//    (The checkpoint above stays a separate prior write — it's resume data with no
	//    wake referencing it, so a partial write is an inert orphan.)
	batch := map[string][]byte{}
	if susp.Wake != nil {
		// Bind the wake to its task so a chain-event notify / boot re-arm can resolve
		// the task (executions are stored task-scoped; no global exec→task index).
		susp.Wake.TaskID = task.Id
		if err := susp.Wake.Validate(); err != nil {
			return nil, fmt.Errorf("validate wake: %w", err)
		}
		wakeBytes, err := marshalWake(susp.Wake)
		if err != nil {
			return nil, fmt.Errorf("marshal wake: %w", err)
		}
		batch[string(wakeSubscriptionKey(execution.Id))] = wakeBytes
	}
	mo := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}
	execBytes, err := mo.Marshal(execution)
	if err != nil {
		return nil, fmt.Errorf("marshal suspended execution: %w", err)
	}
	batch[string(TaskExecutionKey(task, execution.Id))] = execBytes
	if err := x.db.BatchWrite(batch); err != nil {
		return nil, fmt.Errorf("persist suspended execution + wake: %w", err)
	}

	if x.logger != nil {
		x.logger.Info("execution suspended (durable)",
			"task_id", task.Id, "execution_id", execution.Id, "resume_node", susp.AwaitNodeID)
	}
	return execution, nil
}

// DeliverSignal is the signal-intake entrypoint: a gateway approve/reject endpoint
// or operator internal-trigger calls this to wake a suspended execution. It
// validates the signal and advances the execution. (Authorization of the signal
// against the wait's approvers is a follow-up — see the approval security model.)
func (x *WorkflowExecutor) DeliverSignal(task *model.Workflow, signal *Signal) (*avsproto.Execution, error) {
	if err := signal.Validate(); err != nil {
		return nil, fmt.Errorf("invalid signal: %w", err)
	}
	// Enforce the gate: a signal only counts against an actual pending wait whose
	// kind it matches and which hasn't timed out. Otherwise resumption could be
	// triggered with no wait outstanding (or with the wrong wake source).
	wake, err := loadWakeSubscription(x.db, signal.ExecutionID)
	if err != nil {
		return nil, fmt.Errorf("no pending wait for execution %s: %w", signal.ExecutionID, err)
	}
	if signal.Kind != wake.Kind {
		return nil, fmt.Errorf("signal kind %s does not match the pending wait (%s)", signal.Kind, wake.Kind)
	}
	if wake.TimeoutAt > 0 && time.Now().UnixMilli() > wake.TimeoutAt {
		return nil, fmt.Errorf("the wait for execution %s has timed out", signal.ExecutionID)
	}
	return x.Advance(task, signal.ExecutionID, signal)
}

// Advance resumes a WAITING execution from storage. An optional signal's payload
// becomes the suspended step's output (readable by downstream steps). Idempotent
// (durable exactly-once, E8): a non-WAITING execution is a no-op, so a duplicate or
// post-restart signal resumes at most once. On a terminal finish it persists the
// final record and GCs the checkpoint + wake; if the run suspends again it
// re-checkpoints.
//
// Reconstructs node + trigger output vars from the checkpoint; faithful
// reconstruction of arbitrary input variables is a follow-up.
func (x *WorkflowExecutor) Advance(task *model.Workflow, executionID string, signal *Signal) (*avsproto.Execution, error) {
	raw, err := x.db.GetKey(TaskExecutionKey(task, executionID))
	if err != nil {
		return nil, fmt.Errorf("load execution %s: %w", executionID, err)
	}
	execution := &avsproto.Execution{}
	if err := protojson.Unmarshal(raw, execution); err != nil {
		return nil, fmt.Errorf("unmarshal execution %s: %w", executionID, err)
	}
	// Only a WAITING execution resumes (exactly-once across a duplicate / post-restart
	// signal). On the no-op path, still best-effort GC any orphaned durable state in
	// case a prior finalize crashed between persisting the terminal record and cleanup.
	if execution.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING {
		_ = deleteCheckpoint(x.db, executionID)
		_ = deleteWakeSubscription(x.db, executionID)
		return execution, nil
	}

	// Rebuild the VM, mirroring RunTask's swConfig resolution so the resumed leg uses
	// the same wallet/chain settings as the initial leg.
	secrets, secErr := LoadSecretForTask(x.db, task)
	if secErr != nil {
		secrets = map[string]string{}
	}
	swConfig := x.smartWalletConfig
	if x.engine != nil {
		swConfig = x.engine.ResolveSmartWalletConfig(x.engine.defaultChainID())
	}
	vm, err := NewVMWithData(task, nil, swConfig, secrets)
	if err != nil {
		return nil, fmt.Errorf("rebuild vm on resume: %w", err)
	}
	vm.WithDb(x.db).WithLogger(x.logger)
	if x.engine != nil {
		vm.WithChainConfigResolver(x.engine.ResolveSmartWalletConfig)
	}
	if err := vm.Compile(); err != nil {
		return nil, fmt.Errorf("compile on resume: %w", err)
	}

	// Restore prior-leg vars. A WAITING execution always has a checkpoint (written
	// at suspend); a missing/unreadable one is corruption — fail rather than resume
	// with partial state.
	ckpt, lErr := loadCheckpoint(x.db, executionID)
	if lErr != nil {
		return nil, fmt.Errorf("load checkpoint for WAITING execution %s: %w", executionID, lErr)
	}
	if err := vm.restoreNodeVars(ckpt); err != nil {
		return nil, fmt.Errorf("restore vars on resume: %w", err)
	}

	// The wake delivers the suspended step's output.
	if signal != nil && signal.Payload != nil && execution.ResumeNodeId != "" {
		(&CommonProcessor{vm: vm}).SetOutputVarForStep(execution.ResumeNodeId,
			map[string]any{"data": signal.Payload.AsInterface()})
	}

	// Resume state: skip the completed prefix, keep its steps in the final record.
	vm.resumeCompleted = completedNodeIDsFromSteps(execution.Steps)
	vm.ExecutionLogs = append([]*avsproto.Execution_Step{}, execution.Steps...)

	runErr := vm.Run()

	// Suspended again — re-checkpoint and stay WAITING.
	if susp := vm.PendingSuspend(); susp != nil {
		return x.checkpointSuspendedExecution(task, execution, vm, susp)
	}

	// Terminal — finalize and GC the durable state.
	executionError, _, resultStatus := vm.AnalyzeExecutionResult()
	execution.Steps = vm.ExecutionLogs
	execution.Status = convertToExecutionStatus(resultStatus)
	execution.Error = executionError
	execution.EndAt = time.Now().UnixMilli()
	if runErr != nil {
		execution.Status = avsproto.ExecutionStatus_EXECUTION_STATUS_ERROR
		if execution.Error == "" {
			execution.Error = fmt.Sprintf("VM execution error: %s", runErr.Error())
		}
	}
	execution.ResumeNodeId = ""
	execution.WaitReason = ""
	sanitizeExecutionForPersistence(execution)

	mo := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}
	execBytes, err := mo.Marshal(execution)
	if err != nil {
		return nil, fmt.Errorf("marshal resumed execution: %w", err)
	}
	if err := x.db.BatchWrite(map[string][]byte{
		string(TaskExecutionKey(task, executionID)): execBytes,
	}); err != nil {
		return nil, fmt.Errorf("persist resumed execution: %w", err)
	}
	_ = deleteCheckpoint(x.db, executionID)
	_ = deleteWakeSubscription(x.db, executionID)

	if x.logger != nil {
		x.logger.Info("execution resumed to terminal",
			"task_id", task.Id, "execution_id", executionID, "status", execution.Status.String())
	}
	return execution, nil
}

// ExpireWait fails a WAITING execution whose wake timed out, writing a terminal
// (FAILED) record and GC'ing the durable state. Idempotent: a non-WAITING execution
// just GCs any orphaned state. Mirrors Advance's finalize without running the VM —
// nothing resumes, the wait simply expires.
func (x *WorkflowExecutor) ExpireWait(task *model.Workflow, executionID string) (*avsproto.Execution, error) {
	raw, err := x.db.GetKey(TaskExecutionKey(task, executionID))
	if err != nil {
		_ = deleteCheckpoint(x.db, executionID)
		_ = deleteWakeSubscription(x.db, executionID)
		return nil, fmt.Errorf("load execution %s: %w", executionID, err)
	}
	execution := &avsproto.Execution{}
	if err := protojson.Unmarshal(raw, execution); err != nil {
		return nil, fmt.Errorf("unmarshal execution %s: %w", executionID, err)
	}
	// Only a still-WAITING execution expires; otherwise just GC (a resume may have
	// raced the sweep) and return the record as-is.
	if execution.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING {
		_ = deleteCheckpoint(x.db, executionID)
		_ = deleteWakeSubscription(x.db, executionID)
		return execution, nil
	}

	execution.Status = avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED
	if execution.Error == "" {
		execution.Error = "await timed out before a wake arrived"
	}
	execution.EndAt = time.Now().UnixMilli()
	execution.ResumeNodeId = ""
	execution.WaitReason = ""
	sanitizeExecutionForPersistence(execution)

	mo := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}
	execBytes, err := mo.Marshal(execution)
	if err != nil {
		return nil, fmt.Errorf("marshal expired execution: %w", err)
	}
	if err := x.db.BatchWrite(map[string][]byte{
		string(TaskExecutionKey(task, executionID)): execBytes,
	}); err != nil {
		return nil, fmt.Errorf("persist expired execution: %w", err)
	}
	_ = deleteCheckpoint(x.db, executionID)
	_ = deleteWakeSubscription(x.db, executionID)
	return execution, nil
}

// sanitizeExecutionForPersistence walks execution steps and replaces any NaN/Inf float
// occurrences inside step output/config/metadata with safe JSON values (nil or 0).
// NOTE: This function does NOT redact sensitive data - it only handles NaN/Inf for JSON compatibility.
// The actual execution data is preserved as-is for users. Redaction only happens in aggregator logs.
func sanitizeExecutionForPersistence(exec *avsproto.Execution) {
	if exec == nil || len(exec.Steps) == 0 {
		return
	}
	for _, step := range exec.Steps {
		if step == nil {
			continue
		}
		// Sanitize Config value
		if step.Config != nil {
			step.Config = sanitizeProtoValue(step.Config)
		}
		// Sanitize known Output oneofs by converting to map and cleaning numeric values
		// ManualTrigger
		if out := step.GetManualTrigger(); out != nil && out.Data != nil {
			out.Data = sanitizeProtoValue(out.Data)
		}
		// FixedTime/Cron/Block/Event triggers may carry numeric maps; they typically don't include floats, skip
		if out := step.GetRestApi(); out != nil && out.Data != nil {
			out.Data = sanitizeProtoValue(out.Data)
		}
		if out := step.GetCustomCode(); out != nil && out.Data != nil {
			out.Data = sanitizeProtoValue(out.Data)
		}
		if out := step.GetContractRead(); out != nil && out.Data != nil {
			out.Data = sanitizeProtoValue(out.Data)
		}
		if out := step.GetContractWrite(); out != nil && out.Data != nil {
			out.Data = sanitizeProtoValue(out.Data)
		}
		if out := step.GetEthTransfer(); out != nil && out.Data != nil {
			out.Data = sanitizeProtoValue(out.Data)
		}
		if out := step.GetGraphql(); out != nil && out.Data != nil {
			out.Data = sanitizeProtoValue(out.Data)
		}
	}
}

// sanitizeProtoValue returns a new *structpb.Value with any NaN/Inf replaced.
func sanitizeProtoValue(v *structpb.Value) *structpb.Value {
	if v == nil {
		return nil
	}
	iface := v.AsInterface()
	clean := sanitizeInterface(iface)
	pb, err := structpb.NewValue(clean)
	if err != nil {
		// As a last resort, return null
		nullVal, _ := structpb.NewValue(nil)
		return nullVal
	}
	return pb
}

// sanitizeInterface recursively replaces NaN/Inf with safe values.
// NOTE: This function does NOT redact sensitive data - it only handles NaN/Inf for JSON compatibility.
// Redaction should only happen in logging functions (see core/utils.go and core/taskengine/utils.go).
func sanitizeInterface(x interface{}) interface{} {
	switch t := x.(type) {
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0
		}
		return t
	case map[string]interface{}:
		m := make(map[string]interface{}, len(t))
		for k, v := range t {
			m[k] = sanitizeInterface(v)
		}
		return m
	case []interface{}:
		s := make([]interface{}, len(t))
		for i, v := range t {
			s[i] = sanitizeInterface(v)
		}
		return s
	default:
		return x
	}
}

// resolveSmartWalletConfig returns the per-chain smart-wallet config when
// the executor is wired to a gateway-mode engine, falling back to the
// executor's default config in single-chain mode (or when called without
// an engine, e.g. tests). The shared async executor created at aggregator
// startup carries the gateway-default config; a multi-chain task whose
// validation calls reach the factory must use the TASK's chain config
// (different chain → different factory address → different derived
// wallet), otherwise validateDerivedWallet derives against the wrong
// chain and the RPC-derive fallback path always misses.
func (x *WorkflowExecutor) resolveSmartWalletConfig(chainID int64) *config.SmartWalletConfig {
	if x.engine != nil {
		if cfg := x.engine.ResolveSmartWalletConfig(chainID); cfg != nil {
			return cfg
		}
	}
	return x.smartWalletConfig
}

// validateWalletOwnership performs comprehensive wallet ownership validation
// This handles default wallets (salt:0), stored wallets, and legitimately derived wallets.
// ctx is propagated to the chain-worker calls so a cancelled request (client
// disconnect / timeout) interrupts an in-flight derivation; the worker reader's
// own withTimeout still bounds the call when ctx carries no deadline.
func (x *WorkflowExecutor) validateWalletOwnership(ctx context.Context, chainID int64, user *model.User, smartWalletAddr common.Address) (bool, error) {
	swCfg := x.resolveSmartWalletConfig(chainID)

	// Step 1: Load the user's default smart wallet address (salt:0) for comparison.
	// Must use the task's chain config — deriving against the gateway's default
	// chain factory produces a different wallet than the task's chain factory
	// and the comparison will spuriously miss.
	if swCfg != nil {
		if reader := GetChainStateReaderForChain(uint64(chainID)); reader != nil {
			// Gateway mode: derive salt:0 via the chain worker (no gateway dial).
			if addr, derr := reader.GetSmartWalletAddress(ctx, user.Address, swCfg.FactoryAddress, big.NewInt(0)); derr == nil {
				user.SmartAccountAddress = &addr
			} else {
				x.logger.Warn("Failed to load default smart wallet for validation",
					"owner", user.Address.Hex(), "chain_id", chainID, "error", derr)
			}
		} else if swCfg.EthRpcUrl != "" {
			if rpcClient, err := ethclient.Dial(swCfg.EthRpcUrl); err == nil {
				if err := user.LoadDefaultSmartWallet(rpcClient); err != nil {
					x.logger.Warn("Failed to load default smart wallet for validation",
						"owner", user.Address.Hex(), "chain_id", chainID, "error", err)
				}
				rpcClient.Close()
			}
		}
	}

	// Step 2: Use the standard ValidWalletOwner function (checks default + database)
	if isValid, err := ValidWalletOwner(x.db, chainID, user, smartWalletAddr); err == nil && isValid {
		return true, nil
	} else if err != nil {
		x.logger.Debug("ValidWalletOwner check failed", "owner", user.Address.Hex(),
			"wallet", smartWalletAddr.Hex(), "chain_id", chainID, "error", err)
	}

	// Step 3: Enhanced validation - check if this is a legitimately derived wallet.
	// Handles factory-upgrade scenarios where the original wallet record was
	// never written for the post-upgrade derived address. MUST use the task's
	// per-chain config — see resolveSmartWalletConfig above.
	if swCfg != nil {
		if isValid, err := x.validateDerivedWallet(ctx, chainID, swCfg, user.Address, smartWalletAddr); err == nil && isValid {
			x.logger.Info("Wallet validated as legitimate derived wallet",
				"owner", user.Address.Hex(), "wallet", smartWalletAddr.Hex(), "chain_id", chainID)
			return true, nil
		} else if err != nil {
			x.logger.Debug("Derived wallet validation failed", "owner", user.Address.Hex(),
				"wallet", smartWalletAddr.Hex(), "chain_id", chainID, "error", err)
		}
	}

	return false, nil
}

// validateDerivedWallet checks if a wallet address can be legitimately derived
// from the owner using the supplied chain's factory (for any salt value).
// The swCfg argument is the per-chain config resolved by the caller —
// passing it in (rather than reading x.smartWalletConfig) is what makes
// this work in gateway mode where the executor is shared across chains.
func (x *WorkflowExecutor) validateDerivedWallet(ctx context.Context, chainID int64, swCfg *config.SmartWalletConfig, owner common.Address, smartWalletAddr common.Address) (bool, error) {
	factoryAddr := swCfg.FactoryAddress

	// Try salt values from 0 to max_wallets_per_owner to see if any produce the target wallet address
	// This uses the configured limit from aggregator.yaml
	maxWallets := int64(swCfg.MaxWalletsPerOwner)

	// Prefer the per-chain reader: in gateway mode the worker runs the whole
	// salt scan server-side (one round-trip). Fall back to a direct dial +
	// local loop only when no reader is registered (single-chain mode / tests).
	if reader := GetChainStateReaderForChain(uint64(chainID)); reader != nil {
		found, salt, err := reader.FindMatchingWalletSalt(ctx, owner, factoryAddr, smartWalletAddr, maxWallets)
		if err != nil {
			return false, err
		}
		if found {
			x.logger.Debug("Found matching derived wallet",
				"owner", owner.Hex(), "wallet", smartWalletAddr.Hex(), "factory", factoryAddr.Hex(), "salt", salt)
			return true, nil
		}
		// No salt matched: the wallet is legitimately not derivable from this
		// owner. That is a "not valid" result, not an error — only genuine scan
		// failures (above) return a non-nil error, so the caller can tell
		// "checked, not owned" apart from "could not check".
		x.logger.Debug("No matching derived wallet found",
			"owner", owner.Hex(), "wallet", smartWalletAddr.Hex(), "factory", factoryAddr.Hex(), "salts_checked", maxWallets)
		return false, nil
	}

	rpcClient, err := ethclient.Dial(swCfg.EthRpcUrl)
	if err != nil {
		return false, fmt.Errorf("failed to connect to RPC: %w", err)
	}
	defer rpcClient.Close()

	for salt := int64(0); salt < maxWallets; salt++ {
		derivedAddr, derr := aa.GetSenderAddressForFactory(rpcClient, owner, factoryAddr, big.NewInt(salt))
		if derr != nil {
			x.logger.Debug("Failed to derive wallet address",
				"owner", owner.Hex(), "factory", factoryAddr.Hex(), "salt", salt, "error", derr)
			continue
		}

		if derivedAddr != nil && strings.EqualFold(derivedAddr.Hex(), smartWalletAddr.Hex()) {
			x.logger.Debug("Found matching derived wallet",
				"owner", owner.Hex(), "wallet", smartWalletAddr.Hex(),
				"factory", factoryAddr.Hex(), "salt", salt)
			return true, nil
		}
	}

	// No salt matched: not an error, just "not owned" (see worker branch).
	x.logger.Debug("No matching derived wallet found",
		"owner", owner.Hex(), "wallet", smartWalletAddr.Hex(),
		"factory", factoryAddr.Hex(), "salts_checked", maxWallets)
	return false, nil
}

// validationFailureDisableThreshold is the number of consecutive permanent
// validation failures (see permanentValidationErrorPrefixes) tolerated before
// the executor auto-disables the task. Matches the maxConsecutiveFailures
// constant used elsewhere (core/taskengine/vm.go) for symmetry.
const validationFailureDisableThreshold uint32 = 10

// permanentValidationErrorPrefixes lists execution.Error prefixes that will
// not self-resolve without user intervention — wallet misconfiguration or a
// structurally broken task graph. Transient errors (RPC failures while
// validating ownership, insufficient credit) are deliberately excluded:
// they can clear on the next tick when the chain is reachable or the user
// tops up.
var permanentValidationErrorPrefixes = []string{
	"invalid or missing task smart wallet address",
	"task smart wallet address does not belong to owner",
	"failed to create VM:",
}

// isPermanentValidationError reports whether execution.Error matches one of
// the prefixes that should count toward auto-disable.
func isPermanentValidationError(errorMsg string) bool {
	for _, prefix := range permanentValidationErrorPrefixes {
		if strings.HasPrefix(errorMsg, prefix) {
			return true
		}
	}
	return false
}

// persistFailedExecution persists a failed execution record to the database
// This ensures that failed executions (like wallet validation failures) are recorded for troubleshooting.
//
// Logs at Warn rather than Error: a validation failure is an expected outcome
// (misconfigured task, ownership mismatch) that recurs every time the trigger
// fires until the task is fixed or disabled. The execution record itself is
// persisted with Status=FAILED and Error=<reason>, so users can still
// troubleshoot from it. Using Error would route every tick through
// SentryLogger.captureToSentry and flood Sentry — see EIGENLAYER-AVS-1V.
//
// When the same task has hit validationFailureDisableThreshold consecutive
// *permanent* validation rejections, this also flips the task to Disabled
// and fires exactly one Sentry event (CaptureMessage, fingerprinted by
// task_id) so an operator gets a single high-signal alert per broken task
// instead of a per-tick flood.
func (x *WorkflowExecutor) persistFailedExecution(task *model.Workflow, execution *avsproto.Execution, initialTaskStatus avsproto.TaskStatus) {
	x.logger.Warn("task execution failed during validation",
		"error", execution.Error,
		"task_id", task.Id,
		"execution_id", execution.Id,
		"reason", "validation_failure")

	task.LastValidationError = execution.Error
	if isPermanentValidationError(execution.Error) {
		task.ConsecutiveValidationFailures++
		if task.Status == avsproto.TaskStatus_Enabled && task.ConsecutiveValidationFailures >= validationFailureDisableThreshold {
			task.Status = avsproto.TaskStatus_Disabled
			x.reportTaskAutoDisabled(task, execution.Error)
		}
	}

	// Ensure no NaN/Inf sneak into protobuf Values (which reject them)
	sanitizeExecutionForPersistence(execution)

	// Prepare batch update for task + execution log
	updates := map[string][]byte{}

	// Update task data
	if taskJSON, err := task.ToJSON(); err == nil {
		updates[string(WorkflowStorageKey(task.Id, task.Status))] = taskJSON
		updates[string(TaskUserKey(task))] = []byte(fmt.Sprintf("%d", task.Status))
	} else {
		x.logger.Error("Failed to serialize task for persistence", "task_id", task.Id, "error", err)
	}

	// Update execution log
	mo := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}
	if executionByte, mErr := mo.Marshal(execution); mErr == nil {
		key := string(TaskExecutionKey(task, execution.Id))
		updates[key] = executionByte
		x.logger.Info("Executor: persisting failed execution", "task_id", task.Id, "execution_id", execution.Id, "key", key)
	} else {
		x.logger.Error("Executor: failed to serialize execution for persistence", "task_id", task.Id, "execution_id", execution.Id, "error", mErr)
	}

	// Persist to database
	if err := x.db.BatchWrite(updates); err != nil {
		x.logger.Error("error persisting failed execution", "task_id", task.Id, "execution_id", execution.Id, "error", err)
	}

	// Clean up old task status if it changed
	if task.Status != initialTaskStatus {
		if err := x.db.Delete(WorkflowStorageKey(task.Id, initialTaskStatus)); err != nil {
			x.logger.Error("error cleaning up old task status", "task_id", task.Id, "error", err)
		}
	}
}

// reportTaskAutoDisabled fires exactly one Sentry event when a task is
// auto-disabled by validationFailureDisableThreshold. The fingerprint is
// scoped to the task ID so each broken workflow gets its own Sentry issue
// rather than rolling into a single noisy one. The event is logged at Warn
// in addition; SentryLogger.Warn does not double-capture.
//
// The Warn log and the Sentry scope both carry owner, smart_wallet,
// created_at (derived from the ULID task id since Task has no created_at
// field) and factory_address (resolved here via resolveSmartWalletConfig
// for the task's chain — the same lookup the validation path used moments
// earlier; config doesn't drift mid-process, so the values match). Without
// those, an operator triaging a burst of these alerts (e.g.
// EIGENLAYER-AVS-1Y..28) can't tell at a glance whether it's one
// customer's wallet-migration mistake, a cohort from a SDK regression, or
// a chain-config drift — the same observability gap 460292b closed for
// the startup invalid-task log.
func (x *WorkflowExecutor) reportTaskAutoDisabled(task *model.Workflow, reason string) {
	createdAt := "unknown"
	if parsed, perr := ulid.Parse(strings.ToUpper(task.Id)); perr == nil {
		createdAt = time.UnixMilli(int64(parsed.Time())).UTC().Format(time.RFC3339)
	}
	factoryAddress := ""
	if swCfg := x.resolveSmartWalletConfig(x.engine.defaultChainID()); swCfg != nil {
		factoryAddress = swCfg.FactoryAddress.Hex()
	}

	x.logger.Warn("task auto-disabled after consecutive validation failures",
		"task_id", task.Id,
		"owner", task.Owner,
		"smart_wallet", task.SmartWalletAddress,
		"factory_address", factoryAddress,
		"created_at", createdAt,
		"consecutive_failures", task.ConsecutiveValidationFailures,
		"reason", reason)
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("event", "task_auto_disabled")
		scope.SetTag("task_id", task.Id)
		scope.SetTag("owner", task.Owner)
		scope.SetTag("smart_wallet", task.SmartWalletAddress)
		if factoryAddress != "" {
			scope.SetTag("factory_address", factoryAddress)
		}
		scope.SetExtra("created_at", createdAt)
		scope.SetExtra("consecutive_failures", task.ConsecutiveValidationFailures)
		scope.SetExtra("reason", reason)
		scope.SetFingerprint([]string{"task-auto-disabled", task.Id})
		sentry.CaptureMessage(fmt.Sprintf(
			"Task %s auto-disabled after %d consecutive validation failures: %s",
			task.Id, task.ConsecutiveValidationFailures, reason))
	})
}
