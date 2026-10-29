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
)

const (
	ownerPreparationTTL         = 10 * time.Minute
	ownerExecutionRetention     = 24 * time.Hour
	ownerPreparationMaxPerOwner = 64
	ownerBatchMaxCalls          = 32
	// A bundler lookup that fails is not "unknown". Leave the record submitting.
	ownerBundlerLookupTimeout = 15 * time.Second
)

// OwnerExecutionDisabledDetail is configuration, not a stale preparation.
// Preparing again does not turn the feature on. REST maps this exact
// message to OWNER_EXECUTION_DISABLED.
const OwnerExecutionDisabledDetail = "owner execution is disabled on this gateway"

// Stored submission states. The success response only returns submitting,
// submitted, or unknown. rejected and unsent are errors that ask for a new prepare.
const (
	ownerSubmissionPrepared   = "prepared"
	ownerSubmissionSubmitting = "submitting"
	ownerSubmissionSubmitted  = "submitted"
	ownerSubmissionUnknown    = "unknown"
	ownerSubmissionRejected   = "rejected"
	ownerSubmissionUnsent     = "unsent"
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
	Batch           *preset.OwnerBatch          `json:"batch,omitempty"`
	// Legacy fields are retained for decoding pending/reconciled records. An
	// unsigned preparation without a captured batch must be prepared again.
	Definition  []byte `json:"definition,omitempty"`
	InputJSON   []byte `json:"inputJson,omitempty"`
	WriteNodeID string `json:"writeNodeId"`
}

func ownerExecutionPrefix(owner common.Address) []byte {
	return []byte("ownerexec:" + strings.ToLower(owner.Hex()) + ":")
}

func ownerExecutionKey(owner common.Address, executionID string) []byte {
	return append(ownerExecutionPrefix(owner), executionID...)
}

type ownerExecutionBackend interface {
	Prepare(context.Context, *config.SmartWalletConfig, common.Address, common.Address, *big.Int, preset.OwnerBatch, logger.Logger) (*preset.PreparedOwnerUserOp, error)
	Validate(context.Context, *config.SmartWalletConfig, common.Address, *big.Int, *preset.PreparedOwnerUserOp) error
	Submit(context.Context, *config.SmartWalletConfig, *userop.UserOperationV07) (common.Hash, error)
	// UserOpKnown reports whether the bundler already has this hash.
	// false, nil means the bundler returned no operation. A non-nil error
	// means the lookup itself failed and the caller must not change state.
	UserOpKnown(context.Context, *config.SmartWalletConfig, common.Hash) (bool, error)
}

type defaultOwnerExecutionBackend struct{}

func (defaultOwnerExecutionBackend) Prepare(ctx context.Context, sw *config.SmartWalletConfig, owner, sender common.Address, salt *big.Int, batch preset.OwnerBatch, l logger.Logger) (*preset.PreparedOwnerUserOp, error) {
	return preset.PrepareOwnerBatch(ctx, sw, owner, sender, salt, batch, l)
}
func (defaultOwnerExecutionBackend) Validate(ctx context.Context, sw *config.SmartWalletConfig, owner common.Address, salt *big.Int, p *preset.PreparedOwnerUserOp) error {
	return preset.ValidateOwnerUserOp(ctx, sw, owner, salt, p)
}
func (defaultOwnerExecutionBackend) Submit(ctx context.Context, sw *config.SmartWalletConfig, op *userop.UserOperationV07) (common.Hash, error) {
	return preset.SubmitOwnerUserOp(ctx, sw, op)
}
func (defaultOwnerExecutionBackend) UserOpKnown(ctx context.Context, sw *config.SmartWalletConfig, hash common.Hash) (bool, error) {
	if sw == nil {
		return false, fmt.Errorf("nil owner execution chain configuration")
	}
	url, err := sw.ActiveBundlerURL()
	if err != nil {
		return false, err
	}
	client, err := newUserOpBundler(url)
	if err != nil {
		return false, err
	}
	defer client.Close()
	raw, err := client.GetUserOperationByHash(ctx, hash.Hex())
	if err != nil {
		return false, err
	}
	_, _, _, found := parseBundlerUserOp(raw)
	return found, nil
}
func (n *Engine) ownerBackend() ownerExecutionBackend {
	if n.ownerExecutionBackend != nil {
		return n.ownerExecutionBackend
	}
	return defaultOwnerExecutionBackend{}
}

