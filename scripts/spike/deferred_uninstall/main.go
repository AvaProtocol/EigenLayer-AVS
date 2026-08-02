// Deferred-uninstall spike — the revoke composition §6.4 depends on.
//
// A policied grant cannot be revoked by the controller (its own allowlist
// execution hook blocks any non-execute selector — results doc §3.5 R4). The
// question this answers: can the OWNER revoke it with a deferred action —
// gaslessly, the way they GRANT it — instead of a plain transaction (§3.5 R3)?
//
// The mechanics (ModularAccountBase v2.0.1) decide it. A deferred-action op
// carries two signatures: the INNER authorizes the deferred call (via 1271,
// against the locator in the encoded data), the OUTER authorizes the op (via
// the nonce's locator). The deferred call runs DURING validation, before the
// outer validation — the opposite of a plain op, whose call runs in execution
// AFTER validation (which is why §3.5 R1's plain self-uninstall worked). And
// _handleDeferredAction runs only the INNER validation's hooks, so a
// fallback-authorized uninstall is NOT blocked by the policied entity's exec
// hook.
//
// So two configurations, tested on-chain:
//
//	U-A  outer = the policied entity itself. The deferred call removes it
//	     mid-validation, then the outer validation runs against a validation
//	     that no longer exists. PREDICT: fails.
//	U-B  outer = the owner's fallback. Survives the uninstall; the owner
//	     signs both the deferred digest AND the op. PREDICT: succeeds, and
//	     bypasses the exec hook that blocked the controller in §3.5 R4 —
//	     but the owner signs the OP, so it is not gaslessly pre-signable the
//	     way a grant is.
//
// Env: SPIKE_OWNER_KEY, SPIKE_CONTROLLER_KEY, SPIKE_BUNDLER_URL,
//
//	SPIKE_RPC_URL (opt), SPIKE_SALT (opt, default 5),
//	SPIKE_TOKEN (opt, default the §3.4 SpikeToken).
//
// Run: go run ./scripts/spike/deferred_uninstall
package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
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
	defaultRPC   = "https://ethereum-sepolia-rpc.publicnode.com"
	defaultToken = "0xaA4D01B75fdEB5fbbD98276EC7755eF71801c2E7"
)

var (
	prefundWei = big.NewInt(4_000_000_000_000_000) // 0.004 ETH
	oneToken   = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
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

func requireKey(name string) (*ecdsa.PrivateKey, common.Address, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return nil, common.Address{}, fmt.Errorf("%s is not set", name)
	}
	key, err := crypto.HexToECDSA(trim0x(raw))
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("parsing %s: %w", name, err)
	}
	return key, crypto.PubkeyToAddress(key.PublicKey), nil
}

