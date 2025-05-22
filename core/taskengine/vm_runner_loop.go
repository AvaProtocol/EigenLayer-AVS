package taskengine

import (
	"fmt"
	"strings"
	"sync"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/structpb"
)

type LoopProcessor struct {
	*CommonProcessor
}

func NewLoopProcessor(vm *VM) *LoopProcessor {
	return &LoopProcessor{
		CommonProcessor: &CommonProcessor{
			vm: vm,
		},
	}
}

func (r *LoopProcessor) Execute(stepID string, node *avsproto.LoopNode) (*avsproto.Execution_Step, error) {
	t0 := time.Now().UnixMilli()
	s := &avsproto.Execution_Step{
		NodeId:     stepID,
		OutputData: nil,
		Log:        "",
		Error:      "",
		Success:    true,
		StartAt:    t0,
	}

	var log strings.Builder
	log.WriteString(fmt.Sprintf("Start loop execution for input %s at %s", node.Input, time.Now()))

	inputVar, exists := r.vm.vars[node.Input]
	if !exists {
		err := fmt.Errorf("input variable %s not found", node.Input)
		s.Success = false
		s.Error = err.Error()
		s.EndAt = time.Now().UnixMilli()
		log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		s.Log = log.String()
		return s, err
	}

	inputArray, ok := inputVar.([]interface{})
	if !ok {
		if varMap, isMap := inputVar.(map[string]interface{}); isMap {
			if data, hasData := varMap["data"]; hasData {
				if dataArr, isArr := data.([]interface{}); isArr {
					inputArray = dataArr
					ok = true
				}
			}
		}
	}

	if !ok {
		err := fmt.Errorf("input %s is not an array", node.Input)
		s.Success = false
		s.Error = err.Error()
		s.EndAt = time.Now().UnixMilli()
		log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		s.Log = log.String()
		return s, err
	}

	log.WriteString(fmt.Sprintf("\nIterating through %d items", len(inputArray)))

	runInParallel := true
	if node.GetContractWrite() != nil {
		runInParallel = false
		log.WriteString("\nRunning iterations sequentially due to contract write operation")
	}

	results := make([]interface{}, 0, len(inputArray))
	success := true
	var firstError error

	if runInParallel {
		var wg sync.WaitGroup
		resultsMutex := &sync.Mutex{}
		errorsMutex := &sync.Mutex{}

		for i, item := range inputArray {
			wg.Add(1)
			go func(index int, value interface{}) {
				defer wg.Done()
				
				r.vm.vars[node.IterKey] = index
				r.vm.vars[node.IterVal] = value
				
				result, err := r.executeNestedNode(node, fmt.Sprintf("%s.%d", stepID, index))
				
				resultsMutex.Lock()
				results = append(results, result)
				resultsMutex.Unlock()
				
				if err != nil {
					errorsMutex.Lock()
					if firstError == nil {
						firstError = err
						success = false
					}
					errorsMutex.Unlock()
					log.WriteString(fmt.Sprintf("\nError in iteration %d: %s", index, err.Error()))
				}
			}(i, item)
		}
		
		wg.Wait()
	} else {
		for i, item := range inputArray {
			r.vm.vars[node.IterKey] = i
			r.vm.vars[node.IterVal] = item
			
			result, err := r.executeNestedNode(node, fmt.Sprintf("%s.%d", stepID, i))
			results = append(results, result)
			
			if err != nil {
				success = false
				if firstError == nil {
					firstError = err
				}
				log.WriteString(fmt.Sprintf("\nError in iteration %d: %s", i, err.Error()))
			}
		}
	}

	r.SetOutputVarForStep(stepID, results)
	
	value, err := structpb.NewValue(results)
	if err == nil {
		s.OutputData = &avsproto.Execution_Step_Loop{
			Loop: &avsproto.LoopNode_Output{
				Data: value.GetStringValue(),
			},
		}
	}

	log.WriteString(fmt.Sprintf("\nCompleted loop execution at %s", time.Now()))
	s.Log = log.String()
	s.Success = success
	s.EndAt = time.Now().UnixMilli()
	
	if !success && firstError != nil {
		s.Error = firstError.Error()
		return s, firstError
	}
	
	return s, nil
}

func (r *LoopProcessor) executeNestedNode(node *avsproto.LoopNode, iterationStepID string) (interface{}, error) {
	var nestedNode *avsproto.TaskNode
	var result interface{}
	var err error

	nodeName := fmt.Sprintf("loop_iteration_%s", iterationStepID)

	if ethTransfer := node.GetEthTransfer(); ethTransfer != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: ethTransfer},
		}
	} else if contractWrite := node.GetContractWrite(); contractWrite != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: contractWrite},
		}
	} else if contractRead := node.GetContractRead(); contractRead != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_ContractRead{ContractRead: contractRead},
		}
	} else if graphqlQuery := node.GetGraphqlDataQuery(); graphqlQuery != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_GraphqlQuery{GraphqlQuery: graphqlQuery},
		}
	} else if restApi := node.GetRestApi(); restApi != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_RestApi{RestApi: restApi},
		}
	} else if customCode := node.GetCustomCode(); customCode != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_CustomCode{CustomCode: customCode},
		}
	} else {
		return nil, fmt.Errorf("no nested node specified in loop")
	}
	
	r.vm.TaskNodes[iterationStepID] = nestedNode

	_, err = r.vm.executeNode(nestedNode)
	if err != nil {
		return nil, err
	}

	varName := r.vm.GetNodeNameAsVar(iterationStepID)
	if varName != "" {
		if varMap, ok := r.vm.vars[varName].(map[string]interface{}); ok {
			if data, hasData := varMap["data"]; hasData {
				result = data
			}
		}
	}

	return result, nil
}
