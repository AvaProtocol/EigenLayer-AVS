package taskengine

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

type ownerBackendFixture struct {
	prepared       atomic.Int32
	sent           atomic.Int32
	lookups        atomic.Int32
	validateError  error
	sendError      error
	signed         *userop.UserOperationV07
	userOpKnown    bool
	userOpKnownErr error
	// enteredSubmit is closed on the first Submit, before releaseSubmit.
	// Tests use the pair to overlap a second caller, or to cancel the
	// request, while the relay is inside the bundler call.
	enteredSubmit chan struct{}
	releaseSubmit chan struct{}
	enterOnce     sync.Once
	submitCtxErr  error
}

func (f *ownerBackendFixture) Prepare(_ context.Context, _ *config.SmartWalletConfig, _, sender common.Address, _ *big.Int, calls []aa.Call, _ logger.Logger) (*preset.PreparedOwnerUserOp, error) {
	f.prepared.Add(1)
	packed, err := aa.PackExecuteBatchMAv2(calls)
	if err != nil {
		return nil, err
	}
	nonce, err := userop.EncodeNonceMAv2(0, userop.ValidationOptionGlobal, 0)
	if err != nil {
		return nil, err
	}
	return &preset.PreparedOwnerUserOp{Operation: &userop.UserOperationV07{Sender: sender, Nonce: nonce, CallData: packed, CallGasLimit: big.NewInt(100000), VerificationGasLimit: big.NewInt(50000), PreVerificationGas: big.NewInt(30000), MaxFeePerGas: big.NewInt(1000000000), MaxPriorityFeePerGas: big.NewInt(100000000)}}, nil
}
func (f *ownerBackendFixture) Validate(context.Context, *config.SmartWalletConfig, common.Address, *big.Int, *preset.PreparedOwnerUserOp) error {
	return f.validateError
}
func (f *ownerBackendFixture) UserOpKnown(context.Context, *config.SmartWalletConfig, common.Hash) (bool, error) {
	f.lookups.Add(1)
	return f.userOpKnown, f.userOpKnownErr
}

func (f *ownerBackendFixture) Submit(ctx context.Context, sw *config.SmartWalletConfig, op *userop.UserOperationV07) (common.Hash, error) {
	if f.enteredSubmit != nil {
		f.enterOnce.Do(func() { close(f.enteredSubmit) })
	}
	if f.releaseSubmit != nil {
		<-f.releaseSubmit
	}
	f.submitCtxErr = ctx.Err()
	f.sent.Add(1)
	copy := *op
	copy.Signature = append([]byte(nil), op.Signature...)
	f.signed = &copy
	hash, err := op.GetUserOpHash(preset.EntryPointV07(), big.NewInt(sw.ChainID))
	if f.sendError != nil {
		return common.Hash{}, f.sendError
	}
	return hash, err
}

func ownerEngineFixture(t *testing.T) (*Engine, *model.User, *ecdsa.PrivateKey, *ownerBackendFixture) {
	t.Helper()
	db, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	owner := crypto.PubkeyToAddress(key.PublicKey)
	sender := common.HexToAddress("0x2222222222222222222222222222222222222222")
	factory := aa.MAv2FactoryAddress()
	require.NoError(t, StoreWallet(db, 11155111, owner, &model.SmartWallet{Owner: &owner, Address: &sender, Factory: &factory, Salt: big.NewInt(0)}))
	backend := &ownerBackendFixture{}
	// No live configuration, controller, session key, worker or chain is used.
	n := New(db, &config.Config{OwnerExecutionEnabled: true, SmartWallet: &config.SmartWalletConfig{ChainID: 11155111, EthRpcUrl: "http://127.0.0.1:1", DisableGasSponsorship: true}}, nil, testutil.GetLogger())
	t.Cleanup(n.notificationTicker.Stop)
	n.ownerExecutionBackend = backend
	return n, &model.User{Address: owner, SmartAccountAddress: &sender, ChainID: 11155111}, key, backend
}

