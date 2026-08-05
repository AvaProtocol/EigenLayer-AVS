package preset

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

// EntryPointV07Address is the same on every chain Alchemy supports. It is an
// alias for config's constant rather than a second literal: two copies of an
// address are two things to get out of step, and a mismatch would surface as
// signatures that verify nowhere.
const EntryPointV07Address = config.EntryPointV07AddressHex

// EntryPointV07 returns the v0.7 EntryPoint address.
func EntryPointV07() common.Address { return common.HexToAddress(EntryPointV07Address) }

// selectorGetNonce is EntryPoint.getNonce(address,uint192) -> uint256.
//
//	cast sig "getNonce(address,uint192)"  =>  0x35567e1a
//
// Asserted in the tests rather than trusted: NextNonceV07 is only exercised
// against a live bundler, so a wrong selector here would return garbage that
// reads as an AA25 nonce mismatch rather than as a bad call — and would never
// surface in CI.
const selectorGetNonce = "0x35567e1a"

// Rundler does NOT compute verificationGasLimit. Measured against Alchemy's
// Sepolia bundler, eth_estimateUserOperationGas echoes whatever the caller
// sent:
//
//	seed  20,000 -> AA26 over verificationGasLimit
//	seed  50,000 -> estimate returns 50,000
//	seed 100,000 -> estimate returns 100,000
//	seed 300,000 -> estimate returns 300,000
//
// so the caller is squeezed from both sides. Below actual usage the operation
// reverts AA26; above actual/0.4 the send is refused:
//
//	Verification gas limit efficiency too low. Required: 0.4, Actual: 0.080041
//
// i.e. the valid window is actual <= limit <= actual/0.4, and "just pad it"
// is precisely the wrong instinct — a 2,000,000 limit on a 160,000 operation
// is what produces that error.
//
// The seeds below are sized from observed usage: ~160k when the operation
// also deploys the account, ~45k once it exists. SendUserOpV07 adapts from
// the bundler's own efficiency ratio if either is off, so these only need to
// be close, not exact.
const (
	seedVerificationGasDeploying = 200_000 // ~160k actual -> ~0.8 efficiency
	seedVerificationGasDeployed  = 60_000  // ~45k actual  -> ~0.75 efficiency

	// Session-entity seeds, all measured on Sepolia rather than derived. Each
	// step up is a specific cost the previous seed did not cover:
	//
	//   module entity      60k AA26s — validating through an installed module
	//                      is an external call, and the entity's first use
	//                      writes a cold nonce-key slot (~22k)
	//   deferred install   300k AA26s — the install itself runs inside
	//                      validation, writing cold module storage
	//   + permission hooks 300k AA26s again — every allowlist entry is its
	//                      own cold SSTORE, so cost scales with grant contents
	seedVerificationGasModuleEntity  = 100_000
	seedVerificationGasDeferredBare  = 400_000
	seedVerificationGasDeferredHooks = 700_000
	initialCallGasLimit              = 500_000
	initialPreVerificationGas        = 100_000

	// verificationGasEfficiencyFloor is Rundler's published threshold.
	verificationGasEfficiencyFloor = 0.4
	// targetVerificationGasEfficiency is what we retry at — comfortably inside
	// the floor so a small usage change between estimate and inclusion does
	// not push the operation back over.
	targetVerificationGasEfficiency = 0.75
)

// seedVerificationGas picks a starting limit from whether the operation also
// deploys the account, which roughly quadruples verification cost.
func seedVerificationGas(op *userop.UserOperationV07) *big.Int {
	if op.Factory != nil {
		return big.NewInt(seedVerificationGasDeploying)
	}
	return big.NewInt(seedVerificationGasDeployed)
}

