package taskengine

import (
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// NodeType constants define the supported node types in the task engine.
// These constants provide a single source of truth for node types used throughout the codebase,
// ensuring consistency with the protobuf definitions in avs.proto.
const (
	// Trigger node types
	NodeTypeBlockTrigger     = "blockTrigger"
	NodeTypeFixedTimeTrigger = "fixedTimeTrigger"
	NodeTypeCronTrigger      = "cronTrigger"
	NodeTypeEventTrigger     = "eventTrigger"
	NodeTypeManualTrigger    = "manualTrigger"

	// Processing node types
	NodeTypeETHTransfer   = "ethTransfer"
	NodeTypeContractWrite = "contractWrite"
	NodeTypeContractRead  = "contractRead"
	NodeTypeGraphQLQuery  = "graphql"
	NodeTypeRestAPI       = "restApi"
	NodeTypeCustomCode    = "customCode"
	NodeTypeBranch        = "branch"
	NodeTypeFilter        = "filter"
	NodeTypeLoop          = "loop"
)

// AllNodeTypes returns a slice of all supported node types
func AllNodeTypes() []string {
	return []string{
		NodeTypeBlockTrigger,
		NodeTypeFixedTimeTrigger,
		NodeTypeCronTrigger,
		NodeTypeEventTrigger,
		NodeTypeManualTrigger,
		NodeTypeETHTransfer,
		NodeTypeContractWrite,
		NodeTypeContractRead,
		NodeTypeGraphQLQuery,
		NodeTypeRestAPI,
		NodeTypeCustomCode,
		NodeTypeBranch,
		NodeTypeFilter,
		NodeTypeLoop,
	}
}

// IsValidNodeType checks if the given string is a valid node type
func IsValidNodeType(nodeType string) bool {
	for _, validType := range AllNodeTypes() {
		if nodeType == validType {
			return true
		}
	}
	return false
}

// IsTriggerNodeType checks if the given node type is a trigger type
func IsTriggerNodeType(nodeType string) bool {
	triggerTypes := []string{
		NodeTypeBlockTrigger,
		NodeTypeFixedTimeTrigger,
		NodeTypeCronTrigger,
		NodeTypeEventTrigger,
		NodeTypeManualTrigger,
	}

	for _, triggerType := range triggerTypes {
		if nodeType == triggerType {
			return true
		}
	}
	return false
}

// IsProcessingNodeType checks if the given node type is a processing node type
func IsProcessingNodeType(nodeType string) bool {
	return IsValidNodeType(nodeType) && !IsTriggerNodeType(nodeType)
}

// StringToNodeType converts a string node type to protobuf NodeType enum
func StringToNodeType(nodeType string) avsproto.NodeType {
	switch nodeType {
	case NodeTypeETHTransfer:
		return avsproto.NodeType_NODE_TYPE_ETH_TRANSFER
	case NodeTypeContractWrite:
		return avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE
	case NodeTypeContractRead:
		return avsproto.NodeType_NODE_TYPE_CONTRACT_READ
	case NodeTypeGraphQLQuery:
		return avsproto.NodeType_NODE_TYPE_GRAPHQL_QUERY
	case NodeTypeRestAPI:
		return avsproto.NodeType_NODE_TYPE_REST_API
	case NodeTypeCustomCode:
		return avsproto.NodeType_NODE_TYPE_CUSTOM_CODE
	case NodeTypeBranch:
		return avsproto.NodeType_NODE_TYPE_BRANCH
	case NodeTypeFilter:
		return avsproto.NodeType_NODE_TYPE_FILTER
	case NodeTypeLoop:
		return avsproto.NodeType_NODE_TYPE_LOOP
	default:
		return avsproto.NodeType_NODE_TYPE_UNSPECIFIED
	}
}

// NodeTypeToString converts a protobuf NodeType enum to string
func NodeTypeToString(nodeType avsproto.NodeType) string {
	switch nodeType {
	case avsproto.NodeType_NODE_TYPE_ETH_TRANSFER:
		return NodeTypeETHTransfer
	case avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE:
		return NodeTypeContractWrite
	case avsproto.NodeType_NODE_TYPE_CONTRACT_READ:
		return NodeTypeContractRead
	case avsproto.NodeType_NODE_TYPE_GRAPHQL_QUERY:
		return NodeTypeGraphQLQuery
	case avsproto.NodeType_NODE_TYPE_REST_API:
		return NodeTypeRestAPI
	case avsproto.NodeType_NODE_TYPE_CUSTOM_CODE:
		return NodeTypeCustomCode
	case avsproto.NodeType_NODE_TYPE_BRANCH:
		return NodeTypeBranch
	case avsproto.NodeType_NODE_TYPE_FILTER:
		return NodeTypeFilter
	case avsproto.NodeType_NODE_TYPE_LOOP:
		return NodeTypeLoop
	default:
		return ""
	}
}

// StringToTriggerType converts a string trigger type to protobuf TriggerType enum
func StringToTriggerType(triggerType string) avsproto.TriggerType {
	switch triggerType {
	case NodeTypeManualTrigger:
		return avsproto.TriggerType_TRIGGER_TYPE_MANUAL
	case NodeTypeFixedTimeTrigger:
		return avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME
	case NodeTypeCronTrigger:
		return avsproto.TriggerType_TRIGGER_TYPE_CRON
	case NodeTypeBlockTrigger:
		return avsproto.TriggerType_TRIGGER_TYPE_BLOCK
	case NodeTypeEventTrigger:
		return avsproto.TriggerType_TRIGGER_TYPE_EVENT
	default:
		return avsproto.TriggerType_TRIGGER_TYPE_UNSPECIFIED
	}
}

// TriggerTypeToString converts a protobuf TriggerType enum to string
func TriggerTypeToString(triggerType avsproto.TriggerType) string {
	switch triggerType {
	case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
		return NodeTypeManualTrigger
	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
		return NodeTypeFixedTimeTrigger
	case avsproto.TriggerType_TRIGGER_TYPE_CRON:
		return NodeTypeCronTrigger
	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		return NodeTypeBlockTrigger
	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		return NodeTypeEventTrigger
	default:
		return ""
	}
}

// TaskTriggerToTriggerType converts from TaskTrigger oneof types to TriggerType enum
func TaskTriggerToTriggerType(taskTrigger *avsproto.TaskTrigger) avsproto.TriggerType {
	if taskTrigger == nil {
		return avsproto.TriggerType_TRIGGER_TYPE_UNSPECIFIED
	}

	switch taskTrigger.GetTriggerType().(type) {
	case *avsproto.TaskTrigger_Manual:
		return avsproto.TriggerType_TRIGGER_TYPE_MANUAL
	case *avsproto.TaskTrigger_FixedTime:
		return avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME
	case *avsproto.TaskTrigger_Cron:
		return avsproto.TriggerType_TRIGGER_TYPE_CRON
	case *avsproto.TaskTrigger_Block:
		return avsproto.TriggerType_TRIGGER_TYPE_BLOCK
	case *avsproto.TaskTrigger_Event:
		return avsproto.TriggerType_TRIGGER_TYPE_EVENT
	default:
		return avsproto.TriggerType_TRIGGER_TYPE_UNSPECIFIED
	}
}
