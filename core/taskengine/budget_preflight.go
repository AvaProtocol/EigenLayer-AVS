package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

var budgetNativeToken = common.HexToAddress("0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE")

func budgetRPCRefusal(err error) *StructuredError {
	return NewStructuredError(avsproto.ErrorCode_RPC_NODE_ERROR, fmt.Sprintf("%s: %v", BudgetReadUnavailableCode, err))
}

type BudgetSpendCheck struct {
	Policy    *model.SessionPolicy
	Calls     []PlannedCall
	Caller    aa.ContractCaller
	FeeReader CodeAndFeeReader
	Native    NativeIntent
	Now       time.Time
}

// CheckBudgetSpend checks the actual calls of one UserOperation, never a
// workflow's future run count. approve and transfer both consume their full
// amounts. Unknown/read-failed remainders never become stored-limit fallbacks.
func CheckBudgetSpend(ctx context.Context, in BudgetSpendCheck) error {
	p := in.Policy
	if !isWalletBudget(p) {
		return nil
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	if p.Expired(in.Now) {
		return NewBudgetEndedError(p.ValidUntil)
	}
	if p.Status != model.SessionPolicyActive || !p.Grant.Applied() || p.Runner == nil {
		return budgetRPCRefusal(fmt.Errorf("the budget is not active"))
	}
	readCtx, cancel := context.WithTimeout(ctx, occupancyProbeTimeout)
	defer cancel()
	capped := map[common.Address]bool{}
	for _, cap := range policyCaps(p) {
		if cap.Token == nil {
			return budgetRPCRefusal(fmt.Errorf("a stored cap has no token"))
		}
		capped[*cap.Token] = true
	}
	spend := map[common.Address]*big.Int{}
	native := big.NewInt(0)
	for _, call := range in.Calls {
		if call.Value != nil {
			if call.Value.Sign() < 0 {
				return NewInvalidRequestError("negative native value")
			}
			native.Add(native, call.Value)
		}
		if !capped[call.Target] {
			continue
		}
		selector := SelectorFromCalldata(call.Calldata)
		if call.Selector != "" {
			if selector != normalizeSelector(call.Selector) {
				return NewInvalidRequestError("the planned selector differs from the token calldata")
			}
		}
		if selector != selectorTransfer && selector != selectorApprove {
			continue
		}
		actualSelector := SelectorFromCalldata(call.Calldata)
		if len(call.Calldata) != 68 || actualSelector != selector {
			return NewInvalidRequestError("cannot decode the capped token's transfer/approve amount")
		}
		amount := new(big.Int).SetBytes(call.Calldata[36:68])
		if spend[call.Target] == nil {
			spend[call.Target] = big.NewInt(0)
		}
		spend[call.Target].Add(spend[call.Target], amount)
	}
	tokens := make([]model.ERC20SpendCap, 0, len(spend))
	for token := range spend {
		copyToken := token
		tokens = append(tokens, model.ERC20SpendCap{Token: &copyToken})
	}
	sortSpendCaps(tokens)
	for _, token := range tokens {
		left, err := aa.ReadERC20SpendLimit(readCtx, in.Caller, p.EntityID, *token.Token, *p.Runner)
		if err != nil {
			return budgetRPCRefusal(err)
		}
		if spend[*token.Token].Cmp(left) > 0 {
			return NewBudgetUsedUpError(*token.Token, spend[*token.Token], left)
		}
	}
	if p.NativeSpendCap == nil {
		return nil
	}
	left, err := aa.ReadNativeSpendLimit(readCtx, in.Caller, p.EntityID, *p.Runner)
	if err != nil {
		return budgetRPCRefusal(err)
	}
	if !in.Native.Sponsored {
		gas := in.Native.EstimatedGas
		if gas == nil {
			if in.FeeReader == nil {
				return budgetRPCRefusal(fmt.Errorf("no native fee reader"))
			}
			fee, err := in.FeeReader.MaxFeePerGas(readCtx)
			if err != nil {
				return budgetRPCRefusal(err)
			}
			if fee == nil || fee.Sign() <= 0 {
				return budgetRPCRefusal(fmt.Errorf("invalid native fee quote"))
			}
			gas = new(big.Int).Mul(big.NewInt(nativePreflightGasUnitsFor(in.Native.Kind, p)), fee)
		}
		if gas.Sign() < 0 {
			return budgetRPCRefusal(fmt.Errorf("invalid estimated gas"))
		}
		native.Add(native, gas)
	}
	if native.Cmp(left) > 0 {
		return NewBudgetUsedUpError(budgetNativeToken, native, left)
	}
	return nil
}
