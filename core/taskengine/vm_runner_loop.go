package taskengine

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
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
	// Use shared function to create execution step
	s := createNodeExecutionStep(stepID, avsproto.NodeType_NODE_TYPE_LOOP, r.vm)

	var log strings.Builder
	log.WriteString(fmt.Sprintf("Start loop execution at %s", time.Now()))

	// Debug logging
	if r.vm.logger != nil {
		r.vm.logger.Info("🔄 LoopProcessor.Execute: Starting",
			"stepID", stepID,
			"node_exists", node != nil,
			"execution_step_created", s != nil,
			"execution_step_config_exists", s != nil && s.Config != nil)
	}

	// Get configuration from node.Config (new architecture)
	if node.Config == nil {
		err := fmt.Errorf("LoopNode Config is nil")
		log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		if r.vm.logger != nil {
			r.vm.logger.Error("🚫 LoopProcessor.Execute: Config is nil", "stepID", stepID)
		}
		finalizeExecutionStep(s, false, err.Error(), log.String())
		return s, err
	}

	inputNodeName := node.Config.InputNodeName
	iterVal := node.Config.IterVal
	iterKey := node.Config.IterKey
	executionMode := node.Config.ExecutionMode

	if r.vm.logger != nil {
		r.vm.logger.Info("🔄 LoopProcessor.Execute: Config extracted",
			"stepID", stepID,
			"inputNodeName", inputNodeName,
			"iterVal", iterVal,
			"iterKey", iterKey,
			"executionMode", executionMode.String())
	}

	if inputNodeName == "" || iterVal == "" {
		err := fmt.Errorf("missing required configuration: inputNodeName and iterVal are required")
		log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		if r.vm.logger != nil {
			r.vm.logger.Error("🚫 LoopProcessor.Execute: Missing required config", "stepID", stepID, "inputNodeName", inputNodeName, "iterVal", iterVal)
		}
		finalizeExecutionStep(s, false, err.Error(), log.String())
		return s, err
	}

	// In immediate execution, inputNodeName might be a direct variable name, not a node ID
	// Try both approaches: first as node ID (workflow execution), then as direct variable name (immediate execution)
	var inputVarName string
	var inputVar interface{}
	var exists bool

	// First try: resolve as node ID (workflow execution)
	inputVarName = r.vm.GetNodeNameAsVar(inputNodeName)
	r.vm.mu.Lock()
	inputVar, exists = r.vm.vars[inputVarName]
	r.vm.mu.Unlock()

	// Second try: use inputNodeName directly as variable name (immediate execution)
	if !exists {
		inputVarName = inputNodeName
		r.vm.mu.Lock()
		inputVar, exists = r.vm.vars[inputVarName]
		r.vm.mu.Unlock()
	}

	log.WriteString(fmt.Sprintf("\nLoop configuration - input_node_name: %s, input_var: %s, iter_val: %s, iter_key: %s", inputNodeName, inputVarName, iterVal, iterKey))

	if !exists {
		err := fmt.Errorf("input variable %s not found (tried both as node name and direct variable)", inputVarName)
		log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		finalizeExecutionStep(s, false, err.Error(), log.String())
		return s, err
	}

	// Debug: Log the structure of the input variable
	log.WriteString(fmt.Sprintf("\nInput variable structure: %T = %+v", inputVar, inputVar))

	inputArray, ok := inputVar.([]interface{})
	if !ok {
		// Try to extract from data field if wrapped (common for trigger variables)
		if dataMap, ok := inputVar.(map[string]interface{}); ok {
			log.WriteString(fmt.Sprintf("\nInput variable is a map with keys: %v", getMapKeys(dataMap))) // e.g., ["data", "status", "headers"] or ["blockNumber", "timestamp", "hash"]

			if dataValue, hasData := dataMap["data"]; hasData {
				log.WriteString(fmt.Sprintf("\nFound 'data' field of type: %T", dataValue)) // e.g., "[]interface{}" for arrays, "map[string]interface{}" for objects

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

	log.WriteString(fmt.Sprintf("\nInput array has %d items", len(inputArray)))

	// If empty array, return early with empty results
	if len(inputArray) == 0 {
		results := []interface{}{}
		// Use shared function to set output variable for this step
		setNodeOutputData(r.CommonProcessor, stepID, results)

		// Convert empty results to protobuf Value
		dataValue, err := structpb.NewValue(results)
		if err != nil {
			// Fallback to empty data on error
			dataValue, _ = structpb.NewValue([]interface{}{})
		}

		loopOutput := &avsproto.LoopNode_Output{
			Data: dataValue,
		}

		s.OutputData = &avsproto.Execution_Step_Loop{
			Loop: loopOutput,
		}
		log.WriteString("\nEmpty array input - returning empty results")
		finalizeExecutionStep(s, true, "", log.String())
		return s, nil
	}

	// Process each array item
	results := make([]interface{}, len(inputArray))
	success := true
	var firstError error

	// Determine execution mode
	concurrent := false
	var executionModeLog string

	// Check if this is a contract write operation (security requirement)
	isContractWrite := node.GetContractWrite() != nil

	if isContractWrite {
		// Contract write operations must always be sequential for security
		concurrent = false
		executionModeLog = "sequentially due to contract write operation (security requirement)"
	} else {
		// For non-contract-write operations, use the configured execution mode
		switch executionMode {
		case avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL:
			concurrent = true
			executionModeLog = "parallel mode"
		case avsproto.ExecutionMode_EXECUTION_MODE_SEQUENTIAL:
			concurrent = false
			executionModeLog = "sequential mode"
		default:
			// Default to sequential (safer)
			concurrent = false
			executionModeLog = "sequential mode"
		}
	}

	if concurrent {
		log.WriteString(fmt.Sprintf("\nExecuting loop iterations in %s", executionModeLog))
		// Parallel execution using goroutines
		var wg sync.WaitGroup
		var mutex sync.Mutex

		for i, item := range inputArray {
			wg.Add(1)
			go func(index int, item interface{}) {
				defer wg.Done()

				// Create iteration-specific inputs
				iterInputs := make(map[string]interface{})
				iterInputs[iterVal] = item
				if iterKey != "" {
					iterInputs[iterKey] = index
				}

				iterationStepID := fmt.Sprintf("%s_iter_%d", stepID, index)
				resultData, err := r.executeNestedNode(node, iterationStepID, iterInputs)

				mutex.Lock()
				results[index] = resultData
				if err != nil {
					success = false
					if firstError == nil {
						firstError = err
					}
					log.WriteString(fmt.Sprintf("\nError in iteration %d: %s", index, err.Error()))
				}
				mutex.Unlock()
			}(i, item)
		}

		wg.Wait()
	} else {
		log.WriteString(fmt.Sprintf("\nExecuting loop iterations %s", executionModeLog))
		// Sequential execution
		for i, item := range inputArray {
			// Create iteration-specific inputs
			iterInputs := make(map[string]interface{})
			iterInputs[iterVal] = item
			if iterKey != "" {
				iterInputs[iterKey] = i
			}

			iterationStepID := fmt.Sprintf("%s_iter_%d", stepID, i)
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

	// Use shared function to set output variable for this step
	setNodeOutputData(r.CommonProcessor, stepID, results)

	// Convert results to JSON string for output data (Loop expects string, not structpb.Value)
	// Force JSON serialization by marshal -> unmarshal cycle to ensure compatibility
	jsonSerializableResults := make([]interface{}, len(results))
	for i, result := range results {
		if result != nil {
			// Force JSON conversion by marshaling and unmarshaling
			if tempJSON, tempErr := json.Marshal(result); tempErr == nil {
				var cleanResult interface{}
				if unmarshalErr := json.Unmarshal(tempJSON, &cleanResult); unmarshalErr == nil {
					jsonSerializableResults[i] = cleanResult
				} else {
					// If unmarshaling fails, convert to string
					jsonSerializableResults[i] = string(tempJSON)
				}
			} else {
				// If marshaling fails, convert using our converter
				converted := convertToJSONCompatible(result)
				if convertedJSON, convertErr := json.Marshal(converted); convertErr == nil {
					var cleanConverted interface{}
					if unmarshalErr := json.Unmarshal(convertedJSON, &cleanConverted); unmarshalErr == nil {
						jsonSerializableResults[i] = cleanConverted
					} else {
						jsonSerializableResults[i] = string(convertedJSON)
					}
				} else {
					// Last resort: string representation
					jsonSerializableResults[i] = fmt.Sprintf("%v", result)
				}
			}
		} else {
			jsonSerializableResults[i] = nil
		}
	}

	_, err := json.Marshal(jsonSerializableResults)
	if err == nil {
		// Convert results to protobuf Value
		dataValue, protoErr := structpb.NewValue(jsonSerializableResults)
		if protoErr != nil {
			// Fallback to empty data on error
			dataValue, _ = structpb.NewValue([]interface{}{})
		}

		loopOutput := &avsproto.LoopNode_Output{
			Data: dataValue,
		}

		s.OutputData = &avsproto.Execution_Step_Loop{
			Loop: loopOutput,
		}
		log.WriteString(fmt.Sprintf("\nSuccessfully set output data with %d results", len(results)))
	} else {
		log.WriteString(fmt.Sprintf("\nError converting results to JSON: %s", err.Error()))
		// Fallback: Try to parse Go map strings and convert to JSON
		fallbackResults := make([]interface{}, len(results))
		for i, result := range results {
			if result != nil {
				// Try to extract data from Go map string representation
				resultStr := fmt.Sprintf("%v", result)
				if parsed := parseGoMapString(resultStr); parsed != nil {
					fallbackResults[i] = parsed
				} else {
					// If parsing fails, use string representation
					fallbackResults[i] = resultStr
				}
			} else {
				fallbackResults[i] = nil
			}
		}

		// Try to marshal the parsed results
		if _, parseErr := json.Marshal(fallbackResults); parseErr == nil {
			// Convert fallback results to protobuf Value
			dataValue, protoErr := structpb.NewValue(fallbackResults)
			if protoErr != nil {
				// Fallback to empty data on error
				dataValue, _ = structpb.NewValue([]interface{}{})
			}

			loopOutput := &avsproto.LoopNode_Output{
				Data: dataValue,
			}

			s.OutputData = &avsproto.Execution_Step_Loop{
				Loop: loopOutput,
			}
		} else {
			// Final fallback: string array
			stringResults := make([]string, len(results))
			for i, result := range results {
				if result != nil {
					stringResults[i] = fmt.Sprintf("%v", result)
				} else {
					stringResults[i] = "null"
				}
			}
			_, _ = json.Marshal(stringResults)

			// Convert string results to protobuf Value
			dataValue, protoErr := structpb.NewValue(stringResults)
			if protoErr != nil {
				// Fallback to empty data on error
				dataValue, _ = structpb.NewValue([]interface{}{})
			}

			loopOutput := &avsproto.LoopNode_Output{
				Data: dataValue,
			}

			s.OutputData = &avsproto.Execution_Step_Loop{
				Loop: loopOutput,
			}
		}
	}

	log.WriteString(fmt.Sprintf("\nCompleted loop execution at %s", time.Now()))

	// Use shared function to finalize execution step
	var errorMsg string
	if !success && firstError != nil {
		errorMsg = firstError.Error()
	}
	finalizeExecutionStep(s, success, errorMsg, log.String())

	if !success && firstError != nil {
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
			Type:     avsproto.NodeType_NODE_TYPE_ETH_TRANSFER,
			TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: ethTransfer},
		}
	} else if contractWrite := loopNodeDef.GetContractWrite(); contractWrite != nil {
		// Apply template variable substitution to contract write configuration
		processedContractWrite := r.processContractWriteTemplates(contractWrite, iterInputs)
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
			TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: processedContractWrite},
		}
	} else if contractRead := loopNodeDef.GetContractRead(); contractRead != nil {
		// Apply template variable substitution to contract read configuration
		processedContractRead := r.processContractReadTemplates(contractRead, iterInputs)
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_CONTRACT_READ,
			TaskType: &avsproto.TaskNode_ContractRead{ContractRead: processedContractRead},
		}
	} else if graphqlQuery := loopNodeDef.GetGraphqlDataQuery(); graphqlQuery != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_GRAPHQL_QUERY,
			TaskType: &avsproto.TaskNode_GraphqlQuery{GraphqlQuery: graphqlQuery},
		}
	} else if restApi := loopNodeDef.GetRestApi(); restApi != nil {
		// Apply template variable substitution to REST API configuration
		processedRestApi := r.processRestApiTemplates(restApi, iterInputs)
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_REST_API,
			TaskType: &avsproto.TaskNode_RestApi{RestApi: processedRestApi},
		}
	} else if customCode := loopNodeDef.GetCustomCode(); customCode != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			Type:     avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
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

	// Extract result data based on the node type and ensure JSON compatibility
	if customCodeOutput := executionStep.GetCustomCode(); customCodeOutput != nil {
		if customCodeOutput.Data != nil {
			// Convert protobuf Value to JSON-compatible data
			rawData := customCodeOutput.Data.AsInterface()
			return convertToJSONCompatible(rawData), nil
		}
		return nil, nil
	} else if restApiOutput := executionStep.GetRestApi(); restApiOutput != nil {
		if restApiOutput.Data != nil {
			// Convert protobuf Value to JSON-compatible data
			rawData := restApiOutput.Data.AsInterface()
			return convertToJSONCompatible(rawData), nil
		}
		return nil, nil
	} else if contractReadOutput := executionStep.GetContractRead(); contractReadOutput != nil {
		// For contract read, convert the results to JSON-compatible format
		if contractReadOutput.GetData() != nil {
			// Extract results from the protobuf Value
			var results []interface{}

			if contractReadOutput.GetData().GetListValue() != nil {
				// Data is an array
				for _, item := range contractReadOutput.GetData().GetListValue().GetValues() {
					results = append(results, item.AsInterface())
				}
			} else {
				// Data might be a single object, wrap it in an array for consistency
				results = append(results, contractReadOutput.GetData().AsInterface())
			}

			return map[string]interface{}{
				"results": results,
			}, nil
		}
		return nil, nil
	} else if contractWriteOutput := executionStep.GetContractWrite(); contractWriteOutput != nil {
		// For contract write, convert the results to JSON-compatible format
		if contractWriteOutput.GetData() != nil {
			// Extract results from the protobuf Value
			var results []interface{}

			if contractWriteOutput.GetData().GetListValue() != nil {
				// Data is an array - return directly
				for _, item := range contractWriteOutput.GetData().GetListValue().GetValues() {
					results = append(results, item.AsInterface())
				}
			} else {
				// Data might be a single object, wrap it in an array for consistency
				results = append(results, contractWriteOutput.GetData().AsInterface())
			}

			// Return results array directly, not wrapped in a "results" object
			return results, nil
		}
		return nil, nil
	} else if ethTransferOutput := executionStep.GetEthTransfer(); ethTransferOutput != nil {
		// Extract transaction hash from the new data field
		if ethTransferOutput.Data != nil {
			dataMap := gow.ValueToMap(ethTransferOutput.Data)
			if dataMap != nil {
				if txHash, ok := dataMap["transactionHash"]; ok {
					return map[string]interface{}{
						"txHash": txHash,
					}, nil
				}
			}
		}
		return nil, nil
	} else if graphqlOutput := executionStep.GetGraphql(); graphqlOutput != nil {
		if graphqlOutput.Data != nil {
			// Extract data from the new standardized data field
			if dataMap := gow.ValueToMap(graphqlOutput.Data); dataMap != nil {
				return convertToJSONCompatible(dataMap), nil
			}
		}
		return nil, nil
	}

	// Fallback: return a basic success indicator
	return map[string]interface{}{
		"success": executionStep.Success,
		"error":   executionStep.Error,
	}, nil
}

