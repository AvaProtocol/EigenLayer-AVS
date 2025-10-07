package taskengine

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// NodeOutputHandler defines the interface for converting execution results to protobuf responses
// Each node type implements this interface to encapsulate its output handling logic
type NodeOutputHandler interface {
	// ExtractFromExecutionStep extracts data from the execution step's output
	// Returns a map with the extracted data and any error encountered
	ExtractFromExecutionStep(step *avsproto.Execution_Step) (map[string]interface{}, error)

	// ConvertToProtobuf converts the extracted result map to the appropriate protobuf output type
	// Returns the protobuf OutputData variant (which must implement isRunNodeWithInputsResp_OutputData),
	// optional metadata, and any error encountered
	ConvertToProtobuf(result map[string]interface{}) (interface{}, *structpb.Value, error)

	// CreateEmptyOutput creates an empty output structure for error cases
	// This ensures we never return OUTPUT_DATA_NOT_SET
	// Returns the protobuf OutputData variant (which must implement isRunNodeWithInputsResp_OutputData)
	CreateEmptyOutput(nodeConfig map[string]interface{}) interface{}
}

// RestAPIOutputHandler handles RestAPI node output conversion
type RestAPIOutputHandler struct{}

func (h *RestAPIOutputHandler) ExtractFromExecutionStep(step *avsproto.Execution_Step) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	if restAPI := step.GetRestApi(); restAPI != nil && restAPI.GetData() != nil {
		iface := restAPI.GetData().AsInterface()
		if m, ok := iface.(map[string]interface{}); ok {
			result = m
		} else {
			result = map[string]interface{}{"data": iface}
		}
	}
	return result, nil
}

func (h *RestAPIOutputHandler) ConvertToProtobuf(result map[string]interface{}) (interface{}, *structpb.Value, error) {
	var cleanData interface{}
	var rawResponse interface{}

	if result != nil {
		rawResponse = result
		if dataField, ok := result["data"]; ok {
			cleanData = dataField
		} else {
			cleanData = map[string]interface{}{}
		}
	}

	valueData, err := structpb.NewValue(cleanData)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to convert REST API output: %w", err)
	}

	restOutput := &avsproto.RestAPINode_Output{Data: valueData}
	outputData := &avsproto.RunNodeWithInputsResp_RestApi{RestApi: restOutput}

	// Metadata contains raw response
	var metadata *structpb.Value
	if rawResponse != nil {
		metadata, _ = structpb.NewValue(rawResponse)
	}

	return outputData, metadata, nil
}

func (h *RestAPIOutputHandler) CreateEmptyOutput(nodeConfig map[string]interface{}) interface{} {
	return &avsproto.RunNodeWithInputsResp_RestApi{RestApi: &avsproto.RestAPINode_Output{}}
}

// CustomCodeOutputHandler handles CustomCode node output conversion
type CustomCodeOutputHandler struct{}

func (h *CustomCodeOutputHandler) ExtractFromExecutionStep(step *avsproto.Execution_Step) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	if ccode := step.GetCustomCode(); ccode != nil && ccode.GetData() != nil {
		iface := ccode.GetData().AsInterface()
		if m, ok := iface.(map[string]interface{}); ok {
			result = m
		} else {
			result["data"] = iface
		}
	}
	return result, nil
}

func (h *CustomCodeOutputHandler) ConvertToProtobuf(result map[string]interface{}) (interface{}, *structpb.Value, error) {
	var rawData interface{}

	if result != nil {
		// Check if this has been processed by extractExecutionResult with metadata
		if hasMetadata := (result["success"] != nil || result["nodeId"] != nil); hasMetadata {
			if dataField, ok := result["data"]; ok {
				rawData = dataField
			} else {
				// Extract original object by removing metadata fields
				originalObject := make(map[string]interface{})
				for k, v := range result {
					if k != "success" && k != "nodeId" && k != "error" {
						originalObject[k] = v
					}
				}
				rawData = originalObject
			}
		} else {
			rawData = result
		}
	}

	valueData, err := structpb.NewValue(rawData)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to convert CustomCode output: %w", err)
	}

	customOutput := &avsproto.CustomCodeNode_Output{Data: valueData}
	outputData := &avsproto.RunNodeWithInputsResp_CustomCode{CustomCode: customOutput}

	return outputData, nil, nil
}

