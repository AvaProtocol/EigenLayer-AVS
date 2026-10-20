package main

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRPC answers eth_getBalance with zero and eth_call with callResult.
func fakeRPC(t *testing.T, callResult string) *ethclient.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		result := "0x0"
		if req.Method == "eth_call" {
			result = callResult
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	client, err := ethclient.Dial(server.URL)
	require.NoError(t, err)
	return client
}

var testToken = token{Address: common.HexToAddress("0x00000000000000000000000000000000000000AA"), Symbol: "USDC", Decimals: 6}

func TestCheckReportsAFailedTokenReadInsteadOfZero(t *testing.T) {
	w := &wallet{Address: common.HexToAddress("0x00000000000000000000000000000000000000B1")}
	check(w, fakeRPC(t, "0x"), []token{testToken}) // no code: empty return data

	assert.False(t, w.Checked, "an unreadable balance is not a checked zero")
	assert.Contains(t, w.Err, "1 token read(s) failed: USDC")
}

func TestCheckTreatsAZeroBalanceAsChecked(t *testing.T) {
	w := &wallet{Address: common.HexToAddress("0x00000000000000000000000000000000000000B2")}
	check(w, fakeRPC(t, "0x"+strings.Repeat("0", 64)), []token{testToken})

	assert.True(t, w.Checked)
	assert.Empty(t, w.Err)
	assert.Empty(t, w.Holdings)
}

func TestFormatUnits(t *testing.T) {
	amount, ok := new(big.Int).SetString("81644719381898112", 10)
	require.True(t, ok)
	assert.Equal(t, "0.081644719381898112", formatUnits(amount, 18))
	assert.Equal(t, "10", formatUnits(big.NewInt(10_000_000), 6))
	assert.Equal(t, "1.5", formatUnits(big.NewInt(1_500_000), 6))
}
