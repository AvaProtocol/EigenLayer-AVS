package taskengine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// retryAllowlistGuards is the complete set of node-type guards whose branch may
// route its runner through executeNodeWithRetry: idempotent reads only.
//
// ContractWrite and EthTransfer are absent by design. Retry re-invokes the
// runner, and an ambiguous confirmation on a write would resubmit the UserOp
// (issue #676), so those branches must always call their runner directly.
var retryAllowlistGuards = map[string]bool{
	"GetRestApi":      true,
	"GetGraphqlQuery": true,
	"GetContractRead": true,
	"GetBalance":      true,
}

// TestRetryAllowlist_WriteNodesRunExactlyOnce is the test the safety argument
// rests on.
//
// The #676 guarantee is structural: retries exist only where a dispatch branch
// opts into them, so a write node runs its runner exactly once no matter what
// retry_policy it carries. Nothing about that is checked by the type system —
// wiring `v.runContractWrite` through `executeNodeWithRetry` in a later
// refactor would compile, pass every other test, and silently make double
// submission possible. This reads the dispatchers' own source and fails if any
// call to executeNodeWithRetry sits in a branch outside the allowlist.
func TestRetryAllowlist_WriteNodesRunExactlyOnce(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "vm.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing vm.go: %v", err)
	}

	type site struct {
		guard string
		pos   token.Position
	}
	var sites []site

	// Track the enclosing if-statements so each retry call can be attributed to
	// the node-type guard that admits it. An else-if is its own IfStmt inside
	// the parent's Else, so the *innermost* IfStmt whose Body (not Else)
	// contains the call is the branch that runs it.
	var stack []ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		stack = append(stack, n)

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "executeNodeWithRetry" {
			return true
		}

		guard := ""
		for i := len(stack) - 1; i >= 0; i-- {
			ifStmt, ok := stack[i].(*ast.IfStmt)
			if !ok {
				continue
			}
			if call.Pos() < ifStmt.Body.Pos() || call.Pos() > ifStmt.Body.End() {
				// The call is in this if's Else — an else-if we have already
				// attributed, or will attribute, to its own IfStmt.
				continue
			}
			guard = nodeTypeGuard(ifStmt.Cond)
			break
		}
		sites = append(sites, site{guard: guard, pos: fset.Position(call.Pos())})
		return true
	})

	if len(sites) == 0 {
		t.Fatal("found no executeNodeWithRetry call sites in vm.go — either the helper was renamed or retry was removed; update this test deliberately")
	}

	covered := map[string]bool{}
	for _, s := range sites {
		if s.guard == "" {
			t.Errorf("%s: executeNodeWithRetry is called outside any node-type branch, so the allowlist no longer bounds what can be retried", s.pos)
			continue
		}
		if !retryAllowlistGuards[s.guard] {
			t.Errorf("%s: node branch %s() routes its runner through executeNodeWithRetry, but only idempotent reads may be retried — re-invoking a write can resubmit a UserOp (#676)", s.pos, s.guard)
			continue
		}
		covered[s.guard] = true
	}

	// The allowlist must not silently shrink either: a read branch that loses
	// its retry wiring makes the feature a no-op there with no other signal.
	var missing []string
	for guard := range retryAllowlistGuards {
		if !covered[guard] {
			missing = append(missing, guard)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("no dispatch branch retries %s — the retry allowlist lost a read node type", strings.Join(missing, ", "))
	}
}

// TestRetryAllowlist_WriteRunnersNeverWrapped is the same guarantee read from
// the other direction: the write runners must only ever be called directly.
func TestRetryAllowlist_WriteRunnersNeverWrapped(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "vm.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing vm.go: %v", err)
	}

	writeRunners := map[string]bool{"runContractWrite": true, "runEthTransfer": true}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "executeNodeWithRetry" {
			return true
		}
		for _, arg := range call.Args {
			argSel, ok := arg.(*ast.SelectorExpr)
			if ok && writeRunners[argSel.Sel.Name] {
				t.Errorf("%s: %s is passed to executeNodeWithRetry; on-chain writes must never be re-invoked (#676)",
					fset.Position(call.Pos()), argSel.Sel.Name)
			}
		}
		return true
	})
}

// TestNodeSupportsRetry pins which node shapes may carry a policy at all. This
// is what CreateTask validates against, so a write node cannot even be stored
// with a policy that looks like it works.
func TestNodeSupportsRetry(t *testing.T) {
	tests := []struct {
		name string
		node *avsproto.TaskNode
		want bool
	}{
		{"nil node", nil, false},
		{"rest", restTestNode(), true},
		{"contract write", writeTestNode(), false},
		{"eth transfer", transferTestNode(), false},
		{"custom code", customCodeTestNode(), false},
		{"loop over rest", loopOverRestTestNode(), true},
		{"loop over contract write", loopOverWriteTestNode(), false},
		{"graphql", &avsproto.TaskNode{TaskType: &avsproto.TaskNode_GraphqlQuery{GraphqlQuery: &avsproto.GraphQLQueryNode{}}}, true},
		{"contract read", &avsproto.TaskNode{TaskType: &avsproto.TaskNode_ContractRead{ContractRead: &avsproto.ContractReadNode{}}}, true},
		{"balance", &avsproto.TaskNode{TaskType: &avsproto.TaskNode_Balance{Balance: &avsproto.BalanceNode{}}}, true},
		{"branch", &avsproto.TaskNode{TaskType: &avsproto.TaskNode_Branch{Branch: &avsproto.BranchNode{}}}, false},
		{"filter", &avsproto.TaskNode{TaskType: &avsproto.TaskNode_Filter{Filter: &avsproto.FilterNode{}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeSupportsRetry(tt.node); got != tt.want {
				t.Fatalf("nodeSupportsRetry(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// nodeTypeGuard extracts the "GetX" method name from a branch condition of the
// form `node.GetX() != nil`, or "" when the condition is not of that shape.
func nodeTypeGuard(cond ast.Expr) string {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return ""
	}
	call, ok := bin.X.(*ast.CallExpr)
	if !ok {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !strings.HasPrefix(sel.Sel.Name, "Get") {
		return ""
	}
	return sel.Sel.Name
}
