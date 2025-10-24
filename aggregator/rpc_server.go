package aggregator

import (
	"context"
	"fmt"
	"math/big"
	"net"
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
	"google.golang.org/protobuf/types/known/structpb"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
	wrapperspb "google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/auth"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/services"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// RpcServer is our grpc sever struct hold the entry point of request handler
type RpcServer struct {
	avsproto.UnimplementedAggregatorServer
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
}

// FallbackPriceService provides hardcoded fallback prices when Moralis is unavailable
type FallbackPriceService struct{}

func newFallbackPriceService() *FallbackPriceService {
	return &FallbackPriceService{}
}

func (fps *FallbackPriceService) GetNativeTokenPriceUSD(chainID int64) (*big.Float, error) {
	// Only include chains that the aggregator actually supports: Ethereum and Base
	fallbackPrices := map[int64]float64{
		1:        2500.0, // Ethereum Mainnet
		11155111: 2500.0, // Ethereum Sepolia
		8453:     2500.0, // Base Mainnet
		84532:    2500.0, // Base Sepolia
	}

	if price, exists := fallbackPrices[chainID]; exists {
		return big.NewFloat(price), nil
	}
	return big.NewFloat(2500.0), nil // Default ETH price
}

func (fps *FallbackPriceService) GetNativeTokenSymbol(chainID int64) string {
	// Only include chains that the aggregator actually supports: Ethereum and Base
	// All supported chains use ETH as the native token
	tokenSymbols := map[int64]string{
		1:        "ETH", // Ethereum Mainnet
		11155111: "ETH", // Ethereum Sepolia
		8453:     "ETH", // Base Mainnet
		84532:    "ETH", // Base Sepolia
	}

	if symbol, exists := tokenSymbols[chainID]; exists {
		return symbol
	}
	return "ETH"
}

// FallbackPriceInfo provides information about fallback pricing for logging
func (fps *FallbackPriceService) FallbackPriceInfo() string {
	return "using conservative ETH price of $2500"
}

// Get nonce of an existing smart wallet of a given owner
func (r *RpcServer) GetWallet(ctx context.Context, payload *avsproto.GetWalletReq) (*avsproto.GetWalletResp, error) {
	user, err := r.verifyAuth(ctx)

	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}
	r.config.Logger.Info("process create wallet",
		"user", user.Address.String(),
		"salt", payload.Salt,
		"factory", payload.FactoryAddress,
	)

	return r.engine.GetWallet(user, payload)
}

func (r *RpcServer) SetWallet(ctx context.Context, payload *avsproto.SetWalletReq) (*avsproto.GetWalletResp, error) {
	user, err := r.verifyAuth(ctx)

	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process set wallet",
		"user", user.Address.String(),
		"salt", payload.Salt,
		"factory", payload.FactoryAddress,
		"isHidden", payload.IsHidden,
	)

	return r.engine.SetWallet(user.Address, payload)
}

// Get nonce of an existing smart wallet of a given owner
func (r *RpcServer) GetNonce(ctx context.Context, payload *avsproto.NonceRequest) (*avsproto.NonceResp, error) {
	ownerAddress := common.HexToAddress(payload.Owner)

	nonce, err := aa.GetNonce(r.smartWalletRpc, ownerAddress, big.NewInt(0))
	if err != nil {
		return nil, status.Errorf(codes.Code(avsproto.ErrorCode_SMART_WALLET_RPC_ERROR), taskengine.NonceFetchingError)
	}

	return &avsproto.NonceResp{
		Nonce: nonce.String(),
	}, nil
}

// GetAddress returns smart account address of the given owner in the auth key
func (r *RpcServer) ListWallets(ctx context.Context, payload *avsproto.ListWalletReq) (*avsproto.ListWalletResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	return r.engine.ListWallets(user.Address, payload)
}

