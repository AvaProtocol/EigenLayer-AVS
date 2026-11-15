package preset

import (
	"bytes"
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
)

// Error codes for a reserved native transfer that cannot pay its own fee.
// Each names the term of nativeReserveValue that came out at zero or less, so
// the client can say whether the balance or the permission is short.
const (
	NativeBalanceBelowFeeCode = "NATIVE_BALANCE_BELOW_FEE"
	NativeCapBelowFeeCode     = "NATIVE_CAP_BELOW_FEE"
)

// Which term of nativeReserveValue set a reserved transfer's value.
const (
	NativeReserveLimitedByBalance    = "balance"
	NativeReserveLimitedByPermission = "permission"
)

// Ceilings only rise between rounds, so the value only falls. A loop that is
// still moving after this many rounds is refused, never guessed.
const nativeReserveMaxRounds = 8

// NativeReserve asks a session send to size a terminal native transfer from
// the operation it prices, instead of sending a value fixed beforehand. It is
// how a self-funded MAX sends what the balance and the permission allow after
// the operation's own maximum fee.
type NativeReserve struct {
	Recipient common.Address
	// Balance is B: the wallet's native balance, read for MAX.
	Balance *big.Int
	// GrantedLimit is the native limit the grant installs, or nil when the
	// grant has no native limit. An installed grant has already spent part of
	// it, so the send reads what is left on chain; only the operation that
	// carries the install starts from this value.
	GrantedLimit *big.Int
}

func (r *NativeReserve) validate() error {
	if r == nil {
		return fmt.Errorf("missing native reserve")
	}
	if r.Recipient == (common.Address{}) {
		return fmt.Errorf("native reserve has no recipient")
	}
	if r.Balance == nil || r.Balance.Sign() <= 0 || r.Balance.BitLen() > 256 {
		return fmt.Errorf("native reserve needs a positive balance")
	}
	if r.GrantedLimit != nil && (r.GrantedLimit.Sign() < 0 || r.GrantedLimit.BitLen() > 256) {
		return fmt.Errorf("invalid granted native limit")
	}
	return nil
}

// NativeReserveResult is what a reserved transfer sent. All amounts are wei.
type NativeReserveResult struct {
	// Value is what the recipient receives.
	Value *big.Int
	// MaxGasCost is G, the signed maximum gas cost.
	MaxGasCost *big.Int
	// GasReserve is what the balance keeps for the prefund: max(0, G − D).
	GasReserve *big.Int
	// LimitedBy is NativeReserveLimitedByBalance or
	// NativeReserveLimitedByPermission.
	LimitedBy string
	// Left is the ETH the permission leaves in the wallet:
	// B − GasReserve − Value. Zero when the balance set the value.
	Left *big.Int
}

// NativeReserveError refuses a reserved transfer whose value would be zero or
// less. Code is NativeBalanceBelowFeeCode or NativeCapBelowFeeCode.
type NativeReserveError struct {
	Code       string
	Balance    *big.Int
	GasReserve *big.Int
	// Remaining is C, or nil when the grant has no native limit.
	Remaining  *big.Int
	MaxGasCost *big.Int
}

func (e *NativeReserveError) Error() string {
	if e.Code == NativeCapBelowFeeCode {
		return fmt.Sprintf("%s: the permission's remaining native limit (%s wei) does not cover the maximum network fee (%s wei)",
			e.Code, e.Remaining, e.MaxGasCost)
	}
	return fmt.Sprintf("%s: the native balance (%s wei) does not cover the part of the maximum network fee the EntryPoint deposit does not (%s wei)",
		e.Code, e.Balance, e.GasReserve)
}

