// Permission-hooks spike — master doc §5 step 2, the last unproven claim:
// that the policy hooks a SessionGrant carries actually ENFORCE, not merely
// install. One owner signature grants a session signer bounded by all three
// §6.3 dimensions, then four operations probe the bounds:
//
//	C  in-policy ERC-20 transfer            → mines, cap 100 → 60
//	D  call to a non-allowlisted target     → refused at validation
//	E  transfer over the remaining cap      → mines success=false, cap rolls back
//	F  in-policy again, after expiry        → refused as expired
//
// Where each bound enforces is part of the finding: the allowlist enforces at
// VALIDATION (nothing mines), the ERC-20 cap at EXECUTION (the op mines and
// reverts, gas is spent), the expiry at the ENTRYPOINT via validation data.
//
// The grant carries an execution hook (the cap), which obligates EVERY
// operation under it to be executeUserOp-wrapped — including the one carrying
// the deferred install. That wrapping is exercised here for the first time.
//
// Environment: as scripts/spike/deferred_action, plus
//
//	SPIKE_SALT           default 3
//	SPIKE_TOKEN          existing SpikeToken address; deployed fresh if unset
//	SPIKE_VALID_MINUTES  time-range window (default 8) — the harness waits it
//	                     out to prove expiry, so total runtime exceeds it
//
// Run: go run ./scripts/spike/permission_hooks
package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strconv"
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
	oneToken   = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

	capTokens      = tokens(100) // the grant's ERC-20 spend limit
	inPolicyAmount = tokens(40)  // op C — leaves 60 under the cap
	overCapAmount  = tokens(100) // op E — exceeds the remaining 60
	expiryAmount   = tokens(10)  // op F — in policy except for time
)

func tokens(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), oneToken) }

// spikeTokenCreationHex is the compiled SpikeToken (solc 0.8.26 via forge), a
// minimal ERC-20 that mints 1,000,000e18 to its deployer and exposes exactly
// transfer(address,uint256) — the selector the allowlist scopes to. Source in
// the doc; re-compile with `forge build` on the snippet there to reproduce.
const spikeTokenCreationHex = "0x60a060405234801561000f575f80fd5b5069d3c21bcecceda10000006080818152505069d3c21bcecceda10000005f803373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f20819055503373ffffffffffffffffffffffffffffffffffffffff165f73ffffffffffffffffffffffffffffffffffffffff167fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef69d3c21bcecceda10000006040516100d4919061012c565b60405180910390a3610145565b5f819050919050565b5f819050919050565b5f819050919050565b5f61011661011161010c846100e1565b6100f3565b6100ea565b9050919050565b610126816100fc565b82525050565b5f60208201905061013f5f83018461011d565b92915050565b60805161062261015d5f395f61017701526106225ff3fe608060405234801561000f575f80fd5b5060043610610060575f3560e01c806306fdde031461006457806318160ddd14610082578063313ce567146100a057806370a08231146100be57806395d89b41146100ee578063a9059cbb1461010c575b5f80fd5b61006c61013c565b60405161007991906103db565b60405180910390f35b61008a610175565b6040516100979190610413565b60405180910390f35b6100a8610199565b6040516100b59190610447565b60405180910390f35b6100d860048036038101906100d391906104be565b61019e565b6040516100e59190610413565b60405180910390f35b6100f66101b2565b60405161010391906103db565b60405180910390f35b61012660048036038101906101219190610513565b6101eb565b604051610133919061056b565b60405180910390f35b6040518060400160405280601081526020017f4d417632205370696b6520546f6b656e0000000000000000000000000000000081525081565b7f000000000000000000000000000000000000000000000000000000000000000081565b601281565b5f602052805f5260405f205f915090505481565b6040518060400160405280600581526020017f5350494b4500000000000000000000000000000000000000000000000000000081525081565b5f805f803373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205490508281101561026f576040517f08c379a0000000000000000000000000000000000000000000000000000000008152600401610266906105ce565b60405180910390fd5b8281035f803373ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f2081905550825f808673ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020015f205f82825401925050819055508373ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff167fddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef856040516103589190610413565b60405180910390a3600191505092915050565b5f81519050919050565b5f82825260208201905092915050565b8281835e5f83830152505050565b5f601f19601f8301169050919050565b5f6103ad8261036b565b6103b78185610375565b93506103c7818560208601610385565b6103d081610393565b840191505092915050565b5f6020820190508181035f8301526103f381846103a3565b905092915050565b5f819050919050565b61040d816103fb565b82525050565b5f6020820190506104265f830184610404565b92915050565b5f60ff82169050919050565b6104418161042c565b82525050565b5f60208201905061045a5f830184610438565b92915050565b5f80fd5b5f73ffffffffffffffffffffffffffffffffffffffff82169050919050565b5f61048d82610464565b9050919050565b61049d81610483565b81146104a7575f80fd5b50565b5f813590506104b881610494565b92915050565b5f602082840312156104d3576104d2610460565b5b5f6104e0848285016104aa565b91505092915050565b6104f2816103fb565b81146104fc575f80fd5b50565b5f8135905061050d816104e9565b92915050565b5f806040838503121561052957610528610460565b5b5f610536858286016104aa565b9250506020610547858286016104ff565b9150509250929050565b5f8115159050919050565b61056581610551565b82525050565b5f60208201905061057e5f83018461055c565b92915050565b7f5370696b65546f6b656e3a2062616c616e6365000000000000000000000000005f82015250565b5f6105b8601383610375565b91506105c382610584565b602082019050919050565b5f6020820190508181035f8301526105e5816105ac565b905091905056fea2646970667358221220def2f328ce26f0b8b67e18e56a58766dc05a67ea41c0396f3686c267aace530264736f6c634300081a0033"

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

