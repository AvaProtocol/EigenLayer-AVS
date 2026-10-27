package taskengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/bigint"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/oklog/ulid/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	ownerPreparationTTL         = 10 * time.Minute
	ownerExecutionRetention     = 24 * time.Hour
	ownerPreparationMaxPerOwner = 64
	ownerBatchMaxCalls          = 32
)

type ownerPreparationContextKey struct{}

// OwnerWorkflowPreparation is the existing graph's resolved, priced signing
// boundary. Execution steps contain only completed read/check nodes, not a
// fabricated successful ContractWrite. Nothing has been broadcast.
type OwnerWorkflowPreparation struct {
	Execution   *avsproto.Execution
	Operation   *userop.UserOperationV07
	UserOpHash  string
	ChainID     int64
	ExpiresAt   int64
	WriteNodeID string
	Calls       []InnerCall
}

// OwnerWorkflowSubmission reports relay progress. A transport failure is
// uncertain, not proof of failure; the same operation hash is used for polling.
type OwnerWorkflowSubmission struct {
	ExecutionID     string `json:"executionId"`
	UserOpHash      string `json:"userOpHash"`
	ChainID         int64  `json:"chainId"`
	SubmissionState string `json:"submissionState"`
}

// Use the underlying struct's storage encoding (exact big.Int values and byte
// slices), not its bundler MarshalJSON, which has no matching UnmarshalJSON.
// This type is private to persistent storage and never goes over the API.
type storedOwnerOperation userop.UserOperationV07

type ownerExecutionRecord struct {
	Owner           common.Address              `json:"owner"`
	ChainID         int64                       `json:"chainId"`
	Salt            *big.Int                    `json:"salt"`
	Operation       *storedOwnerOperation       `json:"operation"`
	Identity        preset.OwnerAccountIdentity `json:"identity"`
	UserOpHash      common.Hash                 `json:"userOpHash"`
	ExpiresAt       int64                       `json:"expiresAt"`
	SubmissionState string                      `json:"submissionState"`
	Definition      []byte                      `json:"definition"`
	InputJSON       []byte                      `json:"inputJson"`
	WriteNodeID     string                      `json:"writeNodeId"`
}

func ownerExecutionPrefix(owner common.Address) []byte {
	return []byte("ownerexec:" + strings.ToLower(owner.Hex()) + ":")
}

func ownerExecutionKey(owner common.Address, executionID string) []byte {
	return append(ownerExecutionPrefix(owner), executionID...)
}

type ownerExecutionBackend interface {
	Prepare(context.Context, *config.SmartWalletConfig, common.Address, common.Address, *big.Int, []aa.Call, logger.Logger) (*preset.PreparedOwnerUserOp, error)
	Validate(context.Context, *config.SmartWalletConfig, common.Address, *big.Int, *preset.PreparedOwnerUserOp) error
	Submit(context.Context, *config.SmartWalletConfig, *userop.UserOperationV07) (common.Hash, error)
}

type defaultOwnerExecutionBackend struct{}

func (defaultOwnerExecutionBackend) Prepare(ctx context.Context, sw *config.SmartWalletConfig, owner, sender common.Address, salt *big.Int, calls []aa.Call, l logger.Logger) (*preset.PreparedOwnerUserOp, error) {
	return preset.PrepareOwnerUserOp(ctx, sw, owner, sender, salt, calls, l)
}
func (defaultOwnerExecutionBackend) Validate(ctx context.Context, sw *config.SmartWalletConfig, owner common.Address, salt *big.Int, p *preset.PreparedOwnerUserOp) error {
	return preset.ValidateOwnerUserOp(ctx, sw, owner, salt, p)
}
func (defaultOwnerExecutionBackend) Submit(ctx context.Context, sw *config.SmartWalletConfig, op *userop.UserOperationV07) (common.Hash, error) {
	return preset.SubmitOwnerUserOp(ctx, sw, op)
}
func (n *Engine) ownerBackend() ownerExecutionBackend {
	if n.ownerExecutionBackend != nil {
		return n.ownerExecutionBackend
	}
	return defaultOwnerExecutionBackend{}
}

type ownerWorkflowCollector struct {
	source      common.Address
	writeNodeID string
	calls       []aa.Call
}

