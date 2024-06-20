package aggregator

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"net"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/golang-jwt/jwt/v5"
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

func (r *RpcServer) Nonce(ctx context.Context, payload *avsproto.NonceRequest) (*avsproto.NonceResp, error) {

	ownerAddress := common.HexToAddress(payload.Owner)

	nonce, err := aa.GetNonce(r.ethrpc, ownerAddress, big.NewInt(0))
	if err != nil {
		return nil, err
	}

	return &avsproto.NonceResp{
		Nonce: nonce.String(),
	}, nil
}

func (r *RpcServer) CancelTask(ctx context.Context, taskID *avsproto.UUID) (*wrapperspb.BoolValue, error) {
	return nil, nil
}

func (r *RpcServer) GetKey(ctx context.Context, payload *avsproto.GetKeyReq) (*avsproto.KeyResp, error) {
	// We need to have 3 things to verify the signature: the signature, the hash of the original data, and the public key of the signer. With this information we can determine if the private key holder of the public key pair did indeed sign the message
	// The message format we need to sign
	text := fmt.Sprintf("key request for: %s expired_at: %d", payload.Owner, payload.ExpiredAt)
	fmt.Println(text)
	data := []byte(text)
	hash := accounts.TextHash(data)

	signature, err := hexutil.Decode(payload.Signature)
	if err != nil {
		return nil, err
	}
	// https://stackoverflow.com/questions/49085737/geth-ecrecover-invalid-signature-recovery-id
	if signature[crypto.RecoveryIDOffset] == 27 || signature[crypto.RecoveryIDOffset] == 28 {
		signature[crypto.RecoveryIDOffset] -= 27 // Transform yellow paper V from 27/28 to 0/1
	}

	sigPublicKey, err := crypto.SigToPub(hash, signature)
	recoveredAddr := crypto.PubkeyToAddress(*sigPublicKey)
	if err != nil {
		fmt.Println(err)
		return nil, err
	}
	submitAddress := common.HexToAddress(payload.Owner)
	if submitAddress.String() != recoveredAddr.String() {
		return nil, fmt.Errorf("Invalid signature")
	}

	claims := &jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		Issuer:    "AvaProtocol",
		Subject:   payload.Owner,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	ss, err := token.SignedString(r.config.JwtSecret)

	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	return &avsproto.KeyResp{
		Key: ss,
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
