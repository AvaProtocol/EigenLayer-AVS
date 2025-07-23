package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

// noOpLogger implements the sdklogging.Logger interface as a no-op to prevent nil pointer dereferences
type noOpLogger struct{}

func (l *noOpLogger) Info(msg string, keysAndValues ...interface{})        {}
func (l *noOpLogger) Infof(format string, args ...interface{})             {}
func (l *noOpLogger) Debug(msg string, keysAndValues ...interface{})       {}
func (l *noOpLogger) Debugf(format string, args ...interface{})            {}
func (l *noOpLogger) Error(msg string, keysAndValues ...interface{})       {}
func (l *noOpLogger) Errorf(format string, args ...interface{})            {}
func (l *noOpLogger) Warn(msg string, keysAndValues ...interface{})        {}
func (l *noOpLogger) Warnf(format string, args ...interface{})             {}
func (l *noOpLogger) Fatal(msg string, keysAndValues ...interface{})       {}
func (l *noOpLogger) Fatalf(format string, args ...interface{})            {}
func (l *noOpLogger) With(keysAndValues ...interface{}) sdklogging.Logger  { return l }
func (l *noOpLogger) WithComponent(componentName string) sdklogging.Logger { return l }
func (l *noOpLogger) WithName(name string) sdklogging.Logger               { return l }
func (l *noOpLogger) WithServiceName(serviceName string) sdklogging.Logger { return l }
func (l *noOpLogger) WithHostName(hostName string) sdklogging.Logger       { return l }
func (l *noOpLogger) Sync() error                                          { return nil }

type VMState string

const (
	VMStateInitialize         VMState = "vm_initialize"
	VMStateCompiled           VMState = "vm_compiled"
	VMStateReady              VMState = "vm_ready"
	VMStateRunning            VMState = "vm_running"
	VMStateCompleted          VMState = "vm_completed"
	VMMaxPreprocessIterations         = 100
	APContextVarName                  = "apContext"
	WorkflowContextVarName            = "workflowContext"
	ConfigVarsPath                    = "configVars"
	APContextConfigVarsPath           = APContextVarName + "." + ConfigVarsPath
	DataSuffix                        = "data"
	MaxExecutionDepth                 = 50 // Maximum depth for nested workflow execution
)

// ExecutionTask represents a single task in the execution queue
type ExecutionTask struct {
	Node           *avsproto.TaskNode
	InputVariables map[string]interface{}
	StepID         string
	Depth          int
	ParentStepID   string
	IterationIndex int                   // For loop iterations
	ResultChannel  chan *ExecutionResult // Channel to send result back to parent
}

// ExecutionResult represents the result of an execution task
type ExecutionResult struct {
	Step   *avsproto.Execution_Step
	Error  error
	StepID string
	Data   interface{} // For loop iterations, this contains the result data
}

// ExecutionQueue manages a queue of execution tasks
type ExecutionQueue struct {
	tasks   chan *ExecutionTask
	workers int
	vm      *VM
}

// NewExecutionQueue creates a new execution queue
func NewExecutionQueue(vm *VM, workers int) *ExecutionQueue {
	if workers <= 0 {
		workers = 1 // Default to single worker for sequential execution
	}
	return &ExecutionQueue{
		tasks:   make(chan *ExecutionTask, workers*10), // Buffer to prevent blocking
		workers: workers,
		vm:      vm,
	}
}

type Step struct {
	NodeID string
	Next   []string
}

type CommonProcessor struct {
	vm *VM
}

func (c *CommonProcessor) SetVar(name string, data any) {
	c.vm.AddVar(name, data)
}

func (c *CommonProcessor) SetOutputVarForStep(stepID string, data any) {
	c.vm.mu.Lock()
	defer c.vm.mu.Unlock()
	nodeNameVar := c.vm.getNodeNameAsVarLocked(stepID) // Use locked version to avoid deadlock
	if c.vm.vars == nil {
		c.vm.vars = make(map[string]any)
	}

	// Use the data directly without dual access mapping
	processedData := data

	// Get existing variable or create new one
	existingVar := c.vm.vars[nodeNameVar]
	var nodeVar map[string]any
	if existingMap, ok := existingVar.(map[string]any); ok {
		nodeVar = existingMap
	} else {
		nodeVar = make(map[string]any)
	}

	// Set the output data
	nodeVar["data"] = processedData
	c.vm.vars[nodeNameVar] = nodeVar
}

func (c *CommonProcessor) SetInputVarForStep(stepID string, inputData any) {
	c.vm.mu.Lock()
	defer c.vm.mu.Unlock()
	nodeNameVar := c.vm.getNodeNameAsVarLocked(stepID)
	if c.vm.vars == nil {
		c.vm.vars = make(map[string]any)
	}

	// Use the input data directly without dual access mapping
	processedInput := inputData

	// Get existing variable or create new one
	existingVar := c.vm.vars[nodeNameVar]
	var nodeVar map[string]any
	if existingMap, ok := existingVar.(map[string]any); ok {
		nodeVar = existingMap
	} else {
		nodeVar = make(map[string]any)
	}

	// Set the input data
	nodeVar["input"] = processedInput
	c.vm.vars[nodeNameVar] = nodeVar
}

func (c *CommonProcessor) GetOutputVar(stepID string) any {
	c.vm.mu.Lock()
	defer c.vm.mu.Unlock()
	name := c.vm.getNodeNameAsVarLocked(stepID) // Use locked version to avoid deadlock
	if name == "" || c.vm.vars == nil {
		return nil
	}
	value, ok := c.vm.vars[name]
	if !ok {
		return nil
	}
	if valueMap, okValue := value.(map[string]any); okValue {
		return valueMap["data"]
	}
	return nil
}

type triggerDataType struct {
	Block  *avsproto.BlockTrigger_Output
	Time   *avsproto.FixedTimeTrigger_Output
	Cron   *avsproto.CronTrigger_Output
	Event  *avsproto.EventTrigger_Output
	Manual *avsproto.ManualTrigger_Output
}

func (t *triggerDataType) GetValue() avsproto.IsExecution_Step_OutputData {
	if t.Event != nil {
		return &avsproto.Execution_Step_EventTrigger{EventTrigger: t.Event}
	}
	if t.Block != nil {
		return &avsproto.Execution_Step_BlockTrigger{BlockTrigger: t.Block}
	}
	if t.Time != nil {
		return &avsproto.Execution_Step_FixedTimeTrigger{FixedTimeTrigger: t.Time}
	}
	if t.Cron != nil {
		return &avsproto.Execution_Step_CronTrigger{CronTrigger: t.Cron}
	}
	if t.Manual != nil {
		return &avsproto.Execution_Step_ManualTrigger{ManualTrigger: t.Manual}
	}
	return nil
}

type VM struct {
	TaskID            string
	TaskNodes         map[string]*avsproto.TaskNode
	TaskOwner         common.Address
	task              *model.Task
	triggerData       *TriggerData
	parsedTriggerData *triggerDataType
	ExecutionLogs     []*avsproto.Execution_Step
	Status            VMState
	mu                *sync.Mutex // Mutex for vars and TaskNodes, and other shared mutable fields like plans, entrypoint, status
	vars              map[string]any
	secrets           map[string]string
	plans             map[string]*Step
	entrypoint        string
	instructionCount  int64
	smartWalletConfig *config.SmartWalletConfig
	logger            sdklogging.Logger
	db                storage.Storage
}

func NewVM() *VM {
	v := &VM{
		Status:           VMStateInitialize,
		mu:               &sync.Mutex{},
		instructionCount: 0,
		secrets:          make(map[string]string),
		vars:             make(map[string]any),
		TaskNodes:        make(map[string]*avsproto.TaskNode),
		plans:            make(map[string]*Step),
	}
	envVars := macros.GetEnvs(map[string]any{})
	for key, value := range envVars {
		v.vars[key] = value
	}
	return v
}

func (v *VM) Reset() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.ExecutionLogs = []*avsproto.Execution_Step{}
	v.plans = make(map[string]*Step)
	v.entrypoint = ""
	v.Status = VMStateInitialize
	v.instructionCount = 0
	// v.vars may retain some initial values like env vars, or could be fully cleared:
	// For now, retain env vars, clear others if necessary, or re-init like NewVM.
	// Simplified: just reset what's needed for re-compilation and re-run.
}

func (v *VM) WithLogger(logger sdklogging.Logger) *VM {
	v.logger = logger
	return v
}

func (v *VM) WithDb(db storage.Storage) *VM {
	v.db = db
	return v
}

func (v *VM) GetTriggerNameAsVar() (string, error) {
	// This method doesn't modify VM state directly, so mutex is not strictly needed
	// unless task can be modified concurrently, which is unlikely during this call.
	var name string
	if v.task != nil && v.task.Trigger != nil {
		name = v.task.Trigger.Name
		if name == "" {
			return "", fmt.Errorf("trigger name is required but not defined in task")
		}
	} else if v.task != nil {
		return "", fmt.Errorf("trigger is required but not defined in task")
	} else {
		name = "trigger" // Default for nil task (e.g. single node execution)
	}

	// Use the unified sanitization function
	return sanitizeTriggerNameForJS(name), nil
}

func (v *VM) GetNodeNameAsVar(nodeID string) string {
	v.mu.Lock() // Lock for reading TaskNodes
	defer v.mu.Unlock()
	return v.getNodeNameAsVarLocked(nodeID)
}

// getNodeNameAsVarLocked is the internal version that assumes the mutex is already held
func (v *VM) getNodeNameAsVarLocked(nodeID string) string {
	node, ok := v.TaskNodes[nodeID]
	if !ok {
		// Use the unified sanitization function for nodeID itself if not found
		return sanitizeTriggerNameForJS(nodeID)
	}
	name := node.Name
	if name == "" {
		name = nodeID
	}

	// Use the unified sanitization function for node names
	return sanitizeTriggerNameForJS(name)
}

func NewVMWithData(task *model.Task, triggerData *TriggerData, smartWalletConfig *config.SmartWalletConfig, secrets map[string]string) (*VM, error) {
	return NewVMWithDataAndTransferLog(task, triggerData, smartWalletConfig, secrets, nil)
}

func NewVMWithDataAndTransferLog(task *model.Task, triggerData *TriggerData, smartWalletConfig *config.SmartWalletConfig, secrets map[string]string, transferLog *structpb.Value) (*VM, error) {
	var taskOwner common.Address
	if task != nil && task.Owner != "" {
		taskOwner = common.HexToAddress(task.Owner)
	}

	v := NewVM() // Initializes mutex, maps, etc.
	v.TaskOwner = taskOwner
	v.secrets = secrets
	v.task = task
	v.triggerData = triggerData
	v.smartWalletConfig = smartWalletConfig
	v.parsedTriggerData = &triggerDataType{} // Initialize parsedTriggerData

	// Initialize logger if it's nil to prevent panic
	if v.logger == nil {
		// Create a no-op logger to prevent nil pointer dereferences
		v.logger = &noOpLogger{}
	}

	// Initialize apContext with configVars containing secrets and macro variables
	configVars := make(map[string]string)
	// Add secrets (they override macro variables if there are conflicts)
	for k, v := range secrets {
		configVars[k] = v
	}

	v.AddVar(APContextVarName, map[string]map[string]string{
		ConfigVarsPath: configVars,
	})

	if task != nil {
		if task.Trigger == nil {
			return nil, fmt.Errorf("trigger is required but not defined in task")
		}
		if task.Trigger.Name == "" {
			return nil, fmt.Errorf("trigger name is required but not defined in task")
		}
		v.mu.Lock()
		for _, node := range task.Nodes {
			v.TaskNodes[node.Id] = node
		}
		v.mu.Unlock()

		// Add workflowContext variable with task metadata
		workflowContext := map[string]interface{}{
			"id":                 task.Id,
			"name":               task.Name,
			"owner":              task.Owner,
			"smartWalletAddress": task.SmartWalletAddress,
			"runner":             task.SmartWalletAddress, // Alias for smartWalletAddress
			"eoaAddress":         task.Owner,              // Alias for owner
			"startAt":            task.StartAt,
			"expiredAt":          task.ExpiredAt,
			"completedAt":        task.CompletedAt,
			"maxExecution":       task.MaxExecution,
			"executionCount":     task.ExecutionCount,
			"lastRanAt":          task.LastRanAt,
			"status":             getTaskStatusString(task.Status),
		}
		v.AddVar(WorkflowContextVarName, workflowContext)
	}

	// Parse trigger-specific data based on the flattened structure
	if triggerData != nil {
		switch triggerData.Type {
		case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
			if blockOutput, ok := triggerData.Output.(*avsproto.BlockTrigger_Output); ok {
				v.parsedTriggerData.Block = blockOutput
			}
		case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
			if timeOutput, ok := triggerData.Output.(*avsproto.FixedTimeTrigger_Output); ok {
				v.parsedTriggerData.Time = timeOutput
			}
		case avsproto.TriggerType_TRIGGER_TYPE_CRON:
			if cronOutput, ok := triggerData.Output.(*avsproto.CronTrigger_Output); ok {
				v.parsedTriggerData.Cron = cronOutput
			}
		case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
			if eventOutput, ok := triggerData.Output.(*avsproto.EventTrigger_Output); ok {
				v.parsedTriggerData.Event = eventOutput
			}
		case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
			if manualOutput, ok := triggerData.Output.(*avsproto.ManualTrigger_Output); ok {
				v.parsedTriggerData.Manual = manualOutput
			}
		}
	}

	// Create trigger data variable if we have a valid trigger name and trigger data
	if triggerData != nil {
		triggerNameStd, err := v.GetTriggerNameAsVar()
		v.logger.Debug("VM Creation DEBUG", "triggerNameStd", triggerNameStd, "error", err)
		if err == nil { // Proceed if trigger name is valid
			var triggerDataMap map[string]interface{}

			// If we have transfer log data, use it to populate rich trigger data and create proper Event structure
			if transferLog != nil {
				// New format: google.protobuf.Value
				v.parsedTriggerData.Event = &avsproto.EventTrigger_Output{
					Data: transferLog,
				}
				// Convert protobuf value to map
				triggerDataMap = convertProtobufValueToMap(transferLog)
			} else {
				// Use shared function to build trigger data map from protobuf trigger outputs
				triggerDataMap = buildTriggerDataMapFromProtobuf(triggerData.Type, triggerData.Output, v.logger)

				// Debug: Log what triggerData we received and what we built
				v.logger.Debug("VM Creation DEBUG: triggerData received", "triggerData.Type", triggerData.Type, "triggerData.Output", fmt.Sprintf("%+v", triggerData.Output))
				v.logger.Debug("VM Creation DEBUG: buildTriggerDataMapFromProtobuf result", "triggerDataMap", fmt.Sprintf("%+v", triggerDataMap))
			}

			// Create dual-access map to support both camelCase and snake_case field access
			// Extract trigger input data and create trigger variable using shared function
			var triggerInputData map[string]interface{}
			if task != nil && task.Trigger != nil {
				triggerInputData = TaskTriggerToConfig(task.Trigger)
			}

			// Use shared function to build trigger variable data
			triggerVarData := buildTriggerVariableData(task.Trigger, triggerDataMap, triggerInputData)

			// Debug: Log the final trigger variable data
			v.logger.Debug("VM Creation DEBUG: Final trigger variable", "triggerName", triggerNameStd, "triggerVarData", fmt.Sprintf("%+v", triggerVarData))

			v.AddVar(triggerNameStd, triggerVarData)
		}
	} else if task != nil { // Fallback if triggerData is nil but task is not
		triggerNameStd, err := v.GetTriggerNameAsVar()
		if err == nil {
			// Extract trigger input data even when triggerData is nil
			var triggerInputData map[string]interface{}
			if task.Trigger != nil {
				triggerInputData = TaskTriggerToConfig(task.Trigger)
			}

			// Extract trigger config data to use as the trigger's output data
			var triggerDataMap map[string]interface{}
			if task.Trigger != nil {
				triggerConfig := TaskTriggerToConfig(task.Trigger)
				// Use the trigger config as the trigger's output data for all trigger types
				triggerDataMap = triggerConfig
			}

			// Use shared function to build trigger variable data with config data
			triggerVarData := buildTriggerVariableData(task.Trigger, triggerDataMap, triggerInputData)
			v.AddVar(triggerNameStd, triggerVarData)
		}
	}

	// Add trigger configuration data for access by subsequent nodes
	if task != nil && task.Trigger != nil {
		triggerConfig := map[string]interface{}{
			"id":   task.Trigger.Id,
			"name": task.Trigger.Name,
			"type": task.Trigger.Type.String(),
		}

		// Add trigger-specific config data matching SDK interfaces
		switch {
		case task.Trigger.GetEvent() != nil:
			if eventTrigger := task.Trigger.GetEvent(); eventTrigger != nil && eventTrigger.Config != nil {
				// Convert to match EventTriggerDataType from SDK
				queries := make([]interface{}, len(eventTrigger.Config.Queries))
				for i, query := range eventTrigger.Config.Queries {
					queryMap := map[string]interface{}{}

					// Add addresses if present
					if len(query.Addresses) > 0 {
						queryMap["addresses"] = query.Addresses
					}

					// Convert topics to match SDK structure
					if len(query.Topics) > 0 {
						topics := make([]interface{}, len(query.Topics))
						for j, topic := range query.Topics {
							topics[j] = map[string]interface{}{
								"values": topic.Values,
							}
						}
						queryMap["topics"] = topics
					}

					// Add maxEventsPerBlock if present
					if query.MaxEventsPerBlock != nil {
						queryMap["maxEventsPerBlock"] = *query.MaxEventsPerBlock
					}

					queries[i] = queryMap
				}

				triggerConfig["data"] = map[string]interface{}{
					"queries": queries,
				}
			}

		case task.Trigger.GetCron() != nil:
			if cronTrigger := task.Trigger.GetCron(); cronTrigger != nil && cronTrigger.Config != nil {
				triggerConfig["data"] = map[string]interface{}{
					"schedules": cronTrigger.Config.Schedules, // matches CronTriggerDataType
				}
			}

		case task.Trigger.GetFixedTime() != nil:
			if fixedTimeTrigger := task.Trigger.GetFixedTime(); fixedTimeTrigger != nil && fixedTimeTrigger.Config != nil {
				triggerConfig["data"] = map[string]interface{}{
					"epochs": fixedTimeTrigger.Config.Epochs, // matches FixedTimeTriggerDataType
				}
			}

		case task.Trigger.GetBlock() != nil:
			if blockTrigger := task.Trigger.GetBlock(); blockTrigger != nil && blockTrigger.Config != nil {
				triggerConfig["data"] = map[string]interface{}{
					"interval": blockTrigger.Config.Interval, // matches BlockTriggerDataType
				}
			}

		case task.Trigger.Type == avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
			// Manual triggers typically don't have config data, but we include the structure for consistency
			triggerConfig["data"] = map[string]interface{}{}
		}

		// Add triggerConfig variable to VM
		v.AddVar("triggerConfig", triggerConfig)
	}

	return v, nil
}

