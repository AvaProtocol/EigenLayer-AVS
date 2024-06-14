package aggregator

import (
	"context"
	"log"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	wrapperspb "google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/OAK-Foundation/oak-avs/model"
	avsproto "github.com/OAK-Foundation/oak-avs/protobuf"
	"github.com/OAK-Foundation/oak-avs/storage"
)

// RpcServer is our grpc sever struct hold the entry point of request handler
type RpcServer struct {
	avsproto.UnimplementedAggregratorServer
	db storage.Storage
}

func (r *RpcServer) CreateTask(ctx context.Context, taskPayload *avsproto.CreateTaskReq) (*avsproto.CreateTaskResp, error) {

	// Get a task id
	// TODO: move to model

	task, err := model.NewTaskFromProtobuf(taskPayload)
	if err != nil {
		return nil, err
	}

	updates := map[string][]byte{}
	updates[task.ID], err = task.ToJSON()

	// TODO: add tak to user account so we can search by account too

	r.db.BatchWrite(updates)

	return &avsproto.CreateTaskResp{
		Id: task.ID,
	}, nil
}

func (r *RpcServer) CancelTask(ctx context.Context, taskID *avsproto.UUID) (*wrapperspb.BoolValue, error) {
	return nil, nil
}

// startRpcServer initializes and establish a tcp socket on given address from
// config file
func (agg *Aggregator) startRpcServer(ctx context.Context, db storage.Storage) error {
	// https://github.com/grpc/grpc-go/blob/master/examples/helloworld/greeter_server/main.go#L50
	lis, err := net.Listen("tcp", agg.config.RpcBindAddress)
	if err != nil {
		log.Fatalf("Failed to listen to %v", err)
		return err
	}

	s := grpc.NewServer()
	avsproto.RegisterAggregratorServer(s, &RpcServer{
		db: db,
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
