package preset

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"
)

func TestOwnerSignatureIsRawHashPersonalSign(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	owner := crypto.PubkeyToAddress(key.PublicKey)
	hash := crypto.Keccak256Hash([]byte("prepared operation"))
	sig, err := crypto.Sign(accounts.TextHash(hash.Bytes()), key)
	require.NoError(t, err)
	original := bytes.Clone(sig)
	framed, err := FrameOwnerSignature(hash, sig, owner)
	require.NoError(t, err)
	require.Equal(t, original, sig, "normalization must not mutate the caller signature")
	require.Equal(t, []byte{0xff, 0}, framed[:2])
	require.Equal(t, original[64]+27, framed[66])
	sig[64] += 27
	alreadyNormalized, err := FrameOwnerSignature(hash, sig, owner)
	require.NoError(t, err)
	require.Equal(t, framed, alreadyNormalized)

	for name, digest := range map[string][]byte{
		"hex text":          accounts.TextHash([]byte(hash.Hex())),
		"double prefix":     accounts.TextHash(accounts.TextHash(hash.Bytes())),
		"bare hash":         hash.Bytes(),
		"another operation": accounts.TextHash(crypto.Keccak256([]byte("changed operation"))),
	} {
		t.Run(name, func(t *testing.T) {
			wrong, err := crypto.Sign(digest, key)
			require.NoError(t, err)
			_, err = FrameOwnerSignature(hash, wrong, owner)
			require.Error(t, err)
		})
	}
	for _, invalid := range [][]byte{nil, make([]byte, 64), framed, append(bytes.Clone(sig[:64]), byte(2))} {
		_, err := FrameOwnerSignature(hash, invalid, owner)
		require.Error(t, err)
	}
	_, err = FrameOwnerSignature(hash, sig, common.HexToAddress("0x1111111111111111111111111111111111111111"))
	require.Error(t, err)
	highS := bytes.Clone(original)
	new(big.Int).Sub(crypto.S256().Params().N, new(big.Int).SetBytes(highS[32:64])).FillBytes(highS[32:64])
	highS[64] ^= 1
	_, err = FrameOwnerSignature(hash, highS, owner)
	require.ErrorContains(t, err, "canonical")
}

type ownerRPCFixture struct {
	sender, owner, implementation                 common.Address
	chainID                                       int64
	deployed, disabled, changedImpl, ownerHasCode bool
	ownerCode, delegateCode                       []byte
	fallbackOwner                                 *common.Address
	wrongEntry                                    bool
	factoryReads                                  int
	nonce                                         *big.Int
	balance                                       string
	sendError                                     string
	sendCount                                     int
	expectedHash                                  string
	sent                                          map[string]interface{}
	sponsorRefused                                bool
}