func ownerWorkflowFixture() (*avsproto.TaskTrigger, []*avsproto.TaskNode, []*avsproto.TaskEdge, map[string]interface{}) {
	wethTarget := "0x7777777777777777777777777777777777777777"
	trigger := &avsproto.TaskTrigger{Id: "start", Name: "start", Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL, TriggerType: &avsproto.TaskTrigger_Manual{Manual: &avsproto.ManualTrigger{Config: &avsproto.ManualTrigger_Config{Lang: avsproto.Lang_LANG_JSON, Data: structpb.NewStructValue(&structpb.Struct{})}}}}
	abiValue, _ := structpb.NewValue(map[string]interface{}{"type": "function", "name": "transfer", "inputs": []interface{}{map[string]interface{}{"name": "to", "type": "address"}, map[string]interface{}{"name": "amount", "type": "uint256"}}, "outputs": []interface{}{map[string]interface{}{"name": "", "type": "bool"}}})
	nodes := []*avsproto.TaskNode{
		{Id: "amounts", Name: "amounts", Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE, TaskType: &avsproto.TaskNode_CustomCode{CustomCode: &avsproto.CustomCodeNode{Config: &avsproto.CustomCodeNode_Config{Lang: avsproto.Lang_LANG_JAVASCRIPT, Source: "return {usdc: '1000000', weth: '20000000000000000'};"}}}},
		{Id: "transfer", Name: "transfer", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE, TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{Config: &avsproto.ContractWriteNode_Config{ChainId: 11155111, ContractAddress: "0x6666666666666666666666666666666666666666", ContractAbi: []*structpb.Value{abiValue}, MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
			{MethodName: "transfer", MethodParams: []string{"{{settings.destination}}", "{{amounts.data.usdc}}"}},
			{MethodName: "transfer", ContractAddress: &wethTarget, MethodParams: []string{"{{settings.destination}}", "{{amounts.data.weth}}"}},
		}}}}},
	}
	edges := []*avsproto.TaskEdge{{Id: "read", Source: "start", Target: "amounts"}, {Id: "write", Source: "amounts", Target: "transfer"}}
	input := map[string]interface{}{"settings": map[string]interface{}{"name": "Owner sweep test", "runner": "0x2222222222222222222222222222222222222222", "destination": "0x8888888888888888888888888888888888888888", "chain": "sepolia"}}
	return trigger, nodes, edges, input
}

func prepareOwnerFixture(t *testing.T, n *Engine, u *model.User) *OwnerWorkflowPreparation {
	t.Helper()
	trigger, nodes, edges, input := ownerWorkflowFixture()
	p, err := n.PrepareOwnerWorkflow(context.Background(), u, trigger, nodes, edges, input, 11155111)
	require.NoError(t, err)
	return p
}

func signOwnerFixture(t *testing.T, p *OwnerWorkflowPreparation, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	sig, err := crypto.Sign(accounts.TextHash(common.HexToHash(p.UserOpHash).Bytes()), key)
	require.NoError(t, err)
	return sig
}

func TestOwnerWorkflowResolvesCallsAndSubmitsExactlyOnce(t *testing.T) {
	n, user, key, backend := ownerEngineFixture(t)
	p := prepareOwnerFixture(t, n, user)
	require.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING, p.Execution.Status)
	require.Empty(t, p.Operation.Signature)
	require.Len(t, p.Calls, 2)
	require.Equal(t, "0x6666666666666666666666666666666666666666", p.Calls[0].To)
	require.Equal(t, "0x7777777777777777777777777777777777777777", p.Calls[1].To)
	require.Equal(t, "1000000", new(big.Int).SetBytes(common.FromHex(p.Calls[0].Data)[36:68]).String())
	require.Equal(t, "20000000000000000", new(big.Int).SetBytes(common.FromHex(p.Calls[1].Data)[36:68]).String())
	for _, step := range p.Execution.Steps {
		require.NotEqual(t, "transfer", step.Id, "unexecuted writes must not appear completed")
	}
	require.Zero(t, backend.sent.Load())
	sig := signOwnerFixture(t, p, key)
	var wg sync.WaitGroup
	errorsCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
			errorsCh <- err
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, backend.sent.Load())
	wrongSignature := append([]byte(nil), sig...)
	wrongSignature[0] ^= 1
	_, err := n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, wrongSignature)
	require.Equal(t, codes.InvalidArgument, status.Code(err), "a cached submitted result still requires a valid signature")
	require.Len(t, backend.signed.Signature, 67)
	hash, err := backend.signed.GetUserOpHash(preset.EntryPointV07(), big.NewInt(11155111))
	require.NoError(t, err)
	require.Equal(t, p.UserOpHash, hash.Hex(), "stored operation must round-trip with exact nonce/fees/calldata")
	stored, err := n.db.GetKey(ownerExecutionKey(user.Address, p.Execution.Id))
	require.NoError(t, err)
	var record ownerExecutionRecord
	require.NoError(t, json.Unmarshal(stored, &record))
	require.Empty(t, record.Operation.Signature, "owner signature must not be retained")
	// A new engine process still returns the same hash and never rebroadcasts.
	n2 := &Engine{db: n.db, config: n.config, smartWalletConfig: n.smartWalletConfig, ownerExecutionBackend: backend}
	replayed, err := n2.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
	require.NoError(t, err)
	require.Equal(t, p.UserOpHash, replayed.UserOpHash)
	require.EqualValues(t, 1, backend.sent.Load())
}

