package aggregator

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"net"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	wrapperspb "google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/OAK-Foundation/oak-avs/core/chainio/aa"
	"github.com/OAK-Foundation/oak-avs/core/config"
	"github.com/OAK-Foundation/oak-avs/model"
	avsproto "github.com/OAK-Foundation/oak-avs/protobuf"
	"github.com/OAK-Foundation/oak-avs/storage"
)

// RpcServer is our grpc sever struct hold the entry point of request handler
type RpcServer struct {
	avsproto.UnimplementedAggregratorServer
	config *config.Config
	db     storage.Storage

	ethrpc *ethclient.Client
}

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
	updates[task.ID], err = task.ToJSON()
	updates[fmt.Sprintf("%s:%s", user.Address.String(), task.ID)] = []byte(model.TaskStatusActive)

	// TODO: add tak to user account so we can search by account too
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

	fmt.Println("List task for", user.Address.String())
	taskIDs, err := r.db.GetKeyHasPrefix([]byte(user.Address.String()))

	if err != nil {
		return nil, err
	}

	tasks := make([]*avsproto.ListTasksResp_TaskItemResp, len(taskIDs))
	for i, row := range taskIDs {
		tasks[i] = &avsproto.ListTasksResp_TaskItemResp{
			Id: string(row),
		}
	}

	return &avsproto.ListTasksResp{
		Tasks: tasks,
	}, nil
}

// startRpcServer initializes and establish a tcp socket on given address from
// config file
func (agg *Aggregator) startRpcServer(ctx context.Context, db storage.Storage, config *config.Config) error {
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

	avsproto.RegisterAggregratorServer(s, &RpcServer{
		db:     db,
		ethrpc: ethclient,
		config: config,
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