// validateOwnerGraph bounds the first generic handoff: one chain, Manual,
// read/check nodes, and exactly one terminal write. Unsupported graph shapes
// fail before any runner executes; Auto and ordinary simulate are unaffected.
func validateOwnerGraph(trigger *avsproto.TaskTrigger, nodes []*avsproto.TaskNode, edges []*avsproto.TaskEdge, chainID int64) (string, error) {
	if trigger == nil || trigger.Id == "" || trigger.GetManual() == nil || trigger.GetType() != avsproto.TriggerType_TRIGGER_TYPE_MANUAL {
		return "", fmt.Errorf("owner execution requires a Manual trigger")
	}
	if len(nodes) == 0 || len(nodes) > 64 || len(edges) > 128 {
		return "", fmt.Errorf("owner execution requires 1 to 64 nodes and at most 128 edges")
	}
	ids := map[string]bool{trigger.Id: true}
	writeID := ""
	for _, node := range nodes {
		if node == nil || node.Id == "" || ids[node.Id] {
			return "", fmt.Errorf("owner workflow node ids must be unique and nonempty")
		}
		ids[node.Id] = true
		switch node.Type {
		case avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE:
			if writeID != "" || node.GetContractWrite().GetConfig() == nil {
				return "", fmt.Errorf("owner execution requires exactly one ContractWrite node")
			}
			writeID = node.Id
			if node.GetContractWrite().GetConfig().GetChainId() != chainID {
				return "", fmt.Errorf("owner write chain must match the prepared chain")
			}
		case avsproto.NodeType_NODE_TYPE_CONTRACT_READ:
			if node.GetContractRead().GetConfig() == nil || node.GetContractRead().GetConfig().GetChainId() != chainID {
				return "", fmt.Errorf("owner read chain must match the prepared chain")
			}
		case avsproto.NodeType_NODE_TYPE_CUSTOM_CODE:
			if node.GetCustomCode() == nil {
				return "", fmt.Errorf("invalid owner workflow CustomCode node")
			}
		case avsproto.NodeType_NODE_TYPE_BRANCH:
			if node.GetBranch() == nil {
				return "", fmt.Errorf("invalid owner workflow Branch node")
			}
		case avsproto.NodeType_NODE_TYPE_FILTER:
			if node.GetFilter() == nil {
				return "", fmt.Errorf("invalid owner workflow Filter node")
			}
		default:
			return "", fmt.Errorf("node type %s is not supported by owner preparation", node.Type)
		}
	}
	if writeID == "" {
		return "", fmt.Errorf("owner execution requires exactly one ContractWrite node")
	}
	for _, edge := range edges {
		if edge == nil || !ids[edge.Target] || !ids[strings.SplitN(edge.Source, ".", 2)[0]] {
			return "", fmt.Errorf("invalid owner workflow edge")
		}
		if edge.Source == writeID || strings.HasPrefix(edge.Source, writeID+".") {
			return "", fmt.Errorf("the owner ContractWrite must be terminal")
		}
	}
	return writeID, nil
}

// collectOwnerWrite runs through the same template/ABI encoder as ordinary
// ContractWrite, then stops before session checks, Tenderly or submission.
func (r *ContractWriteProcessor) collectOwnerWrite(stepID string, node *avsproto.ContractWriteNode, parsedABI *abi.ABI, methods []*avsproto.ContractWriteNode_MethodCall, targets []common.Address) error {
	collector := r.vm.ownerPreparation
	if collector.writeNodeID != stepID || len(collector.calls) != 0 {
		return fmt.Errorf("unexpected additional owner write")
	}
	if len(methods) == 0 || len(methods) > ownerBatchMaxCalls {
		return fmt.Errorf("owner batch requires 1 to %d calls", ownerBatchMaxCalls)
	}
	r.vm.mu.Lock()
	sender, _ := r.vm.vars["aa_sender"].(string)
	r.vm.mu.Unlock()
	if !strings.EqualFold(sender, collector.source.Hex()) {
		return fmt.Errorf("owner workflow source changed during preparation")
	}
	if raw := r.extractTransactionValue(node); raw != "" {
		value, err := bigint.Parse(raw)
		if err != nil || value.Sign() != 0 {
			return fmt.Errorf("native-value owner calls are not supported yet")
		}
	}
	calls := make([]aa.Call, len(methods))
	for i, method := range methods {
		if method == nil || targets[i] == (common.Address{}) {
			return fmt.Errorf("owner batch has an invalid call or target")
		}
		encoded, _, failure := r.resolveMethodCallData(parsedABI, method)
		if failure != nil {
			return fmt.Errorf("owner call %d: %s", i+1, failure.Error)
		}
		data, err := hexutil.Decode(encoded)
		if err != nil || len(data) < 4 || len(data) > 32*1024 {
			return fmt.Errorf("owner call %d has invalid calldata", i+1)
		}
		if err := r.validateTransactionBeforeGasEstimation(method.MethodName, encoded, data, targets[i]); err != nil {
			return fmt.Errorf("owner call %d failed pre-flight validation: %w", i+1, err)
		}
		calls[i] = aa.Call{Target: targets[i], Value: big.NewInt(0), Data: data}
	}
	collector.calls = calls
	return nil
}