func ownerEOAEngineFixture(t *testing.T) (*Engine, *model.User, *ecdsa.PrivateKey, *ownerBackendFixture) {
	t.Helper()
	n, user, key, backend := ownerEngineFixture(t)
	require.NoError(t, StoreEOA7702Wallet(n.db, 11155111, user.Address, config.SMA7702Delegate()))
	sw := n.ResolveSmartWalletConfig(11155111)
	sw.EOA7702Execute = true
	sw.SMA7702Delegate, sw.SMA7702ImplHash = config.SMA7702Delegate(), config.SMA7702ImplHash()
	return n, user, key, backend
}

func prepareOwnerEOAFixture(t *testing.T, n *Engine, user *model.User) *OwnerWorkflowPreparation {
	t.Helper()
	trigger, nodes, edges, input := ownerWorkflowFixture()
	input["settings"].(map[string]interface{})["runner"] = user.Address.Hex()
	p, err := n.PrepareOwnerWorkflow(context.Background(), user, trigger, nodes, edges, input, 11155111)
	require.NoError(t, err)
	return p
}

func TestOwnerEOAWorkflowResolvesAndSubmitsOnceWithoutSession(t *testing.T) {
	n, user, key, backend := ownerEOAEngineFixture(t)
	p := prepareOwnerEOAFixture(t, n, user)
	require.Equal(t, user.Address, p.Operation.Sender)
	require.Nil(t, p.Operation.Factory)
	require.Len(t, p.Calls, 2)
	require.Zero(t, backend.sent.Load())
	sig := signOwnerFixture(t, p, key)
	first, err := n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
	require.NoError(t, err)
	require.Equal(t, p.UserOpHash, first.UserOpHash)
	require.EqualValues(t, 1, backend.sent.Load())
	n2 := &Engine{db: n.db, config: n.config, smartWalletConfig: n.smartWalletConfig, ownerExecutionBackend: backend}
	repeated, err := n2.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
	require.NoError(t, err)
	require.Equal(t, first, repeated)
	require.EqualValues(t, 1, backend.sent.Load())
	stored, err := n.db.GetKey(ownerExecutionKey(user.Address, p.Execution.Id))
	require.NoError(t, err)
	var record ownerExecutionRecord
	require.NoError(t, json.Unmarshal(stored, &record))
	require.Nil(t, record.Salt, "EOA continuation must round-trip a nil salt")
	require.Empty(t, record.Operation.Signature)
}

