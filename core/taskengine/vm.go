package taskengine

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"

	"github.com/oklog/ulid/v2"
	"github.com/samber/lo"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc20"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

type VMState string

const (
	VMStateInitialize         = "vm_initialize"
	VMStateCompiled           = "vm_compiled"
	VMStateReady              = "vm_ready"
	VMStateExecuting          = "vm_executing"
	VMStateCompleted          = "vm_completed"
	VMMaxPreprocessIterations = 100
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

// Set the variable for step output so it can be refer and use in subsequent steps
func (c *CommonProcessor) SetOutputVarForStep(stepID string, data any) {
	c.vm.vars.Store(c.vm.GetNodeNameAsVar(stepID), map[string]any{
		"data": data,
	})
}

// Set the variable for step output so it can be refer and use in subsequent steps
func (c *CommonProcessor) GetOutputVar(stepID string) any {
	name := c.vm.GetNodeNameAsVar(stepID)
	if name == "" {
		return nil
	}

	value, ok := c.vm.vars.Load(name)
	if !ok {
		return nil
	}
	
	if valueMap, ok := value.(map[string]any); ok {
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
		return &avsproto.Execution_TransferLog{
			TransferLog: t.TransferLog,
		}
	}
	if t.EvmLog != nil {
		return &avsproto.Execution_EvmLog{
			EvmLog: t.EvmLog,
		}
	}
	if t.Block != nil {
		return &avsproto.Execution_Block{
			Block: t.Block,
		}
	}
	if t.Time != nil {
		return &avsproto.Execution_Time{
			Time: t.Time,
		}
	}

	return nil
}

// The VM is the core component that load the node information and execute them, yield finally result
type VM struct {
	// Input raw task data
	// TaskID can be used to cache compile program
	TaskID    string
	TaskNodes sync.Map // Changed from map[string]*avsproto.TaskNode for thread safety

	//TaskEdges   []*avsproto.TaskEdge
	//TaskTrigger *avsproto.TaskTrigger
	TaskOwner common.Address

	task              *model.Task
	reason            *avsproto.TriggerReason
	parsedTriggerData *triggerDataType

	// execution logs and result per plans
	ExecutionLogs []*avsproto.Execution_Step

	Status VMState
	mu     *sync.Mutex
	// internal state that is set through out program execution
	vars sync.Map // Changed from map[string]any for thread safety

	// hold the name->value of loaded secret for this workflow
	secrets map[string]string

	plans            map[string]*Step
	entrypoint       string
	instructionCount int64

	// smartWalletConfig contains the smart wallet config for the task. It contains the bundler url, entrypoint address, paymaster address, wallet factory address, etc.
	smartWalletConfig *config.SmartWalletConfig

	logger sdklogging.Logger

	// db is used for tracking counter in some nodes, not every node needs it. Example, in contract write we only sponsor first N run so we track this off-chain. Only for tx less than this we will generate PaymasterAndData field
	db storage.Storage
}

func NewVM() *VM {
	v := &VM{
		Status:           VMStateInitialize,
		mu:               &sync.Mutex{},
		instructionCount: 0,
		secrets:          map[string]string{},
	}
	
	// Initialize vars with environment variables
	envVars := macros.GetEnvs(map[string]any{})
	for key, value := range envVars {
		v.vars.Store(key, value)
	}

	return v
}

func (v *VM) Reset() {
	v.ExecutionLogs = []*avsproto.Execution_Step{}
	v.plans = map[string]*Step{}
	v.entrypoint = ""
	v.Status = VMStateInitialize
	v.instructionCount = 0
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
	// Replace invalid characters with _
	re := regexp.MustCompile(`[^a-zA-Z0-9_$]`)

	var name string
	if v.task != nil && v.task.Trigger != nil {
		name = v.task.Trigger.Name
		if name == "" {
			return "", fmt.Errorf("trigger name is required but not defined in task")
		}
	} else if v.task != nil {
		// Task exists but trigger is nil
		return "", fmt.Errorf("trigger is required but not defined in task")
	} else {
		// Task is nil (e.g., for single node execution), use default
		name = "trigger"
	}

	standardized := re.ReplaceAllString(name, "_")

	// Ensure the first character is valid
	if len(standardized) == 0 || !regexp.MustCompile(`^[a-zA-Z_$]`).MatchString(standardized[:1]) {
		standardized = "_" + standardized
	}

	return standardized, nil
}

func (v *VM) GetNodeNameAsVar(nodeID string) string {
	// Replace invalid characters with _
	re := regexp.MustCompile(`[^a-zA-Z0-9_$]`)
<<<<<<< HEAD

	// Get node name if it exists, otherwise use nodeID
	var name string
	if node, exists := v.TaskNodes[nodeID]; exists && node != nil {
		name = node.Name
	}
||||||| parent of 64687b9 (Fix: Replace map with sync.Map for concurrent access in LoopProcessor)
	name := v.TaskNodes[nodeID].Name
=======
	
	nodeObj, ok := v.TaskNodes.Load(nodeID)
	if !ok {
		return "_" + nodeID
	}
	
	node := nodeObj.(*avsproto.TaskNode)
	name := node.Name
>>>>>>> 64687b9 (Fix: Replace map with sync.Map for concurrent access in LoopProcessor)
	if name == "" {
		name = nodeID
	}
<<<<<<< HEAD

	standardized := re.ReplaceAllString(name, "_")
||||||| parent of 64687b9 (Fix: Replace map with sync.Map for concurrent access in LoopProcessor)
	standardized := re.ReplaceAllString(v.TaskNodes[nodeID].Name, "_")
=======
	standardized := re.ReplaceAllString(name, "_")
>>>>>>> 64687b9 (Fix: Replace map with sync.Map for concurrent access in LoopProcessor)

	// Ensure the first character is valid
	if len(standardized) == 0 || !regexp.MustCompile(`^[a-zA-Z_$]`).MatchString(standardized[:1]) {
		standardized = "_" + standardized
	}

	return standardized
}

func NewVMWithData(task *model.Task, reason *avsproto.TriggerReason, smartWalletConfig *config.SmartWalletConfig, secrets map[string]string) (*VM, error) {
	v := &VM{
<<<<<<< HEAD
		Status:            VMStateInitialize,
		TaskNodes:         make(map[string]*avsproto.TaskNode),
||||||| parent of 64687b9 (Fix: Replace map with sync.Map for concurrent access in LoopProcessor)
		Status: VMStateInitialize,
		//TaskEdges:         task.Edges,
		//TaskTrigger:       task.Trigger,
		TaskNodes: make(map[string]*avsproto.TaskNode),
		TaskOwner: common.HexToAddress(task.Owner),

=======
		Status: VMStateInitialize,
		//TaskEdges:         task.Edges,
		//TaskTrigger:       task.Trigger,
		TaskOwner: common.HexToAddress(task.Owner),

>>>>>>> 64687b9 (Fix: Replace map with sync.Map for concurrent access in LoopProcessor)
		plans:             make(map[string]*Step),
		mu:                &sync.Mutex{},
		instructionCount:  0,
		secrets:           secrets,
		smartWalletConfig: smartWalletConfig,
		reason:            reason,
		task:              task,
		parsedTriggerData: &triggerDataType{},
	}

<<<<<<< HEAD
	if task != nil {
		v.TaskOwner = common.HexToAddress(task.Owner)
		for _, node := range task.Nodes {
			v.TaskNodes[node.Id] = node
		}
||||||| parent of 64687b9 (Fix: Replace map with sync.Map for concurrent access in LoopProcessor)
	for _, node := range task.Nodes {
		v.TaskNodes[node.Id] = node
=======
	for _, node := range task.Nodes {
		v.TaskNodes.Store(node.Id, node)
>>>>>>> 64687b9 (Fix: Replace map with sync.Map for concurrent access in LoopProcessor)
	}

<<<<<<< HEAD
	v.vars = macros.GetEnvs(map[string]any{})
	triggerVarName, err := v.GetTriggerNameAsVar()
	if err != nil {
		return nil, fmt.Errorf("failed to get trigger variable name: %w", err)
	}
||||||| parent of 64687b9 (Fix: Replace map with sync.Map for concurrent access in LoopProcessor)
	v.vars = macros.GetEnvs(map[string]any{})
	triggerVarName := v.GetTriggerNameAsVar()
=======
	// Initialize vars with environment variables
	envVars := macros.GetEnvs(map[string]any{})
	for key, value := range envVars {
		v.vars.Store(key, value)
	}
	
	triggerVarName := v.GetTriggerNameAsVar()
>>>>>>> 64687b9 (Fix: Replace map with sync.Map for concurrent access in LoopProcessor)
	// popular trigger data for trigger variable
	if reason != nil {
		v.vars.Store(triggerVarName, map[string]any{
			"data": map[string]any{},
		})

		if reason.LogIndex > 0 && reason.TxHash != "" {
			// if it contains event, we need to fetch and populate data
			receipt, err := rpcConn.TransactionReceipt(context.Background(), common.HexToHash(reason.TxHash))
			if err != nil {
				return nil, err
			}

			var event *types.Log
			//event := receipt.Logs[triggerMetadata.LogIndex]

			// TODO Is there a cheaper way to avoid hitting RPC this much?
			for _, l := range receipt.Logs {
				if uint64(l.Index) == reason.LogIndex {
					event = l
					break
				}
			}

			if event == nil {
				return nil, fmt.Errorf("tx %s doesn't content event %d", reason.TxHash, reason.LogIndex)
			}

			tokenMetadata, err := GetMetadataForTransfer(event)
			ef, err := erc20.NewErc20(event.Address, nil)

			blockHeader, err := GetBlock(event.BlockNumber)
			if err != nil {
				return nil, fmt.Errorf("RPC error getting block header. Retry: %w", err)
			}

			// Only parse data for transfer event
			if strings.EqualFold(event.Topics[0].Hex(), EvmErc20TransferTopic0) {
				parseTransfer, err := ef.ParseTransfer(*event)
				if err != nil {
					// TODO: This error is retryable
					return nil, fmt.Errorf("error parsing transfer event: %w", err)
				}
				formattedValue := ToDecimal(parseTransfer.Value, int(tokenMetadata.Decimals)).String()

				triggerData, _ := v.vars.Load(triggerVarName)
				triggerDataMap := triggerData.(map[string]any)
				triggerDataMap["data"] = map[string]any{
					"topics": lo.Map(event.Topics, func(topic common.Hash, _ int) string {
						return "0x" + strings.ToLower(strings.TrimLeft(topic.String(), "0x"))
					}),
					"data": "0x" + common.Bytes2Hex(event.Data),

					"token_name":        tokenMetadata.Name,
					"token_symbol":      tokenMetadata.Symbol,
					"token_decimals":    tokenMetadata.Decimals,
					"transaction_hash":  event.TxHash,
					"address":           strings.ToLower(event.Address.Hex()),
					"block_number":      event.BlockNumber,
					"block_timestamp":   blockHeader.Time,
					"from_address":      parseTransfer.From.String(),
					"to_address":        parseTransfer.To.String(),
					"value":             parseTransfer.Value.String(),
					"value_formatted":   formattedValue,
					"transaction_index": event.TxIndex,
				}
				v.vars.Store(triggerVarName, triggerDataMap)

				v.parsedTriggerData.TransferLog = &avsproto.Execution_TransferLogOutput{
					TokenName:       tokenMetadata.Name,
					TokenSymbol:     tokenMetadata.Symbol,
					TokenDecimals:   uint32(tokenMetadata.Decimals),
					TransactionHash: event.TxHash.Hex(),
					Address:         event.Address.Hex(),
					BlockNumber:     event.BlockNumber,
					// in Ethereum, timestamp is in seconds, but in our app we use milliseconds, so we need to convert it
					// https://docs.soliditylang.org/en/latest/units-and-global-variables.html#block-and-transaction-properties
					// This is requested in ticket https://github.com/AvaProtocol/EigenLayer-AVS/issues/191 and implemented in https://github.com/AvaProtocol/EigenLayer-AVS/pull/192/files
					// But in that PR, the avs.proto file is updated and documented that this field is in milliseconds but we forgot to update the field in the code.
					// This update happen at a time later and migration is configured to reflect the change in PR 192.
					BlockTimestamp:   blockHeader.Time * 1000,
					FromAddress:      parseTransfer.From.String(),
					ToAddress:        parseTransfer.To.String(),
					Value:            parseTransfer.Value.String(),
					ValueFormatted:   formattedValue,
					TransactionIndex: uint32(event.TxIndex),
				}
			} else {
				v.parsedTriggerData.EvmLog = &avsproto.Evm_Log{
					Address:          event.Address.Hex(),
					BlockHash:        event.BlockHash.Hex(),
					BlockNumber:      event.BlockNumber,
					Data:             "0x" + common.Bytes2Hex(event.Data),
					Index:            uint32(event.Index),
					TransactionHash:  event.TxHash.Hex(),
					TransactionIndex: uint32(event.TxIndex),
					Topics: lo.Map(event.Topics, func(topic common.Hash, _ int) string {
						return "0x" + strings.ToLower(strings.TrimLeft(topic.String(), "0x"))
					}),
				}
			}
		}

		if reason.BlockNumber > 0 {
			triggerData, _ := v.vars.Load(triggerVarName)
			triggerDataMap := triggerData.(map[string]any)
			dataMap := triggerDataMap["data"].(map[string]any)

			// Add both snake_case and camelCase versions for compatibility
			dataMap["block_number"] = reason.BlockNumber
			dataMap["blockNumber"] = reason.BlockNumber
			
			v.vars.Store(triggerVarName, triggerDataMap)

			v.parsedTriggerData.Block = &avsproto.Execution_BlockOutput{
				BlockNumber: uint64(reason.BlockNumber),
			}
		}

		if reason.Epoch > 0 {
			triggerData, _ := v.vars.Load(triggerVarName)
			triggerDataMap := triggerData.(map[string]any)
			dataMap := triggerDataMap["data"].(map[string]any)
			
			dataMap["epoch"] = reason.Epoch
			v.vars.Store(triggerVarName, triggerDataMap)
			
			v.parsedTriggerData.Time = &avsproto.Execution_TimeOutput{
				Epoch: uint64(reason.Epoch),
			}
		}
	}

	v.vars.Store("apContext", map[string]map[string]string{
		"configVars": secrets,
	})

	return v, nil
}

func (v *VM) CreateSandbox() error {
	return nil
}

func (v *VM) AddVar(key string, value any) {
	v.vars.Store(key, value)
}

// Compile generates an execution plan based on edge
func (v *VM) Compile() error {
	triggerId := v.task.Trigger.Id
	for _, edge := range v.task.Edges {
		if strings.Contains(edge.Source, ".") {
			v.plans[edge.Source] = &Step{
				NodeID: edge.Source,
				Next:   []string{edge.Target},
			}

			continue
		}

		if _, ok := v.plans[edge.Target]; !ok {
			v.plans[edge.Target] = &Step{
				NodeID: edge.Target,
				Next:   []string{},
			}
		}

		if edge.Source != triggerId {
			if _, ok := v.plans[edge.Source]; ok {
				v.plans[edge.Source].Next = append(v.plans[edge.Source].Next, edge.Target)
			} else {
				v.plans[edge.Source] = &Step{
					NodeID: edge.Source,
					Next:   []string{edge.Target},
				}
			}
		} else {
			v.entrypoint = edge.Target
		}
	}

	// Cannot find entrypoint
	if v.entrypoint == "" {
		return fmt.Errorf("%s", InvalidEntrypoint)
	}

	v.Status = VMStateReady

	return nil
}

// Run the program. The VM will only run once.
func (v *VM) Run() error {
	if v.Status != VMStateReady {
		return fmt.Errorf("VM isn't in ready state")
	}

	v.mu.Lock()
	v.Status = VMStateExecuting
	defer func() {
		v.Status = VMStateCompleted
		v.mu.Unlock()
	}()
	if len(v.plans) == 0 {
		return fmt.Errorf("internal error: not compiled")
	}

	// TODO Setup a timeout context
	currentStep := v.plans[v.entrypoint]

	for currentStep != nil {
		nodeObj, ok := v.TaskNodes.Load(currentStep.NodeID)
		if !ok {
			return fmt.Errorf("node not found: %s", currentStep.NodeID)
		}
		node := nodeObj.(*avsproto.TaskNode)

		jump, err := v.executeNode(node)
		if err != nil {
			// abort execution as soon as a node raise error
			return err
		}

		if jump == nil {
			jump = currentStep
		}

		if len(jump.Next) == 0 {
			break
		}

		// TODO: Support multiple next
		currentStep = v.plans[jump.Next[0]]
	}

	return nil
}

func (v *VM) executeNode(node *avsproto.TaskNode) (*Step, error) {
	v.instructionCount += 1

	if node == nil {
		return nil, nil
	}

	var step *Step
	var err error

	if nodeValue := node.GetRestApi(); nodeValue != nil {
		step, err = v.runRestApi(node.Id, nodeValue)
	} else if nodeValue := node.GetBranch(); nodeValue != nil {
		step, err = v.runBranch(node.Id, nodeValue)
	} else if nodeValue := node.GetGraphqlQuery(); nodeValue != nil {
		step, err = v.runGraphQL(node.Id, nodeValue)
	} else if nodeValue := node.GetCustomCode(); nodeValue != nil {
		step, err = v.runCustomCode(node.Id, nodeValue)
	} else if nodeValue := node.GetContractRead(); nodeValue != nil {
		step, err = v.runContractRead(node.Id, nodeValue)
	} else if nodeValue := node.GetContractWrite(); nodeValue != nil {
		step, err = v.runContractWrite(node.Id, nodeValue)
	} else if nodeValue := node.GetLoop(); nodeValue != nil {
		step, err = v.runLoop(node.Id, nodeValue)
	}

	if step != nil {
		return step, err
	}

	if _, ok := v.plans[node.Id]; ok {
		return v.plans[node.Id], err
	}
	return nil, err
}

func (v *VM) runRestApi(stepID string, nodeValue *avsproto.RestAPINode) (*Step, error) {
	p := NewRestProrcessor(v)

	var err error
	inputs := v.CollectInputs()
	executionLog := &avsproto.Execution_Step{
		NodeId: stepID,
	}
	executionLog, err = p.Execute(stepID, nodeValue)

	executionLog.Inputs = inputs
	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)
	return nil, err
}

func (v *VM) runGraphQL(stepID string, node *avsproto.GraphQLQueryNode) (*Step, error) {
	g, err := NewGraphqlQueryProcessor(v, node.Url)
	if err != nil {
		return nil, err
	}
	inputs := v.CollectInputs()
	executionLog, _, err := g.Execute(stepID, node)
	if err != nil {
		v.logger.Error("error execute graphql node", "task_id", v.TaskID, "step", stepID, "url", node.Url, "error", err)
	}
	executionLog.Inputs = inputs
	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)

	return nil, nil
}

func (v *VM) runContractRead(stepID string, node *avsproto.ContractReadNode) (*Step, error) {
	rpcClient, err := ethclient.Dial(v.smartWalletConfig.EthRpcUrl)
	defer func() {
		rpcClient.Close()
	}()

	if err != nil {
		v.logger.Error("error execute contract read node", "task_id", v.TaskID, "step", stepID, "calldata", node.CallData, "error", err)
		return nil, err

	}

	inputs := v.CollectInputs()
	processor := NewContractReadProcessor(v, rpcClient)
	executionLog, err := processor.Execute(stepID, node)

	if err != nil {
		v.logger.Error("error execute contract read node", "task_id", v.TaskID, "step", stepID, "calldata", node.CallData, "error", err)
		return nil, err
	}
	executionLog.Inputs = inputs
	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)

	return nil, nil
}

