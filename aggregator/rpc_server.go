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

	"github.com/AvaProtocol/ap-avs/core/auth"
	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/AvaProtocol/ap-avs/core/taskengine"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/AvaProtocol/ap-avs/storage"
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
}

// Get nonce of an existing smart wallet of a given owner
func (r *RpcServer) CreateWallet(ctx context.Context, payload *avsproto.CreateWalletReq) (*avsproto.CreateWalletResp, error) {
	user, err := r.verifyAuth(ctx)

	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.InvalidAuthenticationKey, err.Error())
	}
	r.config.Logger.Info("process create wallet",
		"user", user.Address.String(),
		"salt", payload.Salt,
	)

	return r.engine.CreateSmartWallet(user, payload)
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
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.InvalidAuthenticationKey, err.Error())
	}

	r.config.Logger.Info("process list wallet",
		"address", user.Address.String(),
	)
	wallets, err := r.engine.GetSmartWallets(user.Address)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "rpc server is unavailable, retry later. %s", err.Error())
	}

	return &avsproto.ListWalletResp{
		Wallets: wallets,
	}, nil
}

func (r *RpcServer) CancelTask(ctx context.Context, taskID *avsproto.IdReq) (*wrapperspb.BoolValue, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.InvalidAuthenticationKey, err.Error())
	}

	r.config.Logger.Info("process cancel task",
		"user", user.Address.String(),
		"taskID", taskID.Id,
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
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.InvalidAuthenticationKey, err.Error())
	}

	r.config.Logger.Info("process delete task",
		"user", user.Address.String(),
		"taskID", string(taskID.Id),
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
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.InvalidAuthenticationKey, err.Error())
	}

	r.config.Logger.Info("process list task",
		"user", user.Address.String(),
		"smart_wallet_address", payload.SmartWalletAddress,
	)
	return r.engine.ListTasksByUser(user, payload)
}

func (r *RpcServer) ListExecutions(ctx context.Context, payload *avsproto.ListExecutionsReq) (*avsproto.ListExecutionsResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.InvalidAuthenticationKey, err.Error())
	}

	r.config.Logger.Info("process list execution",
		"user", user.Address.String(),
		"task_id", payload.Id,
	)
	return r.engine.ListExecutions(user, payload)
}

func (r *RpcServer) GetTask(ctx context.Context, payload *avsproto.IdReq) (*avsproto.Task, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.InvalidAuthenticationKey, err.Error())
	}

	r.config.Logger.Info("process get task",
		"user", user.Address.String(),
		"taskID", payload.Id,
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
		panic(fmt.Errorf("Failed to listen to %v", err))
		return err
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

	rpcServer := &RpcServer{
		cache:  agg.cache,
		db:     agg.db,
		engine: agg.engine,

		ethrpc:         ethrpc,
		smartWalletRpc: smartwalletClient,

		config:       agg.config,
		operatorPool: agg.operatorPool,
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
