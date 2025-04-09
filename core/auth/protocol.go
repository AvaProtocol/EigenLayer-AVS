package auth

import (
	"fmt"

	jwt "github.com/golang-jwt/jwt/v5"
)

const (
	Issuer = "AvaProtocol"
	JwtAlg = "HS256"

	AdminRole    = ApiRole("admin")
	ReadonlyRole = ApiRole("readonly")
)

var (
	ErrorUnAuthorized = fmt.Errorf("Unauthorized error")

	ErrorInvalidToken = fmt.Errorf("Invalid Bearer Token")

	ErrorMalformedAuthHeader = fmt.Errorf("Malform auth header")
	ErrorExpiredSignature    = fmt.Errorf("Signature is expired")
)

type ApiRole string
type APIClaim struct {
	*jwt.RegisteredClaims
	Roles []ApiRole `json:"roles"`
}

func GetOperatorSigninMessage(operatorAddr string, epoch int64) []byte {
	return []byte(fmt.Sprintf("Operator:%sEpoch:%d", operatorAddr, epoch))
}
