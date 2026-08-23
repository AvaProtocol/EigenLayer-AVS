package auth

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

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

// VerifyOperator reports whether the auth header was signed by
// operatorAddr's own key. Callers that also accept an operator's
// declared alias key should use OperatorSignerFromAuthHeader and compare
// the recovered address themselves.
func VerifyOperator(authHeader string, operatorAddr string) (bool, error) {
	recovered, err := OperatorSignerFromAuthHeader(authHeader, operatorAddr)
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(recovered.Hex(), operatorAddr) {
		return false, ErrorUnAuthorized
	}
	return true, nil
}

// OperatorSignerFromAuthHeader validates an operator auth header's shape
// and freshness and returns the address that signed it.
//
// It deliberately does not decide whether that signer is acceptable. An
// operator may run with an alias key — a hot key declared against its
// registered address in the APConfig contract — so the recovered address
// is legitimately either the operator itself or its alias, and only a
// caller that can resolve that mapping can tell the difference.
//
// The signed message names operatorAddr, so a signature collected for
// one operator cannot be replayed to act as another.
func OperatorSignerFromAuthHeader(authHeader string, operatorAddr string) (common.Address, error) {
	bearerToken := strings.SplitN(authHeader, " ", 2)
	if len(bearerToken) < 2 || bearerToken[0] != "Bearer" {
		return common.Address{}, ErrorInvalidToken
	}

	tokens := strings.SplitN(bearerToken[1], ".", 2)
	if len(tokens) < 2 {
		return common.Address{}, ErrorMalformedAuthHeader
	}
	epoch, err := strconv.Atoi(tokens[0])
	if err != nil {
		return common.Address{}, ErrorMalformedAuthHeader
	}
	now := time.Now()
	// Lower bound: a minted signature is good for a few seconds, so a
	// header captured off the wire stops working almost immediately.
	if now.Add(-operatorSignatureTTL).Unix() > int64(epoch) {
		return common.Address{}, ErrorExpiredSignature
	}
	// Upper bound: without one, an epoch set far in the future yields a
	// credential that never expires, because the check above only bites
	// once the timestamp is in the past.
	if int64(epoch) > now.Add(operatorClockSkew).Unix() {
		return common.Address{}, ErrorExpiredSignature
	}

	recovered, err := signer.RecoverAddress(GetOperatorSigninMessage(operatorAddr, int64(epoch)), tokens[1])
	if err != nil {
		return common.Address{}, fmt.Errorf("unauthorized error: %w", err)
	}

	return recovered, nil
}
