// Native-ETH session-hook spike — Track A PR A0.
//
// Proves on a throwaway MA v2 account, before REST packing (A2):
//
//	1  selector-scoped ERC-20 row rejects execute(alice, 1 wei, 0x)
//	   (NoSelectorSpecified)
//	2  native-recipient row (HasSelectorAllowlist=false) allows that execute
//	3  unlisted bob is AddressNotAllowed — not a wildcard
//	4  NativeTokenLimitModule: value X+1 reverts ExceededNativeTokenLimit;
//	   self-funded value==X also reverts (gas burns the remainder)
//	5  ERC-20/router rows stay selector-scoped when a native row is present
//	6  after val-then-exec uninstall, NativeTokenLimitModule.limits == 0
//	7  native-only session key cannot self-admin (installValidation /
//	   execute(NT, updateLimits))
//	8  verification-gas table (2–3 row, 20-row, replace, steady-state)
//	9  nativeValueCap: payable WETH.deposit under NT succeeds; empty-calldata
//	   ethTransfer still refused
//
// Self-funded. No Gas Manager.
//
// Env (loads repo-root .env / .env.local first; process env wins):
//
//	SPIKE_OWNER_KEY / TEST_PRIVATE_KEY     owner EOA (signs the grant, prefunds)
//	SPIKE_CONTROLLER_KEY / CONTROLLER_PRIVATE_KEY / TEST_PRIVATE_KEY
//	                                      session signer (same key is fine for a spike)
//	SPIKE_BUNDLER_URL / SEPOLIA_BUNDLER_URL
//	SPIKE_RPC_URL (opt; default Sepolia publicnode)
//	SPIKE_SALT (opt, default 17)
//	SPIKE_SKIP_WIDE=1 to skip the 20-recipient gas probe
//
// Run:
//
//	go run ./scripts/spike/native_eth_hooks
//	SPIKE_RPC_URL=https://base-rpc.publicnode.com go run ./scripts/spike/native_eth_hooks
package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

const (
	defaultSepoliaRPC = "https://ethereum-sepolia-rpc.publicnode.com"
	// NativeTokenLimitModule v2.0.0 — not redeployed in v2.0.1.
	nativeTokenLimitHex = "0x00000000000001e541f0D090868FBe24b59Fbe06"
	sepoliaWETH         = "0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14"
	baseWETH            = "0x4200000000000000000000000000000000000006"
	dummyToken          = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238" // USDC-shaped; never called
)

var (
	prefundWei  = big.NewInt(30_000_000_000_000_000) // 0.03 ETH — first-op gas + native probes
	nativeCapX  = big.NewInt(10_000_000_000_000_000) // 0.01 ETH; first UserOp gas must fit under this
	oneWei      = big.NewInt(1)
	depositSel  = [4]byte{0xd0, 0xe3, 0x0d, 0xb0} // deposit()
	transferSel = [4]byte{0xa9, 0x05, 0x9c, 0xbb}
	approveSel  = [4]byte{0x09, 0x5e, 0xa7, 0xb3}
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "spike failed: %v\n", err)
		os.Exit(1)
	}
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func requireKey(names ...string) (*ecdsa.PrivateKey, common.Address, error) {
	var raw, name string
	for _, n := range names {
		if v := strings.TrimPrefix(strings.TrimSpace(os.Getenv(n)), "0x"); v != "" {
			raw, name = v, n
			break
		}
	}
	if raw == "" {
		return nil, common.Address{}, fmt.Errorf("set one of: %s", strings.Join(names, ", "))
	}
	key, err := crypto.HexToECDSA(raw)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("%s is not a private key: %w", name, err)
	}
	return key, crypto.PubkeyToAddress(key.PublicKey), nil
}

func firstNonEmpty(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func loadDotEnv() {
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			for _, name := range []string{".env.local", ".env"} {
				loadDotEnvFile(filepath.Join(dir, name))
			}
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

func loadDotEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if key != "" && os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
		}
	}
}

