package aa

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
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

// ContractCaller is the read surface these checks need — satisfied by
// *ethclient.Client and by anything else that can make an eth_call.
type ContractCaller interface {
	CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}
