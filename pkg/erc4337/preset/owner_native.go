package preset

import (
	"fmt"
	"math/big"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum/common"
)

// OwnerBatch is a resolved workflow, not an instruction to read balances again.
// ReserveNativeGas applies only to the last, empty-calldata native transfer.
// Its value is the captured cap; the priced operation determines the reserve.
type OwnerBatch struct {
	Calls            []aa.Call `json:"calls"`
	ReserveNativeGas bool      `json:"reserveNativeGas,omitempty"`
}

func (b OwnerBatch) validate() error {
	if len(b.Calls) == 0 || len(b.Calls) > 32 {
		return fmt.Errorf("owner batch requires 1 to 32 calls")
	}
	for _, call := range b.Calls {
		if call.Target == (common.Address{}) || call.Value == nil || call.Value.Sign() < 0 || call.Value.BitLen() > 256 {
			return fmt.Errorf("invalid owner call target or value")
		}
	}
	if b.ReserveNativeGas {
		last := b.Calls[len(b.Calls)-1]
		if len(last.Data) != 0 || last.Value.Sign() <= 0 {
			return fmt.Errorf("gas reserve requires a positive terminal native transfer cap")
		}
		// One balance cap cannot also cover another native-value call.
		for _, call := range b.Calls[:len(b.Calls)-1] {
			if call.Value.Sign() != 0 {
				return fmt.Errorf("gas reserve cannot be combined with another native-value call")
			}
		}
	}
	return nil
}

func (b OwnerBatch) estimationCalls() ([]aa.Call, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	calls := append([]aa.Call(nil), b.Calls...)
	if b.ReserveNativeGas {
		calls[len(calls)-1].Value = big.NewInt(1)
	}
	return calls, nil
}

// Resolve fixes the native amount using the signed fee fields only. It is also
// used when replaying a stored preparation: no pricing or balance RPC is needed.
func (b OwnerBatch) Resolve(op *userop.UserOperationV07) ([]aa.Call, error) {
	if op == nil {
		return nil, fmt.Errorf("missing prepared owner operation")
	}
	if err := b.validate(); err != nil {
		return nil, err
	}
	calls := append([]aa.Call(nil), b.Calls...)
	if b.ReserveNativeGas {
		reserve := new(big.Int)
		if op.Paymaster == nil {
			reserve = MaxOwnerGasCost(op)
		}
		amount := new(big.Int).Sub(calls[len(calls)-1].Value, reserve)
		if amount.Sign() <= 0 {
			return nil, fmt.Errorf("native balance does not cover the maximum network fee; deselect ETH or add funds")
		}
		calls[len(calls)-1].Value = amount
	}
	return calls, nil
}

// Sponsored calls use the full captured cap and are priced once, with final
// calldata. Self-funded calls size the terminal transfer through the shared
// reserve loop (convergeNativeReserve), from Resolve's rule: the cap minus the
// whole maximum gas cost.
func priceOwnerBatch(b OwnerBatch, op *userop.UserOperationV07, sponsored bool, price func() error) error {
	if !b.ReserveNativeGas || sponsored {
		packed, err := aa.PackExecuteBatchMAv2(b.Calls)
		if err != nil {
			return err
		}
		op.CallData = packed
		return price()
	}
	return convergeNativeReserve(op, func(priced *userop.UserOperationV07) ([]byte, error) {
		calls, err := b.Resolve(priced)
		if err != nil {
			return nil, err
		}
		return aa.PackExecuteBatchMAv2(calls)
	}, price)
}