func (h *CustomCodeOutputHandler) CreateEmptyOutput(nodeConfig map[string]interface{}) interface{} {
	return &avsproto.RunNodeWithInputsResp_CustomCode{CustomCode: &avsproto.CustomCodeNode_Output{}}
}

// BalanceOutputHandler handles Balance node output conversion
type BalanceOutputHandler struct{}

func (h *BalanceOutputHandler) ExtractFromExecutionStep(step *avsproto.Execution_Step) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	if balance := step.GetBalance(); balance != nil && balance.GetData() != nil {
		balanceArray := balance.GetData().AsInterface()
		result["data"] = balanceArray
	}
	return result, nil
}

func (h *BalanceOutputHandler) ConvertToProtobuf(result map[string]interface{}) (interface{}, *structpb.Value, error) {
	var dataValue *structpb.Value
	var err error

	if result != nil && result["data"] != nil {
		dataValue, err = structpb.NewValue(result["data"])
		if err != nil {
			return nil, nil, fmt.Errorf("failed to convert Balance output: %w", err)
		}
	} else {
		emptyArray := []interface{}{}
		dataValue, err = structpb.NewValue(emptyArray)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create empty Balance output: %w", err)
		}
	}

	balanceOutput := &avsproto.BalanceNode_Output{Data: dataValue}
	outputData := &avsproto.RunNodeWithInputsResp_Balance{Balance: balanceOutput}

	return outputData, nil, nil
}

func (h *BalanceOutputHandler) CreateEmptyOutput(nodeConfig map[string]interface{}) interface{} {
	return &avsproto.RunNodeWithInputsResp_Balance{Balance: &avsproto.BalanceNode_Output{}}
}

// ContractReadOutputHandler handles ContractRead node output conversion
type ContractReadOutputHandler struct{}

func (h *ContractReadOutputHandler) ExtractFromExecutionStep(step *avsproto.Execution_Step) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	if contractRead := step.GetContractRead(); contractRead != nil {
		if contractRead.GetData() != nil {
			iface := contractRead.GetData().AsInterface()
			if m, ok := iface.(map[string]interface{}); ok {
				result["data"] = m
			} else {
				result["data"] = iface
			}
		}

		if step.Metadata != nil {
			if metadataArray := gow.ValueToSlice(step.Metadata); metadataArray != nil {
				result["metadata"] = metadataArray
			} else {
				result["metadata"] = step.Metadata.AsInterface()
			}
		}
	}
	return result, nil
}

func (h *ContractReadOutputHandler) ConvertToProtobuf(result map[string]interface{}) (interface{}, *structpb.Value, error) {
	contractReadOutput := &avsproto.ContractReadNode_Output{}
	var metadata *structpb.Value

	if result != nil && len(result) > 0 {
		if dataInterface, hasData := result["data"]; hasData {
			if resultsValue, err := structpb.NewValue(dataInterface); err == nil {
				contractReadOutput.Data = resultsValue
			}
		} else {
			cleanResult := make(map[string]interface{})
			for k, v := range result {
				if k != "metadata" {
					cleanResult[k] = v
				}
			}
			if len(cleanResult) > 0 {
				if resultsValue, err := structpb.NewValue(cleanResult); err == nil {
					contractReadOutput.Data = resultsValue
				}
			}
		}

		if metadataInterface, hasMetadata := result["metadata"]; hasMetadata {
			if metadataValue, err := structpb.NewValue(metadataInterface); err == nil {
				metadata = metadataValue
			}
		}
	}

	outputData := &avsproto.RunNodeWithInputsResp_ContractRead{ContractRead: contractReadOutput}
	return outputData, metadata, nil
}

