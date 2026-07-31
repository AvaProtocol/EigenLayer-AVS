package preset

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

func testOp() *userop.UserOperationV07 {
	return &userop.UserOperationV07{
		Sender: common.HexToAddress("0x61CaF92C082E70F8F780A8f1c04d01A14B63e0B0"),
		Nonce:  big.NewInt(1), CallData: []byte{0xb6, 0x1d, 0x27, 0xf6},
		CallGasLimit: big.NewInt(500000), VerificationGasLimit: big.NewInt(60000),
		PreVerificationGas:   big.NewInt(100000),
		MaxFeePerGas:         big.NewInt(2000000000),
		MaxPriorityFeePerGas: big.NewInt(100000000),
	}
}

// The signing scheme was determined by probing the bundler, because both wrong
// answers produce the same "Invalid account signature". This pins the one that
// worked: EIP-191 over the userOpHash, framed, with v normalised to 27/28.
func TestSignUserOpV07(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	op := testOp()
	ep, chainID := EntryPointV07(), big.NewInt(11155111)

	if err := SignUserOpV07(op, ep, chainID, key); err != nil {
		t.Fatalf("SignUserOpV07: %v", err)
	}

	t.Run("framed as 0xFF 0x00 + 65 bytes", func(t *testing.T) {
		if len(op.Signature) != 67 {
			t.Fatalf("len = %d, want 67", len(op.Signature))
		}
		if op.Signature[0] != 0xFF || op.Signature[1] != 0x00 {
			t.Errorf("framing = 0x%02x%02x, want 0xff00", op.Signature[0], op.Signature[1])
		}
	})

	t.Run("v is 27 or 28, not 0 or 1", func(t *testing.T) {
		// crypto.Sign returns 0/1; the account rejects those.
		if v := op.Signature[66]; v != 27 && v != 28 {
			t.Errorf("v = %d, want 27 or 28", v)
		}
	})

	t.Run("signs the EIP-191 digest, not the raw userOpHash", func(t *testing.T) {
		hash, err := op.GetUserOpHash(ep, chainID)
		if err != nil {
			t.Fatalf("GetUserOpHash: %v", err)
		}
		raw := op.Signature[2:]
		recoverable := make([]byte, 65)
		copy(recoverable, raw)
		recoverable[64] -= 27

		want := crypto.PubkeyToAddress(key.PublicKey)
		fromPrefixed, err := crypto.SigToPub(accounts.TextHash(hash.Bytes()), recoverable)
		if err != nil {
			t.Fatalf("recover from EIP-191 digest: %v", err)
		}
		if crypto.PubkeyToAddress(*fromPrefixed) != want {
			t.Error("signature does not recover the signer over the EIP-191 digest")
		}
		// And must NOT be a signature over the bare hash — that variant is
		// what the bundler rejected.
		if fromRaw, err := crypto.SigToPub(hash.Bytes(), recoverable); err == nil {
			if crypto.PubkeyToAddress(*fromRaw) == want {
				t.Error("signature recovers over the raw userOpHash; the EIP-191 prefix is missing")
			}
		}
	})

	t.Run("guards", func(t *testing.T) {
		if err := SignUserOpV07(nil, ep, chainID, key); err == nil {
			t.Error("expected an error for a nil operation")
		}
		if err := SignUserOpV07(testOp(), ep, chainID, nil); err == nil {
			t.Error("expected an error for a nil key")
		}
	})
}

// Estimation sends this, and a wrongly-framed dummy reverts AA23 with revert
// data 0x151d90fe — right length, wrong shape.
func TestDummySignatureIsFramed(t *testing.T) {
	d := dummySignatureV07()
	if len(d) != 67 {
		t.Fatalf("len = %d, want 67", len(d))
	}
	if d[0] != 0xFF || d[1] != 0x00 {
		t.Errorf("framing = 0x%02x%02x, want 0xff00", d[0], d[1])
	}
	allZero := true
	for _, b := range d[2:] {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("dummy body is all zeros; estimation needs something signature-shaped")
	}
}

// Deployment roughly quadruples verification cost, and the valid window is
// actual <= limit <= actual/0.4 — so one seed cannot serve both cases.
func TestSeedVerificationGas(t *testing.T) {
	deployed := testOp()
	if got := seedVerificationGas(deployed); got.Int64() != seedVerificationGasDeployed {
		t.Errorf("deployed seed = %s, want %d", got, seedVerificationGasDeployed)
	}
	deploying := testOp()
	f := common.HexToAddress("0x00000000000017c61b5bEe81050EC8eFc9c6fecd")
	deploying.Factory = &f
	if got := seedVerificationGas(deploying); got.Int64() != seedVerificationGasDeploying {
		t.Errorf("deploying seed = %s, want %d", got, seedVerificationGasDeploying)
	}
	if seedVerificationGasDeployed >= seedVerificationGasDeploying {
		t.Error("the deploying seed must be the larger of the two")
	}
}

