// Selector-scoped grant spike — the lighter alternative to global+hooks.
//
// §3.5 R1 showed a bare GLOBAL grant can self-administer: the controller
// uninstalls its own entity, because a global validation applies to ANY
// account selector, including installValidation/uninstallValidation. The fix
// SessionGrant.Validate() already enforces (a global grant needs an execution
// hook or explicit AllowSelfAdministration) has a second form it permits
// without hooks: scope the validation to specific account selectors.
//
// This proves that form on-chain. A grant scoped to execute/executeBatch —
// NOT global, no hooks — validates ordinary operations but is
// self-administration-proof: uninstallValidation's selector is not in the set,
// so the account refuses it at ValidationFunctionMissing. Cheaper than the
// policied grant (no allowlist/cap/time modules) and safe by construction.
//
// The nonce must NOT carry the global bit: that forces GLOBAL checking, which
// a selector-scoped entity fails. Ops under it use plain (entity, no-global)
// nonce options — the one thing that differs from every prior spike.
//
// Env: SPIKE_OWNER_KEY, SPIKE_CONTROLLER_KEY, SPIKE_BUNDLER_URL,
//
//	SPIKE_RPC_URL (opt), SPIKE_SALT (opt, default 7).
//
// Run: go run ./scripts/spike/selector_scoped
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

const defaultRPC = "https://ethereum-sepolia-rpc.publicnode.com"

var (
	prefundWei = big.NewInt(3_000_000_000_000_000) // 0.003 ETH

	selExecute      = [4]byte{0xb6, 0x1d, 0x27, 0xf6} // execute(address,uint256,bytes)
	selExecuteBatch = [4]byte{0x34, 0xfc, 0xd5, 0xbe} // executeBatch((address,uint256,bytes)[])
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
	salt, ok := new(big.Int).SetString(env("SPIKE_SALT", "7"), 10)
	if !ok {
		return fmt.Errorf("SPIKE_SALT not a decimal integer")
	}

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
	fmt.Printf("chain %s\nowner      %s\ncontroller %s\naccount    %s (salt %s)\n\n",
		chainID, owner, controller, account.Hex(), salt)
	// Idempotent: if the grant is already installed (a prior run), skip
	// straight to the probes. Only a deployed account WITHOUT the grant is a
	// stale-fixture error.
	alreadyGranted := false
	if code, cErr := chain.CodeAt(ctx, *account, nil); cErr != nil {
		return cErr
	} else if len(code) > 0 {
		if signer, sErr := readSigner(ctx, chain, entity, *account); sErr == nil && signer == controller {
			alreadyGranted = true
			fmt.Printf("selector-scoped grant already installed on %s — running probes only\n\n", account.Hex())
		} else {
			return fmt.Errorf("account %s deployed but not selector-scoped-granted — bump SPIKE_SALT", account)
		}
	}
	if err := sendETH(ctx, chain, chainID, ownerKey, owner, *account, prefundWei); err != nil {
		return err
	}

	// ── Install a SELECTOR-SCOPED grant (execute + executeBatch, no hooks) ──
	installCall, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: entity, Signer: controller,
		Selectors: [][4]byte{selExecute, selExecuteBatch}, // NOT global, no hooks
	})
	if err != nil {
		return err
	}

	execNoop, err := aa.PackExecute(owner, big.NewInt(0), nil) // 0xb61d27f6 — an allowed selector
	if err != nil {
		return err
	}

	if !alreadyGranted {
		// The install op's nonce carries the deferred bit but NOT the global
		// bit — the outer validation is the selector-scoped entity, which must
		// be checked by SELECTOR, and its callData (execute) is in the set.
		installNonce, err := userop.EncodeNonceMAv2(entity, userop.ValidationOptionDeferredAction, 0)
		if err != nil {
			return err
		}
		deadline := uint64(time.Now().Add(time.Hour).Unix())
		digest, err := userop.DeferredActionDigest(chainID, *account, installNonce, deadline, installCall)
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
		factory, factoryData, err := aa.GetInitCodeMAv2(owner, salt)
		if err != nil {
			return err
		}
		installOp := &userop.UserOperationV07{
			Sender: *account, Nonce: installNonce, Factory: &factory, FactoryData: factoryData,
			CallData: execNoop, VerificationGasLimit: big.NewInt(400_000),
		}
		if err := priceOp(ctx, chain, bundler, installOp, entryPoint, encodedData, ownerSig); err != nil {
			return fmt.Errorf("pricing install: %w", err)
		}
		if err := preset.SignUserOpV07Deferred(installOp, entryPoint, chainID, controllerKey, encodedData, ownerSig); err != nil {
			return err
		}
		hash, err := preset.SendUserOpV07(ctx, bundler, installOp, entryPoint)
		if err != nil {
			return fmt.Errorf("sending install: %w", err)
		}
		r, err := waitReceipt(ctx, bundler, hash)
		if err != nil {
			return err
		}
		fmt.Printf("install op mined: success=%v gas=%s\n", r.Success, r.ActualGasUsed)
		if !r.Success {
			return fmt.Errorf("install reverted")
		}
		if signer, sErr := readSigner(ctx, chain, entity, *account); sErr != nil {
			return sErr
		} else if signer != controller {
			return fmt.Errorf("post-install signer is %s, want the controller", signer)
		}
		fmt.Printf("selector-scoped grant installed: entity %d → controller, scope [execute, executeBatch], no hooks\n\n", entity)
	}

	// ── S-1: an allowed selector (execute) validates and mines ─────────────
	fmt.Println("S-1: ordinary execute op (an allowed selector)")
	if err := sendPlain(ctx, chain, chainRPC, bundler, entryPoint, chainID, *account,
		controllerKey, entity, execNoop, false); err != nil {
		return fmt.Errorf("S-1 must succeed — execute is in scope: %w", err)
	}
	fmt.Println()

	// ── S-2: self-administration (uninstallValidation) is refused ──────────
	fmt.Println("S-2: self-administration attempt — controller calls uninstallValidation")
	uninstallCall, err := aa.PackSessionSignerUninstall(entity, nil)
	if err != nil {
		return err
	}
	errS2 := sendPlain(ctx, chain, chainRPC, bundler, entryPoint, chainID, *account,
		controllerKey, entity, uninstallCall, false)
	if errS2 == nil {
		return fmt.Errorf("S-2 unexpectedly succeeded — a selector-scoped grant self-administered?")
	}
	fmt.Printf("  refused, as required (uninstallValidation's selector is not in scope)\n  %s\n\n", firstLine(errS2.Error()))

	fmt.Println("PROVEN: a grant scoped to execute/executeBatch validates ordinary operations")
	fmt.Println("but CANNOT self-administer — uninstallValidation's selector is out of scope,")
	fmt.Println("so the account refuses it at validation. Self-administration-proof without an")
	fmt.Println("execution hook — the lighter alternative to global+hooks (§3.5).")
	return nil
}