// convertToJSONCompatible converts complex types (like protobuf) to JSON-compatible structures
func convertToJSONCompatible(data interface{}) interface{} {
	if data == nil {
		return nil
	}

	// Use reflection to handle time.Time and other specific types
	rv := reflect.ValueOf(data)
	rt := reflect.TypeOf(data)

	// Handle time.Time specifically
	if rt == reflect.TypeOf(time.Time{}) {
		if timeVal, ok := data.(time.Time); ok {
			return timeVal.Format(time.RFC3339)
		}
	}

	// Handle pointers
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		return convertToJSONCompatible(rv.Elem().Interface())
	}

	switch v := data.(type) {
	case map[string]interface{}:
		// Already JSON-compatible, but recursively convert nested values
		result := make(map[string]interface{})
		for key, value := range v {
			result[key] = convertToJSONCompatible(value)
		}
		return result
	case []interface{}:
		// Already JSON-compatible, but recursively convert nested values
		result := make([]interface{}, len(v))
		for i, value := range v {
			result[i] = convertToJSONCompatible(value)
		}
		return result
	case *structpb.Value:
		// Convert protobuf Value to native Go type
		return convertToJSONCompatible(v.AsInterface())
	case *structpb.Struct:
		// Convert protobuf Struct to map
		return convertToJSONCompatible(v.AsMap())
	case *structpb.ListValue:
		// Convert protobuf ListValue to slice
		values := v.GetValues()
		result := make([]interface{}, len(values))
		for i, value := range values {
			result[i] = convertToJSONCompatible(value)
		}
		return result
	case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		// Basic JSON-compatible types
		return data
	default:
		// For complex types that might not be JSON-compatible, try to convert them
		// Check if it implements json.Marshaler
		if marshaler, ok := data.(json.Marshaler); ok {
			if jsonBytes, err := marshaler.MarshalJSON(); err == nil {
				var result interface{}
				if err := json.Unmarshal(jsonBytes, &result); err == nil {
					return result
				}
			}
		}

		// For other types, try reflection-based conversion
		if rv.Kind() == reflect.Struct {
			result := make(map[string]interface{})
			for i := 0; i < rv.NumField(); i++ {
				field := rt.Field(i)
				if field.IsExported() {
					fieldValue := rv.Field(i)
					if fieldValue.CanInterface() {
						result[field.Name] = convertToJSONCompatible(fieldValue.Interface())
					}
				}
			}
			return result
		}

		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			result := make([]interface{}, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				result[i] = convertToJSONCompatible(rv.Index(i).Interface())
			}
			return result
		}

		if rv.Kind() == reflect.Map {
			result := make(map[string]interface{})
			for _, key := range rv.MapKeys() {
				keyStr := fmt.Sprintf("%v", key.Interface())
				result[keyStr] = convertToJSONCompatible(rv.MapIndex(key).Interface())
			}
			return result
		}

		// Fallback: convert to string
		return fmt.Sprintf("%v", data)
	}
}

