package taskengine

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
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

func TestStoreEOA7702WalletRejectsNonCanonicalDelegate(t *testing.T) {
	_, db, _, owner, _ := newPolicyTestEngine(t)
	err := StoreEOA7702Wallet(db, testPolicyChain, owner, common.HexToAddress("0x000000000000000000000000000000000000dEaD"))
	require.Error(t, err)
}

func TestRequireMAv2SessionWalletEOA7702(t *testing.T) {
	engine, db, _, owner, derived := newPolicyTestEngine(t)
	user := &model.User{Address: owner, ChainID: testPolicyChain}
	require.NoError(t, StoreEOA7702Wallet(db, testPolicyChain, owner, config.SMA7702Delegate()))

	// No chain reader in this fixture: K13 fail-closed, not "not MA v2".
	err := engine.requireMAv2SessionWallet(user, testPolicyChain, owner)
	require.ErrorIs(t, err, ErrEOADelegationMissing)

	// Derived runner still uses the factory path.
	require.NoError(t, engine.requireMAv2SessionWallet(user, testPolicyChain, derived))
}
