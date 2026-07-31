// Deferred-action spike — proves the §5.1 onboarding flow end to end on a
// live chain (avs-infra MA_v2_Authority_Model_And_Spike_Results.md):
//
//  1. The OWNER signs one EIP-712 deferred-action payload, off-chain, at
//     "grant time" — before any gas price or operation exists.
//  2. The CONTROLLER signs the first real UserOperation, which deploys the
//     account (initCode), applies the controller install during validation
//     (the deferred action), and executes — one operation, no owner UserOp
//     signature, ever.
//  3. A second, plain controller-signed operation proves the install
//     persists.
//
// Environment:
//
//	SPIKE_OWNER_KEY       hex private key of the owner EOA (signs the grant,
//	                      prefunds the account)
//	SPIKE_CONTROLLER_KEY  hex private key of the controller
//	SPIKE_BUNDLER_URL     v0.7-capable bundler (Alchemy Rundler)
//	SPIKE_RPC_URL         chain RPC (default: Sepolia publicnode)
//	SPIKE_SALT            account salt (default 2 — salts 0/1 were consumed
//	                      by the §3 spike)
//	SPIKE_DEADLINE_HOURS  grant deadline horizon (default 24)
//
// Run: go run ./scripts/spike/deferred_action
package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
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

// prefundWei covers both operations at Sepolia fee levels with headroom;
// the unspent remainder stays in the account.
var prefundWei = big.NewInt(2_000_000_000_000_000) // 0.002 ETH

