package auth

import (
	"fmt"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	jwt "github.com/golang-jwt/jwt/v5"
)

// GetUserFromKeyOrSignature attempts to verify that the payload is a valid JWT
// token for a particular EOA, or the payload is the right signature of an EOA
func GetUserFromKeyOrSignature(payload string) *common.Address {
	return nil
}

// VerifyJwtKeyForUser checks that the JWT key is either for this user wallet,
// or the JWT key for an API key that can manage the wallet
func VerifyJwtKeyForUser(secret []byte, key string, userWallet common.Address) (bool, error) {
	// Parse takes the token string and a function for looking up the key. The
	// latter is especially
	// useful if you use multiple keys for your application.  The standard is to use
	// 'kid' in the
	// head of the token to identify which key to use, but the parsed token (head
	// and claims) is provided
	// to the callback, providing flexibility.
	token, err := jwt.Parse(key, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("Unexpected signing method: %v", token.Header["alg"])
		}

		if token.Header["alg"] != JwtAlg {
			return nil, fmt.Errorf("invalid signing algorithm")
		}

		return secret, nil
	})

	if err != nil {
		return false, err
	}

	if token.Header["alg"] != JwtAlg {
		return false, fmt.Errorf("invalid signing algorithm")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if ok {
		sub := claims["sub"].(string)

		if sub == "" {
			return false, fmt.Errorf("Missing subject")
		}

		if sub == "apikey" {
			roles := []ApiRole{}
			for _, v := range claims["roles"].([]any) {
				roles = append(roles, ApiRole(v.(string)))
			}
			if claims["roles"] == nil || !slices.Contains(roles, "admin") {
				return false, fmt.Errorf("Invalid API Key")
			}

			return true, nil
		}

		claimAddress := common.HexToAddress(sub)
		if claimAddress.Cmp(userWallet) != 0 {
			return false, fmt.Errorf("Invalid Subject")
		}
	}

	return false, fmt.Errorf("Malform JWT Key Claim")
}
