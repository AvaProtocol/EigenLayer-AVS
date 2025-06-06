package aggregator

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/auth"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/version"
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
	enforceAuth             = false
	TokenExpirationDuration = 48 * time.Hour
	authTemplate            = `Please sign the below text for ownership verification.

URI: https://app.avaprotocol.org
Chain ID: %d
Version: %s
Issued At: %s
Expire At: %s
Wallet: %s`
)

// GetKey exchanges an api key or signature submit by an EOA with an API key that can manage
// the EOA task
func (r *RpcServer) GetKey(ctx context.Context, payload *avsproto.GetKeyReq) (*avsproto.KeyResp, error) {
	message := payload.Message

	r.config.Logger.Info("process getkey with message",
		"message", message,
	)

	// Parse the message to extract necessary information
	// Please sign the below text for ownership verification.
	//
	// URI: https://app.avaprotocol.org
	// Chain ID: %d
	// Version: %s
	// Issued At: %s
	// Expire At: %s
	// Wallet: %s

	lines := strings.Split(message, "\n")
	if len(lines) < 8 {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid message format")
	}

	var chainIDLine, versionLine, issuedAtLine, expireAtLine, walletLine string

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimmedLine, "Chain ID:") {
			chainIDLine = trimmedLine
		} else if strings.HasPrefix(trimmedLine, "Version:") {
			versionLine = trimmedLine
		} else if strings.HasPrefix(trimmedLine, "Issued At:") {
			issuedAtLine = trimmedLine
		} else if strings.HasPrefix(trimmedLine, "Expire At:") {
			expireAtLine = trimmedLine
		} else if strings.HasPrefix(trimmedLine, "Wallet:") {
			walletLine = trimmedLine
		}
	}

	if chainIDLine == "" {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid message format: missing Chain ID")
	}
	chainIDStr := strings.TrimSpace(strings.TrimPrefix(chainIDLine, "Chain ID:"))
	chainID, success := new(big.Int).SetString(chainIDStr, 10)
	if !success || chainID == nil {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid Chain ID format")
	}

	// TODO: Remove this special handling for Holesky (17000) once testnet chain is changed.
	// This allows a requested chainId of 17000 to be valid against a SmartWallet chainId of 11155111.
	isHoleskyDevnetCase := chainIDStr == "17000"

	// Special handling for Ethereum Mainnet (1) to allow requests if server is on a different chain (e.g., Base).
	isEthereumMainnetCase := chainIDStr == "1"

	if r.chainID != nil && chainID.Cmp(r.chainID) != 0 && !isHoleskyDevnetCase && !isEthereumMainnetCase {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid chainId: requested chainId %s does not match SmartWallet chainId %d", chainIDStr, r.chainID.Int64())
	}

	if versionLine == "" {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid message format: missing Version")
	}

	if issuedAtLine == "" {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid message format: missing Issued At")
	}
	issuedAtStr := strings.TrimSpace(strings.TrimPrefix(issuedAtLine, "Issued At:"))
	_, parseErr := time.Parse("2006-01-02T15:04:05.000Z", issuedAtStr)
	if parseErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid Issued At format")
	}

	if expireAtLine == "" {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid message format: missing Expire At")
	}
	expireAtStr := strings.TrimSpace(strings.TrimPrefix(expireAtLine, "Expire At:"))
	expireAt, parseErr := time.Parse("2006-01-02T15:04:05.000Z", expireAtStr)
	if parseErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid Expire At format")
	}

	if walletLine == "" {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid message format: missing Wallet")
	}
	walletStr := strings.TrimSpace(strings.TrimPrefix(walletLine, "Wallet:"))
	if !common.IsHexAddress(walletStr) {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid wallet address format")
	}
	ownerAddress := common.HexToAddress(walletStr)

	if strings.Contains(payload.Signature, ".") {
		// API key directly
		authenticated, err := auth.VerifyJwtKeyForUser(r.config.JwtSecret, payload.Signature, ownerAddress)
		if err != nil || !authenticated {
			return nil, status.Errorf(codes.Unauthenticated, "%s: %s", auth.AuthenticationError, auth.InvalidAPIKey)
		}
	} else {
		// We need to have 3 things to verify the signature: the signature, the hash of the original data, and the public key of the signer (owner address).
		data := []byte(message)
		hash := accounts.TextHash(data)

		signature, err := hexutil.Decode(payload.Signature)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, auth.InvalidSignatureFormat)
		}
		if len(signature) < crypto.RecoveryIDOffset {
			return nil, status.Errorf(codes.InvalidArgument, auth.InvalidSignatureFormat)
		}
		// https://stackoverflow.com/questions/49085737/geth-ecrecover-invalid-signature-recovery-id
		if signature[crypto.RecoveryIDOffset] == 27 || signature[crypto.RecoveryIDOffset] == 28 {
			signature[crypto.RecoveryIDOffset] -= 27 // Transform yellow paper V from 27/28 to 0/1
		}

		sigPublicKey, err := crypto.SigToPub(hash, signature)
		if err != nil {
			return nil, status.Errorf(codes.Unauthenticated, auth.InvalidAuthenticationKey)
		}
		recoveredAddr := crypto.PubkeyToAddress(*sigPublicKey)
		if ownerAddress.String() != recoveredAddr.String() {
			return nil, status.Errorf(codes.Unauthenticated, auth.InvalidAuthenticationKey)
		}
	}

	if expireAt.Before(time.Now()) {
		return nil, status.Errorf(codes.Unauthenticated, auth.MalformedExpirationTime)
	}

	claims := &jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(expireAt),
		Issuer:    auth.Issuer,
		Subject:   ownerAddress.String(),
		Audience:  jwt.ClaimStrings{chainIDStr},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	ss, signErr := token.SignedString(r.config.JwtSecret)

	if signErr != nil {
		return nil, status.Errorf(codes.Internal, "Internal server error")
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

		expectedChainIdStr := fmt.Sprintf("%d", r.chainID) // e.g., "11155111"
		aud, err := token.Claims.GetAudience()

		// Log the details
		r.config.Logger.Info("Verifying JWT audience",
			"expectedServerChainIdStr", expectedChainIdStr,
			"audienceFromToken", aud,
			"errorGettingAudience", err,
		)

		if err != nil || len(aud) == 0 {
			return nil, fmt.Errorf("%s: error getting audience or audience is empty", auth.InvalidAuthenticationKey)
		}

		tokenAudienceChainIdStr := aud[0]

		// TODO: Remove this special handling for Holesky (17000) once testnet chain is changed.
		// This allows a token with audience "17000" to be valid if server expects "11155111".
		isHoleskyDevnetAudienceCase := tokenAudienceChainIdStr == "17000"

		// Special handling for Ethereum Mainnet (1) audience if server is on a different chain.
		isEthereumMainnetAudienceCase := tokenAudienceChainIdStr == "1"

		if tokenAudienceChainIdStr != expectedChainIdStr && !isHoleskyDevnetAudienceCase && !isEthereumMainnetAudienceCase {
			r.config.Logger.Error("JWT audience mismatch",
				"expectedAudience", expectedChainIdStr,
				"tokenAudience", tokenAudienceChainIdStr,
				"isHoleskySpecialCaseApplied", isHoleskyDevnetAudienceCase,
				"isEthereumMainnetSpecialCaseApplied", isEthereumMainnetAudienceCase,
			)
			return nil, fmt.Errorf("%s: invalid chainId in audience", auth.InvalidAuthenticationKey)
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

			// We don't care if its error out in caching, but log it for debugging
			if err := r.cache.Set(cachekey, user.SmartAccountAddress.Bytes()); err != nil {
				r.config.Logger.Debug("failed to cache smart wallet address", "error", err)
			}
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

func (r *RpcServer) GetSignatureFormat(ctx context.Context, req *avsproto.GetSignatureFormatReq) (*avsproto.GetSignatureFormatResp, error) {
	walletAddress := req.Wallet

	if walletAddress == "" {
		return nil, status.Errorf(codes.InvalidArgument, "Wallet address is required")
	}

	if !common.IsHexAddress(walletAddress) {
		return nil, status.Errorf(codes.InvalidArgument, "Invalid Ethereum wallet address format")
	}

	chainId := GetGlobalChainID()
	if chainId == nil {
		return nil, status.Errorf(codes.Internal, "Chain ID not available. Aggregator not fully initialized.")
	}

	issuedAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	expiredAt := time.Now().Add(TokenExpirationDuration).UTC().Format("2006-01-02T15:04:05.000Z")

	currentVersion := version.Get()

	formattedMessage := fmt.Sprintf(authTemplate,
		chainId.Int64(),
		currentVersion,
		issuedAt,
		expiredAt,
		walletAddress)

	return &avsproto.GetSignatureFormatResp{
		Message: formattedMessage,
	}, nil
}