func TestOwnerEOAWorkflowHonorsFlagAndStoredAssociation(t *testing.T) {
	for _, scenario := range []string{"prepare flag off", "submit flag off", "delegate changed", "retired", "salt changed in record"} {
		t.Run(scenario, func(t *testing.T) {
			n, user, key, backend := ownerEOAEngineFixture(t)
			if scenario == "prepare flag off" {
				n.ResolveSmartWalletConfig(11155111).EOA7702Execute = false
				trigger, nodes, edges, input := ownerWorkflowFixture()
				input["settings"].(map[string]interface{})["runner"] = user.Address.Hex()
				_, err := n.PrepareOwnerWorkflow(context.Background(), user, trigger, nodes, edges, input, 11155111)
				require.Equal(t, codes.FailedPrecondition, status.Code(err))
				require.Equal(t, config.EOA7702ExecuteDisabledDetail, status.Convert(err).Message())
				require.Zero(t, backend.prepared.Load(), "disabled execution must stop before graph capture/pricing")
				return
			}
			p := prepareOwnerEOAFixture(t, n, user)
			sig := signOwnerFixture(t, p, key)
			switch scenario {
			case "submit flag off":
				n.ResolveSmartWalletConfig(11155111).EOA7702Execute = false
			case "delegate changed", "retired":
				wallet, err := GetWallet(n.db, 11155111, user.Address, user.Address.Hex())
				require.NoError(t, err)
				if scenario == "delegate changed" {
					other := common.HexToAddress("0x1234")
					wallet.Delegate = &other
				} else {
					wallet.Retired = true
				}
				require.NoError(t, StoreWallet(n.db, 11155111, user.Address, wallet))
			case "salt changed in record":
				stored, err := n.db.GetKey(ownerExecutionKey(user.Address, p.Execution.Id))
				require.NoError(t, err)
				var record ownerExecutionRecord
				require.NoError(t, json.Unmarshal(stored, &record))
				record.Salt = big.NewInt(0)
				require.NoError(t, n.saveOwnerExecution(p.Execution.Id, &record, time.Minute))
			}
			_, err := n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.Zero(t, backend.sent.Load())
		})
	}
}

func TestOwnerWalletAssociationDistinguishesEOAAndDerived(t *testing.T) {
	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	delegate := config.SMA7702Delegate()
	base := model.SmartWallet{Kind: model.WalletKindEOA7702, Owner: &owner, Address: &owner, Delegate: &delegate}
	require.True(t, validOwnerWallet(&base, owner, owner))
	for _, scenario := range []string{"other owner", "other sender", "other delegate", "missing delegate", "factory", "salt", "retired", "stale", "unknown kind", "derived kind"} {
		t.Run(scenario, func(t *testing.T) {
			wallet := base
			other := common.HexToAddress("0x1234")
			switch scenario {
			case "other owner":
				wallet.Owner = &other
			case "other sender":
				wallet.Address = &other
			case "other delegate":
				wallet.Delegate = &other
			case "missing delegate":
				wallet.Delegate = nil
			case "factory":
				wallet.Factory = &other
			case "salt":
				wallet.Salt = big.NewInt(0)
			case "retired":
				wallet.Retired = true
			case "stale":
				wallet.StaleDerivation = true
			case "unknown kind":
				wallet.Kind = "unknown"
			case "derived kind":
				factory := aa.MAv2FactoryAddress()
				wallet.Kind, wallet.Factory, wallet.Salt = "", &factory, big.NewInt(0)
			}
			require.False(t, validOwnerWallet(&wallet, owner, owner))
		})
	}
}

func TestOwnerWorkflowRefusesStaleOrTamperedContinuations(t *testing.T) {
	for _, scenario := range []string{"wrong owner", "wrong signature", "expired", "changed nonce", "tampered storage", "disabled flag"} {
		t.Run(scenario, func(t *testing.T) {
			n, user, key, backend := ownerEngineFixture(t)
			p := prepareOwnerFixture(t, n, user)
			sig := signOwnerFixture(t, p, key)
			caller := *user
			switch scenario {
			case "wrong owner":
				caller.Address = common.HexToAddress("0x9999999999999999999999999999999999999999")
			case "wrong signature":
				sig[0] ^= 1
			case "changed nonce":
				backend.validateError = errors.New("nonce changed")
			case "disabled flag":
				n.config.OwnerExecutionEnabled = false
			case "expired", "tampered storage":
				data, err := n.db.GetKey(ownerExecutionKey(user.Address, p.Execution.Id))
				require.NoError(t, err)
				var record ownerExecutionRecord
				require.NoError(t, json.Unmarshal(data, &record))
				if scenario == "expired" {
					record.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
				} else {
					record.Operation.MaxFeePerGas.Add(record.Operation.MaxFeePerGas, big.NewInt(1))
				}
				require.NoError(t, n.saveOwnerExecution(p.Execution.Id, &record, time.Minute))
			}
			_, err := n.SubmitOwnerExecution(context.Background(), &caller, p.Execution.Id, sig)
			require.Error(t, err)
			require.Zero(t, backend.sent.Load())
		})
	}
}

