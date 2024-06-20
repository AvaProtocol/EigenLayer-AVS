package aggregator

import (
	"context"
	"fmt"
	"math/big"

	"github.com/OAK-Foundation/oak-avs/core/chainio/aa"
	"github.com/OAK-Foundation/oak-avs/model"
	"github.com/ethereum/go-ethereum/common"
	"github.com/golang-jwt/jwt"
	"google.golang.org/grpc/metadata"
)

func (r *RpcServer) verifyAuth(ctx context.Context) (*model.User, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, fmt.Errorf("cannot read metadata from request")
	}
	authRawHeaders := md.Get("authkey")
	if len(authRawHeaders) < 1 {
		return nil, fmt.Errorf("missing auth header")
	}

	tokenString := authRawHeaders[1]

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

		fmt.Println("validate alg", token.Header["alg"])

		// hmacSampleSecret is a []byte containing your
		// secret, e.g. []byte("my_secret_key")
		return r.config.JwtSecret, nil
	})

	if err != nil {
		return nil, fmt.Errorf("token cannot be parsed")
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok {
		if claims["subject"] == "" {
			return nil, fmt.Errorf("Missing subject")
		}

		user := model.User{
			Address: common.HexToAddress(claims["subject"].(string)),
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
