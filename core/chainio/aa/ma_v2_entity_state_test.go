package aa

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

// recordingCaller answers one canned value and remembers what it was asked.
type recordingCaller struct {
	to     common.Address
	data   []byte
	result []byte
	err    error
}

func (r *recordingCaller) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	if call.To != nil {
		r.to = *call.To
	}
	r.data = call.Data
	return r.result, r.err
}

func word(v *big.Int) []byte { return common.LeftPadBytes(v.Bytes(), 32) }

// The sequence is the low 64 bits. Reading the full nonce instead reports every
// entity as spent, because the key above those bits embeds the entity id — the
// check still compiles, still returns a number, and is wrong for every input
// except entity 0.
func TestEntityDeferredNonceSequenceReadsOnlyTheSequence(t *testing.T) {
	const entity = uint32(3)
	key, err := userop.NonceKeyMAv2(entity,
		userop.ValidationOptionGlobal|userop.ValidationOptionDeferredAction)
	require.NoError(t, err)
	require.NotZero(t, key.Sign(), "precondition: the key is non-zero, which is what makes the full nonce misleading")

	// getNonce returns key<<64 | sequence.
	full := new(big.Int).Lsh(key, 64)
	full.Or(full, big.NewInt(7))

	account := common.HexToAddress("0x0643f9d156e2CE584825511C548649F3289619c3")
	caller := &recordingCaller{result: word(full)}
	sequence, err := EntityDeferredNonceSequence(context.Background(), caller, account, entity)
	require.NoError(t, err)
	require.Equal(t, uint64(7), sequence,
		"the sequence is the low 64 bits; the rest is the key")

	// Pin the WIDTH too. A narrower mask agrees on every small sequence and
	// only diverges past its boundary, so a test that never crosses one cannot
	// tell 64 bits from 32.
	wide := uint64(1)<<40 | 5
	full = new(big.Int).Lsh(key, 64)
	full.Or(full, new(big.Int).SetUint64(wide))

	caller = &recordingCaller{result: word(full)}
	sequence, err = EntityDeferredNonceSequence(context.Background(), caller, account, entity)
	require.NoError(t, err)
	require.Equal(t, wide, sequence, "the sequence field is 64 bits wide")
}

// An entity that has never carried a deferred action must read zero, or the
// allocator has no way to tell a fresh entity from a spent one.
func TestEntityDeferredNonceSequenceIsZeroForAnUnusedEntity(t *testing.T) {
	key, err := userop.NonceKeyMAv2(9,
		userop.ValidationOptionGlobal|userop.ValidationOptionDeferredAction)
	require.NoError(t, err)

	caller := &recordingCaller{result: word(new(big.Int).Lsh(key, 64))}
	sequence, err := EntityDeferredNonceSequence(context.Background(), caller,
		common.HexToAddress("0x0643f9d156e2CE584825511C548649F3289619c3"), 9)
	require.NoError(t, err)
	require.Zero(t, sequence)
}

// The call has to reach the EntryPoint with the entity's DEFERRED key. A wrong
// options byte queries a different nonce key entirely, which answers zero for
// an entity that is in fact spent — the failure that produces an opaque AA23
// later, at grant time, with nothing to point at.
func TestEntityDeferredNonceSequenceQueriesTheDeferredKey(t *testing.T) {
	const entity = uint32(4)
	account := common.HexToAddress("0x0643f9d156e2CE584825511C548649F3289619c3")

	caller := &recordingCaller{result: word(big.NewInt(0))}
	_, err := EntityDeferredNonceSequence(context.Background(), caller, account, entity)
	require.NoError(t, err)

	require.Equal(t, common.HexToAddress(config.EntryPointV07AddressHex), caller.to,
		"the nonce lives on the EntryPoint, not on a module")
	require.Len(t, caller.data, 4+32+32, "getNonce(address,uint192)")
	require.Equal(t, account, common.BytesToAddress(caller.data[4+12:4+32]))

	wantKey, err := userop.NonceKeyMAv2(entity,
		userop.ValidationOptionGlobal|userop.ValidationOptionDeferredAction)
	require.NoError(t, err)
	require.Equal(t, wantKey, new(big.Int).SetBytes(caller.data[4+32:]),
		"must query the GLOBAL|DEFERRED key for this entity")
}

// A read failure must not surface as "sequence 0" — that reads as "this entity
// is free" and hands a spent entity to the next grant.
func TestEntityDeferredNonceSequenceReportsReadFailures(t *testing.T) {
	caller := &recordingCaller{result: word(big.NewInt(1))[:16]} // short return
	_, err := EntityDeferredNonceSequence(context.Background(), caller,
		common.HexToAddress("0x0643f9d156e2CE584825511C548649F3289619c3"), 1)
	require.Error(t, err)

	_, err = EntityDeferredNonceSequence(context.Background(), nil,
		common.HexToAddress("0x0643f9d156e2CE584825511C548649F3289619c3"), 1)
	require.Error(t, err, "no client is not evidence that an entity is free")
}

func TestEntityTimeRangeOnChainDecodesBothWords(t *testing.T) {
	account := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")
	const entity = uint32(2)
	until := uint64(1785541473) // 2026-08-13-ish
	after := uint64(0)

	result := append(word(new(big.Int).SetUint64(until)), word(new(big.Int).SetUint64(after))...)
	caller := &recordingCaller{result: result}

	gotUntil, gotAfter, err := EntityTimeRangeOnChain(context.Background(), caller, account, entity)
	require.NoError(t, err)
	require.Equal(t, until, gotUntil)
	require.Equal(t, after, gotAfter)

	require.Equal(t, TimeRangeModuleAddress(), caller.to)
	require.Len(t, caller.data, 4+32+32, "timeRanges(uint32,address)")
	require.Equal(t, account, common.BytesToAddress(caller.data[4+32+12:4+32+32]))
	require.Equal(t, big.NewInt(int64(entity)), new(big.Int).SetBytes(caller.data[4:4+32]))
}

func TestEntityTimeRangeOnChainReportsReadFailures(t *testing.T) {
	account := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")
	caller := &recordingCaller{result: word(big.NewInt(1))} // 32 bytes, need 64
	_, _, err := EntityTimeRangeOnChain(context.Background(), caller, account, 1)
	require.Error(t, err)

	_, _, err = EntityTimeRangeOnChain(context.Background(), nil, account, 1)
	require.Error(t, err, "no client is not evidence that an entity has no window")
}
