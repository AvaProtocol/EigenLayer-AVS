package taskengine

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/walletinventory"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestGlobalRunnerMaterializationWhileExecutionShardsHeld(t *testing.T) {
	db, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := aa.MAv2FactoryAddress()
	for _, salt := range []int64{4, 9007199254740993} {
		runner := mockDerivedSenderAddress(owner, factory, big.NewInt(salt))
		_, err := walletinventory.Register(db, 11155111, owner, &model.SmartWallet{
			Owner: &owner, Factory: &factory, Address: &runner, Salt: big.NewInt(salt),
		}, 3)
		require.NoError(t, err)
		_, err = walletinventory.SetHidden(db, owner, runner.Hex(), true, 3)
		require.NoError(t, err)
		_, before, err := walletinventory.Snapshot(db, owner, 3)
		require.NoError(t, err)
		vm := &VM{db: db, TaskOwner: owner, task: &model.Workflow{Task: &avsproto.Task{SmartWalletAddress: runner.Hex()}}}
		engine := &Engine{}
		// Hold every execution shard, including owner preparation and any hash
		// collision. Inventory must never re-enter one while materializing.
		for i := range engine.executionMutexes {
			engine.executionMutexes[i].Lock()
		}
		done := make(chan error, 1)
		go func() { done <- vm.materializeInventoryRunner(84532) }()
		select {
		case err = <-done:
		case <-time.After(3 * time.Second):
			err = errors.New("inventory waited for an execution shard")
		}
		for i := range engine.executionMutexes {
			engine.executionMutexes[i].Unlock()
		}
		require.NoError(t, err, "inventory must not wait for an execution shard")
		for _, chain := range []int64{84532, 11155111, 84532} {
			require.NoError(t, vm.materializeInventoryRunner(chain))
			raw, err := db.GetKey([]byte(WalletStorageKey(chain, owner, runner.Hex())))
			require.NoError(t, err)
			var runtime model.SmartWallet
			require.NoError(t, runtime.FromStorageData(raw))
			require.Equal(t, big.NewInt(salt), runtime.Salt)
			require.True(t, runtime.IsHidden, "runtime materialization cannot restore an archived identity")
		}
		valid, err := ValidWalletOwner(db, 10, &model.User{Address: owner}, runner)
		require.NoError(t, err)
		require.True(t, valid)
		_, after, err := walletinventory.Snapshot(db, owner, 3)
		require.NoError(t, err)
		require.Equal(t, before, after, "using another runtime network consumes no slot or revision")
	}
}

func TestChainOnlyVisibilityUpdateCannotBypassGlobalAdmission(t *testing.T) {
	db, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	factory := aa.MAv2FactoryAddress()
	for salt := int64(0); salt < 3; salt++ {
		address := mockDerivedSenderAddress(owner, factory, big.NewInt(salt))
		_, err = walletinventory.Register(db, 11155111, owner, &model.SmartWallet{
			Owner: &owner, Address: &address, Factory: &factory, Salt: big.NewInt(salt),
		}, 3)
		require.NoError(t, err)
	}
	for _, hidden := range []bool{true, false} {
		salt := int64(4)
		if !hidden {
			salt = 5
		}
		address := mockDerivedSenderAddress(owner, factory, big.NewInt(salt))
		row := &model.SmartWallet{Owner: &owner, Address: &address, Factory: &factory, Salt: big.NewInt(salt), IsHidden: hidden}
		raw, err := row.ToJSON()
		require.NoError(t, err)
		require.NoError(t, db.Set([]byte(WalletStorageKey(84532, owner, address.Hex())), raw))
		err = SetWalletHiddenStatus(db, 84532, owner, address.Hex(), false, 3)
		require.ErrorIs(t, err, walletinventory.ErrLimit)
		// The same historical address remains hideable at capacity.
		require.NoError(t, SetWalletHiddenStatus(db, 84532, owner, address.Hex(), true, 3))
		stored, err := walletinventory.Get(db, owner, address.Hex())
		require.NoError(t, err)
		require.True(t, stored.IsHidden)
	}
	_, usage, err := walletinventory.Snapshot(db, owner, 3)
	require.NoError(t, err)
	require.Equal(t, 3, usage.ActiveCount)
}