// SignUserOpV07 signs the operation for a Modular Account v2 account and
// installs the framed signature.
//
// Two details, both established by probing Alchemy's Sepolia bundler rather
// than from the spec, because getting either wrong yields "Invalid account
// signature" with nothing to distinguish them:
//
//  1. The digest is the EIP-191 personal-sign hash of the userOpHash, not the
//     userOpHash itself. Signing the raw hash is rejected.
//  2. The result is framed as 0xFF (validation-data segment) + 0x00 (EOA
//     signature type) + the 65 signature bytes. See WrapSignatureMAv2.
//
// The v recovery byte is normalised to 27/28; go-ethereum's crypto.Sign
// returns 0/1, which the account rejects.
func SignUserOpV07(op *userop.UserOperationV07, entryPoint common.Address, chainID *big.Int, key *ecdsa.PrivateKey) error {
	if op == nil {
		return fmt.Errorf("nil user operation")
	}
	if key == nil {
		return fmt.Errorf("nil signing key")
	}
	hash, err := op.GetUserOpHash(entryPoint, chainID)
	if err != nil {
		return fmt.Errorf("computing userOpHash: %w", err)
	}
	sig, err := crypto.Sign(accounts.TextHash(hash.Bytes()), key)
	if err != nil {
		return fmt.Errorf("signing userOpHash %s: %w", hash.Hex(), err)
	}
	sig[64] += 27
	framed, err := userop.WrapSignatureMAv2(sig)
	if err != nil {
		return err
	}
	op.Signature = framed
	return nil
}

// dummySignatureV07 is a correctly-FRAMED placeholder for gas estimation.
//
// The framing matters as much as the length: 67 zero bytes is the right size
// but has 0x00 where the segment index belongs, which reverts validation with
// AA23 and revert data 0x151d90fe. Estimation needs something shaped like a
// real signature, not merely something the right length.
// Built directly rather than through WrapSignatureMAv2 so there is no error to
// discard: the length is fixed here, so the only possible failure is
// unreachable, and an ignored error would still trip errcheck.
func dummySignatureV07() []byte {
	framed := make([]byte, 67)
	framed[0] = userop.SigSegmentValidationData
	framed[1] = userop.SigTypeEOA
	// Non-zero body so ecrecover does real work during estimation; the exact
	// bytes are irrelevant because estimation does not verify the signer.
	for i := 2; i < 66; i++ {
		framed[i] = 0xAA
	}
	framed[66] = 0x1C
	return framed
}

// GasEstimateV07 is what the bundler returns from eth_estimateUserOperationGas.
type GasEstimateV07 struct {
	CallGasLimit         *big.Int
	VerificationGasLimit *big.Int
	PreVerificationGas   *big.Int
}

type rawGasEstimateV07 struct {
	CallGasLimit         string `json:"callGasLimit"`
	VerificationGasLimit string `json:"verificationGasLimit"`
	PreVerificationGas   string `json:"preVerificationGas"`
}

func parseHexBig(s, field string) (*big.Int, error) {
	if len(s) < 3 || s[:2] != "0x" {
		return nil, fmt.Errorf("%s: %q is not a hex quantity", field, s)
	}
	v, ok := new(big.Int).SetString(s[2:], 16)
	if !ok {
		return nil, fmt.Errorf("%s: cannot parse %q", field, s)
	}
	return v, nil
}

