package taskengine

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dop251/goja"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

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
)

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

	// Apply dual-access mapping to node output data if it's a map
	// This enables both camelCase and snake_case field access for node outputs
	// Example: apiNode.data.responseData AND apiNode.data.response_data both work
	var processedData any = data
	if dataMap, ok := data.(map[string]interface{}); ok {
		processedData = CreateDualAccessMap(dataMap)
	}

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

	// Apply dual-access mapping to input data if it's a map
	var processedInput any = inputData
	if inputMap, ok := inputData.(map[string]interface{}); ok {
		processedInput = CreateDualAccessMap(inputMap)
	}

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

func NewVMWithDataAndTransferLog(task *model.Task, triggerData *TriggerData, smartWalletConfig *config.SmartWalletConfig, secrets map[string]string, transferLog *avsproto.EventTrigger_TransferLogOutput) (*VM, error) {
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
			"status":             task.Status.String(),
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
		if err == nil { // Proceed if trigger name is valid
			var triggerDataMap map[string]interface{}

			// If we have transfer log data, use it to populate rich trigger data and create proper Event structure
			if transferLog != nil {
				// Create EventTrigger_Output with oneof TransferLog
				v.parsedTriggerData.Event = &avsproto.EventTrigger_Output{
					OutputType: &avsproto.EventTrigger_Output_TransferLog{
						TransferLog: transferLog,
					},
				}

				// Use shared function to build trigger data map from the TransferLog protobuf
				triggerDataMap = buildTriggerDataMapFromProtobuf(avsproto.TriggerType_TRIGGER_TYPE_EVENT, v.parsedTriggerData.Event, v.logger)
			} else {
				// Use shared function to build trigger data map from protobuf trigger outputs
				triggerDataMap = buildTriggerDataMapFromProtobuf(triggerData.Type, triggerData.Output, v.logger)
			}

			// Create dual-access map to support both camelCase and snake_case field access
			// This enables both template fallback ({{trigger.data.token_symbol}}) and direct JS access
			// (const {tokenSymbol} = eventTrigger.data AND const {token_symbol} = eventTrigger.data)
			dualAccessTriggerData := CreateDualAccessMap(triggerDataMap)
			v.AddVar(triggerNameStd, map[string]any{"data": dualAccessTriggerData})
		}
	} else if task != nil { // Fallback if triggerData is nil but task is not
		triggerNameStd, err := v.GetTriggerNameAsVar()
		if err == nil {
			v.AddVar(triggerNameStd, map[string]any{"data": map[string]any{}}) // Empty data map
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
		if len(q) > 0 {
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
			// that should immediately proceed to its Next nodes.
			v.mu.Lock()
			if len(stepToExecute.Next) > 0 {
				nextStepID := stepToExecute.Next[0]
				currentStep = v.plans[nextStepID]
				v.mu.Unlock()
				continue
			}
			v.mu.Unlock()
			// If no next step, end execution
			currentStep = nil
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
			} else {
				// TODO: Support multiple next for parallel execution paths if VM.Run is to manage them.
				// For now, taking the first.
				currentStep = v.plans[stepToExecute.Next[0]]
			}
		}
		v.mu.Unlock()
	}
	return nil
}

