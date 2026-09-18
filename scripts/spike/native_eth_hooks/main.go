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
//	SPIKE_RPC_URL / SEPOLIA_RPC_URL / Alchemy Sepolia (required paid RPC — no publicnode)
//	SPIKE_SALT (opt, default 19). If that account already has code, the spike
//	resumes (new entity ids) and sweeps leftover ETH to the owner at the end.
//
// Run:
//
//	go run ./scripts/spike/native_eth_hooks
//	SPIKE_RPC_URL=$BASE_RPC_URL SPIKE_BUNDLER_URL=... go run ./scripts/spike/native_eth_hooks
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
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/eip1559"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

const (
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

func isInfraError(err error) bool {
	if err == nil {
		return false
	}
	return containsAny(err.Error(),
		"timeout", "timed out", "429", "rate limit", "too many requests",
		"no such host", "connection refused", "connection reset", "eof",
		"502", "503", "dial tcp", "i/o timeout", "context deadline")
}

// requireRevert fails on success or infra errors. Named module reverts pass;
// a bare AA23 is accepted only for validation-hook probes (Alchemy often
// strips the inner reason).
func requireRevert(err error, proof string, needles ...string) error {
	if err == nil {
		return fmt.Errorf("%s FAIL: expected revert, estimate/send succeeded", proof)
	}
	if isInfraError(err) {
		return fmt.Errorf("%s FAIL: infra error, not a module revert: %s", proof, firstLine(err.Error()))
	}
	msg := err.Error()
	for _, n := range needles {
		if containsAny(msg, n) {
			fmt.Printf("%s PASS (%s)\n  %s\n", proof, n, firstLine(msg))
			return nil
		}
	}
	return fmt.Errorf("%s FAIL: refused as %s (want one of %v)", proof, firstLine(msg), needles)
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
		if k := firstNonEmpty("ALCHEMY_API_KEY"); k != "" {
			bundlerURL = "https://eth-sepolia.g.alchemy.com/v2/" + k
		}
	}
	if bundlerURL == "" {
		return fmt.Errorf("set SPIKE_BUNDLER_URL or SEPOLIA_BUNDLER_URL")
	}
	rpcURL := firstNonEmpty("SPIKE_RPC_URL", "SEPOLIA_RPC_URL")
	if rpcURL == "" {
		if k := firstNonEmpty("ALCHEMY_API_KEY"); k != "" {
			rpcURL = "https://eth-sepolia.g.alchemy.com/v2/" + k
		}
	}
	if rpcURL == "" || containsAny(rpcURL, "publicnode", "public-rpc", "llamarpc") {
		return fmt.Errorf("set SPIKE_RPC_URL to a paid endpoint (public RPCs are refused)")
	}
	salt := big.NewInt(19)
	if s := os.Getenv("SPIKE_SALT"); s != "" {
		v, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return fmt.Errorf("SPIKE_SALT %q is not a decimal integer", s)
		}
		salt = v
	}

	chain, err := ethclient.Dial(rpcURL)
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
	deployed := len(code) > 0
	if deployed {
		fmt.Printf("account %s already deployed — resuming with unused entity ids\n", account)
	} else {
		fmt.Printf("account %s (counterfactual)\n", account)
	}

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
		salt: salt, factoryNeeded: !deployed,
	}
	defer func() { _ = h.sweepToOwner() }()

	e1 := uint32(1)
	if deployed {
		e1 = 31
	}
	hooks1, err := erc20OnlyHooks(e1, token)
	if err != nil {
		return err
	}
	if err := h.installDeferred(e1, hooks1, 1_200_000); err != nil {
		return fmt.Errorf("proof 1 install: %w", err)
	}
	h.factoryNeeded = false
	if err := requireRevert(h.estimateNative(e1, alice, oneWei), "PROOF 1", "NoSelectorSpecified", "AA23"); err != nil {
		return err
	}
	fmt.Println()

	// ── Proofs 2–5, 7, pin: mixed grant ────────────────────────────────
	e2 := e1 + 1
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
	after2, err := readNativeLimit(ctx, chain, e2, account)
	if err != nil {
		return err
	}
	if after2.Sign() == 0 {
		return fmt.Errorf("PROOF 2 FAIL: NT limits still 0 after successful native send — module did not install")
	}
	signedGas := signedGasWei(op2)
	delta := new(big.Int).Sub(nativeCapX, after2)
	fmt.Printf("  K7 NT delta=%s signedGasWei=%s (want similar)\n", delta, signedGas)

	rSS, opSS, err := h.sendNative(e2, alice, oneWei, 200_000)
	if err != nil || rSS == nil || !rSS.Success {
		return fmt.Errorf("PROOF 8 steady-state ethTransfer failed: %v", err)
	}
	fmt.Printf("PROOF 8 steady-state ethTransfer gasUsed=%s vgl=%s (expect ~100k–200k VGL, not 700k)\n",
		rSS.ActualGasUsed, opSS.VerificationGasLimit)
	h.noteGas("steady-state ethTransfer", rSS, opSS)

	if err := requireRevert(h.estimateNative(e2, bob, oneWei), "PROOF 3", "AddressNotAllowed", "AA23"); err != nil {
		return err
	}
	fmt.Println()

	remaining, err := readNativeLimit(ctx, chain, e2, account)
	if err != nil {
		return err
	}
	fmt.Printf("  NT remaining after first send: %s wei\n", remaining)
	over := new(big.Int).Add(remaining, oneWei)
	err4a := h.estimateNative(e2, alice, over)
	if err4a != nil {
		if err := requireRevert(err4a, "PROOF 4a remaining+1", "ExceededNativeTokenLimit", "execution reverted"); err != nil {
			return err
		}
	} else {
		r, _, sendErr := h.sendNative(e2, alice, over, 200_000)
		if sendErr == nil && r != nil && r.Success {
			return fmt.Errorf("PROOF 4a FAIL: remaining+1 succeeded")
		}
		if sendErr == nil && r == nil {
			return fmt.Errorf("PROOF 4a FAIL: no receipt and no error")
		}
		if err := requireRevert(sendErr, "PROOF 4a remaining+1", "ExceededNativeTokenLimit", "execution reverted"); err != nil && r != nil && !r.Success {
			fmt.Printf("PROOF 4a PASS (mined success=false)\n")
		} else if err != nil {
			return err
		}
	}
	err4b := h.estimateNative(e2, alice, remaining)
	if err4b != nil {
		if err := requireRevert(err4b, "PROOF 4b remaining (gas)", "ExceededNativeTokenLimit", "AA23", "execution reverted"); err != nil {
			return err
		}
	} else {
		r, _, sendErr := h.sendNative(e2, alice, remaining, 200_000)
		if sendErr == nil && r != nil && r.Success {
			return fmt.Errorf("PROOF 4b FAIL: value==remaining succeeded")
		}
		if sendErr == nil && r == nil {
			return fmt.Errorf("PROOF 4b FAIL: no receipt and no error")
		}
		if err := requireRevert(sendErr, "PROOF 4b remaining (gas)", "ExceededNativeTokenLimit", "AA23", "execution reverted"); err != nil && !(r != nil && !r.Success) {
			return err
		}
		if r != nil && !r.Success {
			fmt.Printf("PROOF 4b PASS (mined success=false)\n")
		}
	}
	fmt.Println()

	// Token transfer (approve/transfer) to the dummy token would revert at
	// the token. Probe 5 is packing: native row must not set
	// HasSelectorAllowlist=false on the token target. Re-read by decoding
	// is covered by unit tests; on-chain: empty-calldata to the TOKEN
	// (selector-scoped) must still refuse.
	if err := requireRevert(h.estimateNative(e2, token, oneWei), "PROOF 5", "NoSelectorSpecified", "AA23"); err != nil {
		return err
	}
	fmt.Println()

	// ── Proof 7: cannot self-admin ──────────────────────────────────────
	installCall, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: 99, Signer: controllerAddr, Global: true,
		Hooks: [][]byte{aa.AllowlistExecHook(99)},
	})
	if err != nil {
		return err
	}
	if err := requireRevert(h.estimateRaw(e2, installCall, 200_000), "PROOF 7a",
		"SpendingRequestNotAllowed", "RequireUserOperationContext", "AA23"); err != nil {
		return err
	}
	upd := packUpdateLimits(e2, big.NewInt(0))
	execNT, err := aa.PackExecute(nativeModule(), big.NewInt(0), upd)
	if err != nil {
		return err
	}
	if err := requireRevert(h.estimateExec(e2, execNT, 200_000), "PROOF 7b", "AddressNotAllowed", "AA23"); err != nil {
		return err
	}
	fmt.Println()

	// ── Proof 6: val-then-exec clears NT; flat reverse does not ─────────
	before6, err := readNativeLimit(ctx, chain, e2, account)
	if err != nil {
		return err
	}
	if before6.Sign() == 0 {
		return fmt.Errorf("PROOF 6 FAIL: limits already 0 before uninstall")
	}
	if err := h.ownerUninstall(e2, mixed, true); err != nil {
		return fmt.Errorf("proof 6 uninstall: %w", err)
	}
	left, err := readNativeLimit(ctx, chain, e2, account)
	if err != nil {
		return err
	}
	if left.Sign() != 0 {
		return fmt.Errorf("PROOF 6 FAIL: limits after val-then-exec uninstall = %s (want 0)", left)
	}
	fmt.Printf("PROOF 6a PASS: val-then-exec cleared limits (was %s)\n", before6)

	e6 := e2 + 4
	hooks6, err := mixedHooks(e6, token, alice, nativeCapX)
	if err != nil {
		return err
	}
	r6, _, err := h.installDeferredNative(e6, hooks6, alice, oneWei, 900_000)
	if err != nil || r6 == nil || !r6.Success {
		return fmt.Errorf("proof 6 negative-control install: %v", err)
	}
	beforeFlat, err := readNativeLimit(ctx, chain, e6, account)
	if err != nil {
		return err
	}
	if beforeFlat.Sign() == 0 {
		return fmt.Errorf("PROOF 6 FAIL: flat-reverse control never installed NT")
	}
	if err := h.ownerUninstall(e6, hooks6, false); err != nil {
		return fmt.Errorf("proof 6 flat reverse: %w", err)
	}
	afterFlat, err := readNativeLimit(ctx, chain, e6, account)
	if err != nil {
		return err
	}
	if afterFlat.Sign() == 0 {
		return fmt.Errorf("PROOF 6 FAIL: flat reverse cleared limits — K10 would be unnecessary")
	}
	fmt.Printf("PROOF 6b PASS: flat reverse stranded limits at %s (was %s)\n\n", afterFlat, beforeFlat)

	// ── Proof 9: nativeValueCap (no native recipients) ──────────────────
	e5 := e1 + 4
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
	if err := requireRevert(h.estimateNative(e5, alice, oneWei), "PROOF 9b",
		"AddressNotAllowed", "NoSelectorSpecified", "AA23"); err != nil {
		return err
	}
	fmt.Println()

	// ── Proof 8: 20-row + replace teardown (A2-blocking, not skippable) ─
	fmt.Println("PROOF 8 gas table (self-funded; production eip1559.SuggestFee):")
	e8 := e1 + 7
	wide, err := wideNativeHooks(e8, 20, nativeCapX)
	if err != nil {
		return err
	}
	r8, op8, err := h.installDeferredNative(e8, wide, wideRecipient(0), oneWei, 1_500_000)
	if err != nil {
		return fmt.Errorf("PROOF 8 20-row first-op failed (K14): %w", err)
	}
	fmt.Printf("  20-row first-op success=%v gasUsed=%s vgl=%s tx=%s\n",
		r8.Success, r8.ActualGasUsed, op8.VerificationGasLimit, r8.TxHash)
	h.noteGas("first-op 20 native recipients", r8, op8)

	e9 := e8 + 1
	wide2, err := wideNativeHooks(e9, 20, nativeCapX)
	if err != nil {
		return err
	}
	uninst, err := packUninstallCall(e8, wide, true)
	if err != nil {
		return err
	}
	inst9, err := h.installCall(e9, wide2)
	if err != nil {
		return err
	}
	batch, err := aa.PackExecuteBatchMAv2([]aa.Call{
		{Target: h.account, Data: uninst},
		{Target: h.account, Data: inst9},
	})
	if err != nil {
		return err
	}
	execNew, err := aa.PackExecute(wideRecipient(0), oneWei, nil)
	if err != nil {
		return err
	}
	rRep, opRep, err := h.deferredOp(e9, batch, execNew, 1_800_000)
	if err != nil {
		return fmt.Errorf("PROOF 8 replace+20-row teardown failed (K14): %w", err)
	}
	fmt.Printf("  replace 20-row+5-hook teardown success=%v gasUsed=%s vgl=%s tx=%s\n",
		rRep.Success, rRep.ActualGasUsed, opRep.VerificationGasLimit, rRep.TxHash)
	h.noteGas("first-op replace 20-row 5-hook teardown", rRep, opRep)
	left8, err := readNativeLimit(ctx, chain, e8, account)
	if err != nil {
		return err
	}
	if left8.Sign() != 0 {
		fmt.Printf("  NOTE: old 20-row entity limits still %s after replace\n", left8)
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

func signedGasWei(op *userop.UserOperationV07) *big.Int {
	if op == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Mul(
		new(big.Int).Add(new(big.Int).Add(op.CallGasLimit, op.VerificationGasLimit), op.PreVerificationGas),
		op.MaxFeePerGas,
	)
}

func (h *harness) noteGas(label string, r *userOpReceipt, op *userop.UserOperationV07) {
	if r == nil || op == nil {
		return
	}
	fmt.Printf("  GAS %s actual=%s cgl=%s vgl=%s pvg=%s maxFee=%s signedGasWei=%s\n",
		label, r.ActualGasUsed, op.CallGasLimit, op.VerificationGasLimit, op.PreVerificationGas, op.MaxFeePerGas, signedGasWei(op))
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
	r, _, err := h.installDeferredExec(entity, hooks, exec, vgl)
	if err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("install: no UserOp receipt")
	}
	fmt.Printf("  install mined success=%v tx=%s\n", r.Success, r.TxHash)
	return nil
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
	return h.deferredOp(entity, installCall, exec, vgl)
}

