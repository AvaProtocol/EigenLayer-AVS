// Revoke spike — the last unproven line of master §5: uninstallValidation,
// which §6.4's revoke screen depends on. Four probes across the two accounts
// the earlier spikes left behind, answering WHO can revoke, not just whether
// the call works:
//
//	R1  bare grant (salt 2): the CONTROLLER uninstalls itself — one
//	    controller-signed op, no owner involvement. This is §6.4's
//	    "backend uninstalls" one-click revoke. It works because
//	    uninstallValidation "may be validated by a global validation"
//	    (account source) — which cuts both ways: a bare global grant could
//	    equally call installValidation. Bare grants are self-modifiable
//	    BY DESIGN; the receipt here is the evidence.
//	R2  bare grant, after R1: a controller op is refused — authority is gone.
//	R4  policied grant (salt 3): the controller CANNOT self-revoke — the
//	    allowlist's execution hook reverts any selector that is not
//	    execute/executeBatch. Policied grants are not self-modifiable.
//	R3  policied grant: the OWNER revokes with a PLAIN transaction straight
//	    to the account (fallback direct-call validation) — no UserOp, no
//	    bundler. This is the backstop against a gateway that refuses to
//	    revoke. Hook cleanup data rides along in the account's STORED hook
//	    order (validation hooks in reverse install order, then exec hooks);
//	    module state is then read back to prove the cleanup landed, because
//	    a misrouted entry does not fail the transaction — it strands state
//	    silently.
//
// Environment: as the earlier spikes; SPIKE_BARE_SALT (default 2),
// SPIKE_POLICIED_SALT (default 3), SPIKE_TOKEN (default the §3.4 SpikeToken).
// The policied grant's uninstall payload must reproduce the §3.4 install
// inputs exactly — in production this comes from the stored install_call
// (master §7.4a), never re-derived.
//
// Run: go run ./scripts/spike/revoke
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

var oneToken = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

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