// WithdrawFunds handles withdrawal of funds from a smart wallet using UserOp
func (r *RpcServer) WithdrawFunds(ctx context.Context, payload *avsproto.WithdrawFundsReq) (*avsproto.WithdrawFundsResp, error) {
	// Authenticate the user
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process withdraw funds",
		"user", user.Address.String(),
		"recipient", payload.RecipientAddress,
		"amount", payload.Amount,
		"token", payload.Token,
		"smart_wallet", payload.SmartWalletAddress,
	)

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

	// Parse amount
	amount, success := new(big.Int).SetString(payload.Amount, 10)
	if !success || amount == nil || amount.Cmp(big.NewInt(0)) <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "invalid amount: must be a positive integer")
	}

	// Build withdrawal parameters
	params := &WithdrawalParams{
		RecipientAddress: common.HexToAddress(payload.RecipientAddress),
		Amount:           amount,
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
	// Validate that the provided address belongs to the authenticated user by checking wallet database
	validationErr := r.validateSmartWalletOwnership(user.Address, *params.SmartWalletAddress)
	if validationErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid smart wallet address: %v", validationErr)
	}

	smartWalletAddress := params.SmartWalletAddress

	// Build withdrawal calldata
	callData, err := BuildWithdrawalCalldata(params)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to build withdrawal calldata: %v", err)
	}

	// Check if smart wallet config is available
	if r.config.SmartWallet == nil {
		return nil, status.Errorf(codes.Internal, "smart wallet configuration not available")
	}

	// Enable paymaster for gas sponsorship (15 minute validity)
	paymasterReq := preset.GetVerifyingPaymasterRequestForDuration(
		r.config.SmartWallet.PaymasterAddress,
		15*time.Minute,
	)

	r.config.Logger.Info("processing withdrawal with paymaster sponsorship",
		"user", user.Address.String(),
		"smartWallet", smartWalletAddress.Hex(),
		"paymaster", r.config.SmartWallet.PaymasterAddress.Hex(),
		"amount", payload.Amount,
		"token", payload.Token,
	)

	// Send UserOp via preset.SendUserOp with global WebSocket client
	userOp, receipt, err := r.sendUserOpWithGlobalWs(
		user.Address,
		callData,
		smartWalletAddress,
		paymasterReq,
	)

	if err != nil {
		r.config.Logger.Error("failed to send withdrawal UserOp",
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
		// Get UserOp hash
		userOpHash := userOp.GetUserOpHash(r.config.SmartWallet.EntrypointAddress, big.NewInt(int64(r.config.SmartWallet.ChainID)))
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

// validateSmartWalletOwnership validates that the smart wallet address belongs to the specified owner and is deployed
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

	// Validate wallet is deployed on-chain
	code, err := r.smartWalletRpc.CodeAt(context.Background(), smartWalletAddress, nil)
	if err != nil {
		return fmt.Errorf("failed to check if smart wallet is deployed: %w", err)
	}
	if len(code) == 0 {
		return fmt.Errorf("smart wallet %s is not deployed yet - please deploy it first by making a transaction", smartWalletAddress.Hex())
	}

	return nil
}

// sendUserOpWithGlobalWs sends a UserOp using the global WebSocket client for efficient transaction monitoring
func (r *RpcServer) sendUserOpWithGlobalWs(
	owner common.Address,
	callData []byte,
	smartWalletAddress *common.Address,
	paymasterReq *preset.VerifyingPaymasterRequest,
) (*userop.UserOperation, *types.Receipt, error) {
	// Use global WebSocket client if available, otherwise fall back to creating new connection
	if r.smartWalletWsRpc != nil {
		return preset.SendUserOpWithWsClient(
			r.config.SmartWallet,
			owner,
			callData,
			paymasterReq, // Use provided paymaster request
			smartWalletAddress,
			r.smartWalletWsRpc, // Use global WebSocket client
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
			r.config.Logger, // Pass logger for debug/verbose logging
		)
	}
}

func (r *RpcServer) CancelTask(ctx context.Context, taskID *avsproto.IdReq) (*avsproto.CancelTaskResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process cancel task",
		"user", user.Address.String(),
		"task_id", taskID.Id,
	)

	result, err := r.engine.CancelTaskByUser(user, string(taskID.Id))

	if err != nil {
		return nil, err
	}

	return result, nil
}

func (r *RpcServer) DeleteTask(ctx context.Context, taskID *avsproto.IdReq) (*avsproto.DeleteTaskResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process delete task",
		"user", user.Address.String(),
		"task_id", string(taskID.Id),
	)

	result, err := r.engine.DeleteTaskByUser(user, string(taskID.Id))

	if err != nil {
		return nil, err
	}

	return result, nil
}

func (r *RpcServer) CreateTask(ctx context.Context, taskPayload *avsproto.CreateTaskReq) (*avsproto.CreateTaskResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.InvalidAuthenticationKey, err.Error())
	}

	task, err := r.engine.CreateTask(user, taskPayload)
	if err != nil {
		return nil, err
	}

	return &avsproto.CreateTaskResp{
		Id: task.Id,
	}, nil
}