func (h *ContractReadOutputHandler) CreateEmptyOutput(nodeConfig map[string]interface{}) interface{} {
	dataMap := map[string]interface{}{}
	if calls, ok := nodeConfig["methodCalls"].([]interface{}); ok {
		for _, c := range calls {
			if m, ok := c.(map[string]interface{}); ok {
				if mn, ok := m["methodName"].(string); ok && mn != "" {
					dataMap[mn] = map[string]interface{}{}
				}
			}
		}
	}
	dataVal, _ := structpb.NewValue(dataMap)
	return &avsproto.RunNodeWithInputsResp_ContractRead{ContractRead: &avsproto.ContractReadNode_Output{Data: dataVal}}
}

// ContractWriteOutputHandler handles ContractWrite node output conversion
type ContractWriteOutputHandler struct {
	engine *Engine // Need reference to engine for parseEventWithParsedABI
}

func NewContractWriteOutputHandler(engine *Engine) *ContractWriteOutputHandler {
	return &ContractWriteOutputHandler{engine: engine}
}

func (h *ContractWriteOutputHandler) ExtractFromExecutionStep(step *avsproto.Execution_Step) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	if contractWrite := step.GetContractWrite(); contractWrite != nil {
		if contractWrite.GetData() != nil {
			iface := contractWrite.GetData().AsInterface()
			if m, ok := iface.(map[string]interface{}); ok {
				result["data"] = m
			} else {
				result["data"] = iface
			}
		}

		if step.Metadata != nil {
			allResults := ExtractResultsFromProtobufValue(step.Metadata)
			result["results"] = allResults

			if metadataArray := gow.ValueToSlice(step.Metadata); metadataArray != nil {
				result["metadata"] = metadataArray
			} else {
				result["metadata"] = step.Metadata.AsInterface()
			}
		}

		result["success"] = step.Success
		if !step.Success {
			result["error"] = step.Error
		}
	}
	return result, nil
}

func (h *ContractWriteOutputHandler) ConvertToProtobuf(result map[string]interface{}) (interface{}, *structpb.Value, error) {
	contractWriteOutput := &avsproto.ContractWriteNode_Output{}
	var resultsArray []interface{}
	var decodedEventsData = make(map[string]interface{})
	var metadata *structpb.Value

	if result != nil && len(result) > 0 {
		if dataFromVM, ok := result["data"].(map[string]interface{}); ok {
			decodedEventsData = dataFromVM
		}

		if resultsFromVM, ok := result["results"].([]interface{}); ok {
			for _, resultInterface := range resultsFromVM {
				if methodResult, ok := resultInterface.(*avsproto.ContractWriteNode_MethodResult); ok {
					convertedResult := map[string]interface{}{
						"methodName": methodResult.MethodName,
						"success":    methodResult.Success,
						"error":      methodResult.Error,
					}

					if methodResult.MethodAbi != nil {
						convertedResult["methodABI"] = methodResult.MethodAbi.AsInterface()
					}

					if methodResult.Receipt != nil {
						convertedResult["receipt"] = methodResult.Receipt.AsInterface()
					}

					if methodResult.Value != nil {
						convertedResult["value"] = methodResult.Value.AsInterface()
					} else {
						convertedResult["value"] = nil
					}

					resultsArray = append(resultsArray, convertedResult)

					// Parse events for this specific method
					methodEvents := make(map[string]interface{})
					if methodResult.Receipt != nil {
						receiptData := methodResult.Receipt.AsInterface()
						if receiptMap, ok := receiptData.(map[string]interface{}); ok {
							if logs, hasLogs := receiptMap["logs"]; hasLogs {
								if logsArray, ok := logs.([]interface{}); ok && len(logsArray) > 0 {
									var contractABI *abi.ABI
									if methodResult.MethodAbi != nil {
										if abiData := methodResult.MethodAbi.AsInterface(); abiData != nil {
											if abiMap, ok := abiData.(map[string]interface{}); ok {
												if abiString, hasABI := abiMap["contractABI"]; hasABI {
													if abiStr, ok := abiString.(string); ok {
														if parsed, err := abi.JSON(strings.NewReader(abiStr)); err == nil {
															contractABI = &parsed
														}
													}
												}
											}
										}
									}

									for _, logInterface := range logsArray {
										if logMap, ok := logInterface.(map[string]interface{}); ok {
											if contractABI != nil {
												eventLog := &types.Log{}

												if addr, hasAddr := logMap["address"]; hasAddr {
													if addrStr, ok := addr.(string); ok {
														eventLog.Address = common.HexToAddress(addrStr)
													}
												}

												if topics, hasTopics := logMap["topics"]; hasTopics {
													if topicsArray, ok := topics.([]interface{}); ok {
														for _, topic := range topicsArray {
															if topicStr, ok := topic.(string); ok {
																eventLog.Topics = append(eventLog.Topics, common.HexToHash(topicStr))
															}
														}
													}
												}

												if data, hasData := logMap["data"]; hasData {
													if dataStr, ok := data.(string); ok {
														if dataBytes, err := hexutil.Decode(dataStr); err == nil {
															eventLog.Data = dataBytes
														}
													}
												}

												if decodedEvent, err := h.engine.parseEventWithParsedABI(eventLog, contractABI, nil); err == nil {
													for key, value := range decodedEvent {
														if key != "eventName" {
															methodEvents[key] = value
														}
													}
												}
											}
										}
									}
								}
							}
						}
					}

					if len(methodEvents) > 0 {
						decodedEventsData[methodResult.MethodName] = methodEvents
					}
				} else if methodResultMap, ok := resultInterface.(map[string]interface{}); ok {
					resultsArray = append(resultsArray, methodResultMap)
				}
			}
		} else {
			// Fallback for backward compatibility
			if txHash, ok := result["txHash"].(string); ok {
				convertedResult := map[string]interface{}{
					"methodName": UnknownMethodName,
					"success":    true,
					"transaction": map[string]interface{}{
						"hash": txHash,
					},
				}
				resultsArray = append(resultsArray, convertedResult)
			} else if transactionHash, ok := result["transactionHash"].(string); ok {
				convertedResult := map[string]interface{}{
					"methodName": UnknownMethodName,
					"success":    true,
					"transaction": map[string]interface{}{
						"hash": transactionHash,
					},
				}
				resultsArray = append(resultsArray, convertedResult)
			}
		}
	}

	if dataValue, err := structpb.NewValue(decodedEventsData); err == nil {
		contractWriteOutput.Data = dataValue
	}

	if len(resultsArray) > 0 {
		if metadataValue, err := structpb.NewValue(resultsArray); err == nil {
			metadata = metadataValue
		}
	}

	outputData := &avsproto.RunNodeWithInputsResp_ContractWrite{ContractWrite: contractWriteOutput}
	return outputData, metadata, nil
}

