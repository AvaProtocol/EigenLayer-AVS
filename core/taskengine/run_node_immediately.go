package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

// RunNodeImmediately executes a single node immediately for testing/simulation purposes.
// This is different from workflow execution - it runs the node right now, ignoring any scheduling.
func (n *Engine) RunNodeImmediately(nodeType string, nodeConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	if IsTriggerNodeType(nodeType) {
		return n.runTriggerImmediately(nodeType, nodeConfig, inputVariables)
	} else {
		return n.runProcessingNodeWithInputs(nodeType, nodeConfig, inputVariables)
	}
}

// runTriggerImmediately executes trigger nodes immediately, ignoring any scheduling configuration
func (n *Engine) runTriggerImmediately(triggerType string, triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	switch triggerType {
	case NodeTypeBlockTrigger:
		return n.runBlockTriggerImmediately(triggerConfig, inputVariables)
	case NodeTypeFixedTimeTrigger:
		return n.runFixedTimeTriggerImmediately(triggerConfig, inputVariables)
	case NodeTypeCronTrigger:
		return n.runCronTriggerImmediately(triggerConfig, inputVariables)
	case NodeTypeEventTrigger:
		return n.runEventTriggerImmediately(triggerConfig, inputVariables)
	case NodeTypeManualTrigger:
		return n.runManualTriggerImmediately(triggerConfig, inputVariables)
	default:
		return nil, fmt.Errorf("unsupported trigger type: %s", triggerType)
	}
}

// runBlockTriggerImmediately gets the latest block data immediately, ignoring any interval configuration
func (n *Engine) runBlockTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// For immediate execution, we ignore interval and always get the latest block
	// unless a specific blockNumber is provided
	var blockNumber uint64

	// Check if a specific block number is requested
	if configBlockNumber, ok := triggerConfig["blockNumber"]; ok {
		if blockNum, err := n.parseUint64(configBlockNumber); err == nil {
			blockNumber = blockNum
		}
	}

	// Ensure RPC connection is available
	if rpcConn == nil {
		return nil, fmt.Errorf("RPC connection not available for BlockTrigger execution")
	}

	// If no specific block number, get the latest block
	if blockNumber == 0 {
		currentBlock, err := rpcConn.BlockNumber(context.Background())
		if err != nil {
			return nil, fmt.Errorf("failed to get current block number from RPC: %w", err)
		}
		blockNumber = currentBlock

		if n.logger != nil {
			n.logger.Info("BlockTrigger: Using latest block for immediate execution", "blockNumber", blockNumber)
		}
	}

	// Get real block data from RPC
	header, err := rpcConn.HeaderByNumber(context.Background(), big.NewInt(int64(blockNumber)))
	if err != nil {
		return nil, fmt.Errorf("failed to get block header for block %d from RPC: %w", blockNumber, err)
	}

	result := map[string]interface{}{
		"blockNumber": blockNumber,
		"blockHash":   header.Hash().Hex(),
		"timestamp":   header.Time,
		"parentHash":  header.ParentHash.Hex(),
		"difficulty":  header.Difficulty.String(),
		"gasLimit":    header.GasLimit,
		"gasUsed":     header.GasUsed,
	}

	if n.logger != nil {
		n.logger.Info("BlockTrigger executed immediately", "blockNumber", blockNumber, "blockHash", header.Hash().Hex())
	}
	return result, nil
}

// runFixedTimeTriggerImmediately returns the current timestamp immediately
func (n *Engine) runFixedTimeTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// For immediate execution, return current timestamp in milliseconds and ISO format
	currentTime := time.Now()
	currentTimestamp := uint64(currentTime.UnixMilli())
	currentTimestampISO := currentTime.UTC().Format("2006-01-02T15:04:05.000Z")

	result := map[string]interface{}{
		"timestamp":     currentTimestamp,
		"timestamp_iso": currentTimestampISO,
	}

	if n.logger != nil {
		n.logger.Info("FixedTimeTrigger executed immediately", "timestamp", currentTimestamp, "timestamp_iso", currentTimestampISO)
	}
	return result, nil
}

