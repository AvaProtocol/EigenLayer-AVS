package aggregator

import (
	"context"
	"fmt"
	"strings"

	"github.com/AvaProtocol/ap-avs/core/auth"
	"github.com/AvaProtocol/ap-avs/model"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// We had old operators pre 1.3 where auth isn't enforced. upon all operators updated to 1.3.0 we will toggle this server side
	enforceAuth  = false
	authTemplate = `Please sign the below text for ownership verification.

URI: https://app.avaprotocol.org
Chain ID: %d
Version: 1
Issued At: %s
Expire At: %s
Wallet: %s`
)

// GetKey exchanges an api key or signature submit by an EOA with an API key that can manage
// the EOA task
func (r *RpcServer) GetKey(ctx context.Context, payload *avsproto.GetKeyReq) (*avsproto.KeyResp, error) {
	submitAddress := common.HexToAddress(payload.Owner)

	r.config.Logger.Info("process getkey",
		"owner", payload.Owner,
		"expired", payload.ExpiredAt,
		"issued", payload.IssuedAt,
		"chainId", payload.ChainId,
	)

	if strings.Contains(payload.Signature, ".") {
		// API key directly
		authenticated, err := auth.VerifyJwtKeyForUser(r.config.JwtSecret, payload.Signature, submitAddress)
		if err != nil || !authenticated {
			return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, auth.InvalidAPIKey)
		}
	} else {
		// We need to have 3 things to verify the signature: the signature, the hash of the original data, and the public key of the signer. With this information we can determine if the private key holder of the public key pair did indeed sign the message
		text := fmt.Sprintf(authTemplate, payload.ChainId, payload.IssuedAt.AsTime().UTC().Format("2006-01-02T15:04:05.000Z"), payload.ExpiredAt.AsTime().UTC().Format("2006-01-02T15:04:05.000Z"), payload.Owner)
		data := []byte(text)
		hash := accounts.TextHash(data)

		signature, err := hexutil.Decode(payload.Signature)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, auth.InvalidSignatureFormat)
		}
		if len(signature) < crypto.RecoveryIDOffset || len(signature) < crypto.RecoveryIDOffset {
			return nil, status.Errorf(codes.InvalidArgument, auth.InvalidSignatureFormat)
		}
		// https://stackoverflow.com/questions/49085737/geth-ecrecover-invalid-signature-recovery-id
		if signature[crypto.RecoveryIDOffset] == 27 || signature[crypto.RecoveryIDOffset] == 28 {
			signature[crypto.RecoveryIDOffset] -= 27 // Transform yellow paper V from 27/28 to 0/1
		}

		sigPublicKey, err := crypto.SigToPub(hash, signature)
		recoveredAddr := crypto.PubkeyToAddress(*sigPublicKey)
		if err != nil {
			return nil, status.Errorf(codes.Unauthenticated, auth.InvalidAuthenticationKey)
		}
		if submitAddress.String() != recoveredAddr.String() {
			return nil, status.Errorf(codes.Unauthenticated, auth.InvalidAuthenticationKey)
		}
	}

	if err := payload.ExpiredAt.CheckValid(); err != nil {
		return nil, status.Errorf(codes.Unauthenticated, auth.MalformedExpirationTime)
	}

	claims := &jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(payload.ExpiredAt.AsTime()),
		Issuer:    auth.Issuer,
		Subject:   payload.Owner,
		Audience:  jwt.ClaimStrings{fmt.Sprintf("%d", payload.ChainId)},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	ss, err := token.SignedString(r.config.JwtSecret)

	if err != nil {
		return nil, status.Errorf(codes.Internal, InternalError)
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
			return nil, fmt.Errorf("%s", auth.InvalidAuthenticationKey)
		}

		// hmacSampleSecret is a []byte containing your
		// secret, e.g. []byte("my_secret_key")
		return r.config.JwtSecret, nil
	})

	if err != nil {
		return nil, fmt.Errorf("%s", auth.InvalidAuthenticationKey)
	}

	if token.Header["alg"] != auth.JwtAlg {
		return nil, fmt.Errorf("%s", auth.InvalidAuthenticationKey)
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok {
		if claims["sub"] == "" {
			return nil, fmt.Errorf("%s", auth.InvalidAuthenticationKey)
		}

		if aud, ok := claims["aud"].([]interface{}); !ok || len(aud) == 0 {
			return nil, fmt.Errorf("%s: missing chainId in audience", auth.InvalidAuthenticationKey)
		} else {
			chainIdStr := fmt.Sprintf("%d", r.chainID)
			found := false
			for _, a := range aud {
				if a.(string) == chainIdStr {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("%s: invalid chainId in audience", auth.InvalidAuthenticationKey)
			}
		}

		user := model.User{
			Address: common.HexToAddress(claims["sub"].(string)),
		}

		// caching to reduce hitting eth rpc node
		cachekey := "default-wallet" + user.Address.Hex()
		if value, err := r.cache.Get(cachekey); err == nil {
			defaultSmartWallet := common.BytesToAddress(value)
			user.SmartAccountAddress = &defaultSmartWallet
		} else {
			if err := user.LoadDefaultSmartWallet(r.smartWalletRpc); err != nil {
				return nil, fmt.Errorf("Rpc error")
			}

			// We don't care if its error out in caching
			r.cache.Set(cachekey, user.SmartAccountAddress.Bytes())
		}

		return &user, nil
	}
	return nil, fmt.Errorf("%s", auth.InvalidAuthenticationKey)
}

// verifyOperator checks validity of the signature submit by operator related request
func (r *RpcServer) verifyOperator(ctx context.Context, operatorAddr string) (bool, error) {
	// TODO: Temporary not enforce auth
	if !enforceAuth {
		return true, nil
	}

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
