//go:build integration
// +build integration

package taskengine

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// Live Sepolia proofs for Track A native ETH (spec L1–L11).
// On-demand, not per-PR CI. Self-funded; no Gas Manager.
//
//	OWNER_EOA, TEST_PRIVATE_KEY, config/test.yaml
//	go test -tags=integration ./core/taskengine -run 'TestNativeETHSessionGrant_Sepolia|TestNativeTokenLimitModuleBytecode' -v -count=1
//
// L1–L7 and L11 are release-blocking. L10 also checks Base when BASE_RPC_URL is set.

var nativeLiveValidUntilMs = time.Now().Add(7 * 24 * time.Hour).UnixMilli()

const (
	nativeLiveSendWei = "1000000000000"     // 1e12 — L1 amount
	nativeLiveCapWei  = "30000000000000000" // 0.03 ETH — covers gas + send
	nativeLiveTinyCap = "1000000000000"     // 1e12 — L3: amount==cap still fails (gas)
)

type nativeLiveEnv struct {
	t        *testing.T
	cfg      *config.Config
	client   *ethclient.Client
	engine   *Engine
	user     *model.User
	owner    common.Address
	ownerKey *ecdsa.PrivateKey
	runner   common.Address
	db       storage.Storage
	chainID  int64
}

func TestNativeTokenLimitModuleBytecode_SepoliaAndBase(t *testing.T) {
	// L10 — cheap eth_getCode, no owner key, no ETH.
	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
	require.NoError(t, err)
	sepolia, err := ethclient.Dial(cfg.SmartWallet.EthRpcUrl)
	require.NoError(t, err)
	t.Cleanup(func() { sepolia.Close() })
	requireNTModuleCode(t, sepolia, "Sepolia")

	baseURL := strings.TrimSpace(os.Getenv("BASE_RPC_URL"))
	if baseURL == "" {
		t.Skip("L10 Base: set BASE_RPC_URL to check NativeTokenLimitModule on 8453")
	}
	base, err := ethclient.Dial(baseURL)
	require.NoError(t, err)
	t.Cleanup(func() { base.Close() })
	id, err := base.ChainID(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(8453), id.Int64(), "BASE_RPC_URL must be Base mainnet")
	requireNTModuleCode(t, base, "Base")
}

func requireNTModuleCode(t *testing.T, client *ethclient.Client, label string) {
	t.Helper()
	mod := aa.NativeTokenLimitModuleAddress()
	code, err := client.CodeAt(context.Background(), mod, nil)
	require.NoError(t, err)
	require.NotEmpty(t, code, "L10: NativeTokenLimitModule missing at %s on %s", mod.Hex(), label)
	t.Logf("L10 %s NT module %s code=%d bytes", label, mod.Hex(), len(code))
}

