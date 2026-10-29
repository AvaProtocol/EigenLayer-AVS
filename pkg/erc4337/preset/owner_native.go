package preset

import (
	"bytes"
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

// All rounds finish BEFORE a signature is requested. Self-funded ceilings only
// increase, so calldata-byte changes cannot make the reserve oscillate. A
// changing estimate that fails to converge is refused, never guessed. Sponsored
// calls use the full captured cap and are priced once, with final calldata.
func priceOwnerBatch(b OwnerBatch, op *userop.UserOperationV07, sponsored bool, price func() error) error {
	if !b.ReserveNativeGas || sponsored {
		packed, err := aa.PackExecuteBatchMAv2(b.Calls)
		if err != nil {
			return err
		}
		op.CallData = packed
		return price()
	}
	if err := price(); err != nil {
		return err
	}
	for round := 0; round < 8; round++ {
		calls, err := b.Resolve(op)
		if err != nil {
			return err
		}
		packed, err := aa.PackExecuteBatchMAv2(calls)
		if err != nil {
			return err
		}
		if bytes.Equal(packed, op.CallData) {
			return nil
		}
		previous := ownerPricingCeilings(op)
		op.CallData = packed
		op.CallGasLimit, op.VerificationGasLimit, op.PreVerificationGas = new(big.Int), new(big.Int), new(big.Int)
		if err := price(); err != nil {
			return err
		}
		if op.Paymaster != nil {
			return fmt.Errorf("owner gas funding changed while pricing")
		}
		current := ownerPricingCeilings(op)
		for i := range current {
			if current[i].Cmp(previous[i]) < 0 {
				current[i] = previous[i]
			}
		}
		op.CallGasLimit, op.VerificationGasLimit, op.PreVerificationGas = current[0], current[1], current[2]
		op.MaxFeePerGas, op.MaxPriorityFeePerGas = current[3], current[4]
	}
	return fmt.Errorf("native fee reserve did not converge; prepare again")
}

// Own both snapshots: an estimator may reuse or mutate its big.Int pointers.
func ownerPricingCeilings(op *userop.UserOperationV07) [5]*big.Int {
	return [5]*big.Int{
		new(big.Int).Set(op.CallGasLimit), new(big.Int).Set(op.VerificationGasLimit),
		new(big.Int).Set(op.PreVerificationGas), new(big.Int).Set(op.MaxFeePerGas),
		new(big.Int).Set(op.MaxPriorityFeePerGas),
	}
}