// spikeEntityID is the entity this harness grants. Real grants allocate one
// per SessionPolicy; the spike only ever creates one.
const spikeEntityID = aa.MinSessionEntityID

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

	salt := big.NewInt(2)
	if s := os.Getenv("SPIKE_SALT"); s != "" {
		v, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return fmt.Errorf("SPIKE_SALT %q is not a decimal integer", s)
		}
		salt = v
	}
	deadlineHours, err := strconv.Atoi(env("SPIKE_DEADLINE_HOURS", "24"))
	if err != nil {
		return fmt.Errorf("SPIKE_DEADLINE_HOURS: %w", err)
	}

	chain, err := ethclient.Dial(env("SPIKE_RPC_URL", defaultRPC))
	if err != nil {
		return fmt.Errorf("dialing chain rpc: %w", err)
	}
	defer chain.Close()
	chainRPC := chain.Client()

	bundler, err := rpc.DialContext(ctx, bundlerURL)
	if err != nil {
		return fmt.Errorf("dialing bundler: %w", err)
	}
	defer bundler.Close()

	chainID, err := chain.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("reading chain id: %w", err)
	}
	entryPoint := preset.EntryPointV07()

	fmt.Printf("chain %s, entrypoint %s\n", chainID, entryPoint)
	fmt.Printf("owner      %s\ncontroller %s\nsalt       %s\n", ownerAddr, controllerAddr, salt)

	// ── The account ────────────────────────────────────────────────────────
	accountPtr, err := aa.GetSenderAddressMAv2(chain, ownerAddr, salt)
	if err != nil {
		return err
	}
	account := *accountPtr
	code, err := chain.CodeAt(ctx, account, nil)
	if err != nil {
		return err
	}
	alreadyDeployed := len(code) > 0
	if alreadyDeployed {
		fmt.Printf("account    %s already deployed — skipping grant + op A, proving persistence only\n\n", account)
	} else {
		fmt.Printf("account    %s (counterfactual, salt %s)\n\n", account, salt)
	}

	// ── Grant time: the owner's one off-chain signature ───────────────────
	// Everything the owner commits to is knowable before any operation or
	// gas price exists: the install call, the deadline, and the full nonce —
	// a fresh account's first sequence under the deferred key is zero.
	if alreadyDeployed {
		if err := ensurePrefund(ctx, chain, chainID, ownerKey, ownerAddr, account); err != nil {
			return err
		}
		return proveInstallPersists(ctx, chain, chainRPC, bundler, entryPoint, chainID,
			account, ownerAddr, controllerKey)
	}
	installCall, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: spikeEntityID,
		Signer:   controllerAddr,
	})
	if err != nil {
		return err
	}
	opts := uint8(userop.ValidationOptionGlobal | userop.ValidationOptionDeferredAction)
	nonce, err := userop.EncodeNonceMAv2(spikeEntityID, opts, 0)
	if err != nil {
		return err
	}
	// Cross-check the assumption rather than trusting it: the on-chain
	// sequence for this key must still be zero.
	liveNonce, err := preset.NextNonceV07(ctx, chainRPC, entryPoint, account, spikeEntityID, opts)
	if err != nil {
		return err
	}
	if liveNonce.Cmp(nonce) != 0 {
		return fmt.Errorf("live nonce %s != grant-time nonce %s — key already used", liveNonce, nonce)
	}

	deadline := uint64(time.Now().Add(time.Duration(deadlineHours) * time.Hour).Unix())
	digest, err := userop.DeferredActionDigest(chainID, account, nonce, deadline, installCall)
	if err != nil {
		return err
	}
	ownerSig, err := crypto.Sign(digest.Bytes(), ownerKey) // raw digest — no EIP-191 wrap
	if err != nil {
		return err
	}
	ownerSig[64] += 27
	encodedData, err := userop.EncodeDeferredActionData(userop.FallbackSignerLocator(), deadline, installCall)
	if err != nil {
		return err
	}
	fmt.Printf("grant signed (owner, off-chain, gasless)\n  712 digest %s\n  deadline   %s\n  nonce      0x%x\n\n",
		digest, time.Unix(int64(deadline), 0).UTC().Format(time.RFC3339), nonce)

	// ── Prefund the counterfactual account (it pays its own gas) ─────────
	if err := ensurePrefund(ctx, chain, chainID, ownerKey, ownerAddr, account); err != nil {
		return err
	}

	// ── First use: controller builds, signs, sends — deploy + install + execute ──
	callData, err := aa.PackExecute(ownerAddr, big.NewInt(0), nil)
	if err != nil {
		return err
	}
	factory, factoryData, err := aa.GetInitCodeMAv2(ownerAddr, salt)
	if err != nil {
		return err
	}
	opA := &userop.UserOperationV07{
		Sender:      account,
		Nonce:       nonce,
		Factory:     &factory,
		FactoryData: factoryData,
		CallData:    callData,
		// Deploy (~160k) + deferred install (~45k) + 1271 check must fit under
		// the seed, and Rundler echoes seeds rather than computing them.
		VerificationGasLimit: big.NewInt(300_000),
	}
	if err := priceOp(ctx, chain, bundler, opA, entryPoint, encodedData, ownerSig); err != nil {
		return err
	}
	if err := preset.SignUserOpV07Deferred(opA, entryPoint, chainID, controllerKey, encodedData, ownerSig); err != nil {
		return err
	}
	hashA, err := preset.SendUserOpV07(ctx, bundler, opA, entryPoint)
	if err != nil {
		return fmt.Errorf("sending op A (deploy + deferred install + execute): %w", err)
	}
	fmt.Printf("op A sent: deploy + deferred install + execute, controller-signed\n  userOpHash %s\n", hashA)
	receiptA, err := waitReceipt(ctx, bundler, hashA)
	if err != nil {
		return err
	}
	fmt.Printf("  mined: success=%v gasUsed=%s costWei=%s tx=%s\n\n",
		receiptA.Success, receiptA.ActualGasUsed, receiptA.ActualGasCost, receiptA.TxHash)
	if !receiptA.Success {
		return fmt.Errorf("op A mined but reverted")
	}

	// ── Persistence: a plain controller-signed op, no deferred action ─────
	return proveInstallPersists(ctx, chain, chainRPC, bundler, entryPoint, chainID,
		account, ownerAddr, controllerKey)
}

// proveInstallPersists sends an ordinary controller-signed operation with no
// deferred action attached. It can only succeed if the entity-1 validation
// the deferred action installed is durable account state.
func proveInstallPersists(ctx context.Context, chain *ethclient.Client, chainRPC *rpc.Client,
	bundler *rpc.Client, entryPoint common.Address, chainID *big.Int,
	account, ownerAddr common.Address, controllerKey *ecdsa.PrivateKey) error {

	callData, err := aa.PackExecute(ownerAddr, big.NewInt(0), nil)
	if err != nil {
		return err
	}
	nonceB, err := preset.NextNonceV07(ctx, chainRPC, entryPoint, account, spikeEntityID,
		userop.ValidationOptionGlobal)
	if err != nil {
		return err
	}
	opB := &userop.UserOperationV07{
		Sender:   account,
		Nonce:    nonceB,
		CallData: callData,
		// The deployed-account default seed (60k) is too tight here: module
		// validation at entity 1 plus the first write to a fresh nonce-key
		// slot (~22k cold SSTORE) lands actual verification just above it,
		// which AA26s at send. Measured on this spike; see the results doc.
		VerificationGasLimit: big.NewInt(100_000),
	}
	if err := priceOp(ctx, chain, bundler, opB, entryPoint, nil, nil); err != nil {
		return err
	}
	if err := preset.SignUserOpV07(opB, entryPoint, chainID, controllerKey); err != nil {
		return err
	}
	hashB, err := preset.SendUserOpV07(ctx, bundler, opB, entryPoint)
	if err != nil {
		return fmt.Errorf("sending op B (plain controller op): %w", err)
	}
	fmt.Printf("op B sent: ordinary op, controller-signed, no deferred action\n  userOpHash %s\n", hashB)
	receiptB, err := waitReceipt(ctx, bundler, hashB)
	if err != nil {
		return err
	}
	fmt.Printf("  mined: success=%v gasUsed=%s costWei=%s tx=%s\n\n",
		receiptB.Success, receiptB.ActualGasUsed, receiptB.ActualGasCost, receiptB.TxHash)
	if !receiptB.Success {
		return fmt.Errorf("op B mined but reverted")
	}

	fmt.Println("PROVEN: owner signed once, off-chain, before gas existed; the controller")
	fmt.Println("deployed, installed itself, and executed in one operation, and kept")
	fmt.Println("authority afterwards. The owner never signed a UserOperation.")
	return nil
}