func (v *VM) CreateSandbox() error {
	return nil // Placeholder
}

func (v *VM) AddVar(key string, value any) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vars == nil {
		v.vars = make(map[string]any)
	}
	v.vars[key] = value
}

// Compile generates an execution plan. Constants local to Compile to avoid redeclaration issues.
func (v *VM) Compile() error {
	const (
		invalidTaskData   = "invalid task data for compilation"
		invalidEntrypoint = "invalid entrypoint for compilation"
	)

	v.mu.Lock()
	defer v.mu.Unlock()

	if v.Status == VMStateCompiled || v.Status == VMStateReady {
		v.plans = make(map[string]*Step)
		v.entrypoint = ""
		v.Status = VMStateInitialize
	} else if v.Status != VMStateInitialize {
		return fmt.Errorf("VM not in initializable state: %s", v.Status)
	}

	if v.task == nil || v.task.Task == nil {
		if v.entrypoint != "" && len(v.plans) > 0 { // For RunNodeWithInputs scenario
			v.Status = VMStateCompiled
			return nil
		}
		return fmt.Errorf(invalidTaskData + ": task definition is nil")
	}

	if len(v.TaskNodes) == 0 && v.task.Nodes != nil { // Populate if NewVM then task was set
		for _, node := range v.task.Nodes {
			v.TaskNodes[node.Id] = node
		}
	}

	if len(v.TaskNodes) == 0 && len(v.task.Edges) > 0 {
		return fmt.Errorf("task has edges but no nodes defined in TaskNodes map")
	}
	if len(v.TaskNodes) == 0 && len(v.task.Edges) == 0 { // Empty task
		v.entrypoint = ""
		v.Status = VMStateCompiled
		return nil
	}

	adj := make(map[string][]string)
	inDegree := make(map[string]int)
	for nodeID := range v.TaskNodes {
		inDegree[nodeID] = 0
	}

	for _, edge := range v.task.Edges {
		isBranchSource := strings.Contains(edge.Source, ".")
		isTriggerSource := v.task.Trigger != nil && edge.Source == v.task.Trigger.Id
		if !isBranchSource && !isTriggerSource {
			if _, ok := v.TaskNodes[edge.Source]; !ok {
				return fmt.Errorf("compile error: source node '%s' in edge '%s' not in TaskNodes", edge.Source, edge.Id)
			}
		}
		if _, ok := v.TaskNodes[edge.Target]; !ok {
			return fmt.Errorf("compile error: target node '%s' in edge '%s' not in TaskNodes", edge.Target, edge.Id)
		}
		adj[edge.Source] = append(adj[edge.Source], edge.Target)

		// Debug logging for edge processing
		if v.logger != nil {
			v.logger.Info("🔧 VM.Compile: Processing edge",
				"edgeID", edge.Id,
				"source", edge.Source,
				"target", edge.Target,
				"isBranchSource", isBranchSource,
				"isTriggerSource", isTriggerSource)
		}

		// Only count in-degree for edges from actual TaskNodes or triggers
		// Branch condition edges don't count toward in-degree since they're not real nodes
		if !isBranchSource {
			inDegree[edge.Target]++ // All valid targets contribute to in-degree. Trigger adjustment comes next.
		}
	}

	// Track nodes that are targets of branch condition edges
	// These should not be in the initial queue even if they have in-degree 0
	branchTargets := make(map[string]bool)
	for _, edge := range v.task.Edges {
		if strings.Contains(edge.Source, ".") { // Branch condition edge
			branchTargets[edge.Target] = true
		}
	}

	q := make([]string, 0, len(v.TaskNodes))
	initialEntryCandidates := make(map[string]bool)

	if v.task.Trigger == nil || v.task.Trigger.Id == "" {
		if v.entrypoint == "" { // Only if not set by RunNodeWithInputs
			for nodeID, deg := range inDegree {
				if deg == 0 && !branchTargets[nodeID] {
					q = append(q, nodeID)
					initialEntryCandidates[nodeID] = true
				}
			}
			if len(q) == 0 && len(v.TaskNodes) > 0 {
				return fmt.Errorf(invalidEntrypoint + ": no trigger defined and no node with in-degree 0 found")
			}
		}
	} else {
		triggerID := v.task.Trigger.Id
		foundTriggerEdges := false
		for _, edge := range v.task.Edges {
			if edge.Source == triggerID {
				targetNodeID := edge.Target
				if _, ok := v.TaskNodes[targetNodeID]; !ok {
					return fmt.Errorf("compile error: trigger '%s' targets non-existent node '%s'", triggerID, targetNodeID)
				}
				// This node is a direct target from the trigger.
				// Its in-degree from other *TaskNodes* should be 0 for it to be a true start.
				// We decrement its in-degree as the trigger "satisfies" one incoming edge.
				inDegree[targetNodeID]--
				initialEntryCandidates[targetNodeID] = true
				foundTriggerEdges = true
			}
		}
		if !foundTriggerEdges && len(v.TaskNodes) > 0 {
			return fmt.Errorf(invalidEntrypoint+": trigger '%s' has no outgoing edges to TaskNodes", triggerID)
		}
		// After adjustments, collect all nodes with in-degree 0.
		for nodeID, deg := range inDegree {
			if deg == 0 && !branchTargets[nodeID] {
				// Add nodes with in-degree 0 that are not targets of branch condition edges
				// Branch targets should only be executed when their branch condition is met
				q = append(q, nodeID)
			}
		}

		if len(q) == 0 && len(v.TaskNodes) > 0 {
			return fmt.Errorf(invalidEntrypoint + ": no node has an in-degree of 0 after trigger processing")
		}
	}

	if v.entrypoint == "" {
		// If we have a trigger, set it as the entrypoint so vm.Run() can handle the fan-out
		if v.task.Trigger != nil && v.task.Trigger.Id != "" {
			v.entrypoint = v.task.Trigger.Id
			// Add the trigger to the plans so vm.Run() can process it
			triggerPlan := &Step{NodeID: v.task.Trigger.Id}
			if nextNodeIDs, ok := adj[v.task.Trigger.Id]; ok {
				triggerPlan.Next = nextNodeIDs
				// Debug logging to see what nodes are connected to the trigger
				if v.logger != nil {
					v.logger.Info("🔧 VM.Compile: Created trigger plan",
						"triggerID", v.task.Trigger.Id,
						"nextNodesCount", len(nextNodeIDs),
						"nextNodes", nextNodeIDs)
				}
			} else {
				if v.logger != nil {
					v.logger.Warn("🔧 VM.Compile: No adjacency found for trigger", "triggerID", v.task.Trigger.Id)
				}
			}
			v.plans[v.task.Trigger.Id] = triggerPlan
		} else if len(q) > 0 {
			v.entrypoint = q[0]
		} else if len(v.TaskNodes) > 0 {
			return fmt.Errorf(invalidEntrypoint + ": no entry point identified (queue empty after all checks)")
		}
	}

	processedCount := 0
	topoQueue := make([]string, len(q))
	copy(topoQueue, q)

	for len(topoQueue) > 0 {
		currNodeID := topoQueue[0]
		topoQueue = topoQueue[1:]
		processedCount++

		planStep := &Step{NodeID: currNodeID}
		if nextNodeIDs, ok := adj[currNodeID]; ok {
			planStep.Next = nextNodeIDs
		}
		v.plans[currNodeID] = planStep

		if node, isTaskNode := v.TaskNodes[currNodeID]; isTaskNode && node.GetBranch() != nil {
			// For branch nodes, we need to create plans for each condition ID
			// so that edges like "branch1.a1" can be resolved
			branchNode := node.GetBranch()
			if branchNode.Config != nil && len(branchNode.Config.Conditions) > 0 {
				for _, condition := range branchNode.Config.Conditions {
					conditionID := fmt.Sprintf("%s.%s", currNodeID, condition.Id)
					// Create a plan for this condition ID
					conditionPlan := &Step{NodeID: conditionID}
					// Find edges that have this condition as source
					if nextNodeIDs, ok := adj[conditionID]; ok {
						conditionPlan.Next = nextNodeIDs
					}
					v.plans[conditionID] = conditionPlan
				}
			}
		}

		for _, neighborNodeID := range adj[currNodeID] {
			if _, isActualNode := v.TaskNodes[neighborNodeID]; isActualNode {
				inDegree[neighborNodeID]--
				if inDegree[neighborNodeID] == 0 {
					topoQueue = append(topoQueue, neighborNodeID)
				}
			}
		}

		// Branch condition edges are now handled at execution time, not compile time
		// since conditions are in Input messages
	}

	if processedCount != len(v.TaskNodes) {
		// Check if the unprocessed nodes are all branch targets or nodes reachable only through branch targets
		// Branch targets are not processed in the initial topological sort
		unprocessedNodes := len(v.TaskNodes) - processedCount

		// Find all nodes that are reachable only through branch targets
		allUnreachableNodes := make(map[string]bool)
		for nodeID := range v.TaskNodes {
			if _, exists := v.plans[nodeID]; !exists {
				allUnreachableNodes[nodeID] = true
			}
		}

		// Check if all unprocessed nodes are either branch targets or reachable only through branch targets
		if unprocessedNodes == len(allUnreachableNodes) {
			// All unprocessed nodes are unreachable through normal topological sort
			// But we still need to add them to the plans so they can be executed when branch conditions are met
			for nodeID := range allUnreachableNodes {
				// This node wasn't processed in the topological sort, add it to plans
				planStep := &Step{NodeID: nodeID}
				if nextNodeIDs, ok := adj[nodeID]; ok {
					planStep.Next = nextNodeIDs
				}
				v.plans[nodeID] = planStep
			}
			v.Status = VMStateCompiled
			return nil
		}
		return fmt.Errorf("cycle detected: processed %d nodes, but %d TaskNodes exist", processedCount, len(v.TaskNodes))
	}

	v.Status = VMStateCompiled
	return nil
}

func (v *VM) Run() error {
	v.mu.Lock()                                                  // Lock for initial status check and update
	if v.Status != VMStateReady && v.Status != VMStateCompiled { // Allow run if compiled, will set to ready
		v.mu.Unlock()
		// Attempt to compile if not ready. If it's initialize, compile might work.
		if v.Status == VMStateInitialize {
			v.mu.Unlock() // Unlock before calling Compile, which takes its own lock
			if err := v.Compile(); err != nil {
				return fmt.Errorf("auto-compile failed: %w", err)
			}
			v.mu.Lock() // Re-lock
		} else {
			v.mu.Unlock()
			return fmt.Errorf("VM not in ready or compiled state to run, current state: %s", v.Status)
		}
	}
	if v.Status == VMStateCompiled { // If compiled, transition to ready
		v.Status = VMStateReady
	}
	v.Status = VMStateRunning
	v.mu.Unlock() // Unlock after status updates, before long running execution

	// Defer status update requires its own lock if Run can error out early
	defer func() {
		v.mu.Lock()
		v.Status = VMStateCompleted
		v.mu.Unlock()
	}()

	v.mu.Lock()                                    // Lock for accessing plans and entrypoint
	if len(v.plans) == 0 && len(v.TaskNodes) > 0 { // Check if plans is empty but there are nodes (implies not compiled)
		v.mu.Unlock()
		return fmt.Errorf("internal error: not compiled or no execution plan for existing nodes")
	}
	if v.entrypoint == "" && len(v.TaskNodes) > 0 {
		v.mu.Unlock()
		return fmt.Errorf("internal error: entrypoint not set for existing nodes")
	}
	if v.entrypoint == "" && len(v.TaskNodes) == 0 { // No nodes, no entrypoint, valid empty execution
		v.mu.Unlock()
		return nil
	}

	currentStep := v.plans[v.entrypoint]
	v.mu.Unlock() // Unlock before starting loop

	for currentStep != nil {
		var node *avsproto.TaskNode
		var ok bool
		var stepToExecute *Step // Need a copy for potential modification if jump occurs

		v.mu.Lock() // Lock for accessing TaskNodes and plans
		stepToExecute = currentStep
		node, ok = v.TaskNodes[stepToExecute.NodeID]

		v.mu.Unlock()

		if !ok {
			// This can happen if a branch condition ID is in currentStep.NodeID
			// but it doesn't map directly to a TaskNode (it's a conceptual step).
			// The actual jump should have been resolved by executeNode (branch).
			// If we reach here with a non-TaskNode ID, it's likely a conceptual step like "branch1.a1"
			// or a trigger that should immediately proceed to its Next nodes.
			v.mu.Lock()
			if len(stepToExecute.Next) == 0 {
				currentStep = nil // End of this path
			} else if len(stepToExecute.Next) == 1 {
				// Single next step - normal sequential execution
				currentStep = v.plans[stepToExecute.Next[0]]
			} else {
				// Multiple next steps - queue all for execution in the main loop
				// This handles cases like triggers that fan out to multiple nodes
				if v.logger != nil {
					v.logger.Info("🔄 Processing multiple next steps", "count", len(stepToExecute.Next), "steps", stepToExecute.Next)
				}

				// Create a simple queue of steps to execute
				stepQueue := make([]*Step, 0, len(stepToExecute.Next))
				for _, nextStepID := range stepToExecute.Next {
					if nextStep, exists := v.plans[nextStepID]; exists {
						stepQueue = append(stepQueue, nextStep)
					}
				}

				// Process all steps in the queue sequentially
				for i, step := range stepQueue {
					if v.logger != nil {
						v.logger.Info("🔄 Processing queued step", "step", step.NodeID, "index", i+1, "total", len(stepQueue))
					}

					// Set current step and let the main loop process it
					if i == 0 {
						// First step becomes the current step for this iteration
						currentStep = step
					} else {
						// For additional steps, we need to process them immediately
						// since the main loop will only process currentStep
						if node, nodeExists := v.TaskNodes[step.NodeID]; nodeExists {
							v.mu.Unlock() // Unlock before calling executeNode to avoid deadlock
							if v.logger != nil {
								v.logger.Info("🔄 Executing queued node", "nodeID", step.NodeID)
							}
							_, err := v.executeNode(node)
							if err != nil && v.logger != nil {
								v.logger.Error("🔄 Error executing queued node", "nodeID", step.NodeID, "error", err)
							} else if v.logger != nil {
								v.logger.Info("🔄 Successfully executed queued node", "nodeID", step.NodeID)
							}
							v.mu.Lock() // Re-lock for the next iteration
						}
					}
				}

				if v.logger != nil {
					v.logger.Info("🔄 Completed processing all queued steps", "processedCount", len(stepQueue))
				}
			}
			v.mu.Unlock()
			continue
		}

		jump, err := v.executeNode(node) // executeNode calls sub-processors which should use AddVar for VM state changes

		if err != nil {
			// Instead of aborting on first error, we now continue execution
			// The failed step should already be logged by executeNode/runXXX methods
			// Log the error but continue to next step
			if v.logger != nil {
				errorMsg := err.Error()
				// Use regex to remove stack-trace lines for cleaner logging (common in JS errors)
				stackTraceRegex := regexp.MustCompile(`(?m)^\s*at .*$`)
				errorMsg = stackTraceRegex.ReplaceAllString(errorMsg, "")
				// Clean up any extra whitespace left behind
				errorMsg = strings.TrimSpace(errorMsg)

				// Categorize errors to avoid unnecessary stack traces for expected validation errors
				if isExpectedValidationError(err) {
					// Expected validation errors - log at WARN level without stack traces
					v.logger.Warn("node execution failed, continuing execution", "nodeID", node.Id, "error", errorMsg)
				} else {
					// Unexpected system errors - log at ERROR level without stack traces for cleaner output
					v.logger.Error("node execution failed, continuing execution", "nodeID", node.Id, "error", errorMsg)
				}
			}

			// Continue to next step in sequence (don't follow jump since this node failed)
			v.mu.Lock()
			if len(stepToExecute.Next) == 0 {
				currentStep = nil // End of this path
			} else {
				currentStep = v.plans[stepToExecute.Next[0]]
			}
			v.mu.Unlock()
			continue
		}

		v.mu.Lock()      // Lock for plan navigation
		if jump != nil { // A jump occurred (e.g. from a branch)
			currentStep = jump
		} else { // No jump, proceed to next in sequence
			if len(stepToExecute.Next) == 0 {
				currentStep = nil // End of this path
			} else if len(stepToExecute.Next) == 1 {
				// Single next step - normal sequential execution
				currentStep = v.plans[stepToExecute.Next[0]]
			} else {
				// Multiple next steps - execute all sequentially
				// This handles cases like triggers that fan out to multiple nodes
				for _, nextStepID := range stepToExecute.Next {
					if nextStep, exists := v.plans[nextStepID]; exists {
						v.executeSequentialPath(nextStep)
					}
				}
				currentStep = nil // End execution after all paths complete
			}
		}
		v.mu.Unlock()
	}
	return nil
}

