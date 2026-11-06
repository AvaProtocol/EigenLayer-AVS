package taskengine

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil/fixtures"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestSmartWalletEOA7702Shape(t *testing.T) {
	owner := common.HexToAddress("0x00000000000000000000000000000000000000e0")
	del := config.SMA7702Delegate()
	o, a, d := owner, owner, del
	ok := &model.SmartWallet{Kind: model.WalletKindEOA7702, Owner: &o, Address: &a, Delegate: &d}
	require.NoError(t, ok.ValidateEOA7702Shape())
	require.True(t, ok.IsEOA7702())

	other := common.HexToAddress("0x00000000000000000000000000000000000000e1")
	bad := *ok
	bad.Address = &other
	require.Error(t, bad.ValidateEOA7702Shape())

	fac := common.HexToAddress("0x00000000000017c61b5bEe81050EC8eFc9c6fecd")
	withFactory := *ok
	withFactory.Factory = &fac
	require.Error(t, withFactory.ValidateEOA7702Shape())

	withSalt := *ok
	withSalt.Salt = big.NewInt(1)
	require.Error(t, withSalt.ValidateEOA7702Shape())
}

func TestStoreAndListEOA7702Wallet(t *testing.T) {
	engine, db, _, owner, _ := newPolicyTestEngine(t)
	require.NoError(t, StoreEOA7702Wallet(db, testPolicyChain, owner, config.SMA7702Delegate()))

	got, err := GetWallet(db, testPolicyChain, owner, owner.Hex())
	require.NoError(t, err)
	require.True(t, got.IsEOA7702())
	require.Equal(t, owner, *got.Address)
	require.Equal(t, owner, *got.Owner)
	require.Nil(t, got.Salt)
	require.Equal(t, config.SMA7702Delegate(), *got.Delegate)

	user := &model.User{Address: owner, ChainID: testPolicyChain}
	list, err := engine.ListWallets(user, &avsproto.ListWalletReq{})
	require.NoError(t, err)
	var sawEOA, sawDerived bool
	for _, item := range list.GetItems() {
		if item.GetKind() == model.WalletKindEOA7702 {
			sawEOA = true
			require.True(t, strings.EqualFold(item.GetAddress(), owner.Hex()))
			require.Empty(t, item.GetSalt())
			require.Empty(t, item.GetFactory())
			require.True(t, strings.EqualFold(item.GetDelegate(), config.SMA7702Delegate().Hex()))
		} else {
			sawDerived = true
		}
	}
	require.True(t, sawEOA, "list must include the eoa_7702 runner")
	require.True(t, sawDerived, "derived CREATE2 runner still listed")
}

func TestStoreEOA7702WalletPreservesHidden(t *testing.T) {
	engine, db, _, owner, _ := newPolicyTestEngine(t)
	require.NoError(t, StoreEOA7702Wallet(db, testPolicyChain, owner, config.SMA7702Delegate()))
	_, err := engine.SetStoredWalletHidden(testPolicyChain, owner, owner.Hex(), true)
	require.NoError(t, err)
	require.NoError(t, engine.UpsertEOA7702Wallet(testPolicyChain, owner))
	got, err := GetWallet(db, testPolicyChain, owner, owner.Hex())
	require.NoError(t, err)
	require.True(t, got.IsHidden, "GET/upsert must not un-hide an eoa_7702 row")
}

func TestStoreEOA7702WalletRejectsNonCanonicalDelegate(t *testing.T) {
	_, db, _, owner, _ := newPolicyTestEngine(t)
	err := StoreEOA7702Wallet(db, testPolicyChain, owner, common.HexToAddress("0x000000000000000000000000000000000000dEaD"))
	require.Error(t, err)
}

func TestLookupOwnedWalletRecordSkipsCrossChainEOA7702(t *testing.T) {
	engine, db, _, owner, derived := newPolicyTestEngine(t)
	user := &model.User{Address: owner, ChainID: testPolicyChain}
	otherChain := int64(8453)
	require.NoError(t, StoreEOA7702Wallet(db, otherChain, owner, config.SMA7702Delegate()))

	rec, err := engine.lookupOwnedWalletRecord(user, testPolicyChain, owner)
	require.NoError(t, err)
	require.Nil(t, rec, "a Base eoa_7702 row must not satisfy a Sepolia grant")

	rec, err = engine.lookupOwnedWalletRecord(user, otherChain, owner)
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.True(t, rec.IsEOA7702())

	// CREATE2 fallback across chains is unchanged.
	rec, err = engine.lookupOwnedWalletRecord(user, otherChain, derived)
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.False(t, rec.IsEOA7702())
}

