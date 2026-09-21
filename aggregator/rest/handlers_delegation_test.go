package rest

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

func TestSetCodeDigestMatchesSignSetCode(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	eoa := crypto.PubkeyToAddress(key.PublicKey)
	chainID := int64(11155111)
	nonce := uint64(7)
	digest := setCodeDigest(chainID, config.SMA7702Delegate(), nonce)

	signed, err := types.SignSetCode(key, types.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(big.NewInt(chainID)),
		Address: config.SMA7702Delegate(),
		Nonce:   nonce,
	})
	require.NoError(t, err)
	require.Equal(t, digest, signed.SigHash())
	got, err := signed.Authority()
	require.NoError(t, err)
	require.Equal(t, eoa, got)

	sig := make([]byte, 65)
	copy(sig[0:32], common.LeftPadBytes(signed.R.Bytes(), 32))
	copy(sig[32:64], common.LeftPadBytes(signed.S.Bytes(), 32))
	sig[64] = signed.V + 27
	parsed, err := parseSetCodeAuth(chainID, config.SMA7702Delegate(), nonce, "0x"+hex.EncodeToString(sig))
	require.NoError(t, err)
	auth, err := parsed.Authority()
	require.NoError(t, err)
	require.Equal(t, eoa, auth)
}