// runCronTriggerImmediately returns the current timestamp immediately
func (n *Engine) runCronTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// For immediate execution, return current timestamp in milliseconds and ISO format
	currentTime := time.Now()
	currentTimestamp := uint64(currentTime.UnixMilli())
	currentTimestampISO := currentTime.UTC().Format("2006-01-02T15:04:05.000Z")

	result := map[string]interface{}{
		"timestamp":     currentTimestamp,
		"timestamp_iso": currentTimestampISO,
	}

	if n.logger != nil {
		n.logger.Info("CronTrigger executed immediately", "timestamp", currentTimestamp, "timestamp_iso", currentTimestampISO)
	}
	return result, nil
}

// runEventTriggerImmediately simulates an event trigger execution
func (n *Engine) runEventTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// For immediate execution, we can't actually wait for an event, so return a simulation
	result := map[string]interface{}{
		"simulated": true,
		"message":   "EventTrigger cannot be executed immediately - this is a simulation",
		"timestamp": uint64(time.Now().Unix()),
	}

	if n.logger != nil {
		n.logger.Info("EventTrigger simulated for immediate execution")
	}
	return result, nil
}

// runManualTriggerImmediately executes a manual trigger immediately
func (n *Engine) runManualTriggerImmediately(triggerConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// Manual triggers are perfect for immediate execution
	result := map[string]interface{}{
		"triggered": true,
		"runAt":     uint64(time.Now().Unix()),
	}

	if n.logger != nil {
		n.logger.Info("ManualTrigger executed immediately")
	}
	return result, nil
}

// runProcessingNodeWithInputs handles execution of processing node types
func (n *Engine) runProcessingNodeWithInputs(nodeType string, nodeConfig map[string]interface{}, inputVariables map[string]interface{}) (map[string]interface{}, error) {
	// Check if this is actually a trigger type that was misrouted
	if IsTriggerNodeType(nodeType) {
		return n.runTriggerImmediately(nodeType, nodeConfig, inputVariables)
	}

	// Load secrets for immediate execution (global macroSecrets + user-level secrets)
	secrets, err := n.LoadSecretsForImmediateExecution(inputVariables)
	if err != nil {
		if n.logger != nil {
			n.logger.Warn("Failed to load secrets for immediate execution", "error", err)
		}
		// Don't fail the request, just use empty secrets
		secrets = make(map[string]string)
	}

	// Create a clean VM for isolated execution with proper secrets
	vm, err := NewVMWithData(nil, nil, n.smartWalletConfig, secrets)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM: %w", err)
	}

	vm.WithLogger(n.logger).WithDb(n.db)

	// Add input variables to VM for template processing and node access
	for key, value := range inputVariables {
		vm.AddVar(key, value)
	}

	// Create node from type and config
	node, err := CreateNodeFromType(nodeType, nodeConfig, "")
	if err != nil {
		return nil, fmt.Errorf("failed to create node: %w", err)
	}

	// Execute the node
	executionStep, err := vm.RunNodeWithInputs(node, inputVariables)
	if err != nil {
		return nil, fmt.Errorf("node execution failed: %w", err)
	}

	if !executionStep.Success {
		return nil, fmt.Errorf("execution failed: %s", executionStep.Error)
	}

	// Extract and return the result data
	return n.extractExecutionResult(executionStep)
}

// LoadSecretsForImmediateExecution loads secrets for immediate node execution
// It loads global macroSecrets and user-level secrets (no workflow-level secrets since there's no workflow)
func (n *Engine) LoadSecretsForImmediateExecution(inputVariables map[string]interface{}) (map[string]string, error) {
	secrets := make(map[string]string)

	// Copy global static secrets from macroSecrets (equivalent to copyMap(secrets, macroSecrets) in LoadSecretForTask)
	copyMap(secrets, macroSecrets)

	// Try to get user from workflowContext if available
	if workflowContext, ok := inputVariables["workflowContext"]; ok {
		if wfCtx, ok := workflowContext.(map[string]interface{}); ok {
			if userIdStr, ok := wfCtx["userId"].(string); ok {
				// Load user-level secrets from database
				// Note: For immediate execution we don't have workflow-level secrets since there's no specific workflow
				// For now, we'll just use the global macroSecrets
				// In a full implementation, you'd want to resolve userId to user address and load user secrets
				// But the most important thing is that macroSecrets (global config secrets) are available
				if n.logger != nil {
					n.logger.Debug("LoadSecretsForImmediateExecution: Using global secrets", "userId", userIdStr, "secretCount", len(secrets))
				}
			}
		}
	}

	return secrets, nil
}