type ownerRPCRejection struct {
	code    int
	message string
}

func (e ownerRPCRejection) Error() string  { return e.message }
func (e ownerRPCRejection) ErrorCode() int { return e.code }

func TestOwnerWorkflowUncertainAndRejectedSendsDoNotRetry(t *testing.T) {
	for _, scenario := range []string{"timeout", "rpc rejection", "rpc internal error", "already known", "crash marker"} {
		t.Run(scenario, func(t *testing.T) {
			n, user, key, backend := ownerEngineFixture(t)
			p := prepareOwnerFixture(t, n, user)
			sig := signOwnerFixture(t, p, key)
			switch scenario {
			case "timeout":
				backend.sendError = context.DeadlineExceeded
			case "rpc rejection":
				backend.sendError = ownerRPCRejection{code: -32602, message: "Verification gas limit efficiency too low"}
			case "rpc internal error":
				backend.sendError = ownerRPCRejection{code: -32603, message: "internal error"}
			case "already known":
				backend.sendError = ownerRPCRejection{code: -32602, message: "already known"}
			case "crash marker":
				data, err := n.db.GetKey(ownerExecutionKey(user.Address, p.Execution.Id))
				require.NoError(t, err)
				var record ownerExecutionRecord
				require.NoError(t, json.Unmarshal(data, &record))
				record.SubmissionState = "submitting"
				require.NoError(t, n.saveOwnerExecution(p.Execution.Id, &record, time.Hour))
			}
			first, err := n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
			if scenario == "rpc rejection" {
				require.Equal(t, codes.FailedPrecondition, status.Code(err))
			} else {
				require.NoError(t, err)
				require.Equal(t, p.UserOpHash, first.UserOpHash)
			}
			wrongSignature := append([]byte(nil), sig...)
			wrongSignature[0] ^= 1
			_, err = n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, wrongSignature)
			require.Equal(t, codes.InvalidArgument, status.Code(err), "cached reconciliation is still signature-only submit")
			_, _ = n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
			want := int32(1)
			if scenario == "crash marker" {
				want = 0
			}
			require.Equal(t, want, backend.sent.Load())
		})
	}
}

func TestOwnerWorkflowRejectsUnsupportedGraphsBeforeRunning(t *testing.T) {
	n, user, _, backend := ownerEngineFixture(t)
	for _, scenario := range []string{"no write", "second write", "post write", "wrong chain", "nonmanual", "loop", "duplicate id", "unresolved amount", "failed check"} {
		t.Run(scenario, func(t *testing.T) {
			trigger, nodes, edges, input := ownerWorkflowFixture()
			switch scenario {
			case "no write":
				nodes = nodes[:1]
			case "second write":
				copy := proto.Clone(nodes[1]).(*avsproto.TaskNode)
				copy.Id = "other"
				nodes = append(nodes, copy)
			case "post write":
				edges = append(edges, &avsproto.TaskEdge{Id: "post", Source: "transfer", Target: "amounts"})
			case "wrong chain":
				nodes[1].GetContractWrite().Config.ChainId = 8453
			case "nonmanual":
				trigger.Type = avsproto.TriggerType_TRIGGER_TYPE_CRON
			case "loop":
				nodes[0].Type = avsproto.NodeType_NODE_TYPE_LOOP
			case "duplicate id":
				nodes[0].Id = "transfer"
			case "unresolved amount":
				nodes[1].GetContractWrite().Config.MethodCalls[0].MethodParams[1] = "{{missing.amount}}"
			case "failed check":
				nodes[0].GetCustomCode().Config.Source = "throw new Error('Balances changed');"
			}
			_, err := n.PrepareOwnerWorkflow(context.Background(), user, trigger, nodes, edges, input, 11155111)
			require.Error(t, err)
		})
	}
	require.Zero(t, backend.prepared.Load())
	require.Zero(t, backend.sent.Load())
}

