package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
)

func TestWorkflowGraphReferences(t *testing.T) {
	tests := []struct {
		name   string
		change func(*avsproto.Task)
		want   string
	}{
		{name: "matching trigger and node IDs"},
		{
			name:   "missing trigger ID from Sentry 3F",
			change: func(task *avsproto.Task) { task.Trigger.Id = "" },
			want:   `edge "e1" references unknown source "__trigger"; set trigger.id`,
		},
		{
			name:   "mismatched trigger ID",
			change: func(task *avsproto.Task) { task.Trigger.Id = "anotherTrigger" },
			want:   `edge "e1" references unknown source "__trigger"`,
		},
		{
			name: "trigger ID cannot be mistaken for a branch output",
			change: func(task *avsproto.Task) {
				task.Trigger.Id = "start.trigger"
				task.Edges[0].Source = task.Trigger.Id
			},
			want: `trigger ID "start.trigger" must not contain '.'`,
		},
		{
			name: "node ID cannot be mistaken for a branch output",
			change: func(task *avsproto.Task) {
				task.Nodes[0].Id = "code.1"
				task.Edges[0].Target = "code.1"
				task.Edges = append(task.Edges, &avsproto.TaskEdge{Id: "e2", Source: "code.1", Target: "branch"})
			},
			want: `node ID "code.1" must not contain '.'`,
		},
		{
			name:   "dangling target",
			change: func(task *avsproto.Task) { task.Edges[0].Target = "missing" },
			want:   `edge "e1" references unknown target node "missing"`,
		},
		{
			name: "branch condition is a valid source",
			change: func(task *avsproto.Task) {
				task.Edges = append(task.Edges, &avsproto.TaskEdge{Id: "e2", Source: "branch.yes", Target: "c1"})
			},
		},
		{
			name: "unknown branch condition is not a source",
			change: func(task *avsproto.Task) {
				task.Edges = append(task.Edges, &avsproto.TaskEdge{Id: "e2", Source: "branch.missing", Target: "c1"})
			},
			want: `edge "e2" references unknown source "branch.missing"`,
		},
		{
			name: "stale Studio condition gets recovery guidance",
			change: func(task *avsproto.Task) {
				task.Nodes[1].GetBranch().Config.Conditions = []*avsproto.BranchNode_Condition{{Id: "0"}, {Id: "1"}}
				task.Edges = append(task.Edges, &avsproto.TaskEdge{Id: "e2", Source: "branch.2", Target: "c1"})
			},
			want: `branch "branch" has no condition "2"; remove the stale edge or reconnect it to an existing condition`,
		},
		{
			name: "branch condition IDs do not have to be consecutive",
			change: func(task *avsproto.Task) {
				task.Nodes[1].GetBranch().Config.Conditions = []*avsproto.BranchNode_Condition{{Id: "0"}, {Id: "2"}}
				task.Edges = append(task.Edges, &avsproto.TaskEdge{Id: "e2", Source: "branch.2", Target: "c1"})
			},
		},
		{
			name: "dotted source must belong to a branch",
			change: func(task *avsproto.Task) {
				task.Edges = append(task.Edges, &avsproto.TaskEdge{Id: "e2", Source: "c1.yes", Target: "branch"})
			},
			want: `edge "e2" references unknown source "c1.yes"`,
		},
		{
			name: "trigger ID stays optional for node-only edges",
			change: func(task *avsproto.Task) {
				task.Trigger.Id = ""
				task.Edges[0].Source = "c1"
				task.Edges[0].Target = "branch"
			},
		},
		{
			name: "duplicate IDs cannot collapse the node map",
			change: func(task *avsproto.Task) {
				task.Nodes[1].Id = "c1"
			},
			want: `duplicate node ID "c1"`,
		},
		{
			name:   "node cannot shadow the trigger",
			change: func(task *avsproto.Task) { task.Trigger.Id = "c1" },
			want:   `node ID "c1" conflicts with the trigger ID`,
		},
		{
			name:   "null node is a validation error",
			change: func(task *avsproto.Task) { task.Nodes[0] = nil },
			want:   "node at index 0 must have an ID",
		},
		{
			name:   "null edge is a validation error",
			change: func(task *avsproto.Task) { task.Edges[0] = nil },
			want:   "edge at index 0 must not be null",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &avsproto.Task{
				Trigger: &avsproto.TaskTrigger{Id: "__trigger"},
				Nodes: []*avsproto.TaskNode{
					{Id: "c1"},
					{Id: "branch", TaskType: &avsproto.TaskNode_Branch{Branch: &avsproto.BranchNode{
						Config: &avsproto.BranchNode_Config{Conditions: []*avsproto.BranchNode_Condition{
							{Id: "yes"},
						}},
					}}},
				},
				Edges: []*avsproto.TaskEdge{{Id: "e1", Source: "__trigger", Target: "c1"}},
			}
			if tt.change != nil {
				tt.change(task)
			}
			err := validateWorkflowGraphReferences(task.Trigger, task.Nodes, task.Edges)
			if tt.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.want)
			}
		})
	}
}
