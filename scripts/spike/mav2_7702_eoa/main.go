// Track B PR B0 — spike MA v2 7702 scoped session grant.
//
// Does NOT enable production execute and does NOT patch SendUserOpMAv2.
// UserOps are sent with sender=EOA via a spike-local path (no factory
// derivation check, no initCode).
//
// Proofs (Sepolia and Base):
//
//	B1  type-4 delegate to SemiModularAccount7702; K13 (ef0100||impl + hash),
//	    never tx status
//	B2  install session grant (Track A vocabulary); isSignatureValidation=false
//	B3  on-chain getValidationData: isSignatureValidation bit is 0 on THIS
//	    entity; located isValidSignature reverts SignatureValidationInvalid
//	    naming the session ModuleEntity (not a bare 65-byte locator miss)
//	B4  scoped native UserOp succeeds
//	B5  unlisted recipient / over-cap fail
//	B6  owner uninstall; subsequent session UserOp fails
//	B7  CREATE2(owner=eoa, salt=0) != EOA and has no 7702 designation
//	    (address inequality, not a live derived-path send)
//
// Self-funded. No Gas Manager.
//
//	SPIKE_7702_KEY                         throwaway EOA that will be delegated
//	                                       (blast radius: everything at this
//	                                       address). Do not point this at the
//	                                       Track A fixture owner. If unset, a
//	                                       gitignored .eoa.key is created and
//	                                       prefunded from TEST_PRIVATE_KEY.
//	SPIKE_7702_USE_TEST_KEY=1              opt-in to 7702-delegate TEST_PRIVATE_KEY
//	SPIKE_CONTROLLER_KEY / CONTROLLER_PRIVATE_KEY
//	                                       session signer; must differ from the
//	                                       EOA (SMA-7702 1271 admits EOA ECDSA).
//	                                       Falls back to an ephemeral key.
//	SPIKE_OWNER_KEY / TEST_PRIVATE_KEY     prefund sponsor only (not delegated)
//	SPIKE_RPC_URL / SEPOLIA_RPC_URL / BASE_RPC_URL / ALCHEMY_API_KEY
//	SPIKE_BUNDLER_URL / SEPOLIA_BUNDLER_URL / BASE_BUNDLER_URL
//	SPIKE_CHAIN=sepolia|base               default sepolia
//	SPIKE_ENTITY_BASE                      optional; otherwise random if already delegated
//
//	CGO_ENABLED=0 go run ./scripts/spike/mav2_7702_eoa
//	SPIKE_CHAIN=base CGO_ENABLED=0 go run ./scripts/spike/mav2_7702_eoa
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/holiman/uint256"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/eip1559"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

const (
	// Spec K8 / deployments: alchemy.sma-7702.1.0.0 (not v1.1.0).
	sma7702Hex          = "0x69007702764179f14F51cdce752f4f775d74E139"
	nativeTokenLimitHex = "0x00000000000001e541f0D090868FBe24b59Fbe06"
)

var (
	nativeCap  = big.NewInt(10_000_000_000_000_000) // 0.01 ETH
	oneWei     = big.NewInt(1)
	minBalance = big.NewInt(25_000_000_000_000_000) // 0.025 ETH
	prefundWei = big.NewInt(30_000_000_000_000_000) // 0.03 ETH
	alice      = common.HexToAddress("0x000000000000000000000000000000000000a11c")
	bob        = common.HexToAddress("0x000000000000000000000000000000000000b0b0")
)

func main() {
	loadDotEnv()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "spike failed: %s\n", redactSecrets(err.Error()))
		os.Exit(1)
	}
}