func (r *RpcServer) ListTasks(ctx context.Context, payload *avsproto.ListTasksReq) (*avsproto.ListTasksResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	// r.config.Logger.Info("process list task",
	// 	"user", user.Address.String(),
	// 	"smart_wallet_address", payload.SmartWalletAddress,
	// )

	listTaskResp, err := r.engine.ListTasksByUser(user, payload)
	if err != nil {
		contextFields := map[string]interface{}{
			"smart_wallet_address": payload.SmartWalletAddress,
			"before_cursor":        payload.Before,
			"after_cursor":         payload.After,
			"limit":                payload.Limit,
		}
		return nil, r.handlePaginationError(err, "ListTasks", user.Address.String(), contextFields)
	}

	return listTaskResp, nil
}

func (r *RpcServer) ListExecutions(ctx context.Context, payload *avsproto.ListExecutionsReq) (*avsproto.ListExecutionsResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	listExecResp, err := r.engine.ListExecutions(user, payload)
	if err != nil {
		contextFields := map[string]interface{}{
			"task_ids":      payload.TaskIds,
			"before_cursor": payload.Before,
			"after_cursor":  payload.After,
			"limit":         payload.Limit,
		}
		return nil, r.handlePaginationError(err, "ListExecutions", user.Address.String(), contextFields)
	}

	return listExecResp, nil
}

func (r *RpcServer) GetExecution(ctx context.Context, payload *avsproto.ExecutionReq) (*avsproto.Execution, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	return r.engine.GetExecution(user, payload)
}

func (r *RpcServer) GetExecutionStatus(ctx context.Context, payload *avsproto.ExecutionReq) (*avsproto.ExecutionStatusResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	return r.engine.GetExecutionStatus(user, payload)
}

func (r *RpcServer) GetTask(ctx context.Context, payload *avsproto.IdReq) (*avsproto.Task, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	if payload.Id == "" {
		return nil, status.Errorf(codes.InvalidArgument, taskengine.TaskIDMissing)
	}

	task, err := r.engine.GetTask(user, payload.Id)
	if err != nil {
		return nil, err
	}

	return task.ToProtoBuf()
}

// TriggerTask emit a trigger event that cause the task to be queue and execute eventually. It's similar to a trigger
// sending by operator, but in this case the user manually provide a trigger point to force run it.
func (r *RpcServer) TriggerTask(ctx context.Context, payload *avsproto.TriggerTaskReq) (*avsproto.TriggerTaskResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process trigger task",
		"user", user.Address.String(),
		"task_id", payload.TaskId,
	)

	if payload.TaskId == "" {
		return nil, status.Errorf(codes.InvalidArgument, taskengine.TaskIDMissing)
	}

	return r.engine.TriggerTask(user, payload)
}

func (r *RpcServer) CreateSecret(ctx context.Context, payload *avsproto.CreateOrUpdateSecretReq) (*avsproto.CreateSecretResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process create secret",
		"user", user.Address.String(),
		"secret_name", payload.Name,
	)

	result, err := r.engine.CreateSecret(user, payload)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "")
	}

	return &avsproto.CreateSecretResp{
		Success: result,
	}, nil
}

func (r *RpcServer) ListSecrets(ctx context.Context, payload *avsproto.ListSecretsReq) (*avsproto.ListSecretsResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process list secret",
		"user", user.Address.String(),
	)

	listSecretResp, err := r.engine.ListSecrets(user, payload)
	if err != nil {
		contextFields := map[string]interface{}{
			"workflow_id":   payload.WorkflowId,
			"before_cursor": payload.Before,
			"after_cursor":  payload.After,
			"limit":         payload.Limit,
		}
		return nil, r.handlePaginationError(err, "ListSecrets", user.Address.String(), contextFields)
	}

	return listSecretResp, nil
}

func (r *RpcServer) UpdateSecret(ctx context.Context, payload *avsproto.CreateOrUpdateSecretReq) (*avsproto.UpdateSecretResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process update secret",
		"user", user.Address.String(),
		"secret_name", payload.Name,
	)

	result, err := r.engine.UpdateSecret(user, payload)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "")
	}

	return &avsproto.UpdateSecretResp{
		Success: result,
	}, nil
}

func (r *RpcServer) DeleteSecret(ctx context.Context, payload *avsproto.DeleteSecretReq) (*avsproto.DeleteSecretResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process delete secret",
		"user", user.Address.String(),
		"secret_name", payload.Name,
	)

	result, err := r.engine.DeleteSecret(user, payload)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "")
	}

	return result, nil
}