func trim0x(s string) string {
	if len(s) >= 2 && s[:2] == "0x" {
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

func run() error {
	ctx := context.Background()
	ownerKey, owner, err := requireKey("SPIKE_OWNER_KEY")
	if err != nil {
		return err
	}
	controllerKey, controller, err := requireKey("SPIKE_CONTROLLER_KEY")
	if err != nil {
		return err
	}
	bundlerURL := os.Getenv("SPIKE_BUNDLER_URL")
	if bundlerURL == "" {
		return fmt.Errorf("SPIKE_BUNDLER_URL is not set")
	}
	salt, ok := new(big.Int).SetString(env("SPIKE_SALT", "5"), 10)
	if !ok {
		return fmt.Errorf("SPIKE_SALT not a decimal integer")
	}
	token := common.HexToAddress(env("SPIKE_TOKEN", defaultToken))

	chain, err := ethclient.Dial(env("SPIKE_RPC_URL", defaultRPC))
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
	entity := aa.MinSessionEntityID

	account, err := aa.GetSenderAddressMAv2(chain, owner, salt)
	if err != nil {
		return err
	}
	fmt.Printf("chain %s\nowner      %s\ncontroller %s\naccount    %s (salt %s)\ntoken      %s\n\n",
		chainID, owner, controller, account.Hex(), salt, token)

	if code, cErr := chain.CodeAt(ctx, *account, nil); cErr != nil {
		return cErr
	} else if len(code) > 0 {
		return fmt.Errorf("account %s already deployed — bump SPIKE_SALT", account)
	}

	// Seed SPIKE + ETH so the account can carry a policied grant and pay gas.
	if err := transferToken(ctx, chain, chainID, ownerKey, owner, token, *account, tokens(1000)); err != nil {
		return err
	}
	if err := sendETH(ctx, chain, chainID, ownerKey, owner, *account, prefundWei); err != nil {
		return err
	}

	// ── Install a policied grant (deploy + install signer + hooks) ─────────
	validUntil := uint64(time.Now().Add(24 * time.Hour).Unix())
	allowHook, err := aa.AllowlistValidationHook(entity, []aa.AllowlistInput{{
		Target: token, HasSelectorAllowlist: true, HasERC20SpendLimit: true,
		ERC20SpendLimit: tokens(100), Selectors: [][4]byte{{0xa9, 0x05, 0x9c, 0xbb}},
	}})
	if err != nil {
		return err
	}
	timeHook, err := aa.TimeRangeValidationHook(entity, validUntil, 0)
	if err != nil {
		return err
	}
	installCall, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: entity, Signer: controller, Global: true,
		Hooks: [][]byte{allowHook, aa.AllowlistExecHook(entity), timeHook},
	})
	if err != nil {
		return err
	}
	if err := installPoliciedGrant(ctx, chain, chainRPC, bundler, entryPoint, chainID,
		*account, owner, ownerKey, controllerKey, entity, installCall, salt, token, controller); err != nil {
		return fmt.Errorf("installing the policied grant: %w", err)
	}
	if signer, sErr := readSigner(ctx, chain, entity, *account); sErr != nil {
		return sErr
	} else if signer != controller {
		return fmt.Errorf("post-install signer is %s, want the controller", signer)
	}
	fmt.Printf("policied grant installed: entity %d → controller, cap %s SPIKE, expiry set\n\n",
		entity, inTokens(tokens(100)))

	// The uninstall calldata (with hook cleanup in stored order — validation
	// hooks reverse-install [timeRange, allowlist], then the exec hook).
	timeCleanup, err := aa.PackTimeRangeUninstallData(entity)
	if err != nil {
		return err
	}
	allowCleanup, err := aa.PackAllowlistInstallData(entity, []aa.AllowlistInput{{
		Target: token, HasSelectorAllowlist: true, HasERC20SpendLimit: true,
		ERC20SpendLimit: tokens(100), Selectors: [][4]byte{{0xa9, 0x05, 0x9c, 0xbb}},
	}})
	if err != nil {
		return err
	}
	uninstallCall, err := aa.PackSessionSignerUninstall(entity, [][]byte{timeCleanup, allowCleanup, nil})
	if err != nil {
		return err
	}

	// ── U-A: deferred uninstall, OUTER = the policied entity (should fail) ──
	fmt.Println("U-A: deferred uninstall with outer validation = the policied entity")
	errA := attemptDeferredUninstall(ctx, chain, chainRPC, bundler, entryPoint, chainID,
		*account, owner, ownerKey, controllerKey, entity, entity, controllerKey, uninstallCall, true)
	if errA == nil {
		return fmt.Errorf("U-A unexpectedly succeeded — the entity validated its own removal?")
	}
	fmt.Printf("  refused, as predicted (entity is removed mid-validation, before the outer check)\n  %s\n\n", firstLine(errA.Error()))

	// ── U-B: deferred uninstall, OUTER = owner fallback (should succeed) ───
	fmt.Println("U-B: deferred uninstall with outer validation = the owner's fallback signer")
	errB := attemptDeferredUninstall(ctx, chain, chainRPC, bundler, entryPoint, chainID,
		*account, owner, ownerKey, controllerKey, userop.FallbackSignerEntityID, entity, ownerKey, uninstallCall, false)
	if errB != nil {
		return fmt.Errorf("U-B failed — owner-authorized deferred uninstall should succeed: %w", errB)
	}
	if signer, sErr := readSigner(ctx, chain, entity, *account); sErr != nil {
		return sErr
	} else if signer != (common.Address{}) {
		return fmt.Errorf("entity %d still names %s after U-B", entity, signer)
	}
	limit, err := readSpendLimit(ctx, chain, entity, token, *account)
	if err != nil {
		return err
	}
	fmt.Printf("  succeeded: entity %d signer cleared, cap read back %s — grant revoked\n\n", entity, inTokens(limit))

	fmt.Println("PROVEN: a policied grant the CONTROLLER cannot revoke (its exec hook blocks it,")
	fmt.Println("§3.5 R4) IS revocable by the OWNER via a deferred uninstall — the fallback-")
	fmt.Println("authorized deferred call bypasses that hook. But the outer op cannot be the")
	fmt.Println("entity being removed (U-A), so the owner must sign the OP too (U-B): revoke is")
	fmt.Println("owner-authorized, NOT gaslessly pre-signable the way a grant is.")
	return nil
}

