package aggregator

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/auth"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/signer"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/golang-jwt/jwt/v5"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

type MockEthClient struct {
	chainID *big.Int
}

func (m *MockEthClient) ChainID(ctx context.Context) (*big.Int, error) {
	return m.chainID, nil
}

func (m *MockEthClient) Close() {}
func (m *MockEthClient) BlockByHash(ctx context.Context, hash common.Hash) (*types.Block, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) BlockByNumber(ctx context.Context, number *big.Int) (*types.Block, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) TransactionCount(ctx context.Context, blockHash common.Hash) (uint, error) {
	return 0, fmt.Errorf("not implemented")
}
func (m *MockEthClient) TransactionInBlock(ctx context.Context, blockHash common.Hash, index uint) (*types.Transaction, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) TransactionByHash(ctx context.Context, txHash common.Hash) (tx *types.Transaction, isPending bool, err error) {
	return nil, false, fmt.Errorf("not implemented")
}
func (m *MockEthClient) TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) SyncProgress(ctx context.Context) (*ethereum.SyncProgress, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) SubscribeNewHead(ctx context.Context, ch chan<- *types.Header) (ethereum.Subscription, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) NetworkID(ctx context.Context) (*big.Int, error) {
	return big.NewInt(1), nil
}
func (m *MockEthClient) BalanceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (*big.Int, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) StorageAt(ctx context.Context, account common.Address, key common.Hash, blockNumber *big.Int) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) CodeAt(ctx context.Context, account common.Address, blockNumber *big.Int) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) NonceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (uint64, error) {
	return 0, fmt.Errorf("not implemented")
}
func (m *MockEthClient) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) SubscribeFilterLogs(ctx context.Context, q ethereum.FilterQuery, ch chan<- types.Log) (ethereum.Subscription, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) PendingBalanceAt(ctx context.Context, account common.Address) (*big.Int, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) PendingStorageAt(ctx context.Context, account common.Address, key common.Hash) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) PendingCodeAt(ctx context.Context, account common.Address) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) PendingNonceAt(ctx context.Context, account common.Address) (uint64, error) {
	return 0, fmt.Errorf("not implemented")
}
func (m *MockEthClient) PendingTransactionCount(ctx context.Context) (uint, error) {
	return 0, fmt.Errorf("not implemented")
}
func (m *MockEthClient) CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) PendingCallContract(ctx context.Context, msg ethereum.CallMsg) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) SuggestGasTipCap(ctx context.Context) (*big.Int, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *MockEthClient) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	return 0, fmt.Errorf("not implemented")
}
func (m *MockEthClient) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	return fmt.Errorf("not implemented")
}

func TestGetKeyWithSignature(t *testing.T) {
	logger, _ := sdklogging.NewZapLogger("development")

	r := RpcServer{
		config: &config.Config{
			JwtSecret: []byte("test123"),
			Logger:    logger,
		},
	}

	owner := "0x578B110b0a7c06e66b7B1a33C39635304aaF733c"
	chainID := int64(11155111)
	issuedTs, _ := time.Parse(time.RFC3339, "2025-01-01T00:00:00Z")
	expiredTs, _ := time.Parse(time.RFC3339, "2025-01-02T00:00:00Z")
	issuedAt := timestamppb.New(issuedTs)
	expiredAt := timestamppb.New(expiredTs)

	text := fmt.Sprintf(authTemplate, chainID, issuedTs.UTC().Format("2006-01-02T15:04:05.000Z"), expiredTs.UTC().Format("2006-01-02T15:04:05.000Z"), owner)
	// dummy key to test auth
	privateKey, _ := crypto.HexToECDSA("e0502ddd5a0d05ec7b5c22614a01c8ce783810edaa98e44cc82f5fa5a819aaa9")

	signature, _ := signer.SignMessage(privateKey, []byte(text))

	payload := &avsproto.GetKeyReq{
		ChainId:   chainID,
		IssuedAt:  issuedAt,
		ExpiredAt: expiredAt,
		Owner:     owner,
		Signature: hexutil.Encode(signature),
	}

	// Run the test
	resp, err := r.GetKey(context.Background(), payload)

	if err != nil {
		t.Errorf("expect GetKey succesfully but got error: %s", err)
	}
	if resp.Key == "" {
		t.Errorf("expect jwt key but got no")
	}

	// Now let verify the key is valid
	token, err := jwt.Parse(resp.Key, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("%s", auth.InvalidAuthenticationKey)
		}

		// hmacSampleSecret is a []byte containing your
		// secret, e.g. []byte("my_secret_key")
		return r.config.JwtSecret, nil
	})

	sub, _ := token.Claims.GetSubject()
	if sub != "0x578B110b0a7c06e66b7B1a33C39635304aaF733c" {
		t.Errorf("invalid subject. expected 0x578B110b0a7c06e66b7B1a33C39635304aaF733c but got %s", sub)
	}
}

func TestGetSignatureFormat(t *testing.T) {
	logger, _ := sdklogging.NewZapLogger("development")

	mockEthClient := &MockEthClient{
		chainID: big.NewInt(1), // Ethereum mainnet
	}

	r := RpcServer{
		config: &config.Config{
			JwtSecret: []byte("test123"),
			Logger:    logger,
		},
		ethrpc: mockEthClient,
	}

	walletAddress := "0x1234567890123456789012345678901234567890"
	req := &avsproto.GetSignatureFormatReq{
		Wallet: walletAddress,
	}

	resp, err := r.GetSignatureFormat(context.Background(), req)

	if err != nil {
		t.Errorf("expected GetSignatureFormat to succeed but got error: %s", err)
	}

	if !strings.Contains(resp.Format, walletAddress) {
		t.Errorf("expected format to contain wallet address %s but got %s", walletAddress, resp.Format)
	}

	if !strings.Contains(resp.Format, "Chain ID: 1") {
		t.Errorf("expected format to contain Chain ID: 1 but got %s", resp.Format)
	}
}