func newOwnerRPCFixture(t *testing.T) (*ownerRPCFixture, *config.SmartWalletConfig) {
	t.Helper()
	nonce, err := userop.EncodeNonceMAv2(0, userop.ValidationOptionGlobal, 0)
	require.NoError(t, err)
	f := &ownerRPCFixture{
		sender:         common.HexToAddress("0x2222222222222222222222222222222222222222"),
		owner:          common.HexToAddress("0x1111111111111111111111111111111111111111"),
		implementation: common.HexToAddress("0x000000000000c5A9089039570Dd36455b5C07383"),
		chainID:        11155111, deployed: true, nonce: nonce, balance: "0xde0b6b3a7640000",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		var result interface{}
		var rpcError string
		switch req.Method {
		case "eth_chainId":
			result = hexutil.EncodeBig(big.NewInt(f.chainID))
		case "eth_getCode":
			var address common.Address
			require.NoError(t, json.Unmarshal(req.Params[0], &address))
			switch address {
			case f.owner:
				result = "0x"
				if f.ownerCode != nil {
					result = hexutil.Encode(f.ownerCode)
				} else if f.ownerHasCode {
					result = "0xef01000000000000000000000000000000000000000001"
				}
			case f.sender:
				result = "0x"
				if f.deployed {
					result = "0x6000"
				}
			default:
				result = "0x6001"
				if address == config.SMA7702Delegate() && f.delegateCode != nil {
					result = hexutil.Encode(f.delegateCode)
				}
			}
		case "eth_getStorageAt":
			address := f.implementation
			if f.changedImpl {
				address = common.HexToAddress("0x4444444444444444444444444444444444444444")
			}
			result = hexutil.Encode(common.LeftPadBytes(address.Bytes(), 32))
		case "eth_getBalance":
			result = f.balance
		case "eth_maxPriorityFeePerGas", "rundler_maxPriorityFeePerGas":
			result = "0x3b9aca00"
		case "eth_getBlockByNumber":
			result = &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(0), GasLimit: 30000000, BaseFee: big.NewInt(1), Extra: []byte{}}
		case "eth_call":
			var call map[string]string
			require.NoError(t, json.Unmarshal(req.Params[0], &call))
			data := call["data"]
			if data == "" {
				data = call["input"]
			}
			selector := data[:10]
			switch selector {
			case hexutil.Encode(crypto.Keccak256([]byte("getAddressSemiModular(address,uint256)"))[:4]):
				f.factoryReads++
				result = hexutil.Encode(common.LeftPadBytes(f.sender.Bytes(), 32))
			case hexutil.Encode(crypto.Keccak256([]byte("SEMI_MODULAR_ACCOUNT_IMPL()"))[:4]):
				result = hexutil.Encode(common.LeftPadBytes(f.implementation.Bytes(), 32))
			case hexutil.Encode(crypto.Keccak256([]byte("getFallbackSignerData()"))[:4]):
				fallback := f.owner
				if f.fallbackOwner != nil {
					fallback = *f.fallbackOwner
				}
				out := append(common.LeftPadBytes(fallback.Bytes(), 32), make([]byte, 32)...)
				if f.disabled {
					out[63] = 1
				}
				result = hexutil.Encode(out)
			case hexutil.Encode(crypto.Keccak256([]byte("entryPoint()"))[:4]):
				entry := EntryPointV07()
				if f.wrongEntry {
					entry = common.Address{}
				}
				result = hexutil.Encode(common.LeftPadBytes(entry.Bytes(), 32))
			case selectorGetNonce:
				// Pin the full key passed to EntryPoint, not just the sequence.
				decoded := common.FromHex(data)
				require.Equal(t, big.NewInt(1), new(big.Int).SetBytes(decoded[36:68]))
				result = hexutil.Encode(common.LeftPadBytes(f.nonce.Bytes(), 32))
			case hexutil.Encode(crypto.Keccak256([]byte("balanceOf(address)"))[:4]):
				result = hexutil.Encode(make([]byte, 32))
			default:
				t.Errorf("unexpected eth_call selector %s", selector)
			}
		case "eth_estimateUserOperationGas":
			var operation map[string]interface{}
			require.NoError(t, json.Unmarshal(req.Params[0], &operation))
			for _, field := range []string{"callGasLimit", "verificationGasLimit", "preVerificationGas"} {
				require.Equal(t, "0x0", operation[field], "owner gas must be computed before signing")
			}
			result = map[string]string{"callGasLimit": "0x186a0", "verificationGasLimit": "0xc350", "preVerificationGas": "0x7530"}
		case "alchemy_requestGasAndPaymasterAndData":
			if f.sponsorRefused {
				rpcError = "sponsorship refused"
				break
			}
			result = map[string]string{
				"paymaster": "0x5555555555555555555555555555555555555555", "paymasterData": "0x1234",
				"paymasterVerificationGasLimit": "0x7530", "paymasterPostOpGasLimit": "0x2710",
				"callGasLimit": "0x186a0", "verificationGasLimit": "0xc350", "preVerificationGas": "0x7530",
				"maxFeePerGas": "0x3b9aca00", "maxPriorityFeePerGas": "0x3b9aca00",
			}
		case "eth_sendUserOperation":
			f.sendCount++
			require.NoError(t, json.Unmarshal(req.Params[0], &f.sent))
			result, rpcError = f.expectedHash, f.sendError
		default:
			t.Errorf("unexpected RPC method %s", req.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		response := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result}
		if rpcError != "" {
			delete(response, "result")
			response["error"] = map[string]interface{}{"code": -32602, "message": rpcError}
		}
		require.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	t.Cleanup(server.Close)
	return f, &config.SmartWalletConfig{ChainID: 11155111, EthRpcUrl: server.URL, BundlerProvider: config.BundlerProviderSelfHosted, BundlerURL: server.URL, DisableGasSponsorship: true}
}

func ownerTestCalls() []aa.Call {
	return []aa.Call{
		{Target: common.HexToAddress("0x6666666666666666666666666666666666666666"), Value: big.NewInt(0), Data: common.FromHex("0xa9059cbb")},
		{Target: common.HexToAddress("0x7777777777777777777777777777777777777777"), Value: big.NewInt(0), Data: common.FromHex("0xa9059cbb")},
	}
}

func TestOwnerPrepareAndKeylessRelayKeepPricedFields(t *testing.T) {
	f, sw := newOwnerRPCFixture(t)
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	f.owner = crypto.PubkeyToAddress(key.PublicKey)
	prepared, err := PrepareOwnerUserOp(context.Background(), sw, f.owner, f.sender, big.NewInt(0), ownerTestCalls(), nil)
	require.NoError(t, err)
	op := prepared.Operation
	require.Empty(t, op.Signature)
	require.Nil(t, op.Factory)
	require.Equal(t, f.nonce, op.Nonce)
	_, _, data, err := aa.UnpackExecuteCalldata(op.CallData)
	require.NoError(t, err)
	require.Len(t, data, 2)
	hash, err := op.GetUserOpHash(EntryPointV07(), big.NewInt(sw.ChainID))
	require.NoError(t, err)
	f.expectedHash = hash.Hex()
	raw, err := crypto.Sign(accounts.TextHash(hash.Bytes()), key)
	require.NoError(t, err)
	op.Signature, err = FrameOwnerSignature(hash, raw, f.owner)
	require.NoError(t, err)
	before, err := json.Marshal(op)
	require.NoError(t, err)
	require.NoError(t, ValidateOwnerUserOp(context.Background(), sw, f.owner, big.NewInt(0), prepared))
	gotHash, err := SubmitOwnerUserOp(context.Background(), sw, op)
	require.NoError(t, err)
	require.Equal(t, hash, gotHash)
	var expected map[string]interface{}
	require.NoError(t, json.Unmarshal(before, &expected))
	require.Equal(t, expected, f.sent, "relay must send the exact reviewed operation plus signature")
	f.sendError = "Verification gas limit efficiency too low. Required: 0.4, Actual: 0.1"
	_, err = SubmitOwnerUserOp(context.Background(), sw, op)
	require.ErrorContains(t, err, "efficiency")
	require.Equal(t, 2, f.sendCount, "gas rejection must not cause a hidden retry")
	after, err := json.Marshal(op)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
}

func TestOwnerPrepareCompatibilityAndFundingGuards(t *testing.T) {
	for _, scenario := range []string{"undeployed", "insufficient prefund", "disabled owner", "different signer", "changed implementation", "wrong chain", "delegated owner", "wrong nonce key", "native value"} {
		t.Run(scenario, func(t *testing.T) {
			f, sw := newOwnerRPCFixture(t)
			owner := f.owner
			calls := ownerTestCalls()
			switch scenario {
			case "undeployed":
				f.deployed = false
			case "insufficient prefund":
				f.balance = "0x1"
			case "disabled owner":
				f.disabled = true
			case "different signer":
				owner = common.HexToAddress("0x8888888888888888888888888888888888888888")
			case "changed implementation":
				f.changedImpl = true
			case "wrong chain":
				f.chainID = 8453
			case "delegated owner":
				f.ownerHasCode = true
			case "wrong nonce key":
				f.nonce = big.NewInt(0)
			case "native value":
				calls[0].Value = big.NewInt(1)
			}
			prepared, err := PrepareOwnerUserOp(context.Background(), sw, owner, f.sender, big.NewInt(0), calls, nil)
			if scenario == "undeployed" {
				require.NoError(t, err)
				require.NotNil(t, prepared.Operation.Factory)
				require.Equal(t, aa.MAv2FactoryAddress(), *prepared.Operation.Factory)
			} else {
				require.Error(t, err)
			}
			require.Zero(t, f.sendCount)
		})
	}
}

func TestOwnerRecheckRefusesNonceOrAccountChanges(t *testing.T) {
	for _, change := range []string{"nonce", "owner", "implementation", "factory implementation", "deployment", "funding"} {
		t.Run(change, func(t *testing.T) {
			f, sw := newOwnerRPCFixture(t)
			owner := f.owner
			prepared, err := PrepareOwnerUserOp(context.Background(), sw, owner, f.sender, big.NewInt(0), ownerTestCalls(), nil)
			require.NoError(t, err)
			switch change {
			case "nonce":
				f.nonce = new(big.Int).Add(f.nonce, big.NewInt(1))
			case "owner":
				f.disabled = true
			case "implementation":
				f.changedImpl = true
			case "factory implementation":
				f.implementation = common.HexToAddress("0x3333333333333333333333333333333333333333")
			case "deployment":
				f.deployed = false
			case "funding":
				f.balance = "0x0"
			}
			require.Error(t, ValidateOwnerUserOp(context.Background(), sw, owner, big.NewInt(0), prepared))
			require.Zero(t, f.sendCount)
		})
	}
}

func TestOwnerPrepareRefusesUnverifiedFactoryImplementation(t *testing.T) {
	for _, deployed := range []bool{true, false} {
		name := "deployed"
		if !deployed {
			name = "counterfactual"
		}
		t.Run(name, func(t *testing.T) {
			f, sw := newOwnerRPCFixture(t)
			f.deployed = deployed
			// The factory and proxy agree, but a different implementation may
			// validate a different digest. Even a fresh prepare must refuse it.
			f.implementation = common.HexToAddress("0x3333333333333333333333333333333333333333")
			prepared, err := PrepareOwnerUserOp(context.Background(), sw, f.owner, f.sender, big.NewInt(0), ownerTestCalls(), nil)
			require.ErrorContains(t, err, "not verified for owner personal_sign")
			require.Nil(t, prepared)
			require.Zero(t, f.sendCount)
		})
	}
}

func TestOwnerSponsorshipIsFinalizedBeforeSigning(t *testing.T) {
	f, sw := newOwnerRPCFixture(t)
	sw.DisableGasSponsorship = false
	sw.AlchemyPaymasterPolicyID = "test-policy"
	sw.BundlerProvider = config.BundlerProviderAlchemy
	f.balance = "0x0"
	// Exercise the same pricing function with a local RPC client; no live
	// Alchemy endpoint or paymaster policy is contacted by this unit test.
	client, err := rpc.DialContext(context.Background(), sw.EthRpcUrl)
	require.NoError(t, err)
	defer client.Close()
	chain := ethclient.NewClient(client)
	op := testOp()
	op.Sender, op.Nonce = f.sender, f.nonce
	err = priceOperationV07(context.Background(), chain, client, op, EntryPointV07(), sw, logger.EnsureLogger(nil))
	require.NoError(t, err)
	require.NoError(t, checkOwnerPrefund(context.Background(), chain, op))
	require.NotNil(t, op.Paymaster)
	require.Empty(t, op.Signature)
	hash, err := op.GetUserOpHash(EntryPointV07(), big.NewInt(sw.ChainID))
	require.NoError(t, err)
	op.PaymasterData = []byte{0x12, 0x35}
	changed, err := op.GetUserOpHash(EntryPointV07(), big.NewInt(sw.ChainID))
	require.NoError(t, err)
	require.NotEqual(t, hash, changed)
	f.sponsorRefused = true
	err = priceOperationV07(context.Background(), chain, client, testOp(), EntryPointV07(), sw, logger.EnsureLogger(nil))
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "sponsor"))
	require.Zero(t, f.sendCount)
}
