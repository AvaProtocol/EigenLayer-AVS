package aa

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

var (
	targetA = common.HexToAddress("0x981e18d5aade83620a6bd21990b5da0c797e1e5b")
	targetB = common.HexToAddress("0x71c8f4d7d5291edcb3a081802e7efb2788bd232e")
)

// The selectors are the contract's, not ours. These were read off the deployed
// SemiModularAccountBytecode implementation
// (0x000000000000c5A9089039570Dd36455b5C07383) — if a change here flips one,
// the calldata stops being callable rather than merely being encoded oddly.
func TestMAv2Selectors(t *testing.T) {
	single, err := PackExecuteMAv2(targetA, big.NewInt(1), []byte{0xde, 0xad})
	if err != nil {
		t.Fatalf("PackExecuteMAv2: %v", err)
	}
	if got := hexutil.Encode(single[:4]); got != SelectorExecuteMAv2 {
		t.Errorf("execute selector = %s, want %s", got, SelectorExecuteMAv2)
	}

	batch, err := PackExecuteBatchMAv2([]Call{{Target: targetA, Value: big.NewInt(1), Data: []byte{0xde}}})
	if err != nil {
		t.Fatalf("PackExecuteBatchMAv2: %v", err)
	}
	if got := hexutil.Encode(batch[:4]); got != SelectorExecuteBatchMAv2 {
		t.Errorf("executeBatch selector = %s, want %s", got, SelectorExecuteBatchMAv2)
	}

	// v0.6's executeBatch(address[],bytes[]) is 0x18dfb3c7 and is NOT on the
	// MA v2 implementation. Encoding against the old shape would produce
	// calldata no MA v2 account can dispatch.
	if hexutil.Encode(batch[:4]) == "0x18dfb3c7" {
		t.Error("encoded the v0.6 executeBatch shape")
	}
}

func TestPackExecuteMAv2RoundTrip(t *testing.T) {
	parsed, err := ensureModularAccountABI()
	if err != nil {
		t.Fatalf("abi: %v", err)
	}
	data := []byte{0xb6, 0x1d, 0x27, 0xf6, 0x01}
	packed, err := PackExecuteMAv2(targetA, big.NewInt(12345), data)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	args, err := parsed.Methods["execute"].Inputs.Unpack(packed[4:])
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if args[0].(common.Address) != targetA {
		t.Errorf("target = %v, want %v", args[0], targetA)
	}
	if args[1].(*big.Int).Cmp(big.NewInt(12345)) != 0 {
		t.Errorf("value = %v, want 12345", args[1])
	}
	if !bytes.Equal(args[2].([]byte), data) {
		t.Errorf("data = %x, want %x", args[2], data)
	}
}

func TestPackExecuteBatchMAv2RoundTrip(t *testing.T) {
	parsed, err := ensureModularAccountABI()
	if err != nil {
		t.Fatalf("abi: %v", err)
	}
	calls := []Call{
		{Target: targetA, Value: big.NewInt(0), Data: []byte{0x01, 0x02}},
		{Target: targetB, Value: big.NewInt(999), Data: []byte{0x03}},
	}
	packed, err := PackExecuteBatchMAv2(calls)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	args, err := parsed.Methods["executeBatch"].Inputs.Unpack(packed[4:])
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	decoded, ok := args[0].([]struct {
		Target common.Address `json:"target"`
		Value  *big.Int       `json:"value"`
		Data   []byte         `json:"data"`
	})
	if !ok {
		t.Fatalf("unexpected decoded type %T", args[0])
	}
	if len(decoded) != 2 {
		t.Fatalf("len = %d, want 2", len(decoded))
	}
	if decoded[0].Target != targetA || decoded[1].Target != targetB {
		t.Errorf("targets round-tripped wrong: %v, %v", decoded[0].Target, decoded[1].Target)
	}
	if decoded[1].Value.Cmp(big.NewInt(999)) != 0 {
		t.Errorf("per-call value lost: got %v want 999", decoded[1].Value)
	}
	if !bytes.Equal(decoded[0].Data, []byte{0x01, 0x02}) || !bytes.Equal(decoded[1].Data, []byte{0x03}) {
		t.Error("call data round-tripped wrong")
	}
}

// The v0.6 PackExecuteBatchWithValues hand-rolls its ABI encoding, with a
// comment attributing that to a go-ethereum bug encoding empty []byte in a
// dynamic array. This pins down whether the same workaround is needed for MA
// v2's tuple[] — if it encodes and round-trips cleanly, the manual encoder
// does not need to be carried forward.
func TestPackExecuteBatchMAv2HandlesEmptyCallData(t *testing.T) {
	parsed, err := ensureModularAccountABI()
	if err != nil {
		t.Fatalf("abi: %v", err)
	}
	calls := []Call{
		{Target: targetA, Value: big.NewInt(1_000_000), Data: []byte{}}, // plain ETH transfer
		{Target: targetB, Value: big.NewInt(0), Data: []byte{0xab}},
		{Target: targetA, Value: big.NewInt(0), Data: nil}, // nil, not just empty
	}
	packed, err := PackExecuteBatchMAv2(calls)
	if err != nil {
		t.Fatalf("pack with empty call data: %v", err)
	}
	args, err := parsed.Methods["executeBatch"].Inputs.Unpack(packed[4:])
	if err != nil {
		t.Fatalf("unpack with empty call data: %v", err)
	}
	decoded := args[0].([]struct {
		Target common.Address `json:"target"`
		Value  *big.Int       `json:"value"`
		Data   []byte         `json:"data"`
	})
	if len(decoded) != 3 {
		t.Fatalf("len = %d, want 3", len(decoded))
	}
	if len(decoded[0].Data) != 0 || len(decoded[2].Data) != 0 {
		t.Errorf("empty call data did not survive: %x / %x", decoded[0].Data, decoded[2].Data)
	}
	if decoded[0].Value.Cmp(big.NewInt(1_000_000)) != 0 {
		t.Errorf("value on an empty-data call was lost: %v", decoded[0].Value)
	}
}

func TestPackExecuteBatchMAv2Normalisation(t *testing.T) {
	t.Run("nil value and data are normalised", func(t *testing.T) {
		if _, err := PackExecuteBatchMAv2([]Call{{Target: targetA}}); err != nil {
			t.Errorf("nil Value/Data should be accepted: %v", err)
		}
	})
	t.Run("nil value on single execute is normalised", func(t *testing.T) {
		if _, err := PackExecuteMAv2(targetA, nil, nil); err != nil {
			t.Errorf("nil value/data should be accepted: %v", err)
		}
	})
	t.Run("empty batch is rejected", func(t *testing.T) {
		if _, err := PackExecuteBatchMAv2(nil); err == nil {
			t.Error("expected an error for an empty batch — it encodes fine and burns gas doing nothing")
		}
	})
}
