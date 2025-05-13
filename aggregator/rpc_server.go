package aggregator

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"net"

	"github.com/allegro/bigcache/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
	wrapperspb "google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/auth"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
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

	smartWalletRpc *ethclient.Client
	chainID        *big.Int
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

// Get nonce of an existing smart wallet of a given owner
func (r *RpcServer) GetNonce(ctx context.Context, payload *avsproto.NonceRequest) (*avsproto.NonceResp, error) {
	ownerAddress := common.HexToAddress(payload.Owner)

	nonce, err := aa.GetNonce(r.smartWalletRpc, ownerAddress, big.NewInt(0))
	if err != nil {
		return nil, status.Errorf(codes.Code(avsproto.Error_SmartWalletRpcError), taskengine.NonceFetchingError)
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

	r.config.Logger.Info("process list wallet",
		"address", user.Address.String(),
	)
	wallets, err := r.engine.GetSmartWallets(user.Address, payload)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "rpc server is unavailable, retry later. %s", err.Error())
	}

	return &avsproto.ListWalletResp{
		Items: wallets,
	}, nil
}

func (r *RpcServer) CancelTask(ctx context.Context, taskID *avsproto.IdReq) (*wrapperspb.BoolValue, error) {
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

	return wrapperspb.Bool(result), nil
}

func (r *RpcServer) DeleteTask(ctx context.Context, taskID *avsproto.IdReq) (*wrapperspb.BoolValue, error) {
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

	return wrapperspb.Bool(result), nil
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

	r.config.Logger.Info("process list task",
		"user", user.Address.String(),
		"smart_wallet_address", payload.SmartWalletAddress,
		"cursor", payload.Cursor,
	)
	return r.engine.ListTasksByUser(user, payload)
}

func (r *RpcServer) ListExecutions(ctx context.Context, payload *avsproto.ListExecutionsReq) (*avsproto.ListExecutionsResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process list execution",
		"user", user.Address.String(),
		"task_id", payload.TaskIds,
		"cursor", payload.Cursor,
	)
	return r.engine.ListExecutions(user, payload)
}

func (r *RpcServer) GetExecution(ctx context.Context, payload *avsproto.ExecutionReq) (*avsproto.Execution, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process get execution",
		"user", user.Address.String(),
		"task_id", payload.TaskId,
		"execution_id", payload.ExecutionId,
	)
	return r.engine.GetExecution(user, payload)
}

func (r *RpcServer) GetExecutionStatus(ctx context.Context, payload *avsproto.ExecutionReq) (*avsproto.ExecutionStatusResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process get execution",
		"user", user.Address.String(),
		"task_id", payload.TaskId,
		"execution_id", payload.ExecutionId,
	)
	return r.engine.GetExecutionStatus(user, payload)
}

func (r *RpcServer) GetTask(ctx context.Context, payload *avsproto.IdReq) (*avsproto.Task, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process get task",
		"user", user.Address.String(),
		"task_id", payload.Id,
	)

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
func (r *RpcServer) TriggerTask(ctx context.Context, payload *avsproto.UserTriggerTaskReq) (*avsproto.UserTriggerTaskResp, error) {
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

func (r *RpcServer) CreateSecret(ctx context.Context, payload *avsproto.CreateOrUpdateSecretReq) (*wrapperspb.BoolValue, error) {
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

	return wrapperspb.Bool(result), nil
}

func (r *RpcServer) ListSecrets(ctx context.Context, payload *avsproto.ListSecretsReq) (*avsproto.ListSecretsResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process list secret",
		"user", user.Address.String(),
	)

	return r.engine.ListSecrets(user, payload)
}

func (r *RpcServer) UpdateSecret(ctx context.Context, payload *avsproto.CreateOrUpdateSecretReq) (*wrapperspb.BoolValue, error) {
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

	return wrapperspb.Bool(result), nil
}

func (r *RpcServer) DeleteSecret(ctx context.Context, payload *avsproto.DeleteSecretReq) (*wrapperspb.BoolValue, error) {
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

	return wrapperspb.Bool(result), nil

}

// GetWorkflowCount handles the RPC request to get the workflow count
func (r *RpcServer) GetWorkflowCount(ctx context.Context, req *avsproto.GetWorkflowCountReq) (*avsproto.GetWorkflowCountResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process workflow count",
		"user", user.Address.String(),
		"smart_wallet_address", req.Addresses,
	)

	return r.engine.GetWorkflowCount(user, req)
}

// GetExecutionCount handles the RPC request to get the execution count
func (r *RpcServer) GetExecutionCount(ctx context.Context, req *avsproto.GetExecutionCountReq) (*avsproto.GetExecutionCountResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process execution count",
		"user", user.Address.String(),
		"workflow_ids", req.WorkflowIds,
	)

	return r.engine.GetExecutionCount(user, req)
}

func (r *RpcServer) GetExecutionStats(ctx context.Context, req *avsproto.GetExecutionStatsReq) (*avsproto.GetExecutionStatsResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, err.Error())
	}

	r.config.Logger.Info("process execution stats",
		"user", user.Address.String(),
		"workflow_ids", req.WorkflowIds,
		"days", req.Days,
	)

	return r.engine.GetExecutionStats(user, req)
}
// Operator action
func (r *RpcServer) SyncMessages(payload *avsproto.SyncMessagesReq, srv avsproto.Node_SyncMessagesServer) error {
	err := r.engine.StreamCheckToOperator(payload, srv)

	return err
}

// Operator action
func (r *RpcServer) NotifyTriggers(ctx context.Context, payload *avsproto.NotifyTriggersReq) (*avsproto.NotifyTriggersResp, error) {
	if err := r.engine.AggregateChecksResult(payload.Address, payload); err != nil {
		return nil, err
	}

	return &avsproto.NotifyTriggersResp{
		UpdatedAt: timestamppb.Now(),
	}, nil
}

// Operator action
func (r *RpcServer) Ack(ctx context.Context, payload *avsproto.AckMessageReq) (*wrapperspb.BoolValue, error) {
	// TODO: Implement ACK before merge

	return wrapperspb.Bool(true), nil
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

	smartWalletChainID, err := smartwalletClient.ChainID(context.Background())
	if err != nil {
		panic(err)
	}

	rpcServer := &RpcServer{
		cache:  agg.cache,
		db:     agg.db,
		engine: agg.engine,

		ethrpc:         ethrpc,
		smartWalletRpc: smartwalletClient,

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

	go func() {
		if err := s.Serve(lis); err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	}()
	return nil
}
