package taskengine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/require"
)

func TestBudgetWorkflowAuthorityPauseUsesDetailWithoutCodePrefix(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		name := "direct"
		if wrapped {
			name = "wrapped"
		}
		t.Run(name, func(t *testing.T) {
			f, p, backend, signature := newBudgetChangeTest(t)
			_, err := f.n.SubmitBudgetPolicyPreparation(context.Background(), f.user, p.Wallet, p.ChainID, p.ID, signature)
			require.NoError(t, err)
			backend.observation = budgetMined(p, true)
			active, err := f.n.GetBudgetPolicyPreparation(f.user, p.Wallet, p.ChainID, p.ID)
			require.NoError(t, err)
			require.Equal(t, budgetChangeActive, active.State)
			detail := "This wallet has an unrecognized validation authority. Remove it, then check again."
			backend.authorityError = &BudgetPolicyError{Code: BudgetAuthorityUnknownCode, Detail: detail}
			if wrapped {
				backend.authorityError = fmt.Errorf("private inventory context: %w", backend.authorityError)
			}
			task := manualCustomCodeTask(f.user.Address.Hex(), p.Wallet.Hex(), model.GenerateID())
			task.Nodes = append(task.Nodes, &avsproto.TaskNode{
				Id: "write1", Name: "transfer", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
				TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{
					Config: &avsproto.ContractWriteNode_Config{ChainId: testPolicyChain, ContractAddress: testTokenAddr.Hex()},
				}},
			})
			task.Edges = append(task.Edges, &avsproto.TaskEdge{Id: "to_write", Source: task.Nodes[0].Id, Target: "write1"})
			lease, err := f.n.budgetWorkflowLease(context.Background(), task)
			require.Nil(t, lease)
			var paused *BudgetPolicyError
			require.ErrorAs(t, err, &paused)
			require.Equal(t, BudgetChangingCode, paused.Code)
			require.Contains(t, paused.Detail, detail)
			require.Contains(t, paused.Detail, "paused")
			require.NotContains(t, paused.Detail, BudgetAuthorityUnknownCode+":")
			require.NotContains(t, paused.Detail, "private inventory context")
			require.Equal(t, 5*time.Second, paused.RetryAfter())
			require.EqualValues(t, 1, backend.sent.Load(), "only the earlier offline install was relayed")
		})
	}
}

type budgetAdmissionStorage struct {
	storage.Storage
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (s *budgetAdmissionStorage) Set(key, value []byte) error {
	if strings.HasPrefix(string(key), "trigger:") {
		s.once.Do(func() {
			close(s.entered)
			<-s.resume
		})
	}
	return s.Storage.Set(key, value)
}

func TestBudgetWorkflowAdmissionHeldThroughPendingSetupAndBlockingRun(t *testing.T) {
	for _, blocking := range []bool{false, true} {
		name := "queued"
		if blocking {
			name = "blocking"
		}
		t.Run(name, func(t *testing.T) {
			f := newBudgetPrepareFixture(t)
			task := manualCustomCodeTask(f.user.Address.Hex(), f.in.Wallet.Hex(), model.GenerateID())
			// A real graph with a fund-moving branch that is not taken: the
			// workflow needs admission, but this test never broadcasts funds.
			task.Nodes = []*avsproto.TaskNode{{
				Id: "branch1", Name: "check", Type: avsproto.NodeType_NODE_TYPE_BRANCH,
				TaskType: &avsproto.TaskNode_Branch{Branch: &avsproto.BranchNode{Config: &avsproto.BranchNode_Config{
					Conditions: []*avsproto.BranchNode_Condition{{Id: "send", Type: "if", Expression: "false"}},
				}}},
			}, {
				Id: "write1", Name: "transfer", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
				TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{
					Config: &avsproto.ContractWriteNode_Config{ChainId: testPolicyChain, ContractAddress: testTokenAddr.Hex()},
				}},
			}}
			task.Edges = []*avsproto.TaskEdge{{Id: "e1", Source: task.Trigger.Id, Target: "branch1"}, {Id: "e2", Source: "branch1.send", Target: "write1"}}
			raw, err := task.ToJSON()
			require.NoError(t, err)
			require.NoError(t, f.n.db.Set(f.n.findTaskKey(task.Id, task.Status), raw))
			store := &budgetAdmissionStorage{Storage: f.n.db, entered: make(chan struct{}), resume: make(chan struct{})}
			f.n.db = store
			resume := sync.OnceFunc(func() { close(store.resume) })
			t.Cleanup(resume)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type result struct {
				response *avsproto.TriggerTaskResp
				err      error
			}
			done := make(chan result, 1)
			finished := make(chan struct{})
			t.Cleanup(func() {
				cancel()
				resume()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("trigger did not stop during cleanup")
				}
			})
			go func() {
				defer close(finished)
				response, triggerErr := f.n.TriggerWorkflowWithContext(ctx, f.user, &avsproto.TriggerTaskReq{
					TaskId: task.Id, TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK, IsBlocking: blocking,
				})
				done <- result{response, triggerErr}
			}()
			select {
			case <-store.entered:
			case <-ctx.Done():
				t.Fatal("trigger did not reach admission setup")
			}
			// Submit takes this same gate. It must not publish a hold in the
			// gap between the pending marker and execution/enqueue admission.
			probe, stopProbe := context.WithTimeout(context.Background(), 30*time.Millisecond)
			unlock, err := f.n.budgetWorkflowLocks.acquire(probe, budgetGateKey(testPolicyChain, f.user.Address, f.in.Wallet))
			stopProbe()
			if err == nil {
				unlock()
			}
			require.ErrorIs(t, err, context.DeadlineExceeded)
			resume()
			select {
			case result := <-done:
				require.NoError(t, result.err, "blocking execution must reuse admission rather than deadlock reacquiring it")
				require.NotNil(t, result.response)
				if blocking {
					require.NotEqual(t, avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING, result.response.Status)
				}
			case <-ctx.Done():
				t.Fatal("trigger deadlocked after admission")
			}
			unlock, err = f.n.budgetWorkflowLocks.acquire(ctx, budgetGateKey(testPolicyChain, f.user.Address, f.in.Wallet))
			require.NoError(t, err)
			unlock()
		})
	}
}
