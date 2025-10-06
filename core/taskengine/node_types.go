package taskengine

import (
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
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
	NodeTypeBalance       = "balance"
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
		NodeTypeBalance,
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
	case NodeTypeBalance:
		return avsproto.NodeType_NODE_TYPE_BALANCE
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
	case avsproto.NodeType_NODE_TYPE_BALANCE:
		return NodeTypeBalance
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

// TaskTriggerToConfig extracts configuration from TaskTrigger objects into a map[string]interface{}
// This provides a centralized way to extract trigger configurations and eliminates code duplication
func TaskTriggerToConfig(trigger *avsproto.TaskTrigger) map[string]interface{} {
	triggerConfig := make(map[string]interface{})

	if trigger == nil {
		return triggerConfig
	}

	switch trigger.GetTriggerType().(type) {
	case *avsproto.TaskTrigger_Event:
		eventTrigger := trigger.GetEvent()
		if eventTrigger != nil && eventTrigger.Config != nil {
			// Use the generic protobuf to map converter
			configMap, err := gow.ProtoToMap(eventTrigger.Config)
			if err == nil {
				// Merge the config fields into triggerConfig
				for key, value := range configMap {
					triggerConfig[key] = value
				}
			}
		}
	case *avsproto.TaskTrigger_Block:
		blockTrigger := trigger.GetBlock()
		if blockTrigger != nil && blockTrigger.Config != nil {
			// Use the generic protobuf to map converter for consistency
			configMap, err := gow.ProtoToMap(blockTrigger.Config)
			if err == nil {
				for key, value := range configMap {
					triggerConfig[key] = value
				}
			}
		}
	case *avsproto.TaskTrigger_Cron:
		cronTrigger := trigger.GetCron()
		if cronTrigger != nil && cronTrigger.Config != nil {
			// Use the generic protobuf to map converter for consistency
			configMap, err := gow.ProtoToMap(cronTrigger.Config)
			if err == nil {
				for key, value := range configMap {
					triggerConfig[key] = value
				}
			}
		}
	case *avsproto.TaskTrigger_FixedTime:
		fixedTimeTrigger := trigger.GetFixedTime()
		if fixedTimeTrigger != nil && fixedTimeTrigger.Config != nil {
			// Use the generic protobuf to map converter for consistency
			configMap, err := gow.ProtoToMap(fixedTimeTrigger.Config)
			if err == nil {
				for key, value := range configMap {
					triggerConfig[key] = value
				}
			}
		}
	case *avsproto.TaskTrigger_Manual:
		manualTrigger := trigger.GetManual()
		if manualTrigger != nil && manualTrigger.Config != nil {
			// For ManualTrigger, access protobuf fields directly to preserve structured data
			// This avoids the JSON roundtrip in gow.ProtoToMap that converts arrays/objects to JSON strings
			if manualTrigger.Config.Data != nil {
				// Use .AsInterface() to preserve original data types (objects, arrays, primitives)
				triggerConfig["data"] = manualTrigger.Config.Data.AsInterface()
			}

			// Handle lang field - required for validation
			if manualTrigger.Config.Lang != avsproto.Lang_LANG_UNSPECIFIED {
				triggerConfig["lang"] = manualTrigger.Config.Lang
			}

			// Handle headers
			if len(manualTrigger.Config.Headers) > 0 {
				headers := make(map[string]interface{})
				for k, v := range manualTrigger.Config.Headers {
					headers[k] = v
				}
				triggerConfig["headers"] = headers
			}

			// Handle pathParams
			if len(manualTrigger.Config.PathParams) > 0 {
				pathParams := make(map[string]interface{})
				for k, v := range manualTrigger.Config.PathParams {
					pathParams[k] = v
				}
				triggerConfig["pathParams"] = pathParams
			}
		}
	default:
		// Handle unforeseen trigger types by returning empty configuration
		// This ensures consistent behavior for unknown or future trigger types
		return triggerConfig
	}

	return triggerConfig
}