func run() error {
	ctx := context.Background()

	ownerKey, ownerAddr, err := requireKey("SPIKE_OWNER_KEY")
	if err != nil {
		return err
	}
	controllerKey, controllerAddr, err := requireKey("SPIKE_CONTROLLER_KEY")
	if err != nil {
		return err
	}
	bundlerURL := os.Getenv("SPIKE_BUNDLER_URL")
	if bundlerURL == "" {
		return fmt.Errorf("SPIKE_BUNDLER_URL is not set")
	}
	bareSalt, ok := new(big.Int).SetString(env("SPIKE_BARE_SALT", "2"), 10)
	if !ok {
		return fmt.Errorf("SPIKE_BARE_SALT is not a decimal integer")
	}
	policiedSalt, ok := new(big.Int).SetString(env("SPIKE_POLICIED_SALT", "3"), 10)
	if !ok {
		return fmt.Errorf("SPIKE_POLICIED_SALT is not a decimal integer")
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

	bare, err := aa.GetSenderAddressMAv2(chain, ownerAddr, bareSalt)
	if err != nil {
		return err
	}
	policied, err := aa.GetSenderAddressMAv2(chain, ownerAddr, policiedSalt)
	if err != nil {
		return err
	}
	fmt.Printf("chain %s\nowner      %s\ncontroller %s\nbare       %s (salt %s)\npolicied   %s (salt %s)\n\n",
		chainID, ownerAddr, controllerAddr, bare.Hex(), bareSalt, policied.Hex(), policiedSalt)

	// Preconditions: both grants must still be live.
	if signer, sErr := readSessionSigner(ctx, chain, entity, *bare); sErr != nil {
		return sErr
	} else if signer != controllerAddr {
		return fmt.Errorf("bare account's entity-%d signer is %s, want the controller — §3.2 state gone?", entity, signer)
	}
	if signer, sErr := readSessionSigner(ctx, chain, entity, *policied); sErr != nil {
		return sErr
	} else if signer != controllerAddr {
		return fmt.Errorf("policied account's entity-%d signer is %s, want the controller — §3.4 state gone?", entity, signer)
	}

	// ── R1: controller revokes itself on the bare grant ───────────────────
	uninstallBare, err := aa.PackSessionSignerUninstall(entity, nil) // no hooks installed, no cleanup entries
	if err != nil {
		return err
	}
	nonce1, err := preset.NextNonceV07(ctx, chainRPC, entryPoint, *bare, entity, userop.ValidationOptionGlobal)
	if err != nil {
		return err
	}
	op1 := &userop.UserOperationV07{
		Sender: *bare, Nonce: nonce1, CallData: uninstallBare,
		VerificationGasLimit: big.NewInt(150_000),
	}
	if err := priceOp(ctx, chain, bundler, op1, entryPoint); err != nil {
		return fmt.Errorf("pricing R1: %w", err)
	}
	if err := preset.SignUserOpV07(op1, entryPoint, chainID, controllerKey); err != nil {
		return err
	}
	// The retry variant handles Rundler's efficiency floor: an uninstall's
	// verification is light (~50k) and a comfortable seed lands under the
	// 0.4 actual/limit ratio, so the send must tighten and re-sign.
	hash1, err := preset.SendUserOpV07WithRetry(ctx, bundler, op1, entryPoint, chainID, controllerKey)
	if err != nil {
		return fmt.Errorf("sending R1: %w", err)
	}
	fmt.Printf("R1 sent: controller-signed uninstallValidation on the bare grant\n  userOpHash %s\n", hash1)
	r1, err := waitReceipt(ctx, bundler, hash1)
	if err != nil {
		return err
	}
	fmt.Printf("  mined: success=%v gasUsed=%s tx=%s\n", r1.Success, r1.ActualGasUsed, r1.TxHash)
	if !r1.Success {
		return fmt.Errorf("R1 reverted — controller self-revoke on a bare grant should succeed")
	}
	if signer, sErr := readSessionSigner(ctx, chain, entity, *bare); sErr != nil {
		return sErr
	} else if signer != (common.Address{}) {
		return fmt.Errorf("SSVM still names %s after R1", signer)
	}
	fmt.Printf("  module signer cleared: signers(%d, bare) = 0x0\n\n", entity)

	// ── R2: the controller's authority is gone ────────────────────────────
	probe, err := aa.PackExecute(ownerAddr, big.NewInt(0), nil)
	if err != nil {
		return err
	}
	nonce2, err := preset.NextNonceV07(ctx, chainRPC, entryPoint, *bare, entity, userop.ValidationOptionGlobal)
	if err != nil {
		return err
	}
	op2 := &userop.UserOperationV07{Sender: *bare, Nonce: nonce2, CallData: probe,
		VerificationGasLimit: big.NewInt(150_000)}
	if err := priceOp(ctx, chain, bundler, op2, entryPoint); err == nil {
		return fmt.Errorf("R2: a controller op was accepted AFTER revocation")
	} else {
		fmt.Printf("R2: controller op refused after revoke, as required\n  %s\n\n", firstLine(err.Error()))
	}

	// ── R4: the policied grant cannot self-revoke ─────────────────────────
	// Three hook entries exist; content is irrelevant because the allowlist
	// exec hook rejects the selector before the uninstall would run.
	uninstallPolicied, err := aa.PackSessionSignerUninstall(entity, [][]byte{nil, nil, nil})
	if err != nil {
		return err
	}
	wrapped, err := aa.WrapExecuteUserOp(uninstallPolicied)
	if err != nil {
		return err
	}
	nonce4, err := preset.NextNonceV07(ctx, chainRPC, entryPoint, *policied, entity, userop.ValidationOptionGlobal)
	if err != nil {
		return err
	}
	op4 := &userop.UserOperationV07{Sender: *policied, Nonce: nonce4, CallData: wrapped,
		VerificationGasLimit: big.NewInt(200_000)}
	if err := priceOp(ctx, chain, bundler, op4, entryPoint); err == nil {
		return fmt.Errorf("R4: the policied controller's self-revoke was NOT blocked")
	} else {
		fmt.Printf("R4: policied controller self-revoke blocked by the execution hook, as required\n  %s\n\n", firstLine(err.Error()))
	}

	// ── R3: the owner revokes the policied grant with a plain transaction ──
	// Hook cleanup in STORED order: validation hooks reversed from install
	// ([allowlist, timerange] installed → [timerange, allowlist] stored),
	// then the exec hook (empty: same module as the allowlist entry, state
	// already deleted there).
	timeRangeCleanup, err := aa.PackTimeRangeUninstallData(entity)
	if err != nil {
		return err
	}
	allowlistCleanup, err := aa.PackAllowlistInstallData(entity, []aa.AllowlistInput{{
		Target:               token,
		HasSelectorAllowlist: true,
		HasERC20SpendLimit:   true,
		ERC20SpendLimit:      new(big.Int).Mul(big.NewInt(100), oneToken),
		Selectors:            [][4]byte{{0xa9, 0x05, 0x9c, 0xbb}},
	}})
	if err != nil {
		return err
	}
	uninstallR3, err := aa.PackSessionSignerUninstall(entity, [][]byte{timeRangeCleanup, allowlistCleanup, nil})
	if err != nil {
		return err
	}
	txHash, gasUsed, err := sendOwnerTx(ctx, chain, chainID, ownerKey, ownerAddr, *policied, uninstallR3)
	if err != nil {
		return fmt.Errorf("R3: %w", err)
	}
	fmt.Printf("R3: owner revoked the policied grant with a PLAIN transaction (no UserOp, no bundler)\n  tx %s gasUsed=%d\n", txHash, gasUsed)

	// Cleanup verification — reads, not vibes.
	if signer, sErr := readSessionSigner(ctx, chain, entity, *policied); sErr != nil {
		return sErr
	} else if signer != (common.Address{}) {
		return fmt.Errorf("SSVM still names %s after R3", signer)
	}
	limit, err := readSpendLimit(ctx, chain, entity, token, *policied)
	if err != nil {
		return err
	}
	until, after, err := readTimeRange(ctx, chain, entity, *policied)
	if err != nil {
		return err
	}
	fmt.Printf("  module state cleared: signer=0x0, erc20SpendLimit=%s, timeRange=(%d,%d)\n\n", limit, until, after)
	if limit.Sign() != 0 || until != 0 || after != 0 {
		return fmt.Errorf("hook module state survived the uninstall — cleanup order wrong or onUninstall failed")
	}

	// ── Post-R3: the policied controller is dead too ──────────────────────
	probeWrapped, err := aa.WrapExecuteUserOp(probe)
	if err != nil {
		return err
	}
	nonce5, err := preset.NextNonceV07(ctx, chainRPC, entryPoint, *policied, entity, userop.ValidationOptionGlobal)
	if err != nil {
		return err
	}
	op5 := &userop.UserOperationV07{Sender: *policied, Nonce: nonce5, CallData: probeWrapped,
		VerificationGasLimit: big.NewInt(150_000)}
	if err := priceOp(ctx, chain, bundler, op5, entryPoint); err == nil {
		return fmt.Errorf("post-R3: a controller op was accepted AFTER the owner's revoke")
	} else {
		fmt.Printf("post-R3: controller op refused, as required\n  %s\n\n", firstLine(err.Error()))
	}

	fmt.Println("PROVEN: a bare grant one-click revokes itself (and is therefore")
	fmt.Println("self-modifiable — by design); a policied grant cannot touch its own")
	fmt.Println("authority; the owner revokes either kind with a plain transaction and")
	fmt.Println("the hook state provably clears. Revocation has receipts.")
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────

func priceOp(ctx context.Context, chain *ethclient.Client, bundler *rpc.Client,
	op *userop.UserOperationV07, entryPoint common.Address) error {

	tip, err := chain.SuggestGasTipCap(ctx)
	if err != nil {
		return err
	}
	var bundlerTipHex string
	if err := bundler.CallContext(ctx, &bundlerTipHex, "rundler_maxPriorityFeePerGas"); err == nil {
		if bundlerTip, ok := new(big.Int).SetString(trim0x(bundlerTipHex), 16); ok && bundlerTip.Cmp(tip) > 0 {
			tip = bundlerTip
		}
	}
	head, err := chain.HeaderByNumber(ctx, nil)
	if err != nil {
		return err
	}
	op.MaxPriorityFeePerGas = tip
	op.MaxFeePerGas = new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2)))
	if _, err := preset.EstimateUserOpGasV07(ctx, bundler, op, entryPoint); err != nil {
		return fmt.Errorf("estimating gas: %w", err)
	}
	return nil
}