func (h *harness) deferredOp(entity uint32, deferredCall, exec []byte, vgl int64) (*userOpReceipt, *userop.UserOperationV07, error) {
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
	digest, err := userop.DeferredActionDigest(h.chainID, h.account, carrierNonce, deadline, deferredCall)
	if err != nil {
		return nil, nil, err
	}
	ownerSig, err := crypto.Sign(digest.Bytes(), h.ownerKey)
	if err != nil {
		return nil, nil, err
	}
	ownerSig[64] += 27
	encodedData, err := userop.EncodeDeferredActionData(userop.FallbackSignerLocator(), deadline, deferredCall)
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
	maxFee, tip, err := eip1559.SuggestFee(h.chain)
	if err != nil {
		return err
	}
	op.MaxPriorityFeePerGas = tip
	op.MaxFeePerGas = maxFee
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

func packUninstallCall(entity uint32, installHooks [][]byte, valThenExec bool) ([]byte, error) {
	teardown, err := hookTeardown(entity, installHooks, valThenExec)
	if err != nil {
		return nil, err
	}
	return aa.PackSessionSignerUninstall(entity, teardown)
}

func hookTeardown(entity uint32, installHooks [][]byte, valThenExec bool) ([][]byte, error) {
	payload := func(entry []byte) ([]byte, error) {
		if len(entry) < 25 {
			return nil, fmt.Errorf("hook too short")
		}
		mod := common.BytesToAddress(entry[:20])
		data := entry[25:]
		if mod == nativeModule() && entry[24]&aa.HookFlagValidation != 0 {
			return packNTUninstallData(entity)
		}
		if len(data) == 0 {
			return nil, nil
		}
		return append([]byte(nil), data...), nil
	}
	if !valThenExec {
		out := make([][]byte, 0, len(installHooks))
		for i := len(installHooks) - 1; i >= 0; i-- {
			p, err := payload(installHooks[i])
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return out, nil
	}
	var val, exec [][]byte
	for _, entry := range installHooks {
		p, err := payload(entry)
		if err != nil {
			return nil, err
		}
		if entry[24]&aa.HookFlagValidation != 0 {
			val = append(val, p)
		} else {
			exec = append(exec, p)
		}
	}
	out := make([][]byte, 0, len(val)+len(exec))
	for i := len(val) - 1; i >= 0; i-- {
		out = append(out, val[i])
	}
	for i := len(exec) - 1; i >= 0; i-- {
		out = append(out, exec[i])
	}
	return out, nil
}

func (h *harness) ownerUninstall(entity uint32, installHooks [][]byte, valThenExec bool) error {
	call, err := packUninstallCall(entity, installHooks, valThenExec)
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

func (h *harness) sweepToOwner() error {
	bal, err := h.chain.BalanceAt(h.ctx, h.account, nil)
	if err != nil || bal == nil || bal.Cmp(big.NewInt(200_000_000_000_000)) < 0 {
		return err
	}
	leave := big.NewInt(100_000_000_000_000) // 0.0001 ETH
	amt := new(big.Int).Sub(bal, leave)
	call, err := aa.PackExecute(h.ownerAddr, amt, nil)
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
		return fmt.Errorf("sweep estimate: %w", err)
	}
	tx := types.NewTransaction(nonce, h.account, big.NewInt(0), gas+30_000, gasPrice, call)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(h.chainID), h.ownerKey)
	if err != nil {
		return err
	}
	if err := h.chain.SendTransaction(h.ctx, signed); err != nil {
		return fmt.Errorf("sweep: %w", err)
	}
	if _, err := waitMined(h.ctx, h.chain, signed.Hash()); err != nil {
		return err
	}
	fmt.Printf("swept %s wei back to owner tx=%s\n", amt, signed.Hash())
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