func TestNativeETHSessionGrant_Sepolia(t *testing.T) {
	env := setupNativeLive(t)

	t.Run("L1_listed_ethTransfer", func(t *testing.T) {
		env.t = t
		perms := nativeSendPerms(env.owner, nativeLiveCapWei)
		submitNativeGrant(env, perms)
		_, err := runETHTransfer(env, env.owner.Hex(), nativeLiveSendWei)
		require.NoError(t, err, "L1: listed recipient must succeed")
		t.Log("L1 PASS: ethTransfer to listed recipient")
	})

	t.Run("L2_unlisted_recipient", func(t *testing.T) {
		env.t = t
		bob := common.HexToAddress("0x000000000000000000000000000000000000b0b0")
		_, err := runETHTransfer(env, bob.Hex(), nativeLiveSendWei)
		require.Error(t, err, "L2: unlisted recipient must fail")
		msg := err.Error()
		require.True(t,
			strings.Contains(msg, SessionPolicyRecipientNotAllowedCode) ||
				strings.Contains(msg, "AddressNotAllowed"),
			"L2 want RECIPIENT_NOT_ALLOWED or AddressNotAllowed, got %s", msg)
		t.Log("L2 PASS: unlisted recipient refused")
	})

	t.Run("L11_self_admin", func(t *testing.T) {
		env.t = t
		requireNativeSelfAdminBlocked(env)
		t.Log("L11 PASS: session key cannot installValidation or updateLimits")
	})

	t.Run("L9_1271_off", func(t *testing.T) {
		env.t = t
		require1271Denied(env)
		t.Log("L9 PASS: isValidSignature reverts")
	})

	t.Run("L3_cap", func(t *testing.T) {
		env.t = t
		submitNativeGrant(env, nativeSendPerms(env.owner, nativeLiveTinyCap))
		_, err := runETHTransfer(env, env.owner.Hex(), "1000000000001")
		require.Error(t, err, "L3: amount > cap must fail")
		require.True(t,
			strings.Contains(err.Error(), SessionPolicyNativeCapExceededCode) ||
				strings.Contains(err.Error(), "ExceededNativeTokenLimit"),
			"L3 X+1: %v", err)
		_, err = runETHTransfer(env, env.owner.Hex(), nativeLiveTinyCap)
		require.Error(t, err, "L3: self-funded amount==cap must fail (gas)")
		require.True(t,
			strings.Contains(err.Error(), SessionPolicyNativeCapExceededCode) ||
				strings.Contains(err.Error(), "ExceededNativeTokenLimit"),
			"L3 amount==cap: %v", err)
		t.Log("L3 PASS: over-cap and exact-cap self-funded refused")
	})

	t.Run("L4_uniswap_without_native_send", func(t *testing.T) {
		env.t = t
		submitNativeGrant(env, uniswapPerms())
		_, err := runETHTransfer(env, env.owner.Hex(), nativeLiveSendWei)
		require.Error(t, err, "L4: ethTransfer without nativeRecipients must fail")
		require.Contains(t, err.Error(), SessionPolicyNativeNotAllowedCode)
		require.NoError(t, runApprove(env, SEPOLIA_USDC))
		require.NoError(t, runApprove(env, SEPOLIA_WETH))
		require.NoError(t, runWETHDeposit(env, nativeLiveSendWei),
			"L4: WETH.deposit value must succeed on Uniswap-only grant")
		t.Log("L4 PASS: Uniswap-only refuses ethTransfer, allows approve and WETH.deposit")
	})

	t.Run("L5_mixed", func(t *testing.T) {
		env.t = t
		submitNativeGrant(env, mixedPerms(env.owner))
		require.NoError(t, runApprove(env, SEPOLIA_USDC))
		_, err := runETHTransfer(env, env.owner.Hex(), nativeLiveSendWei)
		require.NoError(t, err, "L5: listed ethTransfer on mixed grant")
		t.Log("L5 PASS: mixed grant covers USDC approve and native send")
	})

	t.Run("L8_payable_under_native_cap", func(t *testing.T) {
		env.t = t
		// Mixed grant from L5 still usable (unless L5 failed).
		require.NoError(t, runWETHDeposit(env, nativeLiveSendWei),
			"L8: payable under cap on mixed grant")
		err := runWETHDeposit(env, nativeLiveCapWei) // 0.03 ETH value + gas > cap
		require.Error(t, err, "L8: over-cap payable must fail")
		t.Log("L8 PASS: mixed payable under cap works, over cap refused")
	})

	t.Run("L7_replace_clears_nt_limits", func(t *testing.T) {
		env.t = t
		usable := usableOn(t, env.db, env.owner, env.runner)
		require.NotEmpty(t, usable, "L7 needs an installed native grant")
		oldEntity := usable[0].EntityID
		submitNativeGrant(env, uniswapPerms())
		require.NoError(t, runApprove(env, SEPOLIA_USDC), "L7: landing the replacement")
		limit, err := readNativeLimit(env.client, oldEntity, env.runner)
		require.NoError(t, err)
		require.True(t, limit.Sign() == 0, "L7: limits(%d, runner) = %s, want 0", oldEntity, limit)
		requireEntityClear(t, env.client, env.runner, oldEntity, true)
		t.Logf("L7 PASS: entity %d NT limits cleared", oldEntity)
	})
}