func run() error {
	chainName := strings.ToLower(strings.TrimSpace(env("SPIKE_CHAIN", "sepolia")))
	var chainID int64
	var rpcURL, bundlerURL string
	switch chainName {
	case "sepolia":
		chainID = 11155111
		rpcURL = firstNonEmpty("SPIKE_RPC_URL", "SEPOLIA_RPC_URL")
	case "base":
		chainID = 8453
		rpcURL = firstNonEmpty("SPIKE_RPC_URL", "BASE_RPC_URL")
	default:
		return fmt.Errorf("SPIKE_CHAIN must be sepolia or base, got %q", chainName)
	}
	alchemy := ""
	if k := firstNonEmpty("ALCHEMY_API_KEY"); k != "" {
		alchemy = alchemyURL(chainID, k)
	}
	if rpcURL == "" {
		rpcURL = alchemy
	}
	if rpcURL == "" {
		return fmt.Errorf("set SPIKE_RPC_URL or the chain RPC env (paid endpoint)")
	}
	if containsAny(rpcURL, "publicnode", "public-rpc", "llamarpc") {
		return fmt.Errorf("set SPIKE_RPC_URL to a paid endpoint (public RPCs are refused)")
	}
	// Retired avaprotocol.org bundlers (#725) still sit in some local .env
	// files as SEPOLIA_BUNDLER_URL. Prefer Alchemy unless SPIKE_BUNDLER_URL
	// is an explicit, live override.
	bundlerURL = firstNonEmpty("SPIKE_BUNDLER_URL")
	if bundlerURL == "" || isRetiredBundler(bundlerURL) {
		if alchemy != "" {
			bundlerURL = alchemy
		} else {
			switch chainName {
			case "sepolia":
				bundlerURL = firstNonEmpty("SEPOLIA_BUNDLER_URL")
			case "base":
				bundlerURL = firstNonEmpty("BASE_BUNDLER_URL")
			}
		}
	}
	if bundlerURL == "" || isRetiredBundler(bundlerURL) {
		return fmt.Errorf("set SPIKE_BUNDLER_URL or ALCHEMY_API_KEY (Alchemy bundler)")
	}

	ownerKey, eoa, err := resolveDelegatedEOA()
	if err != nil {
		return err
	}
	ctrlKey, ctrl, err := resolveController(eoa)
	if err != nil {
		return err
	}
	fmt.Printf("B0 spike chain=%s id=%d eoa=%s controller=%s\n", chainName, chainID, eoa, ctrl)
	fmt.Println("WARNING: this delegates the EOA itself (blast radius = assets at this address).")
	fmt.Println("SendUserOpMAv2 is not used; production execute stays on the derived-SW path.")

	ctx := context.Background()
	chain, err := ethclient.Dial(rpcURL)
	if err != nil {
		return err
	}
	defer chain.Close()
	gotID, err := chain.ChainID(ctx)
	if err != nil {
		return err
	}
	if gotID.Int64() != chainID {
		return fmt.Errorf("RPC chain id %s != %d", gotID, chainID)
	}

	sma := common.HexToAddress(sma7702Hex)
	implCode, err := chain.CodeAt(ctx, sma, nil)
	if err != nil {
		return err
	}
	if len(implCode) == 0 {
		return fmt.Errorf("SMA-7702 %s has no code on %s", sma, chainName)
	}
	implHash := crypto.Keccak256Hash(implCode)
	fmt.Printf("SMA-7702 impl code=%d bytes keccak256=%s\n", len(implCode), implHash)

	if err := ensurePrefunded(ctx, chain, gotID, eoa); err != nil {
		return err
	}

	if err := ensureDelegated(ctx, chain, gotID, ownerKey, eoa, sma, implCode); err != nil {
		return err
	}
	// ensureDelegated only returns after K13, so the EOA is always delegated.
	// Randomize the entity unless SPIKE_ENTITY_BASE is set — leftover grants
	// from prior runs occupy previous ids.
	entity := pickEntityBase()
	fmt.Printf("session entity=%d\n", entity)

	h := &harness{
		ctx: ctx, chain: chain, chainID: gotID, eoa: eoa,
		ownerKey: ownerKey, ctrlKey: ctrlKey, ctrl: ctrl,
		entryPoint: preset.EntryPointV07(),
		rpcURL:     rpcURL, bundlerURL: bundlerURL,
		entity: entity,
	}
	h.bundler, err = rpc.DialContext(ctx, bundlerURL)
	if err != nil {
		return err
	}
	defer h.bundler.Close()
	h.chainRPC = chain.Client()

	codeAt := func(addr common.Address) ([]byte, error) {
		return chain.CodeAt(ctx, addr, nil)
	}
	hooks, err := nativeSendHooks(entity, alice, codeAt)
	if err != nil {
		return err
	}
	fmt.Println("B2 packing: Track A SessionPermissions.HooksFor; AllowSignatureValidation=false")

	exec, err := aa.PackExecute(alice, oneWei, nil)
	if err != nil {
		return err
	}
	r, _, err := h.deferredInstall(hooks, exec, 900_000)
	if err != nil {
		return fmt.Errorf("B2/B4 install+send: %w", err)
	}
	if r == nil || !r.Success {
		return fmt.Errorf("B4 FAIL: scoped native send did not mine success tx=%v reason=%s", r, receiptReason(r))
	}
	fmt.Printf("B2+B4 PASS: grant installed; scoped send to listed EOA mined tx=%s gasUsed=%s\n", r.TxHash, r.ActualGasUsed)

	afterInstall, err := readNativeLimit(ctx, chain, entity, eoa)
	if err != nil {
		return err
	}
	if afterInstall.Sign() == 0 {
		return fmt.Errorf("B2 FAIL: NativeTokenLimitModule.limits still 0 after successful native send")
	}
	fmt.Printf("B2 NT limits(entity,eoa)=%s wei\n", afterInstall)

	if err := h.prove1271Deny(); err != nil {
		return err
	}

	if err := requireRevert(h.estimateNative(bob, oneWei), "B5 unlisted",
		"AddressNotAllowed", "AA23"); err != nil {
		return err
	}
	over := new(big.Int).Add(afterInstall, oneWei)
	ntNeedle := exceededNTNeedle()
	if err := requireRevert(h.estimateNative(alice, over), "B5 over-cap remaining+1",
		"ExceededNativeTokenLimit", ntNeedle, "AA23", "execution reverted"); err != nil {
		return err
	}

	uninst, err := aa.SessionSignerUninstallFromInstall(entity, mustInstallCall(entity, hooks, ctrl))
	if err != nil {
		return err
	}
	if err := h.ownerCall(uninst); err != nil {
		return fmt.Errorf("B6 uninstall: %w", err)
	}
	left, err := readNativeLimit(ctx, chain, entity, eoa)
	if err != nil {
		return err
	}
	if left.Sign() != 0 {
		return fmt.Errorf("B6 FAIL: NT limits after uninstall = %s (want 0; receipt is not evidence)", left)
	}
	if err := requireRevert(h.estimateNative(alice, oneWei), "B6 after uninstall",
		"ValidationFunctionMissing", "AA23"); err != nil {
		return err
	}
	fmt.Println("B6 PASS: owner uninstall; limits=0; session UserOp fails")

	derived, err := aa.GetSenderAddressMAv2(chain, eoa, big.NewInt(0))
	if err != nil {
		return err
	}
	if *derived == eoa {
		return fmt.Errorf("B7 FAIL: derived runner equals EOA")
	}
	dcode, err := chain.CodeAt(ctx, *derived, nil)
	if err != nil {
		return err
	}
	if is7702Designation(dcode, sma) {
		return fmt.Errorf("B7 FAIL: derived runner has 7702 designation")
	}
	fmt.Printf("B7 PASS: CREATE2(owner=eoa, salt=0) %s != EOA %s and has no 7702 designation (address inequality, not a live derived-path send)\n", derived.Hex(), eoa.Hex())
	fmt.Println("\nB0 spike finished. Record K13 implHash and proof lines in the PR body.")
	fmt.Printf("B1 config pin: sma_7702_delegate=%s impl_hash=%s chain=%s impl_bytes=%d\n", sma.Hex(), implHash, chainName, len(implCode))
	return nil
}