func (v *VM) runContractWrite(stepID string, node *avsproto.ContractWriteNode) (*Step, error) {
	rpcClient, err := ethclient.Dial(v.smartWalletConfig.EthRpcUrl)
	defer func() {
		rpcClient.Close()
	}()

	inputs := v.CollectInputs()
	processor := NewContractWriteProcessor(v, rpcClient, v.smartWalletConfig, v.TaskOwner)
	executionLog, err := processor.Execute(stepID, node)
	if err != nil {
		v.logger.Error("error execute contract write node", "task_id", v.TaskID, "step", stepID, "calldata", node.CallData, "error", err)
	}
	executionLog.Inputs = inputs
	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)

	return nil, nil
}

func (v *VM) runCustomCode(stepID string, node *avsproto.CustomCodeNode) (*Step, error) {
	r := NewJSProcessor(v)

	inputs := v.CollectInputs()
	executionLog, err := r.Execute(stepID, node)
	if err != nil && v.logger != nil {
		v.logger.Error("error execute JavaScript code", "task_id", v.TaskID, "step", stepID, "error", err)
	}
	executionLog.Inputs = inputs
	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)

	return nil, err
}

func (v *VM) runBranch(stepID string, node *avsproto.BranchNode) (*Step, error) {
	processor := NewBranchProcessor(v)

	inputs := v.CollectInputs()
	executionLog, err := processor.Execute(stepID, node)
	executionLog.Inputs = inputs
	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)

	// In branch node we first need to evaluate the condtion to find the outcome, after find the outcome we need to execute that node
	// the output of a branch node is the node id to jump to
	if err == nil {
		if outputData := executionLog.GetBranch(); outputData != nil {
			branchOutComeID := outputData.GetConditionId()

			outcome, ok := v.plans[branchOutComeID]
			if !ok {
				return nil, fmt.Errorf("branch resolved to node %s but not found in node list", executionLog.OutputData)
			}
			outcomeNodes := outcome.Next
			if len(outcomeNodes) >= 0 {
				for _, nodeID := range outcomeNodes {
					// TODO: track stack too deepth and abort
					node := v.TaskNodes[nodeID]
					jump, err := v.executeNode(node)
					if jump != nil {
						return jump, err
					} else {
						return v.plans[nodeID], err
					}
				}
			}
		}
	}

	return nil, err
}

