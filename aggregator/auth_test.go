package aggregator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/AvaProtocol/ap-avs/core/auth"
	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/golang-jwt/jwt/v5"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"

	"github.com/AvaProtocol/ap-avs/core/chainio/signer"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
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
}
