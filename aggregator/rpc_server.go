package aggregator

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"github.com/allegro/bigcache/v3"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/getsentry/sentry-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
	wrapperspb "google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// RpcServer is our grpc sever struct hold the entry point of request handler
type RpcServer struct {
	avsproto.UnimplementedNodeServer

	config *config.Config
	cache  *bigcache.BigCache
	db     storage.Storage
	engine *taskengine.Engine

	operatorPool *OperatorPool

	ethrpc *ethclient.Client

	smartWalletRpc   *ethclient.Client
	smartWalletWsRpc *ethclient.Client // Global WebSocket client for transaction monitoring
	chainID          *big.Int

	// chainRegistry is set in gateway mode to route chain-specific operations to workers.
	// nil in single-chain aggregator mode.
	chainRegistry *ChainRegistry

	// aliasResolver maps an operator's registered address to the alias
	// key it signs with. Required to authenticate operators that keep
	// their registered key cold — see operator_alias.go.
	aliasResolver *operatorAliasResolver
}

// resolveSmartWalletForChain returns the SmartWalletConfig + RPC client
// for the requested chain. In single-chain mode (no chainRegistry) it
// always returns the aggregator's defaults. In gateway mode, when
// requestedChainID matches a registered chain, it returns that chain's
// SmartWallet config and a lazily-dialed chain-specific RPC client —
// without this, multi-chain operations that read on-chain state directly
// from the aggregator (e.g. the ERC-20 balance check in ExecuteWithdraw)
// always hit the default chain's RPC and miss tokens that only exist on
// other chains.
// resolveSmartWalletConfigForChain resolves the SmartWalletConfig for a
// request chain WITHOUT dialing chain RPC. Use this on the primary path;
// resolveSmartWalletForChain additionally dials the chain RPC (lazily) and
// should only be used on the fallback path where a worker-routed
// ChainStateReader isn't available for the chain.
func (r *RpcServer) resolveSmartWalletConfigForChain(requestedChainID int64) (*config.SmartWalletConfig, error) {
	if r.chainRegistry == nil || requestedChainID == 0 {
		return r.config.SmartWallet, nil
	}
	if r.config.SmartWallet != nil && requestedChainID == r.config.SmartWallet.ChainID {
		return r.config.SmartWallet, nil
	}
	// Config-only lookup — GetChainConfig does NOT require a connected
	// worker gRPC client (unlike GetWorker, which errors when the worker
	// is momentarily disconnected). Config resolution must succeed
	// independently of worker connectivity so the withdraw flow can still
	// reach its direct-RPC fallback when a worker is down.
	chainCfg, err := r.chainRegistry.GetChainConfig(requestedChainID)
	if err != nil {
		return nil, err
	}
	if chainCfg == nil || chainCfg.SmartWallet == nil {
		return nil, fmt.Errorf("chain %d has no smart_wallet config", requestedChainID)
	}
	return chainCfg.SmartWallet, nil
}

func (r *RpcServer) resolveSmartWalletForChain(requestedChainID int64) (*config.SmartWalletConfig, *ethclient.Client, error) {
	if r.chainRegistry == nil || requestedChainID == 0 {
		return r.config.SmartWallet, r.smartWalletRpc, nil
	}
	if r.config.SmartWallet != nil && requestedChainID == r.config.SmartWallet.ChainID {
		return r.config.SmartWallet, r.smartWalletRpc, nil
	}
	entry, err := r.chainRegistry.GetWorker(requestedChainID)
	if err != nil {
		return nil, nil, err
	}
	if entry.Config == nil || entry.Config.SmartWallet == nil {
		return nil, nil, fmt.Errorf("chain %d has no smart_wallet config", requestedChainID)
	}
	rpc, err := entry.GetRPC()
	if err != nil {
		return nil, nil, err
	}
	return entry.Config.SmartWallet, rpc, nil
}

