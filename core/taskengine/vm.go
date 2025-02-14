package taskengine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

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

// The VM is the core component that load the node information and execute them, yield finaly result
type VM struct {
	// Input raw task data
	// TaskID can be used to cache compile program
	TaskID      string
	TaskNodes   map[string]*avsproto.TaskNode
	TaskEdges   []*avsproto.TaskEdge
	TaskTrigger *avsproto.TaskTrigger
	TaskOwner   common.Address

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
	name := v.TaskTrigger.Name
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

func NewVMWithData(task *model.Task, triggerMetadata *avsproto.TriggerMetadata, smartWalletConfig *config.SmartWalletConfig, secrets map[string]string) (*VM, error) {
	v := &VM{
		Status:            VMStateInitialize,
		TaskEdges:         task.Edges,
		TaskNodes:         make(map[string]*avsproto.TaskNode),
		TaskTrigger:       task.Trigger,
		TaskOwner:         common.HexToAddress(task.Owner),
		plans:             make(map[string]*Step),
		mu:                &sync.Mutex{},
		instructionCount:  0,
		secrets:           secrets,
		smartWalletConfig: smartWalletConfig,
	}

	for _, node := range task.Nodes {
		v.TaskNodes[node.Id] = node
	}

	v.vars = macros.GetEnvs(map[string]any{})
	triggerVarName := v.GetTriggerNameAsVar()
	// popular trigger data for trigger variable
	if triggerMetadata != nil {
		if triggerMetadata.LogIndex > 0 && triggerMetadata.TxHash != "" {
			// if it contains event, we need to fetch and pop
			receipt, err := rpcConn.TransactionReceipt(context.Background(), common.HexToHash(triggerMetadata.TxHash))
			if err != nil {
				return nil, err
			}

			var event *types.Log
			//event := receipt.Logs[triggerMetadata.LogIndex]

			for _, l := range receipt.Logs {
				if uint64(l.Index) == triggerMetadata.LogIndex {
					event = l
				}
			}

			if event == nil {
				return nil, fmt.Errorf("tx %s doesn't content event %d", triggerMetadata.TxHash, triggerMetadata.LogIndex)
			}

			tokenMetadata, err := GetMetadataForTransfer(event)
			ef, err := erc20.NewErc20(event.Address, nil)

			blockHeader, err := GetBlock(event.BlockNumber)
			if err != nil {
				return nil, fmt.Errorf("RPC error getting block header. Retry: %w", err)
			}

			parseTransfer, err := ef.ParseTransfer(*event)
			formattedValue := ToDecimal(parseTransfer.Value, int(tokenMetadata.Decimals)).String()

			v.vars[triggerVarName] = map[string]interface{}{
				"data": map[string]interface{}{
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
				},
			}
		}

		if triggerMetadata.Epoch > 0 {
			v.vars[triggerVarName] = map[string]any{
				"epoch": triggerMetadata.Epoch,
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
	triggerId := v.TaskTrigger.Id
	for _, edge := range v.TaskEdges {
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

		_, err := v.executeNode(node)
		if err != nil {
			// abort execution as soon as a node raise error
			return err
		}

		if len(currentStep.Next) == 0 {
			break
		}

		// TODO: Support multiple next
		currentStep = v.plans[currentStep.Next[0]]
	}

	return nil
}

func (v *VM) executeNode(node *avsproto.TaskNode) (*avsproto.Execution_Step, error) {
	v.instructionCount += 1

	var err error
	executionLog := &avsproto.Execution_Step{
		NodeId: node.Id,
	}

	if nodeValue := node.GetRestApi(); nodeValue != nil {
		executionLog, err = v.runRestApi(node.Id, nodeValue)
	} else if nodeValue := node.GetBranch(); nodeValue != nil {
		outcomeID := ""
		executionLog, outcomeID, err = v.runBranch(node.Id, nodeValue)
		v.ExecutionLogs = append(v.ExecutionLogs, executionLog)
		if err == nil && outcomeID != "" {
			outcome, ok := v.plans[outcomeID]
			if !ok {
				return nil, fmt.Errorf("branch resolved to node %s but not found in node list", outcomeID)
			}
			outcomeNodes := outcome.Next
			if len(outcomeNodes) >= 0 {
				for _, nodeID := range outcomeNodes {
					// TODO: track stack too deepth and abort
					node := v.TaskNodes[nodeID]
					if executionLog, err = v.executeNode(node); err != nil {
						return executionLog, err
					}
				}
			}
		}
	} else if nodeValue := node.GetGraphqlQuery(); nodeValue != nil {
		executionLog, err = v.runGraphQL(node.Id, nodeValue)
	} else if nodeValue := node.GetCustomCode(); nodeValue != nil {
		executionLog, err = v.runCustomCode(node.Id, nodeValue)
	} else if nodeValue := node.GetContractRead(); nodeValue != nil {
		executionLog, err = v.runContractRead(node.Id, nodeValue)
	} else if nodeValue := node.GetContractWrite(); nodeValue != nil {
		executionLog, err = v.runContractWrite(node.Id, nodeValue)
	}

	return executionLog, err
}

func (v *VM) runRestApi(stepID string, nodeValue *avsproto.RestAPINode) (*avsproto.Execution_Step, error) {
	p := NewRestProrcessor(v)

	var err error
	executionLog := &avsproto.Execution_Step{
		NodeId: stepID,
	}
	executionLog, err = p.Execute(stepID, nodeValue)

	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)
	return executionLog, err
}

func (v *VM) runGraphQL(stepID string, node *avsproto.GraphQLQueryNode) (*avsproto.Execution_Step, error) {
	g, err := NewGraphqlQueryProcessor(v, node.Url)
	if err != nil {
		return nil, err
	}
	executionLog, _, err := g.Execute(stepID, node)
	if err != nil {
		v.logger.Error("error execute graphql node", "task_id", v.TaskID, "step", stepID, "url", node.Url, "error", err)
	}
	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)

	return executionLog, nil
}

func (v *VM) runContractRead(stepID string, node *avsproto.ContractReadNode) (*avsproto.Execution_Step, error) {
	rpcClient, err := ethclient.Dial(v.smartWalletConfig.EthRpcUrl)
	defer func() {
		rpcClient.Close()
	}()

	if err != nil {
		v.logger.Error("error execute contract read node", "task_id", v.TaskID, "step", stepID, "calldata", node.CallData, "error", err)
		return nil, err

	}

	processor := NewContractReadProcessor(v, rpcClient)
	executionLog, err := processor.Execute(stepID, node)

	if err != nil {
		v.logger.Error("error execute contract read node", "task_id", v.TaskID, "step", stepID, "calldata", node.CallData, "error", err)
		return nil, err
	}
	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)

	return executionLog, nil
}

func (v *VM) runContractWrite(stepID string, node *avsproto.ContractWriteNode) (*avsproto.Execution_Step, error) {
	rpcClient, err := ethclient.Dial(v.smartWalletConfig.EthRpcUrl)
	defer func() {
		rpcClient.Close()
	}()

	processor := NewContractWriteProcessor(v, rpcClient, v.smartWalletConfig, v.TaskOwner)
	executionLog, err := processor.Execute(stepID, node)
	if err != nil {
		v.logger.Error("error execute contract write node", "task_id", v.TaskID, "step", stepID, "calldata", node.CallData, "error", err)
	}
	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)

	return executionLog, nil
}

