package aggregator

import (
	"context"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"github.com/allegro/bigcache/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

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
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
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
	entry, err := r.chainRegistry.GetWorker(requestedChainID)
	if err != nil {
		return nil, err
	}
	if entry.Config == nil || entry.Config.SmartWallet == nil {
		return nil, fmt.Errorf("chain %d has no smart_wallet config", requestedChainID)
	}
	return entry.Config.SmartWallet, nil
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

	// Balance preflight reads route through the chain's worker (gateway
	// mode) so the gateway holds no direct chain-RPC connection for the
	// withdraw flow. Fall back to a direct-RPC reader only when no
	// worker-routed reader is registered (single-chain mode / startup
	// race) — that path lazily dials the chain RPC via resolveSmartWalletForChain.
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

	// Check if smart wallet config is available
	if swCfg == nil {
		return nil, status.Errorf(codes.Internal, "smart wallet configuration not available")
	}

	// Enable paymaster for gas sponsorship (15 minute validity)
	// Skip reimbursement for withdrawals — the paymaster absorbs gas costs so users
	// can withdraw their full balance without reserving ETH for gas reimbursement.
	paymasterReq := preset.GetVerifyingPaymasterRequestForDuration(
		swCfg.PaymasterAddress,
		15*time.Minute,
	)
	paymasterReq.SkipReimbursement = true

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
		// Validate the token balance.
		tokenBalance, balanceErr := chainReader.GetTokenBalance(ctx, common.HexToAddress(payload.Token), *smartWalletAddress)
		if balanceErr != nil {
			return nil, status.Errorf(codes.Internal, "failed to get token balance: %v", balanceErr)
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

	// Send UserOp via preset.SendUserOp with global WebSocket client
	userOp, receipt, err := r.sendUserOpWithGlobalWs(
		user.Address,
		callData,
		smartWalletAddress,
		paymasterReq,
		requestedChainID,
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
		userOpHash := userOp.GetUserOpHash(swCfg.EntrypointAddress, big.NewInt(swCfg.ChainID))
		resp.UserOpHash = userOpHash.Hex()
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

// sendUserOpWithGlobalWs sends a UserOp using the global WebSocket client for efficient transaction monitoring.
// In gateway mode, it delegates to the appropriate chain worker instead.
// requestedChainID picks the worker in gateway mode; pass 0 to use the
// gateway's default chain (single-chain mode ignores the argument).
func (r *RpcServer) sendUserOpWithGlobalWs(
	owner common.Address,
	callData []byte,
	smartWalletAddress *common.Address,
	paymasterReq *preset.VerifyingPaymasterRequest,
	requestedChainID int64,
) (*userop.UserOperation, *types.Receipt, error) {
	// Gateway mode: route to worker
	if r.chainRegistry != nil {
		return r.sendUserOpViaWorker(owner, callData, smartWalletAddress, paymasterReq, requestedChainID)
	}

	// Use global WebSocket client if available, otherwise fall back to creating new connection
	// Note: salt=nil here because rpc_server callers (e.g. WithdrawFunds) operate on already-deployed wallets
	if r.smartWalletWsRpc != nil {
		return preset.SendUserOpWithWsClient(
			r.config.SmartWallet,
			owner,
			callData,
			paymasterReq, // Use provided paymaster request
			smartWalletAddress,
			nil,                // saltOverride - not needed for already-deployed wallets
			r.smartWalletWsRpc, // Use global WebSocket client
			nil,                // executionFeeWei - no platform fee for direct RPC calls (e.g., WithdrawFunds)
			r.config.Logger,    // Pass logger for debug/verbose logging
		)
	} else {
		// Fallback to original method (creates new WebSocket connection)
		r.config.Logger.Warn("Global WebSocket client not available, using fallback method")
		return preset.SendUserOp(
			r.config.SmartWallet,
			owner,
			callData,
			paymasterReq, // Use provided paymaster request
			smartWalletAddress,
			nil,             // saltOverride - not needed for already-deployed wallets
			nil,             // executionFeeWei - no platform fee for direct RPC calls
			r.config.Logger, // Pass logger for debug/verbose logging
		)
	}
}

// sendUserOpViaWorker delegates UserOp execution to a chain worker in gateway mode.
// It converts the worker response into the same types returned by preset.SendUserOp.
func (r *RpcServer) sendUserOpViaWorker(
	owner common.Address,
	callData []byte,
	smartWalletAddress *common.Address,
	paymasterReq *preset.VerifyingPaymasterRequest,
	chainID int64,
) (*userop.UserOperation, *types.Receipt, error) {
	worker, err := r.chainRegistry.GetWorker(chainID)
	if err != nil {
		return nil, nil, fmt.Errorf("no worker for chain %d: %w", chainID, err)
	}

	req := &avsproto.ExecuteUserOpReq{
		Owner:        owner.Hex(),
		CallData:     callData,
		UsePaymaster: paymasterReq != nil,
	}
	if smartWalletAddress != nil {
		req.SmartWalletAddress = smartWalletAddress.Hex()
	}

	resp, err := worker.Client.ExecuteUserOp(context.Background(), req)
	if err != nil {
		return nil, nil, fmt.Errorf("worker ExecuteUserOp failed: %w", err)
	}
	if !resp.Success {
		return nil, nil, fmt.Errorf("worker ExecuteUserOp error: %s", resp.Error)
	}

	// Convert worker response to local types for caller compatibility
	receipt := &types.Receipt{
		TxHash:  common.HexToHash(resp.TxHash),
		GasUsed: resp.GasUsed,
	}
	if resp.GasCostWei != "" {
		gasCost, ok := new(big.Int).SetString(resp.GasCostWei, 10)
		if ok && resp.GasUsed > 0 {
			receipt.EffectiveGasPrice = new(big.Int).Div(gasCost, new(big.Int).SetUint64(resp.GasUsed))
		}
	}

	return nil, receipt, nil
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

	s := grpc.NewServer()

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
	avsproto.RegisterNodeServer(s, rpcServer)

	// Register reflection service on gRPC server.
	// This allow clien to discover url endpoint
	// https://github.com/grpc/grpc-go/blob/master/Documentation/server-reflection-tutorial.md
	reflection.Register(s)

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