// ExecuteWithdraw is the auth-free body of the former WithdrawFunds
// gRPC handler — extracted so the REST WithdrawWallet handler can
// reuse the bundler + paymaster + balance pipeline without going
// through gRPC's metadata-based auth. Callers (now only REST via
// the WithdrawService adapter) supply the already-resolved
// *model.User and the same payload shape; the response is the same
// protobuf result type and gets translated to the OpenAPI
// WithdrawResponse on the REST side.
func (r *RpcServer) ExecuteWithdraw(ctx context.Context, user *model.User, payload *avsproto.WithdrawFundsReq) (*avsproto.WithdrawFundsResp, error) {
	requestedChainID := payload.GetChainId()
	r.config.Logger.Info("process withdraw funds",
		"user", user.Address.String(),
		"recipient", payload.RecipientAddress,
		"amount", payload.Amount,
		"token", payload.Token,
		"smart_wallet", payload.SmartWalletAddress,
		"requested_chain_id", requestedChainID,
	)

	// In single-chain aggregator mode (no chainRegistry), reject explicit chain_id
	// that does not match the aggregator's chain. In gateway mode this resolves
	// to the matching chain's SmartWallet config + RPC client; everything below
	// uses `swCfg` / `swRpc` instead of the aggregator defaults so a sepolia
	// withdraw doesn't hit the mainnet RPC.
	if r.chainRegistry == nil && requestedChainID != 0 && r.chainID != nil && requestedChainID != r.chainID.Int64() {
		return nil, status.Errorf(codes.InvalidArgument,
			"chain_id %d does not match aggregator chain %d", requestedChainID, r.chainID.Int64())
	}
	swCfg, swErr := r.resolveSmartWalletConfigForChain(requestedChainID)
	if swErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "resolve chain %d: %v", requestedChainID, swErr)
	}
	// Checked here, not at the point of use further down: in single-chain mode
	// resolveSmartWalletConfigForChain returns r.config.SmartWallet with a nil
	// error, so an aggregator started without a smart wallet config yields a
	// nil swCfg and no signal. Everything below reads it — the native-ETH
	// refusal calls UsesModularAccountV2, which has no nil receiver guard.
	if swCfg == nil {
		return nil, status.Errorf(codes.Internal, "smart wallet configuration not available")
	}

	// Validate required parameters
	if payload.RecipientAddress == "" {
		return nil, status.Errorf(codes.InvalidArgument, "recipient address is required")
	}
	if payload.Amount == "" {
		return nil, status.Errorf(codes.InvalidArgument, "amount is required")
	}
	if payload.Token == "" {
		return nil, status.Errorf(codes.InvalidArgument, "token is required")
	}

	// Validate recipient address format
	if !common.IsHexAddress(payload.RecipientAddress) {
		return nil, status.Errorf(codes.InvalidArgument, "invalid recipient address format")
	}

	// Native withdrawals build execute(recipient, amount, 0x) — empty inner
	// calldata — which an MA v2 account cannot validate under a session grant:
	// every REST grant is selector-scoped and the allowlist hook reverts on
	// calldata shorter than 4 bytes. Refuse here rather than let the bundler
	// answer with AA23. Checked against this chain's config, not assumed
	// globally, so a chain that later gains native-value support (a native
	// limit module rather than the selector allowlist) starts working without
	// touching this branch. ERC-20 withdrawals carry a real transfer selector
	// and are unaffected.
	if strings.EqualFold(strings.TrimSpace(payload.Token), "ETH") && swCfg.UsesModularAccountV2() {
		recipient := common.HexToAddress(payload.RecipientAddress)
		policyID := ""
		if r.db != nil && common.IsHexAddress(payload.SmartWalletAddress) {
			if wallet := common.HexToAddress(payload.SmartWalletAddress); wallet != (common.Address{}) {
				if policy, perr := taskengine.ActiveSessionPolicyForWallet(r.db, swCfg.ChainID, user.Address, wallet); perr == nil && policy != nil {
					policyID = policy.ID
				}
			}
		}
		r.config.Logger.Warn("refusing native ETH withdraw: session grants cannot authorize it",
			"user", user.Address.String(),
			"smart_wallet", payload.SmartWalletAddress,
			"recipient", payload.RecipientAddress,
			"chain_id", swCfg.ChainID,
		)
		return nil, status.Error(codes.InvalidArgument,
			taskengine.FormatSessionPolicyNativeNotAllowed(recipient, policyID))
	}

	// Balance preflight reads route through the chain's worker (gateway
	// mode). Fall back to a direct-RPC reader only when no worker-routed
	// reader is registered (single-chain mode / startup race) — that path
	// lazily dials the chain RPC via resolveSmartWalletForChain.
	//
	// This no longer means the withdraw flow is worker-only: the send below
	// runs in-process and dials this chain's RPC and bundler directly,
	// because the session grant it needs lives in gateway storage. Keeping
	// the READS worker-routed is still worth it — they are the high-frequency
	// part, and the worker already holds a warm connection.
	chainReader := taskengine.GetChainStateReaderForChain(uint64(requestedChainID))
	if chainReader == nil {
		_, swRpc, rpcErr := r.resolveSmartWalletForChain(requestedChainID)
		if rpcErr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "resolve chain %d rpc: %v", requestedChainID, rpcErr)
		}
		if swRpc == nil {
			return nil, status.Errorf(codes.Internal, "no chain-state reader or RPC client available for chain %d", requestedChainID)
		}
		chainReader = taskengine.NewDirectChainStateReader(swRpc, requestedChainID)
	}

	// Parse amount - support "max" (case-insensitive) for "withdraw all"
	amountStr := strings.TrimSpace(strings.ToLower(payload.Amount))
	withdrawAll := amountStr == "max"

	var requestedAmount *big.Int
	if withdrawAll {
		// Will be calculated later based on balance and gas reimbursement
		requestedAmount = nil // Use nil to indicate it needs to be calculated
	} else {
		var success bool
		requestedAmount, success = new(big.Int).SetString(payload.Amount, 10)
		if !success || requestedAmount == nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid amount: must be a positive integer or 'max'")
		}
		// Validate that numeric amount must be positive (not zero)
		if requestedAmount.Cmp(big.NewInt(0)) <= 0 {
			return nil, status.Errorf(codes.InvalidArgument, "invalid amount: must be a positive integer or 'max'")
		}
	}

	// Build withdrawal parameters (amount will be adjusted if "withdraw all" is requested)
	params := &WithdrawalParams{
		RecipientAddress: common.HexToAddress(payload.RecipientAddress),
		Amount:           requestedAmount,
		Token:            payload.Token,
	}

	// Handle smart wallet address resolution
	if payload.SmartWalletAddress != "" {
		if !common.IsHexAddress(payload.SmartWalletAddress) {
			return nil, status.Errorf(codes.InvalidArgument, "invalid smart wallet address format")
		}
		addr := common.HexToAddress(payload.SmartWalletAddress)
		params.SmartWalletAddress = &addr
	}

	// Validate smart wallet address - it must be provided and exist in user's wallet data
	if params.SmartWalletAddress == nil {
		return nil, status.Errorf(codes.InvalidArgument, "smart wallet address is required - must be obtained from getWallet() call first")
	}
	// Validate that the provided address belongs to the authenticated user.
	// We intentionally skip the on-chain deployment check here because BuildUserOp
	// will include initCode to deploy the wallet atomically as part of the UserOp.
	validationErr := r.validateSmartWalletOwnership(user.Address, *params.SmartWalletAddress)
	if validationErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid smart wallet address: %v", validationErr)
	}

	smartWalletAddress := params.SmartWalletAddress

	// Withdrawals used to attach the v0.6 verifying paymaster with
	// SkipReimbursement so a user could move their full balance without
	// reserving ETH for reimbursement. Both went with the EntryPoint v0.7
	// cutover: sponsorship is the chain's Gas Manager policy and there is no
	// reimbursement leg to skip.

	// Pre-flight: validate the wallet balance covers the withdrawal.
	// Withdrawals always run with SkipReimbursement (set above) — the
	// paymaster absorbs gas, so there's no reimbursement to deduct or
	// estimate. All balance reads route through chainReader, which is
	// worker-routed in gateway mode (the gateway holds no direct chain-RPC
	// connection for the withdraw flow).
	var finalAmount *big.Int
	if strings.ToUpper(payload.Token) == "ETH" {
		balance, balanceErr := chainReader.GetBalance(ctx, *smartWalletAddress)
		if balanceErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to get wallet balance: %v", balanceErr)
		}
		if withdrawAll {
			if balance.Cmp(big.NewInt(0)) == 0 {
				return nil, status.Errorf(codes.InvalidArgument, "wallet has zero balance")
			}
			finalAmount = balance
			r.config.Logger.Info("withdraw all requested (no reimbursement)",
				"balance", balance.String())
		} else {
			if requestedAmount.Cmp(balance) > 0 {
				return nil, status.Errorf(codes.FailedPrecondition,
					"insufficient balance: requested %s wei but wallet has %s wei",
					requestedAmount.String(), balance.String())
			}
			finalAmount = requestedAmount
		}
	} else {
		// ERC20 — no gas reimbursement needed (paymaster covers it).
		// Validate the token address before converting: HexToAddress
		// silently coerces a malformed string to the zero address, which
		// would query the wrong contract. This path is reachable via gRPC
		// (not only the REST handler), so don't assume upstream validation.
		if !common.IsHexAddress(payload.Token) {
			return nil, status.Errorf(codes.InvalidArgument, "invalid token address format: %q", payload.Token)
		}
		tokenAddr := common.HexToAddress(payload.Token)
		// The zero address is never a valid ERC-20. It passes IsHexAddress,
		// so guard explicitly — otherwise GetTokenBalance calls balanceOf on
		// it and fails with the opaque "no contract code at given address".
		// This path is reachable via gRPC (not only the REST handler), so
		// don't assume upstream validation. Native withdrawals use "ETH".
		if tokenAddr == (common.Address{}) {
			return nil, status.Errorf(codes.InvalidArgument,
				"token cannot be the zero address on chain %d; use \"ETH\" to withdraw native currency", requestedChainID)
		}
		// Validate the token balance.
		tokenBalance, balanceErr := chainReader.GetTokenBalance(ctx, tokenAddr, *smartWalletAddress)
		if balanceErr != nil {
			// "no contract code at given address" means the token isn't
			// deployed on this chain (wrong chainId, or a non-token address) —
			// a client error, not a server fault. Return FailedPrecondition
			// (HTTP 400) so it doesn't page Sentry as a 500, and include the
			// token + chain so the event is self-diagnosable without
			// cross-referencing gateway logs (Sentry EIGENLAYER-AVS-1J).
			if errors.Is(balanceErr, bind.ErrNoCode) || strings.Contains(balanceErr.Error(), "no contract code at given address") {
				return nil, status.Errorf(codes.FailedPrecondition,
					"token %s has no contract code on chain %d — verify the token address and chainId", payload.Token, requestedChainID)
			}
			return nil, status.Errorf(codes.Internal,
				"failed to get token balance for token %s on chain %d: %v", payload.Token, requestedChainID, balanceErr)
		}
		if withdrawAll {
			if tokenBalance.Cmp(big.NewInt(0)) == 0 {
				return nil, status.Errorf(codes.InvalidArgument, "token balance is zero")
			}
			finalAmount = tokenBalance
			r.config.Logger.Info("withdraw all requested for ERC20, using full balance",
				"token", payload.Token,
				"balance", finalAmount.String())
		} else {
			if requestedAmount.Cmp(tokenBalance) > 0 {
				return nil, status.Errorf(codes.InvalidArgument,
					"insufficient token balance: requested %s, but wallet has %s",
					requestedAmount.String(), tokenBalance.String())
			}
			finalAmount = requestedAmount
		}
	}

	// Update params with final amount
	params.Amount = finalAmount

	// Build withdrawal calldata with final amount
	callData, err := BuildWithdrawalCalldata(params)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to build withdrawal calldata: %v", err)
	}

	r.config.Logger.Info("processing withdrawal with paymaster sponsorship",
		"user", user.Address.String(),
		"smartWallet", smartWalletAddress.Hex(),
		"paymaster", swCfg.PaymasterAddress.Hex(),
		"amount", payload.Amount,
		"token", payload.Token,
	)

	// Sent in-process, NOT through the chain worker.
	//
	// Session grants live in the gateway's BadgerDB and the resolver that
	// reads them is installed on the gateway (Engine.InstallSessionResolver).
	// A worker has neither, so preset.SendUserOpAuto there resolved no
	// authorization and every withdraw died on "no session authorization for
	// smart wallet …" while a perfectly good grant sat in gateway storage.
	//
	// Passing swCfg (this chain's config, resolved above) is what the worker
	// hop used to provide; the send path dials that chain's RPC and bundler
	// itself. auth is left nil deliberately — SendUserOpMAv2 resolves the
	// grant AFTER it derives the real sender, because grants are keyed by
	// smart-wallet address and resolving earlier against the owner EOA finds
	// nothing.
	userOp, receipt, err := preset.SendUserOpAuto(
		swCfg,
		user.Address,
		callData,
		smartWalletAddress,
		nil, // saltOverride: withdraws operate on already-deployed wallets
		r.config.Logger,
	)

	if err != nil {
		// See preset.LogBundlerError: Warn on on-chain revert (user's withdrawal
		// reverted — e.g. ERC20 transfer to blacklisted recipient, insufficient
		// token balance after race), Error on infra/AA (bundler down, AA21, etc.).
		preset.LogBundlerError(r.config.Logger, err,
			"failed to send withdrawal UserOp",
			"error", err,
			"user", user.Address.String(),
			"recipient", payload.RecipientAddress,
			"amount", payload.Amount,
		)
		return &avsproto.WithdrawFundsResp{
			Success:            false,
			Status:             "failed",
			Message:            fmt.Sprintf("failed to send withdrawal transaction: %v", err),
			SubmittedAt:        time.Now().Unix(),
			SmartWalletAddress: smartWalletAddress.Hex(),
			RecipientAddress:   payload.RecipientAddress,
			Amount:             payload.Amount,
			Token:              payload.Token,
		}, nil
	}

	// Prepare response
	resp := &avsproto.WithdrawFundsResp{
		Success:            true,
		SubmittedAt:        time.Now().Unix(),
		SmartWalletAddress: smartWalletAddress.Hex(),
		RecipientAddress:   payload.RecipientAddress,
		Amount:             payload.Amount,
		Token:              payload.Token,
	}

	if userOp != nil {
		// Get UserOp hash — sign against the chain we actually targeted
		// so the hash matches what the bundler/paymaster validated.
		resp.UserOpHash = userOp.UserOpHash.Hex()
	}

	if receipt != nil {
		resp.Status = "confirmed"
		resp.Message = "withdrawal transaction confirmed"
		resp.TransactionHash = receipt.TxHash.Hex()
		r.config.Logger.Info("withdrawal transaction confirmed",
			"user", user.Address.String(),
			"smartWallet", smartWalletAddress.Hex(),
			"recipient", payload.RecipientAddress,
			"amount", payload.Amount,
			"txHash", receipt.TxHash.Hex(),
		)
	} else {
		resp.Status = "pending"
		resp.Message = "withdrawal transaction submitted, waiting for confirmation"
		r.config.Logger.Info("withdrawal transaction submitted",
			"user", user.Address.String(),
			"recipient", payload.RecipientAddress,
			"amount", payload.Amount,
			"userOpHash", resp.UserOpHash,
		)
	}

	return resp, nil
}

