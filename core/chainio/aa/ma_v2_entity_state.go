package aa

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

// Reading back what a validation entity actually holds.
//
// This exists because a mined transaction is not evidence that an uninstall
// did anything. The account catches a hook module's onUninstall revert, flags
// it only in the ValidationUninstalled event, and leaves the module state in
// place. Demonstrated on Sepolia while spiking #717: a replace batch mined at
// 601,275 gas with success=true and left the prior entity holding both its
// signer and its full 100-token spend cap.
//
// So anything that reports a grant revoked on chain has to read the chain.

// EntitySignerOnChain returns the SingleSignerValidationModule signer for
// (entity, account), or the zero address when the entity holds none.
//
// A read failure is returned rather than folded into the zero address: zero
// means "this entity is clear", and a caller acting on a transient RPC error
// would report a grant revoked that is still live.
func EntitySignerOnChain(ctx context.Context, client ContractCaller, account common.Address, entity uint32) (common.Address, error) {
	if client == nil {
		return common.Address{}, fmt.Errorf("no chain client")
	}
	data := crypto.Keccak256([]byte("signers(uint32,address)"))[:4]
	data = append(data, common.LeftPadBytes([]byte{
		byte(entity >> 24), byte(entity >> 16), byte(entity >> 8), byte(entity),
	}, 32)...)
	data = append(data, common.LeftPadBytes(account.Bytes(), 32)...)

	module := SingleSignerValidationModuleAddress()
	out, err := client.CallContract(ctx, ethereum.CallMsg{To: &module, Data: data}, nil)
	if err != nil {
		return common.Address{}, fmt.Errorf("reading signer of entity %d on %s: %w", entity, account.Hex(), err)
	}
	if len(out) < 32 {
		return common.Address{}, fmt.Errorf("signers(%d, %s) returned %d bytes, want at least 32",
			entity, account.Hex(), len(out))
	}
	return common.BytesToAddress(out[12:32]), nil
}

// EntryPointNonce returns the EntryPoint's current nonce for (account, key) —
// the full 256-bit value, whose low 64 bits are the sequence.
//
// This is the second half of "is this validation entity free", and the half
// that is easy to miss. Teardown clears every module: signer, allowlist, spend
// cap, time range all read zero afterwards, so an entity looks brand new. Its
// EntryPoint nonce sequence does NOT reset — nothing can reset it.
//
// That matters because a deferred action's digest commits to the FULL nonce of
// the operation that will carry it, and PrepareSessionGrant can only know that
// nonce in advance by assuming sequence 0. The assumption holds for an entity
// that has never been used and fails silently for one that has: the owner signs
// a nonce the EntryPoint already consumed, and the operation reverts as an
// opaque AA23 with no on-chain trace of the reason.
//
// So an entity id must never be reissued once used, however clean it reads.
// Observed on Sepolia while implementing #717: entity 1 read clear on all four
// modules with its deferred nonce sequence at 1, and every grant reallocating
// it AA23'd during validation.
func EntryPointNonce(ctx context.Context, client ContractCaller, account common.Address, key *big.Int) (*big.Int, error) {
	if client == nil {
		return nil, fmt.Errorf("no chain client")
	}
	if key == nil {
		return nil, fmt.Errorf("no nonce key")
	}
	data := crypto.Keccak256([]byte("getNonce(address,uint192)"))[:4]
	data = append(data, common.LeftPadBytes(account.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(key.Bytes(), 32)...)

	entryPoint := common.HexToAddress(config.EntryPointV07AddressHex)
	out, err := client.CallContract(ctx, ethereum.CallMsg{To: &entryPoint, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("reading nonce of %s at key %s: %w", account.Hex(), key, err)
	}
	if len(out) < 32 {
		return nil, fmt.Errorf("getNonce(%s, %s) returned %d bytes, want at least 32",
			account.Hex(), key, len(out))
	}
	return new(big.Int).SetBytes(out[:32]), nil
}

// EntityDeferredNonceSequence returns the SEQUENCE half of the deferred nonce
// for (account, entity) — the number that says whether this entity has ever
// carried a deferred action, and so whether it may be issued to a grant.
//
// Read the sequence, never the full nonce. getNonce returns key<<64 | sequence,
// and the key embeds the entity id, so the full value is non-zero for every
// entity above 0. Testing that against zero looks exactly like a usage check
// and is nothing of the kind — it reports every entity as spent.
func EntityDeferredNonceSequence(ctx context.Context, client ContractCaller, account common.Address, entity uint32) (uint64, error) {
	key, err := userop.NonceKeyMAv2(entity,
		userop.ValidationOptionGlobal|userop.ValidationOptionDeferredAction)
	if err != nil {
		return 0, err
	}
	nonce, err := EntryPointNonce(ctx, client, account, key)
	if err != nil {
		return 0, err
	}
	// Low 64 bits. Everything above them is the key.
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))
	return new(big.Int).And(nonce, mask).Uint64(), nil
}

// ContractCaller is the read surface these checks need — satisfied by
// *ethclient.Client and by anything else that can make an eth_call.
type ContractCaller interface {
	CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}