func (v *VM) runCustomCode(stepID string, node *avsproto.CustomCodeNode) (*avsproto.Execution_Step, error) {
	r := NewJSProcessor(v)
	executionLog, err := r.Execute(stepID, node)
	if err != nil {
		v.logger.Error("error execute JavaScript code", "task_id", v.TaskID, "step", stepID, "error", err)
	}
	v.ExecutionLogs = append(v.ExecutionLogs, executionLog)

	return executionLog, nil
}

func (v *VM) runBranch(stepID string, node *avsproto.BranchNode) (*avsproto.Execution_Step, string, error) {
	t0 := time.Now()
	s := &avsproto.Execution_Step{
		NodeId:     stepID,
		OutputData: "",
		Log:        "",
		Error:      "",
		Success:    true,
		StartAt:    t0.Unix(),
	}

	var sb strings.Builder
	sb.WriteString("Execute Branch: ")
	sb.WriteString(stepID)
	outcome := ""

	// Initialize goja runtime
	jsvm := goja.New()

	// Set variables in the JS environment. The value is wrapped into a data, follow a similar approach by other nocode provider
	// even though we arent necessarily need to do this
	for key, value := range v.vars {
		jsvm.Set(key, value)
	}

	for _, statement := range node.Conditions {
		if strings.EqualFold(statement.Type, "else") {
			outcome = fmt.Sprintf("%s.%s", stepID, statement.Id)
			sb.WriteString("\n")
			sb.WriteString(time.Now().String())
			sb.WriteString("evaluate else, follow else path")
			sb.WriteString(outcome)
			s.Log = sb.String()
			s.OutputData = outcome
			return s, outcome, nil
		}

		sb.WriteString(fmt.Sprintf("\n%s evaluate condition: %s expression: `%s`", time.Now(), statement.Id, statement.Expression))

		// Evaluate the condition using goja, notice how we wrap into a function to prevent the value is leak across goja run
		script := fmt.Sprintf(`(() => %s )()`, strings.Trim(statement.Expression, "\n \t"))

		result, err := jsvm.RunString(script)
		if err != nil {
			s.Success = false
			s.Error = fmt.Errorf("error evaluating the statement: %w", err).Error()
			sb.WriteString("error evaluating expression")
			s.Log = sb.String()
			s.EndAt = time.Now().Unix()
			return s, outcome, fmt.Errorf("error evaluating the statement: %w", err)
		}

		branchResult, ok := result.Export().(bool)
		if !ok {
			s.Success = false
			s.Error = fmt.Errorf("error evaluating the statement: %w", err).Error()
			sb.WriteString("error evaluating expression")
			s.Log = sb.String()
			s.EndAt = time.Now().Unix()
			return s, outcome, fmt.Errorf("error evaluating the statement: %w", err)
		}

		if branchResult {
			outcome = fmt.Sprintf("%s.%s", stepID, statement.Id)
			sb.WriteString("\nexpression result to true. follow path ")
			sb.WriteString(outcome)
			s.Log = sb.String()
			s.OutputData = outcome
			s.EndAt = time.Now().Unix()
			return s, outcome, nil
		}
	}

	sb.WriteString("\nno condition matched. halt execution")
	s.Log = sb.String()
	s.EndAt = time.Now().Unix()
	return s, "", nil
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
