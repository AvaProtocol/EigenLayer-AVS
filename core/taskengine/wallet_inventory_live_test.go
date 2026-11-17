//go:build integration

package taskengine

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/walletinventory"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

const inventoryFirstChain = int64(84532)

// No production registry is changed. Random high salts keep this DB's
// history separate from the test owner's existing funded wallets.
func prepareLiveCrossNetworkInventory(t *testing.T, n *Engine, f *budgetSpike) {
	t.Helper()
	sw := n.ResolveSmartWalletConfig(inventoryFirstChain)
	require.NotNil(t, sw, "Base Sepolia must be configured")
	url := sw.EthRpcUrl
	if sw.BundlerProvider == "alchemy" || sw.BundlerProvider == "" {
		var err error
		url, err = sw.ActiveBundlerURL()
		require.NoError(t, err)
	}
	first, err := ethclient.DialContext(f.ctx, url)
	require.NoError(t, err)
	t.Cleanup(first.Close)
	id, err := first.ChainID(f.ctx)
	require.NoError(t, err)
	require.EqualValues(t, inventoryFirstChain, id.Int64(), "allocation must use a testnet")
	// A nil rpcConn selects Engine's fake derivation mode. This probe must
	// actually call both deployed factories, without mock chain readers.
	previousRPC := rpcConn
	rpcConn = f.chain
	t.Cleanup(func() { rpcConn = previousRPC })
	for _, chain := range []int64{inventoryFirstChain, testPolicyChain} {
		require.Nil(t, GetChainStateReaderForChain(uint64(chain)))
	}
	baseSalt := new(big.Int).Set(f.salt)
	require.Greater(t, baseSalt.BitLen(), 128)
	seedAddress, err := aa.GetSenderAddressMAv2WithContext(f.ctx, first, f.owner, baseSalt)
	require.NoError(t, err)
	factory := aa.MAv2FactoryAddress()
	seed, err := walletinventory.Register(n.db, inventoryFirstChain, f.owner, &model.SmartWallet{
		Owner: &f.owner, Factory: &factory, Address: seedAddress, Salt: baseSalt,
	}, 3)
	require.NoError(t, err)
	firstUser := &model.User{Address: f.owner, ChainID: inventoryFirstChain}
	secondUser := &model.User{Address: f.owner, ChainID: testPolicyChain}
	var allocated *model.SmartWallet
	for i := int64(1); i <= 2; i++ {
		wallet, err := n.AllocateWallet(f.ctx, firstUser, fmt.Sprintf("live-%s-%d", baseSalt, i), walletinventory.Profile)
		require.NoError(t, err)
		require.Equal(t, new(big.Int).Add(baseSalt, big.NewInt(i)), wallet.Salt)
		if i == 1 {
			allocated = wallet
		}
	}
	_, err = n.AllocateWallet(f.ctx, secondUser, "live-at-capacity", walletinventory.Profile)
	require.ErrorIs(t, err, walletinventory.ErrLimit)
	_, err = walletinventory.SetHidden(n.db, f.owner, seed.Address.Hex(), true, 3)
	require.NoError(t, err)
	next, err := n.AllocateWallet(f.ctx, secondUser, "live-after-hide", walletinventory.Profile)
	require.NoError(t, err)
	require.Equal(t, new(big.Int).Add(baseSalt, big.NewInt(3)), next.Salt)
	_, err = walletinventory.SetHidden(n.db, f.owner, seed.Address.Hex(), false, 3)
	require.ErrorIs(t, err, walletinventory.ErrLimit)
	firstRows, firstUsage, err := n.InventoryWallets(firstUser, &avsproto.ListWalletReq{})
	require.NoError(t, err)
	secondRows, secondUsage, err := n.InventoryWallets(secondUser, &avsproto.ListWalletReq{})
	require.NoError(t, err)
	require.Equal(t, firstUsage, secondUsage)
	require.Equal(t, 3, firstUsage.ActiveCount)
	require.Len(t, firstRows, 4)
	require.Equal(t, firstRows, secondRows, "network changes must preserve identity and visibility")
	f.wallet, f.salt = *allocated.Address, new(big.Int).Set(allocated.Salt)
	for _, client := range []*ethclient.Client{first, f.chain} {
		derived, err := aa.GetSenderAddressMAv2WithContext(f.ctx, client, f.owner, f.salt)
		require.NoError(t, err)
		require.Equal(t, f.wallet, *derived)
		code, err := client.CodeAt(f.ctx, f.wallet, nil)
		require.NoError(t, err)
		require.Empty(t, code, "use only a fresh counterfactual wallet")
		balance, err := client.BalanceAt(f.ctx, f.wallet, nil)
		require.NoError(t, err)
		require.Zero(t, balance.Sign(), "do not touch a previously funded wallet")
	}
	t.Logf("live inventory: owner=%s firstChain=%d secondChain=%d wallet=%s salt=%s hiddenSalt=%s nextSalt=%s active=3; add and restore refused at capacity",
		f.owner, inventoryFirstChain, testPolicyChain, f.wallet, f.salt, seed.Salt, next.Salt)
}

func assertLiveCrossNetworkPolicyIsolation(t *testing.T, n *Engine, f *budgetSpike) {
	t.Helper()
	user := &model.User{Address: f.owner}
	second, err := n.ListSessionPoliciesForWallet(user, testPolicyChain, f.wallet)
	require.NoError(t, err)
	require.Len(t, second, 1)
	first, err := n.ListSessionPoliciesForWallet(user, inventoryFirstChain, f.wallet)
	require.NoError(t, err)
	require.Empty(t, first, "the second network's budget must not grant authority on the first")
	sw := n.ResolveSmartWalletConfig(inventoryFirstChain)
	url := sw.EthRpcUrl
	if sw.BundlerProvider == "alchemy" || sw.BundlerProvider == "" {
		url, err = sw.ActiveBundlerURL()
		require.NoError(t, err)
	}
	client, err := ethclient.DialContext(f.ctx, url)
	require.NoError(t, err)
	defer client.Close()
	firstCode, err := client.CodeAt(f.ctx, f.wallet, nil)
	require.NoError(t, err)
	require.Empty(t, firstCode, "deploying on Sepolia must not deploy on Base Sepolia")
	secondCode, err := f.chain.CodeAt(f.ctx, f.wallet, nil)
	require.NoError(t, err)
	require.NotEmpty(t, secondCode)
	firstBalance, err := client.BalanceAt(f.ctx, f.wallet, nil)
	require.NoError(t, err)
	require.Zero(t, firstBalance.Sign())
	secondBalance, err := f.chain.BalanceAt(f.ctx, f.wallet, nil)
	require.NoError(t, err)
	require.Positive(t, secondBalance.Sign(), "balance must reflect the selected network")
	_, usage, err := walletinventory.Snapshot(n.db, f.owner, 3)
	require.NoError(t, err)
	require.Equal(t, 3, usage.ActiveCount, "using another network consumes no additional slot")
}