// executeSequentialPath executes a sequential execution path starting from the given step
// This method is used when a node (like a trigger) fans out to multiple target nodes
func (v *VM) executeSequentialPath(startStep *Step) {
	if startStep == nil {
		return
	}

	currentStep := startStep

	for currentStep != nil {
		var node *avsproto.TaskNode
		var ok bool
		var stepToExecute *Step

		v.mu.Lock() // Lock for accessing TaskNodes and plans
		stepToExecute = currentStep
		node, ok = v.TaskNodes[stepToExecute.NodeID]
		v.mu.Unlock()

		if !ok {
			// Handle conceptual steps like branch conditions
			v.mu.Lock()
			if len(stepToExecute.Next) > 0 {
				nextStepID := stepToExecute.Next[0]
				currentStep = v.plans[nextStepID]
				v.mu.Unlock()
				continue
			}
			v.mu.Unlock()
			currentStep = nil
			continue
		}

		jump, err := v.executeNode(node)
		if err != nil {
			// Log error but continue execution in this parallel path
			if v.logger != nil {
				errorMsg := err.Error()
				stackTraceRegex := regexp.MustCompile(`(?m)^\s*at .*$`)
				errorMsg = stackTraceRegex.ReplaceAllString(errorMsg, "")
				errorMsg = strings.TrimSpace(errorMsg)

				if isExpectedValidationError(err) {
					v.logger.Warn("sequential path node execution failed, continuing", "nodeID", node.Id, "error", errorMsg)
				} else {
					v.logger.Error("sequential path node execution failed, continuing", "nodeID", node.Id, "error", errorMsg)
				}
			}

			// Continue to next step in this sequential path
			v.mu.Lock()
			if len(stepToExecute.Next) == 0 {
				currentStep = nil
			} else {
				currentStep = v.plans[stepToExecute.Next[0]]
			}
			v.mu.Unlock()
			continue
		}

		v.mu.Lock()
		if jump != nil { // A jump occurred (e.g. from a branch)
			currentStep = jump
		} else { // No jump, proceed to next in sequence
			if len(stepToExecute.Next) == 0 {
				currentStep = nil // End of this sequential path
			} else {
				// In sequential paths, we only follow the first next step (no further fan-out)
				currentStep = v.plans[stepToExecute.Next[0]]
			}
		}
		v.mu.Unlock()
	}
}

// executeIndependentPath executes a single node independently
// This method is used when a trigger fans out to multiple target nodes that should be executed independently
func (v *VM) executeIndependentPath(startStep *Step) {
	if startStep == nil {
		if v.logger != nil {
			v.logger.Warn("🔄 executeIndependentPath: startStep is nil")
		}
		return
	}

	if v.logger != nil {
		v.logger.Info("🔄 executeIndependentPath: Starting execution", "nodeID", startStep.NodeID)
	}

	var node *avsproto.TaskNode
	var ok bool

	v.mu.Lock() // Lock for accessing TaskNodes
	node, ok = v.TaskNodes[startStep.NodeID]
	v.mu.Unlock()

	if !ok {
		// Handle conceptual steps like branch conditions - skip if not a real node
		if v.logger != nil {
			v.logger.Debug("executeIndependentPath: skipping non-TaskNode step", "nodeID", startStep.NodeID)
		}
		return
	}

	if v.logger != nil {
		v.logger.Info("🔄 executeIndependentPath: Found TaskNode, executing",
			"nodeID", startStep.NodeID,
			"nodeType", node.Type.String(),
			"nodeName", node.Name)
	}

	// Execute the single node independently
	_, err := v.executeNode(node)
	if err != nil {
		// Log error but don't propagate since this is independent execution
		if v.logger != nil {
			errorMsg := err.Error()
			stackTraceRegex := regexp.MustCompile(`(?m)^\s*at .*$`)
			errorMsg = stackTraceRegex.ReplaceAllString(errorMsg, "")
			errorMsg = strings.TrimSpace(errorMsg)

			if isExpectedValidationError(err) {
				v.logger.Warn("independent path node execution failed", "nodeID", node.Id, "error", errorMsg)
			} else {
				v.logger.Error("independent path node execution failed", "nodeID", node.Id, "error", errorMsg)
			}
		}
	} else {
		if v.logger != nil {
			v.logger.Info("🔄 executeIndependentPath: Successfully executed node", "nodeID", startStep.NodeID)
		}
	}
}

func (v *VM) executeNode(node *avsproto.TaskNode) (*Step, error) {
	v.mu.Lock()
	v.instructionCount++
	v.mu.Unlock()

	if node == nil {
		return nil, fmt.Errorf("executeNode called with nil node")
	}

	// Extract and set input data for this node (making it available as node_name.input)
	inputData := ExtractNodeConfiguration(node)
	if inputData != nil {
		processor := &CommonProcessor{vm: v}
		processor.SetInputVarForStep(node.Id, inputData)
	}

	var nextStep *Step // This is the *next step in the plan to jump to*, not the execution log step
	var err error

	// Note: Each runXxx method is responsible for creating an Execution_Step,
	// adding it to v.ExecutionLogs, and setting output vars using v.AddVar or commonProcessor.SetOutputVarForStep.
	// v.AddVar and SetOutputVarForStep handle their own locking for v.vars.

	var executionLogForNode *avsproto.Execution_Step // To capture log from methods that return it

	if nodeValue := node.GetRestApi(); nodeValue != nil {
		executionLogForNode, err = v.runRestApi(node.Id, nodeValue) // runRestApi returns *avsproto.Execution_Step, error
		if executionLogForNode != nil {
			v.addExecutionLog(executionLogForNode)
		}
	} else if nodeValue := node.GetBranch(); nodeValue != nil {
		var branchLog *avsproto.Execution_Step
		branchLog, nextStep, err = v.runBranch(node.Id, nodeValue) // runBranch now returns log, step, err
		if branchLog != nil {
			v.addExecutionLog(branchLog) // Log is added by runBranch itself if successful, this is a fallback or if runBranch changes
		}
	} else if nodeValue := node.GetGraphqlQuery(); nodeValue != nil {
		executionLogForNode, err = v.runGraphQL(node.Id, nodeValue)
		if executionLogForNode != nil {
			v.addExecutionLog(executionLogForNode)
		}
	} else if nodeValue := node.GetCustomCode(); nodeValue != nil {
		executionLogForNode, err = v.runCustomCode(node.Id, nodeValue)
		if executionLogForNode != nil {
			v.addExecutionLog(executionLogForNode)
		}
	} else if nodeValue := node.GetContractRead(); nodeValue != nil {
		executionLogForNode, err = v.runContractRead(node.Id, nodeValue)
		if executionLogForNode != nil {
			v.addExecutionLog(executionLogForNode)
		}
	} else if nodeValue := node.GetContractWrite(); nodeValue != nil {
		executionLogForNode, err = v.runContractWrite(node.Id, nodeValue)
		if executionLogForNode != nil {
			v.addExecutionLog(executionLogForNode)
		}
	} else if nodeValue := node.GetLoop(); nodeValue != nil {
		executionLogForNode, err = v.runLoop(node.Id, nodeValue) // loop does not return a jump step
		if executionLogForNode != nil {
			v.addExecutionLog(executionLogForNode)
		}
	} else if nodeValue := node.GetFilter(); nodeValue != nil {
		executionLogForNode, err = v.runFilter(node.Id, nodeValue)
		if executionLogForNode != nil {
			v.addExecutionLog(executionLogForNode)
		}
	} else if nodeValue := node.GetEthTransfer(); nodeValue != nil {
		executionLogForNode, err = v.runEthTransfer(node.Id, nodeValue)
		if executionLogForNode != nil {
			v.addExecutionLog(executionLogForNode)
		}
	} else {
		err = fmt.Errorf("unknown node type for node ID %s", node.Id)
	}

	if err != nil {
		// If a log was created by the failing function, it might already be in ExecutionLogs.
		// If not, or if we want to ensure a general error log for this step if one wasn't made:
		// This logic might be redundant if all runXxx functions guarantee a log on error.
		// For now, rely on sub-functions to log their own specifics.
		return nil, err // Return nil for step if error, error itself is primary
	}

	if nextStep != nil { // A specific jump was determined (e.g. by branch)
		return nextStep, nil
	}

	// If no specific jump, return nil for step, indicating sequential progression based on original plan.
	// The caller (VM.Run) will use the default next from plans[node.Id].Next[0].
	return nil, nil
}

func (v *VM) addExecutionLog(log *avsproto.Execution_Step) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.ExecutionLogs = append(v.ExecutionLogs, log)
}

func (v *VM) runRestApi(stepID string, nodeValue *avsproto.RestAPINode) (*avsproto.Execution_Step, error) {
	p := NewRestProrcessor(v)                         // v is passed, CommonProcessor uses v.AddVar
	executionLog, err := p.Execute(stepID, nodeValue) // p.Execute should use SetOutputVarForStep
	v.mu.Lock()
	executionLog.Inputs = v.collectInputKeysForLog(stepID) // Pass stepID to exclude current node's variables
	v.mu.Unlock()
	// v.addExecutionLog(executionLog) // Caller will add
	return executionLog, err // RestAPI node doesn't dictate a jump
}

func (v *VM) runGraphQL(stepID string, node *avsproto.GraphQLQueryNode) (*avsproto.Execution_Step, error) {
	// GraphQL URL and query are now in Config message, not input variables
	g, err := NewGraphqlQueryProcessor(v)     // No URL parameter needed, processor gets it from Config
	var executionLog *avsproto.Execution_Step // Declare to ensure it's always initialized
	if err != nil {
		// Create a failed execution log step
		executionLog = v.createExecutionStep(stepID, false, err.Error(), "", time.Now().UnixMilli())
		executionLog.EndAt = time.Now().UnixMilli()
		// v.addExecutionLog(logEntry) // Caller will add
		return executionLog, err
	}
	var dataOutput any                                      // Placeholder, GraphQL processor might return structured data
	executionLog, dataOutput, err = g.Execute(stepID, node) // Execute should use SetOutputVarForStep
	_ = dataOutput                                          // Use dataOutput if needed later
	v.mu.Lock()
	if executionLog != nil { // Guard against nil log
		executionLog.Inputs = v.collectInputKeysForLog(stepID)
	}
	v.mu.Unlock()
	// v.addExecutionLog(executionLog) // Caller will add
	// err from g.Execute is the primary error to return
	return executionLog, err
}

func (v *VM) runContractRead(stepID string, node *avsproto.ContractReadNode) (*avsproto.Execution_Step, error) {
	var executionLog *avsproto.Execution_Step

	// Check if node has empty config first - let processor handle this case
	if node.Config != nil && (node.Config.ContractAddress == "" || len(node.Config.MethodCalls) == 0 || node.Config.ContractAbi == "") {
		// Empty config case - create a mock processor to handle the error
		processor := NewContractReadProcessor(v, nil)
		executionLog, err := processor.Execute(stepID, node)
		v.mu.Lock()
		if executionLog != nil {
			executionLog.Inputs = v.collectInputKeysForLog(stepID)
		}
		v.mu.Unlock()
		return executionLog, err
	}

	if v.smartWalletConfig == nil || v.smartWalletConfig.EthRpcUrl == "" {
		err := fmt.Errorf("smart wallet config or ETH RPC URL not set for contract read")
		executionLog = v.createExecutionStep(stepID, false, err.Error(), "", time.Now().UnixMilli())
		executionLog.EndAt = time.Now().UnixMilli()
		// v.addExecutionLog(logEntry)
		return executionLog, err
	}
	rpcClient, err := ethclient.Dial(v.smartWalletConfig.EthRpcUrl)
	if err != nil {
		executionLog = v.createExecutionStep(stepID, false, fmt.Sprintf("failed to dial ETH RPC: %v", err), "", time.Now().UnixMilli())
		executionLog.EndAt = time.Now().UnixMilli()
		// v.addExecutionLog(logEntry)
		return executionLog, err
	}
	defer rpcClient.Close()

	processor := NewContractReadProcessor(v, rpcClient)
	executionLog, err = processor.Execute(stepID, node)
	v.mu.Lock()
	if executionLog != nil {
		executionLog.Inputs = v.collectInputKeysForLog(stepID)
	}
	v.mu.Unlock()
	// v.addExecutionLog(executionLog)
	return executionLog, err
}

func (v *VM) runContractWrite(stepID string, node *avsproto.ContractWriteNode) (*avsproto.Execution_Step, error) {
	var executionLog *avsproto.Execution_Step
	if v.smartWalletConfig == nil || v.smartWalletConfig.EthRpcUrl == "" {
		err := fmt.Errorf("smart wallet config or ETH RPC URL not set for contract write")
		executionLog = v.createExecutionStep(stepID, false, err.Error(), "", time.Now().UnixMilli())
		executionLog.EndAt = time.Now().UnixMilli()
		// v.addExecutionLog(logEntry)
		return executionLog, err
	}
	rpcClient, err := ethclient.Dial(v.smartWalletConfig.EthRpcUrl)
	if err != nil {
		executionLog = v.createExecutionStep(stepID, false, fmt.Sprintf("failed to dial ETH RPC: %v", err), "", time.Now().UnixMilli())
		executionLog.EndAt = time.Now().UnixMilli()
		// v.addExecutionLog(logEntry)
		return executionLog, err
	}
	defer rpcClient.Close()

	processor := NewContractWriteProcessor(v, rpcClient, v.smartWalletConfig, v.TaskOwner)
	executionLog, err = processor.Execute(stepID, node)
	v.mu.Lock()
	if executionLog != nil {
		executionLog.Inputs = v.collectInputKeysForLog(stepID)
	}
	v.mu.Unlock()
	// v.addExecutionLog(executionLog)
	return executionLog, err
}

func (v *VM) runCustomCode(stepID string, node *avsproto.CustomCodeNode) (*avsproto.Execution_Step, error) {
	// Special handling for blockTrigger nodes that were created via CreateNodeFromType
	// These nodes have no Config but should be handled specially
	if node.Config == nil {
		// Check if this is a blockTrigger node by looking at the node name in TaskNodes
		v.mu.Lock()
		taskNode, exists := v.TaskNodes[stepID]
		v.mu.Unlock()

		if v.logger != nil {
			v.logger.Info("runCustomCode: Config is nil", "stepID", stepID, "exists", exists)
			if exists {
				v.logger.Info("runCustomCode: TaskNode details", "name", taskNode.Name, "id", taskNode.Id)
			}
		}

		if exists && strings.Contains(taskNode.Name, NodeTypeBlockTrigger) {
			if v.logger != nil {
				v.logger.Error("runCustomCode: BlockTrigger nodes require real blockchain data - mock data not supported", "stepID", stepID, "name", taskNode.Name)
			}

			// Get node configuration for error step
			var nodeConfig *structpb.Value
			nodeConfigMap := ExtractNodeConfiguration(taskNode)
			if nodeConfigMap != nil {
				if configProto, err := structpb.NewValue(nodeConfigMap); err == nil {
					nodeConfig = configProto
				}
			}

			return &avsproto.Execution_Step{
				Id:      stepID, // Use new 'id' field
				Success: false,
				Error:   "BlockTrigger nodes require real blockchain data - mock data not supported",
				StartAt: time.Now().UnixMilli(),
				EndAt:   time.Now().UnixMilli(),
				Config:  nodeConfig, // Include node configuration data for debugging
			}, fmt.Errorf("BlockTrigger nodes require real blockchain data - mock data not supported")
		}

		// If Config is nil and it's not a blockTrigger, return an error
		if v.logger != nil {
			v.logger.Error("runCustomCode: CustomCodeNode Config is nil", "stepID", stepID)
		}
		// Get the node's configuration data
		var nodeConfig *structpb.Value
		v.mu.Lock()
		if taskNode, exists := v.TaskNodes[stepID]; exists {
			// Extract node configuration instead of the removed input field
			nodeConfigMap := ExtractNodeConfiguration(taskNode)
			if nodeConfigMap != nil {
				if configProto, err := structpb.NewValue(nodeConfigMap); err == nil {
					nodeConfig = configProto
				}
			}
		}
		v.mu.Unlock()

		return &avsproto.Execution_Step{
			Id:      stepID, // Use new 'id' field
			Success: false,
			Error:   "CustomCodeNode Config is nil",
			StartAt: time.Now().UnixMilli(),
			EndAt:   time.Now().UnixMilli(),
			Config:  nodeConfig, // Include node configuration data for debugging
		}, fmt.Errorf("CustomCodeNode Config is nil")
	}

	// Normal custom code execution
	r := NewJSProcessor(v)
	executionLog, err := r.Execute(stepID, node)
	v.mu.Lock()
	if executionLog != nil {
		executionLog.Inputs = v.collectInputKeysForLog(stepID)
	}
	v.mu.Unlock()
	// v.addExecutionLog(executionLog)
	return executionLog, err
}