// GetWorkflowCount handles the RPC request to get the workflow count
func (r *RpcServer) GetWorkflowCount(ctx context.Context, req *avsproto.GetWorkflowCountReq) (*avsproto.GetWorkflowCountResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	// r.config.Logger.Info("process workflow count",
	// 	"user", user.Address.String(),
	// 	"smart_wallet_address", req.Addresses,
	// )

	return r.engine.GetWorkflowCount(user, req)
}

// GetExecutionCount handles the RPC request to get the execution count
func (r *RpcServer) GetExecutionCount(ctx context.Context, req *avsproto.GetExecutionCountReq) (*avsproto.GetExecutionCountResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	return r.engine.GetExecutionCount(user, req)
}

func (r *RpcServer) GetExecutionStats(ctx context.Context, req *avsproto.GetExecutionStatsReq) (*avsproto.GetExecutionStatsResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	return r.engine.GetExecutionStats(user, req)
}

func (r *RpcServer) RunNodeWithInputs(ctx context.Context, req *avsproto.RunNodeWithInputsReq) (*avsproto.RunNodeWithInputsResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process run node with inputs",
		"user", user.Address.String(),
		"node_type", req.NodeType,
	)

	// Add debug logging for the request details
	configKeys := make([]string, 0, len(req.NodeConfig))
	for k := range req.NodeConfig {
		configKeys = append(configKeys, k)
	}
	inputKeys := make([]string, 0, len(req.InputVariables))
	for k := range req.InputVariables {
		inputKeys = append(inputKeys, k)
	}

	r.config.Logger.Info("run node with inputs details",
		"user", user.Address.String(),
		"node_type", req.NodeType,
		"config_keys", configKeys,
		"input_keys", inputKeys,
	)

	// For contract read debugging, log the full request details
	if req.NodeType == avsproto.NodeType_NODE_TYPE_CONTRACT_READ {
		// Extract keys only to avoid logging sensitive data
		configKeys := getConfigKeys(req.NodeConfig)
		inputKeys := getInputKeys(req.InputVariables)

		r.config.Logger.Debug("ContractRead full request",
			"user", user.Address.String(),
			"config_keys", configKeys,
			"input_keys", inputKeys,
		)
	}

	// Call the immediate execution function directly
	result, err := r.engine.RunNodeImmediatelyRPC(user, req)
	if err != nil {
		r.config.Logger.Error("run node with inputs failed",
			"user", user.Address.String(),
			"error", err.Error(),
		)
		return nil, status.Errorf(codes.Internal, "execution failed: %v", err)
	}

	r.config.Logger.Info("run node with inputs completed",
		"user", user.Address.String(),
		"success", result.Success,
		"error", result.Error,
	)

	return result, nil
}

func (r *RpcServer) RunTrigger(ctx context.Context, req *avsproto.RunTriggerReq) (*avsproto.RunTriggerResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process run trigger",
		"user", user.Address.String(),
		"trigger_type", req.TriggerType,
	)

	// Add debug logging for the request details
	configKeys := make([]string, 0, len(req.TriggerConfig))
	for k := range req.TriggerConfig {
		configKeys = append(configKeys, k)
	}

	r.config.Logger.Info("run trigger details",
		"user", user.Address.String(),
		"trigger_type", req.TriggerType,
		"config_keys", configKeys,
	)

	// Call the trigger execution function directly
	result, err := r.engine.RunTriggerRPC(user, req)
	if err != nil {
		r.config.Logger.Error("run trigger failed",
			"user", user.Address.String(),
			"error", err.Error(),
		)
		return nil, status.Errorf(codes.Internal, "execution failed: %v", err)
	}

	r.config.Logger.Info("run trigger completed",
		"user", user.Address.String(),
		"success", result.Success,
		"error", result.Error,
	)

	return result, nil
}

