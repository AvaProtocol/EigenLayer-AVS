package taskengine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/ginkgoch/godash/v2"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/expr-lang/expr"

	"github.com/AvaProtocol/ap-avs/core/taskengine/macros"
	"github.com/AvaProtocol/ap-avs/pkg/erc20"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

type VMState string

const (
	VMStateInitialize = "vm_initialize"
	VMStateCompiled   = "vm_compiled"
	VMStateReady      = "vm_ready"
	VMStateExecuting  = "vm_executing"
	VMStateCompleted  = "vm_completed"

	TriggerEdge = "__TRIGGER__"
)

type Step struct {
	NodeID string
	Next   []string
}

// The VM is the core component that load the node information and execute them, yield finaly result
type VM struct {
	// Input raw task data
	// TaskID can be used to cache compile program
	TaskID    string
	TaskNodes map[string]*avsproto.TaskNode
	TaskEdges []*avsproto.TaskEdge

	// executin logs and result per plans
	ExecutionLogs []*avsproto.Execution_Step

	Status VMState
	mu     *sync.Mutex
	// internal state that is set through out program execution
	vars map[string]any

	plans            map[string]*Step
	entrypoint       string
	instructionCount int64
}

func NewVM() (*VM, error) {
	v := &VM{
		Status:           VMStateInitialize,
		mu:               &sync.Mutex{},
		instructionCount: 0,
	}

	return v, nil
}

func (v *VM) Reset() {
	v.ExecutionLogs = []*avsproto.Execution_Step{}
	v.plans = map[string]*Step{}
	v.entrypoint = ""
	v.Status = VMStateInitialize
	v.instructionCount = 0
}

func NewVMWithData(taskID string, triggerMetadata *avsproto.TriggerMetadata, nodes []*avsproto.TaskNode, edges []*avsproto.TaskEdge) (*VM, error) {
	v := &VM{
		Status:           VMStateInitialize,
		TaskEdges:        edges,
		TaskNodes:        make(map[string]*avsproto.TaskNode),
		plans:            make(map[string]*Step),
		mu:               &sync.Mutex{},
		instructionCount: 0,
	}

	for _, node := range nodes {
		v.TaskNodes[node.Id] = node
	}

	v.vars = macros.GetEnvs(map[string]any{})

	// popular trigger data for trigger variable
	if triggerMetadata != nil && triggerMetadata.LogIndex > 0 && triggerMetadata.TxHash != "" {
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

		v.vars["trigger1"] = map[string]interface{}{
			"data": map[string]interface{}{
				"topics": godash.Map(event.Topics, func(topic common.Hash) string {
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

		if edge.Source != "__TRIGGER__" {
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
		// TODO: refactor into function
		p := NewRestProrcessor()

		// only evaluate string when there is string interpolation
		if nodeValue.Body != "" && strings.Contains(nodeValue.Body, "$") {
			nodeValue2 := &avsproto.RestAPINode{
				Url:     macros.RenderString(nodeValue.Url, macroEnvs),
				Headers: nodeValue.Headers,
				Method:  nodeValue.Method,
				Body:    strings.Clone(nodeValue.Body),
			}
			vm := goja.New()
			// TODO: dynamically set var instead of hardcode the name
			// client would need to send this over
			vm.Set("trigger1", v.vars["trigger1"])

			renderBody, err := vm.RunString(nodeValue.Body)
			if err == nil {
				nodeValue2.Body = renderBody.Export().(string)
			}
			executionLog, err = p.Execute(node.Id, nodeValue2)
		} else {
			executionLog, err = p.Execute(node.Id, nodeValue)
		}

		v.ExecutionLogs = append(v.ExecutionLogs, executionLog)
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
	}

	return executionLog, err
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
	for _, statement := range node.Conditions {
		if strings.EqualFold(statement.Type, "else") {
			// Execute this directly
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

		// now we need to valuate condition
		program, err := expr.Compile(statement.Expression, expr.Env(v.vars), expr.AsBool())
		if err != nil {
			s.Success = false
			s.Error = fmt.Errorf("error compile the statement: %w", err).Error()
			sb.WriteString("error compile expression")
			s.Log = sb.String()
			s.EndAt = time.Now().Unix()
			return s, outcome, fmt.Errorf("error compile the statement: %w", err)
		}
		result, err := expr.Run(program, v.vars)
		if err != nil {
			s.Success = false
			s.Error = fmt.Errorf("error run statement: %w", err).Error()
			sb.WriteString("error run expression")
			s.Log = sb.String()
			s.EndAt = time.Now().Unix()
			return s, outcome, fmt.Errorf("error evaluate the statement: %w", err)
		}

		if result.(bool) == true {
			outcome = fmt.Sprintf("%s.%s", stepID, statement.Id)
			sb.WriteString("\nexpression result to true. follow path ")
			sb.WriteString(outcome)
			// run the node
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