func trim0x(s string) string {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return s[2:]
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func containsAny(s string, needles ...string) bool {
	low := strings.ToLower(s)
	for _, n := range needles {
		if strings.Contains(low, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

func nativeModule() common.Address { return common.HexToAddress(nativeTokenLimitHex) }

func wethForChain(chainID *big.Int) common.Address {
	switch chainID.Int64() {
	case 8453:
		return common.HexToAddress(baseWETH)
	default:
		return common.HexToAddress(sepoliaWETH)
	}
}

// ── NT packing (spike-local; promote to aa in A2) ──────────────────────────

func packNTInstallData(entity uint32, limit *big.Int) ([]byte, error) {
	u32, err := abi.NewType("uint32", "", nil)
	if err != nil {
		return nil, err
	}
	u256, err := abi.NewType("uint256", "", nil)
	if err != nil {
		return nil, err
	}
	return abi.Arguments{{Type: u32}, {Type: u256}}.Pack(entity, limit)
}

func packNTUninstallData(entity uint32) ([]byte, error) {
	u32, err := abi.NewType("uint32", "", nil)
	if err != nil {
		return nil, err
	}
	return abi.Arguments{{Type: u32}}.Pack(entity)
}

func packNTValHook(entity uint32, limit *big.Int) ([]byte, error) {
	data, err := packNTInstallData(entity, limit)
	if err != nil {
		return nil, err
	}
	cfg := aa.PackHookConfig(nativeModule(), entity, aa.HookFlagValidation)
	return append(cfg[:], data...), nil
}

func packNTExecHook(entity uint32) []byte {
	cfg := aa.PackHookConfig(nativeModule(), entity, aa.HookFlagExecHasPre)
	return cfg[:]
}

func packUpdateLimits(entity uint32, limit *big.Int) []byte {
	sel := crypto.Keccak256([]byte("updateLimits(uint32,uint256)"))[:4]
	out := append([]byte{}, sel...)
	out = append(out, common.LeftPadBytes(big.NewInt(int64(entity)).Bytes(), 32)...)
	return append(out, common.LeftPadBytes(limit.Bytes(), 32)...)
}

func readNativeLimit(ctx context.Context, chain *ethclient.Client, entity uint32, account common.Address) (*big.Int, error) {
	sel := crypto.Keccak256([]byte("limits(uint256,address)"))[:4]
	data := append(sel, common.LeftPadBytes(big.NewInt(int64(entity)).Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(account.Bytes(), 32)...)
	mod := nativeModule()
	out, err := chain.CallContract(ctx, ethereum.CallMsg{To: &mod, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("reading NativeTokenLimitModule.limits: %w", err)
	}
	return new(big.Int).SetBytes(out), nil
}

func run() error {
	ctx := context.Background()
	loadDotEnv()
	ownerKey, ownerAddr, err := requireKey("SPIKE_OWNER_KEY", "TEST_PRIVATE_KEY")
	if err != nil {
		return err
	}
	controllerKey, controllerAddr, err := requireKey("SPIKE_CONTROLLER_KEY", "CONTROLLER_PRIVATE_KEY", "TEST_PRIVATE_KEY")
	if err != nil {
		return err
	}
	bundlerURL := firstNonEmpty("SPIKE_BUNDLER_URL", "SEPOLIA_BUNDLER_URL")
	if bundlerURL == "" {
		return fmt.Errorf("set SPIKE_BUNDLER_URL or SEPOLIA_BUNDLER_URL")
	}
	salt := big.NewInt(19)
	if s := os.Getenv("SPIKE_SALT"); s != "" {
		v, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return fmt.Errorf("SPIKE_SALT %q is not a decimal integer", s)
		}
		salt = v
	}

	chain, err := ethclient.Dial(env("SPIKE_RPC_URL", defaultSepoliaRPC))
	if err != nil {
		return err
	}
	defer chain.Close()
	chainRPC := chain.Client()
	bundler, err := rpc.DialContext(ctx, bundlerURL)
	if err != nil {
		return err
	}
	defer bundler.Close()
	chainID, err := chain.ChainID(ctx)
	if err != nil {
		return err
	}
	entryPoint := preset.EntryPointV07()
	weth := wethForChain(chainID)
	alice := common.HexToAddress("0x000000000000000000000000000000000000a11c")
	bob := common.HexToAddress("0x000000000000000000000000000000000000b0b0")
	token := common.HexToAddress(dummyToken)

	fmt.Printf("chain %s owner %s controller %s salt %s\n", chainID, ownerAddr, controllerAddr, salt)
	fmt.Printf("NT module %s  WETH %s\n", nativeModule(), weth)

	accountPtr, err := aa.GetSenderAddressMAv2(chain, ownerAddr, salt)
	if err != nil {
		return err
	}
	account := *accountPtr
	code, err := chain.CodeAt(ctx, account, nil)
	if err != nil {
		return err
	}
	if len(code) > 0 {
		return fmt.Errorf("account %s already has code — bump SPIKE_SALT", account)
	}
	fmt.Printf("account %s (counterfactual)\n", account)

	if bal, bErr := chain.BalanceAt(ctx, account, nil); bErr != nil {
		return bErr
	} else if bal.Cmp(prefundWei) < 0 {
		if err := sendETH(ctx, chain, chainID, ownerKey, ownerAddr, account, prefundWei); err != nil {
			return err
		}
	}

	h := &harness{
		ctx: ctx, chain: chain, chainRPC: chainRPC, bundler: bundler,
		chainID: chainID, entryPoint: entryPoint, account: account,
		ownerKey: ownerKey, ownerAddr: ownerAddr,
		controllerKey: controllerKey, controllerAddr: controllerAddr,
		salt: salt, factoryNeeded: true,
	}

	// ── Proof 1: selector-scoped ERC-20 cannot native-send ──────────────
	e1 := uint32(1)
	hooks1, err := erc20OnlyHooks(e1, token)
	if err != nil {
		return err
	}
	if err := h.installDeferred(e1, hooks1, 1_200_000); err != nil {
		return fmt.Errorf("proof 1 install: %w", err)
	}
	h.factoryNeeded = false
	err1 := h.estimateNative(e1, alice, oneWei)
	if err1 == nil {
		return fmt.Errorf("PROOF 1 FAIL: native send was estimated under a selector-scoped grant")
	}
	if !containsAny(err1.Error(), "NoSelectorSpecified", "AA23") {
		fmt.Printf("PROOF 1 note: refused as %s (want NoSelectorSpecified in the revert data)\n", firstLine(err1.Error()))
	}
	fmt.Printf("PROOF 1 PASS: selector-scoped grant refused empty-calldata native send\n  %s\n\n", firstLine(err1.Error()))

	// ── Proofs 2–5, 7, pin: mixed grant on entity 2 ─────────────────────
	e2 := uint32(2)
	mixed, err := mixedHooks(e2, token, alice, nativeCapX)
	if err != nil {
		return err
	}
	receipt2, op2, err := h.installDeferredNative(e2, mixed, alice, oneWei, 900_000)
	if err != nil {
		return fmt.Errorf("proof 2 install+send: %w", err)
	}
	if !receipt2.Success {
		return fmt.Errorf("PROOF 2 FAIL: native send to listed EOA reverted tx=%s", receipt2.TxHash)
	}
	fmt.Printf("PROOF 2 PASS: listed EOA native send mined gasUsed=%s vgl=%s tx=%s\n\n",
		receipt2.ActualGasUsed, op2.VerificationGasLimit, receipt2.TxHash)
	h.noteGas("first-op mixed 2-row (alice+token) + 5 hooks", receipt2, op2)

	err3 := h.estimateNative(e2, bob, oneWei)
	if err3 == nil {
		return fmt.Errorf("PROOF 3 FAIL: unlisted bob was estimated")
	}
	fmt.Printf("PROOF 3 PASS: unlisted recipient refused\n  %s\n\n", firstLine(err3.Error()))

	remaining, err := readNativeLimit(ctx, chain, e2, account)
	if err != nil {
		return err
	}
	fmt.Printf("  NT remaining after first send: %s wei\n", remaining)
	over := new(big.Int).Add(remaining, oneWei)
	err4a := h.estimateNative(e2, alice, over)
	if err4a == nil {
		r, _, sendErr := h.sendNative(e2, alice, over, 200_000)
		if sendErr == nil && r != nil && r.Success {
			return fmt.Errorf("PROOF 4 FAIL: value remaining+1 succeeded")
		}
		fmt.Printf("PROOF 4a PASS: remaining+1 did not succeed (%v)\n", firstLine(fmt.Sprint(sendErr)))
	} else {
		fmt.Printf("PROOF 4a PASS: remaining+1 refused at estimate\n  %s\n", firstLine(err4a.Error()))
	}
	err4b := h.estimateNative(e2, alice, remaining)
	if err4b == nil {
		r, _, sendErr := h.sendNative(e2, alice, remaining, 200_000)
		if sendErr == nil && r != nil && r.Success {
			return fmt.Errorf("PROOF 4 FAIL: self-funded value==remaining succeeded (gas should consume the rest)")
		}
		fmt.Printf("PROOF 4b PASS: value==remaining did not succeed (gas burns remainder)\n")
	} else {
		fmt.Printf("PROOF 4b PASS: value==remaining refused at estimate (gas)\n  %s\n", firstLine(err4b.Error()))
	}
	fmt.Println()

	// Token transfer (approve/transfer) to the dummy token would revert at
	// the token. Probe 5 is packing: native row must not set
	// HasSelectorAllowlist=false on the token target. Re-read by decoding
	// is covered by unit tests; on-chain: empty-calldata to the TOKEN
	// (selector-scoped) must still refuse.
	err5 := h.estimateNative(e2, token, oneWei)
	if err5 == nil {
		return fmt.Errorf("PROOF 5 FAIL: empty-calldata to the ERC-20 target was estimated — native row widened it")
	}
	fmt.Printf("PROOF 5 PASS: ERC-20 target still selector-scoped\n  %s\n\n", firstLine(err5.Error()))

	// ── Proof 7: cannot self-admin ──────────────────────────────────────
	installCall, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: 99, Signer: controllerAddr, Global: true,
		Hooks: [][]byte{aa.AllowlistExecHook(99)},
	})
	if err != nil {
		return err
	}
	err7a := h.estimateRaw(e2, installCall, 200_000)
	if err7a == nil {
		return fmt.Errorf("PROOF 7 FAIL: installValidation was estimated under the session key")
	}
	fmt.Printf("PROOF 7a PASS: installValidation refused\n  %s\n", firstLine(err7a.Error()))
	upd := packUpdateLimits(e2, big.NewInt(0))
	execNT, err := aa.PackExecute(nativeModule(), big.NewInt(0), upd)
	if err != nil {
		return err
	}
	err7b := h.estimateExec(e2, execNT, 200_000)
	if err7b == nil {
		return fmt.Errorf("PROOF 7 FAIL: execute(NT, updateLimits) was estimated")
	}
	fmt.Printf("PROOF 7b PASS: execute(NT, updateLimits) refused\n  %s\n\n", firstLine(err7b.Error()))

	// ── Proof 6: val-then-exec uninstall, limits==0 ─────────────────────
	if err := h.ownerUninstall(e2, mixed); err != nil {
		return fmt.Errorf("proof 6 uninstall: %w", err)
	}
	left, err := readNativeLimit(ctx, chain, e2, account)
	if err != nil {
		return err
	}
	if left.Sign() != 0 {
		return fmt.Errorf("PROOF 6 FAIL: limits after uninstall = %s (want 0) — teardown stranded NT state", left)
	}
	fmt.Printf("PROOF 6 PASS: NativeTokenLimitModule.limits(entity=2, account)=0 after val-then-exec uninstall\n\n")

	// ── Proof 9: nativeValueCap (no native recipients) ──────────────────
	e5 := uint32(5)
	valueCapHooks, err := nativeValueCapHooks(e5, weth, nativeCapX)
	if err != nil {
		return err
	}
	depositCall := depositSel[:]
	execDep, err := aa.PackExecute(weth, oneWei, depositCall)
	if err != nil {
		return err
	}
	r9, _, err := h.installDeferredExec(e5, valueCapHooks, execDep, 900_000)
	if err != nil {
		return fmt.Errorf("proof 9 WETH.deposit install: %w", err)
	}
	if !r9.Success {
		return fmt.Errorf("PROOF 9 FAIL: WETH.deposit under nativeValueCap reverted tx=%s", r9.TxHash)
	}
	fmt.Printf("PROOF 9a PASS: payable WETH.deposit under NT (no nativeRecipients) mined tx=%s\n", r9.TxHash)
	err9b := h.estimateNative(e5, alice, oneWei)
	if err9b == nil {
		return fmt.Errorf("PROOF 9 FAIL: ethTransfer estimated under nativeValueCap (no recipients)")
	}
	fmt.Printf("PROOF 9b PASS: empty-calldata ethTransfer refused without nativeRecipients\n  %s\n\n", firstLine(err9b.Error()))

	// ── Proof 8: gas table ──────────────────────────────────────────────
	fmt.Println("PROOF 8 gas table (self-funded; seeds vs signed VGL):")
	fmt.Println("  (2–3 row first-op recorded above as first-op mixed)")
	if os.Getenv("SPIKE_SKIP_WIDE") == "1" {
		fmt.Println("  SKIP 20-row (SPIKE_SKIP_WIDE=1) — re-run without skip before A2")
	} else {
		e8 := uint32(8)
		wide, err := wideNativeHooks(e8, 20, nativeCapX)
		if err != nil {
			return err
		}
		r8, op8, err := h.installDeferredNative(e8, wide, wideRecipient(0), oneWei, 1_500_000)
		if err != nil {
			fmt.Printf("  FINDING 20-row first-op: estimate/send failed: %s\n", firstLine(err.Error()))
			fmt.Println("  A2 must cut max recipients or add per-row VGL seed (K14)")
		} else {
			fmt.Printf("  20-row first-op success=%v gasUsed=%s vgl=%s tx=%s\n",
				r8.Success, r8.ActualGasUsed, op8.VerificationGasLimit, r8.TxHash)
			h.noteGas("first-op 20 native recipients", r8, op8)
		}
	}
	// Steady-state: entity 5 already installed; another deposit.
	execDep2, err := aa.PackExecute(weth, oneWei, depositCall)
	if err != nil {
		return err
	}
	rSS, opSS, err := h.sendExec(e5, execDep2, 200_000)
	if err != nil {
		fmt.Printf("  steady-state deposit estimate/send: %s\n", firstLine(err.Error()))
	} else {
		fmt.Printf("  steady-state (installed) gasUsed=%s vgl=%s (expect ~100k-class VGL, not 700k)\n",
			rSS.ActualGasUsed, opSS.VerificationGasLimit)
		h.noteGas("steady-state after install", rSS, opSS)
	}

	fmt.Println("\nA0 spike finished. Record the PROOF lines and gas table in the PR body.")
	fmt.Println("Re-run with SPIKE_RPC_URL pointing at Base before calling A0 done.")
	return nil
}

type harness struct {
	ctx                 context.Context
	chain               *ethclient.Client
	chainRPC, bundler   *rpc.Client
	chainID             *big.Int
	entryPoint, account common.Address
	ownerKey            *ecdsa.PrivateKey
	ownerAddr           common.Address
	controllerKey       *ecdsa.PrivateKey
	controllerAddr      common.Address
	salt                *big.Int
	factoryNeeded       bool
}

func (h *harness) noteGas(label string, r *userOpReceipt, op *userop.UserOperationV07) {
	if r == nil || op == nil {
		return
	}
	gasWei := new(big.Int).Mul(
		new(big.Int).Add(new(big.Int).Add(op.CallGasLimit, op.VerificationGasLimit), op.PreVerificationGas),
		op.MaxFeePerGas,
	)
	fmt.Printf("  GAS %s actual=%s cgl=%s vgl=%s pvg=%s maxFee=%s signedGasWei=%s\n",
		label, r.ActualGasUsed, op.CallGasLimit, op.VerificationGasLimit, op.PreVerificationGas, op.MaxFeePerGas, gasWei)
}

func erc20OnlyHooks(entity uint32, token common.Address) ([][]byte, error) {
	allow, err := aa.AllowlistValidationHook(entity, []aa.AllowlistInput{{
		Target:               token,
		HasSelectorAllowlist: true,
		HasERC20SpendLimit:   true,
		ERC20SpendLimit:      big.NewInt(1),
		Selectors:            [][4]byte{transferSel, approveSel},
	}})
	if err != nil {
		return nil, err
	}
	tr, err := aa.TimeRangeValidationHook(entity, uint64(time.Now().Add(24*time.Hour).Unix()), 0)
	if err != nil {
		return nil, err
	}
	return [][]byte{allow, aa.AllowlistExecHook(entity), tr}, nil
}

func mixedHooks(entity uint32, token, alice common.Address, cap *big.Int) ([][]byte, error) {
	allow, err := aa.AllowlistValidationHook(entity, []aa.AllowlistInput{
		{
			Target:               token,
			HasSelectorAllowlist: true,
			HasERC20SpendLimit:   true,
			ERC20SpendLimit:      big.NewInt(1),
			Selectors:            [][4]byte{transferSel, approveSel},
		},
		{
			Target:               alice,
			HasSelectorAllowlist: false,
			HasERC20SpendLimit:   false,
			Selectors:            nil,
		},
	})
	if err != nil {
		return nil, err
	}
	ntVal, err := packNTValHook(entity, cap)
	if err != nil {
		return nil, err
	}
	tr, err := aa.TimeRangeValidationHook(entity, uint64(time.Now().Add(24*time.Hour).Unix()), 0)
	if err != nil {
		return nil, err
	}
	return [][]byte{allow, aa.AllowlistExecHook(entity), ntVal, packNTExecHook(entity), tr}, nil
}

func nativeValueCapHooks(entity uint32, weth common.Address, cap *big.Int) ([][]byte, error) {
	allow, err := aa.AllowlistValidationHook(entity, []aa.AllowlistInput{{
		Target:               weth,
		HasSelectorAllowlist: true,
		HasERC20SpendLimit:   false, // deposit is not transfer/approve
		Selectors:            [][4]byte{depositSel},
	}})
	if err != nil {
		return nil, err
	}
	ntVal, err := packNTValHook(entity, cap)
	if err != nil {
		return nil, err
	}
	tr, err := aa.TimeRangeValidationHook(entity, uint64(time.Now().Add(24*time.Hour).Unix()), 0)
	if err != nil {
		return nil, err
	}
	return [][]byte{allow, aa.AllowlistExecHook(entity), ntVal, packNTExecHook(entity), tr}, nil
}

func wideNativeHooks(entity uint32, n int, cap *big.Int) ([][]byte, error) {
	inputs := make([]aa.AllowlistInput, n)
	for i := 0; i < n; i++ {
		inputs[i] = aa.AllowlistInput{
			Target:               wideRecipient(i),
			HasSelectorAllowlist: false,
		}
	}
	allow, err := aa.AllowlistValidationHook(entity, inputs)
	if err != nil {
		return nil, err
	}
	ntVal, err := packNTValHook(entity, cap)
	if err != nil {
		return nil, err
	}
	tr, err := aa.TimeRangeValidationHook(entity, uint64(time.Now().Add(24*time.Hour).Unix()), 0)
	if err != nil {
		return nil, err
	}
	return [][]byte{allow, aa.AllowlistExecHook(entity), ntVal, packNTExecHook(entity), tr}, nil
}

func wideRecipient(i int) common.Address {
	var b [20]byte
	b[18] = byte(i >> 8)
	b[19] = byte(i)
	b[0] = 0xee
	return common.BytesToAddress(b[:])
}

func (h *harness) installCall(entity uint32, hooks [][]byte) ([]byte, error) {
	return aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: entity,
		Signer:   h.controllerAddr,
		Global:   true,
		Hooks:    hooks,
	})
}

func (h *harness) installDeferred(entity uint32, hooks [][]byte, vgl int64) error {
	// In-policy approve(controller, 0) — real Sepolia USDC reverts transfer(0,0).
	inner := append(approveSel[:], common.LeftPadBytes(h.controllerAddr.Bytes(), 32)...)
	inner = append(inner, make([]byte, 32)...)
	exec, err := aa.PackExecute(common.HexToAddress(dummyToken), big.NewInt(0), inner)
	if err != nil {
		return err
	}
	_, _, err = h.installDeferredExec(entity, hooks, exec, vgl)
	return err
}

func (h *harness) installDeferredNative(entity uint32, hooks [][]byte, to common.Address, value *big.Int, vgl int64) (*userOpReceipt, *userop.UserOperationV07, error) {
	exec, err := aa.PackExecute(to, value, nil)
	if err != nil {
		return nil, nil, err
	}
	return h.installDeferredExec(entity, hooks, exec, vgl)
}

func (h *harness) installDeferredExec(entity uint32, hooks [][]byte, exec []byte, vgl int64) (*userOpReceipt, *userop.UserOperationV07, error) {
	installCall, err := h.installCall(entity, hooks)
	if err != nil {
		return nil, nil, err
	}
	call, err := aa.WrapExecuteUserOp(exec)
	if err != nil {
		return nil, nil, err
	}
	opts := uint8(userop.ValidationOptionGlobal | userop.ValidationOptionDeferredAction)
	carrierNonce, err := userop.EncodeNonceMAv2(entity, opts, 0)
	if err != nil {
		return nil, nil, err
	}
	deadline := uint64(time.Now().Add(time.Hour).Unix())
	digest, err := userop.DeferredActionDigest(h.chainID, h.account, carrierNonce, deadline, installCall)
	if err != nil {
		return nil, nil, err
	}
	ownerSig, err := crypto.Sign(digest.Bytes(), h.ownerKey)
	if err != nil {
		return nil, nil, err
	}
	ownerSig[64] += 27
	encodedData, err := userop.EncodeDeferredActionData(userop.FallbackSignerLocator(), deadline, installCall)
	if err != nil {
		return nil, nil, err
	}
	op := &userop.UserOperationV07{
		Sender:               h.account,
		Nonce:                carrierNonce,
		CallData:             call,
		VerificationGasLimit: big.NewInt(vgl),
	}
	if h.factoryNeeded {
		factory, factoryData, err := aa.GetInitCodeMAv2(h.ownerAddr, h.salt)
		if err != nil {
			return nil, nil, err
		}
		op.Factory = &factory
		op.FactoryData = factoryData
	}
	if err := h.priceOp(op, encodedData, ownerSig); err != nil {
		return nil, nil, err
	}
	if err := preset.SignUserOpV07Deferred(op, h.entryPoint, h.chainID, h.controllerKey, encodedData, ownerSig); err != nil {
		return nil, nil, err
	}
	hash, err := preset.SendUserOpV07(h.ctx, h.bundler, op, h.entryPoint)
	if err != nil {
		return nil, op, err
	}
	r, err := waitReceipt(h.ctx, h.bundler, hash)
	if err != nil {
		return nil, op, err
	}
	h.factoryNeeded = false
	return r, op, nil
}

func (h *harness) estimateNative(entity uint32, to common.Address, value *big.Int) error {
	exec, err := aa.PackExecute(to, value, nil)
	if err != nil {
		return err
	}
	return h.estimateExec(entity, exec, 200_000)
}

func (h *harness) estimateExec(entity uint32, exec []byte, vgl int64) error {
	call, err := aa.WrapExecuteUserOp(exec)
	if err != nil {
		return err
	}
	return h.estimateRaw(entity, call, vgl)
}

func (h *harness) estimateRaw(entity uint32, callData []byte, vgl int64) error {
	nonce, err := preset.NextNonceV07(h.ctx, h.chainRPC, h.entryPoint, h.account, entity, userop.ValidationOptionGlobal)
	if err != nil {
		return err
	}
	op := &userop.UserOperationV07{
		Sender: h.account, Nonce: nonce, CallData: callData,
		VerificationGasLimit: big.NewInt(vgl),
	}
	return h.priceOp(op, nil, nil)
}

func (h *harness) sendNative(entity uint32, to common.Address, value *big.Int, vgl int64) (*userOpReceipt, *userop.UserOperationV07, error) {
	exec, err := aa.PackExecute(to, value, nil)
	if err != nil {
		return nil, nil, err
	}
	return h.sendExec(entity, exec, vgl)
}

func (h *harness) sendExec(entity uint32, exec []byte, vgl int64) (*userOpReceipt, *userop.UserOperationV07, error) {
	call, err := aa.WrapExecuteUserOp(exec)
	if err != nil {
		return nil, nil, err
	}
	nonce, err := preset.NextNonceV07(h.ctx, h.chainRPC, h.entryPoint, h.account, entity, userop.ValidationOptionGlobal)
	if err != nil {
		return nil, nil, err
	}
	op := &userop.UserOperationV07{
		Sender: h.account, Nonce: nonce, CallData: call,
		VerificationGasLimit: big.NewInt(vgl),
	}
	if err := h.priceOp(op, nil, nil); err != nil {
		return nil, op, err
	}
	if err := preset.SignUserOpV07(op, h.entryPoint, h.chainID, h.controllerKey); err != nil {
		return nil, op, err
	}
	hash, err := preset.SendUserOpV07(h.ctx, h.bundler, op, h.entryPoint)
	if err != nil {
		return nil, op, err
	}
	r, err := waitReceipt(h.ctx, h.bundler, hash)
	return r, op, err
}

func (h *harness) priceOp(op *userop.UserOperationV07, encodedData, ownerSig []byte) error {
	tip, err := h.chain.SuggestGasTipCap(h.ctx)
	if err != nil {
		return err
	}
	var bundlerTipHex string
	if err := h.bundler.CallContext(h.ctx, &bundlerTipHex, "rundler_maxPriorityFeePerGas"); err == nil {
		if bundlerTip, ok := new(big.Int).SetString(trim0x(bundlerTipHex), 16); ok && bundlerTip.Cmp(tip) > 0 {
			tip = bundlerTip
		}
	}
	head, err := h.chain.HeaderByNumber(h.ctx, nil)
	if err != nil {
		return err
	}
	op.MaxPriorityFeePerGas = tip
	base := big.NewInt(0)
	if head.BaseFee != nil {
		base = head.BaseFee
	}
	op.MaxFeePerGas = new(big.Int).Add(tip, new(big.Int).Mul(base, big.NewInt(2)))
	if encodedData != nil {
		sig, err := preset.DeferredEstimationSignature(encodedData, ownerSig)
		if err != nil {
			return err
		}
		op.Signature = sig
		defer func() { op.Signature = nil }()
	}
	if _, err := preset.EstimateUserOpGasV07(h.ctx, h.bundler, op, h.entryPoint); err != nil {
		return fmt.Errorf("estimating gas: %w", err)
	}
	return nil
}

// ownerUninstall sends a plain owner-signed uninstallValidation with
// val-then-exec hook data (not the flat reverse).
func (h *harness) ownerUninstall(entity uint32, installHooks [][]byte) error {
	// Split val vs exec by flag bit 0 of the 25-byte config; reverse each
	// group; concat val then exec. NT-val teardown is entityId only.
	var val, exec [][]byte
	for _, entry := range installHooks {
		if len(entry) < 25 {
			return fmt.Errorf("hook too short")
		}
		flags := entry[24]
		data := entry[25:]
		mod := common.BytesToAddress(entry[:20])
		if flags&aa.HookFlagValidation != 0 {
			if mod == nativeModule() {
				d, err := packNTUninstallData(entity)
				if err != nil {
					return err
				}
				val = append(val, d)
			} else {
				val = append(val, append([]byte(nil), data...))
			}
		} else {
			exec = append(exec, nil)
		}
	}
	teardown := make([][]byte, 0, len(val)+len(exec))
	for i := len(val) - 1; i >= 0; i-- {
		teardown = append(teardown, val[i])
	}
	for i := len(exec) - 1; i >= 0; i-- {
		teardown = append(teardown, exec[i])
	}
	call, err := aa.PackSessionSignerUninstall(entity, teardown)
	if err != nil {
		return err
	}
	nonce, err := h.chain.PendingNonceAt(h.ctx, h.ownerAddr)
	if err != nil {
		return err
	}
	gasPrice, err := h.chain.SuggestGasPrice(h.ctx)
	if err != nil {
		return err
	}
	gas, err := h.chain.EstimateGas(h.ctx, ethereum.CallMsg{From: h.ownerAddr, To: &h.account, Data: call})
	if err != nil {
		return fmt.Errorf("estimating uninstall: %w", err)
	}
	tx := types.NewTransaction(nonce, h.account, big.NewInt(0), gas+50_000, gasPrice, call)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(h.chainID), h.ownerKey)
	if err != nil {
		return err
	}
	if err := h.chain.SendTransaction(h.ctx, signed); err != nil {
		return err
	}
	if _, err := waitMined(h.ctx, h.chain, signed.Hash()); err != nil {
		return err
	}
	fmt.Printf("owner uninstallValidation mined tx=%s\n", signed.Hash())
	return nil
}

