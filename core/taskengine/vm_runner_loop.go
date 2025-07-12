package taskengine

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
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

	// Get node data using helper function to reduce duplication
	nodeName, nodeInput := r.vm.GetNodeDataForExecution(stepID)

	s := &avsproto.Execution_Step{
		Id:         stepID,
		OutputData: nil,
		Log:        "",
		Error:      "",
		Success:    true,
		StartAt:    t0,
		Type:       avsproto.NodeType_NODE_TYPE_LOOP.String(),
		Name:       nodeName,
		Input:      nodeInput, // Include node input data for debugging
	}

	var log strings.Builder
	log.WriteString(fmt.Sprintf("Start loop execution at %s", time.Now()))

	// Get configuration from node.Config (new architecture)
	if node.Config == nil {
		err := fmt.Errorf("LoopNode Config is nil")
		s.Success = false
		s.Error = err.Error()
		s.EndAt = time.Now().UnixMilli()
		log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		s.Log = log.String()
		return s, err
	}

	sourceNodeID := node.Config.SourceId
	iterVal := node.Config.IterVal
	iterKey := node.Config.IterKey

	if sourceNodeID == "" || iterVal == "" {
		err := fmt.Errorf("missing required configuration: source_id and iter_val are required")
		s.Success = false
		s.Error = err.Error()
		s.EndAt = time.Now().UnixMilli()
		log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		s.Log = log.String()
		return s, err
	}

	// In immediate execution, sourceId might be a direct variable name, not a node ID
	// Try both approaches: first as node ID (workflow execution), then as direct variable name (immediate execution)
	var inputName string
	var inputVar interface{}
	var exists bool

	// First try: resolve as node ID (workflow execution)
	inputName = r.vm.GetNodeNameAsVar(sourceNodeID)
	r.vm.mu.Lock()
	inputVar, exists = r.vm.vars[inputName]
	r.vm.mu.Unlock()

	// Second try: use sourceId directly as variable name (immediate execution)
	if !exists {
		inputName = sourceNodeID
		r.vm.mu.Lock()
		inputVar, exists = r.vm.vars[inputName]
		r.vm.mu.Unlock()
	}

	log.WriteString(fmt.Sprintf("\nLoop configuration - source_id: %s, input_var: %s, iter_val: %s, iter_key: %s", sourceNodeID, inputName, iterVal, iterKey))

	if !exists {
		err := fmt.Errorf("input variable %s not found (tried both as node name and direct variable)", inputName)
		s.Success = false
		s.Error = err.Error()
		s.EndAt = time.Now().UnixMilli()
		log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		s.Log = log.String()
		return s, err
	}

	// Debug: Log the structure of the input variable
	log.WriteString(fmt.Sprintf("\nInput variable structure: %T = %+v", inputVar, inputVar))

	inputArray, ok := inputVar.([]interface{})
	if !ok {
		if varMap, isMap := inputVar.(map[string]interface{}); isMap {
			// First try the standard "data" field
			if data, hasData := varMap["data"]; hasData {
				if dataArr, isArr := data.([]interface{}); isArr {
					inputArray = dataArr
					ok = true
					log.WriteString(fmt.Sprintf("\nFound array in 'data' field with %d items", len(dataArr)))
				} else if dataMap, isMap := data.(map[string]interface{}); isMap {
					// Check if "data" contains a nested map with arrays
					log.WriteString(fmt.Sprintf("\nData field is a map: %+v", dataMap))
					for fieldName, fieldValue := range dataMap {
						if dataArr, isArr := fieldValue.([]interface{}); isArr {
							inputArray = dataArr
							ok = true
							log.WriteString(fmt.Sprintf("\nFound array in nested 'data.%s' field with %d items", fieldName, len(dataArr)))
							break
						}
					}
				}
			}

			// If no "data" field or it's not an array, look for any field containing an array
			if !ok {
				log.WriteString(fmt.Sprintf("\nSearching for arrays in top-level fields of: %+v", varMap))
				for fieldName, fieldValue := range varMap {
					if dataArr, isArr := fieldValue.([]interface{}); isArr {
						inputArray = dataArr
						ok = true
						log.WriteString(fmt.Sprintf("\nFound array in '%s' field with %d items", fieldName, len(dataArr)))
						break
					} else if nestedMap, isMap := fieldValue.(map[string]interface{}); isMap {
						// Look for arrays in nested maps
						for nestedFieldName, nestedFieldValue := range nestedMap {
							if dataArr, isArr := nestedFieldValue.([]interface{}); isArr {
								inputArray = dataArr
								ok = true
								log.WriteString(fmt.Sprintf("\nFound array in nested '%s.%s' field with %d items", fieldName, nestedFieldName, len(dataArr)))
								break
							}
						}
						if ok {
							break
						}
					}
				}
			}
		}
	} else {
		log.WriteString(fmt.Sprintf("\nInput is already an array with %d items", len(inputArray)))
	}

	if !ok {
		err := fmt.Errorf("input %s is not an array and no array field found in output", inputName)
		s.Success = false
		s.Error = err.Error()
		s.EndAt = time.Now().UnixMilli()
		log.WriteString(fmt.Sprintf("\nError: %s", err.Error()))
		s.Log = log.String()
		return s, err
	}

	log.WriteString(fmt.Sprintf("\nIterating through %d items", len(inputArray)))

	// Determine execution mode based on configuration
	executionMode := node.Config.ExecutionMode
	runInParallel := (executionMode == avsproto.ExecutionMode_EXECUTION_MODE_PARALLEL)

	// Always run sequentially for ContractWrite operations (security requirement)
	if node.GetContractWrite() != nil {
		runInParallel = false
		log.WriteString("\nRunning iterations sequentially due to contract write operation (security requirement)")
	} else {
		if runInParallel {
			log.WriteString("\nRunning iterations in parallel mode")
		} else {
			log.WriteString("\nRunning iterations in sequential mode")
		}
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
				if iterKey != "" {
					iterInputs[iterKey] = index
				}
				iterInputs[iterVal] = valueParam

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
			if iterKey != "" {
				iterInputs[iterKey] = i
			}
			iterInputs[iterVal] = item

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

	resultsJSON, err := json.Marshal(jsonSerializableResults)
	if err == nil {
		s.OutputData = &avsproto.Execution_Step_Loop{
			Loop: &avsproto.LoopNode_Output{
				Data: string(resultsJSON),
			},
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
		if parsedJSON, parseErr := json.Marshal(fallbackResults); parseErr == nil {
			s.OutputData = &avsproto.Execution_Step_Loop{
				Loop: &avsproto.LoopNode_Output{
					Data: string(parsedJSON),
				},
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
			finalJSON, _ := json.Marshal(stringResults)
			s.OutputData = &avsproto.Execution_Step_Loop{
				Loop: &avsproto.LoopNode_Output{
					Data: string(finalJSON),
				},
			}
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
		// Apply template variable substitution to contract write configuration
		processedContractWrite := r.processContractWriteTemplates(contractWrite, iterInputs)
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: processedContractWrite},
		}
	} else if contractRead := loopNodeDef.GetContractRead(); contractRead != nil {
		// Apply template variable substitution to contract read configuration
		processedContractRead := r.processContractReadTemplates(contractRead, iterInputs)
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_ContractRead{ContractRead: processedContractRead},
		}
	} else if graphqlQuery := loopNodeDef.GetGraphqlDataQuery(); graphqlQuery != nil {
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_GraphqlQuery{GraphqlQuery: graphqlQuery},
		}
	} else if restApi := loopNodeDef.GetRestApi(); restApi != nil {
		// Apply template variable substitution to REST API configuration
		processedRestApi := r.processRestApiTemplates(restApi, iterInputs)
		nestedNode = &avsproto.TaskNode{
			Id:       iterationStepID,
			Name:     nodeName,
			TaskType: &avsproto.TaskNode_RestApi{RestApi: processedRestApi},
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
				// Data is an array
				for _, item := range contractWriteOutput.GetData().GetListValue().GetValues() {
					results = append(results, item.AsInterface())
				}
			} else {
				// Data might be a single object, wrap it in an array for consistency
				results = append(results, contractWriteOutput.GetData().AsInterface())
			}

			return map[string]interface{}{
				"results": results,
			}, nil
		}
		return nil, nil
	} else if ethTransferOutput := executionStep.GetEthTransfer(); ethTransferOutput != nil {
		return map[string]interface{}{
			"txHash": ethTransferOutput.GetTransactionHash(),
		}, nil
	} else if graphqlOutput := executionStep.GetGraphql(); graphqlOutput != nil {
		if graphqlOutput.Data != nil {
			// Use a generic map to unmarshal GraphQL data
			var result map[string]interface{}
			structVal := &structpb.Struct{}
			if err := graphqlOutput.Data.UnmarshalTo(structVal); err == nil {
				result = structVal.AsMap()
				return convertToJSONCompatible(result), nil
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