func TestOwnerWorkflowRechecksRealReadWithoutReplacingSignedCalls(t *testing.T) {
	n, user, key, backend := ownerEngineFixture(t)
	var balance atomic.Int64
	balance.Store(1000000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		var result interface{}
		switch req.Method {
		case "eth_call":
			result = hexutil.Encode(common.LeftPadBytes(big.NewInt(balance.Load()).Bytes(), 32))
		case "eth_blockNumber":
			result = "0x1"
		case "eth_chainId":
			result = "0xaa36a7"
		case "eth_getCode":
			result = "0x6000"
		default:
			t.Errorf("unexpected balance-read RPC %s", req.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result}))
	}))
	defer server.Close()
	n.smartWalletConfig.EthRpcUrl = server.URL
	trigger, nodes, edges, input := ownerWorkflowFixture()
	readABI, err := structpb.NewValue(map[string]interface{}{"type": "function", "name": "balanceOf", "stateMutability": "view", "inputs": []interface{}{map[string]interface{}{"name": "account", "type": "address"}}, "outputs": []interface{}{map[string]interface{}{"name": "", "type": "uint256"}}})
	require.NoError(t, err)
	nodes[0] = &avsproto.TaskNode{Id: "amounts", Name: "amounts", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_READ, TaskType: &avsproto.TaskNode_ContractRead{ContractRead: &avsproto.ContractReadNode{Config: &avsproto.ContractReadNode_Config{ChainId: 11155111, ContractAddress: "0x6666666666666666666666666666666666666666", ContractAbi: []*structpb.Value{readABI}, MethodCalls: []*avsproto.ContractReadNode_MethodCall{{MethodName: "balanceOf", MethodParams: []string{input["settings"].(map[string]interface{})["runner"].(string)}}}}}}}
	nodes[1].GetContractWrite().Config.MethodCalls[0].MethodParams[1] = "{{amounts.data.balanceOf}}"
	nodes[1].GetContractWrite().Config.MethodCalls[1].MethodParams[1] = "1"
	p, err := n.PrepareOwnerWorkflow(context.Background(), user, trigger, nodes, edges, input, 11155111)
	require.NoError(t, err)
	sig := signOwnerFixture(t, p, key)
	balance.Store(2000000)
	_, err = n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, err.Error(), "calls changed")
	require.Zero(t, backend.sent.Load())
	require.EqualValues(t, 1, backend.prepared.Load(), "continuation must not reprice the operation")
}

type ownerFailingStorage struct {
	storage.Storage
	failOnWrite int
	writes      int
}

func (s *ownerFailingStorage) SetWithTTL(key, value []byte, ttl time.Duration) error {
	s.writes++
	if s.writes == s.failOnWrite {
		return errors.New("durable write failed")
	}
	return s.Storage.SetWithTTL(key, value, ttl)
}

func TestOwnerWorkflowRequiresDurableMarkerBeforeBroadcast(t *testing.T) {
	n, user, key, backend := ownerEngineFixture(t)
	p := prepareOwnerFixture(t, n, user)
	n.db = &ownerFailingStorage{Storage: n.db, failOnWrite: 1}
	_, err := n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, signOwnerFixture(t, p, key))
	require.ErrorContains(t, err, "durable write failed")
	require.Zero(t, backend.sent.Load())
}

func TestOwnerWorkflowPostSendStorageFailurePreservesMarker(t *testing.T) {
	n, user, key, backend := ownerEngineFixture(t)
	p := prepareOwnerFixture(t, n, user)
	sig := signOwnerFixture(t, p, key)
	db := n.db
	n.db = &ownerFailingStorage{Storage: db, failOnWrite: 2}
	_, err := n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
	require.ErrorContains(t, err, "durable write failed")
	require.EqualValues(t, 1, backend.sent.Load(), "the send succeeded before its outcome could be saved")
	stored, err := db.GetKey(ownerExecutionKey(user.Address, p.Execution.Id))
	require.NoError(t, err)
	var record ownerExecutionRecord
	require.NoError(t, json.Unmarshal(stored, &record))
	require.Equal(t, "submitting", record.SubmissionState)
	require.Empty(t, record.Operation.Signature)
	// A restarted process reconciles the durable marker instead of sending again.
	n2 := &Engine{db: db, config: n.config, smartWalletConfig: n.smartWalletConfig, ownerExecutionBackend: backend}
	replayed, err := n2.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
	require.NoError(t, err)
	require.Equal(t, p.UserOpHash, replayed.UserOpHash)
	require.Equal(t, "submitting", replayed.SubmissionState)
	require.EqualValues(t, 1, backend.sent.Load())
}