func (r *RpcServer) SimulateTask(ctx context.Context, req *avsproto.SimulateTaskReq) (*avsproto.Execution, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process simulate task",
		"user", user.Address.String(),
		"trigger_type", req.Trigger.Type,
		"nodes_count", len(req.Nodes),
		"edges_count", len(req.Edges),
	)

	// Basic validation
	if req.Trigger == nil {
		return nil, status.Errorf(codes.InvalidArgument, "trigger is required for task simulation")
	}
	if len(req.Nodes) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "at least one node is required for task simulation")
	}

	// Convert protobuf input variables to Go native types
	inputVariables := make(map[string]interface{})
	for k, v := range req.InputVariables {
		inputVariables[k] = v.AsInterface()
	}

	r.config.Logger.Info("simulate task details",
		"user", user.Address.String(),
		"trigger_type", req.Trigger.Type,
		"trigger_name", req.Trigger.Name,
		"input_keys", getInputKeys(req.InputVariables),
	)

	// Call the simulation function with the provided task definition (no need to extract triggerType and triggerConfig)
	execution, err := r.engine.SimulateTask(user, req.Trigger, req.Nodes, req.Edges, inputVariables)
	if err != nil {
		r.config.Logger.Error("simulate task failed",
			"user", user.Address.String(),
			"trigger_name", req.Trigger.Name,
			"error", err.Error(),
		)
		return nil, status.Errorf(codes.Internal, "simulation failed: %v", err)
	}

	r.config.Logger.Info("simulate task completed",
		"user", user.Address.String(),
		"trigger_name", req.Trigger.Name,
		"status", execution.Status,
		"execution_id", execution.Id,
		"steps_count", len(execution.Steps),
	)

	return execution, nil
}

// GetTokenMetadata handles token metadata lookup requests
func (r *RpcServer) GetTokenMetadata(ctx context.Context, payload *avsproto.GetTokenMetadataReq) (*avsproto.GetTokenMetadataResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process get token metadata",
		"user", user.Address.String(),
		"address", payload.Address,
	)

	return r.engine.GetTokenMetadata(user, payload)
}

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

	// Cancel the overloaded task immediately
	cancelled, err := r.engine.CancelTask(alert.TaskId)
	if err != nil {
		r.config.Logger.Error("❌ Failed to cancel overloaded task",
			"task_id", alert.TaskId,
			"error", err)
		return &avsproto.EventOverloadResponse{
			TaskCancelled: false,
			Message:       fmt.Sprintf("Failed to cancel task: %v", err),
			Timestamp:     uint64(time.Now().UnixMilli()),
		}, nil
	}

	responseMessage := "Task cancelled due to event overload"
	if !cancelled {
		responseMessage = "Task was already cancelled or not found"
	}

	// Capture a message in Sentry for visibility
	sentry.CaptureMessage(fmt.Sprintf("Event overload detected for task %s: %s", alert.TaskId, alert.Details))

	r.config.Logger.Info("🛑 Task cancelled due to event overload",
		"task_id", alert.TaskId,
		"cancelled", cancelled)

	return &avsproto.EventOverloadResponse{
		TaskCancelled: cancelled,
		Message:       responseMessage,
		Timestamp:     uint64(time.Now().UnixMilli()),
	}, nil
}

// Helper functions for logging
func getConfigKeys(config map[string]*structpb.Value) []string {
	keys := make([]string, 0, len(config))
	for k := range config {
		keys = append(keys, k)
	}
	return keys
}

func getInputKeys(inputs map[string]*structpb.Value) []string {
	keys := make([]string, 0, len(inputs))
	for k := range inputs {
		keys = append(keys, k)
	}
	return keys
}