func (v *VM) runBranch(stepID string, nodeValue *avsproto.BranchNode) (*avsproto.Execution_Step, *Step, error) {
	processor := NewBranchProcessor(v)
	executionLog, nextStep, err := processor.Execute(stepID, nodeValue) // This evaluates conditions
	// executionLog is already populated by BranchProcessor.Execute, including Inputs.
	// v.addExecutionLog(executionLog) // BranchProcessor's Execute already adds its log to its own VM or handles it.
	// The log returned here is for the *main* VM.
	// The BranchProcessor's Execute is expected to return a log ready for the main VM.

	if executionLog != nil {
		v.mu.Lock()
		// Ensure inputs are collected if not already done by processor, or if we want to standardize.
		// BranchProcessor.Execute should ideally populate its own executionLog.Inputs.
		// If not, this is a fallback.
		if executionLog.Inputs == nil {
			executionLog.Inputs = v.collectInputKeysForLog(stepID)
		}
		v.mu.Unlock()
	}
	// The returned executionLog from processor.Execute should be the one for the main VM.
	// No need to call v.addExecutionLog here if processor.Execute returns a log that should be added by the caller.
	// However, the original structure had v.addExecutionLog(executionLog) *after* the call to processor.Execute.
	// Let's assume processor.Execute returns a log that should be added by the caller.
	// The `addExecutionLog` for this branch step itself is done by `executeNode` using the returned `branchLog`.

	return executionLog, nextStep, err // Return the log, the next step, and any error
}

func (v *VM) runLoop(stepID string, nodeValue *avsproto.LoopNode) (*avsproto.Execution_Step, error) {
	// Use the new queue-based execution instead of the old recursive approach
	executionLog, err := v.executeLoopWithQueue(stepID, nodeValue)

	v.mu.Lock()
	if executionLog != nil {
		executionLog.Inputs = v.collectInputKeysForLog(stepID)
	}
	v.mu.Unlock()

	return executionLog, err // Loop node itself doesn't dictate a jump in the main plan
}

func (v *VM) runFilter(stepID string, nodeValue *avsproto.FilterNode) (*avsproto.Execution_Step, error) {
	p := NewFilterProcessor(v)
	executionLog, err := p.Execute(stepID, nodeValue)
	v.mu.Lock()
	if executionLog != nil {
		executionLog.Inputs = v.collectInputKeysForLog(stepID)
	}
	v.mu.Unlock()
	// v.addExecutionLog(executionLog)
	return executionLog, err
}

func (v *VM) runEthTransfer(stepID string, node *avsproto.ETHTransferNode) (*avsproto.Execution_Step, error) {
	var executionLog *avsproto.Execution_Step
	if v.smartWalletConfig == nil {
		err := fmt.Errorf("smart wallet config not set for ETH transfer")
		executionLog = v.createExecutionStep(stepID, false, err.Error(), "", time.Now().UnixMilli())
		executionLog.EndAt = time.Now().UnixMilli()
		return executionLog, err
	}

	// For now, we don't need an actual ETH client connection for simulation
	// In the future, this would use the actual ETH RPC client
	processor := NewETHTransferProcessor(v, nil, v.smartWalletConfig, v.TaskOwner)
	executionLog, err := processor.Execute(stepID, node)
	v.mu.Lock()
	if executionLog != nil {
		executionLog.Inputs = v.collectInputKeysForLog(stepID)
	}
	v.mu.Unlock()
	return executionLog, err
}

// convertToCamelCase converts snake_case to camelCase
// Example: "block_number" -> "blockNumber", "gas_limit" -> "gasLimit"

// resolveVariableWithFallback tries to resolve a variable path
func (v *VM) resolveVariableWithFallback(jsvm *goja.Runtime, varPath string, currentVars map[string]any) (interface{}, bool) {
	// Debug: Log the variable path we're trying to resolve
	if v.logger != nil {
		v.logger.Debug("resolveVariableWithFallback DEBUG: Attempting to resolve", "varPath", varPath)
	}

	// SECURITY: Validate variable path using centralized security validation
	validationResult := ValidateCodeInjection(varPath)
	if !validationResult.Valid {
		if v.logger != nil {
			v.logger.Warn("Dangerous variable path detected", "path", varPath, "error", validationResult.Error)
		}
		return nil, false
	}

	// Try to resolve the variable path
	script := fmt.Sprintf(`(() => { try { return %s; } catch(e) { return undefined; } })()`, varPath)
	if v.logger != nil {
		v.logger.Debug("resolveVariableWithFallback DEBUG: JavaScript script", "script", script)
	}

	if evaluated, err := jsvm.RunString(script); err == nil {
		exportedValue := evaluated.Export()
		if v.logger != nil {
			v.logger.Debug("resolveVariableWithFallback DEBUG: JavaScript evaluation result", "exportedValue", exportedValue, "type", fmt.Sprintf("%T", exportedValue))
		}

		// Check if we got a real value (not undefined)
		if exportedValue != nil && fmt.Sprintf("%v", exportedValue) != "undefined" {
			if v.logger != nil {
				v.logger.Debug("resolveVariableWithFallback DEBUG: Resolution successful", "varPath", varPath, "result", exportedValue)
			}
			return exportedValue, true
		}
	} else {
		if v.logger != nil {
			v.logger.Debug("resolveVariableWithFallback DEBUG: JavaScript evaluation failed", "varPath", varPath, "error", err)
		}
	}

	if v.logger != nil {
		v.logger.Debug("resolveVariableWithFallback DEBUG: Resolution failed", "varPath", varPath)
	}
	return nil, false
}

// preprocessTextWithVariableMapping enhances preprocessText with smart variable resolution
// It handles both snake_case and camelCase variable names, especially for node_name.data.field patterns
//
// Used by: REST API, GraphQL, Custom Code, Contract Write, Branch runners
// Returns: "undefined" for missing variables (better for JSON structure and debugging)
//
// This function solves the gRPC protobuf conversion issue where Go snake_case fields
// become JavaScript camelCase, but templates still reference the original snake_case names.
func (v *VM) preprocessTextWithVariableMapping(text string) string {
	if !strings.Contains(text, "{{") || !strings.Contains(text, "}}") {
		return text
	}
	jsvm := NewGojaVM()
	v.mu.Lock()
	currentVars := make(map[string]any, len(v.vars))
	for k, val := range v.vars {
		currentVars[k] = val
	}
	v.mu.Unlock()

	// Debug: Log all available variables
	if v.logger != nil {
		v.logger.Debug("preprocessTextWithVariableMapping DEBUG: Available variables", "vars", func() []string {
			keys := make([]string, 0, len(currentVars))
			for k := range currentVars {
				keys = append(keys, k)
			}
			return keys
		}())
	}

	for key, value := range currentVars {
		if err := jsvm.Set(key, value); err != nil {
			if v.logger != nil {
				v.logger.Error("failed to set variable in JS VM for preprocessing", "key", key, "error", err)
			}
		}
	}

	result := text
	for i := 0; i < VMMaxPreprocessIterations; i++ {
		start := strings.Index(result, "{{")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "}}")
		if end == -1 {
			break
		}
		end += start // Adjust end to be relative to the start of `result`

		expr := strings.TrimSpace(result[start+2 : end])
		if expr == "" {
			result = result[:start] + result[end+2:]
			continue
		}
		// Simple check for nested, though might not be perfect for all cases.
		if strings.Index(expr, "{{") != -1 || strings.Index(expr, "}}") != -1 {
			if v.logger != nil {
				v.logger.Warn("Nested expression detected, replacing with empty string", "expression", expr)
			}
			result = result[:start] + result[end+2:]
			continue
		}

		// Debug: Log the expression we're trying to resolve
		if v.logger != nil {
			v.logger.Debug("preprocessTextWithVariableMapping DEBUG: Trying to resolve expression", "expression", expr)
		}

		// Try to resolve the variable with fallback to camelCase
		exportedValue, resolved := v.resolveVariableWithFallback(jsvm, expr, currentVars)
		if !resolved {
			// Replace with "undefined" instead of removing the expression
			// This helps maintain valid JSON structure and makes debugging easier
			if v.logger != nil {
				v.logger.Debug("template variable evaluation failed, replacing with 'undefined'", "expression", expr)
			}
			result = result[:start] + "undefined" + result[end+2:]
			continue
		}

		var replacement string
		if t, ok := exportedValue.(time.Time); ok {
			replacement = t.In(time.UTC).Format("2006-01-02 15:04:05.000 +0000 UTC")
		} else if exportedValue == nil {
			// Handle null values by returning "undefined" for better debugging
			replacement = "undefined"
		} else if _, okMap := exportedValue.(map[string]interface{}); okMap {
			replacement = "[object Object]" // Mimic JS behavior for objects in strings
		} else if _, okArr := exportedValue.([]interface{}); okArr {
			replacement = fmt.Sprintf("%v", exportedValue) // Or could be "[object Array]" or stringified JSON
		} else {
			replacement = fmt.Sprintf("%v", exportedValue)
		}
		result = result[:start] + replacement + result[end+2:]
	}
	return result
}

// preprocessText is the original template processing function
//
// Returns: Removes failed expressions entirely (empty string replacement)
func (v *VM) preprocessText(text string) string {
	if !strings.Contains(text, "{{") || !strings.Contains(text, "}}") {
		return text
	}
	jsvm := NewGojaVM()
	v.mu.Lock()
	currentVars := make(map[string]any, len(v.vars))
	for k, val := range v.vars {
		currentVars[k] = val
	}
	v.mu.Unlock()

	for key, value := range currentVars {
		if err := jsvm.Set(key, value); err != nil {
			if v.logger != nil {
				v.logger.Error("failed to set variable in JS VM for preprocessing", "key", key, "error", err)
			}
		}
	}

	result := text
	searchPos := 0
	previousResult := ""
	consecutiveFailures := 0
	maxConsecutiveFailures := 10

	for i := 0; i < VMMaxPreprocessIterations; i++ {
		// Break if result hasn't changed (prevents unnecessary iterations)
		if result == previousResult && i > 0 {
			break
		}
		previousResult = result
		start := strings.Index(result[searchPos:], "{{")
		if start == -1 {
			break
		}
		start += searchPos // Adjust to absolute position

		end := strings.Index(result[start:], "}}")
		if end == -1 {
			break
		}
		end += start // Adjust end to be relative to the start of `result`

		expr := strings.TrimSpace(result[start+2 : end])
		if expr == "" {
			result = result[:start] + result[end+2:]
			searchPos = start
			continue
		}
		// Simple check for nested, though might not be perfect for all cases.
		if strings.Index(expr, "{{") != -1 || strings.Index(expr, "}}") != -1 {
			if v.logger != nil {
				v.logger.Warn("Nested expression detected, replacing with empty string", "expression", expr)
			}
			result = result[:start] + result[end+2:]
			searchPos = start
			continue
		}

		// Handle special date macros
		if strings.HasPrefix(expr, "date.") {
			switch expr {
			case "date.now":
				replacement := fmt.Sprintf("%d", time.Now().UnixMilli())
				result = result[:start] + replacement + result[end+2:]
				searchPos = start + len(replacement)
				continue
			case "date.now_iso":
				replacement := time.Now().UTC().Format(time.RFC3339)
				result = result[:start] + replacement + result[end+2:]
				searchPos = start + len(replacement)
				continue
			}
		}

		// Handle array access with dot notation (e.g., array.0 -> array[0])
		jsExpr := expr
		// Convert dot notation array access to bracket notation
		re := regexp.MustCompile(`\.(\d+)`)
		jsExpr = re.ReplaceAllString(jsExpr, "[$1]")

		// SECURITY: Validate template expression using centralized security validation
		validationResult := ValidateCodeInjection(jsExpr)
		if !validationResult.Valid {
			if v.logger != nil {
				v.logger.Warn("Dangerous template expression detected", "expr", jsExpr, "error", validationResult.Error)
			}
			// Replace with "undefined" for safety
			result = result[:start] + "undefined" + result[end+2:]
			continue
		}

		script := fmt.Sprintf(`(() => { return %s; })()`, jsExpr)

		// Add panic recovery and timeout protection
		var evaluated goja.Value
		var err error

		// Simple timeout using a channel and goroutine
		resultChan := make(chan struct{})

		go func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic during JavaScript evaluation: %v", r)
				}
				close(resultChan)
			}()

			// Set a more restrictive timeout for the JS evaluation
			timeoutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			// Check if context is cancelled before starting
			select {
			case <-timeoutCtx.Done():
				err = fmt.Errorf("JavaScript evaluation context timeout")
				return
			default:
				// Continue with evaluation
			}

			evaluated, err = jsvm.RunString(script)
		}()

		// Wait for result or timeout
		select {
		case <-resultChan:
			// Evaluation completed (successfully or with error)
		case <-time.After(3 * time.Second):
			err = fmt.Errorf("JavaScript evaluation timeout after 3 seconds")
			if v.logger != nil {
				v.logger.Error("preprocessText evaluation timeout", "expression", expr, "script", script)
			}
		}

		if v.logger != nil {
			v.logger.Debug("evaluating pre-processor script", "task_id", v.GetTaskId(), "script", script, "result", evaluated, "error", err)
		}
		if err != nil {
			consecutiveFailures++
			if consecutiveFailures >= maxConsecutiveFailures {
				if v.logger != nil {
					v.logger.Error("too many consecutive preprocessing failures, stopping", "failures", consecutiveFailures, "expression", expr)
				}
				break
			}

			// Keep the expression unchanged (original behavior)
			// This is the expected behavior for existing tests
			if v.logger != nil {
				v.logger.Debug("template variable evaluation failed, keeping expression unchanged", "expression", expr, "error", err)
			}
			// Skip this expression and continue looking for the next one
			searchPos = end + 2
			continue
		}

		// Reset failure counter on success
		consecutiveFailures = 0

		exportedValue := evaluated.Export()
		var replacement string
		if t, ok := exportedValue.(time.Time); ok {
			replacement = t.In(time.UTC).Format("2006-01-02 15:04:05.000 +0000 UTC")
		} else if exportedValue == nil {
			// Handle null values by returning "undefined" for better debugging
			replacement = "undefined"
		} else if _, okMap := exportedValue.(map[string]interface{}); okMap {
			replacement = "[object Object]" // Mimic JS behavior for objects in strings
		} else if _, okArr := exportedValue.([]interface{}); okArr {
			replacement = fmt.Sprintf("%v", exportedValue) // Or could be "[object Array]" or stringified JSON
		} else {
			replacement = fmt.Sprintf("%v", exportedValue)
		}
		result = result[:start] + replacement + result[end+2:]
		searchPos = start + len(replacement)
	}
	return result
}

// validateTemplateFormat checks for malformed template syntax and returns an error if found
func (v *VM) validateTemplateFormat(text string) error {
	// Check for malformed template syntax like { { variable } }
	// Use a more specific regex that looks for template-like patterns, not JSON
	// Match: { + whitespace + variable-like-content + whitespace + }
	// But exclude JSON patterns by ensuring the content looks like a variable reference
	malformedTemplateRegex := regexp.MustCompile(`\{\s+([a-zA-Z_$][a-zA-Z0-9_$]*(?:\.[a-zA-Z_$][a-zA-Z0-9_$]*)*)\s+\}`)
	matches := malformedTemplateRegex.FindAllStringSubmatch(text, -1)

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		content := strings.TrimSpace(match[1])

		// Double-check if the content looks like a variable reference
		if v.looksLikeVariableReference(content) {
			return fmt.Errorf("malformed template syntax detected: '{ { %s } }'. Use '{{%s}}' instead", content, content)
		}
	}

	return nil
}

// looksLikeVariableReference determines if a string looks like it's trying to reference a variable
func (v *VM) looksLikeVariableReference(content string) bool {
	// Common patterns that indicate variable references:

	// 1. Contains dots (property access like trigger.data.block_number)
	if strings.Contains(content, ".") {
		return true
	}

	// 2. Starts with common variable names
	commonVarPrefixes := []string{
		"trigger",
		"workflowContext",
		"apContext",
		"taskContext",
		"env",
		"secrets",
	}

	for _, prefix := range commonVarPrefixes {
		if strings.HasPrefix(content, prefix) {
			return true
		}
	}

	// 3. Follows JavaScript variable naming pattern (letters, numbers, underscore, $)
	// and doesn't look like regular text (no spaces, not just a single word)
	jsVarPattern := regexp.MustCompile(`^[a-zA-Z_$][a-zA-Z0-9_$]*(\.[a-zA-Z_$][a-zA-Z0-9_$]*)*$`)
	if jsVarPattern.MatchString(content) && len(content) > 3 {
		return true
	}

	return false
}

