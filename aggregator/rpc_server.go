package aggregator

import (
	"context"
	"log"
	"math/big"
	"net"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
	wrapperspb "google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/AvaProtocol/ap-avs/core/taskengine"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/AvaProtocol/ap-avs/storage"
)

// RpcServer is our grpc sever struct hold the entry point of request handler
type RpcServer struct {
	avsproto.UnimplementedAggregatorServer
	config *config.Config
	db     storage.Storage
	engine *taskengine.Engine

	operatorPool *OperatorPool

	ethrpc *ethclient.Client

	smartWalletRpc *ethclient.Client
}

// Get nonce of an existing smart wallet of a given owner
func (r *RpcServer) GetNonce(ctx context.Context, payload *avsproto.NonceRequest) (*avsproto.NonceResp, error) {

	ownerAddress := common.HexToAddress(payload.Owner)

	nonce, err := aa.GetNonce(r.smartWalletRpc, ownerAddress, big.NewInt(0))
	if err != nil {
		return nil, err
	}

	return &avsproto.NonceResp{
		Nonce: nonce.String(),
	}, nil
}

// GetAddress returns smart account address of the given owner in the auth key
func (r *RpcServer) GetSmartAccountAddress(ctx context.Context, payload *avsproto.AddressRequest) (*avsproto.AddressResp, error) {
	ownerAddress := common.HexToAddress(payload.Owner)
	salt := big.NewInt(0)

	nonce, err := aa.GetNonce(r.smartWalletRpc, ownerAddress, salt)
	if err != nil {
		return nil, err
	}

	sender, err := aa.GetSenderAddress(r.smartWalletRpc, ownerAddress, salt)

	return &avsproto.AddressResp{
		Nonce:               nonce.String(),
		SmartAccountAddress: sender.String(),
	}, nil
}

func (r *RpcServer) CancelTask(ctx context.Context, taskID *avsproto.UUID) (*wrapperspb.BoolValue, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, err
	}

	r.config.Logger.Info("Process Cancel Task",
		"user", user.Address.String(),
		"taskID", string(taskID.Bytes),
	)

	result, err := r.engine.CancelTaskByUser(user, string(taskID.Bytes))

	if err != nil {
		return nil, err
	}

	return wrapperspb.Bool(result), nil
}

func (r *RpcServer) CreateTask(ctx context.Context, taskPayload *avsproto.CreateTaskReq) (*avsproto.CreateTaskResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, err
	}

	task, err := r.engine.CreateTask(user, taskPayload)
	if err != nil {
		return nil, err
	}

	return &avsproto.CreateTaskResp{
		Id: task.ID,
	}, nil
}

func (r *RpcServer) ListTasks(ctx context.Context, _ *avsproto.ListTasksReq) (*avsproto.ListTasksResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, err
	}

	r.config.Logger.Info("Process List Task",
		"user", user.Address.String(),
	)
	tasks, err := r.engine.ListTasksByUser(user)

	return &avsproto.ListTasksResp{
		Tasks: tasks,
	}, nil
}

func (r *RpcServer) GetTask(ctx context.Context, taskID *avsproto.UUID) (*avsproto.Task, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, err
	}

	r.config.Logger.Info("Process Get Task",
		"user", user.Address.String(),
		"taskID", string(taskID.Bytes),
	)

	task, err := r.engine.GetTaskByUser(user, string(taskID.Bytes))
	if err != nil {
		return nil, err
	}

	return task.ToProtoBuf()
}

func (r *RpcServer) SyncTasks(payload *avsproto.SyncTasksReq, srv avsproto.Aggregator_SyncTasksServer) error {
	log.Printf("sync task for operator : %s", payload.Address)

	err := r.engine.StreamCheckToOperator(payload, srv)
	log.Printf("close sync stream for: %s %v", payload.Address, err)

	return err
}

func (r *RpcServer) UpdateChecks(ctx context.Context, payload *avsproto.UpdateChecksReq) (*avsproto.UpdateChecksResp, error) {
	if err := r.engine.AggregateChecksResult(payload.Address, payload.Id); err != nil {
		return nil, err
	}

	return &avsproto.UpdateChecksResp{
		UpdatedAt: timestamppb.Now(),
	}, nil
}

// startRpcServer initializes and establish a tcp socket on given address from
// config file
func (agg *Aggregator) startRpcServer(ctx context.Context) error {
	// https://github.com/grpc/grpc-go/blob/master/examples/helloworld/greeter_server/main.go#L50
	lis, err := net.Listen("tcp", agg.config.RpcBindAddress)
	if err != nil {
		log.Fatalf("Failed to listen to %v", err)
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

	avsproto.RegisterAggregatorServer(s, &RpcServer{
		db:     agg.db,
		engine: agg.engine,

		ethrpc:         ethrpc,
		smartWalletRpc: smartwalletClient,

		config:       agg.config,
		operatorPool: agg.operatorPool,
	})

	// Register reflection service on gRPC server.
	// This allow clien to discover url endpoint
	// https://github.com/grpc/grpc-go/blob/master/Documentation/server-reflection-tutorial.md
	reflection.Register(s)

	agg.logger.Info("start grpc server",
		"address", lis.Addr(),
	)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
	return nil
}