// sendPlain builds and sends an ordinary (non-deferred) controller-signed op
// under a SELECTOR-SCOPED entity — nonce options carry neither the global nor
// the deferred bit.
func sendPlain(ctx context.Context, chain *ethclient.Client, chainRPC, bundler *rpc.Client,
	entryPoint common.Address, chainID *big.Int, account common.Address,
	controllerKey *ecdsa.PrivateKey, entity uint32, callData []byte, wrap bool) error {

	const noGlobalNoDeferred = uint8(0)
	nonce, err := preset.NextNonceV07(ctx, chainRPC, entryPoint, account, entity, noGlobalNoDeferred)
	if err != nil {
		return err
	}
	op := &userop.UserOperationV07{Sender: account, Nonce: nonce, CallData: callData, VerificationGasLimit: big.NewInt(150_000)}
	if err := priceOp(ctx, chain, bundler, op, entryPoint, nil, nil); err != nil {
		return err
	}
	if err := preset.SignUserOpV07(op, entryPoint, chainID, controllerKey); err != nil {
		return err
	}
	// A no-op execute's verification lands right at Rundler's 0.4 efficiency
	// floor, so use the tighten-and-re-sign retry rather than the one-shot.
	hash, err := preset.SendUserOpV07WithRetry(ctx, bundler, op, entryPoint, chainID, controllerKey)
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

func readSigner(ctx context.Context, chain *ethclient.Client, entity uint32, account common.Address) (common.Address, error) {
	sel := crypto.Keccak256([]byte("signers(uint32,address)"))[:4]
	data := append(sel, common.LeftPadBytes(big.NewInt(int64(entity)).Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(account.Bytes(), 32)...)
	module := aa.SingleSignerValidationModuleAddress()
	out, err := chain.CallContract(ctx, ethereum.CallMsg{To: &module, Data: data}, nil)
	if err != nil || len(out) < 32 {
		return common.Address{}, fmt.Errorf("reading signer: %w", err)
	}
	return common.BytesToAddress(out[12:32]), nil
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
	for range 60 {
		if rc, rErr := chain.TransactionReceipt(ctx, signed.Hash()); rErr == nil {
			if rc.Status != types.ReceiptStatusSuccessful {
				return fmt.Errorf("prefund tx failed")
			}
			fmt.Printf("prefunded %s wei\n\n", amount)
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("prefund tx not mined in 3m")
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
