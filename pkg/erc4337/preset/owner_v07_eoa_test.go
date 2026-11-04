package preset

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil/fixtures"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func newOwnerEOARPCFixture(t *testing.T) (*ownerRPCFixture, *config.SmartWalletConfig) {
	t.Helper()
	f, sw := newOwnerRPCFixture(t)
	f.sender = f.owner
	f.ownerCode = designation(config.SMA7702Delegate())
	f.delegateCode = fixtures.SMA7702Runtime()
	require.Len(t, f.delegateCode, 23741)
	require.Equal(t, config.SMA7702ImplHash(), crypto.Keccak256Hash(f.delegateCode))
	sw.EOA7702Execute = true
	sw.SMA7702Delegate, sw.SMA7702ImplHash = config.SMA7702Delegate(), config.SMA7702ImplHash()
	return f, sw
}

func TestOwnerEOAPrepareAndKeylessRelay(t *testing.T) {
	for _, chainID := range []int64{config.SMA7702ChainSepolia, config.SMA7702ChainBase} {
		t.Run(big.NewInt(chainID).String(), func(t *testing.T) {
			f, sw := newOwnerEOARPCFixture(t)
			key, err := crypto.GenerateKey()
			require.NoError(t, err)
			f.owner = crypto.PubkeyToAddress(key.PublicKey)
			f.sender = f.owner
			f.chainID, sw.ChainID = chainID, chainID
			p, err := PrepareOwnerUserOp(context.Background(), sw, f.owner, f.sender, nil, ownerTestCalls(), nil)
			require.NoError(t, err)
			require.Equal(t, f.owner, p.Operation.Sender)
			require.Nil(t, p.Operation.Factory)
			require.Empty(t, p.Operation.FactoryData)
			require.Empty(t, p.Operation.Signature)
			require.Equal(t, config.SMA7702Delegate(), p.Identity.Implementation)
			require.Equal(t, config.SMA7702ImplHash(), p.Identity.ImplementationHash)
			require.Equal(t, common.Hash{}, p.Identity.FactoryHash)
			require.NoError(t, ValidateOwnerUserOp(context.Background(), sw, f.owner, nil, p))
			hash, err := p.Operation.GetUserOpHash(EntryPointV07(), big.NewInt(chainID))
			require.NoError(t, err)
			raw, err := crypto.Sign(accounts.TextHash(hash.Bytes()), key)
			require.NoError(t, err)
			p.Operation.Signature, err = FrameOwnerSignature(hash, raw, f.owner)
			require.NoError(t, err)
			before, err := json.Marshal(p.Operation)
			require.NoError(t, err)
			f.expectedHash = hash.Hex()
			sent, err := SubmitOwnerUserOp(context.Background(), sw, p.Operation)
			require.NoError(t, err)
			require.Equal(t, hash, sent)
			var expected map[string]interface{}
			require.NoError(t, json.Unmarshal(before, &expected))
			require.Equal(t, expected, f.sent)
			require.Equal(t, 1, f.sendCount)
			require.Zero(t, f.factoryReads, "EOA execution must never derive or deploy a CREATE2 account")
		})
	}
}

func TestOwnerEOAPrepareRefusesUnsupportedAuthority(t *testing.T) {
	for _, scenario := range []string{"flag off", "missing pin", "different pin", "wrong chain", "wrong RPC chain", "undelegated", "different delegate", "different implementation code", "disabled fallback", "different fallback", "different EntryPoint", "CREATE2 salt", "wrong nonce key", "insufficient prefund"} {
		t.Run(scenario, func(t *testing.T) {
			f, sw := newOwnerEOARPCFixture(t)
			var salt *big.Int
			calls := ownerTestCalls()
			switch scenario {
			case "flag off":
				sw.EOA7702Execute = false
			case "missing pin":
				sw.SMA7702Delegate, sw.SMA7702ImplHash = common.Address{}, common.Hash{}
			case "different pin":
				sw.SMA7702ImplHash = crypto.Keccak256Hash([]byte("unverified implementation"))
			case "wrong chain":
				sw.ChainID, f.chainID = 84532, 84532
			case "wrong RPC chain":
				f.chainID = 8453
			case "undelegated":
				f.ownerCode = []byte{}
			case "different delegate":
				f.ownerCode = designation(common.HexToAddress("0x1234"))
			case "different implementation code":
				f.delegateCode = []byte{0x60, 0}
			case "disabled fallback":
				f.disabled = true
			case "different fallback":
				other := common.HexToAddress("0x1234")
				f.fallbackOwner = &other
			case "different EntryPoint":
				f.wrongEntry = true
			case "CREATE2 salt":
				salt = big.NewInt(0)
			case "wrong nonce key":
				f.nonce = big.NewInt(0)
			case "insufficient prefund":
				f.balance = "0x0"
			}
			p, err := PrepareOwnerUserOp(context.Background(), sw, f.owner, f.sender, salt, calls, nil)
			require.Error(t, err)
			require.Nil(t, p)
			require.Zero(t, f.sendCount)
			require.Zero(t, f.factoryReads)
		})
	}
}

func TestOwnerEOASubmitRefusesAuthorityChanges(t *testing.T) {
	for _, change := range []string{"flag off", "delegation removed", "delegate replaced", "implementation replaced", "fallback disabled", "fallback replaced", "EntryPoint", "nonce", "funding", "factory data"} {
		t.Run(change, func(t *testing.T) {
			f, sw := newOwnerEOARPCFixture(t)
			p, err := PrepareOwnerUserOp(context.Background(), sw, f.owner, f.sender, nil, ownerTestCalls(), nil)
			require.NoError(t, err)
			switch change {
			case "flag off":
				sw.EOA7702Execute = false
			case "delegation removed":
				f.ownerCode = []byte{}
			case "delegate replaced":
				f.ownerCode = designation(common.HexToAddress("0x1234"))
			case "implementation replaced":
				f.delegateCode = []byte{0x60, 0}
			case "fallback disabled":
				f.disabled = true
			case "fallback replaced":
				other := common.HexToAddress("0x1234")
				f.fallbackOwner = &other
			case "EntryPoint":
				f.wrongEntry = true
			case "nonce":
				f.nonce = new(big.Int).Add(f.nonce, big.NewInt(1))
			case "funding":
				f.balance = "0x0"
			case "factory data":
				factory := common.HexToAddress("0x1234")
				p.Operation.Factory = &factory
			}
			require.Error(t, ValidateOwnerUserOp(context.Background(), sw, f.owner, nil, p))
			require.Zero(t, f.sendCount)
		})
	}
}

func TestOwnerDelegationPreservesDerivedWalletExecution(t *testing.T) {
	f, sw := newOwnerRPCFixture(t)
	f.ownerCode, f.delegateCode = designation(config.SMA7702Delegate()), fixtures.SMA7702Runtime()
	sw.SMA7702Delegate, sw.SMA7702ImplHash = config.SMA7702Delegate(), config.SMA7702ImplHash()
	// The EOA execution flag gates spending from the EOA, not ECDSA signing
	// for its derived wallet. No session or EOA wallet record is needed here.
	require.False(t, sw.EOA7702Execute)
	p, err := PrepareOwnerUserOp(context.Background(), sw, f.owner, f.sender, big.NewInt(0), ownerTestCalls(), nil)
	require.NoError(t, err)
	require.NoError(t, ValidateOwnerUserOp(context.Background(), sw, f.owner, big.NewInt(0), p))
	f.ownerCode = []byte{}
	require.ErrorContains(t, ValidateOwnerUserOp(context.Background(), sw, f.owner, big.NewInt(0), p), "code or implementation changed")
}
