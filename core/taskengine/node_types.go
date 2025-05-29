package taskengine

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
