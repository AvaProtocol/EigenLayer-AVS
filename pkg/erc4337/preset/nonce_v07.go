package preset

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

// v0.7 nonce management.
//
// EntryPoint.getNonce reflects MINED state only. A workflow with two
// sequential contract writes sends the second operation while the first sits
// in the bundler's mempool — a fresh getNonce returns the same sequence and
// the send dies AA25 invalid account nonce. The v0.6 path solved this with a
// per-sender cache (bundler.NonceManager); this is its v0.7 counterpart, with
// one structural difference that is the whole reason it is a separate type:
//
// MA v2 sequences are per NONCE KEY, not per sender. The 192-bit key encodes
// the validation entity and the option bits, so one account has independent
// counters for entity 1 plain, entity 1 deferred, entity 2, and so on. A
// sender-keyed cache would see entity 2's first operation, find entity 1's
// cached value, and hand out a sequence from the wrong counter. The cache key
// here is (sender, nonce key).
//
// Semantics are v0.6 parity, proven in production there:
//
//   - read     → max(on-chain, cached): the chain wins when pending work
//     mined or was dropped; the cache wins while work is in flight.
//   - accepted → cache = used+1, recorded when the BUNDLER accepts, not when
//     the operation mines. That is what lets the next operation overlap the
//     mempool — and it also covers the subtler failure where the operation
//     HAS mined but a load-balanced RPC replica serves a pre-mine getNonce.
//   - failure  → drop the slot; the next read is authoritative chain state.
//     AA25 covers both a duplicate (cache behind) and a gap (cache ahead
//     after a drop), so the caller's response to it is invalidate + re-read.
//
// Deferred operations NEVER go through the cache. A grant's carrying
// operation uses the nonce the owner signed over at grant time — sequence
// zero of a fresh key — and no cache may substitute another value: the
// digest would no longer match and the account would revert
// DeferredActionSignatureInvalid with nothing pointing at the nonce. See
// VerifyDeferredNonceUnused for the check that replaces caching there.
//
// Known limit, inherited from v0.6: reads do not RESERVE. Two goroutines
// racing the same (sender, key) between read and accept can draw the same
// sequence; the loser gets AA25 and heals through invalidate + retry. Node
// execution within a workflow is sequential, so this window only matters for
// concurrent tasks on one wallet.
type nonceManagerV07 struct {
	mu   sync.Mutex
	next map[nonceSlotV07]*big.Int
}

// nonceSlotV07 identifies one sequence counter: an account and one of its
// 192-bit nonce keys (entity + options), string-keyed for comparability.
type nonceSlotV07 struct {
	sender common.Address
	key    string
}

var globalNonceManagerV07 = &nonceManagerV07{next: make(map[nonceSlotV07]*big.Int)}

func slotV07(sender common.Address, entityID uint32, options uint8) (nonceSlotV07, error) {
	key, err := userop.NonceKeyMAv2(entityID, options)
	if err != nil {
		return nonceSlotV07{}, err
	}
	return nonceSlotV07{sender: sender, key: key.String()}, nil
}

// NextNonceV07Managed returns the nonce the next operation should use for
// this (sender, entity, options): the on-chain value, or the cached
// next-after-pending value when that is ahead.
func NextNonceV07Managed(ctx context.Context, client *rpc.Client, entryPoint, sender common.Address, entityID uint32, options uint8) (*big.Int, error) {
	slot, err := slotV07(sender, entityID, options)
	if err != nil {
		return nil, err
	}
	onChain, err := NextNonceV07(ctx, client, entryPoint, sender, entityID, options)
	if err != nil {
		return nil, err
	}

	m := globalNonceManagerV07
	m.mu.Lock()
	defer m.mu.Unlock()
	if cached, ok := m.next[slot]; ok && cached.Cmp(onChain) > 0 {
		return new(big.Int).Set(cached), nil
	}
	return onChain, nil
}

// NoteUserOpAccepted records that the bundler accepted an operation at
// usedNonce, so the next operation on the same key overlaps it in the
// mempool instead of colliding with it.
//
// The successor is built by re-encoding sequence+1 under the slot's own key
// rather than adding 1 to the full 256-bit nonce: the layout is
// [192-bit key][64-bit sequence], and a bare +1 at the sequence ceiling
// would carry into the key half and cache a nonce for a different validation
// entirely. Unrepresentable states drop the record — a missing cache entry
// costs a chain read; a corrupt one costs an AA25.
func NoteUserOpAccepted(sender common.Address, entityID uint32, options uint8, usedNonce *big.Int) {
	slot, err := slotV07(sender, entityID, options)
	if err != nil || usedNonce == nil {
		return // an unencodable slot was never cached; nothing to record
	}
	_, _, sequence, err := userop.DecodeNonceMAv2(usedNonce)
	if err != nil || sequence == ^uint64(0) {
		return
	}
	next, err := userop.EncodeNonceMAv2(entityID, options, sequence+1)
	if err != nil {
		return
	}
	m := globalNonceManagerV07
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next[slot] = next
}

// InvalidateNonce drops the cached counter so the next read is authoritative
// chain state. Call it when a send fails for any reason — a rejected
// operation was never pending, and a stale cache is worse than none.
func InvalidateNonce(sender common.Address, entityID uint32, options uint8) {
	slot, err := slotV07(sender, entityID, options)
	if err != nil {
		return
	}
	m := globalNonceManagerV07
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.next, slot)
}

// isInvalidNonceError recognises the EntryPoint's AA25 in a bundler error.
// (The v0.6 path also matches Voltaire's mempool-replacement phrasing; the
// v0.7 path only ever talks to Rundler, which surfaces AA25 verbatim.)
func isInvalidNonceError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "AA25")
}

// VerifyDeferredNonceUnused is the deferred path's replacement for nonce
// management: the owner's signature committed to sequence zero of the
// grant's key, so the only question is whether that nonce is still available.
// A consumed sequence means the grant was already applied (or a duplicate
// send is racing this one), and the answer is a clear error here rather than
// DeferredActionSignatureInvalid from the account with nothing pointing at
// the nonce.
func VerifyDeferredNonceUnused(liveNonce *big.Int) error {
	if liveNonce == nil {
		return fmt.Errorf("nil nonce")
	}
	_, _, sequence, err := userop.DecodeNonceMAv2(liveNonce)
	if err != nil {
		return err
	}
	if sequence != 0 {
		return fmt.Errorf("the grant's nonce key already has sequence %d on-chain — its deferred action was already applied (or a duplicate send is in flight); this grant cannot be replayed", sequence)
	}
	return nil
}
