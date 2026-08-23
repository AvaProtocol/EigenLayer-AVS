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

// retryAllowlistSites is every (dispatcher, guard) pair that must wrap its
// runner in executeNodeWithRetry. Coverage is per pair, not a union of
// guards: losing REST retry in executeNode while the loop dispatchers still
// set GetRestApi would otherwise leave both tests green.
//
// executeNodeWithIsolatedVars has no Balance branch because Loop has no
// Balance runner; Balance is a top-level TaskNode type only.
var retryAllowlistSites = []struct{ fn, guard string }{
	{"executeNode", "GetRestApi"},
	{"executeNode", "GetGraphqlQuery"},
	{"executeNode", "GetContractRead"},
	{"executeNode", "GetBalance"},
	{"executeNodeWithIsolatedVars", "GetRestApi"},
	{"executeNodeWithIsolatedVars", "GetGraphqlQuery"},
	{"executeNodeWithIsolatedVars", "GetContractRead"},
	{"executeNodeDirect", "GetRestApi"},
	{"executeNodeDirect", "GetGraphqlQuery"},
	{"executeNodeDirect", "GetContractRead"},
	{"executeNodeDirect", "GetBalance"},
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
		fn    string
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

		fn := enclosingFuncName(stack)
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
		sites = append(sites, site{fn: fn, guard: guard, pos: fset.Position(call.Pos())})
		return true
	})

	if len(sites) == 0 {
		t.Fatal("found no executeNodeWithRetry call sites in vm.go — either the helper was renamed or retry was removed; update this test deliberately")
	}

	found := map[string]bool{}
	for _, s := range sites {
		if s.guard == "" {
			t.Errorf("%s: executeNodeWithRetry is called outside any node-type branch, so the allowlist no longer bounds what can be retried", s.pos)
			continue
		}
		if !retryAllowlistGuards[s.guard] {
			t.Errorf("%s: node branch %s() routes its runner through executeNodeWithRetry, but only idempotent reads may be retried — re-invoking a write can resubmit a UserOp (#676)", s.pos, s.guard)
			continue
		}
		found[s.fn+":"+s.guard] = true
	}

	want := map[string]bool{}
	for _, p := range retryAllowlistSites {
		want[p.fn+":"+p.guard] = true
	}
	var missing []string
	for key := range want {
		if !found[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("lost retry wiring at %s — coverage is per (dispatcher, guard), not a union of guards", strings.Join(missing, ", "))
	}
	var extra []string
	for key := range found {
		if !want[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("unexpected retry wiring at %s — add it to retryAllowlistSites deliberately", strings.Join(extra, ", "))
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

// enclosingFuncName returns the name of the innermost FuncDecl on stack.
func enclosingFuncName(stack []ast.Node) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if fn, ok := stack[i].(*ast.FuncDecl); ok {
			return fn.Name.Name
		}
	}
	return ""
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