// priceOp fills fee fields from chain + bundler and sizes gas via estimation.
// For the deferred op, estimation must carry the REAL owner grant — a fake
// deferred signature reverts instead of failing gracefully.
func priceOp(ctx context.Context, chain *ethclient.Client, bundler *rpc.Client,
	op *userop.UserOperationV07, entryPoint common.Address, encodedData, ownerSig []byte) error {

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

	if encodedData != nil {
		sig, err := preset.DeferredEstimationSignature(encodedData, ownerSig)
		if err != nil {
			return err
		}
		op.Signature = sig
		defer func() { op.Signature = nil }()
	}
	if _, err := preset.EstimateUserOpGasV07(ctx, bundler, op, entryPoint); err != nil {
		return fmt.Errorf("estimating gas: %w", err)
	}
	return nil
}

func ensurePrefund(ctx context.Context, chain *ethclient.Client, chainID *big.Int,
	ownerKey *ecdsa.PrivateKey, ownerAddr, account common.Address) error {

	balance, err := chain.BalanceAt(ctx, account, nil)
	if err != nil {
		return err
	}
	if balance.Cmp(prefundWei) >= 0 {
		fmt.Printf("account already holds %s wei, skipping prefund\n\n", balance)
		return nil
	}
	nonce, err := chain.PendingNonceAt(ctx, ownerAddr)
	if err != nil {
		return err
	}
	gasPrice, err := chain.SuggestGasPrice(ctx)
	if err != nil {
		return err
	}
	// A deployed MA v2 account's receive() costs more than a bare transfer's
	// 21k; a hardcoded 21k limit makes the top-up revert out-of-gas. Ask.
	gasLimit, err := chain.EstimateGas(ctx, ethereum.CallMsg{
		From: ownerAddr, To: &account, Value: prefundWei,
	})
	if err != nil {
		return fmt.Errorf("estimating prefund transfer gas: %w", err)
	}
	tx := types.NewTransaction(nonce, account, prefundWei, gasLimit+10_000, gasPrice, nil)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), ownerKey)
	if err != nil {
		return err
	}
	if err := chain.SendTransaction(ctx, signed); err != nil {
		return fmt.Errorf("prefunding %s: %w", account, err)
	}
	fmt.Printf("prefunding %s wei from owner, tx %s ", prefundWei, signed.Hash())
	for range 60 {
		receipt, err := chain.TransactionReceipt(ctx, signed.Hash())
		if err == nil && receipt.Status == types.ReceiptStatusSuccessful {
			fmt.Printf("— mined\n\n")
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("prefund tx %s not mined after 3 minutes", signed.Hash())
}

type userOpReceipt struct {
	Success       bool
	ActualGasUsed *big.Int
	ActualGasCost *big.Int
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
				ActualGasCost string `json:"actualGasCost"`
				Receipt       struct {
					TransactionHash string `json:"transactionHash"`
				} `json:"receipt"`
			}
			if err := json.Unmarshal(raw, &parsed); err != nil {
				return nil, fmt.Errorf("decoding userop receipt: %w", err)
			}
			gasUsed, _ := new(big.Int).SetString(trim0x(parsed.ActualGasUsed), 16)
			gasCost, _ := new(big.Int).SetString(trim0x(parsed.ActualGasCost), 16)
			return &userOpReceipt{
				Success:       parsed.Success,
				ActualGasUsed: gasUsed,
				ActualGasCost: gasCost,
				TxHash:        parsed.Receipt.TransactionHash,
			}, nil
		}
		time.Sleep(4 * time.Second)
	}
	return nil, fmt.Errorf("no receipt for %s after 3 minutes", opHash)
}