// convertProtoFieldsToMap converts protobuf fields to a map
func convertProtoFieldsToMap(fields []*avsproto.ContractReadNode_MethodResult_StructuredField) map[string]interface{} {
	result := make(map[string]interface{})
	for _, field := range fields {
		result[field.GetName()] = field.GetValue()
	}
	return result
}

// parseGoMapString attempts to parse Go map string representation into a structured object
func parseGoMapString(s string) interface{} {
	// Basic Go map string parsing for simple cases
	// This is a simplified parser for Go map[string]interface{} string representations

	// Handle empty cases
	if s == "" || s == "<nil>" || s == "null" {
		return nil
	}

	// Try to detect and parse simple map[key:value] patterns
	if strings.HasPrefix(s, "map[") && strings.HasSuffix(s, "]") {
		// Extract content between map[ and ]
		content := s[4 : len(s)-1]

		// Simple key:value parser
		result := make(map[string]interface{})

		// Split by spaces, but be careful with nested structures
		parts := strings.Fields(content)
		for _, part := range parts {
			if colonIdx := strings.Index(part, ":"); colonIdx > 0 {
				key := part[:colonIdx]
				value := part[colonIdx+1:]

				// Try to convert value to appropriate type
				if value == "true" {
					result[key] = true
				} else if value == "false" {
					result[key] = false
				} else if strings.Contains(value, ".") {
					// Try to parse as float
					if f, err := strconv.ParseFloat(value, 64); err == nil {
						result[key] = f
					} else {
						result[key] = value
					}
				} else {
					// Try to parse as int
					if i, err := strconv.Atoi(value); err == nil {
						result[key] = i
					} else {
						result[key] = value
					}
				}
			}
		}

		if len(result) > 0 {
			return result
		}
	}

	// If parsing fails, return nil so caller can use string representation
	return nil
}