// validateSmartWalletOwnership validates that the smart wallet address belongs to the specified owner in the database
func (r *RpcServer) validateSmartWalletOwnership(owner common.Address, smartWalletAddress common.Address) error {
	// Validate wallet exists in database and belongs to owner
	modelWallet, err := r.engine.GetWalletFromDB(owner, smartWalletAddress.Hex())
	if err != nil {
		return fmt.Errorf("smart wallet address %s not found for owner %s: %w", smartWalletAddress.Hex(), owner.Hex(), err)
	}

	// Validate ownership using direct address comparison for consistency
	if modelWallet.Owner == nil || *modelWallet.Owner != owner {
		return fmt.Errorf("smart wallet address %s does not belong to owner %s", smartWalletAddress.Hex(), owner.Hex())
	}

	return nil
}

// (Aggregator-service gRPC handlers — CreateTask, ListTasks, GetTask,
// TriggerTask, SetTaskEnabled, DeleteTask, ListExecutions, GetExecution,
// GetExecutionStatus, CreateSecret, ListSecrets, UpdateSecret,
// DeleteSecret, GetWorkflowCount, GetExecutionCount, GetExecutionStats,
// RunNodeWithInputs, RunTrigger, SimulateTask, GetTokenMetadata — were
// deleted as part of the REST migration. The REST equivalents live in
// aggregator/rest/handlers_*.go.)