func (h *ContractWriteOutputHandler) CreateEmptyOutput(nodeConfig map[string]interface{}) interface{} {
	dataMap := map[string]interface{}{}
	if calls, ok := nodeConfig["methodCalls"].([]interface{}); ok {
		for _, c := range calls {
			if m, ok := c.(map[string]interface{}); ok {
				if mn, ok := m["methodName"].(string); ok && mn != "" {
					dataMap[mn] = map[string]interface{}{}
				}
			}
		}
	}
	dataVal, _ := structpb.NewValue(dataMap)
	return &avsproto.RunNodeWithInputsResp_ContractWrite{ContractWrite: &avsproto.ContractWriteNode_Output{Data: dataVal}}
}

// ETHTransferOutputHandler handles ETHTransfer node output conversion
type ETHTransferOutputHandler struct{}

func (h *ETHTransferOutputHandler) ExtractFromExecutionStep(step *avsproto.Execution_Step) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	if ethTransfer := step.GetEthTransfer(); ethTransfer != nil {
		if ethTransfer.GetData() != nil {
			if dataMap := gow.ValueToMap(ethTransfer.GetData()); dataMap != nil {
				if txHash, ok := dataMap["transactionHash"]; ok {
					result["txHash"] = txHash
				}
			}
		}
		result["success"] = true
	}
	return result, nil
}

func (h *ETHTransferOutputHandler) ConvertToProtobuf(result map[string]interface{}) (interface{}, *structpb.Value, error) {
	ethData := map[string]interface{}{}
	if result != nil {
		if txHash, ok := result["txHash"].(string); ok {
			ethData["transactionHash"] = txHash
		}
	}

	dataValue, err := structpb.NewValue(ethData)
	if err != nil {
		dataValue, _ = structpb.NewValue(map[string]interface{}{})
	}

	ethOutput := &avsproto.ETHTransferNode_Output{Data: dataValue}
	outputData := &avsproto.RunNodeWithInputsResp_EthTransfer{EthTransfer: ethOutput}

	return outputData, nil, nil
}

