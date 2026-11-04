package taskengine

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

func TestBudgetReadUsesRPCBlockHashAndDetectsReorg(t *testing.T) {
	var canonical atomic.Value
	canonical.Store(common.HexToHash("0x123456"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		var result interface{}
		switch req.Method {
		case "eth_getBlockByNumber":
			// Keep the server's hash, including header fields unknown to a
			// client. A reconstructed minimal Header cannot have this hash.
			result = map[string]interface{}{"number": "0xa", "hash": canonical.Load(), "newForkHeaderField": "0x9999"}
		case "eth_call", "eth_getCode":
			var at struct {
				BlockHash common.Hash `json:"blockHash"`
			}
			require.NoError(t, json.Unmarshal(req.Params[1], &at))
			require.Equal(t, common.HexToHash("0x123456"), at.BlockHash)
			if req.Method == "eth_getCode" {
				result = "0x6000"
			} else {
				result = "0x" + common.Bytes2Hex(common.LeftPadBytes(big.NewInt(7).Bytes(), 32))
			}
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result}))
	}))
	t.Cleanup(server.Close)
	client, err := ethclient.Dial(server.URL)
	require.NoError(t, err)
	t.Cleanup(client.Close)
	block, err := budgetBlockAt(context.Background(), client, big.NewInt(10))
	require.NoError(t, err)
	require.Equal(t, canonical.Load(), block.Hash)
	v := &pinnedBudgetView{chain: client, block: block}
	to := common.HexToAddress("0xabc")
	out, err := v.CallContract(context.Background(), ethereum.CallMsg{To: &to}, nil)
	require.NoError(t, err)
	require.Equal(t, big.NewInt(7), new(big.Int).SetBytes(out))
	code, err := v.CodeAt(context.Background(), to)
	require.NoError(t, err)
	require.Equal(t, []byte{0x60, 0x00}, code)
	require.NoError(t, v.VerifyCanonical(context.Background()))
	canonical.Store(common.HexToHash("0xdead"))
	require.ErrorContains(t, v.VerifyCanonical(context.Background()), "no longer canonical")
}