// ReportEventOverload handles event overload alerts from operators
func (r *RpcServer) ReportEventOverload(ctx context.Context, alert *avsproto.EventOverloadAlert) (*avsproto.EventOverloadResponse, error) {
	r.config.Logger.Warn("🚨 EVENT OVERLOAD ALERT RECEIVED",
		"task_id", alert.TaskId,
		"operator_address", alert.OperatorAddress,
		"block_number", alert.BlockNumber,
		"events_detected", alert.EventsDetected,
		"safety_limit", alert.SafetyLimit,
		"query_index", alert.QueryIndex,
		"details", alert.Details)

	// Disable the overloaded task immediately
	deactivated, err := r.engine.DisableWorkflow(alert.TaskId)
	if err != nil {
		r.config.Logger.Error("❌ Failed to disable overloaded task",
			"task_id", alert.TaskId,
			"error", err)
		return &avsproto.EventOverloadResponse{
			TaskCancelled: false,
			Message:       fmt.Sprintf("Failed to disable task: %v", err),
			Timestamp:     uint64(time.Now().UnixMilli()),
		}, nil
	}

	responseMessage := "Task disabled due to event overload"
	if !deactivated {
		responseMessage = "Task was already disabled or not found"
	}

	// Capture a message in Sentry for visibility
	sentry.CaptureMessage(fmt.Sprintf("Event overload detected for task %s: %s", alert.TaskId, alert.Details))

	r.config.Logger.Info("🛑 Task disabled due to event overload",
		"task_id", alert.TaskId,
		"deactivated", deactivated)

	return &avsproto.EventOverloadResponse{
		TaskCancelled: deactivated,
		Message:       responseMessage,
		Timestamp:     uint64(time.Now().UnixMilli()),
	}, nil
}

