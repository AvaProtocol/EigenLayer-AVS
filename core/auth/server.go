package auth

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/signer"
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
	epoch, _ := strconv.Atoi(tokens[0])
	if time.Now().Add(-10*time.Second).Unix() > int64(epoch) {
		return false, ErrorExpiredSignature
	}

	result, err := signer.Verify(GetOperatorSigninMessage(operatorAddr, int64(epoch)), tokens[1], operatorAddr)
	if err == nil {
		return result, nil
	}

	return result, fmt.Errorf("unauthorized error: %w", err)

}
