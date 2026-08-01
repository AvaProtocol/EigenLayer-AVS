package preset

import (
	"errors"
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

// The manager is package state; each test claims distinct senders so they do
// not interfere.

func mustNonce(t *testing.T, entityID uint32, options uint8, seq uint64) *big.Int {
	t.Helper()
	n, err := userop.EncodeNonceMAv2(entityID, options, seq)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// cacheAhead reads the cached value the way NextNonceV07Managed does once the
// chain read is in hand, without needing a live RPC.
func cachedNext(t *testing.T, sender common.Address, entityID uint32, options uint8) (*big.Int, bool) {
	t.Helper()
	slot, err := slotV07(sender, entityID, options)
	if err != nil {
		t.Fatal(err)
	}
	globalNonceManagerV07.mu.Lock()
	defer globalNonceManagerV07.mu.Unlock()
	v, ok := globalNonceManagerV07.next[slot]
	if !ok {
		return nil, false
	}
	return new(big.Int).Set(v), true
}

func TestNonceManagerV07AcceptedAdvancesTheSlot(t *testing.T) {
	sender := common.HexToAddress("0x00000000000000000000000000000000000000A1")
	used := mustNonce(t, 1, userop.ValidationOptionGlobal, 0)

	NoteUserOpAccepted(sender, 1, userop.ValidationOptionGlobal, used)

	next, ok := cachedNext(t, sender, 1, userop.ValidationOptionGlobal)
	if !ok {
		t.Fatal("acceptance did not populate the slot")
	}
	want := mustNonce(t, 1, userop.ValidationOptionGlobal, 1)
	if next.Cmp(want) != 0 {
		t.Fatalf("cached next = %s, want %s (used+1)", next, want)
	}

	InvalidateNonce(sender, 1, userop.ValidationOptionGlobal)
	if _, ok := cachedNext(t, sender, 1, userop.ValidationOptionGlobal); ok {
		t.Fatal("invalidate did not drop the slot")
	}
}

// One account's counters are per (entity, options) — the whole reason this is
// not the v0.6 sender-keyed manager. Entity 1 plain, entity 1 deferred, and
// entity 2 must be three independent slots.
func TestNonceManagerV07SlotsAreIndependent(t *testing.T) {
	sender := common.HexToAddress("0x00000000000000000000000000000000000000A2")
	deferredOpts := uint8(userop.ValidationOptionGlobal | userop.ValidationOptionDeferredAction)

	NoteUserOpAccepted(sender, 1, userop.ValidationOptionGlobal, mustNonce(t, 1, userop.ValidationOptionGlobal, 4))

	if _, ok := cachedNext(t, sender, 1, deferredOpts); ok {
		t.Fatal("entity 1's plain acceptance leaked into its deferred slot")
	}
	if _, ok := cachedNext(t, sender, 2, userop.ValidationOptionGlobal); ok {
		t.Fatal("entity 1's acceptance leaked into entity 2's slot")
	}

	next, ok := cachedNext(t, sender, 1, userop.ValidationOptionGlobal)
	if !ok || next.Cmp(mustNonce(t, 1, userop.ValidationOptionGlobal, 5)) != 0 {
		t.Fatalf("entity 1 plain slot = %v, want sequence 5", next)
	}
}

func TestNonceManagerV07SendersAreIndependent(t *testing.T) {
	a := common.HexToAddress("0x00000000000000000000000000000000000000A3")
	b := common.HexToAddress("0x00000000000000000000000000000000000000A4")

	NoteUserOpAccepted(a, 1, userop.ValidationOptionGlobal, mustNonce(t, 1, userop.ValidationOptionGlobal, 7))
	if _, ok := cachedNext(t, b, 1, userop.ValidationOptionGlobal); ok {
		t.Fatal("sender A's acceptance leaked into sender B's slot")
	}
}

func TestVerifyDeferredNonceUnused(t *testing.T) {
	deferredOpts := uint8(userop.ValidationOptionGlobal | userop.ValidationOptionDeferredAction)

	if err := VerifyDeferredNonceUnused(mustNonce(t, 1, deferredOpts, 0)); err != nil {
		t.Fatalf("sequence 0 must be usable: %v", err)
	}
	err := VerifyDeferredNonceUnused(mustNonce(t, 1, deferredOpts, 1))
	if err == nil {
		t.Fatal("a consumed sequence must be rejected")
	}
	if !errorsMentionsReplay(err) {
		t.Fatalf("the error should say the grant cannot be replayed, got: %v", err)
	}
	if err := VerifyDeferredNonceUnused(nil); err == nil {
		t.Fatal("nil nonce must be rejected")
	}
}

func errorsMentionsReplay(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if containsAll(e.Error(), "already", "replay") {
			return true
		}
	}
	return false
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// The manager exists because the *previous* nonce bug was a concurrency bug,
// so it must be race-clean under concurrent access. Run with -race. This
// asserts no data race and that every concurrent accept lands a
// well-formed successor — not a sequential ordering (concurrent callers may
// interleave; the send path heals interleavings via AA25-invalidate-retry).
func TestNonceManagerV07ConcurrentAccessIsRaceFree(t *testing.T) {
	sender := common.HexToAddress("0x00000000000000000000000000000000000000A5")
	const goroutines, iterations = 8, 200

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(entityID uint32) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				used := mustNonce(t, entityID, userop.ValidationOptionGlobal, uint64(i))
				NoteUserOpAccepted(sender, entityID, userop.ValidationOptionGlobal, used)
				_, _ = cachedNext(t, sender, entityID, userop.ValidationOptionGlobal)
				if i%3 == 0 {
					InvalidateNonce(sender, entityID, userop.ValidationOptionGlobal)
				}
			}
		}(uint32(g + 1)) // distinct entity per goroutine: distinct slots, same map
	}
	wg.Wait()

	// After the storm, a fresh accept must still produce the exact successor —
	// the map is intact, not corrupted by the concurrent writers.
	NoteUserOpAccepted(sender, 1, userop.ValidationOptionGlobal, mustNonce(t, 1, userop.ValidationOptionGlobal, 41))
	next, ok := cachedNext(t, sender, 1, userop.ValidationOptionGlobal)
	if !ok || next.Cmp(mustNonce(t, 1, userop.ValidationOptionGlobal, 42)) != 0 {
		t.Fatalf("post-concurrency slot = %v, want sequence 42", next)
	}
}

func TestIsInvalidNonceError(t *testing.T) {
	if !isInvalidNonceError(errors.New("eth_sendUserOperation: validation reverted: [reason]: AA25 invalid account nonce")) {
		t.Fatal("AA25 not recognised")
	}
	if isInvalidNonceError(errors.New("AA23 reverted")) {
		t.Fatal("AA23 misclassified as a nonce error")
	}
	if isInvalidNonceError(nil) {
		t.Fatal("nil error misclassified")
	}
}
