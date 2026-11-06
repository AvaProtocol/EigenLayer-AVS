package config

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestBudgetRetiredSignersAreExplicitAndChainScoped(t *testing.T) {
	address := common.HexToAddress("0xabc")
	parsed, err := parseBudgetRetiredSessionSigners(map[int64][]string{11155111: {address.Hex(), address.Hex()}})
	require.NoError(t, err)
	require.Equal(t, []common.Address{address}, parsed[11155111])
	require.Empty(t, parsed[1])
	for _, raw := range []map[int64][]string{{0: {address.Hex()}}, {1: {"not-an-address"}}, {1: {common.Address{}.Hex()}}} {
		_, err = parseBudgetRetiredSessionSigners(raw)
		require.Error(t, err)
	}
}