func (h *ETHTransferOutputHandler) CreateEmptyOutput(nodeConfig map[string]interface{}) interface{} {
	return &avsproto.RunNodeWithInputsResp_EthTransfer{EthTransfer: &avsproto.ETHTransferNode_Output{}}
}

// GraphQLOutputHandler handles GraphQL node output conversion
type GraphQLOutputHandler struct{}

func (h *GraphQLOutputHandler) ExtractFromExecutionStep(step *avsproto.Execution_Step) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	if graphql := step.GetGraphql(); graphql != nil && graphql.GetData() != nil {
		iface := graphql.GetData().AsInterface()
		if graphqlData, ok := iface.(map[string]interface{}); ok {
			return graphqlData, nil
		}
		result["data"] = iface
	}
	return result, nil
}

func (h *GraphQLOutputHandler) ConvertToProtobuf(result map[string]interface{}) (interface{}, *structpb.Value, error) {
	var dataValue *structpb.Value
	var err error

	if result != nil && len(result) > 0 {
		dataValue, err = structpb.NewValue(result)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to convert GraphQL output: %w", err)
		}
	} else {
		dataValue, err = structpb.NewValue(map[string]interface{}{})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create empty GraphQL output: %w", err)
		}
	}

	graphqlOutput := &avsproto.GraphQLQueryNode_Output{Data: dataValue}
	outputData := &avsproto.RunNodeWithInputsResp_Graphql{Graphql: graphqlOutput}

	return outputData, nil, nil
}

func (h *GraphQLOutputHandler) CreateEmptyOutput(nodeConfig map[string]interface{}) interface{} {
	return &avsproto.RunNodeWithInputsResp_Graphql{Graphql: &avsproto.GraphQLQueryNode_Output{}}
}

// BranchOutputHandler handles Branch node output conversion
type BranchOutputHandler struct{}

func (h *BranchOutputHandler) ExtractFromExecutionStep(step *avsproto.Execution_Step) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	if branch := step.GetBranch(); branch != nil {
		if branch.Data != nil {
			dataMap := gow.ValueToMap(branch.Data)
			if dataMap != nil {
				if conditionId, ok := dataMap["conditionId"]; ok {
					result["conditionId"] = conditionId
				}
			}
		}
		result["success"] = true
	}
	return result, nil
}

func (h *BranchOutputHandler) ConvertToProtobuf(result map[string]interface{}) (interface{}, *structpb.Value, error) {
	branchData := map[string]interface{}{}
	if result != nil && len(result) > 0 {
		if conditionId, ok := result["conditionId"].(string); ok {
			branchData["conditionId"] = conditionId
		}
	} else {
		branchData["conditionId"] = ""
	}

	dataValue, err := structpb.NewValue(branchData)
	if err != nil {
		dataValue, _ = structpb.NewValue(map[string]interface{}{})
	}

	branchOutput := &avsproto.BranchNode_Output{Data: dataValue}
	outputData := &avsproto.RunNodeWithInputsResp_Branch{Branch: branchOutput}

	return outputData, nil, nil
}

func (h *BranchOutputHandler) CreateEmptyOutput(nodeConfig map[string]interface{}) interface{} {
	return &avsproto.RunNodeWithInputsResp_Branch{Branch: &avsproto.BranchNode_Output{}}
}

// FilterOutputHandler handles Filter node output conversion
type FilterOutputHandler struct{}

func (h *FilterOutputHandler) ExtractFromExecutionStep(step *avsproto.Execution_Step) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	if filter := step.GetFilter(); filter != nil && filter.GetData() != nil {
		iface := filter.GetData().AsInterface()
		if filterArray, ok := iface.([]interface{}); ok {
			return map[string]interface{}{"data": filterArray}, nil
		}
		result["data"] = iface
	}
	return result, nil
}