func sendETH(ctx context.Context, chain *ethclient.Client, chainID *big.Int,
	ownerKey *ecdsa.PrivateKey, ownerAddr, to common.Address, amount *big.Int) error {

	nonce, err := chain.PendingNonceAt(ctx, ownerAddr)
	if err != nil {
		return err
	}
	gasPrice, err := chain.SuggestGasPrice(ctx)
	if err != nil {
		return err
	}
	gasLimit, err := chain.EstimateGas(ctx, ethereum.CallMsg{From: ownerAddr, To: &to, Value: amount})
	if err != nil {
		return err
	}
	tx := types.NewTransaction(nonce, to, amount, gasLimit+10_000, gasPrice, nil)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), ownerKey)
	if err != nil {
		return err
	}
	if err := chain.SendTransaction(ctx, signed); err != nil {
		return fmt.Errorf("prefunding: %w", err)
	}
	if _, err := waitMined(ctx, chain, signed.Hash()); err != nil {
		return err
	}
	fmt.Printf("prefunded %s wei to %s\n", amount, to)
	return nil
}

func waitMined(ctx context.Context, chain *ethclient.Client, hash common.Hash) (*types.Receipt, error) {
	for range 60 {
		receipt, err := chain.TransactionReceipt(ctx, hash)
		if err == nil {
			if receipt.Status != types.ReceiptStatusSuccessful {
				return nil, fmt.Errorf("tx %s mined but failed", hash)
			}
			return receipt, nil
		}
		time.Sleep(3 * time.Second)
	}
	return nil, fmt.Errorf("tx %s not mined after 3 minutes", hash)
}

type userOpReceipt struct {
	Success       bool
	ActualGasUsed *big.Int
	TxHash        string
}

func waitReceipt(ctx context.Context, bundler *rpc.Client, opHash common.Hash) (*userOpReceipt, error) {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		var raw json.RawMessage
		if err := bundler.CallContext(ctx, &raw, "eth_getUserOperationReceipt", opHash.Hex()); err == nil &&
			len(raw) > 0 && string(raw) != "null" {
			var parsed struct {
				Success       bool   `json:"success"`
				ActualGasUsed string `json:"actualGasUsed"`
				Receipt       struct {
					TransactionHash string `json:"transactionHash"`
				} `json:"receipt"`
			}
			if err := json.Unmarshal(raw, &parsed); err != nil {
				return nil, err
			}
			gasUsed, _ := new(big.Int).SetString(trim0x(parsed.ActualGasUsed), 16)
			return &userOpReceipt{
				Success:       parsed.Success,
				ActualGasUsed: gasUsed,
				TxHash:        parsed.Receipt.TransactionHash,
			}, nil
		}
		time.Sleep(4 * time.Second)
	}
	return nil, fmt.Errorf("no receipt for %s after 3 minutes", opHash)
}