type ownerWorkflowCollector struct {
	source            common.Address
	calls             []aa.Call
	contractID        string
	nativeID          string
	reserveGas        bool
	contractCollected bool
	nativeCollected   bool
}

func newOwnerCollector(source common.Address, nodes []*avsproto.TaskNode) *ownerWorkflowCollector {
	c := &ownerWorkflowCollector{source: source}
	for _, node := range nodes {
		if node.Type == avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE {
			c.contractID = node.Id
		}
		if node.Type == avsproto.NodeType_NODE_TYPE_ETH_TRANSFER {
			c.nativeID = node.Id
		}
	}
	return c
}
func (c *ownerWorkflowCollector) batch() preset.OwnerBatch {
	return preset.OwnerBatch{Calls: c.calls, ReserveNativeGas: c.reserveGas}
}
func (c *ownerWorkflowCollector) complete() bool {
	return (c.contractID == "" || c.contractCollected) && (c.nativeID == "" || c.nativeCollected)
}

// Stored/session workflows cannot honor an owner preparation's captured cap.
// Check at creation rather than persisting a task that fails on every trigger.
func rejectOwnerReserveGas(nodes []*avsproto.TaskNode) error {
	for _, node := range nodes {
		for _, transfer := range []*avsproto.ETHTransferNode{node.GetEthTransfer(), node.GetLoop().GetEthTransfer()} {
			if transfer.GetConfig().GetReserveGas() {
				return status.Errorf(codes.InvalidArgument, "node %q uses reserveGas, which is only supported by owner workflow preparation", node.GetId())
			}
		}
	}
	return nil
}