func (v *VM) runLoop(stepID string, node *avsproto.LoopNode) (*Step, error) {
	processor := NewLoopProcessor(v)

	inputs := v.CollectInputs()
	executionLog, err := processor.Execute(stepID, node)
	if err != nil {
		v.logger.Error("error execute loop node", "task_id", v.TaskID, "step", stepID, "error", err)
	}
	executionLog.Inputs = inputs
	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)

	return nil, err
}

// preprocessText processes any text within {{ }} using goja JavaScript engine
// It returns the processed text with all {{ }} expressions evaluated
// If there's an invalid syntax, it returns the original text
func (v *VM) preprocessText(text string) string {
	// Quick return if no template syntax found
	if !strings.Contains(text, "{{") || !strings.Contains(text, "}}") {
		return text
	}

	// Initialize goja runtime using the new constructor
	jsvm := NewGojaVM()

	v.vars.Range(func(key, value interface{}) bool {
		keyStr, ok := key.(string)
		if !ok {
			return true
		}
		
		if err := jsvm.Set(keyStr, value); err != nil {
			if v.logger != nil {
				v.logger.Error("failed to set variable in JS VM", "key", keyStr, "error", err)
			}
		}
		return true
	})

	// Find all {{ }} expressions
	result := text
	currentIteration := 0

	for currentIteration < VMMaxPreprocessIterations {
		start := strings.Index(result, "{{")
		if start == -1 {
			break
		}

		// Find the next closing brackets after start
		end := strings.Index(result[start:], "}}")
		if end == -1 {
			break
		}
		end += start

		// Check for nested opening/closing brackets between start and end
		if strings.Index(result[start+2:end], "{{") >= 0 || strings.Index(result[start+2:end], "}}") >= 0 {
			// We don't support nested expressions so we will just set this to blank, similar to how Retool does
			result = result[:start] + result[end+2:]
			continue
		}

		// Extract the expression
		expr := strings.TrimSpace(result[start+2 : end])
		if expr == "" {
			// Skip empty expressions
			result = result[:start] + result[end+2:]
			continue
		}

		// Evaluate the expression using goja, notice that we wrap in an IIFE to prevent variable leakage
		// We are re-using the same goja instance
		script := fmt.Sprintf(`(() => { return %s; })()`, expr)

		evaluated, err := jsvm.RunString(script)
		if v.logger != nil {
			v.logger.Debug("evaludate pre-processor script", "task_id", v.GetTaskId(), "script", script, "result", evaluated)
		}
		if err != nil {
			// If there's an error, move past this opening bracket and continue
			result = result[:start] + result[end+2:]
			currentIteration++
			continue
		}

		// Replace the expression with its evaluated result
		exportedValue := evaluated.Export()
		var replacement string

		if t, ok := exportedValue.(time.Time); ok {
			// Always format time.Time to a consistent UTC string
			replacement = t.In(time.UTC).Format("2006-01-02 15:04:05.000 +0000 UTC")
		} else if _, ok := exportedValue.(map[string]interface{}); ok {
			// In Golang, it's better because it can return the actually object data. But in JavaScript, it will return "[object Object]",
			// We're mimicking the behavior of Retool here to follow the script gotcha.
			// In real of userness the golang might be useful for debugging because it's will return the actual object data, eg `map[id:123 message:test]`
			// but at the same time, map ins't  concept in JavaScript, so end user might get confused
			replacement = "[object Object]"
		} else {
			replacement = fmt.Sprintf("%v", exportedValue)
		}

		result = result[:start] + replacement + result[end+2:]
		currentIteration++
	}

	return result
}