// installPoliciedGrant deploys the account and installs the grant via the
// owner's deferred action, controller-signed (the §3.4 flow).
func installPoliciedGrant(ctx context.Context, chain *ethclient.Client, chainRPC, bundler *rpc.Client,
	entryPoint common.Address, chainID *big.Int, account, owner common.Address,
	ownerKey, controllerKey *ecdsa.PrivateKey, entity uint32, installCall []byte, salt *big.Int,
	token, controller common.Address) error {

	opts := uint8(userop.ValidationOptionGlobal | userop.ValidationOptionDeferredAction)
	nonce, err := userop.EncodeNonceMAv2(entity, opts, 0)
	if err != nil {
		return err
	}
	deadline := uint64(time.Now().Add(time.Hour).Unix())
	digest, err := userop.DeferredActionDigest(chainID, account, nonce, deadline, installCall)
	if err != nil {
		return err
	}
	ownerSig, err := crypto.Sign(digest.Bytes(), ownerKey)
	if err != nil {
		return err
	}
	ownerSig[64] += 27
	encodedData, err := userop.EncodeDeferredActionData(userop.FallbackSignerLocator(), deadline, installCall)
	if err != nil {
		return err
	}

	// The op's outer validation is the newly-installed policied entity, whose
	// allowlist VALIDATION hook checks this callData — so it must be an
	// allowlisted action (a token transfer), not an arbitrary call.
	transfer := append([]byte{0xa9, 0x05, 0x9c, 0xbb}, common.LeftPadBytes(controller.Bytes(), 32)...)
	transfer = append(transfer, common.LeftPadBytes(oneToken.Bytes(), 32)...) // 1 SPIKE
	exec, err := aa.PackExecute(token, big.NewInt(0), transfer)
	if err != nil {
		return err
	}
	callData, err := aa.WrapExecuteUserOp(exec) // grant carries an exec hook
	if err != nil {
		return err
	}
	factory, factoryData, err := aa.GetInitCodeMAv2(owner, salt)
	if err != nil {
		return err
	}
	op := &userop.UserOperationV07{
		Sender: account, Nonce: nonce, Factory: &factory, FactoryData: factoryData,
		CallData: callData, VerificationGasLimit: big.NewInt(700_000),
	}
	if err := priceOp(ctx, chain, bundler, op, entryPoint, encodedData, ownerSig); err != nil {
		return err
	}
	if err := preset.SignUserOpV07Deferred(op, entryPoint, chainID, controllerKey, encodedData, ownerSig); err != nil {
		return err
	}
	hash, err := preset.SendUserOpV07(ctx, bundler, op, entryPoint)
	if err != nil {
		return err
	}
	r, err := waitReceipt(ctx, bundler, hash)
	if err != nil {
		return err
	}
	fmt.Printf("install op mined: success=%v gas=%s\n", r.Success, r.ActualGasUsed)
	if !r.Success {
		return fmt.Errorf("install reverted")
	}
	return nil
}

