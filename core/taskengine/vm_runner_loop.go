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

	r.vm.mu.Lock()
	inputVar, exists := r.vm.vars[node.Input]
	r.vm.mu.Unlock()

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
		errorsMutex := &sync.Mutex{}

		// Pre-allocate results slice with fixed size to maintain order
		results = make([]interface{}, len(inputArray))

		for i, item := range inputArray {
			wg.Add(1)
			go func(index int, valueParam interface{}) {
				defer wg.Done()

				iterInputs := map[string]interface{}{}
				if node.IterKey != "" {
					iterInputs[node.IterKey] = index
				}
				iterInputs[node.IterVal] = valueParam

				iterationStepID := fmt.Sprintf("%s.%d", stepID, index)
				resultData, err := r.executeNestedNode(node, iterationStepID, iterInputs)

				// Store result at the correct index to maintain order
				results[index] = resultData

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
		results = make([]interface{}, len(inputArray))
		for i, item := range inputArray {
			iterInputs := map[string]interface{}{}
			if node.IterKey != "" {
				iterInputs[node.IterKey] = i
			}
			iterInputs[node.IterVal] = item

			iterationStepID := fmt.Sprintf("%s.%d", stepID, i)
			resultData, err := r.executeNestedNode(node, iterationStepID, iterInputs)
			results[i] = resultData

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
	} else {
		log.WriteString(fmt.Sprintf("\nError converting results to structpb.Value: %s", err.Error()))
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

func (r *LoopProcessor) executeNestedNode(loopNodeDef *avsproto.LoopNode, iterationStepID string, iterInputs map[string]interface{}) (interface{}, error) {
	var nestedNode *avsproto.TaskNode

	nodeName := fmt.Sprintf("loop_iteration_%s", iterationStepID)

	if ethTransfer := loopNodeDef.GetEthTransfer(); ethTransfer != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: ethTransfer},
		}
	} else if contractWrite := loopNodeDef.GetContractWrite(); contractWrite != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: contractWrite},
		}
	} else if contractRead := loopNodeDef.GetContractRead(); contractRead != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_ContractRead{ContractRead: contractRead},
		}
	} else if graphqlQuery := loopNodeDef.GetGraphqlDataQuery(); graphqlQuery != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_GraphqlQuery{GraphqlQuery: graphqlQuery},
		}
	} else if restApi := loopNodeDef.GetRestApi(); restApi != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_RestApi{RestApi: restApi},
		}
	} else if customCode := loopNodeDef.GetCustomCode(); customCode != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_CustomCode{CustomCode: customCode},
		}
	} else {
		return nil, fmt.Errorf("no nested node specified in loop")
	}

	executionStep, err := r.vm.RunNodeWithInputs(nestedNode, iterInputs)
	if err != nil {
		return nil, err
	}

	if executionStep == nil || !executionStep.Success {
		if executionStep != nil && executionStep.Error != "" {
			return nil, fmt.Errorf("nested node execution failed: %s", executionStep.Error)
		}
		return nil, fmt.Errorf("nested node execution failed without specific error")
	}

	if customCodeOutput := executionStep.GetCustomCode(); customCodeOutput != nil {
		if customCodeOutput.Data != nil {
			return customCodeOutput.Data.AsInterface(), nil
		}
		return nil, nil
	}

	return nil, nil
}