func TestOwnerWorkflowFlagAndRetentionCap(t *testing.T) {
	trigger, nodes, edges, input := ownerWorkflowFixture()
	t.Run("disabled", func(t *testing.T) {
		n, user, _, backend := ownerEngineFixture(t)
		n.config.OwnerExecutionEnabled = false
		_, err := n.PrepareOwnerWorkflow(context.Background(), user, trigger, nodes, edges, input, 11155111)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.Equal(t, OwnerExecutionDisabledDetail, status.Convert(err).Message())
		require.Zero(t, backend.prepared.Load())
	})
	t.Run("submitted and invalid records do not consume the cap", func(t *testing.T) {
		n, user, _, backend := ownerEngineFixture(t)
		submitted, err := json.Marshal(ownerExecutionRecord{SubmissionState: ownerSubmissionSubmitted, Owner: user.Address})
		require.NoError(t, err)
		for i := 0; i < ownerPreparationMaxPerOwner; i++ {
			require.NoError(t, n.db.SetWithTTL(ownerExecutionKey(user.Address, model.GenerateID()), []byte("{}"), time.Minute))
			require.NoError(t, n.db.SetWithTTL(ownerExecutionKey(user.Address, model.GenerateID()), []byte("not-json"), time.Minute))
			require.NoError(t, n.db.SetWithTTL(ownerExecutionKey(user.Address, model.GenerateID()), submitted, time.Minute))
		}
		_, err = n.PrepareOwnerWorkflow(context.Background(), user, trigger, nodes, edges, input, 11155111)
		require.NoError(t, err)
		require.EqualValues(t, 1, backend.prepared.Load())
	})
	t.Run("prepared records consume the cap", func(t *testing.T) {
		n, user, _, backend := ownerEngineFixture(t)
		prepared, err := json.Marshal(ownerExecutionRecord{SubmissionState: ownerSubmissionPrepared})
		require.NoError(t, err)
		for i := 0; i < ownerPreparationMaxPerOwner; i++ {
			require.NoError(t, n.db.SetWithTTL(ownerExecutionKey(user.Address, model.GenerateID()), prepared, time.Minute))
		}
		_, err = n.PrepareOwnerWorkflow(context.Background(), user, trigger, nodes, edges, input, 11155111)
		require.Equal(t, codes.ResourceExhausted, status.Code(err))
		require.Contains(t, err.Error(), "prepared")
		require.Zero(t, backend.prepared.Load())
	})
}

func patchOwnerSubmission(t *testing.T, n *Engine, user *model.User, id string, state string, expiresAt int64) {
	t.Helper()
	data, err := n.db.GetKey(ownerExecutionKey(user.Address, id))
	require.NoError(t, err)
	var record ownerExecutionRecord
	require.NoError(t, json.Unmarshal(data, &record))
	record.SubmissionState = state
	if expiresAt != 0 {
		record.ExpiresAt = expiresAt
	}
	require.NoError(t, n.saveOwnerExecution(id, &record, time.Hour))
}

func loadOwnerSubmission(t *testing.T, n *Engine, user *model.User, id string) ownerExecutionRecord {
	t.Helper()
	data, err := n.db.GetKey(ownerExecutionKey(user.Address, id))
	require.NoError(t, err)
	var record ownerExecutionRecord
	require.NoError(t, json.Unmarshal(data, &record))
	return record
}

