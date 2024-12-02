package taskengine

import (
	"fmt"
	"log"
	"sync"

	"github.com/AvaProtocol/ap-avs/core/taskengine/runner"
	"github.com/AvaProtocol/ap-avs/core/taskengine/types"
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
	TaskID    types.TaskID
	TaskNodes map[string]*avsproto.TaskNode
	TaskEdges []*avsproto.TaskEdge

	// executin logs and result per steps
	ExecutionLogs []*types.StepExecution

	Status VMState
	mu     *sync.Mutex
	// internal state that is set through out program execution
	vars map[string]any

	steps      map[string]*Step
	entrypoint string
}

func NewVM() (*VM, error) {
	v := &VM{
		Status: VMStateInitialize,
		mu:     &sync.Mutex{},
	}

	return v, nil
}

func NewVMWithData(taskID types.TaskID, nodes []*avsproto.TaskNode, edges []*avsproto.TaskEdge) (*VM, error) {
	v := &VM{
		Status:    VMStateInitialize,
		TaskEdges: edges,
		TaskNodes: make(map[string]*avsproto.TaskNode),
		steps:     make(map[string]*Step),
		mu:        &sync.Mutex{},
	}

	for _, node := range nodes {
		v.TaskNodes[node.Id] = node
	}

	return v, nil
}

func (v *VM) CreateSandbox() error {
	return nil
}

func (v *VM) Compile() error {
	// Find the initial trigger
	for _, edge := range v.TaskEdges {
		if edge.Source != "__TRIGGER__" {
			if _, ok := v.steps[edge.Source]; ok {
				v.steps[edge.Source].Next = append(v.steps[edge.Source].Next, edge.Target)
			} else {
				v.steps[edge.Source] = &Step{
					NodeID: edge.Source,
					Next:   []string{edge.Target},
				}
			}
		} else {
			// There should be only one entrypoint
			v.steps[edge.Target] = &Step{
				NodeID: edge.Target,
				Next:   []string{},
			}
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
	defer func() {
		v.mu.Unlock()
	}()
	if len(v.steps) == 0 {
		return fmt.Errorf("internal error: not compiled")
	}

	log.Println("step", v.steps, v.entrypoint)

	// TODO Setup a timeout context
	currentStep := v.steps[v.entrypoint]
	for currentStep != nil {
		node := v.TaskNodes[currentStep.NodeID]
		if nodeValue := node.GetRestApi(); nodeValue != nil {
			p := runner.NewRestProrcessor()
			if executionLog, err := p.Execute(currentStep.NodeID, nodeValue); err != nil {
				// abort execution as soon as a node raise error
				return err
			} else {
				v.ExecutionLogs = append(v.ExecutionLogs, executionLog)
			}
		}

		log.Println("exeuction log", v.ExecutionLogs[0].Logs[0], v.ExecutionLogs[0].Result.(string))

		if len(currentStep.Next) == 0 {
			break
		}

		// TODO: Support multiple next
		currentStep = v.steps[currentStep.Next[0]]
	}

	return nil
}
