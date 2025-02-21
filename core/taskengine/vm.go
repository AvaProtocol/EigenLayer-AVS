package taskengine

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/dop251/goja"
	"github.com/samber/lo"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/AvaProtocol/ap-avs/core/taskengine/macros"
	"github.com/AvaProtocol/ap-avs/model"
	"github.com/AvaProtocol/ap-avs/pkg/erc20"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
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
	c.vm.AddVar(c.vm.GetNodeNameAsVar(stepID), map[string]any{
		"data": data,
	})
}

type triggerDataType struct {
	TransferLog *avsproto.Execution_TransferLogOutput
	EvmLog      *avsproto.Execution_EvmLogOutput
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

// The VM is the core component that load the node information and execute them, yield finaly result
type VM struct {
	// Input raw task data
	// TaskID can be used to cache compile program
	TaskID    string
	TaskNodes map[string]*avsproto.TaskNode

	//TaskEdges   []*avsproto.TaskEdge
	//TaskTrigger *avsproto.TaskTrigger
	TaskOwner common.Address

	task              *model.Task
	reason            *avsproto.TriggerReason
	parsedTriggerData *triggerDataType

	// executin logs and result per plans
	ExecutionLogs []*avsproto.Execution_Step

	Status VMState
	mu     *sync.Mutex
	// internal state that is set through out program execution
	vars map[string]any

	// hold the name->value of loaded secret for this workflow
	secrets map[string]string

	plans            map[string]*Step
	entrypoint       string
	instructionCount int64

	smartWalletConfig *config.SmartWalletConfig
	logger            sdklogging.Logger
}

func NewVM() *VM {
	v := &VM{
		Status:           VMStateInitialize,
		mu:               &sync.Mutex{},
		instructionCount: 0,
		secrets:          map[string]string{},
		vars:             macros.GetEnvs(map[string]any{}),
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

func (v *VM) GetTriggerNameAsVar() string {
	// Replace invalid characters with _
	re := regexp.MustCompile(`[^a-zA-Z0-9_$]`)
	name := v.task.Trigger.Name
	standardized := re.ReplaceAllString(name, "_")

	// Ensure the first character is valid
	if len(standardized) == 0 || !regexp.MustCompile(`^[a-zA-Z_$]`).MatchString(standardized[:1]) {
		standardized = "_" + standardized
	}

	return standardized
}

func (v *VM) GetNodeNameAsVar(nodeID string) string {
	// Replace invalid characters with _
	re := regexp.MustCompile(`[^a-zA-Z0-9_$]`)
	name := v.TaskNodes[nodeID].Name
	if name == "" {
		name = nodeID
	}
	standardized := re.ReplaceAllString(v.TaskNodes[nodeID].Name, "_")

	// Ensure the first character is valid
	if len(standardized) == 0 || !regexp.MustCompile(`^[a-zA-Z_$]`).MatchString(standardized[:1]) {
		standardized = "_" + standardized
	}

	return standardized
}

func NewVMWithData(task *model.Task, reason *avsproto.TriggerReason, smartWalletConfig *config.SmartWalletConfig, secrets map[string]string) (*VM, error) {
	v := &VM{
		Status: VMStateInitialize,
		//TaskEdges:         task.Edges,
		//TaskTrigger:       task.Trigger,
		TaskNodes: make(map[string]*avsproto.TaskNode),
		TaskOwner: common.HexToAddress(task.Owner),

		plans:             make(map[string]*Step),
		mu:                &sync.Mutex{},
		instructionCount:  0,
		secrets:           secrets,
		smartWalletConfig: smartWalletConfig,
		reason:            reason,
		task:              task,
		parsedTriggerData: &triggerDataType{},
	}

	for _, node := range task.Nodes {
		v.TaskNodes[node.Id] = node
	}

	v.vars = macros.GetEnvs(map[string]any{})
	triggerVarName := v.GetTriggerNameAsVar()
	// popular trigger data for trigger variable
	if reason != nil {
		v.vars[triggerVarName] = map[string]any{
			"data": map[string]any{},
		}

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

				v.vars[triggerVarName].(map[string]any)["data"] = map[string]any{
					"topics": lo.Map(event.Topics, func(topic common.Hash, _ int) string {
						return "0x" + strings.ToLower(strings.TrimLeft(topic.String(), "0x0"))
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

				v.parsedTriggerData.TransferLog = &avsproto.Execution_TransferLogOutput{
					TokenName:        tokenMetadata.Name,
					TokenSymbol:      tokenMetadata.Symbol,
					TokenDecimals:    uint32(tokenMetadata.Decimals),
					TransactionHash:  event.TxHash.Hex(),
					Address:          event.Address.Hex(),
					BlockNumber:      event.BlockNumber,
					BlockTimestamp:   blockHeader.Time,
					FromAddress:      parseTransfer.From.String(),
					ToAddress:        parseTransfer.To.String(),
					Value:            parseTransfer.Value.String(),
					ValueFormatted:   formattedValue,
					TransactionIndex: uint32(event.TxIndex),
				}
			} else {
				v.parsedTriggerData.EvmLog = &avsproto.Execution_EvmLogOutput{
					Address:          event.Address.Hex(),
					BlockHash:        event.BlockHash.Hex(),
					BlockNumber:      event.BlockNumber,
					Data:             "0x" + common.Bytes2Hex(event.Data),
					Index:            uint32(event.Index),
					TransactionHash:  event.TxHash.Hex(),
					TransactionIndex: uint32(event.TxIndex),
					Topics: lo.Map(event.Topics, func(topic common.Hash, _ int) string {
						return "0x" + strings.ToLower(strings.TrimLeft(topic.String(), "0x0"))
					}),
				}
			}
		}

		if reason.BlockNumber > 0 {
			v.vars[triggerVarName].(map[string]any)["data"].(map[string]any)["block_number"] = reason.BlockNumber
			v.parsedTriggerData.Block = &avsproto.Execution_BlockOutput{
				BlockNumber: uint64(reason.BlockNumber),
			}
		}

		if reason.Epoch > 0 {
			v.vars[triggerVarName].(map[string]any)["data"].(map[string]any)["epoch"] = reason.Epoch
			v.parsedTriggerData.Time = &avsproto.Execution_TimeOutput{
				Epoch: uint64(reason.Epoch),
			}
		}
	}

	v.vars["apContext"] = map[string]map[string]string{
		"configVars": secrets,
	}

	return v, nil
}

func (v *VM) CreateSandbox() error {
	return nil
}

func (v *VM) AddVar(key string, value any) {
	v.vars[key] = value
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
		return fmt.Errorf(InvalidEntrypoint)
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
		node := v.TaskNodes[currentStep.NodeID]

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
	}

	if step != nil {
		return step, err
	}

	return v.plans[node.Id], err
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
	if err != nil {
		v.logger.Error("error execute JavaScript code", "task_id", v.TaskID, "step", stepID, "error", err)
	}
	executionLog.Inputs = inputs
	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)

	return nil, nil
}

func (v *VM) runBranch(stepID string, node *avsproto.BranchNode) (*Step, error) {
	processor := NewBranchProcessor(v)

	inputs := v.CollectInputs()
	executionLog, err := processor.Execute(stepID, node)
	executionLog.Inputs = inputs
	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)

	// In branch node we first need to evaluate the condtion to find the outcome, after find the outcome we need to execute that node
	// the output of a branch node is the node id to jump to
	if err == nil && executionLog.OutputData != "" {
		outcome, ok := v.plans[executionLog.OutputData]
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

	// Initialize goja runtime
	jsvm := goja.New()

	for key, value := range v.vars {
		jsvm.Set(key, value)
	}

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
		if err != nil {
			// If there's an error, move past this opening bracket and continue
			result = result[:start] + result[end+2:]
			currentIteration++
			continue
		}

		// Replace the expression with its evaluated result
		replacement := fmt.Sprintf("%v", evaluated.Export())
		result = result[:start] + replacement + result[end+2:]
		currentIteration++
	}

	return result
}

func (v *VM) CollectInputs() []string {
	inputs := []string{}
	for k, _ := range v.vars {
		if slices.Contains(macros.MacroFuncs, k) {
			continue
		}

		varname := k
		if varname == "apContext" {
			varname = "apContext.configVars"
		} else {
			varname = fmt.Sprintf("%s.data", varname)
		}
		inputs = append(inputs, varname)
	}

	return inputs
}