// PrepareOwnerWorkflow reuses the existing graph runner in read/check mode;
// only the terminal ContractWrite is intercepted to build an owner batch.
func (n *Engine) PrepareOwnerWorkflow(ctx context.Context, user *model.User, trigger *avsproto.TaskTrigger, nodes []*avsproto.TaskNode, edges []*avsproto.TaskEdge, input map[string]interface{}, chainID int64) (*OwnerWorkflowPreparation, error) {
	if user == nil || user.Address == (common.Address{}) {
		return nil, status.Error(codes.Unauthenticated, "owner JWT required")
	}
	if n.config == nil || !n.config.OwnerExecutionEnabled {
		return nil, status.Error(codes.FailedPrecondition, "owner execution is disabled on this gateway")
	}
	if chainID <= 0 || !n.isChainConfigured(chainID) {
		return nil, status.Error(codes.InvalidArgument, "unsupported owner execution chain")
	}
	writeID, err := validateOwnerGraph(trigger, nodes, edges, chainID)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	settings, _ := input["settings"].(map[string]interface{})
	source, _ := settings["runner"].(string)
	if !common.IsHexAddress(source) {
		return nil, status.Error(codes.InvalidArgument, "settings.runner must be the selected source wallet")
	}
	wallet, err := GetWallet(n.db, chainID, user.Address, source)
	if err != nil || !validOwnerWallet(wallet, user.Address, common.HexToAddress(source)) {
		return nil, status.Error(codes.PermissionDenied, "source must be a registered MA v2 derived wallet for this owner")
	}
	// Serialize the bounded insertion check, not just the eventual DB write.
	lock := n.executionMutex("owner-prepare:" + user.Address.Hex())
	lock.Lock()
	defer lock.Unlock()
	count, err := n.db.CountKeysByPrefix(ownerExecutionPrefix(user.Address))
	if err != nil {
		return nil, err
	}
	if count >= ownerPreparationMaxPerOwner {
		return nil, status.Error(codes.ResourceExhausted, "too many retained owner executions; retry after expiry")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	definition, err := proto.Marshal(&avsproto.Task{Trigger: trigger, Nodes: nodes, Edges: edges})
	if err != nil {
		return nil, err
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	collector := &ownerWorkflowCollector{source: *wallet.Address, writeNodeID: writeID}
	execution, err := n.SimulateWorkflowWithContext(context.WithValue(ctx, ownerPreparationContextKey{}, collector), user, trigger, nodes, edges, input, chainID)
	if err != nil {
		return nil, err
	}
	if execution.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS || len(collector.calls) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "owner workflow checks failed or did not reach its terminal write; refresh and simulate again")
	}
	sw := n.ResolveSmartWalletConfig(chainID)
	if sw == nil || sw.ChainID != chainID {
		return nil, status.Error(codes.FailedPrecondition, "owner execution chain configuration changed")
	}
	prepared, err := n.ownerBackend().Prepare(ctx, sw, user.Address, *wallet.Address, wallet.Salt, collector.calls, n.logger)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "owner operation could not be prepared: %v", err)
	}
	packed, err := aa.PackExecuteBatchMAv2(collector.calls)
	if err != nil {
		return nil, err
	}
	op := prepared.Operation
	if op == nil || op.Sender != *wallet.Address || len(op.Signature) != 0 || !bytes.Equal(op.CallData, packed) {
		return nil, fmt.Errorf("owner operation differs from the resolved workflow")
	}
	hash, err := op.GetUserOpHash(preset.EntryPointV07(), big.NewInt(chainID))
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(ownerPreparationTTL).UnixMilli()
	record := &ownerExecutionRecord{Owner: user.Address, ChainID: chainID, Salt: wallet.Salt, Operation: (*storedOwnerOperation)(op), Identity: prepared.Identity, UserOpHash: hash, ExpiresAt: expires, SubmissionState: "prepared", Definition: definition, InputJSON: inputJSON, WriteNodeID: writeID}
	if err := n.saveOwnerExecution(execution.Id, record, ownerPreparationTTL); err != nil {
		return nil, err
	}
	// Prepared calls are in the signing review; do not report them as completed
	// blockchain steps, nor return a fabricated simulation receipt for them.
	steps := execution.Steps[:0]
	for _, step := range execution.Steps {
		if step.Id != writeID {
			steps = append(steps, step)
		}
	}
	execution.Steps, execution.Status, execution.EndAt = steps, avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING, 0
	calls, err := InnerCallsFromExecuteCalldata(op.CallData)
	if err != nil {
		return nil, err
	}
	if n.logger != nil {
		n.logger.Info("owner workflow awaiting signature", "execution_id", execution.Id, "chain_id", chainID, "user_op_hash", hash.Hex(), "calls", len(calls))
	}
	return &OwnerWorkflowPreparation{Execution: execution, Operation: op, UserOpHash: hash.Hex(), ChainID: chainID, ExpiresAt: expires, WriteNodeID: writeID, Calls: calls}, nil
}