func (v *VM) collectInputKeysForLog(excludeStepID string) []string {
	// This function assumes v.mu is already locked by the caller
	inputKeys := make([]string, 0, len(v.vars)*2) // Allocate space for both .data and .input

	// Get the variable name for the current node to exclude it
	var excludeVarName string
	if excludeStepID != "" {
		excludeVarName = v.getNodeNameAsVarLocked(excludeStepID)
	}

	// Debug logging to understand what's happening
	if v.logger != nil {
		v.logger.Debug("collectInputKeysForLog processing",
			"excludeStepID", excludeStepID,
			"excludeVarName", excludeVarName,
			"totalVars", len(v.vars))
	}

	for k, value := range v.vars {
		if !contains(macros.MacroFuncs, k) { // `contains` is a global helper
			// Skip the current node's own variables from its inputsList
			if excludeVarName != "" && k == excludeVarName {
				continue
			}

			// Skip system variables that shouldn't appear as inputs
			if k == APContextVarName {
				inputKeys = append(inputKeys, APContextConfigVarsPath)
			} else if k == WorkflowContextVarName {
				inputKeys = append(inputKeys, WorkflowContextVarName) // Use as-is, no .data suffix
			} else if k == "triggerConfig" {
				// Skip triggerConfig system variable - it shouldn't appear in inputsList
				continue
			} else {
				// For regular variables, check if they have data and/or input fields
				if valueMap, ok := value.(map[string]any); ok {
					// Check for .data field
					if _, hasData := valueMap["data"]; hasData {
						dataKey := fmt.Sprintf("%s.%s", k, DataSuffix)
						inputKeys = append(inputKeys, dataKey)
					}
					// Check for .input field
					if _, hasInput := valueMap["input"]; hasInput {
						inputKey := fmt.Sprintf("%s.input", k)
						inputKeys = append(inputKeys, inputKey)
					}
					// Check for .headers field (for ManualTrigger template access)
					if _, hasHeaders := valueMap["headers"]; hasHeaders {
						headersKey := fmt.Sprintf("%s.headers", k)
						inputKeys = append(inputKeys, headersKey)
					}
					// Check for .pathParams field (for ManualTrigger template access)
					if _, hasPathParams := valueMap["pathParams"]; hasPathParams {
						pathParamsKey := fmt.Sprintf("%s.pathParams", k)
						inputKeys = append(inputKeys, pathParamsKey)
					}

					// Special case: ManualTrigger variables may have flattened data (no .data field)
					// If the variable is a map but has no .data or .input fields, it might be a ManualTrigger variable
					// In this case, add the variable name directly to inputKeys
					if _, hasData := valueMap["data"]; !hasData {
						if _, hasInput := valueMap["input"]; !hasInput && len(valueMap) > 0 {
							// This looks like a ManualTrigger variable with flattened data
							inputKeys = append(inputKeys, k)
						}
					}
				} else {
					// For non-map variables (simple scalars like input variables), use the variable name as-is
					// This fixes the issue where input variables like "userToken" were incorrectly becoming "userToken.data"
					inputKeys = append(inputKeys, k)
				}
			}
		}
	}

	if v.logger != nil {
		v.logger.Debug("Final inputKeys", "keys", inputKeys, "count", len(inputKeys))
	}

	return inputKeys
}

func (v *VM) CollectInputs() map[string]string {
	v.mu.Lock()
	defer v.mu.Unlock()
	inputs := make(map[string]string)
	for key, value := range v.vars {
		if contains(macros.MacroFuncs, key) {
			continue
		}
		valueStr := ""
		if value != nil {
			valueStr = fmt.Sprintf("%v", value)
		}
		varname := key
		if varname == APContextVarName {
			varname = APContextConfigVarsPath
		} else if varname == WorkflowContextVarName {
			varname = WorkflowContextVarName // Use as-is, no .data suffix
		} else {
			// Check if this is a map variable with .data field
			if valueMap, ok := value.(map[string]any); ok {
				if _, hasData := valueMap["data"]; hasData {
					// Only add .data suffix for variables that actually have a data field
					varname = fmt.Sprintf("%s.%s", varname, DataSuffix)
				}
				// For map variables without .data field, use as-is
			}
			// For non-map variables (simple scalars like input variables), use as-is
		}
		inputs[varname] = valueStr
	}
	return inputs
}

func (v *VM) GetTaskId() string {
	// No lock needed if v.task is immutable after VM creation for this ID.
	if v.task != nil && v.task.Task != nil { // Check if task.Task itself is not nil
		return v.task.Id
	}
	return ""
}

func (v *VM) RunNodeWithInputs(node *avsproto.TaskNode, inputVariables map[string]interface{}) (*avsproto.Execution_Step, error) {
	// Special handling for blockTrigger - require real blockchain data
	if node.GetCustomCode() != nil && node.Name == "Single Node Execution: "+NodeTypeBlockTrigger {
		// Get node configuration for error step
		var nodeConfig *structpb.Value
		nodeConfigMap := ExtractNodeConfiguration(node)
		if nodeConfigMap != nil {
			if configProto, err := structpb.NewValue(nodeConfigMap); err == nil {
				nodeConfig = configProto
			}
		}

		return &avsproto.Execution_Step{
			Id:      node.Id, // Use new 'id' field
			Success: false,
			Error:   "BlockTrigger nodes require real blockchain data - mock data not supported",
			StartAt: time.Now().UnixMilli(),
			EndAt:   time.Now().UnixMilli(),
			Config:  nodeConfig, // Include node configuration data for debugging
		}, fmt.Errorf("BlockTrigger nodes require real blockchain data - mock data not supported")
	}

	// Validate node name for JavaScript compatibility
	if err := model.ValidateNodeNameForJavaScript(node.Name); err != nil {
		// Get node configuration for error step
		var nodeConfig *structpb.Value
		nodeConfigMap := ExtractNodeConfiguration(node)
		if nodeConfigMap != nil {
			if configProto, err := structpb.NewValue(nodeConfigMap); err == nil {
				nodeConfig = configProto
			}
		}

		return &avsproto.Execution_Step{
			Id:      node.Id,
			Success: false,
			Error:   fmt.Sprintf("Node name validation failed: %v", err),
			StartAt: time.Now().UnixMilli(),
			EndAt:   time.Now().UnixMilli(),
			Config:  nodeConfig, // Include node configuration data for debugging
		}, fmt.Errorf("node name validation failed: %w", err)
	}

	// Create a temporary, clean VM for isolated node execution.
	// It inherits logger, secrets, and global configs but has its own state (vars, plans, etc.)
	tempVM := NewVM() // NewVM initializes maps and mutex
	tempVM.logger = v.logger
	tempVM.smartWalletConfig = v.smartWalletConfig
	tempVM.db = v.db
	tempVM.secrets = v.secrets // Inherit secrets
	tempVM.TaskID = v.TaskID   // Inherit original TaskID for logging context

	tempVM.mu.Lock()
	tempVM.TaskNodes[node.Id] = node // Add the single node to its map

	// Copy apContext if it exists in the original VM's vars (might contain global config)
	v.mu.Lock() // Lock original VM to read its vars
	if apContextValue, ok := v.vars[APContextVarName]; ok {
		if tempVM.vars == nil { // Ensure tempVM.vars is initialized
			tempVM.vars = make(map[string]any)
		}
		tempVM.vars[APContextVarName] = apContextValue
	}
	v.mu.Unlock()

	// Add provided input variables to the temporary VM
	if tempVM.vars == nil { // Ensure tempVM.vars is initialized
		tempVM.vars = make(map[string]any)
	}
	for key, value := range inputVariables {
		tempVM.vars[key] = value // Directly add, AddVar would also work but this is fine for init
	}

	// Since it's a single node, entrypoint is this node, no complex compilation needed for plan.
	tempVM.entrypoint = node.Id
	tempVM.plans[node.Id] = &Step{NodeID: node.Id, Next: []string{}} // Simple plan for one node
	tempVM.Status = VMStateReady                                     // Mark as ready to run
	tempVM.mu.Unlock()                                               // tempVM lock released

	// Execute the single node. VM.Run handles status changes.
	// We are calling tempVM.executeNode directly for more control and to get the step log.

	_, err := tempVM.executeNode(node) // This will append to tempVM.ExecutionLogs

	if err != nil {
		// If executeNode itself errored, the log might already be there with failure.
		// If not, ensure a failed log entry.
		if len(tempVM.ExecutionLogs) == 0 {
			// No log was created, create a failed step
			failedStep := tempVM.createExecutionStep(node.Id, false, err.Error(), "", time.Now().UnixMilli())
			failedStep.EndAt = time.Now().UnixMilli()
			tempVM.addExecutionLog(failedStep) // addExecutionLog handles locking
		} else {
			// If a log exists, update it with error if not already set (though executeNode's sub-calls should do this)
			tempVM.mu.Lock()
			lastLog := tempVM.ExecutionLogs[len(tempVM.ExecutionLogs)-1]
			if lastLog.Id == node.Id && lastLog.Success { // If marked success but error occurred here
				lastLog.Success = false
				lastLog.Error = err.Error()
			}
			tempVM.mu.Unlock()
		}
		return tempVM.ExecutionLogs[0], err // Return the log and the error
	}

	tempVM.mu.Lock() // Lock to safely access ExecutionLogs
	defer tempVM.mu.Unlock()
	if len(tempVM.ExecutionLogs) > 0 {
		// Assuming the relevant log is the last one added for this node.
		// If executeNode calls multiple sub-nodes (not typical for single node context), this might need refinement.
		return tempVM.ExecutionLogs[len(tempVM.ExecutionLogs)-1], nil
	}

	// Fallback if no logs were generated but no error (should ideally not happen if executeNode is robust)
	return nil, fmt.Errorf("node execution produced no logs for node ID: %s", node.Id)
}

func CreateNodeFromType(nodeType string, config map[string]interface{}, nodeID string) (*avsproto.TaskNode, error) {
	if nodeID == "" {
		nodeID = "node_" + ulid.Make().String()
	}
	node := &avsproto.TaskNode{Id: nodeID, Name: "singleNodeExecution_" + nodeType}

	switch nodeType {
	case NodeTypeRestAPI, "restAPI": // Support both "restApi" and "restAPI" for backward compatibility
		node.Type = avsproto.NodeType_NODE_TYPE_REST_API
		// Create REST API node with proper configuration
		restConfig := &avsproto.RestAPINode_Config{}
		if url, ok := config["url"].(string); ok {
			restConfig.Url = url
		}
		if method, ok := config["method"].(string); ok {
			restConfig.Method = method
		}
		if body, ok := config["body"].(string); ok {
			restConfig.Body = body
		}

		// Handle headers - can be map[string]string or [][]string (headersMap format)
		if headers, ok := config["headers"].(map[string]string); ok {
			restConfig.Headers = headers
		} else if headersMap, ok := config["headersMap"].([][]string); ok {
			headers := make(map[string]string)
			for _, header := range headersMap {
				if len(header) == 2 {
					headers[header[0]] = header[1]
				}
			}
			restConfig.Headers = headers
		} else if headersAny, ok := config["headersMap"].([]interface{}); ok {
			headers := make(map[string]string)
			for _, headerAny := range headersAny {
				if headerSlice, ok := headerAny.([]interface{}); ok && len(headerSlice) == 2 {
					if key, ok := headerSlice[0].(string); ok {
						if value, ok := headerSlice[1].(string); ok {
							headers[key] = value
						}
					}
				}
			}
			restConfig.Headers = headers
		}

		node.TaskType = &avsproto.TaskNode_RestApi{
			RestApi: &avsproto.RestAPINode{
				Config: restConfig,
			},
		}
	case NodeTypeContractRead:
		node.Type = avsproto.NodeType_NODE_TYPE_CONTRACT_READ
		// Create contract read node with proper configuration
		contractConfig := &avsproto.ContractReadNode_Config{}

		// Use camelCase only for consistency with JavaScript SDK
		if address, ok := config["contractAddress"].(string); ok {
			contractConfig.ContractAddress = address
		} else {
			return nil, fmt.Errorf("contract read node requires 'contractAddress' field")
		}

		if contractAbiArray, ok := config["contractAbi"].([]interface{}); ok {
			// Convert array to JSON string for protobuf storage
			if abiBytes, err := json.Marshal(contractAbiArray); err == nil {
				contractConfig.ContractAbi = string(abiBytes)
			} else {
				return nil, fmt.Errorf("failed to convert contractAbi array to JSON: %v", err)
			}
		} else {
			return nil, fmt.Errorf("contract read node requires 'contractAbi' field as array")
		}

		// Handle method calls - use camelCase only for consistency
		if callData, ok := config["callData"].(string); ok {
			// Single method call
			methodCall := &avsproto.ContractReadNode_MethodCall{
				CallData:   callData,
				MethodName: "", // Will be determined from ABI
			}
			contractConfig.MethodCalls = []*avsproto.ContractReadNode_MethodCall{methodCall}
		} else if methodCalls, ok := config["methodCalls"].([]interface{}); ok {
			// Multiple method calls (camelCase only)
			for _, methodCallInterface := range methodCalls {
				if methodCallMap, ok := methodCallInterface.(map[string]interface{}); ok {
					methodCall := &avsproto.ContractReadNode_MethodCall{}
					if callData, ok := methodCallMap["callData"].(string); ok {
						methodCall.CallData = callData
					}
					if methodName, ok := methodCallMap["methodName"].(string); ok {
						methodCall.MethodName = methodName
					}
					// Handle applyToFields for decimal formatting
					if applyToFields, ok := methodCallMap["applyToFields"].([]interface{}); ok {
						for _, field := range applyToFields {
							if fieldStr, ok := field.(string); ok {
								methodCall.ApplyToFields = append(methodCall.ApplyToFields, fieldStr)
							}
						}
					}
					contractConfig.MethodCalls = append(contractConfig.MethodCalls, methodCall)
				}
			}
		}

		node.TaskType = &avsproto.TaskNode_ContractRead{
			ContractRead: &avsproto.ContractReadNode{
				Config: contractConfig,
			},
		}
	case NodeTypeContractWrite:
		node.Type = avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE
		// Create contract write node with proper configuration
		contractConfig := &avsproto.ContractWriteNode_Config{}

		// Use camelCase only for consistency with JavaScript SDK
		if address, ok := config["contractAddress"].(string); ok {
			contractConfig.ContractAddress = address
		} else {
			return nil, fmt.Errorf("contract write node requires 'contractAddress' field")
		}

		if abi, ok := config["contractAbi"].(string); ok {
			contractConfig.ContractAbi = abi
		} else {
			return nil, fmt.Errorf("contract write node requires 'contractAbi' field")
		}

		// Use camelCase only for consistency
		if callData, ok := config["callData"].(string); ok {
			contractConfig.CallData = callData
		}

		// Handle method calls array - use camelCase only
		if methodCalls, ok := config["methodCalls"].([]interface{}); ok {
			// Multiple method calls (camelCase only)
			for _, methodCallInterface := range methodCalls {
				if methodCallMap, ok := methodCallInterface.(map[string]interface{}); ok {
					methodCall := &avsproto.ContractWriteNode_MethodCall{}
					if callData, ok := methodCallMap["callData"].(string); ok {
						methodCall.CallData = callData
					}
					if methodName, ok := methodCallMap["methodName"].(string); ok {
						methodCall.MethodName = methodName
					}
					contractConfig.MethodCalls = append(contractConfig.MethodCalls, methodCall)
				}
			}
		}

		node.TaskType = &avsproto.TaskNode_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode{
				Config: contractConfig,
			},
		}
	case NodeTypeCustomCode:
		node.Type = avsproto.NodeType_NODE_TYPE_CUSTOM_CODE
		// Create custom code node with proper configuration
		customConfig := &avsproto.CustomCodeNode_Config{}
		if source, ok := config["source"].(string); ok {
			customConfig.Source = source
		}
		if lang, ok := config["lang"].(string); ok {
			switch strings.ToLower(lang) {
			case "javascript", "js":
				customConfig.Lang = avsproto.Lang_JavaScript
			default:
				customConfig.Lang = avsproto.Lang_JavaScript // Default to JavaScript
			}
		} else {
			customConfig.Lang = avsproto.Lang_JavaScript // Default to JavaScript
		}

		node.TaskType = &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{
				Config: customConfig,
			},
		}
	case NodeTypeBranch:
		node.Type = avsproto.NodeType_NODE_TYPE_BRANCH
		// Create branch node with proper configuration
		branchConfig := &avsproto.BranchNode_Config{}

		// Handle conditions from client
		conditionsValue, exists := config["conditions"]
		if !exists {
			return nil, fmt.Errorf("branch node requires conditions configuration - no conditions field found")
		}

		conditionsData, ok := conditionsValue.([]interface{})
		if !ok {
			// Try to convert from []map[string]interface{} to []interface{}
			if conditionsSlice, ok := conditionsValue.([]map[string]interface{}); ok {
				conditionsData = make([]interface{}, len(conditionsSlice))
				for i, condition := range conditionsSlice {
					conditionsData[i] = condition
				}
			} else {
				return nil, fmt.Errorf("branch node requires conditions configuration - invalid type: %T", conditionsValue)
			}
		}

		if len(conditionsData) == 0 {
			return nil, fmt.Errorf("branch node requires conditions configuration - empty conditions array")
		}

		conditions := make([]*avsproto.BranchNode_Condition, len(conditionsData))
		for i, conditionInterface := range conditionsData {
			if conditionMap, ok := conditionInterface.(map[string]interface{}); ok {
				condition := &avsproto.BranchNode_Condition{}
				if id, ok := conditionMap["id"].(string); ok {
					condition.Id = id
				}
				if condType, ok := conditionMap["type"].(string); ok {
					condition.Type = condType
				}
				if expression, ok := conditionMap["expression"].(string); ok {
					condition.Expression = expression
				}
				conditions[i] = condition
			}
		}
		branchConfig.Conditions = conditions

		node.TaskType = &avsproto.TaskNode_Branch{
			Branch: &avsproto.BranchNode{
				Config: branchConfig,
			},
		}
	case NodeTypeFilter:
		node.Type = avsproto.NodeType_NODE_TYPE_FILTER
		// Create filter node with proper configuration
		filterConfig := &avsproto.FilterNode_Config{}
		if expression, ok := config["expression"].(string); ok {
			filterConfig.Expression = expression
		}
		// Use camelCase only for consistency with JavaScript SDK
		if inputNodeName, ok := config["inputNodeName"].(string); ok {
			filterConfig.InputNodeName = inputNodeName
		}

		node.TaskType = &avsproto.TaskNode_Filter{
			Filter: &avsproto.FilterNode{
				Config: filterConfig,
			},
		}
	case NodeTypeBlockTrigger:
		node.Type = avsproto.NodeType_NODE_TYPE_CUSTOM_CODE // BlockTrigger is handled as custom code
		// Create a custom code node that will be handled specially by RunNodeWithInputs
		node.TaskType = &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{},
		}
	case NodeTypeETHTransfer:
		node.Type = avsproto.NodeType_NODE_TYPE_ETH_TRANSFER
		// Create ETH transfer node with proper configuration
		ethConfig := &avsproto.ETHTransferNode_Config{}
		if destination, ok := config["destination"].(string); ok {
			ethConfig.Destination = destination
		}
		if amount, ok := config["amount"].(string); ok {
			ethConfig.Amount = amount
		}

		node.TaskType = &avsproto.TaskNode_EthTransfer{
			EthTransfer: &avsproto.ETHTransferNode{
				Config: ethConfig,
			},
		}
	case NodeTypeLoop:
		node.Type = avsproto.NodeType_NODE_TYPE_LOOP
		// Create loop node with proper configuration
		loopConfig := &avsproto.LoopNode_Config{}

		// Extract required configuration fields (camelCase only)
		if inputNodeName, ok := config["inputNodeName"].(string); ok {
			loopConfig.InputNodeName = inputNodeName
		} else {
			return nil, fmt.Errorf("loop node requires 'inputNodeName' field")
		}

		if iterVal, ok := config["iterVal"].(string); ok {
			loopConfig.IterVal = iterVal
		} else {
			return nil, fmt.Errorf("loop node requires 'iterVal' field")
		}

		if iterKey, ok := config["iterKey"].(string); ok {
			loopConfig.IterKey = iterKey
		} else {
			return nil, fmt.Errorf("loop node requires 'iterKey' field")
		}

		// Handle executionMode parameter (camelCase only)
		if executionMode, ok := config["executionMode"].(string); ok {
			switch strings.ToLower(executionMode) {
			case "sequential":
				loopConfig.ExecutionMode = avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL
			case "parallel":
				loopConfig.ExecutionMode = avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL
			default:
				loopConfig.ExecutionMode = avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL // Default to sequential for safety
			}
		} else {
			// Default to sequential if not specified (safer default)
			loopConfig.ExecutionMode = avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL
		}

		// Handle the nested runner configuration (CustomCode, RestAPI, etc.)
		loopNode := &avsproto.LoopNode{
			Config: loopConfig,
		}

		// Extract runner configuration (required)
		runner, ok := config["runner"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("loop node requires 'runner' field with nested runner configuration")
		}

		runnerType, hasType := runner["type"].(string)
		if !hasType {
			return nil, fmt.Errorf("loop node runner requires 'type' field")
		}

		runnerConfig, hasConfig := runner["config"].(map[string]interface{})
		if !hasConfig {
			return nil, fmt.Errorf("loop node runner requires 'config' field")
		}

		switch runnerType {
		case "customCode":
			ccConfig := &avsproto.CustomCodeNode_Config{}
			if source, ok := runnerConfig["source"].(string); ok {
				ccConfig.Source = source
			}
			if lang, ok := runnerConfig["lang"].(string); ok {
				switch strings.ToLower(lang) {
				case "javascript", "js":
					ccConfig.Lang = avsproto.Lang_JavaScript
				default:
					ccConfig.Lang = avsproto.Lang_JavaScript
				}
			} else {
				ccConfig.Lang = avsproto.Lang_JavaScript
			}
			loopNode.Runner = &avsproto.LoopNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{Config: ccConfig},
			}
		case "restApi":
			rConfig := &avsproto.RestAPINode_Config{}
			if url, ok := runnerConfig["url"].(string); ok {
				rConfig.Url = url
			}
			if method, ok := runnerConfig["method"].(string); ok {
				rConfig.Method = method
			}
			if body, ok := runnerConfig["body"].(string); ok {
				rConfig.Body = body
			}

			// Handle headers
			if headersMap, ok := runnerConfig["headersMap"].([][]string); ok {
				headers := make(map[string]string)
				for _, header := range headersMap {
					if len(header) == 2 {
						headers[header[0]] = header[1]
					}
				}
				rConfig.Headers = headers
			} else if headersAny, ok := runnerConfig["headersMap"].([]interface{}); ok {
				headers := make(map[string]string)
				for _, headerAny := range headersAny {
					if headerSlice, ok := headerAny.([]interface{}); ok && len(headerSlice) == 2 {
						if key, ok := headerSlice[0].(string); ok {
							if value, ok := headerSlice[1].(string); ok {
								headers[key] = value
							}
						}
					}
				}
				rConfig.Headers = headers
			}

			loopNode.Runner = &avsproto.LoopNode_RestApi{
				RestApi: &avsproto.RestAPINode{Config: rConfig},
			}
		case "contractRead":
			crConfig := &avsproto.ContractReadNode_Config{}

			// Extract contract configuration
			if contractAddress, ok := runnerConfig["contractAddress"].(string); ok {
				crConfig.ContractAddress = contractAddress
			}

			// Handle contractAbi - accept only array format
			if contractAbiArray, ok := runnerConfig["contractAbi"].([]interface{}); ok {
				// Convert array to JSON string for protobuf storage
				if abiBytes, err := json.Marshal(contractAbiArray); err == nil {
					crConfig.ContractAbi = string(abiBytes)
				} else {
					return nil, fmt.Errorf("failed to convert contractAbi array to JSON: %v", err)
				}
			} else {
				return nil, fmt.Errorf("loop node contractRead runner requires 'contractAbi' field as array")
			}

			// Handle method calls
			if methodCalls, ok := runnerConfig["methodCalls"].([]interface{}); ok {
				for _, methodCallInterface := range methodCalls {
					if methodCallMap, ok := methodCallInterface.(map[string]interface{}); ok {
						methodCall := &avsproto.ContractReadNode_MethodCall{}
						if callData, ok := methodCallMap["callData"].(string); ok {
							methodCall.CallData = callData
						}
						if methodName, ok := methodCallMap["methodName"].(string); ok {
							methodCall.MethodName = methodName
						}
						// Handle applyToFields for decimal formatting
						if applyToFields, ok := methodCallMap["applyToFields"].([]interface{}); ok {
							for _, field := range applyToFields {
								if fieldStr, ok := field.(string); ok {
									methodCall.ApplyToFields = append(methodCall.ApplyToFields, fieldStr)
								}
							}
						}
						crConfig.MethodCalls = append(crConfig.MethodCalls, methodCall)
					}
				}
			}

			loopNode.Runner = &avsproto.LoopNode_ContractRead{
				ContractRead: &avsproto.ContractReadNode{Config: crConfig},
			}
		case "contractWrite":
			cwConfig := &avsproto.ContractWriteNode_Config{}

			// Extract contract configuration
			if contractAddress, ok := runnerConfig["contractAddress"].(string); ok {
				cwConfig.ContractAddress = contractAddress
			}
			if contractAbi, ok := runnerConfig["contractAbi"].(string); ok {
				cwConfig.ContractAbi = contractAbi
			}
			if callData, ok := runnerConfig["callData"].(string); ok {
				cwConfig.CallData = callData
			}

			// Handle method calls array
			if methodCalls, ok := runnerConfig["methodCalls"].([]interface{}); ok {
				for _, methodCallInterface := range methodCalls {
					if methodCallMap, ok := methodCallInterface.(map[string]interface{}); ok {
						methodCall := &avsproto.ContractWriteNode_MethodCall{}
						if callData, ok := methodCallMap["callData"].(string); ok {
							methodCall.CallData = callData
						}
						if methodName, ok := methodCallMap["methodName"].(string); ok {
							methodCall.MethodName = methodName
						}
						cwConfig.MethodCalls = append(cwConfig.MethodCalls, methodCall)
					}
				}
			}

			loopNode.Runner = &avsproto.LoopNode_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode{Config: cwConfig},
			}
		default:
			return nil, fmt.Errorf("unsupported loop runner type: %s", runnerType)
		}

		// Validate that a runner is provided
		if loopNode.Runner == nil {
			return nil, fmt.Errorf("loop node must have a valid runner configuration")
		}

		node.TaskType = &avsproto.TaskNode_Loop{
			Loop: loopNode,
		}
	case "trigger": // Support trigger as a generic node type for testing purposes
		// Support trigger as a generic node type - all trigger types use custom code for testing
		node.Type = avsproto.NodeType_NODE_TYPE_CUSTOM_CODE
		// Create a minimal config for trigger simulation
		customConfig := &avsproto.CustomCodeNode_Config{
			Source: "// Trigger simulation node",
			Lang:   avsproto.Lang_JavaScript,
		}
		node.TaskType = &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{
				Config: customConfig,
			},
		}
	default:
		return nil, fmt.Errorf("unsupported node type for CreateNodeFromType: %s", nodeType)
	}
	return node, nil
}