func (h *FilterOutputHandler) ConvertToProtobuf(result map[string]interface{}) (interface{}, *structpb.Value, error) {
	var dataValue *structpb.Value
	var err error

	if result != nil && len(result) > 0 {
		dataValue, err = structpb.NewValue(result)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to convert Filter output: %w", err)
		}
	} else {
		emptyArray := []interface{}{}
		dataValue, err = structpb.NewValue(emptyArray)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create empty Filter output: %w", err)
		}
	}

	filterOutput := &avsproto.FilterNode_Output{Data: dataValue}
	outputData := &avsproto.RunNodeWithInputsResp_Filter{Filter: filterOutput}

	return outputData, nil, nil
}

func (h *FilterOutputHandler) CreateEmptyOutput(nodeConfig map[string]interface{}) interface{} {
	return &avsproto.RunNodeWithInputsResp_Filter{Filter: &avsproto.FilterNode_Output{}}
}

// LoopOutputHandler handles Loop node output conversion
type LoopOutputHandler struct{}

func (h *LoopOutputHandler) ExtractFromExecutionStep(step *avsproto.Execution_Step) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	if loop := step.GetLoop(); loop != nil && loop.GetData() != nil {
		iface := loop.GetData().AsInterface()
		result["loopResult"] = iface
	}
	return result, nil
}

func (h *LoopOutputHandler) ConvertToProtobuf(result map[string]interface{}) (interface{}, *structpb.Value, error) {
	if result != nil {
		var loopData interface{}
		if loopResult, exists := result["loopResult"]; exists {
			loopData = loopResult
		} else {
			loopData = result
		}

		dataValue, err := structpb.NewValue(loopData)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to convert loop output: %w", err)
		}

		outputData := &avsproto.RunNodeWithInputsResp_Loop{
			Loop: &avsproto.LoopNode_Output{Data: dataValue},
		}
		return outputData, nil, nil
	}

	emptyArray := []interface{}{}
	dataValue, err := structpb.NewValue(emptyArray)
	if err != nil {
		dataValue, _ = structpb.NewValue([]interface{}{})
	}

	outputData := &avsproto.RunNodeWithInputsResp_Loop{
		Loop: &avsproto.LoopNode_Output{Data: dataValue},
	}
	return outputData, nil, nil
}

func (h *LoopOutputHandler) CreateEmptyOutput(nodeConfig map[string]interface{}) interface{} {
	return &avsproto.RunNodeWithInputsResp_Loop{Loop: &avsproto.LoopNode_Output{}}
}

// NodeOutputHandlerFactory creates the appropriate handler for each node type
type NodeOutputHandlerFactory struct {
	handlers map[string]NodeOutputHandler
}

// NewNodeOutputHandlerFactory creates a new factory with all handlers registered
func NewNodeOutputHandlerFactory(engine *Engine) *NodeOutputHandlerFactory {
	factory := &NodeOutputHandlerFactory{
		handlers: make(map[string]NodeOutputHandler),
	}

	// Register all handlers
	factory.handlers[NodeTypeRestAPI] = &RestAPIOutputHandler{}
	factory.handlers[NodeTypeCustomCode] = &CustomCodeOutputHandler{}
	factory.handlers[NodeTypeBalance] = &BalanceOutputHandler{}
	factory.handlers[NodeTypeContractRead] = &ContractReadOutputHandler{}
	factory.handlers[NodeTypeContractWrite] = NewContractWriteOutputHandler(engine)
	factory.handlers[NodeTypeETHTransfer] = &ETHTransferOutputHandler{}
	factory.handlers[NodeTypeGraphQLQuery] = &GraphQLOutputHandler{}
	factory.handlers[NodeTypeBranch] = &BranchOutputHandler{}
	factory.handlers[NodeTypeFilter] = &FilterOutputHandler{}
	factory.handlers[NodeTypeLoop] = &LoopOutputHandler{}

	return factory
}

// GetHandler returns the appropriate handler for the given node type
func (f *NodeOutputHandlerFactory) GetHandler(nodeType string) (NodeOutputHandler, error) {
	handler, exists := f.handlers[nodeType]
	if !exists {
		return nil, fmt.Errorf("no handler found for node type: %s", nodeType)
	}
	return handler, nil
}