// Operator action
func (r *RpcServer) SyncMessages(payload *avsproto.SyncMessagesReq, srv avsproto.Node_SyncMessagesServer) error {
	err := r.engine.StreamCheckToOperator(payload, srv)

	return err
}

// Operator action
func (r *RpcServer) NotifyTriggers(ctx context.Context, payload *avsproto.NotifyTriggersReq) (*avsproto.NotifyTriggersResp, error) {
	r.config.Logger.Debug("📨 Operator triggered workflow execution",
		"operator", payload.Address,
		"task_id", payload.TaskId,
		"trigger_type", payload.TriggerType.String())

	// Process the trigger and get execution state information
	executionState, err := r.engine.AggregateChecksResultWithState(payload.Address, payload)
	if err != nil {
		r.config.Logger.Error("❌ Failed to process operator trigger",
			"operator", payload.Address,
			"task_id", payload.TaskId,
			"error", err)
		return nil, err
	}

	r.config.Logger.Debug("✅ Operator trigger processed successfully",
		"operator", payload.Address,
		"task_id", payload.TaskId,
		"status", executionState.Status,
		"remaining_executions", executionState.RemainingExecutions,
		"task_still_enabled", executionState.TaskStillEnabled)

	return &avsproto.NotifyTriggersResp{
		UpdatedAt:           timestamppb.Now(),
		RemainingExecutions: executionState.RemainingExecutions,
		TaskStillEnabled:    executionState.TaskStillEnabled,
		Status:              executionState.Status,
		Message:             executionState.Message,
	}, nil
}