// validateOwnerGraph bounds the owner handoff: one chain, Manual, read/check
// nodes, at most one ContractWrite and one terminal ETHTransfer. Unsupported graph shapes
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
	nativeID := ""
	for _, node := range nodes {
		if node == nil || node.Id == "" || ids[node.Id] {
			return "", fmt.Errorf("owner workflow node ids must be unique and nonempty")
		}
		ids[node.Id] = true
		switch node.Type {
		case avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE:
			if writeID != "" || node.GetContractWrite().GetConfig() == nil {
				return "", fmt.Errorf("owner execution supports at most one ContractWrite node")
			}
			writeID = node.Id
			if node.GetContractWrite().GetConfig().GetChainId() != chainID {
				return "", fmt.Errorf("owner write chain must match the prepared chain")
			}
		case avsproto.NodeType_NODE_TYPE_ETH_TRANSFER:
			if nativeID != "" || node.GetEthTransfer().GetConfig() == nil || node.GetEthTransfer().GetConfig().GetChainId() != chainID {
				return "", fmt.Errorf("owner execution supports one terminal ETHTransfer on the prepared chain")
			}
			nativeID = node.Id
		case avsproto.NodeType_NODE_TYPE_CONTRACT_READ:
			if node.GetContractRead().GetConfig() == nil || node.GetContractRead().GetConfig().GetChainId() != chainID {
				return "", fmt.Errorf("owner read chain must match the prepared chain")
			}
		case avsproto.NodeType_NODE_TYPE_CUSTOM_CODE:
			// Same runner as ordinary simulation. Prepare calls
			// SimulateWorkflowWithContext, which executes CustomCode through
			// JSProcessor and its Goja runtime. There is no second sandbox
			// and no separate time limit for this path.
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
	if writeID == "" && nativeID == "" {
		return "", fmt.Errorf("owner execution requires a ContractWrite or ETHTransfer")
	}
	terminalID := writeID
	if nativeID != "" {
		terminalID = nativeID
	}
	connected := writeID == "" || nativeID == ""
	for _, edge := range edges {
		// A branch or filter output is "nodeId.port". The node id is the
		// segment before the first dot; a plain source is the whole string.
		if edge == nil || !ids[edge.Target] || !ids[strings.SplitN(edge.Source, ".", 2)[0]] {
			return "", fmt.Errorf("invalid owner workflow edge")
		}
		if edge.Source == terminalID || strings.HasPrefix(edge.Source, terminalID+".") {
			return "", fmt.Errorf("the final owner write must be terminal")
		}
		if writeID != "" && (edge.Source == writeID || strings.HasPrefix(edge.Source, writeID+".")) {
			if edge.Target != nativeID {
				return "", fmt.Errorf("only ETHTransfer may follow the owner ContractWrite")
			}
			connected = true
		}
	}
	if !connected {
		return "", fmt.Errorf("owner ETHTransfer must follow ContractWrite")
	}
	return terminalID, nil
}

// collectOwnerWrite runs through the same template/ABI encoder as ordinary
// ContractWrite, then stops before session checks, Tenderly or submission.
func (r *ContractWriteProcessor) collectOwnerWrite(stepID string, node *avsproto.ContractWriteNode, parsedABI *abi.ABI, methods []*avsproto.ContractWriteNode_MethodCall, targets []common.Address) error {
	collector := r.vm.ownerPreparation
	if collector.contractID != stepID || collector.contractCollected || collector.nativeCollected {
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
			return fmt.Errorf("native ContractWrite values are unsupported; use an ETHTransfer node")
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
	collector.contractCollected = true
	return nil
}

// PrepareOwnerWorkflow reuses the existing graph runner in read/check mode;
// ContractWrite and terminal ETHTransfer are intercepted to build one owner batch.
func (n *Engine) PrepareOwnerWorkflow(ctx context.Context, user *model.User, trigger *avsproto.TaskTrigger, nodes []*avsproto.TaskNode, edges []*avsproto.TaskEdge, input map[string]interface{}, chainID int64) (*OwnerWorkflowPreparation, error) {
	if user == nil || user.Address == (common.Address{}) {
		return nil, status.Error(codes.Unauthenticated, "owner JWT required")
	}
	if n.config == nil || !n.config.OwnerExecutionEnabled {
		return nil, status.Error(codes.FailedPrecondition, OwnerExecutionDisabledDetail)
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
		return nil, status.Error(codes.PermissionDenied, "source must be a registered MA v2 derived or eoa_7702 wallet for this owner")
	}
	sw := n.ResolveSmartWalletConfig(chainID)
	if err := checkOwnerWalletConfig(sw, wallet, chainID); err != nil {
		return nil, ownerPreconditionError(err, "")
	}
	// Serialize this owner's cap check with the insert. executionMutex is a
	// fixed 256-shard array, not a map that grows with execution ids, and
	// defer releases the shard when prepare returns. The lock stays held
	// across simulation so two prepares cannot both pass the cap.
	lock := n.executionMutex("owner-prepare:" + user.Address.Hex())
	lock.Lock()
	defer lock.Unlock()
	count, err := n.countPreparedOwnerExecutions(user.Address)
	if err != nil {
		return nil, err
	}
	if count >= ownerPreparationMaxPerOwner {
		return nil, status.Error(codes.ResourceExhausted, "too many prepared owner executions; retry after one expires")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	collector := newOwnerCollector(*wallet.Address, nodes)
	execution, err := n.SimulateWorkflowWithContext(context.WithValue(ctx, ownerPreparationContextKey{}, collector), user, trigger, nodes, edges, input, chainID)
	if err != nil {
		return nil, err
	}
	if execution.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS || len(collector.calls) == 0 || !collector.complete() {
		return nil, status.Error(codes.FailedPrecondition, "owner workflow checks failed or did not reach its terminal write; refresh and simulate again")
	}
	batch := collector.batch()
	prepared, err := n.ownerBackend().Prepare(ctx, sw, user.Address, *wallet.Address, wallet.Salt, batch, n.logger)
	if err != nil {
		return nil, ownerPreconditionError(err, "owner operation could not be prepared: %v")
	}
	resolvedCalls, err := batch.Resolve(prepared.Operation)
	if err != nil {
		return nil, err
	}
	packed, err := aa.PackExecuteBatchMAv2(resolvedCalls)
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
	record := &ownerExecutionRecord{
		Owner: user.Address, ChainID: chainID, Salt: wallet.Salt,
		Operation: (*storedOwnerOperation)(op), Identity: prepared.Identity,
		UserOpHash: hash, ExpiresAt: expires, SubmissionState: ownerSubmissionPrepared,
		Batch: &batch, WriteNodeID: writeID,
	}
	if err := n.saveOwnerExecution(execution.Id, record, ownerPreparationTTL); err != nil {
		return nil, err
	}
	// Prepared calls are in the signing review; do not report them as completed
	// blockchain steps, nor return a fabricated simulation receipt for them.
	steps := execution.Steps[:0]
	for _, step := range execution.Steps {
		if step.Id != collector.contractID && step.Id != collector.nativeID {
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
	if wallet == nil || owner == (common.Address{}) || wallet.Owner == nil || *wallet.Owner != owner || wallet.Address == nil || *wallet.Address != sender || wallet.Retired || wallet.StaleDerivation {
		return false
	}
	if wallet.IsEOA7702() {
		return wallet.ValidateEOA7702Shape() == nil && *wallet.Delegate == config.SMA7702Delegate()
	}
	return sender != owner && wallet.Kind == "" && wallet.Factory != nil && *wallet.Factory == aa.MAv2FactoryAddress() && wallet.Salt != nil && wallet.Salt.Sign() >= 0 && wallet.Salt.BitLen() <= 256
}

func checkOwnerWalletConfig(sw *config.SmartWalletConfig, wallet *model.SmartWallet, chainID int64) error {
	if sw == nil || sw.ChainID != chainID {
		return fmt.Errorf("owner execution chain configuration changed")
	}
	if wallet.IsEOA7702() {
		return sw.CheckEOA7702Execute()
	}
	return nil
}

// ownerPreconditionError keeps the disabled EOA flag as its own exact
// FailedPrecondition. A longer wrapper would make REST ask the client to
// prepare again for a flag that prepare cannot turn on.
func ownerPreconditionError(err error, format string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, config.ErrEOA7702ExecuteDisabled) {
		return status.Error(codes.FailedPrecondition, config.EOA7702ExecuteDisabledDetail)
	}
	if format == "" {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	return status.Errorf(codes.FailedPrecondition, format, err)
}

func sameOwnerWalletSalt(a, b *big.Int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Cmp(b) == 0
}

// countPreparedOwnerExecutions ignores submitted, rejected, unknown and
// unsent records. Those are kept for 24h and must not block a new prepare.
// The prefix is at most 64 prepared records plus that day of retained
// submissions, so the prepare shard reads the values directly.
func (n *Engine) countPreparedOwnerExecutions(owner common.Address) (int, error) {
	items, err := n.db.GetByPrefix(ownerExecutionPrefix(owner))
	if err != nil {
		return 0, err
	}
	count := 0
	for _, item := range items {
		var record ownerExecutionRecord
		if json.Unmarshal(item.Value, &record) != nil {
			continue
		}
		if record.SubmissionState == ownerSubmissionPrepared {
			count++
		}
	}
	return count, nil
}

// reconcileSubmittingOwnerExecution runs when a retry finds the pre-send
// marker. A bundler hit becomes submitted. An unknown hash past the signing
// window becomes unsent, which asks for a new prepare. A lookup error leaves
// the marker alone: a failed read is not an empty result.
//
// A new prepare after unsent uses the same sender and the same owner nonce
// key: entity 0 (the fallback signer) with ValidationOptionGlobal.
// PrepareOwnerUserOp reads EntryPoint.getNonce for that key and refuses any
// other key. It does not consult the session nonce cache, and owner submit
// does not record acceptance there. Until that nonce is mined, the
// replacement carries the same full nonce, so the EntryPoint includes at
// most one of the two operations. After it is mined, getNonce advances and
// the new prepare is a separate signed operation.
func (n *Engine) reconcileSubmittingOwnerExecution(ctx context.Context, id string, record *ownerExecutionRecord) error {
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ownerBundlerLookupTimeout)
	defer cancel()
	sw := n.ResolveSmartWalletConfig(record.ChainID)
	known, err := n.ownerBackend().UserOpKnown(lookupCtx, sw, record.UserOpHash)
	if err != nil {
		if n.logger != nil {
			n.logger.Warn("owner execution bundler lookup failed; leaving submitting", "execution_id", id, "user_op_hash", record.UserOpHash.Hex(), "error", err)
		}
		return nil
	}
	if known {
		record.SubmissionState = ownerSubmissionSubmitted
		return n.saveOwnerExecution(id, record, ownerExecutionRetention)
	}
	if time.Now().UnixMilli() < record.ExpiresAt {
		return nil
	}
	record.SubmissionState = ownerSubmissionUnsent
	if err := n.saveOwnerExecution(id, record, ownerExecutionRetention); err != nil {
		return err
	}
	return status.Error(codes.FailedPrecondition, "owner operation was not sent; prepare and sign again")
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
	// Recovers the signer and requires it to equal user.Address. REST also
	// rejects a non-65-byte body; this is the check every caller hits,
	// including a gRPC entry that bypasses that body check.
	framed, err := preset.FrameOwnerSignature(hash, signature, user.Address)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	out := &OwnerWorkflowSubmission{ExecutionID: id, UserOpHash: hash.Hex(), ChainID: record.ChainID, SubmissionState: record.SubmissionState}
	if record.SubmissionState != ownerSubmissionPrepared {
		switch record.SubmissionState {
		case ownerSubmissionRejected:
			return nil, status.Error(codes.FailedPrecondition, "operation was rejected; prepare and sign again")
		case ownerSubmissionUnsent:
			return nil, status.Error(codes.FailedPrecondition, "owner operation was not sent; prepare and sign again")
		case ownerSubmissionSubmitting:
			if err := n.reconcileSubmittingOwnerExecution(ctx, id, &record); err != nil {
				return nil, err
			}
			out.SubmissionState = record.SubmissionState
		}
		return out, nil // includes submitting/unknown/submitted after a restart or timeout
	}
	if time.Now().UnixMilli() >= record.ExpiresAt {
		return nil, status.Error(codes.FailedPrecondition, "owner preparation expired; prepare and sign again")
	}
	if n.config == nil || !n.config.OwnerExecutionEnabled {
		return nil, status.Error(codes.FailedPrecondition, OwnerExecutionDisabledDetail)
	}
	if !n.isChainConfigured(record.ChainID) {
		return nil, status.Error(codes.FailedPrecondition, "owner execution chain is no longer configured")
	}
	wallet, err := GetWallet(n.db, record.ChainID, user.Address, op.Sender.Hex())
	if err != nil || !validOwnerWallet(wallet, user.Address, op.Sender) || !sameOwnerWalletSalt(wallet.Salt, record.Salt) {
		return nil, status.Error(codes.FailedPrecondition, "owner source wallet association changed; prepare again")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	sw := n.ResolveSmartWalletConfig(record.ChainID)
	if err := checkOwnerWalletConfig(sw, wallet, record.ChainID); err != nil {
		return nil, ownerPreconditionError(err, "")
	}
	// Continue the captured preparation. Never re-run reads or branch checks:
	// incoming funds must not alter the amounts the owner already reviewed.
	if record.Batch == nil {
		return nil, status.Error(codes.FailedPrecondition, "owner preparation has no captured batch; prepare and review again")
	}
	resolvedCalls, err := record.Batch.Resolve(op)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "stored owner batch is invalid; prepare again")
	}
	resolved, err := aa.PackExecuteBatchMAv2(resolvedCalls)
	if err != nil || !bytes.Equal(resolved, op.CallData) {
		return nil, status.Error(codes.FailedPrecondition, "owner workflow calls changed; prepare and review again")
	}
	prepared := &preset.PreparedOwnerUserOp{Operation: op, Identity: record.Identity}
	if err := n.ownerBackend().Validate(ctx, sw, user.Address, record.Salt, prepared); err != nil {
		return nil, ownerPreconditionError(err, "owner preparation is stale: %v")
	}
	record.SubmissionState = ownerSubmissionSubmitting
	// Keep the stored operation unsigned; signatures are neither logged nor
	// needed for reconciliation. The client can poll the already known hash.
	op.Signature = nil
	if err := n.saveOwnerExecution(id, &record, ownerExecutionRetention); err != nil {
		return nil, err
	}
	// The marker is durable. A client disconnect must not cancel the relay
	// or turn a sent operation into "unknown" via context.Canceled.
	relayCtx, relayCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer relayCancel()
	op.Signature = framed
	sentHash, sendErr := n.ownerBackend().Submit(relayCtx, sw, op)
	op.Signature = nil
	record.SubmissionState = ownerSubmissionSubmitted
	if sendErr != nil || sentHash != hash {
		record.SubmissionState = ownerSubmissionUnknown
		if ownerBundlerRejected(sendErr) {
			record.SubmissionState = ownerSubmissionRejected
		}
	}
	if err := n.saveOwnerExecution(id, &record, ownerExecutionRetention); err != nil {
		return nil, err
	}
	if n.logger != nil {
		n.logger.Info("owner workflow relay outcome", "execution_id", id, "chain_id", record.ChainID, "user_op_hash", hash.Hex(), "submission_state", record.SubmissionState)
	}
	if record.SubmissionState == ownerSubmissionRejected {
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