// nativeReserveValue sizes a terminal native transfer:
//
//	value = min(B − max(0, G − D), C − G)
//
// B is the balance, G the signed maximum gas cost, D the EntryPoint deposit
// and C the native permission's remaining limit (nil: no native limit).
//
//   - Balance: a v0.7 account pays only the prefund its deposit does not
//     cover, so the balance keeps max(0, G − D).
//   - Permission: NativeTokenLimitModule debits the whole G at validation,
//     whatever pays it, and the value at execution, so the limit must cover
//     value + G.
//
// When the two terms are equal the balance sets the value and nothing is left.
func nativeReserveValue(balance, maxGasCost, deposit, remaining *big.Int) (*NativeReserveResult, error) {
	gasReserve := new(big.Int).Sub(maxGasCost, deposit)
	if gasReserve.Sign() < 0 {
		gasReserve.SetInt64(0)
	}
	byBalance := new(big.Int).Sub(balance, gasReserve)
	value, limitedBy := byBalance, NativeReserveLimitedByBalance
	if remaining != nil {
		if byPermission := new(big.Int).Sub(remaining, maxGasCost); byPermission.Cmp(byBalance) < 0 {
			value, limitedBy = byPermission, NativeReserveLimitedByPermission
		}
	}
	if value.Sign() <= 0 {
		code := NativeBalanceBelowFeeCode
		if limitedBy == NativeReserveLimitedByPermission {
			code = NativeCapBelowFeeCode
		}
		return nil, &NativeReserveError{
			Code: code, Balance: new(big.Int).Set(balance), GasReserve: gasReserve,
			Remaining: copyBig(remaining), MaxGasCost: new(big.Int).Set(maxGasCost),
		}
	}
	return &NativeReserveResult{
		Value: value, MaxGasCost: new(big.Int).Set(maxGasCost), GasReserve: gasReserve,
		LimitedBy: limitedBy, Left: new(big.Int).Sub(byBalance, value),
	}, nil
}

