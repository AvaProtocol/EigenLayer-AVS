package taskengine

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/oklog/ulid/v2"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

type VMState string

const (
	VMStateInitialize         VMState = "vm_initialize"
	VMStateCompiled           VMState = "vm_compiled"
	VMStateReady              VMState = "vm_ready"
	VMStateExecuting          VMState = "vm_executing"
	VMStateCompleted          VMState = "vm_completed"
	VMMaxPreprocessIterations         = 100
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
	c.vm.vars[nodeNameVar] = map[string]any{
		"data": data,
	}
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
	TransferLog *avsproto.Execution_TransferLogOutput
	EvmLog      *avsproto.Evm_Log
	Block       *avsproto.Execution_BlockOutput
	Time        *avsproto.Execution_TimeOutput
}

func (t *triggerDataType) GetValue() avsproto.IsExecution_OutputData {
	if t.TransferLog != nil {
		return &avsproto.Execution_TransferLog{TransferLog: t.TransferLog}
	}
	if t.EvmLog != nil {
		return &avsproto.Execution_EvmLog{EvmLog: t.EvmLog}
	}
	if t.Block != nil {
		return &avsproto.Execution_Block{Block: t.Block}
	}
	if t.Time != nil {
		return &avsproto.Execution_Time{Time: t.Time}
	}
	return nil
}

type VM struct {
	TaskID            string
	TaskNodes         map[string]*avsproto.TaskNode
	TaskOwner         common.Address
	task              *model.Task
	reason            *avsproto.TriggerReason
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
	re := regexp.MustCompile(`[^a-zA-Z0-9_$]`)
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
	standardized := re.ReplaceAllString(name, "_")
	if len(standardized) == 0 || !regexp.MustCompile(`^[a-zA-Z_$]`).MatchString(standardized[:1]) {
		standardized = "_" + standardized
	}
	return standardized, nil
}

func (v *VM) GetNodeNameAsVar(nodeID string) string {
	v.mu.Lock() // Lock for reading TaskNodes
	defer v.mu.Unlock()
	return v.getNodeNameAsVarLocked(nodeID)
}