func setupNativeLive(t *testing.T) *nativeLiveEnv {
	t.Helper()
	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
	require.NoError(t, err, "config/test.yaml must load")

	client, err := ethclient.Dial(cfg.SmartWallet.EthRpcUrl)
	require.NoError(t, err, "cannot reach the configured RPC")
	t.Cleanup(func() { client.Close() })

	chainID, err := client.ChainID(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(11155111), chainID.Int64(), "this check is Sepolia-specific")

	ownerHex := strings.TrimSpace(os.Getenv("OWNER_EOA"))
	require.NotEmpty(t, ownerHex, "OWNER_EOA must be set")
	owner := common.HexToAddress(ownerHex)
	ownerKey := requireOwnerKey(t)
	require.Equal(t, owner, crypto.PubkeyToAddress(ownerKey.PublicKey),
		"TEST_PRIVATE_KEY must be OWNER_EOA's key")

	setGlobalFactory(t, cfg.SmartWallet)
	runner, err := aa.GetSenderAddressMAv2(client, owner, big.NewInt(fixtureSaltNativeETH))
	require.NoError(t, err)
	t.Logf("runner %s (salt %d)", runner.Hex(), fixtureSaltNativeETH)

	requireFundedRunner(t, cfg.SmartWallet, *runner, big.NewInt(50_000_000_000_000_000)) // 0.05 ETH

	code, err := client.CodeAt(context.Background(), *runner, nil)
	require.NoError(t, err)
	require.NotEmpty(t, code,
		"runner %s is not deployed; deploy with scripts/fixture_wallet -salt %d -deploy -fund 0.05",
		runner.Hex(), fixtureSaltNativeETH)

	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
	engine := New(db, cfg, nil, testutil.GetLogger())
	t.Cleanup(engine.Stop)
	engine.InstallSessionResolver()
	t.Cleanup(func() { preset.SetSessionResolver(nil) })

	RegisterChainStateReader(uint64(11155111), NewDirectChainStateReader(client, 11155111))
	t.Cleanup(ClearChainStateReaderRegistry)

	factoryAddr := effectiveFactoryAddr(t, cfg.SmartWallet)
	require.NoError(t, StoreWallet(db, chainID.Int64(), owner, &model.SmartWallet{
		Owner: &owner, Address: runner, Factory: &factoryAddr,
		Salt: big.NewInt(fixtureSaltNativeETH),
	}))
	user := &model.User{Address: owner, SmartAccountAddress: runner}
	seedEntitiesConsumedOnChain(t, db, client, cfg.SmartWallet, ownerKey, chainID.Int64(), owner, *runner)

	return &nativeLiveEnv{
		t: t, cfg: cfg, client: client, engine: engine, user: user,
		owner: owner, ownerKey: ownerKey, runner: *runner, db: db, chainID: chainID.Int64(),
	}
}

func nativeSendPerms(recipient common.Address, cap string) SessionPermissions {
	r := recipient
	return SessionPermissions{
		NativeRecipients: []*common.Address{&r},
		NativeSpendCap:   &model.NativeSpendCap{Amount: cap, GrantedCap: cap},
		ValidUntilMs:     nativeLiveValidUntilMs,
	}
}

