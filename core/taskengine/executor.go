package taskengine

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/apqueue"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func NewExecutor(config *config.SmartWalletConfig, db storage.Storage, logger sdklogging.Logger) *TaskExecutor {
	return &TaskExecutor{
		db:                     db,
		logger:                 logger,
		smartWalletConfig:      config,
		tokenEnrichmentService: GetTokenEnrichmentService(),
	}
}

type TaskExecutor struct {
	db                     storage.Storage
	logger                 sdklogging.Logger
	smartWalletConfig      *config.SmartWalletConfig
	tokenEnrichmentService *TokenEnrichmentService
}

type QueueExecutionData struct {
	TriggerType   avsproto.TriggerType
	TriggerOutput interface{} // Will hold the specific trigger output (BlockTrigger.Output, etc.)
	ExecutionID   string
}

func (x *TaskExecutor) GetTask(id string) (*model.Task, error) {
	task := &model.Task{
		Task: &avsproto.Task{},
	}
	storageKey := []byte(fmt.Sprintf("t:%s:%s", TaskStatusToStorageKey(avsproto.TaskStatus_Active), id))
	item, err := x.db.GetKey(storageKey)

	if err != nil {
		return nil, fmt.Errorf("storage access failed for key 't:%s:%s': %w", TaskStatusToStorageKey(avsproto.TaskStatus_Active), id, err)
	}
	err = protojson.Unmarshal(item, task)
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

func (x *TaskExecutor) Perform(job *apqueue.Job) error {
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
		if queueData != nil && queueData.ExecutionID != "" { // Assumes `ExecutionID` is always set for queued jobs. Verify this assumption if the logic changes.
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

func (x *TaskExecutor) RunTask(task *model.Task, queueData *QueueExecutionData) (*avsproto.Execution, error) {
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

	// Create VM with trigger reason data
	vm, err := NewVMWithData(task, triggerReason, x.smartWalletConfig, secrets)

	if err != nil {
		return nil, err
	}

	// Add chain name to workflowContext - extract chainId from enriched event data first, fallback to tokenEnrichmentService
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

	if chainId > 0 {
		if x.logger != nil {
			x.logger.Info("🔗 Executor: Adding chain name to VM", "chainId", chainId, "source", chainIdSource)
		}
		vm.WithChainName(chainId)
	} else {
		if x.logger != nil {
			x.logger.Warn("⚠️ Executor: No chainId available for chain name resolution")
		}
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

	// Initialize timing and execution record BEFORE validation to ensure we always have a record
	t0 := time.Now()
	task.ExecutionCount += 1
	task.LastRanAt = t0.UnixMilli()
	initialTaskStatus := task.Status

	// Create execution record immediately - this ensures we have a record even if validation fails
	execution := &avsproto.Execution{
		Id:      queueData.ExecutionID,
		StartAt: t0.UnixMilli(),
		EndAt:   0,                            // Will be set when execution completes or fails
		Success: false,                        // Default to false, will be updated if successful
		Error:   "",                           // Will be populated if there are errors
		Steps:   []*avsproto.Execution_Step{}, // Will be populated during execution
	}

	// Wallet validation - if this fails, we'll record the failure and return the execution record
	if task != nil && task.Owner != "" {
		if task.SmartWalletAddress == "" || !common.IsHexAddress(task.SmartWalletAddress) {
			execution.EndAt = time.Now().UnixMilli()
			execution.Error = "invalid or missing task smart wallet address for deployed run"
			x.persistFailedExecution(task, execution, initialTaskStatus)
			return execution, nil // Return execution record with failure details
		}
		owner := common.HexToAddress(task.Owner)
		user := &model.User{Address: owner}
		smartWalletAddr := common.HexToAddress(task.SmartWalletAddress)

		// Enhanced wallet validation that handles any legitimately derived wallet
		isValid, err := x.validateWalletOwnership(user, smartWalletAddr)
		if err != nil {
			execution.EndAt = time.Now().UnixMilli()
			execution.Error = fmt.Sprintf("failed to validate wallet ownership for owner %s: %v", owner.Hex(), err)
			x.persistFailedExecution(task, execution, initialTaskStatus)
			return execution, nil // Return execution record with failure details
		}
		if !isValid {
			execution.EndAt = time.Now().UnixMilli()
			execution.Error = "task smart wallet address does not belong to owner"
			x.persistFailedExecution(task, execution, initialTaskStatus)
			return execution, nil // Return execution record with failure details
		}
		vm.AddVar("aa_sender", common.HexToAddress(task.SmartWalletAddress).Hex())
		if x.logger != nil {
			x.logger.Info("Executor: AA sender resolved", "sender", task.SmartWalletAddress)
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

	if err != nil {
		return nil, fmt.Errorf("vm failed to initialize: %w", err)
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
				// Convert JSON-decoded map back to ManualTrigger_Output
				var dataVal *structpb.Value
				if raw, exists := m["data"]; exists && raw != nil {
					if pb, err := structpb.NewValue(raw); err == nil {
						dataVal = pb
					}
				}
				triggerStep.OutputData = &avsproto.Execution_Step_ManualTrigger{ManualTrigger: &avsproto.ManualTrigger_Output{Data: dataVal}}
			}
		case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
			if output, ok := queueData.TriggerOutput.(*avsproto.FixedTimeTrigger_Output); ok {
				triggerStep.OutputData = &avsproto.Execution_Step_FixedTimeTrigger{FixedTimeTrigger: output}
			}
		case avsproto.TriggerType_TRIGGER_TYPE_CRON:
			if output, ok := queueData.TriggerOutput.(*avsproto.CronTrigger_Output); ok {
				triggerStep.OutputData = &avsproto.Execution_Step_CronTrigger{CronTrigger: output}
			}
		case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
			if output, ok := queueData.TriggerOutput.(*avsproto.BlockTrigger_Output); ok {
				triggerStep.OutputData = &avsproto.Execution_Step_BlockTrigger{BlockTrigger: output}
			}
		case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
			if output, ok := queueData.TriggerOutput.(*avsproto.EventTrigger_Output); ok {
				triggerStep.OutputData = &avsproto.Execution_Step_EventTrigger{EventTrigger: output}
			}
		}

		// Add trigger step to execution logs before running nodes
		vm.ExecutionLogs = append(vm.ExecutionLogs, triggerStep)

		runTaskErr = vm.Run()
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
	executionSuccess, executionError, failedStepCount, resultStatus := vm.AnalyzeExecutionResult()

	// Update the execution record we created earlier with the final results
	execution.EndAt = t1.UnixMilli()
	execution.Success = executionSuccess // Based on analysis of all steps
	execution.Error = executionError     // Comprehensive error message from failed steps
	execution.Steps = vm.ExecutionLogs   // Contains all steps including failed ones

	// Ensure no NaN/Inf sneak into protobuf Values (which reject them)
	sanitizeExecutionForPersistence(execution)

	// Log execution status based on result type
	switch resultStatus {
	case ExecutionSuccess:
		x.logger.Info("task execution completed successfully", "task_id", task.Id, "execution_id", queueData.ExecutionID, "total_steps", len(vm.ExecutionLogs))
	case ExecutionPartialSuccess:
		x.logger.Warn("task execution completed with partial success",
			"error", executionError,
			"task_id", task.Id,
			"execution_id", queueData.ExecutionID,
			"failed_steps", failedStepCount,
			"total_steps", len(vm.ExecutionLogs))
	case ExecutionFailure:
		x.logger.Error("task execution completed with failures",
			"error", executionError,
			"task_id", task.Id,
			"execution_id", queueData.ExecutionID,
			"failed_steps", failedStepCount,
			"total_steps", len(vm.ExecutionLogs))
	}

	if runTaskErr != nil {
		// This should not happen if AnalyzeExecutionResult is working correctly,
		// but handle it as a fallback for VM-level errors
		x.logger.Error("task execution had VM-level error", "vm_error", runTaskErr, "task_id", task.Id, "execution_id", queueData.ExecutionID)
		if execution.Error == "" {
			execution.Error = fmt.Sprintf("VM execution error: %s", runTaskErr.Error())
			execution.Success = false
		}
	}

	// batch update storage for task + execution log
	updates := map[string][]byte{}
	updates[string(TaskStorageKey(task.Id, task.Status))], err = task.ToJSON()
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
		if err = x.db.Delete(TaskStorageKey(task.Id, initialTaskStatus)); err != nil {
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
	case ExecutionPartialSuccess:
		x.logger.Info("task execution completed with partial success", "task_id", task.Id, "failed_steps", failedStepCount, "triggermark", queueData)
	default: // ExecutionFailure or other
		x.logger.Warn("task execution completed with step failures", "task_id", task.Id, "failed_steps", failedStepCount)
	}

	return execution, nil
}

// sanitizeExecutionForPersistence walks execution steps and replaces any NaN/Inf float
// occurrences inside step output/config/metadata with safe JSON values (nil or 0).
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

// validateWalletOwnership performs comprehensive wallet ownership validation
// This handles default wallets (salt:0), stored wallets, and legitimately derived wallets
func (x *TaskExecutor) validateWalletOwnership(user *model.User, smartWalletAddr common.Address) (bool, error) {
	// Step 1: Load the user's default smart wallet address (salt:0) for comparison
	if x.smartWalletConfig != nil && x.smartWalletConfig.EthRpcUrl != "" {
		if rpcClient, err := ethclient.Dial(x.smartWalletConfig.EthRpcUrl); err == nil {
			// Load the default smart wallet address (salt:0)
			if err := user.LoadDefaultSmartWallet(rpcClient); err != nil {
				x.logger.Warn("Failed to load default smart wallet for validation",
					"owner", user.Address.Hex(), "error", err)
			}
			rpcClient.Close()
		}
	}

	// Step 2: Use the standard ValidWalletOwner function (checks default + database)
	if isValid, err := ValidWalletOwner(x.db, user, smartWalletAddr); err == nil && isValid {
		return true, nil
	} else if err != nil {
		x.logger.Debug("ValidWalletOwner check failed", "owner", user.Address.Hex(),
			"wallet", smartWalletAddr.Hex(), "error", err)
	}

	// Step 3: Enhanced validation - check if this is a legitimately derived wallet
	// This handles cases where the wallet was derived but not stored in the database
	if x.smartWalletConfig != nil && x.smartWalletConfig.EthRpcUrl != "" {
		if isValid, err := x.validateDerivedWallet(user.Address, smartWalletAddr); err == nil && isValid {
			x.logger.Info("Wallet validated as legitimate derived wallet",
				"owner", user.Address.Hex(), "wallet", smartWalletAddr.Hex())
			return true, nil
		} else if err != nil {
			x.logger.Debug("Derived wallet validation failed", "owner", user.Address.Hex(),
				"wallet", smartWalletAddr.Hex(), "error", err)
		}
	}

	return false, nil
}

// validateDerivedWallet checks if a wallet address can be legitimately derived
// from the owner using the configured factory (for any salt value)
func (x *TaskExecutor) validateDerivedWallet(owner common.Address, smartWalletAddr common.Address) (bool, error) {
	rpcClient, err := ethclient.Dial(x.smartWalletConfig.EthRpcUrl)
	if err != nil {
		return false, fmt.Errorf("failed to connect to RPC: %w", err)
	}
	defer rpcClient.Close()

	factoryAddr := x.smartWalletConfig.FactoryAddress

	// Try salt values from 0 to max_wallets_per_owner to see if any produce the target wallet address
	// This uses the configured limit from aggregator.yaml
	maxWallets := int64(x.smartWalletConfig.MaxWalletsPerOwner)

	for salt := int64(0); salt < maxWallets; salt++ {
		derivedAddr, err := aa.GetSenderAddressForFactory(rpcClient, owner, factoryAddr, big.NewInt(salt))
		if err != nil {
			// Log error for debugging but continue with next salt
			x.logger.Debug("Failed to derive wallet address",
				"owner", owner.Hex(), "factory", factoryAddr.Hex(), "salt", salt, "error", err)
			continue
		}

		if derivedAddr != nil && strings.EqualFold(derivedAddr.Hex(), smartWalletAddr.Hex()) {
			x.logger.Debug("Found matching derived wallet",
				"owner", owner.Hex(), "wallet", smartWalletAddr.Hex(),
				"factory", factoryAddr.Hex(), "salt", salt)
			return true, nil
		}
	}

	x.logger.Debug("No matching derived wallet found",
		"owner", owner.Hex(), "wallet", smartWalletAddr.Hex(),
		"factory", factoryAddr.Hex(), "salts_checked", maxWallets)
	return false, fmt.Errorf("wallet address cannot be derived from owner with factory %s (checked %d salts)", factoryAddr.Hex(), maxWallets)
}

// persistFailedExecution persists a failed execution record to the database
// This ensures that failed executions (like wallet validation failures) are recorded for troubleshooting
func (x *TaskExecutor) persistFailedExecution(task *model.Task, execution *avsproto.Execution, initialTaskStatus avsproto.TaskStatus) {
	// Log the failure for debugging
	x.logger.Error("task execution failed during validation",
		"error", execution.Error,
		"task_id", task.Id,
		"execution_id", execution.Id,
		"reason", "validation_failure")

	// Ensure no NaN/Inf sneak into protobuf Values (which reject them)
	sanitizeExecutionForPersistence(execution)

	// Prepare batch update for task + execution log
	updates := map[string][]byte{}

	// Update task data
	if taskJSON, err := task.ToJSON(); err == nil {
		updates[string(TaskStorageKey(task.Id, task.Status))] = taskJSON
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
		if err := x.db.Delete(TaskStorageKey(task.Id, initialTaskStatus)); err != nil {
			x.logger.Error("error cleaning up old task status", "task_id", task.Id, "error", err)
		}
	}
}