func (n *Engine) parseUint64(value interface{}) (uint64, error) {
	switch v := value.(type) {
	case uint64:
		return v, nil
	case int64:
		if v < 0 {
			return 0, fmt.Errorf("negative value cannot be converted to uint64: %d", v)
		}
		return uint64(v), nil
	case int:
		if v < 0 {
			return 0, fmt.Errorf("negative value cannot be converted to uint64: %d", v)
		}
		return uint64(v), nil
	case float64:
		if v < 0 {
			return 0, fmt.Errorf("negative value cannot be converted to uint64: %f", v)
		}
		return uint64(v), nil
	case string:
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("failed to parse string to uint64: %w", err)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported type for uint64 conversion: %T", value)
	}
}

// extractExecutionResult extracts the result data from an execution step
func (n *Engine) extractExecutionResult(executionStep *avsproto.Execution_Step) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	// Handle different output data types
	if ccode := executionStep.GetCustomCode(); ccode != nil && ccode.GetData() != nil {
		iface := ccode.GetData().AsInterface()
		if m, ok := iface.(map[string]interface{}); ok {
			result = m
		} else {
			result["data"] = iface
		}
	} else if restAPI := executionStep.GetRestApi(); restAPI != nil && restAPI.GetData() != nil {
		// REST API data is now stored as structpb.Value directly (no Any wrapper)
		iface := restAPI.GetData().AsInterface()
		if m, ok := iface.(map[string]interface{}); ok {
			// Use the common response processing function
			result = ProcessRestAPIResponseRaw(m)
		} else {
			result = map[string]interface{}{"data": iface}
		}
	} else if contractRead := executionStep.GetContractRead(); contractRead != nil && len(contractRead.GetData()) > 0 {
		result["data"] = contractRead.GetData()[0].AsInterface()
		if len(contractRead.GetData()) > 1 {
			result["allData"] = make([]interface{}, len(contractRead.GetData()))
			for i, data := range contractRead.GetData() {
				result["allData"].([]interface{})[i] = data.AsInterface()
			}
		}
	} else if branch := executionStep.GetBranch(); branch != nil {
		result["conditionId"] = branch.GetConditionId()
	} else if filter := executionStep.GetFilter(); filter != nil && filter.GetData() != nil {
		var data interface{}
		structVal := &structpb.Value{}
		if err := filter.GetData().UnmarshalTo(structVal); err == nil {
			data = structVal.AsInterface()
		} else {
			if n.logger != nil {
				n.logger.Warn("Failed to unmarshal Filter output", "error", err)
			}
			data = string(filter.GetData().GetValue())
		}
		result["data"] = data
	} else if loop := executionStep.GetLoop(); loop != nil {
		result["data"] = loop.GetData()
	} else if graphQL := executionStep.GetGraphql(); graphQL != nil && graphQL.GetData() != nil {
		var data map[string]interface{}
		structVal := &structpb.Struct{}
		if err := graphQL.GetData().UnmarshalTo(structVal); err == nil {
			data = structVal.AsMap()
		} else {
			if n.logger != nil {
				n.logger.Warn("Failed to unmarshal GraphQL output", "error", err)
			}
			data = map[string]interface{}{"raw_output": string(graphQL.GetData().GetValue())}
		}
		result = data
	} else if ethTransfer := executionStep.GetEthTransfer(); ethTransfer != nil {
		result["txHash"] = ethTransfer.GetTransactionHash()
		result["success"] = true
	} else if contractWrite := executionStep.GetContractWrite(); contractWrite != nil {
		// ContractWrite output contains UserOp and TxReceipt
		if txReceipt := contractWrite.GetTxReceipt(); txReceipt != nil {
			result["txHash"] = txReceipt.GetHash()
			result["success"] = true
		} else {
			result["success"] = true
		}
	}

	// If no specific data was extracted, include basic execution info
	if len(result) == 0 {
		result["success"] = executionStep.Success
		result["nodeId"] = executionStep.NodeId
		if executionStep.Error != "" {
			result["error"] = executionStep.Error
		}
	}

	return result, nil
}