func nativeSendHooks(entity uint32, recipient common.Address, codeAt func(common.Address) ([]byte, error)) ([][]byte, error) {
	r := recipient
	perms := taskengine.SessionPermissions{
		NativeRecipients: []*common.Address{&r},
		NativeSpendCap:   &model.NativeSpendCap{Amount: nativeCap.String()},
		ValidUntilMs:     time.Now().Add(24 * time.Hour).UnixMilli(),
		CodeAt:           codeAt,
	}
	if err := perms.Validate(); err != nil {
		return nil, fmt.Errorf("Track A Validate: %w", err)
	}
	return perms.HooksFor(entity)
}

func mustInstallCall(entity uint32, hooks [][]byte, signer common.Address) []byte {
	call, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: entity, Signer: signer, Global: true, Hooks: hooks,
	})
	if err != nil {
		panic(err)
	}
	return call
}

func ensureDelegated(ctx context.Context, chain *ethclient.Client, chainID *big.Int, key *ecdsa.PrivateKey, eoa, sma common.Address, implCode []byte) error {
	code, err := chain.CodeAt(ctx, eoa, nil)
	if err != nil {
		return err
	}
	if is7702Designation(code, sma) {
		if err := assertK13(code, sma, implCode, "already delegated"); err != nil {
			return err
		}
		return nil
	}
	if chainID.Sign() == 0 {
		return fmt.Errorf("refusing chain_id=0 7702 authorization")
	}
	nonce, err := chain.PendingNonceAt(ctx, eoa)
	if err != nil {
		return err
	}
	// Self-sponsored type-4: tx uses nonce N, authorization uses N+1.
	auth, err := types.SignSetCode(key, types.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(chainID),
		Address: sma,
		Nonce:   nonce + 1,
	})
	if err != nil {
		return fmt.Errorf("signing 7702 authorization: %w", err)
	}
	head, err := chain.HeaderByNumber(ctx, nil)
	if err != nil {
		return err
	}
	tip, err := chain.SuggestGasTipCap(ctx)
	if err != nil {
		return err
	}
	fee := eip1559.MaxFeeFromTipAndBase(tip, head.BaseFee)
	inner := &types.SetCodeTx{
		ChainID:   uint256.MustFromBig(chainID),
		Nonce:     nonce,
		GasTipCap: uint256.MustFromBig(tip),
		GasFeeCap: uint256.MustFromBig(fee),
		Gas:       150_000,
		To:        eoa,
		Value:     uint256.NewInt(0),
		AuthList:  []types.SetCodeAuthorization{auth},
	}
	signed, err := types.SignNewTx(key, types.LatestSignerForChainID(chainID), inner)
	if err != nil {
		return err
	}
	if err := chain.SendTransaction(ctx, signed); err != nil {
		return fmt.Errorf("broadcast type-4: %w", err)
	}
	if _, err := waitMined(ctx, chain, signed.Hash(), false); err != nil {
		return fmt.Errorf("type-4 wait: %w", err)
	}
	fmt.Printf("type-4 mined tx=%s (status ignored for K13)\n", signed.Hash())
	code, err = chain.CodeAt(ctx, eoa, nil)
	if err != nil {
		return err
	}
	return assertK13(code, sma, implCode, "after type-4")
}

func is7702Designation(code []byte, sma common.Address) bool {
	if len(code) < 23 {
		return false
	}
	return code[0] == 0xef && code[1] == 0x01 && code[2] == 0x00 &&
		common.BytesToAddress(code[3:23]) == sma
}

func assertK13(code []byte, sma common.Address, implCode []byte, when string) error {
	if len(code) < 23 {
		return fmt.Errorf("B1 FAIL %s: code len %d (not a 7702 designation)", when, len(code))
	}
	if code[0] != 0xef || code[1] != 0x01 || code[2] != 0x00 {
		return fmt.Errorf("B1 FAIL %s: prefix %x want ef0100", when, code[:min(3, len(code))])
	}
	got := common.BytesToAddress(code[3:23])
	if got != sma {
		return fmt.Errorf("B1 FAIL %s: impl %s want %s", when, got, sma)
	}
	h := crypto.Keccak256Hash(implCode)
	fmt.Printf("B1 PASS (%s): K13 ef0100||%s implHash=%s (not tx status)\n", when, got.Hex(), h.Hex())
	return nil
}

type harness struct {
	ctx                   context.Context
	chain                 *ethclient.Client
	chainRPC, bundler     *rpc.Client
	chainID               *big.Int
	eoa, ctrl, entryPoint common.Address
	ownerKey, ctrlKey     *ecdsa.PrivateKey
	rpcURL, bundlerURL    string
	entity                uint32
}

