package auth

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/signer"
)

const (
	// operatorSignatureTTL is how long a minted operator signature stays
	// valid. Deliberately short: the operator transport is plaintext, so
	// a captured Authorization header is replayable for exactly this long.
	operatorSignatureTTL = 10 * time.Second

	// operatorClockSkew is how far ahead of us an operator's clock may
	// run before its signatures are refused.
	operatorClockSkew = 60 * time.Second
)

// VerifyOperator checks and confirm that the auth header is indeed signed by
// the operatorAddr
func VerifyOperator(authHeader string, operatorAddr string) (bool, error) {
	bearerToken := strings.SplitN(authHeader, " ", 2)
	if len(bearerToken) < 2 || bearerToken[0] != "Bearer" {
		return false, ErrorInvalidToken
	}

	tokens := strings.SplitN(bearerToken[1], ".", 2)
	if len(tokens) < 2 {
		return false, ErrorMalformedAuthHeader
	}
	epoch, err := strconv.Atoi(tokens[0])
	if err != nil {
		return false, ErrorMalformedAuthHeader
	}
	now := time.Now()
	// Lower bound: a minted signature is good for a few seconds, so a
	// header captured off the wire stops working almost immediately.
	if now.Add(-operatorSignatureTTL).Unix() > int64(epoch) {
		return false, ErrorExpiredSignature
	}
	// Upper bound: without one, an epoch set far in the future yields a
	// credential that never expires, because the check above only bites
	// once the timestamp is in the past.
	if int64(epoch) > now.Add(operatorClockSkew).Unix() {
		return false, ErrorExpiredSignature
	}

	result, err := signer.Verify(GetOperatorSigninMessage(operatorAddr, int64(epoch)), tokens[1], operatorAddr)
	if err == nil {
		return result, nil
	}

	return result, fmt.Errorf("unauthorized error: %w", err)
}
