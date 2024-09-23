package aggregator

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/AvaProtocol/ap-avs/core/auth"
	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/model"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc/metadata"
)

// GetKey exchanges an api key or signature submit by an EOA with an API key that can manage
// the EOA task
func (r *RpcServer) GetKey(ctx context.Context, payload *avsproto.GetKeyReq) (*avsproto.KeyResp, error) {
	submitAddress := common.HexToAddress(payload.Owner)

	if strings.Contains(payload.Signature, ".") {
		authenticated, err := auth.VerifyJwtKeyForUser(r.config.JwtSecret, payload.Signature, submitAddress)
		if err != nil {
			return nil, err
		}

		if !authenticated {
			return nil, auth.ErrorUnAuthorized
		}
	} else {
		// We need to have 3 things to verify the signature: the signature, the hash of the original data, and the public key of the signer. With this information we can determine if the private key holder of the public key pair did indeed sign the message
		// The message format we need to sign
		//
		text := fmt.Sprintf("key request for %s expired at %d", payload.Owner, payload.ExpiredAt)
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
			return nil, err
		}
		if submitAddress.String() != recoveredAddr.String() {
			return nil, fmt.Errorf("Invalid signature")
		}
	}

	claims := &jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Unix(payload.ExpiredAt, 0)),
		Issuer:    auth.Issuer,
		Subject:   payload.Owner,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	ss, err := token.SignedString(r.config.JwtSecret)

	if err != nil {
		return nil, err
	}

	return &avsproto.KeyResp{
		Key: ss,
	}, nil
}

// verifyAuth checks validity of the apikey submit by user related request
func (r *RpcServer) verifyAuth(ctx context.Context) (*model.User, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, fmt.Errorf("cannot read metadata from request")
	}
	authRawHeaders := md.Get("authkey")
	if len(authRawHeaders) < 1 {
		return nil, fmt.Errorf("missing auth header")
	}

	tokenString := authRawHeaders[0]

	// Parse takes the token string and a function for looking up the key. The
	// latter is especially
	// useful if you use multiple keys for your application.  The standard is to use
	// 'kid' in the
	// head of the token to identify which key to use, but the parsed token (head
	// and claims) is provided
	// to the callback, providing flexibility.
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("Unexpected signing method: %v", token.Header["alg"])
		}

		// hmacSampleSecret is a []byte containing your
		// secret, e.g. []byte("my_secret_key")
		return r.config.JwtSecret, nil
	})

	if err != nil {
		return nil, err
	}

	if token.Header["alg"] != auth.JwtAlg {
		return nil, fmt.Errorf("invalid signing algorithm")
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok {
		if claims["sub"] == "" {
			return nil, fmt.Errorf("Missing subject")
		}

		user := model.User{
			Address: common.HexToAddress(claims["sub"].(string)),
		}

		smartAccountAddress, err := aa.GetSenderAddress(r.ethrpc, user.Address, big.NewInt(0))
		if err != nil {
			return nil, fmt.Errorf("Rpc error")
		}
		user.SmartAccountAddress = smartAccountAddress

		return &user, nil
	}
	return nil, fmt.Errorf("Malform claims")
}

// verifyOperator checks validity of the signature submit by operator related request
func (r *RpcServer) verifyOperator(ctx context.Context, operatorAddr string) (bool, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false, fmt.Errorf("cannot read metadata from request")
	}

	authRawHeaders := md.Get("authorization")
	if len(authRawHeaders) < 1 {
		return false, fmt.Errorf("missing auth header")
	}

	return auth.VerifyOperator(authRawHeaders[0], operatorAddr)
}