func uniswapPerms() SessionPermissions {
	usdc := common.HexToAddress(SEPOLIA_USDC)
	weth := common.HexToAddress(SEPOLIA_WETH)
	router := common.HexToAddress(SEPOLIA_SWAPROUTER)
	return SessionPermissions{
		AllowedActions: []model.AllowedAction{
			{Target: &router, Selectors: []string{"0x04e45aaf"}},
			{Target: &usdc, Selectors: []string{"0x095ea7b3"}},
			{Target: &weth, Selectors: []string{"0x095ea7b3", "0xd0e30db0"}}, // approve + deposit
		},
		SpendCap: &model.ERC20SpendCap{
			Token: &usdc, Amount: "1000000000000", GrantedCap: "1000000000000",
		},
		ValidUntilMs: nativeLiveValidUntilMs,
	}
}

func mixedPerms(recipient common.Address) SessionPermissions {
	p := uniswapPerms()
	r := recipient
	p.NativeRecipients = []*common.Address{&r}
	p.NativeSpendCap = &model.NativeSpendCap{Amount: nativeLiveCapWei, GrantedCap: nativeLiveCapWei}
	return p
}

func submitNativeGrant(env *nativeLiveEnv, perms SessionPermissions) *model.SessionPolicy {
	t := env.t
	t.Helper()
	in := SessionPolicyInput{
		Wallet: env.runner, ChainID: env.chainID,
		AgentLabel: "NativeLive", Permissions: perms,
	}
	prepared, err := env.engine.PrepareSessionPolicy(env.user, in)
	require.NoError(t, err)
	sig, err := crypto.Sign(prepared.Digest.Bytes(), env.ownerKey)
	require.NoError(t, err)
	sig[64] += 27
	stored, _, err := env.engine.SubmitSessionPolicy(env.user, in,
		prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline, sig)
	require.NoError(t, err)
	t.Logf("grant %s entity %d", stored.ID, stored.EntityID)
	return stored
}

func runETHTransfer(env *nativeLiveEnv, dest, amount string) (map[string]interface{}, error) {
	result, err := env.engine.RunNodeImmediately("ethTransfer", map[string]interface{}{
		"destination": dest,
		"amount":      amount,
		"chainId":     env.chainID,
	}, map[string]interface{}{
		"settings": map[string]interface{}{
			"runner": env.runner.Hex(), "smartWallet": env.runner.Hex(), "chain_id": env.chainID,
		},
	}, env.user, false)
	if err != nil {
		return result, err
	}
	if success, ok := result["success"].(bool); ok && !success {
		if e, has := result["error"]; has {
			return result, fmt.Errorf("%v", e)
		}
		return result, fmt.Errorf("ethTransfer success=false")
	}
	time.Sleep(2 * time.Second)
	return result, nil
}

func runApprove(env *nativeLiveEnv, token string) error {
	result, err := env.engine.RunNodeImmediately("contractWrite", map[string]interface{}{
		"contractAddress": token,
		"contractAbi":     approveABIForReplace(),
		"chainId":         env.chainID,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName":   "approve",
				"methodParams": []interface{}{SEPOLIA_SWAPROUTER, big.NewInt(time.Now().Unix()).String()},
			},
		},
	}, map[string]interface{}{
		"settings": map[string]interface{}{
			"runner": env.runner.Hex(), "smartWallet": env.runner.Hex(), "chain_id": env.chainID,
		},
	}, env.user, false)
	if err != nil {
		return err
	}
	if success, ok := result["success"].(bool); ok && !success {
		return fmt.Errorf("approve failed: %v", result["error"])
	}
	time.Sleep(2 * time.Second)
	return nil
}

func runWETHDeposit(env *nativeLiveEnv, valueWei string) error {
	result, err := env.engine.RunNodeImmediately("contractWrite", map[string]interface{}{
		"contractAddress": SEPOLIA_WETH,
		"contractAbi":     wethDepositABI(),
		"chainId":         env.chainID,
		"value":           valueWei,
		"methodCalls": []interface{}{
			map[string]interface{}{"methodName": "deposit", "methodParams": []interface{}{}},
		},
	}, map[string]interface{}{
		"settings": map[string]interface{}{
			"runner": env.runner.Hex(), "smartWallet": env.runner.Hex(), "chain_id": env.chainID,
		},
	}, env.user, false)
	if err != nil {
		return err
	}
	if success, ok := result["success"].(bool); ok && !success {
		return fmt.Errorf("WETH.deposit failed: %v", result["error"])
	}
	time.Sleep(2 * time.Second)
	return nil
}