func packTransfer(to common.Address, amount *big.Int) []byte {
	out := make([]byte, 0, 4+64)
	out = append(out, 0xa9, 0x05, 0x9c, 0xbb)
	out = append(out, common.LeftPadBytes(to.Bytes(), 32)...)
	return append(out, common.LeftPadBytes(amount.Bytes(), 32)...)
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
	salt := big.NewInt(3)
	if s := os.Getenv("SPIKE_SALT"); s != "" {
		v, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return fmt.Errorf("SPIKE_SALT %q is not a decimal integer", s)
		}
		salt = v
	}
	validMinutes, err := strconv.Atoi(env("SPIKE_VALID_MINUTES", "8"))
	if err != nil {
		return fmt.Errorf("SPIKE_VALID_MINUTES: %w", err)
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

	fmt.Printf("chain %s, owner %s, controller %s, salt %s\n", chainID, ownerAddr, controllerAddr, salt)

	// ── Test token ─────────────────────────────────────────────────────────
	var token common.Address
	if t := os.Getenv("SPIKE_TOKEN"); t != "" {
		token = common.HexToAddress(t)
	} else {
		token, err = deployToken(ctx, chain, chainID, ownerKey, ownerAddr)
		if err != nil {
			return err
		}
	}
	fmt.Printf("token      %s\n", token)

	// ── The account ────────────────────────────────────────────────────────
	accountPtr, err := aa.GetSenderAddressMAv2(chain, ownerAddr, salt)
	if err != nil {
		return err
	}
	account := *accountPtr
	if code, cErr := chain.CodeAt(ctx, account, nil); cErr != nil {
		return cErr
	} else if len(code) > 0 {
		return fmt.Errorf("account %s already has code — bump SPIKE_SALT for a fresh grant", account)
	}
	fmt.Printf("account    %s (counterfactual)\n\n", account)

	// Seed it: SPIKE for the transfers, ETH for gas. Both idempotent so a
	// failed run can be retried without doubling the account's stake.
	if held, hErr := readTokenBalance(ctx, chain, token, account); hErr != nil {
		return hErr
	} else if held.Cmp(tokens(1000)) < 0 {
		if err := transferToken(ctx, chain, chainID, ownerKey, ownerAddr, token, account, tokens(1000)); err != nil {
			return err
		}
	}
	if bal, bErr := chain.BalanceAt(ctx, account, nil); bErr != nil {
		return bErr
	} else if bal.Cmp(prefundWei) < 0 {
		if err := sendETH(ctx, chain, chainID, ownerKey, ownerAddr, account, prefundWei); err != nil {
			return err
		}
	}

	// ── The grant: signer + all three §6.3 bounds, one owner signature ─────
	entity := aa.MinSessionEntityID
	validUntil := uint64(time.Now().Add(time.Duration(validMinutes) * time.Minute).Unix())
	allowHook, err := aa.AllowlistValidationHook(entity, []aa.AllowlistInput{{
		Target:               token,
		HasSelectorAllowlist: true,
		HasERC20SpendLimit:   true,
		ERC20SpendLimit:      capTokens,
		Selectors:            [][4]byte{{0xa9, 0x05, 0x9c, 0xbb}}, // transfer
	}})
	if err != nil {
		return err
	}
	timeHook, err := aa.TimeRangeValidationHook(entity, validUntil, 0)
	if err != nil {
		return err
	}
	installCall, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: entity,
		Signer:   controllerAddr,
		Hooks:    [][]byte{allowHook, aa.AllowlistExecHook(entity), timeHook},
	})
	if err != nil {
		return err
	}

	opts := uint8(userop.ValidationOptionGlobal | userop.ValidationOptionDeferredAction)
	carrierNonce, err := userop.EncodeNonceMAv2(entity, opts, 0)
	if err != nil {
		return err
	}
	deadline := uint64(time.Now().Add(time.Hour).Unix())
	digest, err := userop.DeferredActionDigest(chainID, account, carrierNonce, deadline, installCall)
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
	fmt.Printf("grant signed: allowlist(token, transfer) + cap 100 SPIKE + expires %s\n  712 digest %s\n\n",
		time.Unix(int64(validUntil), 0).UTC().Format(time.RFC3339), digest)

	// ── Op C: in-policy transfer, carries the deferred install ────────────
	execC, err := aa.PackExecute(token, big.NewInt(0), packTransfer(controllerAddr, inPolicyAmount))
	if err != nil {
		return err
	}
	callC, err := aa.WrapExecuteUserOp(execC)
	if err != nil {
		return err
	}
	factory, factoryData, err := aa.GetInitCodeMAv2(ownerAddr, salt)
	if err != nil {
		return err
	}
	opC := &userop.UserOperationV07{
		Sender:      account,
		Nonce:       carrierNonce,
		Factory:     &factory,
		FactoryData: factoryData,
		CallData:    callC,
		// Deploy + install (validation + 3 hooks + allowlist state) + hook
		// validation runtime. Rundler echoes seeds; the floor is 0.4. The
		// hook-carrying install is far heavier than the bare one (§3.2's
		// 300k seed AA26'd here): every allowlist entry is cold SSTOREs.
		VerificationGasLimit: big.NewInt(700_000),
	}
	if err := priceOp(ctx, chain, bundler, opC, entryPoint, encodedData, ownerSig); err != nil {
		return fmt.Errorf("pricing op C: %w", err)
	}
	if err := preset.SignUserOpV07Deferred(opC, entryPoint, chainID, controllerKey, encodedData, ownerSig); err != nil {
		return err
	}
	hashC, err := preset.SendUserOpV07(ctx, bundler, opC, entryPoint)
	if err != nil {
		return fmt.Errorf("sending op C: %w", err)
	}
	fmt.Printf("op C sent: deploy + deferred install(signer+3 hooks) + transfer 40 SPIKE\n  userOpHash %s\n", hashC)
	rC, err := waitReceipt(ctx, bundler, hashC)
	if err != nil {
		return err
	}
	fmt.Printf("  mined: success=%v gasUsed=%s tx=%s\n", rC.Success, rC.ActualGasUsed, rC.TxHash)
	if !rC.Success {
		return fmt.Errorf("op C reverted — the in-policy operation must succeed")
	}
	limitAfterC, err := readSpendLimit(ctx, chain, entity, token, account)
	if err != nil {
		return err
	}
	fmt.Printf("  on-chain cap after C: %s SPIKE (want 60)\n\n", inTokens(limitAfterC))
	if limitAfterC.Cmp(tokens(60)) != 0 {
		return fmt.Errorf("cap after C is %s, want 60e18", limitAfterC)
	}

	// ── Op D: off-allowlist target — must die at validation ───────────────
	execD, err := aa.PackExecute(ownerAddr, big.NewInt(0), nil)
	if err != nil {
		return err
	}
	callD, err := aa.WrapExecuteUserOp(execD)
	if err != nil {
		return err
	}
	nonceD, err := preset.NextNonceV07(ctx, chainRPC, entryPoint, account, entity, userop.ValidationOptionGlobal)
	if err != nil {
		return err
	}
	opD := &userop.UserOperationV07{
		Sender: account, Nonce: nonceD, CallData: callD,
		VerificationGasLimit: big.NewInt(150_000),
	}
	errD := priceOp(ctx, chain, bundler, opD, entryPoint, nil, nil)
	if errD == nil {
		return fmt.Errorf("op D (non-allowlisted target) was NOT refused — the allowlist does not enforce")
	}
	fmt.Printf("op D refused at validation, as required (target not on allowlist)\n  %s\n\n", firstLine(errD.Error()))

	// ── Op E: over the remaining cap — mines, reverts at execution ────────
	execE, err := aa.PackExecute(token, big.NewInt(0), packTransfer(controllerAddr, overCapAmount))
	if err != nil {
		return err
	}
	callE, err := aa.WrapExecuteUserOp(execE)
	if err != nil {
		return err
	}
	opE := &userop.UserOperationV07{
		Sender: account, Nonce: nonceD, CallData: callE,
		VerificationGasLimit: big.NewInt(150_000),
	}
	if err := priceOp(ctx, chain, bundler, opE, entryPoint, nil, nil); err != nil {
		// The cap enforces at execution: some estimators refuse the reverting
		// call outright. Fall back to op C's numbers — validation is cheaper
		// here than there, so they are safe over-estimates for sending.
		fmt.Printf("op E estimation refused (%s) — sending with op C's gas numbers\n", firstLine(err.Error()))
		opE.CallGasLimit = opC.CallGasLimit
		opE.PreVerificationGas = opC.PreVerificationGas
		opE.MaxFeePerGas = opC.MaxFeePerGas
		opE.MaxPriorityFeePerGas = opC.MaxPriorityFeePerGas
	}
	if err := preset.SignUserOpV07(opE, entryPoint, chainID, controllerKey); err != nil {
		return err
	}
	hashE, err := preset.SendUserOpV07(ctx, bundler, opE, entryPoint)
	if err != nil {
		return fmt.Errorf("sending op E: %w", err)
	}
	fmt.Printf("op E sent: transfer 100 SPIKE against a remaining cap of 60\n  userOpHash %s\n", hashE)
	rE, err := waitReceipt(ctx, bundler, hashE)
	if err != nil {
		return err
	}
	fmt.Printf("  mined: success=%v gasUsed=%s tx=%s\n", rE.Success, rE.ActualGasUsed, rE.TxHash)
	if rE.Success {
		return fmt.Errorf("op E SUCCEEDED over the cap — the spend limit does not enforce")
	}
	limitAfterE, err := readSpendLimit(ctx, chain, entity, token, account)
	if err != nil {
		return err
	}
	fmt.Printf("  on-chain cap after E: %s SPIKE (rollback intact, want 60)\n\n", inTokens(limitAfterE))
	if limitAfterE.Cmp(tokens(60)) != 0 {
		return fmt.Errorf("cap after E is %s, want 60e18 — the failed execution leaked accounting", limitAfterE)
	}

	// ── Op F: in policy except for time ───────────────────────────────────
	wait := time.Until(time.Unix(int64(validUntil), 0).Add(30 * time.Second))
	if wait > 0 {
		fmt.Printf("waiting %s for the grant to expire…\n", wait.Round(time.Second))
		time.Sleep(wait)
	}
	execF, err := aa.PackExecute(token, big.NewInt(0), packTransfer(controllerAddr, expiryAmount))
	if err != nil {
		return err
	}
	callF, err := aa.WrapExecuteUserOp(execF)
	if err != nil {
		return err
	}
	nonceF, err := preset.NextNonceV07(ctx, chainRPC, entryPoint, account, entity, userop.ValidationOptionGlobal)
	if err != nil {
		return err
	}
	opF := &userop.UserOperationV07{
		Sender: account, Nonce: nonceF, CallData: callF,
		VerificationGasLimit: big.NewInt(150_000),
	}
	errF := priceOp(ctx, chain, bundler, opF, entryPoint, nil, nil)
	if errF == nil {
		if err := preset.SignUserOpV07(opF, entryPoint, chainID, controllerKey); err != nil {
			return err
		}
		_, errF = preset.SendUserOpV07(ctx, bundler, opF, entryPoint)
		if errF == nil {
			return fmt.Errorf("op F was accepted AFTER expiry — the time range does not enforce")
		}
	}
	fmt.Printf("op F refused after expiry, as required\n  %s\n\n", firstLine(errF.Error()))

	fmt.Println("PROVEN: one owner signature installed signer + allowlist + cap + expiry;")
	fmt.Println("in-policy ran, off-list died at validation, over-cap reverted at execution")
	fmt.Println("with clean rollback, and the whole grant went dark on schedule.")
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────

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

