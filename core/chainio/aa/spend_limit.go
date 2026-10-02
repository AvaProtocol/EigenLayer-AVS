package aa

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Remaining spend on an installed session entity.
//
// AllowlistModule meters ERC-20 with erc20SpendLimits(uint32 entityId,
// address token, address account). account is the runner smart wallet.
// NativeTokenLimitModule meters ETH with limits(uint256 entityId, address
// account). The two getters are not interchangeable: calling limits on the
// Allowlist module does not read the token cap.
//
// A 32-byte zero is "nothing left". A short return or an RPC error is not
// a zero — the caller must fail closed rather than treat a failed read as
// spent.

// ReadERC20SpendLimit reads AllowlistModule.erc20SpendLimits(entity, token, account).
func ReadERC20SpendLimit(ctx context.Context, client ContractCaller, entity uint32, token, account common.Address) (*big.Int, error) {
	if client == nil {
		return nil, fmt.Errorf("no chain client")
	}
	data := crypto.Keccak256([]byte("erc20SpendLimits(uint32,address,address)"))[:4]
	data = append(data, abiWord(new(big.Int).SetUint64(uint64(entity)))...)
	data = append(data, abiWord(new(big.Int).SetBytes(token.Bytes()))...)
	data = append(data, abiWord(new(big.Int).SetBytes(account.Bytes()))...)

	module := AllowlistModuleAddress()
	out, err := client.CallContract(ctx, ethereum.CallMsg{To: &module, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("reading erc20SpendLimits(%d, %s, %s): %w", entity, token.Hex(), account.Hex(), err)
	}
	if len(out) < 32 {
		return nil, fmt.Errorf("erc20SpendLimits(%d, %s, %s) returned %d bytes, want 32", entity, token.Hex(), account.Hex(), len(out))
	}
	return new(big.Int).SetBytes(out[:32]), nil
}

// ReadNativeSpendLimit reads NativeTokenLimitModule.limits(uint256 entityId, address account).
func ReadNativeSpendLimit(ctx context.Context, client ContractCaller, entity uint32, account common.Address) (*big.Int, error) {
	if client == nil {
		return nil, fmt.Errorf("no chain client")
	}
	data := crypto.Keccak256([]byte("limits(uint256,address)"))[:4]
	data = append(data, abiWord(new(big.Int).SetUint64(uint64(entity)))...)
	data = append(data, abiWord(new(big.Int).SetBytes(account.Bytes()))...)

	module := NativeTokenLimitModuleAddress()
	out, err := client.CallContract(ctx, ethereum.CallMsg{To: &module, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("reading NativeTokenLimitModule.limits(%d, %s): %w", entity, account.Hex(), err)
	}
	if len(out) < 32 {
		return nil, fmt.Errorf("limits(%d, %s) returned %d bytes, want 32", entity, account.Hex(), len(out))
	}
	return new(big.Int).SetBytes(out[:32]), nil
}

func abiWord(v *big.Int) []byte {
	if v == nil {
		v = big.NewInt(0)
	}
	return common.LeftPadBytes(v.Bytes(), 32)
}
