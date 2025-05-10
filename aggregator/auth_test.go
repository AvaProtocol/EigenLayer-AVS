package aggregator

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/auth"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/signer"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

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
	
	aud, _ := token.Claims.GetAudience()
	if len(aud) != 1 || aud[0] != "11155111" {
		t.Errorf("invalid audience. expected [11155111] but got %v", aud)
	}
}

func TestCrossChainJWTValidation(t *testing.T) {
	logger, _ := sdklogging.NewZapLogger("development")

	// Create RpcServer with chainID set to Sepolia (11155111)
	r := RpcServer{
		config: &config.Config{
			JwtSecret: []byte("test123"),
			Logger:    logger,
		},
		chainID: big.NewInt(11155111), // Sepolia chainID
	}

	owner := "0x578B110b0a7c06e66b7B1a33C39635304aaF733c"
	differentChainID := int64(5) // Goerli chainID
	issuedTs, _ := time.Parse(time.RFC3339, "2025-01-01T00:00:00Z")
	expiredTs, _ := time.Parse(time.RFC3339, "2025-01-02T00:00:00Z")
	issuedAt := timestamppb.New(issuedTs)
	expiredAt := timestamppb.New(expiredTs)

	text := fmt.Sprintf(authTemplate, differentChainID, issuedTs.UTC().Format("2006-01-02T15:04:05.000Z"), expiredTs.UTC().Format("2006-01-02T15:04:05.000Z"), owner)
	privateKey, _ := crypto.HexToECDSA("e0502ddd5a0d05ec7b5c22614a01c8ce783810edaa98e44cc82f5fa5a819aaa9")
	signature, _ := signer.SignMessage(privateKey, []byte(text))

	payload := &avsproto.GetKeyReq{
		ChainId:   differentChainID,
		IssuedAt:  issuedAt,
		ExpiredAt: expiredAt,
		Owner:     owner,
		Signature: hexutil.Encode(signature),
	}

	_, err := r.GetKey(context.Background(), payload)

	if err == nil {
		t.Errorf("expected GetKey to fail for mismatched chainId, but it succeeded")
	}

	statusErr, ok := status.FromError(err)
	if !ok {
		t.Errorf("expected a gRPC status error, got: %v", err)
	} else if statusErr.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument error code, got: %v", statusErr.Code())
	} else if expected := fmt.Sprintf("Invalid chainId: requested chainId %d does not match SmartWallet chainId %d", differentChainID, r.chainID.Int64()); statusErr.Message() != expected {
		t.Errorf("expected error message '%s', got: '%s'", expected, statusErr.Message())
	}
}