// EstimateUserOpGasV07 asks the bundler to size the operation.
//
// The operation is mutated in place with realistic seed limits before the
// call — see seedVerificationGas and the constants above for why the seed
// cannot simply be generous — and with the returned values afterwards.
//
// The signature is restored to whatever the caller set, including empty. A
// dummy is needed for the RPC (estimation reverts without a well-framed one)
// but must not survive the call: SendUserOpV07 treats an empty signature as
// "unsigned", so a dummy left behind would sail past that guard and go out
// with a signature the account rejects. Estimate first, then sign — the gas
// values are part of the hash.
func EstimateUserOpGasV07(ctx context.Context, client *rpc.Client, op *userop.UserOperationV07, entryPoint common.Address) (*GasEstimateV07, error) {
	if client == nil {
		return nil, fmt.Errorf("nil bundler client")
	}
	if op == nil {
		return nil, fmt.Errorf("nil user operation")
	}
	if op.CallGasLimit == nil {
		op.CallGasLimit = big.NewInt(initialCallGasLimit)
	}
	if op.VerificationGasLimit == nil {
		op.VerificationGasLimit = seedVerificationGas(op)
	}
	if op.PreVerificationGas == nil {
		op.PreVerificationGas = big.NewInt(initialPreVerificationGas)
	}
	callerSignature := op.Signature
	if len(op.Signature) == 0 {
		op.Signature = dummySignatureV07()
	}
	// Restore unconditionally, including on the error paths below — an
	// estimation failure must not leave a dummy signature behind either.
	defer func() { op.Signature = callerSignature }()

	payload, err := json.Marshal(op)
	if err != nil {
		return nil, fmt.Errorf("marshaling user operation: %w", err)
	}

	var raw rawGasEstimateV07
	if err := client.CallContext(ctx, &raw, "eth_estimateUserOperationGas",
		json.RawMessage(payload), entryPoint.Hex()); err != nil {
		return nil, fmt.Errorf("eth_estimateUserOperationGas: %w", err)
	}

	est := &GasEstimateV07{}
	for _, f := range []struct {
		dst  **big.Int
		src  string
		name string
	}{
		{&est.CallGasLimit, raw.CallGasLimit, "callGasLimit"},
		{&est.VerificationGasLimit, raw.VerificationGasLimit, "verificationGasLimit"},
		{&est.PreVerificationGas, raw.PreVerificationGas, "preVerificationGas"},
	} {
		v, err := parseHexBig(f.src, f.name)
		if err != nil {
			return nil, err
		}
		*f.dst = v
	}

	op.CallGasLimit = est.CallGasLimit
	op.VerificationGasLimit = est.VerificationGasLimit
	op.PreVerificationGas = est.PreVerificationGas
	return est, nil
}

// NextNonceV07 reads the account's next nonce for the validation entity the
// operation will use, and returns it already combined into the full nonce.
//
// The sequence counter is PER KEY. Asking the EntryPoint for the sequence
// under one key and then sending under another yields AA25 invalid account
// nonce, so the key is derived here from the same (entityID, options) that go
// into the returned nonce rather than being passed in separately.
func NextNonceV07(ctx context.Context, client *rpc.Client, entryPoint, sender common.Address, entityID uint32, options uint8) (*big.Int, error) {
	if client == nil {
		return nil, fmt.Errorf("nil client")
	}
	key, err := userop.NonceKeyMAv2(entityID, options)
	if err != nil {
		return nil, err
	}
	var padded [64]byte
	copy(padded[12:32], sender.Bytes())
	key.FillBytes(padded[32+8 : 64]) // uint192 occupies the low 24 bytes
	data := append(common.FromHex(selectorGetNonce), padded[:]...)

	var out string
	if err := client.CallContext(ctx, &out, "eth_call", map[string]interface{}{
		"to":   entryPoint.Hex(),
		"data": fmt.Sprintf("0x%x", data),
	}, "latest"); err != nil {
		return nil, fmt.Errorf("EntryPoint.getNonce: %w", err)
	}
	full, err := parseHexBig(out, "nonce")
	if err != nil {
		return nil, err
	}
	return full, nil
}

// SponsorshipRequestV07 asks Alchemy's Gas Manager to cover the operation.
type SponsorshipRequestV07 struct {
	PolicyID string
}

type sponsorshipResultV07 struct {
	Paymaster                     string `json:"paymaster"`
	PaymasterData                 string `json:"paymasterData"`
	PaymasterVerificationGasLimit string `json:"paymasterVerificationGasLimit"`
	PaymasterPostOpGasLimit       string `json:"paymasterPostOpGasLimit"`
	CallGasLimit                  string `json:"callGasLimit"`
	VerificationGasLimit          string `json:"verificationGasLimit"`
	PreVerificationGas            string `json:"preVerificationGas"`
	MaxFeePerGas                  string `json:"maxFeePerGas"`
	MaxPriorityFeePerGas          string `json:"maxPriorityFeePerGas"`
}

