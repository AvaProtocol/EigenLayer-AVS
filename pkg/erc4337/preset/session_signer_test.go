package preset

import (
	"bytes"
	"context"
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

// testDeferredGrant is a grant's install as the owner signed it: the encoded
// deferred action and the owner's 65-byte signature.
func testDeferredGrant(t *testing.T, entityID uint32) (encodedData, ownerSig []byte) {
	t.Helper()
	locator, err := userop.PackValidationLocator(entityID, userop.ValidationOptionGlobal|userop.ValidationOptionDeferredAction)
	require.NoError(t, err)
	encodedData, err = userop.EncodeDeferredActionData(locator, 1_900_000_000, []byte{0xde, 0xad, 0xbe, 0xef, 0x01})
	require.NoError(t, err)
	ownerSig, err = crypto.Sign(crypto.Keccak256([]byte("grant")), testKey(t))
	require.NoError(t, err)
	ownerSig[64] += 27
	return encodedData, ownerSig
}

// The efficiency retry lowers verificationGasLimit and re-signs through the
// signer the send first used (#912). An operation that installs the grant
// keeps its deferred action and the owner's signature, so the account can
// still install the entity it validates against; a plain one is re-signed as
// the entity alone, as before.
func TestEfficiencyRetryReSignsThroughTheSessionSigner(t *testing.T) {
	const entityID = 7
	chainID := big.NewInt(11155111)
	encodedData, ownerSig := testDeferredGrant(t, entityID)
	controller := testKey(t)

	for _, tc := range []struct {
		name string
		auth *SessionAuthorization
		want func(op *userop.UserOperationV07) error
	}{
		{
			name: "an install operation keeps its deferred action",
			auth: &SessionAuthorization{EntityID: entityID, SignerKey: controller, DeferredData: encodedData, OwnerSignature: ownerSig},
			want: func(op *userop.UserOperationV07) error {
				return SignUserOpV07Deferred(op, EntryPointV07(), chainID, controller, encodedData, ownerSig)
			},
		},
		{
			name: "a plain operation is re-signed as the entity",
			auth: &SessionAuthorization{EntityID: entityID, SignerKey: controller},
			want: func(op *userop.UserOperationV07) error {
				return SignUserOpV07(op, EntryPointV07(), chainID, controller)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, sends, accepted := efficiencyRetryBundler(t)
			op := &userop.UserOperationV07{
				Sender: common.HexToAddress("0x2222222222222222222222222222222222222222"), Nonce: big.NewInt(1),
				CallData: []byte{0x01, 0x02}, CallGasLimit: big.NewInt(1_000), VerificationGasLimit: big.NewInt(10_000),
				PreVerificationGas: big.NewInt(1_000), MaxFeePerGas: big.NewInt(1_000_000_000), MaxPriorityFeePerGas: big.NewInt(1),
			}
			sign := tc.auth.userOpSigner(EntryPointV07(), chainID)
			require.NoError(t, sign(op))

			_, err := sendUserOpV07(context.Background(), client, op, EntryPointV07(), sign, nil)
			require.NoError(t, err)
			require.Equal(t, 2, *sends)
			require.Equal(t, "1333", op.VerificationGasLimit.String(), "the retry tightened a signed field")

			// Signing is deterministic, so the accepted signature must be the
			// matching signer's for the operation as accepted.
			acceptedSig := common.FromHex((*accepted)["signature"].(string))
			expected := *op
			require.NoError(t, tc.want(&expected))
			require.Equal(t, expected.Signature, acceptedSig)

			var dataLength [4]byte
			binary.BigEndian.PutUint32(dataLength[:], uint32(len(encodedData)))
			carriesInstall := bytes.HasPrefix(acceptedSig, append(dataLength[:], encodedData...)) && bytes.Contains(acceptedSig, ownerSig)
			require.Equal(t, tc.auth.Deferred(), carriesInstall)
		})
	}
}
