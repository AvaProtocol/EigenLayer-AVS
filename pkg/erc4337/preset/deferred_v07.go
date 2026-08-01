package preset

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

// Deferred-action variants of the v0.7 signing helpers. The operation's nonce
// must carry ValidationOptionDeferredAction and the entity id of the
// validation the deferred call installs — the CONTROLLER's entity, not the
// owner's. The owner's part (encodedData + ownerSig) was produced at grant
// time; only the controller signs here.

// DeferredEstimationSignature builds the signature to estimate a deferred
// operation with. The deferred segments must be REAL: unlike ordinary
// signature validation, which fails gracefully so estimation can proceed, a
// bad deferred-action signature REVERTS (DeferredActionSignatureInvalid) —
// the owner's actual grant has to ride along even for estimation. Only the
// controller's part is a dummy.
func DeferredEstimationSignature(encodedData []byte, ownerSig []byte) ([]byte, error) {
	return userop.WrapSignatureMAv2Deferred(encodedData, ownerSig, dummySignatureV07())
}

// SignUserOpV07Deferred signs the operation with the controller key and
// installs the full deferred-action signature: the owner's grant segments
// followed by the controller's framed signature. Sign last — the controller
// signature covers every gas and paymaster field. The owner's signature does
// not, which is the property that lets it be collected before pricing exists.
func SignUserOpV07Deferred(
	op *userop.UserOperationV07,
	entryPoint common.Address,
	chainID *big.Int,
	controllerKey *ecdsa.PrivateKey,
	encodedData []byte,
	ownerSig []byte,
) error {
	if op == nil {
		return fmt.Errorf("nil user operation")
	}
	if controllerKey == nil {
		return fmt.Errorf("nil controller key")
	}
	hash, err := op.GetUserOpHash(entryPoint, chainID)
	if err != nil {
		return fmt.Errorf("computing userOpHash: %w", err)
	}
	sig, err := crypto.Sign(accounts.TextHash(hash.Bytes()), controllerKey)
	if err != nil {
		return fmt.Errorf("signing userOpHash %s: %w", hash.Hex(), err)
	}
	sig[64] += 27
	framed, err := userop.WrapSignatureMAv2(sig)
	if err != nil {
		return err
	}
	full, err := userop.WrapSignatureMAv2Deferred(encodedData, ownerSig, framed)
	if err != nil {
		return err
	}
	op.Signature = full
	return nil
}
