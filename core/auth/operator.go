package auth

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/signer"
	"github.com/ethereum/go-ethereum/common"
)

type ClientAuth struct {
	EcdsaPrivateKey *ecdsa.PrivateKey
	SignerAddr      common.Address
}

// Return value is mapped to request headers.
func (a ClientAuth) GetRequestMetadata(ctx context.Context, in ...string) (map[string]string, error) {
	epoch := time.Now().Unix()
	token, err := signer.SignMessageAsHex(
		a.EcdsaPrivateKey,
		GetOperatorSigninMessage(a.SignerAddr.String(), epoch),
	)

	if err != nil {
		panic(err)
	}

	return map[string]string{
		"authorization": fmt.Sprintf("Bearer %d.%s", epoch, token),
	}, nil
}

func (ClientAuth) RequireTransportSecurity() bool {
	// TODO: Toggle true on prod
	return false
}