func wethDepositABI() []interface{} {
	return []interface{}{
		map[string]interface{}{
			"inputs":          []interface{}{},
			"name":            "deposit",
			"outputs":         []interface{}{},
			"stateMutability": "payable",
			"type":            "function",
		},
	}
}

func readNativeLimit(client *ethclient.Client, entity uint32, account common.Address) (*big.Int, error) {
	sel := crypto.Keccak256([]byte("limits(uint256,address)"))[:4]
	data := append(sel, common.LeftPadBytes(big.NewInt(int64(entity)).Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(account.Bytes(), 32)...)
	mod := aa.NativeTokenLimitModuleAddress()
	out, err := client.CallContract(context.Background(), ethereum.CallMsg{To: &mod, Data: data}, nil)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(out), nil
}

func requireNativeSelfAdminBlocked(env *nativeLiveEnv) {
	t := env.t
	t.Helper()
	controller := crypto.PubkeyToAddress(env.cfg.SmartWallet.ControllerPrivateKey.PublicKey)
	install, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: 999, Signer: controller, Global: true,
		Hooks: [][]byte{aa.AllowlistExecHook(999)},
	})
	require.NoError(t, err)
	_, _, err = preset.SendUserOpMAv2(env.cfg.SmartWallet, env.owner, install, &env.runner, big.NewInt(fixtureSaltNativeETH), nil, logger.NewNoOpLogger())
	require.Error(t, err, "L11: session key installValidation must fail")
	require.True(t,
		strings.Contains(err.Error(), "SpendingRequestNotAllowed") ||
			strings.Contains(err.Error(), "AA23") ||
			strings.Contains(err.Error(), "AddressNotAllowed"),
		"L11 installValidation: %v", err)

	sel := crypto.Keccak256([]byte("updateLimits(uint32,uint256)"))[:4]
	call := append(sel, common.LeftPadBytes(big.NewInt(1).Bytes(), 32)...)
	call = append(call, common.LeftPadBytes(big.NewInt(1).Bytes(), 32)...)
	exec, err := aa.PackExecute(aa.NativeTokenLimitModuleAddress(), big.NewInt(0), call)
	require.NoError(t, err)
	_, _, err = preset.SendUserOpMAv2(env.cfg.SmartWallet, env.owner, exec, &env.runner, big.NewInt(fixtureSaltNativeETH), nil, logger.NewNoOpLogger())
	require.Error(t, err, "L11: execute(NT, updateLimits) must fail")
	require.True(t,
		strings.Contains(err.Error(), "AddressNotAllowed") ||
			strings.Contains(err.Error(), "AA23") ||
			strings.Contains(err.Error(), "SESSION_POLICY_TARGET_NOT_ALLOWED"),
		"L11 updateLimits: %v", err)
}

func require1271Denied(env *nativeLiveEnv) {
	t := env.t
	t.Helper()
	bytes32, err := abi.NewType("bytes32", "", nil)
	require.NoError(t, err)
	bytesT, err := abi.NewType("bytes", "", nil)
	require.NoError(t, err)
	args := abi.Arguments{{Type: bytes32}, {Type: bytesT}}
	digest := common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	payload, err := args.Pack(digest, []byte{0x01})
	require.NoError(t, err)
	sel := crypto.Keccak256([]byte("isValidSignature(bytes32,bytes)"))[:4]
	to := env.runner
	_, err = env.client.CallContract(context.Background(), ethereum.CallMsg{
		To: &to, Data: append(sel, payload...),
	}, nil)
	require.Error(t, err, "L9: isValidSignature must revert with signature validation off")
}
