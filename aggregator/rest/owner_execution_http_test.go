package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// Exercises the real REST handlers, graph runner, account checks, pricing,
// personal signature, persistent continuation, keyless relay and existing status
// decoder. Only the external chain/bundler are local HTTP fixtures. No real
// network, controller/session key, testnet wallet or transaction is used.
func TestOwnerWorkflowHTTPPrepareSignSubmitAndExistingStatus(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	owner := crypto.PubkeyToAddress(key.PublicKey)
	sender := common.HexToAddress("0x2222222222222222222222222222222222222222")
	implementation := common.HexToAddress("0x000000000000c5A9089039570Dd36455b5C07383")
	factory := aa.MAv2FactoryAddress()
	token1 := "0x6666666666666666666666666666666666666666"
	token2 := "0x7777777777777777777777777777777777777777"
	var mu sync.Mutex
	var sent map[string]string
	sendCount := 0
	selector := func(signature string) string { return hexutil.Encode(crypto.Keccak256([]byte(signature))[:4]) }
	chain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		var result interface{}
		switch req.Method {
		case "eth_chainId":
			result = "0xaa36a7"
		case "eth_getCode":
			var address common.Address
			require.NoError(t, json.Unmarshal(req.Params[0], &address))
			result = "0x6000"
			if address == owner {
				result = "0x"
			}
		case "eth_getStorageAt":
			result = hexutil.Encode(common.LeftPadBytes(implementation.Bytes(), 32))
		case "eth_getBalance":
			result = "0xde0b6b3a7640000"
		case "eth_blockNumber":
			result = "0x1"
		case "eth_getBlockByNumber":
			result = &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(0), GasLimit: 30000000, BaseFee: big.NewInt(1), Extra: []byte{}}
		case "eth_maxPriorityFeePerGas", "rundler_maxPriorityFeePerGas":
			result = "0x3b9aca00"
		case "eth_call":
			var call map[string]string
			require.NoError(t, json.Unmarshal(req.Params[0], &call))
			data := call["data"]
			if data == "" {
				data = call["input"]
			}
			require.GreaterOrEqual(t, len(data), 10)
			var value []byte
			switch data[:10] {
			case selector("getAddressSemiModular(address,uint256)"):
				value = common.LeftPadBytes(sender.Bytes(), 32)
			case selector("SEMI_MODULAR_ACCOUNT_IMPL()"):
				value = common.LeftPadBytes(implementation.Bytes(), 32)
			case selector("getFallbackSignerData()"):
				value = append(common.LeftPadBytes(owner.Bytes(), 32), make([]byte, 32)...)
			case selector("entryPoint()"):
				value = common.LeftPadBytes(preset.EntryPointV07().Bytes(), 32)
			case selector("getNonce(address,uint192)"):
				value = common.LeftPadBytes(new(big.Int).Lsh(big.NewInt(1), 64).Bytes(), 32)
			case selector("balanceOf(address)"):
				amount := big.NewInt(0)
				if strings.EqualFold(call["to"], token1) {
					amount = big.NewInt(1000000)
				}
				if strings.EqualFold(call["to"], token2) {
					amount = big.NewInt(20000000000000000)
				}
				value = common.LeftPadBytes(amount.Bytes(), 32)
			default:
				t.Errorf("unexpected owner HTTP eth_call selector %s", data[:10])
			}
			result = hexutil.Encode(value)
		case "eth_estimateUserOperationGas":
			result = map[string]string{"callGasLimit": "0x186a0", "verificationGasLimit": "0xc350", "preVerificationGas": "0x7530"}
		case "eth_sendUserOperation":
			var wire map[string]string
			require.NoError(t, json.Unmarshal(req.Params[0], &wire))
			op := ownerHTTPOperation(t, wire)
			hash, err := op.GetUserOpHash(preset.EntryPointV07(), big.NewInt(11155111))
			require.NoError(t, err)
			require.Equal(t, []byte{0xff, 0}, op.Signature[:2])
			sig := bytes.Clone(op.Signature[2:])
			sig[64] -= 27
			pub, err := crypto.SigToPub(accounts.TextHash(hash.Bytes()), sig)
			require.NoError(t, err)
			require.Equal(t, owner, crypto.PubkeyToAddress(*pub))
			mu.Lock()
			sent = wire
			sendCount++
			mu.Unlock()
			result = hash.Hex()
		case "eth_getUserOperationByHash":
			mu.Lock()
			wire := sent
			mu.Unlock()
			result = map[string]interface{}{"userOperation": wire}
		case "eth_getUserOperationReceipt":
			result = map[string]interface{}{"success": true, "receipt": map[string]interface{}{"transactionHash": common.HexToHash("0x1234").Hex(), "blockNumber": "0x1"}}
		default:
			t.Errorf("unexpected owner HTTP RPC %s", req.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result}))
	}))
	defer chain.Close()
	db, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, taskengine.StoreWallet(db, 11155111, owner, &model.SmartWallet{Owner: &owner, Address: &sender, Factory: &factory, Salt: big.NewInt(0)}))
	cfg := &config.Config{OwnerExecutionEnabled: true, SmartWallet: &config.SmartWalletConfig{ChainID: 11155111, EthRpcUrl: chain.URL, BundlerURL: chain.URL, BundlerProvider: config.BundlerProviderSelfHosted, DisableGasSponsorship: true}}
	engine := taskengine.New(db, cfg, nil, testutil.GetLogger())
	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("auth.user", &restmw.AuthenticatedUser{Subject: owner.Hex(), ChainID: 11155111})
			return next(c)
		}
	})
	s := &Server{engine: engine, config: cfg, logger: testutil.GetLogger()}
	s.Mount(e)
	server := httptest.NewServer(e)
	defer server.Close()
	call := func(method, path string, body interface{}, out interface{}, want int) {
		t.Helper()
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		req, err := http.NewRequestWithContext(context.Background(), method, server.URL+"/api/v1"+path, bytes.NewReader(encoded))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var raw json.RawMessage
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&raw))
		require.Equal(t, want, resp.StatusCode, string(raw))
		require.NoError(t, json.Unmarshal(raw, out))
	}
	readABI := []interface{}{map[string]interface{}{"type": "function", "name": "balanceOf", "stateMutability": "view", "inputs": []interface{}{map[string]interface{}{"name": "account", "type": "address"}}, "outputs": []interface{}{map[string]interface{}{"name": "", "type": "uint256"}}}}
	writeABI := []interface{}{map[string]interface{}{"type": "function", "name": "transfer", "inputs": []interface{}{map[string]interface{}{"name": "to", "type": "address"}, map[string]interface{}{"name": "amount", "type": "uint256"}}, "outputs": []interface{}{map[string]interface{}{"name": "", "type": "bool"}}}}
	definition := map[string]interface{}{
		"chainId": 11155111, "trigger": map[string]interface{}{"id": "start", "name": "start", "type": "manual", "config": map[string]interface{}{"lang": "json", "data": map[string]interface{}{}}},
		"inputVariables": map[string]interface{}{"settings": map[string]interface{}{"name": "Sweep HTTP proof", "runner": sender.Hex(), "chain": "sepolia"}},
		"nodes": []interface{}{
			map[string]interface{}{"id": "usdc", "name": "usdc", "type": "contractRead", "config": map[string]interface{}{"chainId": 11155111, "contractAddress": token1, "contractAbi": readABI, "methodCalls": []interface{}{map[string]interface{}{"methodName": "balanceOf", "methodParams": []string{sender.Hex()}}}}},
			map[string]interface{}{"id": "weth", "name": "weth", "type": "contractRead", "config": map[string]interface{}{"chainId": 11155111, "contractAddress": token2, "contractAbi": readABI, "methodCalls": []interface{}{map[string]interface{}{"methodName": "balanceOf", "methodParams": []string{sender.Hex()}}}}},
			map[string]interface{}{"id": "transfer", "name": "transfer", "type": "contractWrite", "config": map[string]interface{}{"chainId": 11155111, "contractAddress": token1, "contractAbi": writeABI, "methodCalls": []interface{}{
				map[string]interface{}{"methodName": "transfer", "methodParams": []string{owner.Hex(), "{{usdc.data.balanceOf}}"}},
				map[string]interface{}{"methodName": "transfer", "contractAddress": token2, "methodParams": []string{owner.Hex(), "{{weth.data.balanceOf}}"}},
			}}},
		},
		"edges": []interface{}{map[string]string{"id": "a", "source": "start", "target": "usdc"}, map[string]string{"id": "b", "source": "usdc", "target": "weth"}, map[string]string{"id": "c", "source": "weth", "target": "transfer"}},
	}
	var prepared generated.PrepareWorkflowResponse
	call(http.MethodPost, "/workflows:prepare", definition, &prepared, http.StatusOK)
	require.Equal(t, generated.PrepareWorkflowResponseSigningMethod("personal_sign"), prepared.SigningMethod)
	require.Equal(t, "waiting", string(prepared.Execution.Status))
	require.Len(t, prepared.Calls, 2)
	require.Equal(t, "0x", prepared.UserOperation.Signature)
	require.Equal(t, "1000000", new(big.Int).SetBytes(common.FromHex(prepared.Calls[0].Data)[36:68]).String())
	require.Equal(t, "20000000000000000", new(big.Int).SetBytes(common.FromHex(prepared.Calls[1].Data)[36:68]).String())
	sig, err := crypto.Sign(accounts.TextHash(common.HexToHash(prepared.UserOpHash).Bytes()), key)
	require.NoError(t, err)
	sig[64] += 27
	for i := 0; i < 2; i++ {
		var submitted generated.SubmitExecutionResponse
		call(http.MethodPost, "/executions/"+prepared.Execution.Id+":submit", generated.SubmitExecutionRequest{Signature: hexutil.Encode(sig)}, &submitted, http.StatusAccepted)
		require.Equal(t, prepared.UserOpHash, submitted.UserOpHash)
	}
	var receipt generated.UserOpStatusResponse
	call(http.MethodGet, "/userops/"+prepared.UserOpHash+"?chainId=11155111", nil, &receipt, http.StatusOK)
	require.Equal(t, "confirmed", string(receipt.ExecutionStatus))
	require.NotNil(t, receipt.Calls)
	require.Len(t, *receipt.Calls, 2)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, sendCount)
}

func ownerHTTPOperation(t *testing.T, wire map[string]string) *userop.UserOperationV07 {
	t.Helper()
	quantity := func(name string) *big.Int { v, err := hexutil.DecodeBig(wire[name]); require.NoError(t, err); return v }
	data, err := hexutil.Decode(wire["callData"])
	require.NoError(t, err)
	sig, err := hexutil.Decode(wire["signature"])
	require.NoError(t, err)
	return &userop.UserOperationV07{Sender: common.HexToAddress(wire["sender"]), Nonce: quantity("nonce"), CallData: data, Signature: sig, CallGasLimit: quantity("callGasLimit"), VerificationGasLimit: quantity("verificationGasLimit"), PreVerificationGas: quantity("preVerificationGas"), MaxFeePerGas: quantity("maxFeePerGas"), MaxPriorityFeePerGas: quantity("maxPriorityFeePerGas")}
}
