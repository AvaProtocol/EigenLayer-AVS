# Branch Execution Logic: simulateTask vs Deployed Task

## Summary

Based on our investigation, we found that branch node execution was not properly gating successor nodes in `simulateWorkflow`. The issue was in the Kahn scheduler implementation where all nodes were initially scheduled regardless of branch conditions.

## Problem Statement

**Issue**: When a branch condition evaluates to false (e.g., `oracle1.data.latestRoundData.answer < 4000` when price is 4368), subsequent nodes that should only execute on the true path were still being executed in `simulateWorkflow`.

**Expected Behavior**: Only nodes reachable through the selected branch condition should execute.

## Root Cause Analysis

### 1. simulateTask (Kahn Scheduler)
- **Problem**: All nodes were added to the initial ready queue regardless of branch conditions
- **Location**: `core/taskengine/vm.go` - `runKahnScheduler` function
- **Issue**: Branch condition edges like `branch1.0 -> NodeA` were not properly gating NodeA from initial execution

### 2. Deployed Task Execution (Sequential Paths)
- **Status**: Already working correctly via execution plans
- **Location**: `core/taskengine/vm.go` - `executeSequentialPath` function
- **Mechanism**: Uses pre-computed execution plans that respect branch conditions

## Implementation Solution

### Fixed Kahn Scheduler Logic

```go
// 1. Identify targets of branch condition edges (e.g., branch1.0 -> NodeA)
branchTargets := make(map[string]bool)
for _, e := range edges {
    if strings.Contains(e.Source, ".") { // branch condition edge
        branchTargets[e.Target] = true
    }
}

// 2. Add gating predecessor to branch targets
for nodeID := range branchTargets {
    if _, exists := predCount[nodeID]; exists {
        predCount[nodeID]++ // +1 gating predecessor
    }
}

// 3. Don't schedule branch targets initially
for nodeID, c := range predCount {
    if c == 0 {
        if branchTargets[nodeID] {
            continue // still gated by branch until condition selected
        }
        ready <- nodeID
        scheduled[nodeID] = true
        scheduledCount++
    }
}

// 4. When branch executes and selects condition, schedule successors
if selected != nil {
    v.mu.Lock()
    plan, ok := v.plans[selected.NodeID]
    v.mu.Unlock()
    if ok && plan != nil {
        for _, succ := range plan.Next {
            if _, exists := predCount[succ]; exists {
                if predCount[succ] > 0 {
                    predCount[succ]-- // remove branch gating
                }
                if predCount[succ] == 0 && !scheduled[succ] {
                    ready <- succ
                    scheduled[succ] = true
                    scheduledCount++
                }
            }
        }
    }
}
```

## Branch Node Execution Flow

### 1. Condition Evaluation
```go
// Branch evaluates conditions in order
for _, condition := range conditions {
    if condition.Type == "if" {
        result, err := jsvm.RunString(condition.Expression)
        if err == nil && result.ToBoolean() {
            // Condition true - return selected condition step
            return executionStep, &Step{NodeID: fmt.Sprintf("%s.%s", stepID, condition.Id)}, nil
        }
    } else if condition.Type == "else" {
        // Else condition - return selected condition step  
        return executionStep, &Step{NodeID: fmt.Sprintf("%s.%s", stepID, condition.Id)}, nil
    }
}
```

### 2. Output Data Structure
```go
branchData := map[string]interface{}{
    "conditionId": fmt.Sprintf("%s.%s", stepID, condition.Id),
}

branchVarOutput := map[string]interface{}{
    "condition_results": []map[string]interface{}{
        {
            "id":           condition.Id,
            "result":       true,
            "next_node_id": targetNodeId,
        },
    },
}
```

## Test Coverage

### Added Tests
1. **simulateWorkflow Tests**:
   - `condition true -> NodeA only` (NodeB absent)
   - `condition false -> NodeB only` (NodeA absent)

2. **Deployed Workflow Tests**:
   - Same scenarios as above for deployed execution

### Test Structure
```typescript
// Example test workflow
const branch = NodeFactory.create({
  type: NodeType.Branch,
  data: {
    conditions: [
      { id: "on", type: "if", expression: "true" },
      { id: "off", type: "else", expression: "" }
    ]
  }
});

const edges = [
  { source: triggerId, target: branch.id },
  { source: `${branch.id}.on`, target: nodeA.id },   // if path
  { source: `${branch.id}.off`, target: nodeB.id },  // else path
];

// Assert only selected path executes
expect(stepIds).toContain(nodeA.id);
expect(stepIds).not.toContain(nodeB.id);
```

## Key Differences: simulateTask vs Deployed

| Aspect | simulateTask (Kahn) | Deployed (Sequential) |
|--------|-------------------|---------------------|
| **Execution Model** | Parallel DAG traversal | Sequential path following |
| **Branch Handling** | Must gate successors until condition selected | Plans pre-compute correct paths |
| **Concurrency** | Multi-worker parallel execution | Single-threaded sequential |
| **Edge Processing** | Real-time dependency resolution | Pre-computed execution plans |
| **Termination** | When all scheduled nodes complete | When path ends naturally |

## Files Modified

1. **`core/taskengine/vm.go`**: Fixed Kahn scheduler branch gating logic
2. **`ava-sdk-js/tests/nodes/branchNode.test.ts`**: Added comprehensive branch gating tests
3. **`docs/BranchExecutionLogic.md`**: This documentation

## Verification

Run the branch tests to verify both simulateWorkflow and deployed execution respect branch conditions:

```bash
cd /Users/mikasa/Code/ava-sdk-js
yarn test tests/nodes/branchNode.test.ts
```

The tests should pass, confirming that:
- When condition is true, only the "if" path executes
- When condition is false, only the "else" path executes  
- Non-selected branch paths are properly gated and do not execute