func deployToken(ctx context.Context, chain *ethclient.Client, chainID *big.Int,
	ownerKey *ecdsa.PrivateKey, ownerAddr common.Address) (common.Address, error) {

	nonce, err := chain.PendingNonceAt(ctx, ownerAddr)
	if err != nil {
		return common.Address{}, err
	}
	gasPrice, err := chain.SuggestGasPrice(ctx)
	if err != nil {
		return common.Address{}, err
	}
	data := common.FromHex(spikeTokenCreationHex)
	tx := types.NewContractCreation(nonce, big.NewInt(0), 600_000, gasPrice, data)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), ownerKey)
	if err != nil {
		return common.Address{}, err
	}
	if err := chain.SendTransaction(ctx, signed); err != nil {
		return common.Address{}, fmt.Errorf("deploying SpikeToken: %w", err)
	}
	receipt, err := waitMined(ctx, chain, signed.Hash())
	if err != nil {
		return common.Address{}, err
	}
	fmt.Printf("SpikeToken deployed at %s (tx %s)\n", receipt.ContractAddress, signed.Hash())
	return receipt.ContractAddress, nil
}

func transferToken(ctx context.Context, chain *ethclient.Client, chainID *big.Int,
	ownerKey *ecdsa.PrivateKey, ownerAddr, token, to common.Address, amount *big.Int) error {

	nonce, err := chain.PendingNonceAt(ctx, ownerAddr)
	if err != nil {
		return err
	}
	gasPrice, err := chain.SuggestGasPrice(ctx)
	if err != nil {
		return err
	}
	tx := types.NewTransaction(nonce, token, big.NewInt(0), 80_000, gasPrice, packTransfer(to, amount))
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), ownerKey)
	if err != nil {
		return err
	}
	if err := chain.SendTransaction(ctx, signed); err != nil {
		return fmt.Errorf("seeding SPIKE: %w", err)
	}
	if _, err := waitMined(ctx, chain, signed.Hash()); err != nil {
		return err
	}
	fmt.Printf("seeded %s SPIKE to the account\n", inTokens(amount))
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
	fmt.Printf("prefunded %s wei\n\n", amount)
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

