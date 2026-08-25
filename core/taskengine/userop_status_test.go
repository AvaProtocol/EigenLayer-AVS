package taskengine

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeUserOpHash(t *testing.T) {
	want := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	got, err := normalizeUserOpHash("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	require.NoError(t, err)
	assert.Equal(t, want, strings.ToLower(got))

	_, err = normalizeUserOpHash("0xabc")
	assert.ErrorIs(t, err, ErrUserOpHashInvalid)

	_, err = normalizeUserOpHash("not-hex")
	assert.ErrorIs(t, err, ErrUserOpHashInvalid)
}

func TestParseBundlerUserOp_Envelope(t *testing.T) {
	token := common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	packed, err := aa.PackExecute(token, big.NewInt(0), []byte{0xa9, 0x05, 0x9c, 0xbb})
	require.NoError(t, err)

	sender := common.HexToAddress("0x2222222222222222222222222222222222222222")
	raw := map[string]interface{}{
		"userOperation": map[string]interface{}{
			"sender":   sender.Hex(),
			"callData": "0x" + common.Bytes2Hex(packed),
		},
		"transactionHash": "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	gotSender, callData, txHash, found := parseBundlerUserOp(raw)
	require.True(t, found)
	assert.Equal(t, sender, gotSender)
	assert.Equal(t, packed, callData)
	assert.Equal(t, "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", txHash)

	calls, err := InnerCallsFromExecuteCalldata(callData)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, token.Hex(), calls[0].To)
}

func TestParseBundlerUserOp_FlatAndNil(t *testing.T) {
	sender := common.HexToAddress("0x3333333333333333333333333333333333333333")
	gotSender, _, _, found := parseBundlerUserOp(map[string]interface{}{
		"sender":   sender.Hex(),
		"callData": "0x",
	})
	require.True(t, found)
	assert.Equal(t, sender, gotSender)

	_, _, _, found = parseBundlerUserOp(nil)
	assert.False(t, found)
	_, _, _, found = parseBundlerUserOp(map[string]interface{}{})
	assert.False(t, found)
}

func TestParseBundlerReceipt_SuccessAndNested(t *testing.T) {
	success, txHash, block := parseBundlerReceipt(map[string]interface{}{
		"success": true,
		"receipt": map[string]interface{}{
			"transactionHash": "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			"blockNumber":     "0x64",
		},
	})
	require.NotNil(t, success)
	assert.True(t, *success)
	assert.Equal(t, "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", txHash)
	assert.Equal(t, "0x64", block)

	success, txHash, block = parseBundlerReceipt(nil)
	assert.Nil(t, success)
	assert.Empty(t, txHash)
	assert.Empty(t, block)

	failed := false
	success, _, _ = parseBundlerReceipt(map[string]interface{}{"success": failed})
	require.NotNil(t, success)
	assert.False(t, *success)
}

const lookupHash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type mockUserOpBundler struct {
	op     interface{}
	opErr  error
	rec    interface{}
	recErr error
}

func (m *mockUserOpBundler) GetUserOperationByHash(context.Context, string) (interface{}, error) {
	return m.op, m.opErr
}
func (m *mockUserOpBundler) GetUserOperationReceipt(context.Context, string) (interface{}, error) {
	return m.rec, m.recErr
}
func (m *mockUserOpBundler) Close() {}

func lookupTestEngine(t *testing.T) (*Engine, storage.Storage, common.Address, common.Address) {
	t.Helper()
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	sepolia := &config.SmartWalletConfig{
		ChainID:         11155111,
		BundlerProvider: config.BundlerProviderSelfHosted,
		BundlerURL:      "http://bundler-sepolia.test",
	}
	base := &config.SmartWalletConfig{
		ChainID:         84532,
		BundlerProvider: config.BundlerProviderSelfHosted,
		BundlerURL:      "http://bundler-base.test",
	}
	engine := &Engine{
		db:                db,
		smartWalletConfig: sepolia,
		chainConfigs: map[int64]*config.ChainConfig{
			11155111: {ChainID: 11155111, Name: "sepolia", SmartWallet: sepolia},
			84532:    {ChainID: 84532, Name: "base-sepolia", SmartWallet: base},
		},
	}
	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	sender := common.HexToAddress("0x2222222222222222222222222222222222222222")
	require.NoError(t, StoreWallet(db, 11155111, owner, &model.SmartWallet{Address: &sender}))
	return engine, db, owner, sender
}

func packedTransfer(t *testing.T, to common.Address) []byte {
	t.Helper()
	packed, err := aa.PackExecute(to, big.NewInt(0), []byte{0xa9, 0x05, 0x9c, 0xbb})
	require.NoError(t, err)
	return packed
}

func installMockBundler(t *testing.T, mock *mockUserOpBundler) {
	t.Helper()
	orig := newUserOpBundler
	t.Cleanup(func() { newUserOpBundler = orig })
	newUserOpBundler = func(string) (userOpBundler, error) { return mock, nil }
}

func TestLookupUserOpStatus(t *testing.T) {
	engine, _, owner, sender := lookupTestEngine(t)
	user := &model.User{Address: owner, ChainID: 11155111}
	token := common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	op := map[string]interface{}{
		"userOperation": map[string]interface{}{
			"sender":   sender.Hex(),
			"callData": "0x" + common.Bytes2Hex(packedTransfer(t, token)),
		},
	}

	t.Run("pending when receipt is null", func(t *testing.T) {
		installMockBundler(t, &mockUserOpBundler{op: op})
		got, err := engine.LookupUserOpStatus(context.Background(), user, lookupHash, 11155111)
		require.NoError(t, err)
		assert.Equal(t, "pending", got.ExecutionStatus)
		require.Len(t, got.Calls, 1)
		assert.Equal(t, token.Hex(), got.Calls[0].To)
		assert.Nil(t, got.FailedCall)
	})

	t.Run("confirmed when receipt success=true", func(t *testing.T) {
		installMockBundler(t, &mockUserOpBundler{
			op:  op,
			rec: map[string]interface{}{"success": true, "transactionHash": "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "blockNumber": "0x64"},
		})
		got, err := engine.LookupUserOpStatus(context.Background(), user, lookupHash, 11155111)
		require.NoError(t, err)
		assert.Equal(t, "confirmed", got.ExecutionStatus)
		require.NotNil(t, got.Success)
		assert.True(t, *got.Success)
		assert.Nil(t, got.FailedCall)
	})

	t.Run("failed inner sets failedCall", func(t *testing.T) {
		installMockBundler(t, &mockUserOpBundler{
			op:  op,
			rec: map[string]interface{}{"success": false, "transactionHash": "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		})
		got, err := engine.LookupUserOpStatus(context.Background(), user, lookupHash, 11155111)
		require.NoError(t, err)
		assert.Equal(t, "failed", got.ExecutionStatus)
		require.NotNil(t, got.FailedCall)
		assert.Equal(t, token.Hex(), got.FailedCall.To)
	})

	t.Run("receipt RPC error is not pending", func(t *testing.T) {
		installMockBundler(t, &mockUserOpBundler{op: op, recErr: fmt.Errorf("bundler down")})
		_, err := engine.LookupUserOpStatus(context.Background(), user, lookupHash, 11155111)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "eth_getUserOperationReceipt")
		assert.False(t, errors.Is(err, ErrUserOpNotFound))
	})

	t.Run("unknown hash is not found", func(t *testing.T) {
		installMockBundler(t, &mockUserOpBundler{})
		_, err := engine.LookupUserOpStatus(context.Background(), user, lookupHash, 11155111)
		assert.ErrorIs(t, err, ErrUserOpNotFound)
	})

	t.Run("foreign sender is not found", func(t *testing.T) {
		other := common.HexToAddress("0x3333333333333333333333333333333333333333")
		installMockBundler(t, &mockUserOpBundler{op: map[string]interface{}{
			"sender":   other.Hex(),
			"callData": "0x",
		}})
		_, err := engine.LookupUserOpStatus(context.Background(), user, lookupHash, 11155111)
		assert.ErrorIs(t, err, ErrUserOpNotFound)
	})

	t.Run("explicit unsupported chain is rejected", func(t *testing.T) {
		installMockBundler(t, &mockUserOpBundler{op: op})
		_, err := engine.LookupUserOpStatus(context.Background(), user, lookupHash, 99999)
		assert.ErrorIs(t, err, ErrUserOpChainUnsupported)
	})

	t.Run("wallet on chain A does not authorize chain B", func(t *testing.T) {
		installMockBundler(t, &mockUserOpBundler{op: op})
		_, err := engine.LookupUserOpStatus(context.Background(), user, lookupHash, 84532)
		assert.ErrorIs(t, err, ErrUserOpNotFound)
	})
}
