package taskengine

import (
	"context"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

func TestBudgetSessionLowerNonceRetryKeepsExecutableFlight(t *testing.T) {
	f, p, backend, _ := newBudgetChangeTest(t)
	lease, err := f.n.guardBudgetSessionSend(context.Background(), testPolicyChain, f.user.Address, f.in.Wallet)
	require.NoError(t, err)
	t.Cleanup(lease.Release)
	op := *(*userop.UserOperationV07)(p.Operation)
	highNonce, err := userop.EncodeNonceMAv2(7, userop.ValidationOptionGlobal, 1)
	require.NoError(t, err)
	op.Nonce = highNonce
	require.NoError(t, lease.BeforeBroadcast(context.Background(), &op))
	highHash, err := op.GetUserOpHash(preset.EntryPointV07(), big.NewInt(testPolicyChain))
	require.NoError(t, err)
	op.Nonce, err = userop.EncodeNonceMAv2(7, userop.ValidationOptionGlobal, 0)
	require.NoError(t, err)
	require.NoError(t, lease.BeforeBroadcast(context.Background(), &op))
	lowHash, err := op.GetUserOpHash(preset.EntryPointV07(), big.NewInt(testPolicyChain))
	require.NoError(t, err)
	receipt := budgetSessionReceipt(f.in.Wallet, lowHash, op.Nonce)
	require.NoError(t, lease.Settled(context.Background(), receipt))
	prefix := budgetFlightPrefix(testPolicyChain, f.user.Address, f.in.Wallet)
	_, err = f.n.db.GetKey(append(prefix, highHash.Hex()...))
	require.NoError(t, err, "the higher-nonce signature remains executable")
	_, err = f.n.db.GetKey(append(prefix, lowHash.Hex()...))
	require.Error(t, err, "the included nonce is settled")
	view, err := backend.View(context.Background(), testPolicyChain, nil)
	require.NoError(t, err)
	defer view.Close()
	nextNonce := new(big.Int).Set(highNonce)
	backend.caller = func(ethereum.CallMsg) ([]byte, error) {
		return common.LeftPadBytes(nextNonce.Bytes(), 32), nil
	}
	require.ErrorContains(t, f.n.checkBudgetSessionFlights(context.Background(), p, view), "still in flight")
	nextNonce.Add(nextNonce, big.NewInt(1))
	require.NoError(t, f.n.checkBudgetSessionFlights(context.Background(), p, view))
}

func budgetSessionReceipt(wallet common.Address, hash common.Hash, nonce *big.Int) *types.Receipt {
	blockHash := common.BigToHash(big.NewInt(10))
	return &types.Receipt{BlockNumber: big.NewInt(10), BlockHash: blockHash, Logs: []*types.Log{{
		Address: preset.EntryPointV07(), BlockHash: blockHash, BlockNumber: 10,
		Topics: []common.Hash{budgetUserOpEvent, hash, common.BytesToHash(wallet.Bytes()), {}},
		Data:   append(common.LeftPadBytes(nonce.Bytes(), 32), make([]byte, 96)...),
	}}}
}

func TestBudgetSessionSettlementRequiresExactInclusionEvidence(t *testing.T) {
	wallet := common.HexToAddress("0x1234")
	hash := common.HexToHash("0x5678")
	nonce, err := userop.EncodeNonceMAv2(7, userop.ValidationOptionGlobal, 3)
	require.NoError(t, err)
	lower := new(big.Int).Sub(nonce, big.NewInt(1))
	higher := new(big.Int).Add(nonce, big.NewInt(1))
	otherKey, err := userop.EncodeNonceMAv2(6, userop.ValidationOptionGlobal, 0)
	require.NoError(t, err)
	flights := map[string]budgetSessionFlight{
		"included": {Hash: hash, Nonce: nonce.String()},
		"lower":    {Hash: common.HexToHash("0x1"), Nonce: lower.String()},
		"higher":   {Hash: common.HexToHash("0x2"), Nonce: higher.String()},
		"other":    {Hash: common.HexToHash("0x3"), Nonce: otherKey.String()},
	}
	for _, name := range []string{"no event", "unknown hash", "wrong sender", "wrong nonce", "wrong block", "removed", "included"} {
		t.Run(name, func(t *testing.T) {
			receipt := budgetSessionReceipt(wallet, hash, nonce)
			log := receipt.Logs[0]
			switch name {
			case "no event":
				receipt.Logs = nil
			case "unknown hash":
				log.Topics[1] = common.Hash{}
			case "wrong sender":
				log.Topics[2] = common.Hash{}
			case "wrong nonce":
				log.Data[31]++
			case "wrong block":
				log.BlockHash = common.Hash{}
			case "removed":
				log.Removed = true
			}
			deleted := consumedBudgetFlightKeys(receipt, wallet, flights)
			if name == "included" {
				require.ElementsMatch(t, [][]byte{[]byte("included"), []byte("lower")}, deleted)
			} else {
				require.Empty(t, deleted)
			}
		})
	}
}