// readTokenBalance reads SpikeToken.balanceOf(holder).
func readTokenBalance(ctx context.Context, chain *ethclient.Client, token, holder common.Address) (*big.Int, error) {
	selector := crypto.Keccak256([]byte("balanceOf(address)"))[:4]
	data := append(selector, common.LeftPadBytes(holder.Bytes(), 32)...)
	out, err := chain.CallContract(ctx, ethereum.CallMsg{To: &token, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("reading balanceOf: %w", err)
	}
	return new(big.Int).SetBytes(out), nil
}

// readSpendLimit reads AllowlistModule.erc20SpendLimits(entity, token, account)
// — the live on-chain cap accounting.
func readSpendLimit(ctx context.Context, chain *ethclient.Client, entity uint32, token, account common.Address) (*big.Int, error) {
	selector := crypto.Keccak256([]byte("erc20SpendLimits(uint32,address,address)"))[:4]
	data := make([]byte, 0, 4+96)
	data = append(data, selector...)
	data = append(data, common.LeftPadBytes(big.NewInt(int64(entity)).Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(token.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(account.Bytes(), 32)...)
	module := aa.AllowlistModuleAddress()
	out, err := chain.CallContract(ctx, ethereum.CallMsg{To: &module, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("reading erc20SpendLimits: %w", err)
	}
	return new(big.Int).SetBytes(out), nil
}

func inTokens(wei *big.Int) string {
	return new(big.Int).Div(wei, oneToken).String()
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