func TestOwnerWorkflowReconcilesSubmittingMarker(t *testing.T) {
	for _, scenario := range []string{"known after window", "unknown during window", "unknown after window", "lookup error after window"} {
		t.Run(scenario, func(t *testing.T) {
			n, user, key, backend := ownerEngineFixture(t)
			p := prepareOwnerFixture(t, n, user)
			sig := signOwnerFixture(t, p, key)
			expires := time.Now().Add(-time.Second).UnixMilli()
			switch scenario {
			case "known after window":
				backend.userOpKnown = true
			case "unknown during window":
				expires = time.Now().Add(time.Minute).UnixMilli()
			case "lookup error after window":
				backend.userOpKnownErr = errors.New("bundler unavailable")
			}
			patchOwnerSubmission(t, n, user, p.Execution.Id, ownerSubmissionSubmitting, expires)
			out, err := n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
			stored := loadOwnerSubmission(t, n, user, p.Execution.Id)
			require.Zero(t, backend.sent.Load())
			switch scenario {
			case "known after window":
				require.NoError(t, err)
				require.Equal(t, ownerSubmissionSubmitted, out.SubmissionState)
				require.Equal(t, ownerSubmissionSubmitted, stored.SubmissionState)
				require.Equal(t, p.UserOpHash, out.UserOpHash)
			case "unknown during window":
				require.NoError(t, err)
				require.Equal(t, ownerSubmissionSubmitting, out.SubmissionState)
				require.Equal(t, ownerSubmissionSubmitting, stored.SubmissionState)
			case "unknown after window":
				require.Equal(t, codes.FailedPrecondition, status.Code(err))
				require.Contains(t, err.Error(), "not sent")
				require.Equal(t, ownerSubmissionUnsent, stored.SubmissionState)
				_, err = n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
				require.Equal(t, codes.FailedPrecondition, status.Code(err))
				require.EqualValues(t, 1, backend.lookups.Load(), "an unsent record does not ask the bundler again")
			case "lookup error after window":
				require.NoError(t, err)
				require.Equal(t, ownerSubmissionSubmitting, out.SubmissionState)
				require.Equal(t, ownerSubmissionSubmitting, stored.SubmissionState)
			}
			require.Zero(t, backend.sent.Load())
		})
	}
}

func TestOwnerPreconditionKeepsEOADisabledSentinel(t *testing.T) {
	wrapped := fmt.Errorf("owner operation could not be prepared: %w", config.ErrEOA7702ExecuteDisabled)
	err := ownerPreconditionError(wrapped, "owner operation could not be prepared: %v")
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, config.EOA7702ExecuteDisabledDetail, status.Convert(err).Message())
	other := ownerPreconditionError(errors.New("nonce changed"), "owner preparation is stale: %v")
	require.Contains(t, status.Convert(other).Message(), "nonce changed")
}

func TestOwnerWorkflowOneInFlightSubmit(t *testing.T) {
	n, user, key, backend := ownerEngineFixture(t)
	p := prepareOwnerFixture(t, n, user)
	sig := signOwnerFixture(t, p, key)
	backend.enteredSubmit = make(chan struct{})
	backend.releaseSubmit = make(chan struct{})
	type result struct {
		out *OwnerWorkflowSubmission
		err error
	}
	results := make(chan result, 2)
	go func() {
		out, err := n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
		results <- result{out, err}
	}()
	select {
	case <-backend.enteredSubmit:
	case <-time.After(20 * time.Second):
		t.Fatal("first submit did not reach the bundler")
	}
	go func() {
		out, err := n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, sig)
		results <- result{out, err}
	}()
	close(backend.releaseSubmit)
	for i := 0; i < 2; i++ {
		got := <-results
		require.NoError(t, got.err)
		require.Equal(t, ownerSubmissionSubmitted, got.out.SubmissionState)
		require.Equal(t, p.UserOpHash, got.out.UserOpHash)
	}
	require.EqualValues(t, 1, backend.sent.Load())
	require.Equal(t, ownerSubmissionSubmitted, loadOwnerSubmission(t, n, user, p.Execution.Id).SubmissionState)
}

func TestOwnerWorkflowClientCancelDoesNotDropRelay(t *testing.T) {
	n, user, key, backend := ownerEngineFixture(t)
	p := prepareOwnerFixture(t, n, user)
	sig := signOwnerFixture(t, p, key)
	backend.enteredSubmit = make(chan struct{})
	backend.releaseSubmit = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		out *OwnerWorkflowSubmission
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := n.SubmitOwnerExecution(ctx, user, p.Execution.Id, sig)
		done <- result{out, err}
	}()
	select {
	case <-backend.enteredSubmit:
	case <-time.After(20 * time.Second):
		t.Fatal("submit did not reach the bundler")
	}
	cancel()
	close(backend.releaseSubmit)
	got := <-done
	require.NoError(t, got.err)
	require.NoError(t, backend.submitCtxErr)
	require.Equal(t, ownerSubmissionSubmitted, got.out.SubmissionState)
	require.EqualValues(t, 1, backend.sent.Load())
	require.Equal(t, ownerSubmissionSubmitted, loadOwnerSubmission(t, n, user, p.Execution.Id).SubmissionState)
}