func sendOwnerTx(ctx context.Context, chain *ethclient.Client, chainID *big.Int,
	ownerKey *ecdsa.PrivateKey, ownerAddr, to common.Address, data []byte) (common.Hash, uint64, error) {

	nonce, err := chain.PendingNonceAt(ctx, ownerAddr)
	if err != nil {
		return common.Hash{}, 0, err
	}
	gasPrice, err := chain.SuggestGasPrice(ctx)
	if err != nil {
		return common.Hash{}, 0, err
	}
	gasLimit, err := chain.EstimateGas(ctx, ethereum.CallMsg{From: ownerAddr, To: &to, Data: data})
	if err != nil {
		return common.Hash{}, 0, fmt.Errorf("estimating the owner's revoke tx: %w", err)
	}
	tx := types.NewTransaction(nonce, to, big.NewInt(0), gasLimit+20_000, gasPrice, data)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), ownerKey)
	if err != nil {
		return common.Hash{}, 0, err
	}
	if err := chain.SendTransaction(ctx, signed); err != nil {
		return common.Hash{}, 0, err
	}
	for range 60 {
		receipt, rErr := chain.TransactionReceipt(ctx, signed.Hash())
		if rErr == nil {
			if receipt.Status != types.ReceiptStatusSuccessful {
				return signed.Hash(), receipt.GasUsed, fmt.Errorf("owner revoke tx mined but failed")
			}
			return signed.Hash(), receipt.GasUsed, nil
		}
		time.Sleep(3 * time.Second)
	}
	return signed.Hash(), 0, fmt.Errorf("owner revoke tx not mined after 3 minutes")
}