func (v *VM) CollectInputs() map[string]string {
	inputs := make(map[string]string)
	
	v.vars.Range(func(key, value interface{}) bool {
		keyStr, ok := key.(string)
		if !ok {
			return true
		}
		
		if slices.Contains(macros.MacroFuncs, keyStr) {
			return true
		}
		
		valueStr := ""
		if value != nil {
			valueStr = fmt.Sprintf("%v", value)
		}
		
		varname := keyStr
		if varname == "apContext" {
			varname = "apContext.configVars"
		} else {
			varname = fmt.Sprintf("%s.data", varname)
		}
		
		inputs[varname] = valueStr
		return true
	})
	
	return inputs
}

func (v *VM) GetTaskId() string {
	if v.task != nil && v.task.Task != nil {
		return v.task.Id
	}

	return ""
}

// RunNodeWithInputs executes a single task node within the VM using the provided input variables.
//
// Parameters:
//   - node: The task node to be executed. This should be a valid instance of avsproto.TaskNode.
//   - inputVariables: A map of variable names to their corresponding values, which will be used as inputs
//     during the execution of the task node.
//
// Returns:
//   - *avsproto.Execution_Step: An object representing the result of the node execution, including details
//     such as the node ID, success status, and inputs used.
//   - error: An error object if the execution fails, or nil if it succeeds.
//
// Use case:
// This method is typically used to execute a single node in isolation, such as for debugging or testing
// purposes. It creates a temporary VM instance to execute the node and collects the results.
func (v *VM) RunNodeWithInputs(node *avsproto.TaskNode, inputVariables map[string]interface{}) (*avsproto.Execution_Step, error) {
	tempVM := &VM{
		vars:              macros.GetEnvs(make(map[string]interface{})),
		TaskID:            v.TaskID,
		TaskNodes:         map[string]*avsproto.TaskNode{node.Id: node},
		plans:             map[string]*Step{},
		ExecutionLogs:     []*avsproto.Execution_Step{},
		Status:            VMStateReady,
		logger:            v.logger,
		smartWalletConfig: v.smartWalletConfig,
		db:                v.db,
		secrets:           v.secrets, // Preserve secrets from the original VM
	}

	if v.vars != nil && v.vars["apContext"] != nil {
		tempVM.vars["apContext"] = v.vars["apContext"]
	}

	for key, value := range inputVariables {
		tempVM.AddVar(key, value)
	}

	step, err := tempVM.executeNode(node)
	if err != nil {
		return nil, fmt.Errorf("error executing node: %w", err)
	}

	if len(tempVM.ExecutionLogs) > 0 {
		return tempVM.ExecutionLogs[0], nil
	}

	// If no execution logs but we have a step, convert it to an Execution_Step
	if step != nil {
		executionStep := &avsproto.Execution_Step{
			NodeId:  node.Id,
			Success: true, // Default to success since we don't have error information
			Inputs:  tempVM.CollectInputs(),
		}
		return executionStep, nil
	}

	return nil, fmt.Errorf("node execution produced no results for node ID: %s", node.Id)
}

