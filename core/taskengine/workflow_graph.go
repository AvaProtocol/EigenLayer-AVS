package taskengine

import (
	"fmt"
	"strings"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// validateWorkflowGraphReferences checks new submissions before they are stored
// or any simulation trigger runs. Keep this out of model.ValidateWithError:
// the startup sweep uses that method to fail already-stored workflows.
func validateWorkflowGraphReferences(trigger *avsproto.TaskTrigger, nodes []*avsproto.TaskNode, edges []*avsproto.TaskEdge) error {
	if trigger == nil {
		return fmt.Errorf("workflow trigger is required")
	}
	// Compile and the scheduler classify every dotted source as a branch
	// output, so a real node or trigger ID must not use that separator.
	if strings.Contains(trigger.Id, ".") {
		return fmt.Errorf("trigger ID %q must not contain '.'; dots are reserved for branch condition sources", trigger.Id)
	}

	nodesByID := make(map[string]*avsproto.TaskNode, len(nodes))
	sources := make(map[string]bool, len(nodes)+1)
	if trigger.Id != "" {
		sources[trigger.Id] = true
	}
	for i, node := range nodes {
		if node == nil || node.Id == "" {
			return fmt.Errorf("node at index %d must have an ID", i)
		}
		if strings.Contains(node.Id, ".") {
			return fmt.Errorf("node ID %q must not contain '.'; dots are reserved for branch condition sources", node.Id)
		}
		if nodesByID[node.Id] != nil {
			return fmt.Errorf("duplicate node ID %q", node.Id)
		}
		if node.Id == trigger.Id {
			return fmt.Errorf("node ID %q conflicts with the trigger ID", node.Id)
		}
		nodesByID[node.Id] = node
		sources[node.Id] = true

		// Branch edges originate at <node ID>.<condition ID>, rather than
		// at another TaskNode. A dot alone does not make a valid source.
		for _, condition := range node.GetBranch().GetConfig().GetConditions() {
			if condition.GetId() != "" {
				sources[node.Id+"."+condition.Id] = true
			}
		}
	}

	for i, edge := range edges {
		if edge == nil {
			return fmt.Errorf("edge at index %d must not be null", i)
		}
		if !sources[edge.Source] {
			if branchID, conditionID, isCondition := strings.Cut(edge.Source, "."); isCondition && nodesByID[branchID].GetBranch() != nil {
				return fmt.Errorf("edge %q references unknown source %q: branch %q has no condition %q; remove the stale edge or reconnect it to an existing condition", edge.Id, edge.Source, branchID, conditionID)
			}
			if trigger.Id == "" {
				return fmt.Errorf("edge %q references unknown source %q; set trigger.id to match the source of trigger edges", edge.Id, edge.Source)
			}
			return fmt.Errorf("edge %q references unknown source %q", edge.Id, edge.Source)
		}
		if nodesByID[edge.Target] == nil {
			return fmt.Errorf("edge %q references unknown target node %q", edge.Id, edge.Target)
		}
	}
	return nil
}