// attemptDeferredUninstall builds and sends a deferred-uninstall op:
//   - the OWNER signs the deferred action (inner = fallback) authorizing the
//     uninstall — gaslessly.
//   - the op's OUTER validation is `outerEntity`, signed by outerKey.
//
// wrap opts the callData into executeUserOp context (needed when the outer
// entity carries exec hooks). Returns the send/estimation error, or nil on a
// mined success.
func attemptDeferredUninstall(ctx context.Context, chain *ethclient.Client, chainRPC, bundler *rpc.Client,
	entryPoint common.Address, chainID *big.Int, account, owner common.Address,
	ownerKey, controllerKey *ecdsa.PrivateKey, outerEntity, uninstallEntity uint32,
	outerKey *ecdsa.PrivateKey, uninstallCall []byte, wrap bool) error {

	opts := uint8(userop.ValidationOptionGlobal | userop.ValidationOptionDeferredAction)
	nonce, err := preset.NextNonceV07(ctx, chainRPC, entryPoint, account, outerEntity, opts)
	if err != nil {
		return err
	}
	deadline := uint64(time.Now().Add(time.Hour).Unix())
	// The deferred digest commits to the FULL op nonce (outer entity + opts).
	digest, err := userop.DeferredActionDigest(chainID, account, nonce, deadline, uninstallCall)
	if err != nil {
		return err
	}
	ownerSig, err := crypto.Sign(digest.Bytes(), ownerKey) // inner = owner's fallback
	if err != nil {
		return err
	}
	ownerSig[64] += 27
	encodedData, err := userop.EncodeDeferredActionData(userop.FallbackSignerLocator(), deadline, uninstallCall)
	if err != nil {
		return err
	}

	exec, err := aa.PackExecute(owner, big.NewInt(0), nil) // outer op is a no-op; the uninstall rides the deferred action
	if err != nil {
		return err
	}
	callData := exec
	if wrap {
		if callData, err = aa.WrapExecuteUserOp(exec); err != nil {
			return err
		}
	}
	seed := big.NewInt(200_000)
	op := &userop.UserOperationV07{Sender: account, Nonce: nonce, CallData: callData, VerificationGasLimit: seed}
	if err := priceOp(ctx, chain, bundler, op, entryPoint, encodedData, ownerSig); err != nil {
		return err
	}
	if err := preset.SignUserOpV07Deferred(op, entryPoint, chainID, outerKey, encodedData, ownerSig); err != nil {
		return err
	}
	hash, err := preset.SendUserOpV07(ctx, bundler, op, entryPoint)
	if err != nil {
		return err
	}
	r, err := waitReceipt(ctx, bundler, hash)
	if err != nil {
		return err
	}
	fmt.Printf("  op mined: success=%v gas=%s tx=%s\n", r.Success, r.ActualGasUsed, r.TxHash)
	if !r.Success {
		return fmt.Errorf("op mined but reverted")
	}
	return nil
}

func priceOp(ctx context.Context, chain *ethclient.Client, bundler *rpc.Client,
	op *userop.UserOperationV07, entryPoint common.Address, encodedData, ownerSig []byte) error {
	tip, err := chain.SuggestGasTipCap(ctx)
	if err != nil {
		return err
	}
	var bundlerTipHex string
	if err := bundler.CallContext(ctx, &bundlerTipHex, "rundler_maxPriorityFeePerGas"); err == nil {
		if bt, ok := new(big.Int).SetString(trim0x(bundlerTipHex), 16); ok && bt.Cmp(tip) > 0 {
			tip = bt
		}
	}
	head, err := chain.HeaderByNumber(ctx, nil)
	if err != nil {
		return err
	}
	op.MaxPriorityFeePerGas = tip
	op.MaxFeePerGas = new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2)))
	if encodedData != nil {
		sig, sErr := preset.DeferredEstimationSignature(encodedData, ownerSig)
		if sErr != nil {
			return sErr
		}
		op.Signature = sig
		defer func() { op.Signature = nil }()
	}
	if _, err := preset.EstimateUserOpGasV07(ctx, bundler, op, entryPoint); err != nil {
		return fmt.Errorf("estimating gas: %w", err)
	}
	return nil
}

// ── on-chain reads / funding ────────────────────────────────────────────────

func tokens(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), oneToken) }
func inTokens(wei *big.Int) string {
	return new(big.Int).Div(wei, oneToken).String()
}

func callWords(ctx context.Context, chain *ethclient.Client, to common.Address, sig string, args ...[]byte) ([]byte, error) {
	data := crypto.Keccak256([]byte(sig))[:4]
	for _, a := range args {
		data = append(data, common.LeftPadBytes(a, 32)...)
	}
	return chain.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
}