func (h *harness) deferredInstall(hooks [][]byte, exec []byte, vgl int64) (*userOpReceipt, *userop.UserOperationV07, error) {
	installCall, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: h.entity, Signer: h.ctrl, Global: true, Hooks: hooks,
	})
	if err != nil {
		return nil, nil, err
	}
	call, err := aa.WrapExecuteUserOp(exec)
	if err != nil {
		return nil, nil, err
	}
	opts := uint8(userop.ValidationOptionGlobal | userop.ValidationOptionDeferredAction)
	carrier, err := userop.EncodeNonceMAv2(h.entity, opts, 0)
	if err != nil {
		return nil, nil, err
	}
	deadline := uint64(time.Now().Add(time.Hour).Unix())
	digest, err := userop.DeferredActionDigest(h.chainID, h.eoa, carrier, deadline, installCall)
	if err != nil {
		return nil, nil, err
	}
	ownerSig, err := crypto.Sign(digest.Bytes(), h.ownerKey)
	if err != nil {
		return nil, nil, err
	}
	ownerSig[64] += 27
	encoded, err := userop.EncodeDeferredActionData(userop.FallbackSignerLocator(), deadline, installCall)
	if err != nil {
		return nil, nil, err
	}
	op := &userop.UserOperationV07{
		Sender: h.eoa, Nonce: carrier, CallData: call,
		VerificationGasLimit: big.NewInt(vgl),
		CallGasLimit:         big.NewInt(500_000),
		PreVerificationGas:   big.NewInt(100_000),
	}
	if err := h.priceOp(op, encoded, ownerSig); err != nil {
		return nil, nil, err
	}
	if err := preset.SignUserOpV07Deferred(op, h.entryPoint, h.chainID, h.ctrlKey, encoded, ownerSig); err != nil {
		return nil, nil, err
	}
	hash, err := preset.SendUserOpV07(h.ctx, h.bundler, op, h.entryPoint)
	if err != nil {
		return nil, op, err
	}
	r, err := waitReceipt(h.ctx, h.bundler, hash)
	return r, op, err
}

func (h *harness) estimateNative(to common.Address, value *big.Int) error {
	exec, err := aa.PackExecute(to, value, nil)
	if err != nil {
		return err
	}
	call, err := aa.WrapExecuteUserOp(exec)
	if err != nil {
		return err
	}
	nonce, err := preset.NextNonceV07(h.ctx, h.chainRPC, h.entryPoint, h.eoa, h.entity, userop.ValidationOptionGlobal)
	if err != nil {
		return err
	}
	maxFee, tip, err := eip1559.SuggestFee(h.chain)
	if err != nil {
		return err
	}
	op := &userop.UserOperationV07{
		Sender: h.eoa, Nonce: nonce, CallData: call,
		CallGasLimit: big.NewInt(200_000), VerificationGasLimit: big.NewInt(200_000),
		PreVerificationGas: big.NewInt(80_000), MaxFeePerGas: maxFee, MaxPriorityFeePerGas: tip,
	}
	if err := preset.SignUserOpV07(op, h.entryPoint, h.chainID, h.ctrlKey); err != nil {
		return err
	}
	_, err = preset.EstimateUserOpGasV07(h.ctx, h.bundler, op, h.entryPoint)
	return err
}

func (h *harness) prove1271Deny() error {
	if err := h.assertSessionValidationFlags(); err != nil {
		return err
	}
	return h.assertLocated1271Reverts()
}

// assertSessionValidationFlags is B3(a): read THIS entity's stored flags off
// the account. A bare 65-byte isValidSignature never reaches them.
func (h *harness) assertSessionValidationFlags() error {
	flags, valHooks, execHooks, err := h.readValidationData()
	if err != nil {
		return fmt.Errorf("B3 getValidationData: %w", err)
	}
	if flags&aa.ValidationFlagSignature != 0 {
		return fmt.Errorf("B3 FAIL: isSignatureValidation is set (flags=0x%02x); session entity must not speak as the user", flags)
	}
	wantFlags := aa.ValidationFlagUserOp | aa.ValidationFlagGlobal
	if flags != wantFlags {
		return fmt.Errorf("B3 FAIL: validation flags 0x%02x want 0x%02x (UserOp|Global, no Signature)", flags, wantFlags)
	}
	wantVal, wantExec := expectedNativeGrantHooks(h.entity)
	order := "install"
	if !hooksEqual(valHooks, wantVal) || !hooksEqual(execHooks, wantExec) {
		revVal, revExec := reverseHooks(wantVal), reverseHooks(wantExec)
		if hooksEqual(valHooks, revVal) && hooksEqual(execHooks, revExec) {
			order = "reverse-of-install"
			wantVal, wantExec = revVal, revExec
		} else {
			return fmt.Errorf("B3 FAIL: stored hooks val=%s exec=%s want val=%s exec=%s (or reverse)",
				formatHooks(valHooks), formatHooks(execHooks), formatHooks(wantVal), formatHooks(wantExec))
		}
	}
	fmt.Printf("B3 PASS: getValidationData entity=%d flags=0x%02x (isSignatureValidation=0)\n", h.entity, flags)
	fmt.Printf("B3 K10 stored order (%s): val=%s exec=%s\n", order, formatHooks(valHooks), formatHooks(execHooks))
	return nil
}