// RequestSponsorshipV07 fills the paymaster fields (and the gas values Gas
// Manager prices alongside them) from a Gas Manager policy.
//
// Verified against the live Sepolia policy: an MA v2 operation comes back with
// a real paymaster (0x2cc0c798…) and priced gas.
//
// Whatever the policy enforces applies here — spend caps and, once configured,
// the custom-rules webhook — and a denial surfaces as an RPC error rather than
// an unsponsored operation. That is the shape we want: an operation that
// silently fell back to self-funded would drain the account instead.
//
// Note the webhook is NOT currently configured on the policy (webhookRules is
// null), so nothing consults the gateway's FeeLedger gate today and any sender
// reaching the policy is sponsored within its caps. Enabling it is what makes
// the credit limit bind — and it also means MA v2 wallets must be registered
// in gateway storage first, or the webhook will refuse every one of them.
func RequestSponsorshipV07(ctx context.Context, client *rpc.Client, op *userop.UserOperationV07, entryPoint common.Address, req SponsorshipRequestV07) error {
	if client == nil {
		return fmt.Errorf("nil bundler client")
	}
	if op == nil {
		return fmt.Errorf("nil user operation")
	}
	if req.PolicyID == "" {
		return fmt.Errorf("gas manager policy id is empty")
	}

	payload, err := json.Marshal(op)
	if err != nil {
		return fmt.Errorf("marshaling user operation: %w", err)
	}
	// Prefer a signature already installed on the operation. Deferred-action
	// estimation MUST carry the real owner grant (a plain dummy reverts with
	// DeferredActionSignatureInvalid → AA23); SendUserOpMAv2 puts that grant
	// on op.Signature before pricing. Ordinary operations leave Signature
	// empty and fall through to the framed dummy.
	dummy := dummySignatureV07()
	if len(op.Signature) > 0 {
		dummy = op.Signature
	}
	params := map[string]interface{}{
		"policyId":       req.PolicyID,
		"entryPoint":     entryPoint.Hex(),
		"dummySignature": fmt.Sprintf("0x%x", dummy),
		"userOperation":  json.RawMessage(payload),
	}

	var res sponsorshipResultV07
	if err := client.CallContext(ctx, &res, "alchemy_requestGasAndPaymasterAndData", params); err != nil {
		return fmt.Errorf("alchemy_requestGasAndPaymasterAndData (policy %s): %w", req.PolicyID, err)
	}
	if res.Paymaster == "" {
		return fmt.Errorf("gas manager returned no paymaster for policy %s", req.PolicyID)
	}

	pm := common.HexToAddress(res.Paymaster)
	op.Paymaster = &pm
	if op.PaymasterData, err = decodeHexBytes(res.PaymasterData); err != nil {
		return fmt.Errorf("paymasterData: %w", err)
	}
	for _, f := range []struct {
		dst  **big.Int
		src  string
		name string
	}{
		{&op.PaymasterVerificationGasLimit, res.PaymasterVerificationGasLimit, "paymasterVerificationGasLimit"},
		{&op.PaymasterPostOpGasLimit, res.PaymasterPostOpGasLimit, "paymasterPostOpGasLimit"},
		{&op.CallGasLimit, res.CallGasLimit, "callGasLimit"},
		{&op.VerificationGasLimit, res.VerificationGasLimit, "verificationGasLimit"},
		{&op.PreVerificationGas, res.PreVerificationGas, "preVerificationGas"},
		{&op.MaxFeePerGas, res.MaxFeePerGas, "maxFeePerGas"},
		{&op.MaxPriorityFeePerGas, res.MaxPriorityFeePerGas, "maxPriorityFeePerGas"},
	} {
		if f.src == "" {
			continue // Gas Manager may leave fields it did not reprice
		}
		v, err := parseHexBig(f.src, f.name)
		if err != nil {
			return err
		}
		*f.dst = v
	}
	return nil
}

func decodeHexBytes(s string) ([]byte, error) {
	if s == "" {
		return []byte{}, nil
	}
	if len(s) < 2 || s[:2] != "0x" {
		return nil, fmt.Errorf("%q is not 0x-prefixed", s)
	}
	return common.FromHex(s), nil
}