func TestRequireMAv2SessionWalletEOA7702(t *testing.T) {
	engine, db, _, owner, derived := newPolicyTestEngine(t)
	user := &model.User{Address: owner, ChainID: testPolicyChain}
	require.NoError(t, StoreEOA7702Wallet(db, testPolicyChain, owner, config.SMA7702Delegate()))

	// No pin in this fixture: a gateway verification failure, not owner advice.
	err := engine.requireMAv2SessionWallet(user, testPolicyChain, owner)
	require.ErrorIs(t, err, config.ErrSMA7702Unavailable)

	// Derived runner still uses the factory path.
	require.NoError(t, engine.requireMAv2SessionWallet(user, testPolicyChain, derived))
}

type eoaSetupReader struct {
	ChainStateReader
	owner      common.Address
	code, impl []byte
	failAt     common.Address
	cause      error
	implReads  int
}

func (r *eoaSetupReader) CodeAt(_ context.Context, address common.Address) ([]byte, error) {
	if address == r.failAt {
		return nil, r.cause
	}
	if address == r.owner {
		return r.code, nil
	}
	r.implReads++
	return r.impl, nil
}

func TestEOA7702PolicyChecksSeparateReadAndSetupFailures(t *testing.T) {
	for _, name := range []string{"no pin", "partial pin", "invalid pin", "no reader", "owner RPC", "implementation RPC", "runtime mismatch", "undelegated", "other delegate", "valid"} {
		t.Run(name, func(t *testing.T) {
			n, db, _, owner, _ := newPolicyTestEngine(t)
			require.NoError(t, StoreEOA7702Wallet(db, testPolicyChain, owner, config.SMA7702Delegate()))
			sw := n.ResolveSmartWalletConfig(testPolicyChain)
			sw.SMA7702Delegate, sw.SMA7702ImplHash = config.SMA7702Delegate(), config.SMA7702ImplHash()
			cause := errors.New("private RPC failure")
			r := &eoaSetupReader{owner: owner, code: append([]byte{0xef, 0x01, 0x00}, config.SMA7702Delegate().Bytes()...), impl: fixtures.SMA7702Runtime(), cause: cause}
			previous := GetChainStateReaderForChain(uint64(testPolicyChain))
			unregisterChainStateReader(uint64(testPolicyChain))
			t.Cleanup(func() {
				unregisterChainStateReader(uint64(testPolicyChain))
				RegisterChainStateReader(uint64(testPolicyChain), previous)
			})
			expected := config.ErrSMA7702Unavailable
			switch name {
			case "no pin":
				sw.SMA7702Delegate, sw.SMA7702ImplHash = common.Address{}, common.Hash{}
			case "partial pin":
				sw.SMA7702ImplHash = common.Hash{}
			case "invalid pin":
				sw.SMA7702ImplHash = common.HexToHash("0x1234")
			case "owner RPC":
				r.failAt = owner
			case "implementation RPC":
				r.failAt = config.SMA7702Delegate()
			case "runtime mismatch":
				r.impl = []byte{0x60, 0x00}
			case "undelegated":
				r.code, expected = nil, config.ErrEOADelegationMissing
			case "other delegate":
				r.code, expected = append([]byte{0xef, 0x01, 0x00}, common.HexToAddress("0x1234").Bytes()...), config.ErrEOADelegationMissing
			case "valid":
				expected = nil
			}
			if name != "no reader" {
				RegisterChainStateReader(uint64(testPolicyChain), r)
			}
			err := n.requireMAv2SessionWallet(&model.User{Address: owner, ChainID: testPolicyChain}, testPolicyChain, owner)
			require.ErrorIs(t, err, expected)
			if expected != config.ErrEOADelegationMissing {
				require.NotErrorIs(t, err, config.ErrEOADelegationMissing)
			}
			if name == "owner RPC" || name == "implementation RPC" {
				require.ErrorIs(t, err, cause)
			}
			if expected == config.ErrEOADelegationMissing {
				require.Zero(t, r.implReads)
			}
		})
	}
}