// Operator action
func (r *RpcServer) Ack(ctx context.Context, payload *avsproto.AckMessageReq) (*wrapperspb.BoolValue, error) {
	// TODO: Implement ACK before merge

	return wrapperspb.Bool(true), nil
}

// HealthCheck provides a simple connection test that doesn't store any data
func (r *RpcServer) HealthCheck(ctx context.Context, req *avsproto.HealthCheckRequest) (*avsproto.HealthCheckResponse, error) {
	// Simple health check - just verify the connection works
	// No authentication required, no data storage

	r.config.Logger.Debug("Health check request received",
		"operator_address", req.OperatorAddress,
	)

	return &avsproto.HealthCheckResponse{
		Status:    "OK",
		Message:   "Aggregator is running",
		Timestamp: uint64(time.Now().UnixMilli()),
	}, nil
}

// startRpcServer initializes and establish a tcp socket on given address from
// config file
func (agg *Aggregator) startRpcServer(ctx context.Context) error {
	// https://github.com/grpc/grpc-go/blob/master/examples/helloworld/greeter_server/main.go#L50
	lis, err := net.Listen("tcp", agg.config.RpcBindAddress)
	if err != nil {
		panic(fmt.Errorf("failed to listen to %v", err))
	}

	ethrpc, err := ethclient.Dial(agg.config.EthHttpRpcUrl)

	if err != nil {
		panic(err)
	}

	smartwalletClient, err := ethclient.Dial(agg.config.SmartWallet.EthRpcUrl)
	if err != nil {
		panic(err)
	}

	// Create global WebSocket client for transaction monitoring
	smartwalletWsClient, err := ethclient.Dial(agg.config.SmartWallet.EthWsUrl)
	if err != nil {
		agg.logger.Warn("Failed to create WebSocket client for transaction monitoring", "error", err, "wsUrl", agg.config.SmartWallet.EthWsUrl)
		// Continue without WebSocket - withdrawals will work but won't wait for confirmation
		smartwalletWsClient = nil
	}

	smartWalletChainID, err := smartwalletClient.ChainID(context.Background())
	if err != nil {
		panic(err)
	}

	rpcServer := &RpcServer{
		cache:  agg.cache,
		db:     agg.db,
		engine: agg.engine,

		ethrpc:           ethrpc,
		smartWalletRpc:   smartwalletClient,
		smartWalletWsRpc: smartwalletWsClient,

		config:        agg.config,
		operatorPool:  agg.operatorPool,
		chainID:       smartWalletChainID,
		chainRegistry: agg.chainRegistry,
	}

	// Operator authentication has to resolve alias keys. Those live on
	// the APConfig of the AVS chain the operator registered on — Sepolia
	// for the testnet operator, Ethereum for the two alias-key mainnet
	// operators. Binding only eth_rpc_url (Sepolia in production) would
	// refuse most of the fleet at first heartbeat. A failure here is
	// fatal: a degraded resolver is a silent outage.
	bindCtx, bindCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer bindCancel()
	aliasSources, err := bindAliasSources(bindCtx, ethrpc, agg.chainRegistry, agg.logger)
	if err != nil {
		return fmt.Errorf("operator alias resolution: %w", err)
	}
	rpcServer.aliasResolver = newOperatorAliasResolver(aliasSources, agg.logger)
	for _, src := range aliasSources {
		agg.logger.Info("operator alias resolution enabled",
			"source", src.name, "apconfig_address", src.address.Hex(), "chain_id", src.chainID)
	}

	// Expose the smart-wallet clients + rpcServer to the rest of the
	// aggregator (specifically the REST layer's WithdrawService /
	// EstimateFees / GetWalletNonce handlers). startHttpServer runs
	// after startRpcServer so these reads are always populated by the
	// time the REST router is mounted.
	agg.smartWalletRpc = smartwalletClient
	agg.smartWalletWsRpc = smartwalletWsClient
	agg.rpcServer = rpcServer

	// The Aggregator service (public client surface) is no longer
	// registered. Clients use the REST API at /api/v1 — see
	// aggregator/rest/ and api/openapi.yaml. The proto service is
	// kept (marked DEPRECATED) so generated types stay available
	// and old SDKs get a clear "Unimplemented" instead of a wire
	// parse error. Handler methods on RpcServer that implemented
	// the removed interface are dead code in this commit; they get
	// deleted in a follow-up alongside the proto service block.

	// Every Node RPC is authenticated by these interceptors — see
	// operator_auth_interceptor.go. They are attached at construction so
	// there is no window in which the service is registered without
	// them, and so a handler added later inherits the check instead of
	// having to remember it.
	s := grpc.NewServer(
		grpc.UnaryInterceptor(rpcServer.operatorUnaryInterceptor()),
		grpc.StreamInterceptor(rpcServer.operatorStreamInterceptor()),
	)

	avsproto.RegisterNodeServer(s, rpcServer)

	// Reflection lets any client enumerate the service and every message
	// shape on it. That is worth having while developing and is pure
	// attack surface in production, where operators speak to us through
	// generated stubs and never ask the server what it offers.
	// Gated on the `environment:` config field rather than an env var:
	// APP_ENV is set in no deployment, so a check against it would leave
	// reflection on everywhere it matters.
	if agg.config != nil && agg.config.Environment == sdklogging.Development {
		// Register reflection service on gRPC server.
		// This allow clien to discover url endpoint
		// https://github.com/grpc/grpc-go/blob/master/Documentation/server-reflection-tutorial.md
		reflection.Register(s)
	}

	agg.logger.Info("start grpc server",
		"address", lis.Addr(),
	)

	goSafe(func() {
		if err := s.Serve(lis); err != nil {
			agg.logger.Error("gRPC server failed to serve", "error", err)
		}
	})
	return nil
}
