package rest

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// Real mounted routes, storage, attestation verification, account inspection and
// pricing. Only chain/bundler I/O is mocked; no private test override or send.
func TestBudgetPreparationHTTPStorageAndSignedLifecycle(t *testing.T) {
	for _, deployed := range []bool{true, false} {
		t.Run(map[bool]string{true: "deployed", false: "counterfactual"}[deployed], func(t *testing.T) {
			r := newPolicyRig(t)
			cfg := r.server.config
			cfg.JwtSecret = nil
			cfg.WalletBudgetPreparationEnabled = true
			service, err := crypto.GenerateKey()
			require.NoError(t, err)
			cfg.WalletBudgetDiscoverySigner = crypto.PubkeyToAddress(service.PublicKey).Hex()
			impl := common.HexToAddress("0x000000000000c5A9089039570Dd36455b5C07383")
			token := common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
			spender := common.HexToAddress("0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E")
			var sends, estimates atomic.Int32
			var pinnedOwnerReads atomic.Int32
			zeroBalance := atomic.Bool{}
			sendEnabled := atomic.Bool{}
			mined := atomic.Bool{}
			var submittedHash atomic.Value
			header := func(number int64) *types.Header {
				return &types.Header{Number: big.NewInt(number), Difficulty: big.NewInt(0), GasLimit: 30000000, BaseFee: big.NewInt(1), Extra: []byte{}}
			}
			chain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var rpcReq struct {
					ID     json.RawMessage   `json:"id"`
					Method string            `json:"method"`
					Params []json.RawMessage `json:"params"`
				}
				require.NoError(t, json.NewDecoder(req.Body).Decode(&rpcReq))
				if sendEnabled.Load() && !mined.Load() {
					switch rpcReq.Method {
					case "eth_getCode", "eth_getStorageAt", "eth_getBalance", "eth_call":
						last := string(rpcReq.Params[len(rpcReq.Params)-1])
						if !strings.Contains(last, "blockHash") {
							require.Equal(t, `"0x1"`, last, "all owner identity/nonce/fee reads must use the final canonical block")
							pinnedOwnerReads.Add(1)
						}
					}
				}
				var result interface{}
				switch rpcReq.Method {
				case "eth_chainId":
					result = "0xaa36a7"
				case "eth_getCode":
					var address common.Address
					require.NoError(t, json.Unmarshal(rpcReq.Params[0], &address))
					result = "0x6000"
					if address == r.owner || address == r.wallet && !deployed {
						result = "0x"
					}
				case "eth_getStorageAt":
					result = hexutil.Encode(common.LeftPadBytes(impl.Bytes(), 32))
				case "eth_getBalance":
					result = "0xde0b6b3a7640000"
					if zeroBalance.Load() {
						result = "0x0"
					}
				case "eth_getBlockByNumber":
					number := int64(1)
					if mined.Load() {
						number = 2
					}
					result = header(number)
				case "eth_maxPriorityFeePerGas", "rundler_maxPriorityFeePerGas":
					result = "0x3b9aca00"
				case "eth_call":
					var call map[string]string
					require.NoError(t, json.Unmarshal(rpcReq.Params[0], &call))
					data := common.FromHex(call["data"])
					if len(data) == 0 {
						data = common.FromHex(call["input"])
					}
					require.GreaterOrEqual(t, len(data), 4)
					selector := hexutil.Encode(data[:4])
					var value []byte
					sel := func(name string) string { return hexutil.Encode(crypto.Keccak256([]byte(name))[:4]) }
					switch selector {
					case sel("getAddressSemiModular(address,uint256)"):
						value = common.LeftPadBytes(r.wallet.Bytes(), 32)
					case sel("SEMI_MODULAR_ACCOUNT_IMPL()"):
						value = common.LeftPadBytes(impl.Bytes(), 32)
					case sel("getFallbackSignerData()"):
						value = append(common.LeftPadBytes(r.owner.Bytes(), 32), make([]byte, 32)...)
					case sel("entryPoint()"):
						value = common.LeftPadBytes(preset.EntryPointV07().Bytes(), 32)
					case sel("getNonce(address,uint192)"):
						full := new(big.Int).Lsh(new(big.Int).SetBytes(data[36:68]), 64)
						if mined.Load() {
							full.Add(full, big.NewInt(1))
						}
						value = common.LeftPadBytes(full.Bytes(), 32)
					case sel("getValidationData(bytes24)"):
						for _, n := range []int64{32, 0, 128, 160, 192, 0, 0, 0} {
							value = append(value, common.LeftPadBytes(big.NewInt(n).Bytes(), 32)...)
						}
					case sel("erc20SpendLimits(uint32,address,address)"):
						value = common.LeftPadBytes(big.NewInt(1000).Bytes(), 32)
					case sel("signers(uint32,address)"):
						value = make([]byte, 32)
					case sel("balanceOf(address)"):
						value = make([]byte, 32)
					case sel("allowance(address,address)"):
						value = make([]byte, 32)
						if common.BytesToAddress(data[36:]) == spender && !mined.Load() {
							value = common.LeftPadBytes(big.NewInt(123).Bytes(), 32)
						}
					default:
						t.Errorf("unexpected budget HTTP eth_call selector %s", selector)
					}
					result = hexutil.Encode(value)
				case "eth_estimateUserOperationGas":
					estimates.Add(1)
					result = map[string]string{"callGasLimit": "0x186a0", "verificationGasLimit": "0xc350", "preVerificationGas": "0x7530"}
				case "eth_getLogs":
					result = []types.Log{}
					if mined.Load() && strings.Contains(strings.ToLower(string(rpcReq.Params[0])), strings.ToLower(preset.EntryPointV07().Hex())) {
						nonce := new(big.Int).Lsh(big.NewInt(1), 64)
						data := common.LeftPadBytes(nonce.Bytes(), 32)
						for _, n := range []int64{1, 1000, 1000} {
							data = append(data, common.LeftPadBytes(big.NewInt(n).Bytes(), 32)...)
						}
						result = []types.Log{{Address: preset.EntryPointV07(), Topics: []common.Hash{crypto.Keccak256Hash([]byte("UserOperationEvent(bytes32,address,address,uint256,bool,uint256,uint256)")), submittedHash.Load().(common.Hash), common.BytesToHash(r.wallet.Bytes()), {}}, Data: data, BlockNumber: 2, BlockHash: header(2).Hash(), TxHash: common.HexToHash("0xabc")}}
					}
				case "eth_sendUserOperation":
					sends.Add(1)
					require.True(t, sendEnabled.Load())
					result = submittedHash.Load().(common.Hash).Hex()
				case "alchemy_requestGasAndPaymasterAndData":
					sends.Add(1)
					t.Errorf("budget preparation must not send or sponsor")
					result = nil
				default:
					t.Errorf("unexpected budget HTTP RPC %s", rpcReq.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": rpcReq.ID, "result": result}))
			}))
			defer chain.Close()
			cfg.SmartWallet.EthRpcUrl = chain.URL
			cfg.SmartWallet.BundlerURL = chain.URL
			cfg.SmartWallet.BundlerProvider = "self_hosted"
			cfg.SmartWallet.DisableGasSponsorship = false
			cfg.SmartWallet.AlchemyPaymasterPolicyID = "must-not-use"
			client, err := ethclient.Dial(chain.URL)
			require.NoError(t, err)
			defer client.Close()
			previous := taskengine.GetChainStateReaderForChain(uint64(policyTestChain))
			taskengine.RegisterChainStateReader(uint64(policyTestChain), taskengine.NewDirectChainStateReader(client, policyTestChain))
			defer func() {
				if previous != nil {
					taskengine.RegisterChainStateReader(uint64(policyTestChain), previous)
				} else {
					taskengine.ClearChainStateReaderRegistry()
				}
				preset.SetSessionResolver(nil)
				preset.SetEOA7702AccountLookup(nil)
			}()
			r.server.engine.InstallSessionResolver()
			e := echo.New()
			e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					c.Set("auth.user", &restmw.AuthenticatedUser{Subject: r.owner.Hex(), ChainID: policyTestChain})
					return next(c)
				}
			})
			r.server.Mount(e)
			call := func(method, path string, body interface{}, want int) map[string]interface{} {
				t.Helper()
				data, marshalErr := json.Marshal(body)
				require.NoError(t, marshalErr)
				req := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(data))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				require.Equal(t, want, rec.Code, rec.Body.String())
				var out map[string]interface{}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
				return out
			}
			d := &taskengine.BudgetApprovalDiscovery{Owner: r.owner, Wallet: r.wallet, ChainID: policyTestChain, Source: "moralis", FetchedAt: time.Now().UnixMilli(), Complete: true, AllowedContracts: []common.Address{token, spender}, Candidates: []taskengine.BudgetApprovalPair{{Token: token, Spender: spender}}}
			hash, err := taskengine.BudgetDiscoveryDigest(*d)
			require.NoError(t, err)
			sig, err := crypto.Sign(hash.Bytes(), service)
			require.NoError(t, err)
			sig[64] += 27
			body := map[string]interface{}{"chainId": policyTestChain, "agentLabel": "Budget HTTP", "budget": map[string]interface{}{"validUntil": time.Now().Add(time.Hour).UnixMilli(), "erc20": []interface{}{map[string]interface{}{"token": token.Hex(), "amount": "1000"}}}, "add": map[string]interface{}{"allowedActions": []interface{}{map[string]interface{}{"target": token.Hex(), "selectors": []string{"0x095ea7b3"}}, map[string]interface{}{"target": spender.Hex(), "selectors": []string{"0x04e45aaf"}}}}, "approvalDiscovery": map[string]interface{}{"owner": r.owner.Hex(), "wallet": r.wallet.Hex(), "chainId": policyTestChain, "source": "moralis", "fetchedAt": d.FetchedAt, "complete": true, "candidates": d.Candidates, "allowedContracts": d.AllowedContracts, "signature": hexutil.Encode(sig)}}
			path := "/wallets/" + r.wallet.Hex() + "/policies"
			out := call(http.MethodPost, path+":prepare", body, 200)
			require.NotContains(t, out, "typedData")
			require.NotContains(t, out, "digest")
			id := out["preparationId"].(string)
			signing := out["signing"].(map[string]interface{})
			require.Equal(t, "owner_operation", signing["kind"])
			require.Equal(t, "personal_sign", signing["signingMethod"])
			require.NotEmpty(t, signing["maxFeeWei"])
			require.Equal(t, []interface{}{map[string]interface{}{"token": token.Hex(), "spender": spender.Hex(), "amount": "123"}}, out["approvalResets"])
			operation := signing["userOp"].(map[string]interface{})
			if deployed {
				require.NotContains(t, operation, "factory")
			} else {
				require.NotEmpty(t, operation["factory"])
				require.NotEmpty(t, operation["factoryData"])
			}
			stored := call(http.MethodGet, path+"/preparations/"+id, nil, 200)
			require.Equal(t, out, stored)
			require.Positive(t, estimates.Load())
			// Route shim must reach our strict signature-only endpoint; not a 404 or generic owner relay.
			refusal := call(http.MethodPost, path+"/preparations/"+id+":submit", map[string]interface{}{"signature": "0x" + strings.Repeat("11", 65)}, 409)
			require.Equal(t, taskengine.BudgetHandoffRequiredCode, refusal["code"])
			call(http.MethodPost, path+"/preparations/"+id+":submit", map[string]interface{}{"signature": "0x" + strings.Repeat("11", 65), "userOp": operation}, 400)
			for _, malformed := range []string{strings.Repeat("11", 65), "0x" + strings.Repeat("11", 65) + "zz", "0x1", "0x"} {
				call(http.MethodPost, path+"/preparations/"+id+":submit", map[string]interface{}{"signature": malformed}, 400)
			}
			for _, query := range []string{"not-a-chain", "0", "-1", "999999999999999999999999999999"} {
				call(http.MethodPost, path+"/preparations/"+id+":submit?chainId="+query, map[string]interface{}{"signature": "0x" + strings.Repeat("11", 65)}, 400)
				call(http.MethodPost, path+"/preparations/"+id+":cancel?chainId="+query, nil, 400)
			}
			policies, err := taskengine.ListSessionPolicies(r.db, policyTestChain, r.owner)
			require.NoError(t, err)
			require.Empty(t, policies)
			require.Zero(t, sends.Load())
			// Empty wallets are quoted but receive a structured self-funding refusal.
			zeroBalance.Store(true)
			fee := call(http.MethodPost, path+":prepare", body, 409)
			require.Equal(t, taskengine.BudgetFeeInsufficientCode, fee["code"])
			require.NotEmpty(t, fee["neededWei"])
			require.Equal(t, "0", fee["availableWei"])
			require.Zero(t, sends.Load())
			zeroBalance.Store(false)
			cfg.WalletBudgetSigningEnabled = true
			sendEnabled.Store(true)
			userHash := common.HexToHash(signing["userOpHash"].(string))
			submittedHash.Store(userHash)
			ownerSignature, err := crypto.Sign(accounts.TextHash(userHash.Bytes()), r.ownerKey)
			require.NoError(t, err)
			sent := call(http.MethodPost, path+"/preparations/"+id+":submit?chainId=11155111", map[string]interface{}{"signature": hexutil.Encode(ownerSignature)}, 200)
			require.Equal(t, "submitted", sent["state"])
			require.Positive(t, pinnedOwnerReads.Load())
			require.EqualValues(t, 1, sends.Load())
			pending := call(http.MethodGet, path+"/preparations/"+id+"?chainId=11155111", nil, 200)
			require.Equal(t, "submitted", pending["state"])
			mined.Store(true)
			active := call(http.MethodGet, path+"/preparations/"+id+"?chainId=11155111", nil, 200)
			require.Equal(t, "active", active["state"])
			call(http.MethodPost, path+"/preparations/"+id+":submit?chainId=11155111", map[string]interface{}{"signature": hexutil.Encode(ownerSignature)}, 200)
			require.EqualValues(t, 1, sends.Load())
		})
	}
}

func TestBudgetPreparationRoutesRequireUserAndRefusePartner(t *testing.T) {
	r := newPolicyRig(t)
	addr := r.wallet.Hex()
	handler := func(c echo.Context) error {
		return r.server.SubmitWalletBudgetPreparation(c, addr, "01test", generated.SubmitWalletBudgetPreparationParams{})
	}
	rec := r.call(t, map[string]string{"signature": "0x" + strings.Repeat("11", 65)}, handler, map[string]string{"X-Partner-Assertion": "not-authority"})
	require.Equal(t, http.StatusForbidden, rec.Code)
	// Newly reserved operations are user-only in the shared permission map.
	require.Equal(t, LevelUserRefusePartner, permissionMap[OpGetWalletBudgetPreparation])
}