// contains is a helper function (not a VM method)
func contains(slice []string, str string) bool {
	for _, item := range slice {
		if item == str {
			return true
		}
	}
	return false
}

// Helper function to create execution step with node information
func (v *VM) createExecutionStep(nodeId string, success bool, errorMsg string, logMsg string, startTime int64) *avsproto.Execution_Step {
	v.mu.Lock()
	defer v.mu.Unlock()

	// Look up the node to get its type, name, and configuration data
	var nodeType string = "UNSPECIFIED"
	var nodeName string = "unknown"
	var nodeConfig *structpb.Value

	if node, exists := v.TaskNodes[nodeId]; exists {
		nodeType = node.Type.String()
		nodeName = node.Name

		// Extract node configuration instead of the removed input field
		nodeConfigMap := ExtractNodeConfiguration(node)
		if nodeConfigMap != nil {
			if configProto, err := structpb.NewValue(nodeConfigMap); err == nil {
				nodeConfig = configProto
			}
		}
	}

	step := &avsproto.Execution_Step{
		Id:      nodeId, // Use new 'id' field
		Success: success,
		Error:   errorMsg,
		Log:     logMsg,
		StartAt: startTime,
		EndAt:   startTime,  // Will be updated by caller if needed
		Type:    nodeType,   // Use new 'type' field as string
		Name:    nodeName,   // Use new 'name' field
		Config:  nodeConfig, // Include node configuration data for debugging
	}

	return step
}

// AnalyzeExecutionResult examines all execution steps and determines overall success/failure
// Returns (success, errorMessage, failedStepCount)
func (v *VM) AnalyzeExecutionResult() (bool, string, int) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if len(v.ExecutionLogs) == 0 {
		return false, "no execution steps found", 0
	}

	var failedStepNames []string
	var firstErrorMessage string

	for _, step := range v.ExecutionLogs {
		if !step.Success && step.Error != "" {
			if firstErrorMessage == "" {
				firstErrorMessage = step.Error
			}
			// Collect the step name (prefer name over ID)
			stepName := step.Name
			if stepName == "" || stepName == "unknown" {
				stepName = step.Id
			}
			failedStepNames = append(failedStepNames, stepName)
		}
	}

	failedCount := len(failedStepNames)
	if failedCount == 0 {
		return true, "", 0
	}

	// Build error message with failed step count and failed node names
	var errorMessage string
	failedNodesList := strings.Join(failedStepNames, ", ")

	if failedCount == 1 {
		errorMessage = fmt.Sprintf("This %d step encountered error: %s", failedCount, failedNodesList)
	} else {
		errorMessage = fmt.Sprintf("These %d steps encountered error: %s", failedCount, failedNodesList)
	}

	return false, errorMessage, failedCount
}

// ExtractTriggerConfigData extracts configuration data from a TaskTrigger protobuf message
// This function now extracts from the Config field since the duplicate input field was removed
func ExtractTriggerConfigData(trigger *avsproto.TaskTrigger) map[string]interface{} {
	if trigger == nil {
		return nil
	}

	// Check each trigger type and extract config from the correct nested object
	switch trigger.GetTriggerType().(type) {
	case *avsproto.TaskTrigger_Block:
		blockTrigger := trigger.GetBlock()
		if blockTrigger != nil && blockTrigger.Config != nil {
			// Use the same approach as TaskTriggerToConfig for consistency
			configMap, err := gow.ProtoToMap(blockTrigger.Config)
			if err == nil {
				return configMap
			}
		}
	case *avsproto.TaskTrigger_Cron:
		cronTrigger := trigger.GetCron()
		if cronTrigger != nil && cronTrigger.Config != nil {
			// Use the same approach as TaskTriggerToConfig for consistency
			configMap, err := gow.ProtoToMap(cronTrigger.Config)
			if err == nil {
				return configMap
			}
		}
	case *avsproto.TaskTrigger_Event:
		eventTrigger := trigger.GetEvent()
		if eventTrigger != nil && eventTrigger.Config != nil {
			// Use the same approach as TaskTriggerToConfig for consistency
			configMap, err := gow.ProtoToMap(eventTrigger.Config)
			if err == nil {
				return configMap
			}
		}
	case *avsproto.TaskTrigger_FixedTime:
		fixedTimeTrigger := trigger.GetFixedTime()
		if fixedTimeTrigger != nil && fixedTimeTrigger.Config != nil {
			// Use the same approach as TaskTriggerToConfig for consistency
			configMap, err := gow.ProtoToMap(fixedTimeTrigger.Config)
			if err == nil {
				return configMap
			}
		}
	case *avsproto.TaskTrigger_Manual:
		manualTrigger := trigger.GetManual()
		if manualTrigger != nil && manualTrigger.Config != nil {
			// Use the same approach as TaskTriggerToConfig for consistency
			configMap, err := gow.ProtoToMap(manualTrigger.Config)
			if err == nil {
				return configMap
			}
		}
		return nil
	}
	return nil
}

// convertProtobufValueToMap converts a google.protobuf.Value to a map[string]interface{}
func convertProtobufValueToMap(value *structpb.Value) map[string]interface{} {
	if value == nil {
		return map[string]interface{}{}
	}

	// Use the built-in AsInterface() method to convert to Go native types
	interfaceValue := value.AsInterface()

	// Try to convert to map[string]interface{}
	if mapValue, ok := interfaceValue.(map[string]interface{}); ok {
		return mapValue
	}

	// If it's not a map, return empty map
	return map[string]interface{}{}
}

// validateAllNodeNamesForJavaScript validates all node names in a task
func validateAllNodeNamesForJavaScript(task *model.Task) error {
	if task == nil {
		return nil
	}

	// Validate trigger name
	if task.Trigger != nil && task.Trigger.Name != "" {
		if err := model.ValidateNodeNameForJavaScript(task.Trigger.Name); err != nil {
			return fmt.Errorf("trigger name validation failed: %w", err)
		}
	}

	// Validate all node names
	for _, node := range task.Nodes {
		if err := model.ValidateNodeNameForJavaScript(node.Name); err != nil {
			return fmt.Errorf("node '%s' validation failed: %w", node.Id, err)
		}
	}

	return nil
}

// ExtractNodeConfiguration extracts configuration from a TaskNode based on its type
// This function returns the configuration that was used to execute the node
func ExtractNodeConfiguration(taskNode *avsproto.TaskNode) map[string]interface{} {
	if taskNode == nil {
		return nil
	}

	// Note: This function is used in isolated execution contexts where logger might not be available

	switch taskNode.GetTaskType().(type) {
	case *avsproto.TaskNode_RestApi:
		restApi := taskNode.GetRestApi()
		if restApi != nil && restApi.Config != nil {
			config := map[string]interface{}{
				"url":    restApi.Config.Url,
				"method": restApi.Config.Method,
				"body":   restApi.Config.Body,
			}

			// Handle headers map format - convert to array format like in LoopNode
			if restApi.Config.Headers != nil && len(restApi.Config.Headers) > 0 {
				headersList := make([]interface{}, 0, len(restApi.Config.Headers))
				for key, value := range restApi.Config.Headers {
					headersList = append(headersList, []interface{}{key, value})
				}
				config["headersMap"] = headersList
			}

			// Clean up complex protobuf types before returning
			return removeComplexProtobufTypes(config)
		}

	case *avsproto.TaskNode_Loop:
		loop := taskNode.GetLoop()
		if loop != nil {
			if loop.Config != nil {
				config := map[string]interface{}{
					"inputNodeName": loop.Config.InputNodeName,
					"iterVal":       loop.Config.IterVal,
					"iterKey":       loop.Config.IterKey,
				}

				// Add execution mode (always include it)
				config["executionMode"] = loop.Config.ExecutionMode.String()

				// Extract runner information from the oneof field
				runnerConfig := extractLoopRunnerConfig(loop)
				if runnerConfig != nil {
					config["runner"] = runnerConfig
				}

				// Clean up complex protobuf types before returning
				cleanConfig := removeComplexProtobufTypes(config)
				return cleanConfig
			}
		}

	case *avsproto.TaskNode_CustomCode:
		customCode := taskNode.GetCustomCode()
		if customCode != nil && customCode.Config != nil {
			config := map[string]interface{}{
				"lang":   customCode.Config.Lang.String(),
				"source": customCode.Config.Source,
			}

			// Clean up complex protobuf types before returning
			return removeComplexProtobufTypes(config)
		}

	case *avsproto.TaskNode_GraphqlQuery:
		graphqlQuery := taskNode.GetGraphqlQuery()
		if graphqlQuery != nil && graphqlQuery.Config != nil {
			config := map[string]interface{}{
				"url":   graphqlQuery.Config.Url,
				"query": graphqlQuery.Config.Query,
			}

			// Handle variables map format
			if len(graphqlQuery.Config.Variables) > 0 {
				variablesMap := make(map[string]interface{})
				for key, value := range graphqlQuery.Config.Variables {
					variablesMap[key] = value
				}
				config["variables"] = variablesMap
			}

			// Clean up complex protobuf types before returning
			return removeComplexProtobufTypes(config)
		}

	case *avsproto.TaskNode_ContractRead:
		contractRead := taskNode.GetContractRead()
		if contractRead != nil && contractRead.Config != nil {
			config := map[string]interface{}{
				"contractAddress": contractRead.Config.ContractAddress,
				"contractAbi":     contractRead.Config.ContractAbi,
			}

			// Handle method calls - extract fields to simple map for protobuf compatibility
			if len(contractRead.Config.MethodCalls) > 0 {
				methodCallsArray := make([]interface{}, len(contractRead.Config.MethodCalls))
				for i, methodCall := range contractRead.Config.MethodCalls {
					// Extract fields from protobuf struct into simple map
					methodCallMap := map[string]interface{}{
						"methodName": methodCall.MethodName,
						"callData":   methodCall.CallData,
					}

					// Convert applyToFields []string to []interface{} for protobuf compatibility
					if len(methodCall.ApplyToFields) > 0 {
						applyToFieldsArray := make([]interface{}, len(methodCall.ApplyToFields))
						for j, field := range methodCall.ApplyToFields {
							applyToFieldsArray[j] = field
						}
						methodCallMap["applyToFields"] = applyToFieldsArray
					}

					methodCallsArray[i] = methodCallMap
				}
				config["methodCalls"] = methodCallsArray
			}

			// Clean up complex protobuf types before returning
			return removeComplexProtobufTypes(config)
		}

	case *avsproto.TaskNode_ContractWrite:
		contractWrite := taskNode.GetContractWrite()
		if contractWrite != nil && contractWrite.Config != nil {
			config := map[string]interface{}{
				"contractAddress": contractWrite.Config.ContractAddress,
				"contractAbi":     contractWrite.Config.ContractAbi,
				"callData":        contractWrite.Config.CallData,
			}

			// Handle method calls - extract fields to simple map for protobuf compatibility
			if len(contractWrite.Config.MethodCalls) > 0 {
				methodCallsArray := make([]interface{}, len(contractWrite.Config.MethodCalls))
				for i, methodCall := range contractWrite.Config.MethodCalls {
					// Extract fields from protobuf struct into simple map
					methodCallMap := map[string]interface{}{
						"methodName": methodCall.MethodName,
						"callData":   methodCall.CallData,
					}
					methodCallsArray[i] = methodCallMap
				}
				config["methodCalls"] = methodCallsArray
			}

			// Clean up complex protobuf types before returning
			return removeComplexProtobufTypes(config)
		}

	case *avsproto.TaskNode_Filter:
		filter := taskNode.GetFilter()
		if filter != nil && filter.Config != nil {
			config := map[string]interface{}{
				"expression":    filter.Config.Expression,
				"inputNodeName": filter.Config.InputNodeName,
			}

			// Clean up complex protobuf types before returning
			return removeComplexProtobufTypes(config)
		}
	}

	return nil
}