// handlePaginationError processes pagination-related errors and returns user-friendly messages
func (r *RpcServer) handlePaginationError(err error, methodName string, userAddr string, contextFields map[string]interface{}) error {
	if err == nil {
		return nil
	}

	// Enhanced error handling for cursor validation failures
	if st, ok := status.FromError(err); ok && st.Code() == codes.InvalidArgument {
		// Prepare logging fields
		logFields := []interface{}{
			"user", userAddr,
			"error", st.Message(),
		}

		// Add context-specific fields
		for key, value := range contextFields {
			logFields = append(logFields, key, value)
		}

		// Log detailed information about the invalid request
		r.config.Logger.Warn("invalid pagination parameters in "+methodName, logFields...)

		// Return a more user-friendly error message for cursor validation
		if st.Message() == "cursor is not valid" {
			return status.Errorf(codes.InvalidArgument,
				"Invalid pagination cursor. Please retry without pagination parameters or use a fresh cursor from a recent response.")
		}

		// Return original error for other InvalidArgument cases
		return err
	}

	// Prepare logging fields for non-pagination errors
	logFields := []interface{}{
		"user", userAddr,
		"error", err.Error(),
	}

	// Add context-specific fields
	for key, value := range contextFields {
		logFields = append(logFields, key, value)
	}

	// Log other types of errors
	r.config.Logger.Error("error in "+methodName, logFields...)
	return err
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
		"task_still_active", executionState.TaskStillActive)

	return &avsproto.NotifyTriggersResp{
		UpdatedAt:           timestamppb.Now(),
		RemainingExecutions: executionState.RemainingExecutions,
		TaskStillActive:     executionState.TaskStillActive,
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

// EstimateFees provides comprehensive fee estimation for workflow deployment
func (r *RpcServer) EstimateFees(ctx context.Context, req *avsproto.EstimateFeesReq) (*avsproto.EstimateFeesResp, error) {
	// Authenticate the user
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process estimate fees",
		"user", user.Address.String(),
		"trigger_type", req.Trigger.Type.String(),
		"nodes_count", len(req.Nodes),
		"runner", req.Runner)

	// Validate required fields
	if req.Trigger == nil {
		return nil, status.Errorf(codes.InvalidArgument, "trigger is required")
	}
	if len(req.Nodes) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "at least one node is required")
	}
	if req.CreatedAt <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "created_at must be a positive timestamp")
	}
	if req.ExpireAt <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "expire_at must be a positive timestamp")
	}
	if req.ExpireAt <= req.CreatedAt {
		return nil, status.Errorf(codes.InvalidArgument, "expire_at must be after created_at")
	}

	// Create price service (Moralis if API key available, otherwise fallback)
	var priceService taskengine.PriceService
	if r.config.MoralisApiKey != "" {
		priceService = services.GetMoralisService(r.config.MoralisApiKey, r.config.Logger)
	} else {
		priceService = newFallbackPriceService()
		var fallbackPriceInfo string
		// Try to extract fallback price info for logging
		type fallbackPricer interface {
			FallbackPriceInfo() string
		}
		if fp, ok := priceService.(fallbackPricer); ok {
			fallbackPriceInfo = fp.FallbackPriceInfo()
		} else {
			fallbackPriceInfo = "unknown fallback price"
		}
		r.config.Logger.Warn(fmt.Sprintf("No Moralis API key configured, using fallback price service for fee estimation (%s)", fallbackPriceInfo))
	}

	// Create fee estimator - use configuration-aware version if fee rates are configured
	var feeEstimator *taskengine.FeeEstimator
	if r.config.FeeRates != nil {
		// Use configurable fee rates from YAML configuration
		feeEstimator = taskengine.NewFeeEstimatorWithConfig(
			r.config.Logger,
			r.smartWalletRpc,
			r.engine.GetTenderlyClient(),
			r.config.SmartWallet,
			priceService,
			r.config.FeeRates,
		)
	} else {
		// Use hardcoded defaults (backwards compatible)
		feeEstimator = taskengine.NewFeeEstimator(
			r.config.Logger,
			r.smartWalletRpc,
			r.engine.GetTenderlyClient(),
			r.config.SmartWallet,
			priceService,
		)
	}

	// Perform fee estimation
	resp, err := feeEstimator.EstimateFees(ctx, req)
	if err != nil {
		r.config.Logger.Error("failed to estimate fees",
			"error", err.Error(),
			"user", user.Address.String(),
			"trigger_type", req.Trigger.Type.String())
		return nil, status.Errorf(codes.Internal, "failed to estimate fees: %v", err)
	}

	if !resp.Success {
		// Map custom error codes to gRPC status codes
		grpcCode := codes.Internal
		switch resp.ErrorCode {
		case avsproto.ErrorCode_SMART_WALLET_NOT_FOUND:
			grpcCode = codes.NotFound
		case avsproto.ErrorCode_INVALID_REQUEST:
			grpcCode = codes.InvalidArgument
		case avsproto.ErrorCode_SIMULATION_ERROR:
			grpcCode = codes.Unavailable
		default:
			grpcCode = codes.Internal
		}
		return nil, status.Errorf(grpcCode, resp.Error)
	}

	r.config.Logger.Info("✅ fee estimation completed successfully",
		"user", user.Address.String(),
		"final_total_usd", resp.FinalTotal.UsdAmount,
		"estimation_method", resp.GasFees.EstimationMethod)

	return resp, nil
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

		config:       agg.config,
		operatorPool: agg.operatorPool,
		chainID:      smartWalletChainID,
	}

	// TODO: split node and aggregator
	avsproto.RegisterAggregatorServer(s, rpcServer)
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
			agg.logger.Error("gRPC server failed to serve", "error", err.Error())
		}
	})
	return nil
}