func validOwnerWallet(wallet *model.SmartWallet, owner, sender common.Address) bool {
	return wallet != nil && wallet.Owner != nil && *wallet.Owner == owner && wallet.Address != nil && *wallet.Address == sender && wallet.Factory != nil && *wallet.Factory == aa.MAv2FactoryAddress() && wallet.Salt != nil && !wallet.Retired && !wallet.StaleDerivation && wallet.Kind == ""
}

func (n *Engine) saveOwnerExecution(id string, record *ownerExecutionRecord, ttl time.Duration) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return n.db.SetWithTTL(ownerExecutionKey(record.Owner, id), data, ttl)
}

// SubmitOwnerExecution accepts a signature only: the client cannot replace any
// operation fields. The durable submitting marker is written before the RPC, so
// a timeout/restart/repeated click never constructs or broadcasts a second op.
func (n *Engine) SubmitOwnerExecution(ctx context.Context, user *model.User, id string, signature []byte) (*OwnerWorkflowSubmission, error) {
	if user == nil || user.Address == (common.Address{}) {
		return nil, status.Error(codes.Unauthenticated, "owner JWT required")
	}
	if _, err := ulid.ParseStrict(id); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid prepared execution id")
	}
	lock := n.executionMutex(id)
	lock.Lock()
	defer lock.Unlock()
	data, err := n.db.GetKey(ownerExecutionKey(user.Address, id))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil, status.Error(codes.NotFound, "prepared owner execution not found or expired")
	}
	if err != nil {
		return nil, err
	}
	var record ownerExecutionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	if record.Owner != user.Address || record.Operation == nil {
		return nil, status.Error(codes.NotFound, "prepared owner execution not found")
	}
	op := (*userop.UserOperationV07)(record.Operation)
	hash, err := op.GetUserOpHash(preset.EntryPointV07(), big.NewInt(record.ChainID))
	if err != nil || hash != record.UserOpHash {
		return nil, fmt.Errorf("stored owner operation failed its integrity check")
	}
	framed, err := preset.FrameOwnerSignature(hash, signature, user.Address)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	out := &OwnerWorkflowSubmission{ExecutionID: id, UserOpHash: hash.Hex(), ChainID: record.ChainID, SubmissionState: record.SubmissionState}
	if record.SubmissionState != "prepared" {
		if record.SubmissionState == "rejected" {
			return nil, status.Error(codes.FailedPrecondition, "operation was rejected; prepare and sign again")
		}
		return out, nil // includes submitting/unknown after a restart or timeout
	}
	if time.Now().UnixMilli() >= record.ExpiresAt {
		return nil, status.Error(codes.FailedPrecondition, "owner preparation expired; prepare and sign again")
	}
	if n.config == nil || !n.config.OwnerExecutionEnabled {
		return nil, status.Error(codes.FailedPrecondition, "owner execution is disabled on this gateway")
	}
	if !n.isChainConfigured(record.ChainID) {
		return nil, status.Error(codes.FailedPrecondition, "owner execution chain is no longer configured")
	}
	wallet, err := GetWallet(n.db, record.ChainID, user.Address, op.Sender.Hex())
	if err != nil || !validOwnerWallet(wallet, user.Address, op.Sender) || wallet.Salt.Cmp(record.Salt) != 0 {
		return nil, status.Error(codes.FailedPrecondition, "owner source wallet association changed; prepare again")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	sw := n.ResolveSmartWalletConfig(record.ChainID)
	if sw == nil || sw.ChainID != record.ChainID {
		return nil, status.Error(codes.FailedPrecondition, "owner execution chain configuration changed")
	}
	// Re-run only read/check preparation and require byte-identical calls. The
	// original operation is never replaced or repriced after the owner signs.
	// This catches changed balances/branch conditions before the relay; state
	// can still change between this check and eventual on-chain inclusion.
	var definition avsproto.Task
	if err := proto.Unmarshal(record.Definition, &definition); err != nil {
		return nil, err
	}
	var input map[string]interface{}
	if err := json.Unmarshal(record.InputJSON, &input); err != nil {
		return nil, err
	}
	collector := &ownerWorkflowCollector{source: op.Sender, writeNodeID: record.WriteNodeID}
	checked, err := n.SimulateWorkflowWithContext(context.WithValue(ctx, ownerPreparationContextKey{}, collector), user, definition.Trigger, definition.Nodes, definition.Edges, input, record.ChainID)
	if err != nil || checked.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS || len(collector.calls) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "owner workflow checks changed; prepare and review again")
	}
	resolved, err := aa.PackExecuteBatchMAv2(collector.calls)
	if err != nil || !bytes.Equal(resolved, op.CallData) {
		return nil, status.Error(codes.FailedPrecondition, "owner workflow calls changed; prepare and review again")
	}
	prepared := &preset.PreparedOwnerUserOp{Operation: op, Identity: record.Identity}
	if err := n.ownerBackend().Validate(ctx, sw, user.Address, record.Salt, prepared); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "owner preparation is stale: %v", err)
	}
	record.SubmissionState = "submitting"
	// Keep the stored operation unsigned; signatures are neither logged nor
	// needed for reconciliation. The client can poll the already known hash.
	op.Signature = nil
	if err := n.saveOwnerExecution(id, &record, ownerExecutionRetention); err != nil {
		return nil, err
	}
	op.Signature = framed
	sentHash, sendErr := n.ownerBackend().Submit(ctx, sw, op)
	op.Signature = nil
	record.SubmissionState = "submitted"
	if sendErr != nil || sentHash != hash {
		record.SubmissionState = "unknown"
		if ownerBundlerRejected(sendErr) {
			record.SubmissionState = "rejected"
		}
	}
	if err := n.saveOwnerExecution(id, &record, ownerExecutionRetention); err != nil {
		return nil, err
	}
	if n.logger != nil {
		n.logger.Info("owner workflow relay outcome", "execution_id", id, "chain_id", record.ChainID, "user_op_hash", hash.Hex(), "submission_state", record.SubmissionState)
	}
	if record.SubmissionState == "rejected" {
		return nil, status.Error(codes.FailedPrecondition, "bundler rejected the operation; prepare, review and sign again")
	}
	out.SubmissionState = record.SubmissionState
	return out, nil
}

// A transport/internal-server error can arrive after acceptance. Only known
// precheck rejections require a new preparation; duplicates stay reconcilable.
func ownerBundlerRejected(err error) bool {
	var rpcErr rpc.Error
	if !errors.As(err, &rpcErr) {
		return false
	}
	message := strings.ToLower(rpcErr.Error())
	if strings.Contains(message, "already known") || strings.Contains(message, "already seen") {
		return false
	}
	code := rpcErr.ErrorCode()
	return code == -32602 || (code >= -32508 && code <= -32500) || strings.Contains(message, "verification gas limit efficiency")
}