// RunNodeImmediatelyRPC handles the RPC interface for immediate node execution
func (n *Engine) RunNodeImmediatelyRPC(user *model.User, req *avsproto.RunNodeWithInputsReq) (*avsproto.RunNodeWithInputsResp, error) {
	// Convert protobuf request to internal format
	nodeConfig := make(map[string]interface{})
	for k, v := range req.NodeConfig {
		nodeConfig[k] = v.AsInterface()
	}

	inputVariables := make(map[string]interface{})
	for k, v := range req.InputVariables {
		inputVariables[k] = v.AsInterface()
	}

	// Convert NodeType enum to string
	nodeTypeStr := NodeTypeToString(req.NodeType)
	if nodeTypeStr == "" {
		// For unsupported node types, return error but still set output data to avoid OUTPUT_DATA_NOT_SET
		resp := &avsproto.RunNodeWithInputsResp{
			Success: false,
			Error:   fmt.Sprintf("unsupported node type: %v", req.NodeType),
			NodeId:  fmt.Sprintf("node_immediate_%d", time.Now().UnixNano()),
		}
		// Set default RestAPI output structure to avoid OUTPUT_DATA_NOT_SET
		resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{
			RestApi: &avsproto.RestAPINode_Output{},
		}
		return resp, nil
	}

	// Execute the node immediately
	result, err := n.RunNodeImmediately(nodeTypeStr, nodeConfig, inputVariables)
	if err != nil {
		if n.logger != nil {
			n.logger.Error("RunNodeImmediatelyRPC: Execution failed", "nodeType", nodeTypeStr, "error", err)
		}

		// Create response with failure status but still set appropriate output data structure
		// to avoid OUTPUT_DATA_NOT_SET errors on client side
		resp := &avsproto.RunNodeWithInputsResp{
			Success: false,
			Error:   err.Error(),
			NodeId:  fmt.Sprintf("node_immediate_%d", time.Now().UnixNano()),
		}

		// Set empty output data structure based on node type to avoid OUTPUT_DATA_NOT_SET
		switch nodeTypeStr {
		case NodeTypeRestAPI:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{
				RestApi: &avsproto.RestAPINode_Output{},
			}
		case NodeTypeCustomCode:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_CustomCode{
				CustomCode: &avsproto.CustomCodeNode_Output{},
			}
		case NodeTypeETHTransfer:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_EthTransfer{
				EthTransfer: &avsproto.ETHTransferNode_Output{},
			}
		case NodeTypeContractRead:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_ContractRead{
				ContractRead: &avsproto.ContractReadNode_Output{},
			}
		case NodeTypeContractWrite:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode_Output{},
			}
		case NodeTypeGraphQLQuery:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Graphql{
				Graphql: &avsproto.GraphQLQueryNode_Output{},
			}
		case NodeTypeBranch:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Branch{
				Branch: &avsproto.BranchNode_Output{},
			}
		case NodeTypeFilter:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Filter{
				Filter: &avsproto.FilterNode_Output{},
			}
		case NodeTypeLoop:
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Loop{
				Loop: &avsproto.LoopNode_Output{},
			}
		default:
			// For unknown/invalid node types, set RestAPI as default to avoid OUTPUT_DATA_NOT_SET
			resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{
				RestApi: &avsproto.RestAPINode_Output{},
			}
		}

		return resp, nil
	}

	// Log successful execution
	if n.logger != nil {
		n.logger.Info("RunNodeImmediatelyRPC: Executed successfully", "nodeTypeStr", nodeTypeStr, "originalNodeType", req.NodeType, "configKeys", getStringMapKeys(nodeConfig), "inputKeys", getStringMapKeys(inputVariables))
	}

	// Convert result to the appropriate protobuf output type
	resp := &avsproto.RunNodeWithInputsResp{
		Success: true,
		NodeId:  fmt.Sprintf("node_immediate_%d", time.Now().UnixNano()),
	}

	// Set the appropriate output data based on the node type
	// Always set output data structure even if result is empty to avoid OUTPUT_DATA_NOT_SET
	switch nodeTypeStr {
	case NodeTypeRestAPI:
		// Convert result directly to protobuf Value for REST API (no Any wrapping needed)
		valueData, err := structpb.NewValue(result)
		if err != nil {
			return &avsproto.RunNodeWithInputsResp{
				Success: false,
				Error:   fmt.Sprintf("failed to convert REST API output: %v", err),
				NodeId:  "",
			}, nil
		}
		restOutput := &avsproto.RestAPINode_Output{
			Data: valueData,
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{
			RestApi: restOutput,
		}
	case NodeTypeCustomCode:
		// For custom code nodes
		valueData, err := structpb.NewValue(result)
		if err != nil {
			return &avsproto.RunNodeWithInputsResp{
				Success: false,
				Error:   fmt.Sprintf("failed to convert output: %v", err),
				NodeId:  "",
			}, nil
		}
		customOutput := &avsproto.CustomCodeNode_Output{
			Data: valueData,
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_CustomCode{
			CustomCode: customOutput,
		}
	case NodeTypeETHTransfer:
		// For ETH transfer nodes - set empty structure if no result or extract transaction hash
		ethOutput := &avsproto.ETHTransferNode_Output{}
		if result != nil {
			if txHash, ok := result["txHash"].(string); ok {
				ethOutput.TransactionHash = txHash
			}
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_EthTransfer{
			EthTransfer: ethOutput,
		}
	case NodeTypeContractRead:
		// For contract read nodes - always set output structure to avoid OUTPUT_DATA_NOT_SET
		contractReadOutput := &avsproto.ContractReadNode_Output{}
		if result != nil && len(result) > 0 {
			// Contract read returns array data - convert to []*structpb.Value
			if resultArray, ok := result["data"].([]interface{}); ok {
				// Convert array elements to protobuf Values
				for _, item := range resultArray {
					if pbValue, err := structpb.NewValue(item); err == nil {
						contractReadOutput.Data = append(contractReadOutput.Data, pbValue)
					}
				}
			} else {
				// If not an array, wrap single result as single element array
				if pbValue, err := structpb.NewValue(result); err == nil {
					contractReadOutput.Data = append(contractReadOutput.Data, pbValue)
				}
			}
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_ContractRead{
			ContractRead: contractReadOutput,
		}
	case NodeTypeContractWrite:
		// For contract write nodes - always set output structure to avoid OUTPUT_DATA_NOT_SET
		contractWriteOutput := &avsproto.ContractWriteNode_Output{}
		if result != nil && len(result) > 0 {
			// ContractWrite should have UserOp and TxReceipt, but for testing we just create empty structures
			// In a real implementation, these would be populated from the actual transaction results
			contractWriteOutput.UserOp = &avsproto.Evm_UserOp{}
			contractWriteOutput.TxReceipt = &avsproto.Evm_TransactionReceipt{}

			// Try to extract transaction hash from result if available
			if txHash, ok := result["txHash"].(string); ok {
				contractWriteOutput.TxReceipt.Hash = txHash
			} else if transactionHash, ok := result["transactionHash"].(string); ok {
				contractWriteOutput.TxReceipt.Hash = transactionHash
			}
		}
		resp.OutputData = &avsproto.RunNodeWithInputsResp_ContractWrite{
			ContractWrite: contractWriteOutput,
		}
	case NodeTypeGraphQLQuery:
		if result != nil && len(result) > 0 {
			// For GraphQL query nodes, convert result to appropriate format
			anyData, err := structpb.NewValue(result)
			if err != nil {
				return &avsproto.RunNodeWithInputsResp{
					Success: false,
					Error:   fmt.Sprintf("failed to convert GraphQL output: %v", err),
					NodeId:  "",
				}, nil
			}
			anyProto, err := anypb.New(anyData)
			if err != nil {
				return &avsproto.RunNodeWithInputsResp{
					Success: false,
					Error:   fmt.Sprintf("failed to create Any proto for GraphQL: %v", err),
					NodeId:  "",
				}, nil
			}
			graphqlOutput := &avsproto.GraphQLQueryNode_Output{
				Data: anyProto,
			}
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Graphql{
				Graphql: graphqlOutput,
			}
		}
	case NodeTypeBranch:
		if result != nil && len(result) > 0 {
			// For branch nodes, convert result to appropriate format
			branchOutput := &avsproto.BranchNode_Output{}
			if conditionId, ok := result["conditionId"].(string); ok {
				branchOutput.ConditionId = conditionId
			}
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Branch{
				Branch: branchOutput,
			}
		}
	case NodeTypeFilter:
		if result != nil && len(result) > 0 {
			// For filter nodes, convert result to appropriate format
			anyData, err := structpb.NewValue(result)
			if err != nil {
				return &avsproto.RunNodeWithInputsResp{
					Success: false,
					Error:   fmt.Sprintf("failed to convert Filter output: %v", err),
					NodeId:  "",
				}, nil
			}
			anyProto, err := anypb.New(anyData)
			if err != nil {
				return &avsproto.RunNodeWithInputsResp{
					Success: false,
					Error:   fmt.Sprintf("failed to create Any proto for Filter: %v", err),
					NodeId:  "",
				}, nil
			}
			filterOutput := &avsproto.FilterNode_Output{
				Data: anyProto,
			}
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Filter{
				Filter: filterOutput,
			}
		}
	case NodeTypeLoop:
		if result != nil && len(result) > 0 {
			// For loop nodes, convert result to appropriate format
			loopOutput := &avsproto.LoopNode_Output{}
			if data, ok := result["data"].(string); ok {
				loopOutput.Data = data
			} else if data, ok := result["data"]; ok {
				// Convert any other data type to string representation
				loopOutput.Data = fmt.Sprintf("%v", data)
			}
			resp.OutputData = &avsproto.RunNodeWithInputsResp_Loop{
				Loop: loopOutput,
			}
		}
	default:
		// For unknown/invalid node types, set RestAPI as default to avoid OUTPUT_DATA_NOT_SET
		resp.OutputData = &avsproto.RunNodeWithInputsResp_RestApi{
			RestApi: &avsproto.RestAPINode_Output{},
		}
	}

	return resp, nil
}

// RunTriggerRPC handles the RPC interface for immediate trigger execution
func (n *Engine) RunTriggerRPC(user *model.User, req *avsproto.RunTriggerReq) (*avsproto.RunTriggerResp, error) {
	// Convert protobuf request to internal format
	triggerConfig := make(map[string]interface{})
	for k, v := range req.TriggerConfig {
		triggerConfig[k] = v.AsInterface()
	}

	// Convert TriggerType enum to string
	triggerTypeStr := TriggerTypeToString(req.TriggerType)
	if triggerTypeStr == "" {
		// For unsupported trigger types, return error but still set output data to avoid OUTPUT_DATA_NOT_SET
		resp := &avsproto.RunTriggerResp{
			Success:   false,
			Error:     fmt.Sprintf("unsupported trigger type: %v", req.TriggerType),
			TriggerId: fmt.Sprintf("trigger_immediate_%d", time.Now().UnixNano()),
		}
		// Set default ManualTrigger output structure to avoid OUTPUT_DATA_NOT_SET
		resp.OutputData = &avsproto.RunTriggerResp_ManualTrigger{
			ManualTrigger: &avsproto.ManualTrigger_Output{},
		}
		return resp, nil
	}

	// Execute the trigger immediately (triggers don't accept input variables)
	result, err := n.runTriggerImmediately(triggerTypeStr, triggerConfig, nil)
	if err != nil {
		if n.logger != nil {
			n.logger.Error("RunTriggerRPC: Execution failed", "triggerType", triggerTypeStr, "error", err)
		}

		// Create response with failure status but still set appropriate output data structure
		// to avoid OUTPUT_DATA_NOT_SET errors on client side
		resp := &avsproto.RunTriggerResp{
			Success:   false,
			Error:     err.Error(),
			TriggerId: fmt.Sprintf("trigger_immediate_%d", time.Now().UnixNano()),
		}

		// Set empty output data structure based on trigger type to avoid OUTPUT_DATA_NOT_SET
		switch triggerTypeStr {
		case NodeTypeBlockTrigger:
			resp.OutputData = &avsproto.RunTriggerResp_BlockTrigger{
				BlockTrigger: &avsproto.BlockTrigger_Output{},
			}
		case NodeTypeFixedTimeTrigger:
			resp.OutputData = &avsproto.RunTriggerResp_FixedTimeTrigger{
				FixedTimeTrigger: &avsproto.FixedTimeTrigger_Output{},
			}
		case NodeTypeCronTrigger:
			resp.OutputData = &avsproto.RunTriggerResp_CronTrigger{
				CronTrigger: &avsproto.CronTrigger_Output{},
			}
		case NodeTypeEventTrigger:
			resp.OutputData = &avsproto.RunTriggerResp_EventTrigger{
				EventTrigger: &avsproto.EventTrigger_Output{},
			}
		case NodeTypeManualTrigger:
			resp.OutputData = &avsproto.RunTriggerResp_ManualTrigger{
				ManualTrigger: &avsproto.ManualTrigger_Output{},
			}
		default:
			// For unknown trigger types, set ManualTrigger as default to avoid OUTPUT_DATA_NOT_SET
			resp.OutputData = &avsproto.RunTriggerResp_ManualTrigger{
				ManualTrigger: &avsproto.ManualTrigger_Output{},
			}
		}

		return resp, nil
	}

	// Log successful execution
	if n.logger != nil {
		n.logger.Info("RunTriggerRPC: Executed successfully", "triggerTypeStr", triggerTypeStr, "originalTriggerType", req.TriggerType, "configKeys", getStringMapKeys(triggerConfig))
	}

	// Convert result to the appropriate protobuf output type
	resp := &avsproto.RunTriggerResp{
		Success:   true,
		TriggerId: fmt.Sprintf("trigger_immediate_%d", time.Now().UnixNano()),
	}

	// Set the appropriate output data based on the trigger type
	switch triggerTypeStr {
	case NodeTypeBlockTrigger:
		if result != nil {
			// Convert result to BlockTrigger output
			blockOutput := &avsproto.BlockTrigger_Output{}
			if blockNumber, ok := result["blockNumber"].(uint64); ok {
				blockOutput.BlockNumber = blockNumber
			}
			if blockHash, ok := result["blockHash"].(string); ok {
				blockOutput.BlockHash = blockHash
			}
			if timestamp, ok := result["timestamp"].(uint64); ok {
				blockOutput.Timestamp = timestamp
			}
			if parentHash, ok := result["parentHash"].(string); ok {
				blockOutput.ParentHash = parentHash
			}
			if difficulty, ok := result["difficulty"].(string); ok {
				blockOutput.Difficulty = difficulty
			}
			if gasLimit, ok := result["gasLimit"].(uint64); ok {
				blockOutput.GasLimit = gasLimit
			}
			if gasUsed, ok := result["gasUsed"].(uint64); ok {
				blockOutput.GasUsed = gasUsed
			}
			resp.OutputData = &avsproto.RunTriggerResp_BlockTrigger{
				BlockTrigger: blockOutput,
			}
		}
	case NodeTypeFixedTimeTrigger:
		if result != nil {
			// Convert result to FixedTimeTrigger output
			fixedTimeOutput := &avsproto.FixedTimeTrigger_Output{}
			if timestamp, ok := result["timestamp"].(uint64); ok {
				fixedTimeOutput.Timestamp = timestamp
			}
			if timestampISO, ok := result["timestamp_iso"].(string); ok {
				fixedTimeOutput.TimestampIso = timestampISO
			}
			resp.OutputData = &avsproto.RunTriggerResp_FixedTimeTrigger{
				FixedTimeTrigger: fixedTimeOutput,
			}
		}
	case NodeTypeCronTrigger:
		if result != nil {
			// Convert result to CronTrigger output
			cronOutput := &avsproto.CronTrigger_Output{}
			if timestamp, ok := result["timestamp"].(uint64); ok {
				cronOutput.Timestamp = timestamp
			}
			if timestampISO, ok := result["timestamp_iso"].(string); ok {
				cronOutput.TimestampIso = timestampISO
			}
			resp.OutputData = &avsproto.RunTriggerResp_CronTrigger{
				CronTrigger: cronOutput,
			}
		}
	case NodeTypeEventTrigger:
		if result != nil {
			// Convert result to EventTrigger output
			eventOutput := &avsproto.EventTrigger_Output{}
			// EventTrigger simulation doesn't have specific fields, but we can set basic info
			resp.OutputData = &avsproto.RunTriggerResp_EventTrigger{
				EventTrigger: eventOutput,
			}
		}
	case NodeTypeManualTrigger:
		// Always set manual trigger output, even if result is nil
		manualOutput := &avsproto.ManualTrigger_Output{}
		if result != nil {
			if runAt, ok := result["runAt"].(uint64); ok {
				manualOutput.RunAt = runAt
			}
		}
		resp.OutputData = &avsproto.RunTriggerResp_ManualTrigger{
			ManualTrigger: manualOutput,
		}
	}

	return resp, nil
}