func readSigner(ctx context.Context, chain *ethclient.Client, entity uint32, account common.Address) (common.Address, error) {
	out, err := callWords(ctx, chain, aa.SingleSignerValidationModuleAddress(),
		"signers(uint32,address)", big.NewInt(int64(entity)).Bytes(), account.Bytes())
	if err != nil || len(out) < 32 {
		return common.Address{}, fmt.Errorf("reading signer: %w", err)
	}
	return common.BytesToAddress(out[12:32]), nil
}

func readSpendLimit(ctx context.Context, chain *ethclient.Client, entity uint32, token, account common.Address) (*big.Int, error) {
	out, err := callWords(ctx, chain, aa.AllowlistModuleAddress(),
		"erc20SpendLimits(uint32,address,address)", big.NewInt(int64(entity)).Bytes(), token.Bytes(), account.Bytes())
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(out), nil
}

func transferToken(ctx context.Context, chain *ethclient.Client, chainID *big.Int,
	ownerKey *ecdsa.PrivateKey, owner, token, to common.Address, amount *big.Int) error {
	held, err := callWords(ctx, chain, token, "balanceOf(address)", to.Bytes())
	if err == nil && new(big.Int).SetBytes(held).Cmp(amount) >= 0 {
		return nil // already funded
	}
	nonce, err := chain.PendingNonceAt(ctx, owner)
	if err != nil {
		return err
	}
	gp, err := chain.SuggestGasPrice(ctx)
	if err != nil {
		return err
	}
	data := append([]byte{0xa9, 0x05, 0x9c, 0xbb}, common.LeftPadBytes(to.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(amount.Bytes(), 32)...)
	tx := types.NewTransaction(nonce, token, big.NewInt(0), 80_000, gp, data)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), ownerKey)
	if err != nil {
		return err
	}
	if err := chain.SendTransaction(ctx, signed); err != nil {
		return err
	}
	_, err = waitMined(ctx, chain, signed.Hash())
	return err
}

func sendETH(ctx context.Context, chain *ethclient.Client, chainID *big.Int,
	ownerKey *ecdsa.PrivateKey, owner, to common.Address, amount *big.Int) error {
	if bal, err := chain.BalanceAt(ctx, to, nil); err == nil && bal.Cmp(amount) >= 0 {
		return nil
	}
	nonce, err := chain.PendingNonceAt(ctx, owner)
	if err != nil {
		return err
	}
	gp, err := chain.SuggestGasPrice(ctx)
	if err != nil {
		return err
	}
	gl, err := chain.EstimateGas(ctx, ethereum.CallMsg{From: owner, To: &to, Value: amount})
	if err != nil {
		return err
	}
	tx := types.NewTransaction(nonce, to, amount, gl+10_000, gp, nil)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), ownerKey)
	if err != nil {
		return err
	}
	if err := chain.SendTransaction(ctx, signed); err != nil {
		return err
	}
	_, err = waitMined(ctx, chain, signed.Hash())
	return err
}

func waitMined(ctx context.Context, chain *ethclient.Client, hash common.Hash) (*types.Receipt, error) {
	for range 60 {
		if r, err := chain.TransactionReceipt(ctx, hash); err == nil {
			if r.Status != types.ReceiptStatusSuccessful {
				return nil, fmt.Errorf("tx %s failed", hash)
			}
			return r, nil
		}
		time.Sleep(3 * time.Second)
	}
	return nil, fmt.Errorf("tx %s not mined in 3m", hash)
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
			var p struct {
				Success       bool   `json:"success"`
				ActualGasUsed string `json:"actualGasUsed"`
				Receipt       struct {
					TransactionHash string `json:"transactionHash"`
				} `json:"receipt"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			g, _ := new(big.Int).SetString(trim0x(p.ActualGasUsed), 16)
			return &userOpReceipt{Success: p.Success, ActualGasUsed: g, TxHash: p.Receipt.TransactionHash}, nil
		}
		time.Sleep(4 * time.Second)
	}
	return nil, fmt.Errorf("no receipt for %s in 3m", opHash)
}
