package taskengine

import (
	"context"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

// TestUserOpAtomicBatch_Sepolia is the end-to-end proof for G4: an on-demand (nodes:run) multi-call
// contractWrite with heterogeneous per-call targets is submitted as ONE atomic UserOp that composes
// with live paymaster reimbursement, and both sub-calls land in a single transaction.
//
// The batch is two ERC-20 approve()s on DIFFERENT tokens (USDC + WETH) to the SwapRouter — a faithful
// stand-in for the approve+swap shape that exercises every piece under test (multi-call → one
// executeBatch, per-call contract_address routing, and executeBatch → executeBatchWithValues +
// reimbursement wrapping) while staying cheap and repeatable: approve needs no token balance and no
// slippage. Both allowances are set to a fresh per-run amount and verified on-chain, so a stale
// allowance can't produce a false pass — if either allowance matches, both sub-calls executed
// atomically in the one UserOp.
//
// Uses the same live infra + key as the Sepolia withdraw test (OWNER_EOA + controller key from .env);
// paymaster sponsors gas and the wallet reimburses, so the wallet needs a little ETH.
func TestUserOpAtomicBatch_Sepolia(t *testing.T) {
	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
	if err != nil {
		cfg, err = config.NewConfig("../../config/test.yaml")
		if err != nil {
			t.Skipf("Failed to load test.yaml: %v", err)
		}
	}

	// Confirm we're actually connected to Sepolia before spending testnet funds.
	tempClient, err := ethclient.Dial(cfg.SmartWallet.EthRpcUrl)
	if err != nil {
		t.Skipf("Cannot connect to RPC: %v", err)
	}
	chainID, err := tempClient.ChainID(context.Background())
	tempClient.Close()
	if err != nil {
		t.Skipf("Cannot get chain ID from RPC: %v", err)
	}
	const sepoliaChainID = int64(11155111)
	if chainID.Int64() != sepoliaChainID {
		t.Skipf("Test requires Sepolia (current chain ID: %d)", chainID.Int64())
	}

	ownerEOAHex := os.Getenv("OWNER_EOA")
	if ownerEOAHex == "" {
		t.Skip("OWNER_EOA environment variable not set")
	}
	ownerAddress := common.HexToAddress(ownerEOAHex)

	client, err := ethclient.Dial(cfg.SmartWallet.EthRpcUrl)
	require.NoError(t, err, "Failed to connect to RPC")
	t.Cleanup(func() { client.Close() })

	aa.SetFactoryAddress(cfg.SmartWallet.FactoryAddress)

	smartWalletAddress, err := aa.GetSenderAddress(client, ownerAddress, big.NewInt(0))
	require.NoError(t, err, "Failed to derive smart wallet address")
	t.Logf("🔑 Owner EOA: %s", ownerAddress.Hex())
	t.Logf("💼 Smart Wallet (salt:0): %s", smartWalletAddress.Hex())

	// Ensure the wallet is deployed (executeBatch is called on the deployed account).
	code, err := client.CodeAt(context.Background(), *smartWalletAddress, nil)
	require.NoError(t, err, "Failed to check wallet deployment status")
	if len(code) == 0 {
		t.Logf("⚠️  Wallet not deployed, deploying it first...")
		err = testutil.EnsureWalletDeployed(client, cfg.SmartWallet.FactoryAddress, ownerAddress, big.NewInt(0), testutil.GetTestControllerPrivateKey())
		require.NoError(t, err, "Failed to deploy wallet (controller needs funds to deploy)")
		t.Logf("✅ Wallet deployed")
	}

	// The wallet reimburses the paymaster for gas, so it needs a little ETH.
	balance, err := client.BalanceAt(context.Background(), *smartWalletAddress, nil)
	require.NoError(t, err, "Failed to get wallet balance")
	t.Logf("💰 Smart Wallet ETH: %s wei", balance.String())
	if balance.Sign() == 0 {
		t.Skip("Smart wallet has 0 ETH — fund it so it can reimburse paymaster gas")
	}

	router := common.HexToAddress(SEPOLIA_SWAPROUTER)
	usdc := common.HexToAddress(SEPOLIA_USDC)
	weth := common.HexToAddress(SEPOLIA_WETH)

	// Fresh, unique per-run approval amount so the post-execution allowance check can't pass on a
	// stale allowance from a previous run.
	approveAmount := big.NewInt(time.Now().Unix())
	t.Logf("🎯 Atomic batch: approve(%s)@USDC + approve(%s)@WETH, amount=%s", router.Hex(), router.Hex(), approveAmount.String())

	// Minimal ERC-20 ABI (approve + allowance) reused for both the node's methodCalls and the
	// on-chain verification below.
	erc20ABIJSON := `[
		{"inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"name":"approve","outputs":[{"name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"},
		{"inputs":[{"name":"owner","type":"address"},{"name":"spender","type":"address"}],"name":"allowance","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"}
	]`
	approveABI := []interface{}{
		map[string]interface{}{
			"inputs": []interface{}{
				map[string]interface{}{"name": "spender", "type": "address"},
				map[string]interface{}{"name": "amount", "type": "uint256"},
			},
			"name":            "approve",
			"outputs":         []interface{}{map[string]interface{}{"name": "", "type": "bool"}},
			"stateMutability": "nonpayable",
			"type":            "function",
		},
	}

	// One node, two method calls on two different tokens. The first call omits contractAddress and
	// falls back to the node-level target (USDC); the second overrides it to WETH — exercising both
	// the fallback and the per-call override in the same batch.
	batchConfig := map[string]interface{}{
		"contractAddress": usdc.Hex(),
		"contractAbi":     approveABI,
		"chainId":         sepoliaChainID,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName":   "approve",
				"methodParams": []interface{}{router.Hex(), approveAmount.String()},
			},
			map[string]interface{}{
				"methodName":      "approve",
				"methodParams":    []interface{}{router.Hex(), approveAmount.String()},
				"contractAddress": weth.Hex(),
			},
		},
	}

	settings := map[string]interface{}{
		"runner":      smartWalletAddress.Hex(),
		"smartWallet": smartWalletAddress.Hex(),
		"chain_id":    sepoliaChainID,
	}
	inputVars := map[string]interface{}{"settings": settings}

	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
	engine := New(db, cfg, nil, testutil.GetLogger())
	t.Cleanup(func() { engine.Stop() })

	user := &model.User{Address: ownerAddress, SmartAccountAddress: smartWalletAddress}
	require.NoError(t, StoreWallet(db, int64(1), ownerAddress, &model.SmartWallet{
		Owner:   &ownerAddress,
		Address: smartWalletAddress,
		Salt:    big.NewInt(0),
	}), "Failed to store wallet")

	// Real execution (is_simulated=false) with paymaster — this is the composed path:
	// executeBatch(approve, approve) wrapped by the reimbursement wrapper into executeBatchWithValues.
	usePaymaster := true
	t.Logf("🚀 Submitting atomic batch as one UserOp (real, paymaster-sponsored)...")
	result, err := engine.RunNodeImmediately("contractWrite", batchConfig, inputVars, user, false, &usePaymaster)
	require.NoError(t, err, "batch RunNodeImmediately should not error")
	require.NotNil(t, result, "batch result should not be nil")

	if successVal, ok := result["success"].(bool); ok && !successVal {
		t.Fatalf("❌ atomic batch execution failed: %v", result["error"])
	}
	t.Logf("✅ Batch reported success; result keys: %v", mapKeys(result))

	// Ground truth: read the on-chain allowances the batch was supposed to set. Both must equal the
	// fresh per-run amount, proving BOTH sub-calls landed — atomically, in the one UserOp.
	parsedERC20, err := abi.JSON(strings.NewReader(erc20ABIJSON))
	require.NoError(t, err, "Failed to parse ERC-20 ABI")

	readAllowance := func(token common.Address) *big.Int {
		c := bind.NewBoundContract(token, parsedERC20, client, client, client)
		var out []interface{}
		require.NoError(t, c.Call(nil, &out, "allowance", *smartWalletAddress, router), "allowance() call failed for %s", token.Hex())
		require.Len(t, out, 1)
		v, ok := out[0].(*big.Int)
		require.True(t, ok, "allowance is not a *big.Int")
		return v
	}

	usdcAllowance := readAllowance(usdc)
	wethAllowance := readAllowance(weth)
	t.Logf("🔎 On-chain allowances after batch — USDC→router: %s, WETH→router: %s", usdcAllowance, wethAllowance)

	require.Equal(t, approveAmount.String(), usdcAllowance.String(), "USDC allowance must equal the batched approve amount")
	require.Equal(t, approveAmount.String(), wethAllowance.String(), "WETH allowance must equal the batched approve amount")

	t.Logf("🎉 Atomic approve+approve batch landed on-chain with live paymaster reimbursement")
}

func mapKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