func (h *harness) readValidationData() (uint8, []hookRef, []hookRef, error) {
	parsed, err := abi.JSON(strings.NewReader(`[
	  {"type":"function","name":"getValidationData","stateMutability":"view",
	   "inputs":[{"name":"validationFunction","type":"bytes24"}],
	   "outputs":[{"name":"","type":"tuple","components":[
	     {"name":"validationFlags","type":"uint8"},
	     {"name":"validationHooks","type":"bytes25[]"},
	     {"name":"executionHooks","type":"bytes25[]"},
	     {"name":"selectors","type":"bytes4[]"}]}]}]`))
	if err != nil {
		return 0, nil, nil, err
	}
	moduleEntity := aa.PackModuleEntity(aa.SingleSignerValidationModuleAddress(), h.entity)
	data, err := parsed.Pack("getValidationData", moduleEntity)
	if err != nil {
		return 0, nil, nil, err
	}
	out, err := h.chain.CallContract(h.ctx, ethereum.CallMsg{To: &h.eoa, Data: data}, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	values, err := parsed.Methods["getValidationData"].Outputs.Unpack(out)
	if err != nil {
		return 0, nil, nil, err
	}
	if len(values) != 1 {
		return 0, nil, nil, fmt.Errorf("getValidationData returned %d values", len(values))
	}
	view, ok := values[0].(struct {
		ValidationFlags uint8       `json:"validationFlags"`
		ValidationHooks [][25]uint8 `json:"validationHooks"`
		ExecutionHooks  [][25]uint8 `json:"executionHooks"`
		Selectors       [][4]uint8  `json:"selectors"`
	})
	if !ok {
		return 0, nil, nil, fmt.Errorf("getValidationData returned %T", values[0])
	}
	return view.ValidationFlags, hooksFrom25(view.ValidationHooks), hooksFrom25(view.ExecutionHooks), nil
}

// assertLocated1271Reverts is B3(b): locator 0x01||entity||0xFF so the account
// looks up THIS session entity. Bare ECDSA is 65 bytes and never gets here
// (SMA-7702 v1.1.0 raw-mode, or v1.0.0 loadFromSignature of r[1:5] as a
// random entity). 0xFF is RESERVED_VALIDATION_DATA_INDEX; without it
// getFinalSegment reverts ValidationSignatureSegmentMissing before the flag
// check. Hooks are no-ops; empty per-hook segments are skipped when the next
// index is 0xFF.
func (h *harness) assertLocated1271Reverts() error {
	digest := permit2ShapedDigest(h.chainID, h.eoa)
	loc := make([]byte, 6)
	loc[0] = userop.ValidationOptionGlobal
	binary.BigEndian.PutUint32(loc[1:5], h.entity)
	loc[5] = userop.SigSegmentValidationData // 0xFF — final segment marker
	bytes32, err := abi.NewType("bytes32", "", nil)
	if err != nil {
		return err
	}
	bytesT, err := abi.NewType("bytes", "", nil)
	if err != nil {
		return err
	}
	payload, err := abi.Arguments{{Type: bytes32}, {Type: bytesT}}.Pack(digest, loc)
	if err != nil {
		return err
	}
	sel := crypto.Keccak256([]byte("isValidSignature(bytes32,bytes)"))[:4]
	out, err := h.chain.CallContract(h.ctx, ethereum.CallMsg{To: &h.eoa, Data: append(sel, payload...)}, nil)
	if err == nil {
		return fmt.Errorf("B3 FAIL: located isValidSignature succeeded for session entity %d; ret=%s", h.entity, hex.EncodeToString(out))
	}
	if isInfraError(err) {
		return fmt.Errorf("B3 FAIL: infra error, not a 1271 deny: %s", showErr(err))
	}
	invalidSel := crypto.Keccak256([]byte("SignatureValidationInvalid(bytes24)"))[:4]
	wantEntity := aa.PackModuleEntity(aa.SingleSignerValidationModuleAddress(), h.entity)
	data := revertData(err)
	if len(data) >= 28 && bytes.Equal(data[:4], invalidSel) && bytes.Equal(data[4:28], wantEntity[:]) {
		fmt.Printf("B3 PASS: located isValidSignature reverted SignatureValidationInvalid(session entity %d) data=%s\n",
			h.entity, hex.EncodeToString(data[:min(36, len(data))]))
		return nil
	}
	if len(data) >= 4 && bytes.Equal(data[:4], invalidSel) {
		return fmt.Errorf("B3 FAIL: SignatureValidationInvalid but ModuleEntity %s want %s",
			hex.EncodeToString(data[4:min(28, len(data))]), hex.EncodeToString(wantEntity[:]))
	}
	// geth jsonError.Error() is "execution reverted" and may still carry data
	// on ErrorData(); if even that is empty, the flag read above is the proof.
	fmt.Printf("B3 note: located 1271 reverted (%s) data=%s (flag read is the evidence if selector stripped)\n",
		showErr(err), hex.EncodeToString(data))
	return nil
}

type hookRef struct {
	module common.Address
	entity uint32
	flags  byte
}

func hooksFrom25(configs [][25]uint8) []hookRef {
	out := make([]hookRef, 0, len(configs))
	for _, c := range configs {
		out = append(out, hookRef{
			module: common.BytesToAddress(c[:20]),
			entity: binary.BigEndian.Uint32(c[20:24]),
			flags:  c[24],
		})
	}
	return out
}

func expectedNativeGrantHooks(entity uint32) (val, exec []hookRef) {
	val = []hookRef{
		{aa.AllowlistModuleAddress(), entity, aa.HookFlagValidation},
		{aa.NativeTokenLimitModuleAddress(), entity, aa.HookFlagValidation},
		{aa.TimeRangeModuleAddress(), entity, aa.HookFlagValidation},
	}
	exec = []hookRef{
		{aa.AllowlistModuleAddress(), entity, aa.HookFlagExecHasPre},
		{aa.NativeTokenLimitModuleAddress(), entity, aa.HookFlagExecHasPre},
	}
	return val, exec
}

func reverseHooks(in []hookRef) []hookRef {
	out := make([]hookRef, len(in))
	for i := range in {
		out[len(in)-1-i] = in[i]
	}
	return out
}

func hooksEqual(a, b []hookRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func formatHooks(hooks []hookRef) string {
	parts := make([]string, 0, len(hooks))
	for _, h := range hooks {
		name := h.module.Hex()
		switch h.module {
		case aa.AllowlistModuleAddress():
			name = "Allowlist"
		case aa.NativeTokenLimitModuleAddress():
			name = "NT"
		case aa.TimeRangeModuleAddress():
			name = "TimeRange"
		}
		kind := "exec"
		if h.flags&aa.HookFlagValidation != 0 {
			kind = "val"
		}
		parts = append(parts, fmt.Sprintf("%s-%s@%d/0x%02x", name, kind, h.entity, h.flags))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func revertData(err error) []byte {
	if err == nil {
		return nil
	}
	type dataError interface{ ErrorData() interface{} }
	for e := err; e != nil; e = errors.Unwrap(e) {
		d, ok := e.(dataError)
		if !ok {
			continue
		}
		switch v := d.ErrorData().(type) {
		case []byte:
			return v
		case string:
			b, decErr := hex.DecodeString(strings.TrimPrefix(v, "0x"))
			if decErr == nil {
				return b
			}
		}
	}
	return nil
}

func permit2ShapedDigest(chainID *big.Int, eoa common.Address) common.Hash {
	permit2 := common.HexToAddress("0x000000000022D473030F116dDEE9F6B43aC78BA3")
	domain := crypto.Keccak256(
		crypto.Keccak256([]byte("EIP712Domain(string name,uint256 chainId,address verifyingContract)")),
		crypto.Keccak256([]byte("Permit2")),
		common.LeftPadBytes(chainID.Bytes(), 32),
		common.LeftPadBytes(permit2.Bytes(), 32),
	)
	inner := crypto.Keccak256(
		crypto.Keccak256([]byte("Permit(address owner,address spender,uint256 value)")),
		common.LeftPadBytes(eoa.Bytes(), 32),
		common.LeftPadBytes(bob.Bytes(), 32),
		common.LeftPadBytes(big.NewInt(1).Bytes(), 32),
	)
	return common.BytesToHash(crypto.Keccak256([]byte{0x19, 0x01}, domain, inner))
}

func (h *harness) ownerCall(data []byte) error {
	nonce, err := h.chain.PendingNonceAt(h.ctx, h.eoa)
	if err != nil {
		return err
	}
	tip, err := h.chain.SuggestGasTipCap(h.ctx)
	if err != nil {
		return err
	}
	head, err := h.chain.HeaderByNumber(h.ctx, nil)
	if err != nil {
		return err
	}
	fee := eip1559.MaxFeeFromTipAndBase(tip, head.BaseFee)
	gas, err := h.chain.EstimateGas(h.ctx, ethereum.CallMsg{From: h.eoa, To: &h.eoa, Data: data})
	if err != nil {
		return fmt.Errorf("owner call estimate: %w", err)
	}
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: h.chainID, Nonce: nonce, GasTipCap: tip, GasFeeCap: fee,
		Gas: gas + 50_000, To: &h.eoa, Data: data,
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(h.chainID), h.ownerKey)
	if err != nil {
		return err
	}
	if err := h.chain.SendTransaction(h.ctx, signed); err != nil {
		return err
	}
	if _, err := waitMined(h.ctx, h.chain, signed.Hash(), true); err != nil {
		return err
	}
	fmt.Printf("owner runtime call mined tx=%s\n", signed.Hash())
	return nil
}

func (h *harness) priceOp(op *userop.UserOperationV07, encoded, ownerSig []byte) error {
	maxFee, tip, err := eip1559.SuggestFee(h.chain)
	if err != nil {
		return err
	}
	op.MaxPriorityFeePerGas = tip
	op.MaxFeePerGas = maxFee
	if encoded != nil {
		sig, err := preset.DeferredEstimationSignature(encoded, ownerSig)
		if err != nil {
			return err
		}
		op.Signature = sig
		defer func() { op.Signature = nil }()
	}
	est, err := preset.EstimateUserOpGasV07(h.ctx, h.bundler, op, h.entryPoint)
	if err != nil {
		return fmt.Errorf("estimating gas: %w", err)
	}
	if est != nil {
		if est.CallGasLimit != nil {
			op.CallGasLimit = est.CallGasLimit
		}
		if est.VerificationGasLimit != nil {
			op.VerificationGasLimit = est.VerificationGasLimit
		}
		if est.PreVerificationGas != nil {
			op.PreVerificationGas = est.PreVerificationGas
		}
	}
	return nil
}

func requireRevert(err error, proof string, needles ...string) error {
	if err == nil {
		return fmt.Errorf("%s FAIL: expected revert", proof)
	}
	if isInfraError(err) {
		return fmt.Errorf("%s FAIL: infra error, not a module revert: %s", proof, showErr(err))
	}
	msg := err.Error()
	for _, n := range needles {
		if containsAny(msg, n) {
			fmt.Printf("%s PASS (%s)\n  %s\n", proof, n, showErr(err))
			return nil
		}
	}
	return fmt.Errorf("%s FAIL: %s (want %v)", proof, showErr(err), needles)
}

func isInfraError(err error) bool {
	if err == nil {
		return false
	}
	return containsAny(err.Error(),
		"timeout", "timed out", "429", "rate limit", "too many requests",
		"no such host", "connection refused", "connection reset", "eof",
		"401", "403", "unauthorized",
		"502", "503", "dial tcp", "i/o timeout", "context deadline")
}

func exceededNTNeedle() string {
	sel := crypto.Keccak256([]byte("ExceededNativeTokenLimit()"))[:4]
	return "0x" + hex.EncodeToString(sel)
}

func readNativeLimit(ctx context.Context, chain *ethclient.Client, entity uint32, account common.Address) (*big.Int, error) {
	sel := crypto.Keccak256([]byte("limits(uint256,address)"))[:4]
	data := append(sel, common.LeftPadBytes(big.NewInt(int64(entity)).Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(account.Bytes(), 32)...)
	mod := common.HexToAddress(nativeTokenLimitHex)
	out, err := chain.CallContract(ctx, ethereum.CallMsg{To: &mod, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("reading NativeTokenLimitModule.limits: %w", err)
	}
	return new(big.Int).SetBytes(out), nil
}

type userOpReceipt struct {
	Success       bool
	ActualGasUsed *big.Int
	TxHash        string
	Reason        string
}

func receiptReason(r *userOpReceipt) string {
	if r == nil {
		return "nil"
	}
	return r.Reason
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
				Reason        string `json:"reason"`
				RevertReason  string `json:"revertReason"`
				Receipt       struct {
					TransactionHash string `json:"transactionHash"`
				} `json:"receipt"`
			}
			if err := json.Unmarshal(raw, &parsed); err != nil {
				return nil, err
			}
			gasUsed, _ := new(big.Int).SetString(strings.TrimPrefix(parsed.ActualGasUsed, "0x"), 16)
			reason := parsed.Reason
			if reason == "" {
				reason = parsed.RevertReason
			}
			return &userOpReceipt{
				Success: parsed.Success, ActualGasUsed: gasUsed,
				TxHash: parsed.Receipt.TransactionHash, Reason: reason,
			}, nil
		}
		time.Sleep(4 * time.Second)
	}
	return nil, fmt.Errorf("no receipt for %s", opHash)
}

func waitMined(ctx context.Context, chain *ethclient.Client, hash common.Hash, requireSuccess bool) (*types.Receipt, error) {
	for range 60 {
		receipt, err := chain.TransactionReceipt(ctx, hash)
		if err == nil {
			if requireSuccess && receipt.Status != types.ReceiptStatusSuccessful {
				return receipt, fmt.Errorf("tx %s mined but failed", hash)
			}
			return receipt, nil
		}
		time.Sleep(3 * time.Second)
	}
	return nil, fmt.Errorf("tx %s not mined", hash)
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

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func showErr(err error) string {
	if err == nil {
		return ""
	}
	return redactSecrets(firstLine(err.Error()))
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func firstNonEmpty(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func truthyEnv(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes"
}

func alchemyURL(chainID int64, key string) string {
	sub := map[int64]string{11155111: "eth-sepolia", 8453: "base-mainnet"}[chainID]
	if sub == "" {
		return ""
	}
	return "https://" + sub + ".g.alchemy.com/v2/" + key
}

func isRetiredBundler(u string) bool {
	return containsAny(u,
		"bundler-sepolia.avaprotocol.org",
		"bundler-base.avaprotocol.org",
		"bundler-base-sepolia.avaprotocol.org",
		"bundler-ethereum.avaprotocol.org",
		"bundler-proxy.avaprotocol.org")
}

func redactSecrets(s string) string {
	if k := firstNonEmpty("ALCHEMY_API_KEY"); k != "" {
		s = strings.ReplaceAll(s, k, "***")
	}
	for {
		i := strings.Index(strings.ToLower(s), "apikey=")
		if i < 0 {
			break
		}
		rest := s[i+7:]
		end := len(rest)
		for j, r := range rest {
			if r == '&' || r == '"' || r == ' ' || r == '\n' {
				end = j
				break
			}
		}
		s = s[:i+7] + "***" + rest[end:]
		break
	}
	const marker = ".g.alchemy.com/v2/"
	if i := strings.Index(s, marker); i >= 0 {
		rest := s[i+len(marker):]
		end := 0
		for end < len(rest) && rest[end] != '/' && rest[end] != ' ' && rest[end] != '"' && rest[end] != '\n' {
			end++
		}
		s = s[:i+len(marker)] + "***" + rest[end:]
	}
	return s
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
		return nil, common.Address{}, fmt.Errorf("%s: %w", name, err)
	}
	return key, crypto.PubkeyToAddress(key.PublicKey), nil
}

func resolveDelegatedEOA() (*ecdsa.PrivateKey, common.Address, error) {
	if key, addr, err := requireKey("SPIKE_7702_KEY"); err == nil {
		return key, addr, nil
	}
	if truthyEnv("SPIKE_7702_USE_TEST_KEY") {
		key, addr, err := requireKey("TEST_PRIVATE_KEY")
		if err != nil {
			return nil, common.Address{}, err
		}
		fmt.Printf("WARNING: 7702-delegating TEST_PRIVATE_KEY EOA %s (Track A fixture owner). SPIKE_7702_USE_TEST_KEY is set.\n", addr)
		return key, addr, nil
	}
	return loadOrCreateThrowaway()
}

func loadOrCreateThrowaway() (*ecdsa.PrivateKey, common.Address, error) {
	path, err := throwawayKeyPath()
	if err != nil {
		return nil, common.Address{}, err
	}
	if raw, err := os.ReadFile(path); err == nil {
		hexKey := strings.TrimPrefix(strings.TrimSpace(string(raw)), "0x")
		key, err := crypto.HexToECDSA(hexKey)
		if err != nil {
			return nil, common.Address{}, fmt.Errorf("%s: %w", path, err)
		}
		addr := crypto.PubkeyToAddress(key.PublicKey)
		fmt.Printf("loaded throwaway 7702 EOA %s from %s\n", addr, path)
		return key, addr, nil
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		return nil, common.Address{}, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(crypto.FromECDSA(key))+"\n"), 0o600); err != nil {
		return nil, common.Address{}, err
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	fmt.Printf("wrote throwaway 7702 EOA %s to %s (gitignored; prefunded from TEST_PRIVATE_KEY)\n", addr, path)
	return key, addr, nil
}

func throwawayKeyPath() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "scripts/spike/mav2_7702_eoa/.eoa.key"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("go.mod not found; run from the repo")
}

func resolveController(eoa common.Address) (*ecdsa.PrivateKey, common.Address, error) {
	if key, addr, err := requireKey("SPIKE_CONTROLLER_KEY"); err == nil {
		if addr == eoa {
			return nil, common.Address{}, fmt.Errorf("SPIKE_CONTROLLER_KEY is the delegated EOA; B3 1271 deny needs a distinct session key")
		}
		return key, addr, nil
	}
	if key, addr, err := requireKey("CONTROLLER_PRIVATE_KEY"); err == nil {
		if addr == eoa {
			return nil, common.Address{}, fmt.Errorf("CONTROLLER_PRIVATE_KEY is the delegated EOA; B3 needs a distinct session key (not falling back to ephemeral)")
		}
		fmt.Printf("session signer from CONTROLLER_PRIVATE_KEY %s\n", addr)
		return key, addr, nil
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		return nil, common.Address{}, err
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	fmt.Printf("ephemeral session signer %s (not persisted)\n", addr)
	return key, addr, nil
}

func ensurePrefunded(ctx context.Context, chain *ethclient.Client, chainID *big.Int, eoa common.Address) error {
	bal, err := chain.BalanceAt(ctx, eoa, nil)
	if err != nil {
		return err
	}
	if bal.Cmp(minBalance) >= 0 {
		fmt.Printf("eoa balance %s wei\n", bal)
		return nil
	}
	sponsorKey, sponsor, err := requireKey("SPIKE_OWNER_KEY", "TEST_PRIVATE_KEY")
	if err != nil {
		return fmt.Errorf("eoa %s has %s wei; fund it with ~0.03 ETH or set TEST_PRIVATE_KEY to prefund: %w", eoa, bal, err)
	}
	if sponsor == eoa {
		return fmt.Errorf("eoa %s has %s wei (below %s); fund it before delegating", eoa, bal, minBalance)
	}
	fmt.Printf("prefunding %s wei from %s -> %s (have %s)\n", prefundWei, sponsor, eoa, bal)
	return sendETH(ctx, chain, chainID, sponsorKey, sponsor, eoa, prefundWei)
}

func sendETH(ctx context.Context, chain *ethclient.Client, chainID *big.Int,
	key *ecdsa.PrivateKey, from, to common.Address, amount *big.Int) error {

	nonce, err := chain.PendingNonceAt(ctx, from)
	if err != nil {
		return err
	}
	gasPrice, err := chain.SuggestGasPrice(ctx)
	if err != nil {
		return err
	}
	gasLimit, err := chain.EstimateGas(ctx, ethereum.CallMsg{From: from, To: &to, Value: amount})
	if err != nil {
		return err
	}
	tx := types.NewTransaction(nonce, to, amount, gasLimit+10_000, gasPrice, nil)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), key)
	if err != nil {
		return err
	}
	if err := chain.SendTransaction(ctx, signed); err != nil {
		return fmt.Errorf("prefunding: %w", err)
	}
	if _, err := waitMined(ctx, chain, signed.Hash(), true); err != nil {
		return err
	}
	fmt.Printf("prefunded %s wei to %s tx=%s\n", amount, to, signed.Hash())
	return nil
}

func pickEntityBase() uint32 {
	if v := strings.TrimSpace(os.Getenv("SPIKE_ENTITY_BASE")); v != "" {
		n, ok := new(big.Int).SetString(v, 10)
		if ok && n.Sign() > 0 && n.IsUint64() && n.Uint64() < 1<<32 {
			return uint32(n.Uint64())
		}
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return 10_000 + binary.BigEndian.Uint32(b)%1_000_000
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
		if os.Getenv(key) != "" {
			continue
		}
		switch key {
		case "SPIKE_7702_KEY", "SPIKE_CONTROLLER_KEY", "SPIKE_BUNDLER_URL", "SPIKE_RPC_URL",
			"SPIKE_CHAIN", "SPIKE_ENTITY_BASE", "SPIKE_7702_USE_TEST_KEY", "SPIKE_OWNER_KEY",
			"TEST_PRIVATE_KEY", "CONTROLLER_PRIVATE_KEY",
			"SEPOLIA_BUNDLER_URL", "SEPOLIA_RPC_URL", "BASE_RPC_URL", "BASE_BUNDLER_URL",
			"ALCHEMY_API_KEY", "ETH_RPC_URL":
			_ = os.Setenv(key, val)
		default:
			if strings.HasPrefix(key, "SPIKE_") {
				_ = os.Setenv(key, val)
			}
		}
	}
}
