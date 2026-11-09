package taskengine

import (
	"fmt"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// validateWorkflowGraphReferences checks new submissions before they are stored
// or any simulation trigger runs. Keep this out of model.ValidateWithError:
// the startup sweep uses that method to fail already-stored workflows.
func validateWorkflowGraphReferences(trigger *avsproto.TaskTrigger, nodes []*avsproto.TaskNode, edges []*avsproto.TaskEdge) error {
	if trigger == nil {
		return fmt.Errorf("workflow trigger is required")
	}

	nodeIDs := make(map[string]bool, len(nodes))
	sources := make(map[string]bool, len(nodes)+1)
	if trigger.Id != "" {
		sources[trigger.Id] = true
	}
	for i, node := range nodes {
		if node == nil || node.Id == "" {
			return fmt.Errorf("node at index %d must have an ID", i)
		}
		if nodeIDs[node.Id] {
			return fmt.Errorf("duplicate node ID %q", node.Id)
		}
		if node.Id == trigger.Id {
			return fmt.Errorf("node ID %q conflicts with the trigger ID", node.Id)
		}
		nodeIDs[node.Id] = true
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
			if trigger.Id == "" {
				return fmt.Errorf("edge %q references unknown source %q; set trigger.id to match the source of trigger edges", edge.Id, edge.Source)
			}
			return fmt.Errorf("edge %q references unknown source %q", edge.Id, edge.Source)
		}
		if !nodeIDs[edge.Target] {
			return fmt.Errorf("edge %q references unknown target node %q", edge.Id, edge.Target)
		}
	}
	return nil
}