func callWords(ctx context.Context, chain *ethclient.Client, to common.Address, sig string, args ...[]byte) ([]byte, error) {
	data := crypto.Keccak256([]byte(sig))[:4]
	for _, a := range args {
		data = append(data, common.LeftPadBytes(a, 32)...)
	}
	return chain.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
}

func readSessionSigner(ctx context.Context, chain *ethclient.Client, entity uint32, account common.Address) (common.Address, error) {
	out, err := callWords(ctx, chain, aa.SingleSignerValidationModuleAddress(),
		"signers(uint32,address)", big.NewInt(int64(entity)).Bytes(), account.Bytes())
	if err != nil || len(out) < 32 {
		return common.Address{}, fmt.Errorf("reading SSVM signer: %w", err)
	}
	return common.BytesToAddress(out[12:32]), nil
}

func readSpendLimit(ctx context.Context, chain *ethclient.Client, entity uint32, token, account common.Address) (*big.Int, error) {
	out, err := callWords(ctx, chain, aa.AllowlistModuleAddress(),
		"erc20SpendLimits(uint32,address,address)", big.NewInt(int64(entity)).Bytes(), token.Bytes(), account.Bytes())
	if err != nil {
		return nil, fmt.Errorf("reading erc20SpendLimits: %w", err)
	}
	return new(big.Int).SetBytes(out), nil
}

func readTimeRange(ctx context.Context, chain *ethclient.Client, entity uint32, account common.Address) (uint64, uint64, error) {
	out, err := callWords(ctx, chain, aa.TimeRangeModuleAddress(),
		"timeRanges(uint32,address)", big.NewInt(int64(entity)).Bytes(), account.Bytes())
	if err != nil || len(out) < 64 {
		return 0, 0, fmt.Errorf("reading timeRanges: %w", err)
	}
	return new(big.Int).SetBytes(out[:32]).Uint64(), new(big.Int).SetBytes(out[32:64]).Uint64(), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
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
			return &userOpReceipt{Success: parsed.Success, ActualGasUsed: gasUsed,
				TxHash: parsed.Receipt.TransactionHash}, nil
		}
		time.Sleep(4 * time.Second)
	}
	return nil, fmt.Errorf("no receipt for %s after 3 minutes", opHash)
}
