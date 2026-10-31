package taskengine

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Exercise the real single-node API, including the method-result and step
// finalizers. Any attempt to simulate, estimate, or broadcast is a test failure.
func TestBudgetContractNodeRefusalSurvivesStepFinalization(t *testing.T) {
	for _, expired := range []bool{false, true} {
		for _, simulation := range []bool{false, true} {
			t.Run(fmt.Sprintf("expired=%t/simulation=%t", expired, simulation), func(t *testing.T) {
				engine, db, key, owner, wallet := newPolicyTestEngine(t)
				valid, _ := grantOn(t, engine, key, owner, wallet)
				p, _ := semanticsBudget()
				p.ID, p.SessionSigner, p.Grant = valid.ID, valid.SessionSigner, valid.Grant
				p.Grant.AppliedAt = 1
				p.Owner, p.Runner = &owner, &wallet
				p.NativeSpendCap, p.BudgetStartNative = nil, nil
				if expired {
					p.ValidUntil = time.Now().Add(-time.Minute).UnixMilli()
				}
				require.NoError(t, StoreSessionPolicy(db, p))
				var reads, unexpected atomic.Int32
				rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.NewDecoder(r.Body).Decode(&req) != nil || req.Method != "eth_call" {
						unexpected.Add(1)
						http.Error(w, "unexpected execution RPC", http.StatusBadRequest)
						return
					}
					reads.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": "0x" + fmt.Sprintf("%064x", 99)})
				}))
				t.Cleanup(rpc.Close)
				oldReader := GetChainStateReaderForChain(uint64(p.ChainID))
				unregisterChainStateReader(uint64(p.ChainID))
				t.Cleanup(func() { RegisterChainStateReader(uint64(p.ChainID), oldReader) })
				vm, err := NewVMWithData(nil, nil, &config.SmartWalletConfig{ChainID: p.ChainID, EthRpcUrl: rpc.URL}, nil)
				require.NoError(t, err)
				vm.db, vm.TaskOwner = db, owner
				vm.SetSimulation(simulation)
				data := transferCalldata(testRouterAddr, big.NewInt(100))
				node := &avsproto.TaskNode{Id: "pay", Name: "pay", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE, TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{Config: &avsproto.ContractWriteNode_Config{ChainId: p.ChainID, ContractAddress: testTokenAddr.Hex(), MethodCalls: []*avsproto.ContractWriteNode_MethodCall{{MethodName: "transfer", CallData: &data}, {MethodName: "transfer", CallData: &data}}}}}}
				step, err := vm.RunNodeWithInputs(node, map[string]interface{}{"aa_sender": wallet.Hex(), "settings": map[string]interface{}{"runner": wallet.Hex(), "chain_id": p.ChainID}})
				require.NoError(t, err)
				require.False(t, step.Success)
				want := avsproto.ErrorCode_BUDGET_USED_UP
				if expired {
					want = avsproto.ErrorCode_BUDGET_ENDED
				}
				require.Equal(t, want, step.ErrorCode, step.Error)
				require.NotNil(t, step.ErrorDetails)
				results := stepResults(t, step)
				if simulation {
					require.Len(t, results, 1, "a refused method must stop the following call")
				} else {
					require.Len(t, results, 2, "both atomic batch results carry the refusal")
				}
				if expired {
					require.EqualValues(t, 0, reads.Load())
					require.EqualValues(t, p.ValidUntil, step.ErrorDetails.AsMap()["validUntil"])
				} else {
					require.EqualValues(t, 1, reads.Load())
					require.Equal(t, "99", step.ErrorDetails.AsMap()["remaining"])
					require.Equal(t, testTokenAddr.Hex(), step.ErrorDetails.AsMap()["token"])
				}
				require.Zero(t, unexpected.Load())
			})
		}
	}
}

func TestBudgetETHNodeRefusalSurvivesStepFinalization(t *testing.T) {
	for _, expired := range []bool{false, true} {
		engine, db, key, owner, wallet := newPolicyTestEngine(t)
		p, _ := grantOn(t, engine, key, owner, wallet)
		p.Sizing, p.Status = model.BudgetSizingBudget, model.SessionPolicyActive
		p.Grant.AppliedAt = 1
		p.NativeSpendCap = &model.NativeSpendCap{Amount: "400"}
		destination := common.HexToAddress("0x9999")
		p.NativeRecipients = []*common.Address{&destination}
		if expired {
			p.ValidUntil = time.Now().Add(-time.Minute).UnixMilli()
		}
		require.NoError(t, StoreSessionPolicy(db, p))
		var reads, unexpected atomic.Int32
		rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil || req.Method != "eth_call" {
				unexpected.Add(1)
				http.Error(w, "unexpected execution RPC", http.StatusBadRequest)
				return
			}
			reads.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": fmt.Sprintf("0x%064x", 99)})
		}))
		t.Cleanup(rpc.Close)
		cfg := &config.SmartWalletConfig{ChainID: p.ChainID, EthRpcUrl: rpc.URL, BundlerProvider: config.BundlerProviderAlchemy, AlchemyPaymasterPolicyID: "test-no-request-allowed"}
		vm, err := NewVMWithData(nil, nil, cfg, nil)
		require.NoError(t, err)
		vm.db, vm.TaskOwner = db, owner
		vm.SetSimulation(true)
		node := &avsproto.TaskNode{Id: "pay", Name: "pay", Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER, TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{Config: &avsproto.ETHTransferNode_Config{ChainId: p.ChainID, Destination: destination.Hex(), Amount: "100"}}}}
		step, err := vm.RunNodeWithInputs(node, map[string]interface{}{"aa_sender": wallet.Hex(), "settings": map[string]interface{}{"runner": wallet.Hex(), "chain_id": p.ChainID}})
		require.Error(t, err)
		require.False(t, step.Success)
		if expired {
			require.Equal(t, avsproto.ErrorCode_BUDGET_ENDED, step.ErrorCode, step.Error)
			require.Zero(t, reads.Load())
		} else {
			require.Equal(t, avsproto.ErrorCode_BUDGET_USED_UP, step.ErrorCode, step.Error)
			require.EqualValues(t, 1, reads.Load())
			require.Equal(t, "100", step.ErrorDetails.AsMap()["needed"])
		}
		require.Zero(t, unexpected.Load())
	}
}
