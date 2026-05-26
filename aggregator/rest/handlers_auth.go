package rest

import (
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/auth"
)

// Token lifetime mirrors the legacy GetKey gRPC flow — 48 hours, long
// enough for SDK callers that mint a token on app launch and short
// enough that a leaked token expires before it can be silently reused
// for weeks.
const authTokenExpirationDuration = 48 * time.Hour

// authMessageRequired is the minimum number of lines we need to parse a
// signed auth message. The canonical template has eight lines; anything
// shorter is malformed and rejected up front.
const authMessageRequired = 8

// AuthExchange — POST /api/v1/auth:exchange
//
// Verify an EIP-191 personal_sign signature and issue a JWT bound to the
// signer's EOA. The signed message must use the canonical template with
// the EigenLayer registration chain id (chain-agnostic auth). SDKs
// construct the message locally; there is no GetSignatureFormat endpoint.
func (s *Server) AuthExchange(ctx echo.Context) error {
	var body generated.AuthExchangeRequest
	if err := ctx.Bind(&body); err != nil {
		return &restmw.HTTPError{
			Status: http.StatusBadRequest,
			Code:   "AUTH_BAD_REQUEST",
			Title:  "Invalid request",
			Detail: "Body must be JSON with ownerAddress, signature, and message fields.",
		}
	}

	owner := strings.TrimSpace(string(body.OwnerAddress))
	signature := strings.TrimSpace(string(body.Signature))
	message := body.Message

	if owner == "" || signature == "" || message == "" {
		return &restmw.HTTPError{
			Status: http.StatusBadRequest,
			Code:   "AUTH_MISSING_FIELDS",
			Title:  "Missing required fields",
			Detail: "ownerAddress, signature, and message are required.",
		}
	}

	if !common.IsHexAddress(owner) {
		return &restmw.HTTPError{
			Status: http.StatusBadRequest,
			Code:   "AUTH_INVALID_ADDRESS",
			Title:  "Invalid owner address",
			Detail: "ownerAddress must be a valid 0x-prefixed hex address.",
		}
	}
	ownerAddress := common.HexToAddress(owner)
	if ownerAddress == (common.Address{}) {
		return &restmw.HTTPError{
			Status: http.StatusBadRequest,
			Code:   "AUTH_ZERO_ADDRESS",
			Title:  "Owner address cannot be the zero address",
			Detail: "0x0000…0000 is never a real EOA. Refusing to mint a token bound to it.",
		}
	}

	chainID, expireAt, err := parseAuthMessage(message)
	if err != nil {
		return &restmw.HTTPError{
			Status: http.StatusBadRequest,
			Code:   "AUTH_BAD_MESSAGE",
			Title:  "Malformed auth message",
			Detail: err.Error(),
		}
	}
	if expireAt.Before(time.Now()) {
		return &restmw.HTTPError{
			Status: http.StatusBadRequest,
			Code:   "AUTH_EXPIRED_MESSAGE",
			Title:  "Auth message expired",
			Detail: "The signed message has already passed its Expire At timestamp; sign a fresh one.",
		}
	}

	if err := verifyPersonalSign(message, signature, ownerAddress); err != nil {
		return &restmw.HTTPError{
			Status: http.StatusUnauthorized,
			Code:   "AUTH_INVALID_SIGNATURE",
			Title:  "Invalid signature",
			Detail: err.Error(),
		}
	}

	if s.config == nil || len(s.config.JwtSecret) == 0 {
		return &restmw.HTTPError{
			Status: http.StatusInternalServerError,
			Code:   "AUTH_NOT_CONFIGURED",
			Title:  "Auth not configured",
			Detail: "JWT signing key is missing on this aggregator instance.",
		}
	}

	claims := &jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(expireAt),
		Issuer:    auth.Issuer,
		Subject:   ownerAddress.Hex(),
		Audience:  jwt.ClaimStrings{chainID.String()},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.config.JwtSecret)
	if err != nil {
		return &restmw.HTTPError{
			Status: http.StatusInternalServerError,
			Code:   "AUTH_SIGN_FAILED",
			Title:  "Failed to sign token",
			Detail: "Aggregator could not sign the JWT; check server logs.",
		}
	}

	subject := generated.EthereumAddress(ownerAddress.Hex())
	resp := generated.AuthExchangeResponse{
		Token:     signed,
		ExpiresAt: generated.Timestamp(expireAt.UTC()),
		Subject:   &subject,
	}
	return ctx.JSON(http.StatusOK, resp)
}

// parseAuthMessage extracts the chain ID and expiry from the canonical
// EIP-191 auth template. It does NOT touch crypto — see verifyPersonalSign
// for the signature check. Returning structured fields means the handler
// can produce a clean 4xx with a specific Detail rather than a generic
// "malformed message".
func parseAuthMessage(message string) (chainID *big.Int, expireAt time.Time, err error) {
	lines := strings.Split(message, "\n")
	if len(lines) < authMessageRequired {
		return nil, time.Time{}, fmt.Errorf("message must have at least %d lines following the canonical template", authMessageRequired)
	}

	var chainIDLine, expireAtLine, walletLine string
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "Chain ID:"):
			chainIDLine = line
		case strings.HasPrefix(line, "Expire At:"):
			expireAtLine = line
		case strings.HasPrefix(line, "Wallet:"):
			walletLine = line
		}
	}
	if chainIDLine == "" || expireAtLine == "" || walletLine == "" {
		return nil, time.Time{}, fmt.Errorf("missing one of Chain ID / Expire At / Wallet headers")
	}

	chainIDStr := strings.TrimSpace(strings.TrimPrefix(chainIDLine, "Chain ID:"))
	cid, ok := new(big.Int).SetString(chainIDStr, 10)
	if !ok {
		return nil, time.Time{}, fmt.Errorf("Chain ID must be a base-10 integer")
	}

	expireAtStr := strings.TrimSpace(strings.TrimPrefix(expireAtLine, "Expire At:"))
	exp, perr := time.Parse("2006-01-02T15:04:05.000Z", expireAtStr)
	if perr != nil {
		return nil, time.Time{}, fmt.Errorf("Expire At must be RFC 3339 with milliseconds (e.g. 2026-05-25T12:00:00.000Z)")
	}

	return cid, exp, nil
}

// verifyPersonalSign reproduces the standard `eth_sign` / `personal_sign`
// recovery used by the legacy gRPC GetKey flow: prepend the EIP-191
// prefix, hash, then recover the public key and check the resulting
// address matches `expected`. Returns nil on success.
func verifyPersonalSign(message, signatureHex string, expected common.Address) error {
	sig, err := hexutil.Decode(signatureHex)
	if err != nil {
		return fmt.Errorf("signature must be 0x-prefixed hex")
	}
	if len(sig) < crypto.RecoveryIDOffset+1 {
		return fmt.Errorf("signature is too short to recover a public key")
	}
	// Geth recovers with v ∈ {0,1}; MetaMask emits {27,28}. Normalize.
	if sig[crypto.RecoveryIDOffset] == 27 || sig[crypto.RecoveryIDOffset] == 28 {
		sig[crypto.RecoveryIDOffset] -= 27
	}

	hash := accounts.TextHash([]byte(message))
	pub, err := crypto.SigToPub(hash, sig)
	if err != nil {
		return fmt.Errorf("could not recover signer from signature")
	}
	recovered := crypto.PubkeyToAddress(*pub)
	if recovered != expected {
		return fmt.Errorf("signature does not match ownerAddress")
	}
	return nil
}