// convergeNativeReserve prices op, then rebuilds its calldata from its own
// signed gas fields until the calldata stops changing. Both the owner batch
// and the session send size their terminal native transfer through it, so
// there is one reserve loop.
//
// All rounds finish BEFORE anything is signed. Ceilings only rise, so
// calldata-byte changes cannot make the reserve oscillate. A changing estimate
// that fails to converge is refused, never guessed.
func convergeNativeReserve(op *userop.UserOperationV07, build func(*userop.UserOperationV07) ([]byte, error), price func() error) error {
	if err := price(); err != nil {
		return err
	}
	if op.Paymaster != nil {
		return fmt.Errorf("a native reserve is for a self-funded operation, but the operation was sponsored")
	}
	for round := 0; round < nativeReserveMaxRounds; round++ {
		packed, err := build(op)
		if err != nil {
			return err
		}
		if bytes.Equal(packed, op.CallData) {
			return nil
		}
		previous := pricingCeilings(op)
		op.CallData = packed
		op.CallGasLimit, op.VerificationGasLimit, op.PreVerificationGas = new(big.Int), new(big.Int), new(big.Int)
		if err := price(); err != nil {
			return err
		}
		if op.Paymaster != nil {
			return fmt.Errorf("gas funding changed while pricing")
		}
		current := pricingCeilings(op)
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
func pricingCeilings(op *userop.UserOperationV07) [5]*big.Int {
	return [5]*big.Int{
		new(big.Int).Set(op.CallGasLimit), new(big.Int).Set(op.VerificationGasLimit),
		new(big.Int).Set(op.PreVerificationGas), new(big.Int).Set(op.MaxFeePerGas),
		new(big.Int).Set(op.MaxPriorityFeePerGas),
	}
}

// priceOnceThenEstimate prices the first round in full. Later rounds only
// re-estimate the final calldata with that first fee quote, instead of
// chasing a new base fee on every round.
func priceOnceThenEstimate(ctx context.Context, chain *ethclient.Client, bundler *rpc.Client, op *userop.UserOperationV07, sw *config.SmartWalletConfig, l logger.Logger) func() error {
	priced := false
	return func() error {
		if !priced {
			if err := priceOperationV07(ctx, chain, bundler, op, EntryPointV07(), sw, logger.EnsureLogger(l)); err != nil {
				return err
			}
			priced = true
			return nil
		}
		estimate, err := EstimateUserOpGasV07(ctx, bundler, op, EntryPointV07())
		if err != nil {
			return fmt.Errorf("estimating gas: %w", err)
		}
		op.CallGasLimit, op.VerificationGasLimit, op.PreVerificationGas = estimate.CallGasLimit, estimate.VerificationGasLimit, estimate.PreVerificationGas
		return nil
	}
}

// readNativeReserveInputs reads D and C once, before pricing.
//
//   - A failed deposit read takes D = 0, which reserves more, never less.
//   - A failed limit read refuses: a guessed C could sign an operation the
//     module reverts, or leave ETH behind for no reason.
//   - An operation that carries the grant's install starts from the granted
//     limit: on chain the limit does not exist until this operation's
//     validation installs it.
func readNativeReserveInputs(ctx context.Context, chain aa.ContractCaller, reserve *NativeReserve, auth *SessionAuthorization, sender common.Address) (deposit, remaining *big.Int, err error) {
	deposit, depositErr := entryPointDeposit(ctx, chain, EntryPointV07(), sender)
	if depositErr != nil {
		deposit = big.NewInt(0)
	}
	if reserve.GrantedLimit == nil {
		return deposit, nil, nil
	}
	if auth.Deferred() {
		return deposit, new(big.Int).Set(reserve.GrantedLimit), nil
	}
	if auth == nil {
		return nil, nil, fmt.Errorf("a native limit needs the session entity that spends it")
	}
	remaining, err = aa.ReadNativeSpendLimit(ctx, chain, auth.EntityID, sender)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the remaining native limit: %w", err)
	}
	return deposit, remaining, nil
}

// priceSessionNativeReserve prices a session operation whose only call is a
// native transfer to reserve.Recipient, sized by nativeReserveValue from the
// operation's own signed gas fields. build packs a value into the operation's
// calldata with the send's own pack-and-wrap, so a grant with execution hooks
// gets wrapped calldata on every round.
func priceSessionNativeReserve(op *userop.UserOperationV07, reserve *NativeReserve, deposit, remaining *big.Int, build func(value *big.Int) ([]byte, error), price func() error) (*NativeReserveResult, error) {
	// Estimate with a 1 wei value: the final value depends on the price.
	estimation, err := build(big.NewInt(1))
	if err != nil {
		return nil, err
	}
	op.CallData = estimation
	var sized *NativeReserveResult
	err = convergeNativeReserve(op, func(priced *userop.UserOperationV07) ([]byte, error) {
		result, sizeErr := nativeReserveValue(reserve.Balance, MaxOwnerGasCost(priced), deposit, remaining)
		if sizeErr != nil {
			return nil, sizeErr
		}
		sized = result
		return build(result.Value)
	}, price)
	if err != nil {
		return nil, err
	}
	if err = checkNativeReserveCovered(op, sized, reserve.Balance, deposit, remaining); err != nil {
		return nil, err
	}
	return sized, nil
}

// checkNativeReserveCovered asserts, against the operation about to be
// signed, what nativeReserveValue guarantees by construction: the balance
// covers the value plus the prefund the deposit does not, and the remaining
// limit covers the value plus the whole maximum gas cost.
func checkNativeReserveCovered(op *userop.UserOperationV07, sized *NativeReserveResult, balance, deposit, remaining *big.Int) error {
	maxGasCost := MaxOwnerGasCost(op)
	if sized == nil || sized.MaxGasCost.Cmp(maxGasCost) != 0 {
		return fmt.Errorf("native reserve was sized for a different maximum gas cost")
	}
	gasReserve := new(big.Int).Sub(maxGasCost, deposit)
	if gasReserve.Sign() < 0 {
		gasReserve.SetInt64(0)
	}
	if new(big.Int).Add(sized.Value, gasReserve).Cmp(balance) > 0 {
		return &NativeReserveError{Code: NativeBalanceBelowFeeCode, Balance: new(big.Int).Set(balance), GasReserve: gasReserve, Remaining: copyBig(remaining), MaxGasCost: maxGasCost}
	}
	if remaining != nil && new(big.Int).Add(sized.Value, maxGasCost).Cmp(remaining) > 0 {
		return &NativeReserveError{Code: NativeCapBelowFeeCode, Balance: new(big.Int).Set(balance), GasReserve: gasReserve, Remaining: new(big.Int).Set(remaining), MaxGasCost: maxGasCost}
	}
	return nil
}

func copyBig(value *big.Int) *big.Int {
	if value == nil {
		return nil
	}
	return new(big.Int).Set(value)
}