// getNodeNameAsVarLocked is the internal version that assumes the mutex is already held
func (v *VM) getNodeNameAsVarLocked(nodeID string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9_$]`)
	node, ok := v.TaskNodes[nodeID]
	if !ok {
		return "_" + re.ReplaceAllString(nodeID, "_") // Sanitize nodeID itself if not found
	}
	name := node.Name
	if name == "" {
		name = nodeID
	}
	standardized := re.ReplaceAllString(name, "_")
	if len(standardized) == 0 || !regexp.MustCompile(`^[a-zA-Z_$]`).MatchString(standardized[:1]) {
		standardized = "_" + standardized
	}
	return standardized
}

func NewVMWithData(task *model.Task, reason *avsproto.TriggerReason, smartWalletConfig *config.SmartWalletConfig, secrets map[string]string) (*VM, error) {
	return NewVMWithDataAndTransferLog(task, reason, smartWalletConfig, secrets, nil)
}

func NewVMWithDataAndTransferLog(task *model.Task, reason *avsproto.TriggerReason, smartWalletConfig *config.SmartWalletConfig, secrets map[string]string, transferLog *avsproto.Execution_TransferLogOutput) (*VM, error) {
	var taskOwner common.Address
	if task != nil && task.Owner != "" {
		taskOwner = common.HexToAddress(task.Owner)
	}

	v := NewVM() // Initializes mutex, maps, etc.
	v.TaskOwner = taskOwner
	v.secrets = secrets
	v.task = task
	v.reason = reason
	v.smartWalletConfig = smartWalletConfig
	v.parsedTriggerData = &triggerDataType{} // Initialize parsedTriggerData

	// Initialize apContext with configVars containing secrets and macro variables
	configVars := make(map[string]string)
	// Add secrets (they override macro variables if there are conflicts)
	if secrets != nil {
		for k, v := range secrets {
			configVars[k] = v
		}
	}
	v.AddVar("apContext", map[string]map[string]string{
		"configVars": configVars,
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
	}

	// If 'reason' is not nil, we can still try to add its basic fields (block_number, epoch, etc.)
	// to the vars, if that's intended for the <trigger-name>.data variable.
	if reason != nil {
		triggerNameStd, err := v.GetTriggerNameAsVar()
		if err == nil { // Proceed if trigger name is valid
			var triggerData map[string]interface{}

			// If we have transfer log data, use it to populate rich trigger data
			if transferLog != nil {
				v.parsedTriggerData.TransferLog = transferLog
				triggerData = map[string]interface{}{
					"token_name":        transferLog.TokenName,
					"token_symbol":      transferLog.TokenSymbol,
					"token_decimals":    transferLog.TokenDecimals,
					"transaction_hash":  transferLog.TransactionHash,
					"address":           transferLog.Address,
					"block_number":      transferLog.BlockNumber,
					"block_timestamp":   transferLog.BlockTimestamp,
					"from_address":      transferLog.FromAddress,
					"to_address":        transferLog.ToAddress,
					"value":             transferLog.Value,
					"value_formatted":   transferLog.ValueFormatted,
					"transaction_index": transferLog.TransactionIndex,
				}
			} else {
				// Create a map to store the basic fields from the reason object
				triggerData = make(map[string]interface{})
				if reason.BlockNumber != 0 {
					triggerData["block_number"] = reason.BlockNumber
				}
				if reason.LogIndex != 0 { // Use a reasonable check, e.g. not zero or if it's optional
					triggerData["log_index"] = reason.LogIndex
				}
				if reason.TxHash != "" {
					triggerData["tx_hash"] = reason.TxHash
				}
				if reason.Epoch != 0 {
					triggerData["epoch"] = reason.Epoch
				}
				if reason.Type != avsproto.TriggerReason_Unset {
					triggerData["type"] = reason.Type.String() // Store as string
				}
			}
			v.AddVar(triggerNameStd, map[string]any{"data": triggerData})
		}
	} else if task != nil { // Fallback if reason is nil but task is not
		triggerNameStd, err := v.GetTriggerNameAsVar()
		if err == nil {
			v.AddVar(triggerNameStd, map[string]any{"data": map[string]any{}}) // Empty data map
		}
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
		inDegree[edge.Target]++ // All valid targets contribute to in-degree. Trigger adjustment comes next.
	}

	q := make([]string, 0, len(v.TaskNodes))
	initialEntryCandidates := make(map[string]bool)

	if v.task.Trigger == nil || v.task.Trigger.Id == "" {
		if v.entrypoint == "" { // Only if not set by RunNodeWithInputs
			for nodeID, deg := range inDegree {
				if deg == 0 {
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
			if deg == 0 && initialEntryCandidates[nodeID] { // Must be a trigger target (or graph start if no trigger)
				q = append(q, nodeID)
			} else if deg == 0 && (v.task.Trigger == nil || v.task.Trigger.Id == "") { // No trigger, any 0-in-degree node
				q = append(q, nodeID)
			}
		}
		// Remove duplicates from q if any node was added twice through different logic paths
		// (though current logic should prevent this for trigger-based starts)
		if len(q) > 0 {
			uniqueQ := make([]string, 0, len(q))
			seenInQ := make(map[string]bool)
			for _, item := range q {
				if !seenInQ[item] {
					uniqueQ = append(uniqueQ, item)
					seenInQ[item] = true
				}
			}
			q = uniqueQ
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
			for _, cond := range node.GetBranch().Conditions {
				condSourceID := fmt.Sprintf("%s.%s", currNodeID, cond.Id)
				if nextNodesFromCond, ok := adj[condSourceID]; ok {
					v.plans[condSourceID] = &Step{NodeID: condSourceID, Next: nextNodesFromCond}
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

		// Also process branch condition edges if this is a branch node
		if node, isTaskNode := v.TaskNodes[currNodeID]; isTaskNode && node.GetBranch() != nil {
			for _, cond := range node.GetBranch().Conditions {
				condSourceID := fmt.Sprintf("%s.%s", currNodeID, cond.Id)
				for _, neighborNodeID := range adj[condSourceID] {
					if _, isActualNode := v.TaskNodes[neighborNodeID]; isActualNode {
						inDegree[neighborNodeID]--
						if inDegree[neighborNodeID] == 0 {
							topoQueue = append(topoQueue, neighborNodeID)
						}
					}
				}
			}
		}
	}

	if processedCount != len(v.TaskNodes) {
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
	v.Status = VMStateExecuting
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
			// If we reach here with a non-TaskNode ID, it's likely a logic error in plan navigation.
			if v.logger != nil {
				v.logger.Error("Node ID in current step not found in TaskNodes, might be a plan navigation issue.", "nodeID", stepToExecute.NodeID)
			}
			// If it's a conceptual step (like branch output), its 'Next' should point to a real node.
			v.mu.Lock()
			if len(stepToExecute.Next) > 0 {
				currentStep = v.plans[stepToExecute.Next[0]]
				v.mu.Unlock()
				continue
			}
			v.mu.Unlock()
			return fmt.Errorf("plan error: node ID '%s' not found in TaskNodes and no next step defined", stepToExecute.NodeID)
		}

		jump, err := v.executeNode(node) // executeNode calls sub-processors which should use AddVar for VM state changes
		if err != nil {
			return err // Abort on first error
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
	executionLog.Inputs = v.collectInputKeysForLog() // Uses locked v.vars
	v.mu.Unlock()
	// v.addExecutionLog(executionLog) // Caller will add
	return executionLog, err // RestAPI node doesn't dictate a jump
}

func (v *VM) runGraphQL(stepID string, node *avsproto.GraphQLQueryNode) (*avsproto.Execution_Step, error) {
	g, err := NewGraphqlQueryProcessor(v, node.Url)
	var executionLog *avsproto.Execution_Step // Declare to ensure it's always initialized
	if err != nil {
		// Create a failed execution log step
		executionLog = &avsproto.Execution_Step{NodeId: stepID, Success: false, Error: err.Error(), StartAt: time.Now().UnixMilli()}
		executionLog.EndAt = time.Now().UnixMilli()
		// v.addExecutionLog(logEntry) // Caller will add
		return executionLog, err
	}
	var dataOutput any                                      // Placeholder, GraphQL processor might return structured data
	executionLog, dataOutput, err = g.Execute(stepID, node) // Execute should use SetOutputVarForStep
	_ = dataOutput                                          // Use dataOutput if needed later
	v.mu.Lock()
	if executionLog != nil { // Guard against nil log
		executionLog.Inputs = v.collectInputKeysForLog()
	}
	v.mu.Unlock()
	// v.addExecutionLog(executionLog) // Caller will add
	// err from g.Execute is the primary error to return
	return executionLog, err
}

func (v *VM) runContractRead(stepID string, node *avsproto.ContractReadNode) (*avsproto.Execution_Step, error) {
	var executionLog *avsproto.Execution_Step
	if v.smartWalletConfig == nil || v.smartWalletConfig.EthRpcUrl == "" {
		err := fmt.Errorf("smart wallet config or ETH RPC URL not set for contract read")
		executionLog = &avsproto.Execution_Step{NodeId: stepID, Success: false, Error: err.Error(), StartAt: time.Now().UnixMilli()}
		executionLog.EndAt = time.Now().UnixMilli()
		// v.addExecutionLog(logEntry)
		return executionLog, err
	}
	rpcClient, err := ethclient.Dial(v.smartWalletConfig.EthRpcUrl)
	if err != nil {
		executionLog = &avsproto.Execution_Step{NodeId: stepID, Success: false, Error: fmt.Sprintf("failed to dial ETH RPC: %v", err), StartAt: time.Now().UnixMilli()}
		executionLog.EndAt = time.Now().UnixMilli()
		// v.addExecutionLog(logEntry)
		return executionLog, err
	}
	defer rpcClient.Close()

	processor := NewContractReadProcessor(v, rpcClient)
	executionLog, err = processor.Execute(stepID, node)
	v.mu.Lock()
	if executionLog != nil {
		executionLog.Inputs = v.collectInputKeysForLog()
	}
	v.mu.Unlock()
	// v.addExecutionLog(executionLog)
	return executionLog, err
}

func (v *VM) runContractWrite(stepID string, node *avsproto.ContractWriteNode) (*avsproto.Execution_Step, error) {
	var executionLog *avsproto.Execution_Step
	if v.smartWalletConfig == nil || v.smartWalletConfig.EthRpcUrl == "" {
		err := fmt.Errorf("smart wallet config or ETH RPC URL not set for contract write")
		executionLog = &avsproto.Execution_Step{NodeId: stepID, Success: false, Error: err.Error(), StartAt: time.Now().UnixMilli()}
		executionLog.EndAt = time.Now().UnixMilli()
		// v.addExecutionLog(logEntry)
		return executionLog, err
	}
	rpcClient, err := ethclient.Dial(v.smartWalletConfig.EthRpcUrl)
	if err != nil {
		executionLog = &avsproto.Execution_Step{NodeId: stepID, Success: false, Error: fmt.Sprintf("failed to dial ETH RPC: %v", err), StartAt: time.Now().UnixMilli()}
		executionLog.EndAt = time.Now().UnixMilli()
		// v.addExecutionLog(logEntry)
		return executionLog, err
	}
	defer rpcClient.Close()

	processor := NewContractWriteProcessor(v, rpcClient, v.smartWalletConfig, v.TaskOwner)
	executionLog, err = processor.Execute(stepID, node)
	v.mu.Lock()
	if executionLog != nil {
		executionLog.Inputs = v.collectInputKeysForLog()
	}
	v.mu.Unlock()
	// v.addExecutionLog(executionLog)
	return executionLog, err
}

func (v *VM) runCustomCode(stepID string, node *avsproto.CustomCodeNode) (*avsproto.Execution_Step, error) {
	r := NewJSProcessor(v)
	executionLog, err := r.Execute(stepID, node)
	v.mu.Lock()
	if executionLog != nil {
		executionLog.Inputs = v.collectInputKeysForLog()
	}
	v.mu.Unlock()
	// v.addExecutionLog(executionLog)
	return executionLog, err
}

func (v *VM) runBranch(stepID string, nodeValue *avsproto.BranchNode) (*avsproto.Execution_Step, *Step, error) {
	processor := NewBranchProcessor(v)
	executionLog, nextStepIfBranch, err := processor.Execute(stepID, nodeValue) // This evaluates conditions
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
			executionLog.Inputs = v.collectInputKeysForLog()
		}
		v.mu.Unlock()
	}
	// The returned executionLog from processor.Execute should be the one for the main VM.
	// No need to call v.addExecutionLog here if processor.Execute is designed to return the log for the *caller* VM.
	// However, the original structure had v.addExecutionLog(executionLog) *after* the call to processor.Execute.
	// Let's assume processor.Execute returns a log that should be added by the caller.
	// The `addExecutionLog` for this branch step itself is done by `executeNode` using the returned `branchLog`.

	return executionLog, nextStepIfBranch, err // Return the log, the next step, and any error
}

func (v *VM) runLoop(stepID string, nodeValue *avsproto.LoopNode) (*avsproto.Execution_Step, error) {
	p := NewLoopProcessor(v)
	executionLog, err := p.Execute(stepID, nodeValue) // Loop processor internally calls RunNodeWithInputs
	v.mu.Lock()
	if executionLog != nil {
		executionLog.Inputs = v.collectInputKeysForLog()
	}
	v.mu.Unlock()
	// v.addExecutionLog(executionLog)
	return executionLog, err // Loop node itself doesn't dictate a jump in the main plan
}

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

		script := fmt.Sprintf(`(() => { return %s; })()`, expr)
		evaluated, err := jsvm.RunString(script)
		if v.logger != nil {
			v.logger.Debug("evaluating pre-processor script", "task_id", v.GetTaskId(), "script", script, "result", evaluated, "error", err)
		}
		if err != nil {
			result = result[:start] + result[end+2:] // Remove invalid expression
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
	}
	return result
}

func (v *VM) collectInputKeysForLog() []string {
	// This function assumes v.mu is already locked by the caller
	inputKeys := make([]string, 0, len(v.vars))
	for k := range v.vars {
		if !contains(macros.MacroFuncs, k) { // `contains` is a global helper
			varname := k
			if varname == "apContext" { // Specific handling for apContext
				varname = "apContext.configVars"
			} else {
				varname = fmt.Sprintf("%s.data", varname)
			}
			inputKeys = append(inputKeys, varname)
		}
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
		if varname == "apContext" {
			varname = "apContext.configVars"
		} else {
			varname = fmt.Sprintf("%s.data", varname)
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
	// Special handling for blockTrigger - simulate blockchain data locally
	if node.GetCustomCode() != nil && node.Name == "Single Node Execution: blockTrigger" {
		// Extract block number from input variables if provided
		blockNumber := uint64(time.Now().Unix()) // Default to current timestamp as mock block number
		if blockNum, ok := inputVariables["blockNumber"]; ok {
			if bn, ok := blockNum.(float64); ok {
				blockNumber = uint64(bn)
			}
		}

		// Create mock block data
		mockBlockData := map[string]interface{}{
			"blockNumber": blockNumber,
			"blockHash":   fmt.Sprintf("0x%x", time.Now().UnixNano()), // Mock hash
			"timestamp":   time.Now().Unix(),
			"parentHash":  fmt.Sprintf("0x%x", time.Now().UnixNano()-1),
			"difficulty":  "1000000000000000",
			"gasLimit":    uint64(30000000),
			"gasUsed":     uint64(21000),
		}

		// Convert to structpb.Value for protobuf
		structData, err := structpb.NewStruct(mockBlockData)
		if err != nil {
			return nil, fmt.Errorf("failed to create struct data: %w", err)
		}

		// Create successful execution step
		executionStep := &avsproto.Execution_Step{
			NodeId:  node.Id,
			Success: true,
			StartAt: time.Now().UnixMilli(),
			EndAt:   time.Now().UnixMilli(),
			OutputData: &avsproto.Execution_Step_CustomCode{
				CustomCode: &avsproto.CustomCodeNode_Output{
					Data: structpb.NewStructValue(structData),
				},
			},
		}

		return executionStep, nil
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
	if apContextValue, ok := v.vars["apContext"]; ok {
		if tempVM.vars == nil { // Ensure tempVM.vars is initialized
			tempVM.vars = make(map[string]any)
		}
		tempVM.vars["apContext"] = apContextValue
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

	// _, err := tempVM.Run() // Run() would execute and store logs in tempVM.ExecutionLogs
	// For RunNodeWithInputs, we are more interested in the direct log of *this* node's execution.
	// So, call executeNode directly.

	_, err := tempVM.executeNode(node) // This will append to tempVM.ExecutionLogs

	if err != nil {
		// If executeNode itself errored, the log might already be there with failure.
		// If not, ensure a failed log entry.
		if len(tempVM.ExecutionLogs) == 0 {
			failedStep := &avsproto.Execution_Step{
				NodeId:  node.Id,
				Success: false,
				Error:   err.Error(),
				StartAt: time.Now().UnixMilli(),
			}
			failedStep.EndAt = time.Now().UnixMilli()
			tempVM.addExecutionLog(failedStep) // addExecutionLog handles locking
		} else {
			// If a log exists, update it with error if not already set (though executeNode's sub-calls should do this)
			tempVM.mu.Lock()
			lastLog := tempVM.ExecutionLogs[len(tempVM.ExecutionLogs)-1]
			if lastLog.NodeId == node.Id && lastLog.Success { // If marked success but error occurred here
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
	case "restApi":
		url, _ := config["url"].(string)
		method, _ := config["method"].(string)
		body, _ := config["body"].(string)
		headers := make(map[string]string)
		if hm, ok := config["headers"].(map[string]interface{}); ok {
			for k, v := range hm {
				headers[k] = fmt.Sprintf("%v", v)
			}
		}
		node.TaskType = &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{Url: url, Method: method, Body: body, Headers: headers}}
	case "contractRead":
		contractAddress, _ := config["contractAddress"].(string)
		callData, _ := config["callData"].(string)
		contractAbi, _ := config["contractAbi"].(string)
		node.TaskType = &avsproto.TaskNode_ContractRead{ContractRead: &avsproto.ContractReadNode{ContractAddress: contractAddress, CallData: callData, ContractAbi: contractAbi}}
	case "customCode":
		source, _ := config["source"].(string)
		node.TaskType = &avsproto.TaskNode_CustomCode{CustomCode: &avsproto.CustomCodeNode{Lang: avsproto.CustomCodeLang_JavaScript, Source: source}}
	case "branch":
		var conditions []*avsproto.Condition
		if ca, ok := config["conditions"].([]interface{}); ok {
			for _, c := range ca {
				if cm, okc := c.(map[string]interface{}); okc {
					id, _ := cm["id"].(string)
					condType, _ := cm["type"].(string)
					expression, _ := cm["expression"].(string)
					conditions = append(conditions, &avsproto.Condition{Id: id, Type: condType, Expression: expression})
				}
			}
		}
		node.TaskType = &avsproto.TaskNode_Branch{Branch: &avsproto.BranchNode{Conditions: conditions}}
	case "filter": // Assuming FilterNode fields are "expression" and "input" (referring to a var name)
		expression, _ := config["expression"].(string)
		inputVarName, _ := config["input"].(string)
		node.TaskType = &avsproto.TaskNode_Filter{Filter: &avsproto.FilterNode{Expression: expression, Input: inputVarName}}
	case "blockTrigger":
		// Create a custom code node that will be handled specially by RunNodeWithInputs
		node.TaskType = &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{
				Lang:   avsproto.CustomCodeLang_JavaScript,
				Source: "// BlockTrigger placeholder - handled specially",
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
