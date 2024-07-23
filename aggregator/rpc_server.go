package aggregator

import (
	"context"
	"log"
	"math/big"
	"net"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	wrapperspb "google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/AvaProtocol/ap-avs/model"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/AvaProtocol/ap-avs/storage"
)

// RpcServer is our grpc sever struct hold the entry point of request handler
type RpcServer struct {
	avsproto.UnimplementedAggregatorServer
	config *config.Config
	db     storage.Storage

	operatorPool *OperatorPool

	ethrpc *ethclient.Client
}

// Get nonce of an existing smart wallet of a given owner
func (r *RpcServer) GetNonce(ctx context.Context, payload *avsproto.NonceRequest) (*avsproto.NonceResp, error) {

	ownerAddress := common.HexToAddress(payload.Owner)

	nonce, err := aa.GetNonce(r.ethrpc, ownerAddress, big.NewInt(0))
	if err != nil {
		return nil, err
	}

	return &avsproto.NonceResp{
		Nonce: nonce.String(),
	}, nil
}

// GetAddress returns smart account address of the given owner in the auth key
func (r *RpcServer) GetAddress(ctx context.Context, payload *avsproto.AddressRequest) (*avsproto.AddressResp, error) {
	ownerAddress := common.HexToAddress(payload.Owner)
	salt := big.NewInt(0)

	nonce, err := aa.GetNonce(r.ethrpc, ownerAddress, salt)
	if err != nil {
		return nil, err
	}

	sender, err := aa.GetSenderAddress(r.ethrpc, ownerAddress, salt)

	return &avsproto.AddressResp{
		Nonce:               nonce.String(),
		SmartAccountAddress: sender.String(),
	}, nil
}

func (r *RpcServer) CancelTask(ctx context.Context, taskID *avsproto.UUID) (*wrapperspb.BoolValue, error) {
	return nil, nil
}

func (r *RpcServer) CreateTask(ctx context.Context, taskPayload *avsproto.CreateTaskReq) (*avsproto.CreateTaskResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, err
	}

	task, err := model.NewTaskFromProtobuf(user, taskPayload)
	if err != nil {
		return nil, err
	}

	updates := map[string][]byte{}

	// global unique key-value for fast lookup
	updates[task.ID], err = task.ToJSON()

	// storage to find task belong to a user
	updates[string(task.Key())] = []byte(model.TaskStatusActive)

	r.db.BatchWrite(updates)

	return &avsproto.CreateTaskResp{
		Id: task.ID,
	}, nil
}

func (r *RpcServer) ListTasks(ctx context.Context, _ *avsproto.ListTasksReq) (*avsproto.ListTasksResp, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, err
	}

	taskIDs, err := r.db.GetKeyHasPrefix([]byte(user.Address.String()))

	if err != nil {
		return nil, err
	}

	tasks := make([]*avsproto.ListTasksResp_TaskItemResp, len(taskIDs))
	for i, taskKey := range taskIDs {
		tasks[i] = &avsproto.ListTasksResp_TaskItemResp{
			Id: string(model.TaskKeyToId(taskKey)),
		}
	}

	return &avsproto.ListTasksResp{
		Tasks: tasks,
	}, nil
}

func (r *RpcServer) GetTask(ctx context.Context, taskID *avsproto.UUID) (*avsproto.Task, error) {
	user, err := r.verifyAuth(ctx)
	if err != nil {
		return nil, err
	}

	task := &model.Task{
		ID:    taskID.Bytes,
		Owner: user.Address.Hex(),
	}

	taskRawByte, err := r.db.GetKey([]byte(task.ID))

	if err != nil {
		return nil, err
	}

	task.FromStorageData(taskRawByte)

	return task.ToProtoBuf()
}

func (r *RpcServer) SyncTasks(payload *avsproto.SyncTasksReq, srv avsproto.Aggregator_SyncTasksServer) error {
	log.Printf("sync task for operator : %s", payload.Address)

	for {
		resp := avsproto.SyncTasksResp{
			// TODO: Hook up to the new task channel to syncdicate in realtime
			// Currently this is setup just to generate the metrics so we can
			// prepare the dashboard
			// Our actually task will be more completed
			Id:       "1",
			TaskType: "CurrentBlockTime",
		}

		if err := srv.Send(&resp); err != nil {
			log.Printf("error when sending task to operator %s: %v", payload.Address, err)
			return err
		}
		time.Sleep(time.Duration(5) * time.Second)
	}

	return nil
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

	// TODO: Remove hard code
	ethclient, err := ethclient.Dial("https://eth-sepolia.api.onfinality.io/public")

	if err != nil {
		panic(err)
	}

	avsproto.RegisterAggregatorServer(s, &RpcServer{
		db:           agg.db,
		ethrpc:       ethclient,
		config:       agg.config,
		operatorPool: agg.operatorPool,
	})

	// Register reflection service on gRPC server.
	// This allow clien to discover url endpoint
	// https://github.com/grpc/grpc-go/blob/master/Documentation/server-reflection-tutorial.md
	reflection.Register(s)

	log.Printf("grpc server listening at %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
	return nil
}