// SendUserOpV07 submits a signed operation and returns its userOpHash.
//
// Retries once if the bundler refuses the verificationGasLimit as inefficient.
// The rejection carries the actual/limit ratio, which is the only way to learn
// real verification usage — estimation cannot tell us, since it just echoes
// the input. Re-signing is required because the gas limit is part of the hash,
// so the caller's key is needed for the retry; without it the error is
// returned unchanged.
func SendUserOpV07(ctx context.Context, client *rpc.Client, op *userop.UserOperationV07, entryPoint common.Address) (common.Hash, error) {
	return sendUserOpV07(ctx, client, op, entryPoint, nil, nil)
}

// SendUserOpV07WithRetry is SendUserOpV07 with the material needed to re-sign
// after tightening the verification gas limit.
func SendUserOpV07WithRetry(ctx context.Context, client *rpc.Client, op *userop.UserOperationV07, entryPoint common.Address, chainID *big.Int, key *ecdsa.PrivateKey) (common.Hash, error) {
	return sendUserOpV07(ctx, client, op, entryPoint, chainID, key)
}

func sendUserOpV07(ctx context.Context, client *rpc.Client, op *userop.UserOperationV07, entryPoint common.Address, chainID *big.Int, key *ecdsa.PrivateKey) (common.Hash, error) {
	if client == nil {
		return common.Hash{}, fmt.Errorf("nil bundler client")
	}
	if op == nil {
		return common.Hash{}, fmt.Errorf("nil user operation")
	}
	if len(op.Signature) == 0 {
		return common.Hash{}, fmt.Errorf("user operation is unsigned")
	}

	hash, err := rawSendUserOpV07(ctx, client, op, entryPoint)
	if err == nil {
		return hash, nil
	}
	ratio, ok := parseVerificationEfficiency(err.Error())
	if !ok || key == nil || chainID == nil {
		return common.Hash{}, err
	}

	// actual = limit * ratio; retry at a limit that puts us at the target
	// efficiency rather than just inside the floor.
	limit := new(big.Float).SetInt(op.VerificationGasLimit)
	actual := new(big.Float).Mul(limit, big.NewFloat(ratio))
	tightened, _ := new(big.Float).Quo(actual, big.NewFloat(targetVerificationGasEfficiency)).Int(nil)
	if tightened.Sign() <= 0 || tightened.Cmp(op.VerificationGasLimit) >= 0 {
		return common.Hash{}, err
	}
	op.VerificationGasLimit = tightened
	if signErr := SignUserOpV07(op, entryPoint, chainID, key); signErr != nil {
		return common.Hash{}, fmt.Errorf("re-signing after tightening verificationGasLimit to %s: %w", tightened, signErr)
	}
	return rawSendUserOpV07(ctx, client, op, entryPoint)
}

func rawSendUserOpV07(ctx context.Context, client *rpc.Client, op *userop.UserOperationV07, entryPoint common.Address) (common.Hash, error) {
	payload, err := json.Marshal(op)
	if err != nil {
		return common.Hash{}, fmt.Errorf("marshaling user operation: %w", err)
	}
	var hash string
	if err := client.CallContext(ctx, &hash, "eth_sendUserOperation",
		json.RawMessage(payload), entryPoint.Hex()); err != nil {
		return common.Hash{}, fmt.Errorf("eth_sendUserOperation: %w", err)
	}
	return common.HexToHash(hash), nil
}

// parseVerificationEfficiency pulls the actual/limit ratio out of Rundler's
// rejection, e.g.
//
//	Verification gas limit efficiency too low. Required: 0.4, Actual: 0.080041
func parseVerificationEfficiency(msg string) (float64, bool) {
	m := verificationEfficiencyRe.FindStringSubmatch(msg)
	if len(m) != 2 {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || v <= 0 || v >= 1 {
		return 0, false
	}
	return v, true
}

var verificationEfficiencyRe = regexp.MustCompile(`Verification gas limit efficiency too low\..*?Actual:\s*([0-9.]+)`)