// GetNodeDataForExecution retrieves node name and configuration data for a given stepID
// This helper function extracts the repetitive locking pattern used across all vm_runner files
// to get node information from TaskNodes map.
//
// Returns:
// - nodeName: The name of the node (defaults to "unknown" if not found)
// - nodeConfig: The configuration data of the node (can be nil)
func (v *VM) GetNodeDataForExecution(stepID string) (nodeName string, nodeConfig *structpb.Value) {
	nodeName = "unknown" // default value

	v.mu.Lock()
	defer v.mu.Unlock()

	if v.logger != nil {
		v.logger.Debug("GetNodeDataForExecution: Starting", "stepID", stepID, "taskNodesCount", len(v.TaskNodes))
	}

	if taskNode, exists := v.TaskNodes[stepID]; exists {
		nodeName = taskNode.Name

		if v.logger != nil {
		}

		// Extract node configuration instead of input data
		nodeConfigMap := ExtractNodeConfiguration(taskNode)

		if v.logger != nil {
			if nodeConfigMap != nil {
			}
		}

		if nodeConfigMap != nil {
			// Convert config map to protobuf Value
			// First convert for frontend user-friendly format
			frontendConfig := convertConfigForFrontend(nodeConfigMap)
			// Then remove complex protobuf types that can't be converted
			cleanedConfig := removeComplexProtobufTypes(frontendConfig)
			// Convert any map[string]string to map[string]interface{} for protobuf compatibility
			protobufCompatibleConfig := convertMapStringStringToInterface(cleanedConfig)
			if configProto, err := structpb.NewValue(protobufCompatibleConfig); err == nil {
				nodeConfig = configProto
				if v.logger != nil {
				}
			} else {
				if v.logger != nil {
					v.logger.Error("🔍 GetNodeDataForExecution: Failed to convert config to protobuf", "stepID", stepID, "error", err)
				}
			}
		} else {
			if v.logger != nil {
				v.logger.Warn("🔍 GetNodeDataForExecution: Node config map is nil", "stepID", stepID)
			}
		}
	} else {
		if v.logger != nil {
			v.logger.Warn("🔍 GetNodeDataForExecution: Node not found", "stepID", stepID)
		}
	}

	if v.logger != nil {
	}

	return nodeName, nodeConfig
}

// Helper function to extract runner configuration from LoopNode oneof runner field
func extractLoopRunnerConfig(loop *avsproto.LoopNode) map[string]interface{} {
	if loop == nil {
		return nil
	}

	switch runner := loop.GetRunner().(type) {
	case *avsproto.LoopNode_CustomCode:
		return map[string]interface{}{
			"type": "customCode",
			"config": map[string]interface{}{
				"source": runner.CustomCode.Config.Source,
				"lang":   runner.CustomCode.Config.Lang.String(),
			},
		}
	case *avsproto.LoopNode_RestApi:
		config := map[string]interface{}{
			"type": "restApi",
			"config": map[string]interface{}{
				"url":    runner.RestApi.Config.Url,
				"method": runner.RestApi.Config.Method,
				"body":   runner.RestApi.Config.Body,
			},
		}
		// Handle headers if present
		if runner.RestApi.Config.Headers != nil && len(runner.RestApi.Config.Headers) > 0 {
			config["config"].(map[string]interface{})["headers"] = runner.RestApi.Config.Headers
		}
		return config
	case *avsproto.LoopNode_ContractRead:
		configData := map[string]interface{}{
			"contractAddress": runner.ContractRead.Config.ContractAddress,
			"contractAbi":     runner.ContractRead.Config.ContractAbi,
		}
		// Handle method calls if present
		if len(runner.ContractRead.Config.MethodCalls) > 0 {
			methodCallsArray := make([]interface{}, len(runner.ContractRead.Config.MethodCalls))
			for i, methodCall := range runner.ContractRead.Config.MethodCalls {
				methodCallMap := map[string]interface{}{
					"callData":   methodCall.CallData,
					"methodName": methodCall.MethodName,
				}
				// Include applyToFields if present
				if len(methodCall.ApplyToFields) > 0 {
					applyToFieldsArray := make([]interface{}, len(methodCall.ApplyToFields))
					for j, field := range methodCall.ApplyToFields {
						applyToFieldsArray[j] = field
					}
					methodCallMap["applyToFields"] = applyToFieldsArray
				}
				methodCallsArray[i] = methodCallMap
			}
			configData["methodCalls"] = methodCallsArray
		}
		return map[string]interface{}{
			"type":   "contractRead",
			"config": configData,
		}
	case *avsproto.LoopNode_ContractWrite:
		configData := map[string]interface{}{
			"contractAddress": runner.ContractWrite.Config.ContractAddress,
			"contractAbi":     runner.ContractWrite.Config.ContractAbi,
			"callData":        runner.ContractWrite.Config.CallData,
		}
		// Handle method calls if present
		if len(runner.ContractWrite.Config.MethodCalls) > 0 {
			methodCallsArray := make([]interface{}, len(runner.ContractWrite.Config.MethodCalls))
			for i, methodCall := range runner.ContractWrite.Config.MethodCalls {
				methodCallMap := map[string]interface{}{
					"callData":   methodCall.CallData,
					"methodName": methodCall.MethodName,
				}
				methodCallsArray[i] = methodCallMap
			}
			configData["methodCalls"] = methodCallsArray
		}
		return map[string]interface{}{
			"type":   "contractWrite",
			"config": configData,
		}
	case *avsproto.LoopNode_EthTransfer:
		return map[string]interface{}{
			"type": "ethTransfer",
			"config": map[string]interface{}{
				"destination": runner.EthTransfer.Config.Destination,
				"amount":      runner.EthTransfer.Config.Amount,
			},
		}
	case *avsproto.LoopNode_GraphqlDataQuery:
		configData := map[string]interface{}{
			"url":   runner.GraphqlDataQuery.Config.Url,
			"query": runner.GraphqlDataQuery.Config.Query,
		}
		// Handle variables if present
		if runner.GraphqlDataQuery.Config.Variables != nil && len(runner.GraphqlDataQuery.Config.Variables) > 0 {
			variablesMap := make(map[string]interface{})
			for key, value := range runner.GraphqlDataQuery.Config.Variables {
				variablesMap[key] = value
			}
			configData["variables"] = variablesMap
		}
		return map[string]interface{}{
			"type":   "graphqlDataQuery",
			"config": configData,
		}
	}

	return nil
}

// Helper function to convert map[string]string to map[string]interface{} for protobuf compatibility
func convertMapStringStringToInterface(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}

	result := make(map[string]interface{})
	for key, value := range input {
		switch v := value.(type) {
		case map[string]string:
			// Convert map[string]string to map[string]interface{}
			interfaceMap := make(map[string]interface{})
			for k, val := range v {
				interfaceMap[k] = val
			}
			result[key] = interfaceMap
		case map[string]interface{}:
			// Recursively convert nested maps
			result[key] = convertMapStringStringToInterface(v)
		default:
			// Keep other types as-is since methodCalls should already be in correct format after our fixes
			result[key] = value
		}
	}
	return result
}

// removeComplexProtobufTypes removes complex protobuf types that can't be converted to structpb.Value
func removeComplexProtobufTypes(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}

	result := make(map[string]interface{})
	for key, value := range input {
		if key == "methodCalls" {
			// methodCalls should always be []interface{} with maps after our fixes
			if methodCallsArray, ok := value.([]interface{}); ok {
				result[key] = methodCallsArray
			} else {
				// Fallback: convert unexpected format to string (should not happen with our fixes)
				result[key] = fmt.Sprintf("%v", value)
			}
		} else if stringMap, ok := value.(map[string]string); ok {
			// Convert map[string]string to map[string]interface{} for protobuf compatibility
			interfaceMap := make(map[string]interface{})
			for k, v := range stringMap {
				interfaceMap[k] = v
			}
			result[key] = interfaceMap
		} else if nestedMap, ok := value.(map[string]interface{}); ok {
			// Recursively clean nested maps
			result[key] = removeComplexProtobufTypes(nestedMap)
		} else {
			result[key] = value
		}
	}
	return result
}

// convertConfigForFrontend converts internal configuration to user-friendly format for frontend
func convertConfigForFrontend(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}

	result := make(map[string]interface{})
	for key, value := range input {
		switch key {
		case "executionMode":
			// Convert execution mode to user-friendly format
			if executionModeStr, ok := value.(string); ok {
				if executionModeStr == "EXECUTION_MODE_SEQUENTIAL" {
					result[key] = "sequential"
				} else if executionModeStr == "EXECUTION_MODE_PARALLEL" {
					result[key] = "parallel"
				} else {
					result[key] = executionModeStr
				}
			} else {
				result[key] = value
			}
		case "headersMap":
			// Convert headersMap array format back to headers object format for frontend
			if headersArray, ok := value.([]interface{}); ok {
				headersObj := make(map[string]string)
				for _, header := range headersArray {
					if headerPair, ok := header.([]interface{}); ok && len(headerPair) == 2 {
						if keyStr, ok := headerPair[0].(string); ok {
							if valueStr, ok := headerPair[1].(string); ok {
								headersObj[keyStr] = valueStr
							}
						}
					}
				}
				result["headers"] = headersObj
			} else {
				result[key] = value
			}
		case "runner":
			// Handle runner configuration recursively
			if runnerMap, ok := value.(map[string]interface{}); ok {
				result[key] = convertConfigForFrontend(runnerMap)
			} else {
				result[key] = value
			}
		default:
			// For other fields, recursively convert if it's a map
			if nestedMap, ok := value.(map[string]interface{}); ok {
				result[key] = convertConfigForFrontend(nestedMap)
			} else {
				result[key] = value
			}
		}
	}
	return result
}

// executeStepInMainLoop executes a single step in the main execution loop context
// This method is used when a trigger fans out to multiple target nodes that should be executed sequentially
func (v *VM) executeStepInMainLoop(step *Step) {
	if step == nil {
		if v.logger != nil {
			v.logger.Warn("🔄 executeStepInMainLoop: step is nil")
		}
		return
	}

	if v.logger != nil {
		v.logger.Info("🔄 executeStepInMainLoop: Starting execution", "nodeID", step.NodeID)
	}

	var node *avsproto.TaskNode
	var ok bool

	v.mu.Lock() // Lock for accessing TaskNodes
	node, ok = v.TaskNodes[step.NodeID]
	v.mu.Unlock()

	if !ok {
		// Handle conceptual steps like branch conditions - skip if not a real node
		if v.logger != nil {
			v.logger.Debug("executeStepInMainLoop: skipping non-TaskNode step", "nodeID", step.NodeID)
		}
		return
	}

	if v.logger != nil {
		v.logger.Info("🔄 executeStepInMainLoop: Found TaskNode, executing",
			"nodeID", step.NodeID,
			"nodeType", node.Type.String(),
			"nodeName", node.Name)
	}

	// Execute the single node
	_, err := v.executeNode(node)
	if err != nil {
		// Log error but continue with other steps
		if v.logger != nil {
			errorMsg := err.Error()
			stackTraceRegex := regexp.MustCompile(`(?m)^\s*at .*$`)
			errorMsg = stackTraceRegex.ReplaceAllString(errorMsg, "")
			errorMsg = strings.TrimSpace(errorMsg)

			if isExpectedValidationError(err) {
				v.logger.Warn("main loop step execution failed", "nodeID", node.Id, "error", errorMsg)
			} else {
				v.logger.Error("main loop step execution failed", "nodeID", node.Id, "error", errorMsg)
			}
		}

		// Create a failed execution step and add it to the logs
		failedStep := v.createExecutionStep(node.Id, false, err.Error(), fmt.Sprintf("Node execution failed: %s", err.Error()), time.Now().UnixMilli())
		failedStep.EndAt = time.Now().UnixMilli()
		v.addExecutionLog(failedStep)

	} else {
		if v.logger != nil {
			v.logger.Info("🔄 executeStepInMainLoop: Successfully executed node", "nodeID", step.NodeID)
		}
	}
}

// Start begins processing tasks with worker goroutines
func (eq *ExecutionQueue) Start() {
	for i := 0; i < eq.workers; i++ {
		go eq.worker()
	}
}

// Stop closes the task channel to stop accepting new tasks
func (eq *ExecutionQueue) Stop() {
	close(eq.tasks)
}

// Submit adds a task to the execution queue
func (eq *ExecutionQueue) Submit(task *ExecutionTask) error {
	if task.Depth > MaxExecutionDepth {
		return fmt.Errorf("maximum execution depth (%d) exceeded", MaxExecutionDepth)
	}

	select {
	case eq.tasks <- task:
		return nil
	default:
		return fmt.Errorf("execution queue is full")
	}
}

// worker processes tasks from the queue
func (eq *ExecutionQueue) worker() {
	for task := range eq.tasks {
		result := eq.executeTask(task)
		if task.ResultChannel != nil {
			select {
			case task.ResultChannel <- result:
			default:
				// Channel might be closed, log but don't block
				if eq.vm.logger != nil {
					eq.vm.logger.Warn("Failed to send execution result - channel closed", "stepID", task.StepID)
				}
			}
		}
	}
}

// executeTask executes a single task without creating a new VM
func (eq *ExecutionQueue) executeTask(task *ExecutionTask) *ExecutionResult {
	if task.Node == nil {
		return &ExecutionResult{
			Error:  fmt.Errorf("task node is nil"),
			StepID: task.StepID,
		}
	}

	// Set input variables in the VM context for this execution
	eq.vm.mu.Lock()
	if eq.vm.vars == nil {
		eq.vm.vars = make(map[string]any)
	}

	// Store original variables to restore later
	originalVars := make(map[string]any)
	for key, value := range task.InputVariables {
		if originalValue, exists := eq.vm.vars[key]; exists {
			originalVars[key] = originalValue
		}
		eq.vm.vars[key] = value
	}
	eq.vm.mu.Unlock()

	// Execute the node directly without creating a new VM
	step, err := eq.vm.executeNodeDirect(task.Node, task.StepID)

	// Restore original variables
	eq.vm.mu.Lock()
	for key := range task.InputVariables {
		if originalValue, exists := originalVars[key]; exists {
			eq.vm.vars[key] = originalValue
		} else {
			delete(eq.vm.vars, key)
		}
	}
	eq.vm.mu.Unlock()

	// Extract result data for loops
	var resultData interface{}
	if step != nil && step.Success {
		resultData = eq.extractResultData(step)
	}

	return &ExecutionResult{
		Step:   step,
		Error:  err,
		StepID: task.StepID,
		Data:   resultData,
	}
}

// extractResultData extracts the actual result data from an execution step
func (eq *ExecutionQueue) extractResultData(step *avsproto.Execution_Step) interface{} {
	if customCodeOutput := step.GetCustomCode(); customCodeOutput != nil && customCodeOutput.Data != nil {
		return customCodeOutput.Data.AsInterface()
	}
	if restApiOutput := step.GetRestApi(); restApiOutput != nil && restApiOutput.Data != nil {
		return restApiOutput.Data.AsInterface()
	}
	if contractReadOutput := step.GetContractRead(); contractReadOutput != nil && contractReadOutput.Data != nil {
		return contractReadOutput.Data.AsInterface()
	}
	if contractWriteOutput := step.GetContractWrite(); contractWriteOutput != nil && contractWriteOutput.Data != nil {
		return contractWriteOutput.Data.AsInterface()
	}
	if ethTransferOutput := step.GetEthTransfer(); ethTransferOutput != nil && ethTransferOutput.Data != nil {
		return ethTransferOutput.Data.AsInterface()
	}
	return nil
}