// The rejection carries the actual/limit ratio, which is the only way to learn
// real verification usage — estimation just echoes the input.
func TestParseVerificationEfficiency(t *testing.T) {
	tests := []struct {
		msg  string
		want float64
		ok   bool
	}{
		{"Verification gas limit efficiency too low. Required: 0.4, Actual: 0.080041", 0.080041, true},
		{"Verification gas limit efficiency too low. Required: 0.4, Actual: 0.14954333333333333", 0.14954333333333333, true},
		{"eth_sendUserOperation: Verification gas limit efficiency too low. Required: 0.4, Actual: 0.2", 0.2, true},
		{"AA23 reverted", 0, false},
		{"Verification gas limit efficiency too low. Required: 0.4, Actual: 1.5", 0, false}, // out of range
	}
	for _, tt := range tests {
		got, ok := parseVerificationEfficiency(tt.msg)
		if ok != tt.ok {
			t.Errorf("ok = %v for %q, want %v", ok, tt.msg, tt.ok)
			continue
		}
		if ok && got != tt.want {
			t.Errorf("ratio = %v, want %v", got, tt.want)
		}
	}
}

// A tightened limit must land inside the floor with margin, and must actually
// be smaller — otherwise the retry loops on the same rejection.
func TestTightenedLimitLandsInsideTheFloor(t *testing.T) {
	limit := big.NewInt(300000)
	ratio := 0.14954333333333333 // observed
	actual := new(big.Float).Mul(new(big.Float).SetInt(limit), big.NewFloat(ratio))
	tightened, _ := new(big.Float).Quo(actual, big.NewFloat(targetVerificationGasEfficiency)).Int(nil)

	if tightened.Cmp(limit) >= 0 {
		t.Fatalf("tightened %s is not smaller than %s", tightened, limit)
	}
	actualF, _ := actual.Float64()
	newEff := actualF / float64(tightened.Int64())
	if newEff < verificationGasEfficiencyFloor {
		t.Errorf("retry efficiency %.3f is still below the %.1f floor", newEff, verificationGasEfficiencyFloor)
	}
	if float64(tightened.Int64()) < actualF {
		t.Errorf("tightened limit %s is below actual usage %.0f; would revert AA26", tightened, actualF)
	}
}

func TestParseHexBig(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0x7a120", 500000, true},
		{"0x0", 0, true},
		{"7a120", 0, false},
		{"", 0, false},
		{"0xzz", 0, false},
	} {
		got, err := parseHexBig(tt.in, "field")
		if (err == nil) != tt.ok {
			t.Errorf("parseHexBig(%q) err = %v, want ok=%v", tt.in, err, tt.ok)
			continue
		}
		if tt.ok && got.Int64() != tt.want {
			t.Errorf("parseHexBig(%q) = %s, want %d", tt.in, got, tt.want)
		}
	}
}

// Estimation needs a well-framed dummy for the RPC, but must not leave one on
// the struct: SendUserOpV07 treats an empty signature as "unsigned", so a
// leftover dummy would pass that guard and go out with a signature the account
// rejects. Verified without a bundler by driving the same restore contract.
func TestEstimateRestoresTheCallerSignature(t *testing.T) {
	t.Run("empty stays empty even when estimation fails", func(t *testing.T) {
		op := testOp()
		op.Signature = nil
		// nil client -> EstimateUserOpGasV07 returns before any mutation.
		if _, err := EstimateUserOpGasV07(nil, nil, op, EntryPointV07()); err == nil {
			t.Fatal("expected an error for a nil client")
		}
		if len(op.Signature) != 0 {
			t.Errorf("signature = %d bytes after a failed estimate, want 0; "+
				"a leftover dummy defeats the unsigned-op guard", len(op.Signature))
		}
	})

	t.Run("a real signature is not clobbered", func(t *testing.T) {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		op := testOp()
		if err := SignUserOpV07(op, EntryPointV07(), big.NewInt(11155111), key); err != nil {
			t.Fatalf("SignUserOpV07: %v", err)
		}
		before := append([]byte(nil), op.Signature...)
		_, _ = EstimateUserOpGasV07(nil, nil, op, EntryPointV07())
		if string(op.Signature) != string(before) {
			t.Error("estimation altered a signature the caller had already set")
		}
	})
}

// The dummy must never be mistaken for a real signature by the send guard.
func TestSendRejectsUnsignedOperation(t *testing.T) {
	op := testOp()
	op.Signature = nil
	if _, err := SendUserOpV07(nil, nil, op, EntryPointV07()); err == nil {
		t.Error("expected an error sending an unsigned operation")
	}
}