// processContractReadTemplates processes template variables in contract read configuration
func (r *LoopProcessor) processContractReadTemplates(contractRead *avsproto.ContractReadNode, iterInputs map[string]interface{}) *avsproto.ContractReadNode {
	// Create a copy of the contract read configuration
	processed := &avsproto.ContractReadNode{
		Config: &avsproto.ContractReadNode_Config{
			ContractAddress: r.substituteTemplateVariables(contractRead.Config.ContractAddress, iterInputs),
			ContractAbi:     r.substituteTemplateVariables(contractRead.Config.ContractAbi, iterInputs),
		},
	}

	// Process method calls
	for _, methodCall := range contractRead.Config.MethodCalls {
		processedMethodCall := &avsproto.ContractReadNode_MethodCall{
			CallData:      r.substituteTemplateVariables(methodCall.CallData, iterInputs),
			MethodName:    r.substituteTemplateVariables(methodCall.MethodName, iterInputs),
			ApplyToFields: make([]string, len(methodCall.ApplyToFields)),
		}

		// Copy applyToFields (no template substitution needed for field names)
		copy(processedMethodCall.ApplyToFields, methodCall.ApplyToFields)

		processed.Config.MethodCalls = append(processed.Config.MethodCalls, processedMethodCall)
	}

	return processed
}