// executeNodeDirect executes a node directly without creating a new VM or updating execution logs
// This is used by the execution queue to avoid recursive VM creation
func (v *VM) executeNodeDirect(node *avsproto.TaskNode, stepID string) (*avsproto.Execution_Step, error) {
	if node == nil {
		return nil, fmt.Errorf("executeNodeDirect called with nil node")
	}

	// Extract and set input data for this node (making it available as node_name.input)
	inputData := ExtractNodeConfiguration(node)
	if inputData != nil {
		processor := &CommonProcessor{vm: v}
		processor.SetInputVarForStep(stepID, inputData)
	}

	var executionLogForNode *avsproto.Execution_Step
	var err error

	// Execute the appropriate node type
	if nodeValue := node.GetRestApi(); nodeValue != nil {
		executionLogForNode, err = v.runRestApi(stepID, nodeValue)
	} else if nodeValue := node.GetBranch(); nodeValue != nil {
		var nextStep *Step
		executionLogForNode, nextStep, err = v.runBranch(stepID, nodeValue)
		// Note: We ignore nextStep in direct execution as we don't follow jumps
		_ = nextStep
	} else if nodeValue := node.GetGraphqlQuery(); nodeValue != nil {
		executionLogForNode, err = v.runGraphQL(stepID, nodeValue)
	} else if nodeValue := node.GetCustomCode(); nodeValue != nil {
		executionLogForNode, err = v.runCustomCode(stepID, nodeValue)
	} else if nodeValue := node.GetContractRead(); nodeValue != nil {
		executionLogForNode, err = v.runContractRead(stepID, nodeValue)
	} else if nodeValue := node.GetContractWrite(); nodeValue != nil {
		executionLogForNode, err = v.runContractWrite(stepID, nodeValue)
	} else if nodeValue := node.GetFilter(); nodeValue != nil {
		executionLogForNode, err = v.runFilter(stepID, nodeValue)
	} else if nodeValue := node.GetEthTransfer(); nodeValue != nil {
		executionLogForNode, err = v.runEthTransfer(stepID, nodeValue)
	} else if nodeValue := node.GetLoop(); nodeValue != nil {
		// For loop nodes, we need special handling to use the execution queue
		return v.executeLoopWithQueue(stepID, nodeValue)
	} else {
		err = fmt.Errorf("unknown node type for node ID %s", stepID)
	}

	return executionLogForNode, err
}

// executeLoopWithQueue executes a loop node using the execution queue instead of recursive VM calls
func (v *VM) executeLoopWithQueue(stepID string, node *avsproto.LoopNode) (*avsproto.Execution_Step, error) {
	// Use shared function to create execution step
	s := createNodeExecutionStep(stepID, avsproto.NodeType_NODE_TYPE_LOOP, v)

	var log strings.Builder
	log.WriteString(fmt.Sprintf("Start loop execution at %s", time.Now()))

	// Get configuration from node.Config
	if node.Config == nil {
		err := fmt.Errorf("LoopNode Config is nil")
		log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		finalizeExecutionStep(s, false, err.Error(), log.String())
		return s, err
	}

	inputNodeName := node.Config.InputNodeName
	iterVal := node.Config.IterVal
	iterKey := node.Config.IterKey
	executionMode := node.Config.ExecutionMode

	// Resolve input variable
	var inputVarName string
	var inputVar interface{}
	var exists bool

	inputVarName = v.GetNodeNameAsVar(inputNodeName)
	v.mu.Lock()
	inputVar, exists = v.vars[inputVarName]
	v.mu.Unlock()

	if !exists {
		inputVarName = inputNodeName
		v.mu.Lock()
		inputVar, exists = v.vars[inputVarName]
		v.mu.Unlock()
	}

	if !exists {
		err := fmt.Errorf("input variable %s not found", inputVarName)
		log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		finalizeExecutionStep(s, false, err.Error(), log.String())
		return s, err
	}

	inputArray, ok := inputVar.([]interface{})
	if !ok {
		// Try to extract from data field if wrapped (common for trigger variables)
		if dataMap, ok := inputVar.(map[string]interface{}); ok {
			log.WriteString(fmt.Sprintf("\nInput variable is a map with keys: %v", getMapKeys(dataMap)))

			if dataValue, hasData := dataMap["data"]; hasData {
				log.WriteString(fmt.Sprintf("\nFound 'data' field of type: %T", dataValue))

				// Try different array types that might be present
				if dataArray, ok := dataValue.([]interface{}); ok {
					inputArray = dataArray
					log.WriteString(fmt.Sprintf("\nExtracted array from 'data' field: %d items", len(inputArray)))
				} else if dataSlice, ok := dataValue.([]any); ok {
					// Handle []any type
					inputArray = make([]interface{}, len(dataSlice))
					for i, v := range dataSlice {
						inputArray[i] = v
					}
					log.WriteString(fmt.Sprintf("\nExtracted []any array from 'data' field: %d items", len(inputArray)))
				} else {
					// Data field exists but is not an array
					err := fmt.Errorf("input variable %s.data is type %T, expected array", inputVarName, dataValue)
					log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
					finalizeExecutionStep(s, false, err.Error(), log.String())
					return s, err
				}
			} else {
				// No data field found
				err := fmt.Errorf("input variable %s is not an array and has no 'data' field (available keys: %v)", inputVarName, getMapKeys(dataMap))
				log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
				finalizeExecutionStep(s, false, err.Error(), log.String())
				return s, err
			}
		} else {
			err := fmt.Errorf("input variable %s is type %T, expected array or object with 'data' field", inputVarName, inputVar)
			log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
			finalizeExecutionStep(s, false, err.Error(), log.String())
			return s, err
		}
	}

	// Determine execution mode
	concurrent := false
	isContractWrite := node.GetContractWrite() != nil
	var executionModeLog string

	if isContractWrite {
		concurrent = false
		executionModeLog = "sequentially due to contract write operation (security requirement)"
	} else {
		switch executionMode {
		case avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL:
			concurrent = true
			executionModeLog = "parallel mode"
		case avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL:
			concurrent = false
			executionModeLog = "sequential mode"
		default:
			concurrent = false
			executionModeLog = "sequential mode"
		}
	}

	// Create execution queue for this loop
	workers := 1
	if concurrent {
		workers = len(inputArray)
		if workers > 10 { // Limit max workers to prevent resource exhaustion
			workers = 10
		}
	}

	eq := NewExecutionQueue(v, workers)
	eq.Start()
	defer eq.Stop()

	results := make([]interface{}, len(inputArray))
	success := true
	var firstError error

	if concurrent {
		log.WriteString(fmt.Sprintf("\nExecuting loop iterations in %s with %d workers", executionModeLog, workers))
		// Parallel execution using the queue
		resultChannels := make([]chan *ExecutionResult, len(inputArray))

		for i, item := range inputArray {
			iterInputs := make(map[string]interface{})
			iterInputs[iterVal] = item
			if iterKey != "" {
				iterInputs[iterKey] = i
			}

			iterationStepID := fmt.Sprintf("%s_iter_%d", stepID, i)
			nestedNode := v.createNestedNodeFromLoop(node, iterationStepID, iterInputs)

			resultChannel := make(chan *ExecutionResult, 1)
			resultChannels[i] = resultChannel

			task := &ExecutionTask{
				Node:           nestedNode,
				InputVariables: iterInputs,
				StepID:         iterationStepID,
				Depth:          1, // Loop iterations are depth 1
				ResultChannel:  resultChannel,
				IterationIndex: i,
			}

			if err := eq.Submit(task); err != nil {
				log.WriteString(fmt.Sprintf("\nError submitting iteration %d: %s", i, err.Error()))
				success = false
				if firstError == nil {
					firstError = err
				}
			}
		}

		// Collect results
		for i, resultChannel := range resultChannels {
			select {
			case result := <-resultChannel:
				if result.Error != nil {
					success = false
					if firstError == nil {
						firstError = result.Error
					}
					log.WriteString(fmt.Sprintf("\nError in iteration %d: %s", i, result.Error.Error()))
				} else {
					results[i] = result.Data
				}
				close(resultChannel)
			case <-time.After(30 * time.Second): // Timeout for each iteration
				success = false
				err := fmt.Errorf("iteration %d timed out", i)
				if firstError == nil {
					firstError = err
				}
				log.WriteString(fmt.Sprintf("\nTimeout in iteration %d", i))
				close(resultChannel)
			}
		}
	} else {
		log.WriteString(fmt.Sprintf("\nExecuting loop iterations %s", executionModeLog))
		// Sequential execution using the queue
		for i, item := range inputArray {
			iterInputs := make(map[string]interface{})
			iterInputs[iterVal] = item
			if iterKey != "" {
				iterInputs[iterKey] = i
			}

			iterationStepID := fmt.Sprintf("%s_iter_%d", stepID, i)
			nestedNode := v.createNestedNodeFromLoop(node, iterationStepID, iterInputs)

			resultChannel := make(chan *ExecutionResult, 1)
			task := &ExecutionTask{
				Node:           nestedNode,
				InputVariables: iterInputs,
				StepID:         iterationStepID,
				Depth:          1,
				ResultChannel:  resultChannel,
				IterationIndex: i,
			}

			if err := eq.Submit(task); err != nil {
				success = false
				if firstError == nil {
					firstError = err
				}
				log.WriteString(fmt.Sprintf("\nError submitting iteration %d: %s", i, err.Error()))
				continue
			}

			// Wait for result
			select {
			case result := <-resultChannel:
				if result.Error != nil {
					success = false
					if firstError == nil {
						firstError = result.Error
					}
					log.WriteString(fmt.Sprintf("\nError in iteration %d: %s", i, result.Error.Error()))
				} else {
					results[i] = result.Data
				}
				close(resultChannel)
			case <-time.After(30 * time.Second):
				success = false
				err := fmt.Errorf("iteration %d timed out", i)
				if firstError == nil {
					firstError = err
				}
				log.WriteString(fmt.Sprintf("\nTimeout in iteration %d", i))
				close(resultChannel)
			}
		}
	}

	// Set output variable for this step
	processor := &CommonProcessor{vm: v}
	setNodeOutputData(processor, stepID, results)

	// Convert results to JSON-compatible format
	jsonSerializableResults := make([]interface{}, len(results))
	for i, result := range results {
		if result != nil {
			jsonSerializableResults[i] = convertToJSONCompatible(result)
		} else {
			jsonSerializableResults[i] = nil
		}
	}

	// Convert to protobuf Value for output
	outputValue, err := structpb.NewValue(jsonSerializableResults)
	if err != nil {
		outputValue, _ = structpb.NewValue([]interface{}{})
	}

	loopOutput := &avsproto.LoopNode_Output{
		Data: outputValue,
	}

	s.OutputData = &avsproto.Execution_Step_Loop{
		Loop: loopOutput,
	}

	if !success && firstError != nil {
		finalizeExecutionStep(s, false, firstError.Error(), log.String())
		return s, firstError
	}

	finalizeExecutionStep(s, true, "", log.String())
	return s, nil
}

// createNestedNodeFromLoop creates a nested node for loop iteration
func (v *VM) createNestedNodeFromLoop(loopNodeDef *avsproto.LoopNode, iterationStepID string, iterInputs map[string]interface{}) *avsproto.TaskNode {
	nodeName := fmt.Sprintf("loop_iteration_%s", iterationStepID)

	if ethTransfer := loopNodeDef.GetEthTransfer(); ethTransfer != nil {
		return &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_ETH_TRANSFER,
			TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: ethTransfer},
		}
	} else if contractWrite := loopNodeDef.GetContractWrite(); contractWrite != nil {
		// Apply template variable substitution
		processedContractWrite := v.processContractWriteTemplates(contractWrite, iterInputs)
		return &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
			TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: processedContractWrite},
		}
	} else if contractRead := loopNodeDef.GetContractRead(); contractRead != nil {
		processedContractRead := v.processContractReadTemplates(contractRead, iterInputs)
		return &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_CONTRACT_READ,
			TaskType: &avsproto.TaskNode_ContractRead{ContractRead: processedContractRead},
		}
	} else if graphqlQuery := loopNodeDef.GetGraphqlDataQuery(); graphqlQuery != nil {
		return &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_GRAPHQL_QUERY,
			TaskType: &avsproto.TaskNode_GraphqlQuery{GraphqlQuery: graphqlQuery},
		}
	} else if restApi := loopNodeDef.GetRestApi(); restApi != nil {
		processedRestApi := v.processRestApiTemplates(restApi, iterInputs)
		return &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_REST_API,
			TaskType: &avsproto.TaskNode_RestApi{RestApi: processedRestApi},
		}
	} else if customCode := loopNodeDef.GetCustomCode(); customCode != nil {
		return &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{CustomCode: customCode},
		}
	}

	return nil
}

// Helper methods for template processing (these need to be moved from LoopProcessor)
func (v *VM) processContractWriteTemplates(contractWrite *avsproto.ContractWriteNode, iterInputs map[string]interface{}) *avsproto.ContractWriteNode {
	// Create a copy of the contract write configuration
	processed := &avsproto.ContractWriteNode{
		Config: &avsproto.ContractWriteNode_Config{
			ContractAddress: v.substituteTemplateVariables(contractWrite.Config.ContractAddress, iterInputs),
			ContractAbi:     v.substituteTemplateVariables(contractWrite.Config.ContractAbi, iterInputs),
			CallData:        v.substituteTemplateVariables(contractWrite.Config.CallData, iterInputs),
		},
	}

	// Process method calls
	for _, methodCall := range contractWrite.Config.MethodCalls {
		processedMethodCall := &avsproto.ContractWriteNode_MethodCall{
			CallData:   v.substituteTemplateVariables(methodCall.CallData, iterInputs),
			MethodName: v.substituteTemplateVariables(methodCall.MethodName, iterInputs),
		}

		processed.Config.MethodCalls = append(processed.Config.MethodCalls, processedMethodCall)
	}

	return processed
}

func (v *VM) processContractReadTemplates(contractRead *avsproto.ContractReadNode, iterInputs map[string]interface{}) *avsproto.ContractReadNode {
	// Create a copy of the contract read configuration
	processed := &avsproto.ContractReadNode{
		Config: &avsproto.ContractReadNode_Config{
			ContractAddress: v.substituteTemplateVariables(contractRead.Config.ContractAddress, iterInputs),
			ContractAbi:     v.substituteTemplateVariables(contractRead.Config.ContractAbi, iterInputs),
		},
	}

	// Process method calls
	for _, methodCall := range contractRead.Config.MethodCalls {
		processedMethodCall := &avsproto.ContractReadNode_MethodCall{
			CallData:      v.substituteTemplateVariables(methodCall.CallData, iterInputs),
			MethodName:    v.substituteTemplateVariables(methodCall.MethodName, iterInputs),
			ApplyToFields: make([]string, len(methodCall.ApplyToFields)),
		}

		// Copy applyToFields (no template substitution needed for field names)
		copy(processedMethodCall.ApplyToFields, methodCall.ApplyToFields)

		processed.Config.MethodCalls = append(processed.Config.MethodCalls, processedMethodCall)
	}

	return processed
}

func (v *VM) processRestApiTemplates(restApi *avsproto.RestAPINode, iterInputs map[string]interface{}) *avsproto.RestAPINode {
	// Create a copy of the REST API configuration
	processed := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url:    v.substituteTemplateVariables(restApi.Config.Url, iterInputs),
			Method: v.substituteTemplateVariables(restApi.Config.Method, iterInputs),
			Body:   v.substituteTemplateVariables(restApi.Config.Body, iterInputs),
		},
	}

	// Process headers
	if restApi.Config.Headers != nil {
		processed.Config.Headers = make(map[string]string)
		for key, value := range restApi.Config.Headers {
			processedKey := v.substituteTemplateVariables(key, iterInputs)
			processedValue := v.substituteTemplateVariables(value, iterInputs)
			processed.Config.Headers[processedKey] = processedValue
		}
	}

	return processed
}

// substituteTemplateVariables replaces template variables like {{value}} and {{index}} with actual values
func (v *VM) substituteTemplateVariables(text string, iterInputs map[string]interface{}) string {
	if text == "" {
		return text
	}

	// Simple template variable substitution
	// Replace {{value}} with the current iteration value
	// Replace {{index}} with the current iteration index
	result := text

	for varName, varValue := range iterInputs {
		placeholder := fmt.Sprintf("{{%s}}", varName)
		replacement := fmt.Sprintf("%v", varValue)
		result = strings.ReplaceAll(result, placeholder, replacement)
	}

	return result
}

// getStatusText returns the standard HTTP status text for a given status code
func getStatusText(statusCode int) string {
	switch statusCode {
	case 200:
		return "OK"
	case 201:
		return "Created"
	case 202:
		return "Accepted"
	case 204:
		return "No Content"
	case 400:
		return "Bad Request"
	case 401:
		return "Unauthorized"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 405:
		return "Method Not Allowed"
	case 409:
		return "Conflict"
	case 422:
		return "Unprocessable Entity"
	case 429:
		return "Too Many Requests"
	case 500:
		return "Internal Server Error"
	case 502:
		return "Bad Gateway"
	case 503:
		return "Service Unavailable"
	case 504:
		return "Gateway Timeout"
	default:
		if statusCode >= 200 && statusCode < 300 {
			return "Success"
		} else if statusCode >= 300 && statusCode < 400 {
			return "Redirection"
		} else if statusCode >= 400 && statusCode < 500 {
			return "Client Error"
		} else if statusCode >= 500 && statusCode < 600 {
			return "Server Error"
		} else {
			return "Unknown"
		}
	}
}