func (v *VM) executeNode(node *avsproto.TaskNode) (*Step, error) {
	v.mu.Lock()
	v.instructionCount++
	v.mu.Unlock()

	if node == nil {
		return nil, fmt.Errorf("executeNode called with nil node")
	}

	// Extract and set input data for this node (making it available as node_name.input)
	inputData := ExtractNodeInputData(node)
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
			executionLog := v.createExecutionStep(stepID, false, "BlockTrigger nodes require real blockchain data - mock data not supported", "", time.Now().UnixMilli())
			executionLog.EndAt = time.Now().UnixMilli()
			return executionLog, fmt.Errorf("BlockTrigger nodes require real blockchain data - mock data not supported")
		}

		// If Config is nil and it's not a blockTrigger, return an error
		if v.logger != nil {
			v.logger.Error("runCustomCode: CustomCodeNode Config is nil", "stepID", stepID)
		}
		// Get the node's input data
		var nodeInput *structpb.Value
		v.mu.Lock()
		if taskNode, exists := v.TaskNodes[stepID]; exists {
			nodeInput = taskNode.Input
		}
		v.mu.Unlock()

		return &avsproto.Execution_Step{
			Id:      stepID, // Use new 'id' field
			Success: false,
			Error:   "CustomCodeNode Config is nil",
			StartAt: time.Now().UnixMilli(),
			EndAt:   time.Now().UnixMilli(),
			Input:   nodeInput, // Include node input data for debugging
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
	p := NewLoopProcessor(v)
	executionLog, err := p.Execute(stepID, nodeValue) // Loop processor internally calls RunNodeWithInputs
	v.mu.Lock()
	if executionLog != nil {
		executionLog.Inputs = v.collectInputKeysForLog(stepID)
	}
	v.mu.Unlock()
	// v.addExecutionLog(executionLog)
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
func convertToCamelCase(s string) string {
	parts := strings.Split(s, "_")
	if len(parts) <= 1 {
		return s
	}

	result := parts[0]
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			result += strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return result
}

// convertToSnakeCase converts camelCase to snake_case
// Example: "blockNumber" -> "block_number"
func convertToSnakeCase(s string) string {
	// Handle empty strings
	if s == "" {
		return s
	}

	var result strings.Builder
	runes := []rune(s)

	for i, r := range runes {
		// Convert to lowercase
		lower := unicode.ToLower(r)

		// Add underscore before uppercase letter if:
		// 1. Not the first character AND
		// 2. Current character is uppercase AND
		// 3. Previous character is not uppercase OR next character is lowercase
		if i > 0 && unicode.IsUpper(r) {
			prevIsUpper := i > 0 && unicode.IsUpper(runes[i-1])
			nextIsLower := i < len(runes)-1 && unicode.IsLower(runes[i+1])

			// Add underscore if:
			// - Previous char is not uppercase (camelCase boundary like "testHTTP" -> "test_HTTP")
			// - OR this is part of acronym but next char is lowercase (like "HTTP" in "HTTPSConnection" -> "HTTPS_Connection")
			if !prevIsUpper || nextIsLower {
				result.WriteRune('_')
			}
		}

		result.WriteRune(lower)
	}

	return result.String()
}

// resolveVariableWithFallback tries to resolve a variable path, with smart fallback for node_name.data paths
//
// PROBLEM: gRPC automatically converts Go snake_case field names to JavaScript camelCase during protobuf conversion.
// Templates using camelCase (like {{trigger.data.blockNumber}}) expect protobuf-converted field names,
// but the actual data may still be in snake_case format (like block_number).
//
// SOLUTION: For any node_name.data.field_name patterns using camelCase, provide snake_case fallback:
// 1. First try original path as-is
// 2. If original uses camelCase and fails, try snake_case conversion as fallback
// 3. If original uses snake_case, no fallback needed (already native format)
//
// Examples:
//
//	{{trigger.data.blockNumber}} -> tries blockNumber, then block_number (camelCase with fallback)
//	{{trigger.data.block_number}} -> tries block_number only (snake_case, no fallback needed)
//	{{apiNode.data.responseData}} -> tries responseData, then response_data (camelCase with fallback)
//	{{apiNode.data.response_data}} -> tries response_data only (snake_case, no fallback needed)
//	{{workflowContext.user_id}} -> tries user_id only (not a node.data pattern)
func (v *VM) resolveVariableWithFallback(jsvm *goja.Runtime, varPath string, currentVars map[string]any) (interface{}, bool) {
	// First try the original path
	script := fmt.Sprintf(`(() => { try { return %s; } catch(e) { return undefined; } })()`, varPath)
	if evaluated, err := jsvm.RunString(script); err == nil {
		exportedValue := evaluated.Export()
		// Check if we got a real value (not undefined)
		if exportedValue != nil && fmt.Sprintf("%v", exportedValue) != "undefined" {
			return exportedValue, true
		}
	}

	// Check if this is a node_name.data.field_name pattern for smart variable resolution fallback
	if strings.Contains(varPath, ".data.") {
		parts := strings.Split(varPath, ".data.")
		if len(parts) == 2 {
			nodeName := parts[0]
			fieldPath := parts[1]

			fieldParts := strings.Split(fieldPath, ".")

			// Check if the original field path contains snake_case (underscores)
			hasSnakeCase := false
			for _, part := range fieldParts {
				if strings.Contains(part, "_") {
					hasSnakeCase = true
					break
				}
			}

			// If we have snake_case, try converting to camelCase as fallback
			if hasSnakeCase {
				// Convert snake_case field path to camelCase - handle nested paths like "field.subfield"
				camelFieldParts := make([]string, len(fieldParts))
				for i, part := range fieldParts {
					camelFieldParts[i] = convertToCamelCase(part)
				}
				camelFieldPath := strings.Join(camelFieldParts, ".")

				// Try with camelCase field names
				camelScript := fmt.Sprintf(`(() => { try { return %s.data.%s; } catch(e) { return undefined; } })()`, nodeName, camelFieldPath)
				if evaluated, err := jsvm.RunString(camelScript); err == nil {
					exportedValue := evaluated.Export()
					if exportedValue != nil && fmt.Sprintf("%v", exportedValue) != "undefined" {
						if v.logger != nil {
							v.logger.Debug("variable resolved using camelCase fallback",
								"originalPath", varPath,
								"resolvedPath", fmt.Sprintf("%s.data.%s", nodeName, camelFieldPath),
								"value", exportedValue)
						}
						return exportedValue, true
					}
				}
			}

			// Check if the original field path contains camelCase (uppercase letters)
			hasCamelCase := false
			for _, part := range fieldParts {
				for _, r := range part {
					if unicode.IsUpper(r) {
						hasCamelCase = true
						break
					}
				}
				if hasCamelCase {
					break
				}
			}

			// If we have camelCase, try converting to snake_case as fallback
			if hasCamelCase {
				// Convert camelCase field path to snake_case - handle nested paths like "field.subfield"
				snakeFieldParts := make([]string, len(fieldParts))
				for i, part := range fieldParts {
					snakeFieldParts[i] = convertToSnakeCase(part)
				}
				snakeFieldPath := strings.Join(snakeFieldParts, ".")

				// Try with snake_case field names
				snakeScript := fmt.Sprintf(`(() => { try { return %s.data.%s; } catch(e) { return undefined; } })()`, nodeName, snakeFieldPath)
				if evaluated, err := jsvm.RunString(snakeScript); err == nil {
					exportedValue := evaluated.Export()
					if exportedValue != nil && fmt.Sprintf("%v", exportedValue) != "undefined" {
						if v.logger != nil {
							v.logger.Debug("variable resolved using snake_case fallback",
								"originalPath", varPath,
								"resolvedPath", fmt.Sprintf("%s.data.%s", nodeName, snakeFieldPath),
								"value", exportedValue)
						}
						return exportedValue, true
					}
				}
			}
		}
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

		script := fmt.Sprintf(`(() => { return %s; })()`, jsExpr)
		evaluated, err := jsvm.RunString(script)
		if v.logger != nil {
			v.logger.Debug("evaluating pre-processor script", "task_id", v.GetTaskId(), "script", script, "result", evaluated, "error", err)
		}
		if err != nil {
			// Keep the expression unchanged (original behavior)
			// This is the expected behavior for existing tests
			if v.logger != nil {
				v.logger.Debug("template variable evaluation failed, keeping expression unchanged", "expression", expr, "error", err)
			}
			// Skip this expression and continue looking for the next one
			searchPos = end + 2
			continue
		}

		exportedValue := evaluated.Export()
		var replacement string
		if t, ok := exportedValue.(time.Time); ok {
			replacement = t.In(time.UTC).Format("2006-01-02 15:04:05.000 +0000 UTC")
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
		v.logger.Info("🔍 collectInputKeysForLog DEBUG",
			"excludeStepID", excludeStepID,
			"excludeVarName", excludeVarName,
			"totalVars", len(v.vars))
	}

	for k, value := range v.vars {
		if !contains(macros.MacroFuncs, k) { // `contains` is a global helper
			// Debug log each variable being processed
			if v.logger != nil {
				v.logger.Info("🔍 Processing variable", "key", k, "exclude", excludeVarName, "match", k == excludeVarName)
			}

			// Skip the current node's own variables from its inputsList
			if excludeVarName != "" && k == excludeVarName {
				if v.logger != nil {
					v.logger.Info("🚫 Excluding current node variable", "key", k)
				}
				continue
			}

			// Skip system variables that shouldn't appear as inputs
			if k == APContextVarName {
				inputKeys = append(inputKeys, APContextConfigVarsPath)
			} else if k == WorkflowContextVarName {
				inputKeys = append(inputKeys, WorkflowContextVarName) // Use as-is, no .data suffix
			} else if k == "triggerConfig" {
				// Skip triggerConfig system variable - it shouldn't appear in inputsList
				if v.logger != nil {
					v.logger.Info("🚫 Excluding triggerConfig system variable")
				}
				continue
			} else {
				// For regular variables, check if they have data and/or input fields
				if valueMap, ok := value.(map[string]any); ok {
					// Check for .data field
					if _, hasData := valueMap["data"]; hasData {
						dataKey := fmt.Sprintf("%s.%s", k, DataSuffix)
						inputKeys = append(inputKeys, dataKey)
						if v.logger != nil {
							v.logger.Info("✅ Added data field", "key", dataKey)
						}
					}
					// Check for .input field
					if _, hasInput := valueMap["input"]; hasInput {
						inputKey := fmt.Sprintf("%s.input", k)
						inputKeys = append(inputKeys, inputKey)
						if v.logger != nil {
							v.logger.Info("✅ Added input field", "key", inputKey)
						}
					}
				} else {
					// Fallback for non-map variables (backward compatibility)
					dataKey := fmt.Sprintf("%s.%s", k, DataSuffix)
					inputKeys = append(inputKeys, dataKey)
					if v.logger != nil {
						v.logger.Info("✅ Added fallback data field", "key", dataKey)
					}
				}
			}
		}
	}

	if v.logger != nil {
		v.logger.Info("🔍 Final inputKeys", "keys", inputKeys, "count", len(inputKeys))
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
			varname = fmt.Sprintf("%s.%s", varname, DataSuffix)
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
		return &avsproto.Execution_Step{
			Id:      node.Id, // Use new 'id' field
			Success: false,
			Error:   "BlockTrigger nodes require real blockchain data - mock data not supported",
			StartAt: time.Now().UnixMilli(),
			EndAt:   time.Now().UnixMilli(),
			Input:   node.Input, // Include node input data for debugging
		}, fmt.Errorf("BlockTrigger nodes require real blockchain data - mock data not supported")
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
	node := &avsproto.TaskNode{Id: nodeID, Name: "Single Node Execution: " + nodeType}

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

		// Support both snake_case and camelCase for backward compatibility
		if address, ok := config["contract_address"].(string); ok {
			contractConfig.ContractAddress = address
		} else if address, ok := config["contractAddress"].(string); ok {
			contractConfig.ContractAddress = address
		}

		if abi, ok := config["contract_abi"].(string); ok {
			contractConfig.ContractAbi = abi
		} else if abi, ok := config["contractAbi"].(string); ok {
			contractConfig.ContractAbi = abi
		}

		// Handle method calls - for backward compatibility, support single call_data
		if callData, ok := config["call_data"].(string); ok {
			// Single method call for backward compatibility
			methodCall := &avsproto.ContractReadNode_MethodCall{
				CallData:   callData,
				MethodName: "", // Will be determined from ABI
			}
			contractConfig.MethodCalls = []*avsproto.ContractReadNode_MethodCall{methodCall}
		} else if callData, ok := config["callData"].(string); ok {
			// Single method call for backward compatibility (camelCase)
			methodCall := &avsproto.ContractReadNode_MethodCall{
				CallData:   callData,
				MethodName: "", // Will be determined from ABI
			}
			contractConfig.MethodCalls = []*avsproto.ContractReadNode_MethodCall{methodCall}
		} else if methodCalls, ok := config["method_calls"].([]interface{}); ok {
			// Multiple method calls (snake_case)
			for _, methodCallInterface := range methodCalls {
				if methodCallMap, ok := methodCallInterface.(map[string]interface{}); ok {
					methodCall := &avsproto.ContractReadNode_MethodCall{}
					if callData, ok := methodCallMap["call_data"].(string); ok {
						methodCall.CallData = callData
					}
					if methodName, ok := methodCallMap["method_name"].(string); ok {
						methodCall.MethodName = methodName
					}
					contractConfig.MethodCalls = append(contractConfig.MethodCalls, methodCall)
				}
			}
		} else if methodCalls, ok := config["methodCalls"].([]interface{}); ok {
			// Multiple method calls (new clean naming from SDK)
			for _, methodCallInterface := range methodCalls {
				if methodCallMap, ok := methodCallInterface.(map[string]interface{}); ok {
					methodCall := &avsproto.ContractReadNode_MethodCall{}
					if callData, ok := methodCallMap["callData"].(string); ok {
						methodCall.CallData = callData
					} else if callData, ok := methodCallMap["call_data"].(string); ok {
						methodCall.CallData = callData
					}
					if methodName, ok := methodCallMap["methodName"].(string); ok {
						methodCall.MethodName = methodName
					} else if methodName, ok := methodCallMap["method_name"].(string); ok {
						methodCall.MethodName = methodName
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

		// Support both snake_case and camelCase for backward compatibility
		if address, ok := config["contract_address"].(string); ok {
			contractConfig.ContractAddress = address
		} else if address, ok := config["contractAddress"].(string); ok {
			contractConfig.ContractAddress = address
		}

		if abi, ok := config["contract_abi"].(string); ok {
			contractConfig.ContractAbi = abi
		} else if abi, ok := config["contractAbi"].(string); ok {
			contractConfig.ContractAbi = abi
		}

		// For backward compatibility, support single callData field
		if callData, ok := config["call_data"].(string); ok {
			contractConfig.CallData = callData
		} else if callData, ok := config["callData"].(string); ok {
			contractConfig.CallData = callData
		}

		// Handle method calls array - newer format supports multiple method calls
		if methodCalls, ok := config["method_calls"].([]interface{}); ok {
			// Multiple method calls (snake_case)
			for _, methodCallInterface := range methodCalls {
				if methodCallMap, ok := methodCallInterface.(map[string]interface{}); ok {
					methodCall := &avsproto.ContractWriteNode_MethodCall{}
					if callData, ok := methodCallMap["call_data"].(string); ok {
						methodCall.CallData = callData
					}
					if methodName, ok := methodCallMap["method_name"].(string); ok {
						methodCall.MethodName = methodName
					}
					contractConfig.MethodCalls = append(contractConfig.MethodCalls, methodCall)
				}
			}
		} else if methodCalls, ok := config["methodCalls"].([]interface{}); ok {
			// Multiple method calls (new clean naming from SDK)
			for _, methodCallInterface := range methodCalls {
				if methodCallMap, ok := methodCallInterface.(map[string]interface{}); ok {
					methodCall := &avsproto.ContractWriteNode_MethodCall{}
					if callData, ok := methodCallMap["callData"].(string); ok {
						methodCall.CallData = callData
					} else if callData, ok := methodCallMap["call_data"].(string); ok {
						methodCall.CallData = callData
					}
					if methodName, ok := methodCallMap["methodName"].(string); ok {
						methodCall.MethodName = methodName
					} else if methodName, ok := methodCallMap["method_name"].(string); ok {
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
		// Support both snake_case and camelCase for backward compatibility
		if sourceId, ok := config["source_id"].(string); ok {
			filterConfig.SourceId = sourceId
		} else if sourceId, ok := config["sourceId"].(string); ok {
			filterConfig.SourceId = sourceId
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

		// Support both snake_case and camelCase for backward compatibility
		if sourceId, ok := config["source_id"].(string); ok {
			loopConfig.SourceId = sourceId
		} else if sourceId, ok := config["sourceId"].(string); ok {
			loopConfig.SourceId = sourceId
		}

		if iterVal, ok := config["iter_val"].(string); ok {
			loopConfig.IterVal = iterVal
		} else if iterVal, ok := config["iterVal"].(string); ok {
			loopConfig.IterVal = iterVal
		}

		if iterKey, ok := config["iter_key"].(string); ok {
			loopConfig.IterKey = iterKey
		} else if iterKey, ok := config["iterKey"].(string); ok {
			loopConfig.IterKey = iterKey
		}

		// Handle the nested runner configuration (CustomCode, RestAPI, etc.)
		loopNode := &avsproto.LoopNode{
			Config: loopConfig,
		}

		// Check for different runner types
		if customCode, ok := config["customCode"].(map[string]interface{}); ok {
			if customConfig, ok := customCode["config"].(map[string]interface{}); ok {
				ccConfig := &avsproto.CustomCodeNode_Config{}
				if source, ok := customConfig["source"].(string); ok {
					ccConfig.Source = source
				}
				if lang, ok := customConfig["lang"].(string); ok {
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
			}
		} else if restApi, ok := config["restApi"].(map[string]interface{}); ok {
			if restConfig, ok := restApi["config"].(map[string]interface{}); ok {
				rConfig := &avsproto.RestAPINode_Config{}
				if url, ok := restConfig["url"].(string); ok {
					rConfig.Url = url
				}
				if method, ok := restConfig["method"].(string); ok {
					rConfig.Method = method
				}
				if body, ok := restConfig["body"].(string); ok {
					rConfig.Body = body
				}

				// Handle headers
				if headersMap, ok := restConfig["headersMap"].([][]string); ok {
					headers := make(map[string]string)
					for _, header := range headersMap {
						if len(header) == 2 {
							headers[header[0]] = header[1]
						}
					}
					rConfig.Headers = headers
				} else if headersAny, ok := restConfig["headersMap"].([]interface{}); ok {
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
			}
		}

		// Validate that a runner is provided to prevent nil-pointer dereference
		if loopNode.Runner == nil {
			return nil, fmt.Errorf("loop node must have either customCode or restApi runner configuration")
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

	// Look up the node to get its type, name, and input data
	var nodeType string = "UNSPECIFIED"
	var nodeName string = "unknown"
	var nodeInput *structpb.Value

	if node, exists := v.TaskNodes[nodeId]; exists {
		nodeType = node.Type.String()
		nodeName = node.Name
		nodeInput = node.Input
	}

	step := &avsproto.Execution_Step{
		Id:      nodeId, // Use new 'id' field
		Success: success,
		Error:   errorMsg,
		Log:     logMsg,
		StartAt: startTime,
		EndAt:   startTime, // Will be updated by caller if needed
		Type:    nodeType,  // Use new 'type' field as string
		Name:    nodeName,  // Use new 'name' field
		Input:   nodeInput, // Include node input data for debugging
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

// CreateDualAccessMap creates a map with both camelCase and snake_case field names
// pointing to the same values. This enables JavaScript code to access fields using
// either naming convention, providing fallback support for direct variable access.
//
// Example:
//
//	input: {"tokenSymbol": "USDC", "blockNumber": 123}
//	output: {"tokenSymbol": "USDC", "token_symbol": "USDC", "blockNumber": 123, "block_number": 123}
//
// This solves the issue where:
// - Templates use fallback: {{trigger.data.token_symbol}} -> tries token_symbol, then tokenSymbol
// - Direct JS access needs both: const {tokenSymbol} = data AND const {token_symbol} = data
func CreateDualAccessMap(data map[string]interface{}) map[string]interface{} {
	if data == nil {
		return nil
	}

	result := make(map[string]interface{})

	// First, copy all original fields
	for key, value := range data {
		result[key] = value
	}

	// Then, add the alternate naming convention for each field
	for key, value := range data {
		// Check if the key contains underscores (snake_case)
		if strings.Contains(key, "_") {
			// Convert snake_case to camelCase and add it
			camelKey := convertToCamelCase(key)
			if camelKey != key && result[camelKey] == nil {
				result[camelKey] = value
			}
		} else {
			// Check if the key contains uppercase letters (camelCase)
			hasCamelCase := false
			for _, r := range key {
				if unicode.IsUpper(r) {
					hasCamelCase = true
					break
				}
			}

			if hasCamelCase {
				// Convert camelCase to snake_case and add it
				snakeKey := convertToSnakeCase(key)
				if snakeKey != key && result[snakeKey] == nil {
					result[snakeKey] = value
				}
			}
		}
	}

	return result
}

// ExtractNodeInputData extracts input data from a TaskNode protobuf message
func ExtractNodeInputData(taskNode *avsproto.TaskNode) map[string]interface{} {
	if taskNode == nil || taskNode.Input == nil {
		return nil
	}

	// Convert protobuf.Value to Go native types
	inputInterface := taskNode.Input.AsInterface()
	if inputMap, ok := inputInterface.(map[string]interface{}); ok {
		return inputMap
	}
	return nil
}

// ExtractTriggerInputData extracts input data from a TaskTrigger protobuf message
func ExtractTriggerInputData(trigger *avsproto.TaskTrigger) map[string]interface{} {
	if trigger == nil {
		return nil
	}

	// Check each trigger type and extract input from the correct nested object
	switch trigger.GetTriggerType().(type) {
	case *avsproto.TaskTrigger_Block:
		blockTrigger := trigger.GetBlock()
		if blockTrigger != nil && blockTrigger.GetInput() != nil {
			inputInterface := blockTrigger.GetInput().AsInterface()
			if inputMap, ok := inputInterface.(map[string]interface{}); ok {
				return inputMap
			}
		}
	case *avsproto.TaskTrigger_Cron:
		cronTrigger := trigger.GetCron()
		if cronTrigger != nil && cronTrigger.GetInput() != nil {
			inputInterface := cronTrigger.GetInput().AsInterface()
			if inputMap, ok := inputInterface.(map[string]interface{}); ok {
				return inputMap
			}
		}
	case *avsproto.TaskTrigger_Event:
		eventTrigger := trigger.GetEvent()
		if eventTrigger != nil && eventTrigger.GetInput() != nil {
			inputInterface := eventTrigger.GetInput().AsInterface()
			if inputMap, ok := inputInterface.(map[string]interface{}); ok {
				return inputMap
			}
		}
	case *avsproto.TaskTrigger_FixedTime:
		fixedTimeTrigger := trigger.GetFixedTime()
		if fixedTimeTrigger != nil && fixedTimeTrigger.GetInput() != nil {
			inputInterface := fixedTimeTrigger.GetInput().AsInterface()
			if inputMap, ok := inputInterface.(map[string]interface{}); ok {
				return inputMap
			}
		}
	case *avsproto.TaskTrigger_Manual:
		// Manual triggers use the top-level TaskTrigger.input field
		// since the trigger_type is just a boolean, not a nested object
		if trigger.GetInput() != nil {
			inputInterface := trigger.GetInput().AsInterface()
			if inputMap, ok := inputInterface.(map[string]interface{}); ok {
				return inputMap
			}
		}
		return nil
	}
	return nil
}