// processContractWriteTemplates processes template variables in contract write configuration
func (r *LoopProcessor) processContractWriteTemplates(contractWrite *avsproto.ContractWriteNode, iterInputs map[string]interface{}) *avsproto.ContractWriteNode {
	// Create a copy of the contract write configuration
	processed := &avsproto.ContractWriteNode{
		Config: &avsproto.ContractWriteNode_Config{
			ContractAddress: r.substituteTemplateVariables(contractWrite.Config.ContractAddress, iterInputs),
			ContractAbi:     r.substituteTemplateVariables(contractWrite.Config.ContractAbi, iterInputs),
			CallData:        r.substituteTemplateVariables(contractWrite.Config.CallData, iterInputs),
		},
	}

	// Process method calls
	for _, methodCall := range contractWrite.Config.MethodCalls {
		processedMethodCall := &avsproto.ContractWriteNode_MethodCall{
			CallData:   r.substituteTemplateVariables(methodCall.CallData, iterInputs),
			MethodName: r.substituteTemplateVariables(methodCall.MethodName, iterInputs),
		}

		processed.Config.MethodCalls = append(processed.Config.MethodCalls, processedMethodCall)
	}

	return processed
}

// processRestApiTemplates processes template variables in REST API configuration
func (r *LoopProcessor) processRestApiTemplates(restApi *avsproto.RestAPINode, iterInputs map[string]interface{}) *avsproto.RestAPINode {
	// Create a copy of the REST API configuration
	processed := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url:    r.substituteTemplateVariables(restApi.Config.Url, iterInputs),
			Method: r.substituteTemplateVariables(restApi.Config.Method, iterInputs),
			Body:   r.substituteTemplateVariables(restApi.Config.Body, iterInputs),
		},
	}

	// Process headers
	if restApi.Config.Headers != nil {
		processed.Config.Headers = make(map[string]string)
		for key, value := range restApi.Config.Headers {
			processedKey := r.substituteTemplateVariables(key, iterInputs)
			processedValue := r.substituteTemplateVariables(value, iterInputs)
			processed.Config.Headers[processedKey] = processedValue
		}
	}

	return processed
}

// substituteTemplateVariables replaces template variables like {{value}} and {{index}} with actual values
func (r *LoopProcessor) substituteTemplateVariables(text string, iterInputs map[string]interface{}) string {
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