// This is a helper function to create nodes for RunNodeWithInputs when working with triggers or simplified inputs.
func CreateNodeFromType(nodeType string, config map[string]interface{}, nodeID string) (*avsproto.TaskNode, error) {
	if nodeID == "" {
		nodeID = "node_" + ulid.Make().String()
	}

	node := &avsproto.TaskNode{
		Id:   nodeID,
		Name: "Single Node Execution",
	}

	switch nodeType {
	case "blockTrigger":
		node.TaskType = &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{
				Lang: avsproto.CustomCodeLang_JavaScript,
				Source: `
					return { 
						blockNumber: blockNumber 
					};
				`,
			},
		}

	case "restApi":
		url, _ := config["url"].(string)
		method, _ := config["method"].(string)
		body, _ := config["body"].(string)

		headers := make(map[string]string)
		if headersMap, ok := config["headers"].(map[string]interface{}); ok {
			for k, v := range headersMap {
				headers[k] = fmt.Sprintf("%v", v)
			}
		}

		node.TaskType = &avsproto.TaskNode_RestApi{
			RestApi: &avsproto.RestAPINode{
				Url:     url,
				Method:  method,
				Body:    body,
				Headers: headers,
			},
		}

	case "contractRead":
		contractAddress, _ := config["contractAddress"].(string)
		callData, _ := config["callData"].(string)
		contractAbi, _ := config["contractAbi"].(string)

		node.TaskType = &avsproto.TaskNode_ContractRead{
			ContractRead: &avsproto.ContractReadNode{
				ContractAddress: contractAddress,
				CallData:        callData,
				ContractAbi:     contractAbi,
			},
		}

	case "customCode":
		source, _ := config["source"].(string)

		node.TaskType = &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{
				Lang:   avsproto.CustomCodeLang_JavaScript,
				Source: source,
			},
		}

	case "branch":
		conditions := []*avsproto.Condition{}
		if conditionsArray, ok := config["conditions"].([]interface{}); ok {
			for _, c := range conditionsArray {
				if condMap, ok := c.(map[string]interface{}); ok {
					id, _ := condMap["id"].(string)
					condType, _ := condMap["type"].(string)
					expression, _ := condMap["expression"].(string)

					conditions = append(conditions, &avsproto.Condition{
						Id:         id,
						Type:       condType,
						Expression: expression,
					})
				}
			}
		}

		node.TaskType = &avsproto.TaskNode_Branch{
			Branch: &avsproto.BranchNode{
				Conditions: conditions,
			},
		}

	case "filter":
		expression, _ := config["expression"].(string)
		input, _ := config["input"].(string)

		node.TaskType = &avsproto.TaskNode_Filter{
			Filter: &avsproto.FilterNode{
				Expression: expression,
				Input:      input,
			},
		}

	default:
		return nil, fmt.Errorf("unsupported node type: %s", nodeType)
	}

	return node, nil
}
