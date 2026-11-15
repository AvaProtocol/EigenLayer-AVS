package preset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

var reserveRecipient = common.HexToAddress("0x3333333333333333333333333333333333333333")

// big.Int zeros differ internally (nil or empty words), so compare values.
func requireWei(t *testing.T, want int64, got *big.Int) {
	t.Helper()
	require.NotNil(t, got)
	require.Equal(t, big.NewInt(want).String(), got.String())
}

func requireNativeReserveError(t *testing.T, err error, code string) {
	t.Helper()
	var refusal *NativeReserveError
	require.True(t, errors.As(err, &refusal), "want a NativeReserveError, got %v", err)
	require.Equal(t, code, refusal.Code)
	require.ErrorContains(t, err, code+":")
}

// The plan's table: B = 100 and G = 10, with the deposit at zero, partial and
// full coverage, each against the remaining limit.
func TestNativeReserveValueAgainstDepositAndPermission(t *testing.T) {
	type expected struct {
		value, left int64
		limitedBy   string
		code        string
	}
	balance, maxGasCost := big.NewInt(100), big.NewInt(10)
	cases := []struct {
		name      string
		deposit   int64
		remaining *big.Int
		want      expected
	}{
		{"no deposit, no native limit", 0, nil, expected{value: 90, limitedBy: NativeReserveLimitedByBalance}},
		{"no deposit, limit above B + D", 0, big.NewInt(150), expected{value: 90, limitedBy: NativeReserveLimitedByBalance}},
		{"no deposit, limit equals B", 0, big.NewInt(100), expected{value: 90, limitedBy: NativeReserveLimitedByBalance}},
		{"no deposit, limit equals G", 0, big.NewInt(10), expected{code: NativeCapBelowFeeCode}},
		{"no deposit, limit below G", 0, big.NewInt(5), expected{code: NativeCapBelowFeeCode}},
		{"partial deposit, limit B + D", 5, big.NewInt(105), expected{value: 95, limitedBy: NativeReserveLimitedByBalance}},
		{"partial deposit, limit equals B", 5, big.NewInt(100), expected{value: 90, left: 5, limitedBy: NativeReserveLimitedByPermission}},
		{"partial deposit, limit equals G", 5, big.NewInt(10), expected{code: NativeCapBelowFeeCode}},
		{"full deposit, limit B + D", 10, big.NewInt(110), expected{value: 100, limitedBy: NativeReserveLimitedByBalance}},
		{"full deposit, limit equals B", 10, big.NewInt(100), expected{value: 90, left: 10, limitedBy: NativeReserveLimitedByPermission}},
		{"full deposit, limit equals G", 10, big.NewInt(10), expected{code: NativeCapBelowFeeCode}},
		{"deposit above G keeps no reserve", 15, nil, expected{value: 100, limitedBy: NativeReserveLimitedByBalance}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nativeReserveValue(balance, maxGasCost, big.NewInt(tc.deposit), tc.remaining)
			if tc.want.code != "" {
				requireNativeReserveError(t, err, tc.want.code)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			requireWei(t, tc.want.value, got.Value)
			requireWei(t, tc.want.left, got.Left)
			require.Equal(t, tc.want.limitedBy, got.LimitedBy)
			requireWei(t, maxGasCost.Int64(), got.MaxGasCost)
			requireWei(t, max(10-tc.deposit, 0), got.GasReserve)
			// What the balance keeps, plus what was sent and what was left, is B.
			requireWei(t, balance.Int64(), new(big.Int).Add(new(big.Int).Add(got.Value, got.Left), got.GasReserve))
			if tc.remaining != nil {
				require.LessOrEqual(t, new(big.Int).Add(got.Value, maxGasCost).Cmp(tc.remaining), 0, "the limit covers value + G")
			}
		})
	}
}

func TestNativeReserveValueRefusesBalanceAtOrBelowFee(t *testing.T) {
	for _, balance := range []int64{10, 5} {
		_, err := nativeReserveValue(big.NewInt(balance), big.NewInt(10), big.NewInt(0), nil)
		requireNativeReserveError(t, err, NativeBalanceBelowFeeCode)
	}
	// Both short by the same amount: the balance term is reported.
	_, err := nativeReserveValue(big.NewInt(10), big.NewInt(10), big.NewInt(0), big.NewInt(10))
	requireNativeReserveError(t, err, NativeBalanceBelowFeeCode)
	// The permission term is the smaller one.
	_, err = nativeReserveValue(big.NewInt(10), big.NewInt(10), big.NewInt(0), big.NewInt(5))
	requireNativeReserveError(t, err, NativeCapBelowFeeCode)
}

// wrappedReserveBuild is the send's own pack-and-wrap for a grant whose
// execution hooks need user-op context.
func wrappedReserveBuild(t *testing.T) func(*big.Int) ([]byte, error) {
	t.Helper()
	auth := &SessionAuthorization{WrapExecuteUserOp: true}
	return func(value *big.Int) ([]byte, error) {
		packed, err := packNativeTransfer(reserveRecipient, value)
		if err != nil {
			return nil, err
		}
		return sessionCallData(auth, packed)
	}
}

// reservePricer stands in for the bundler. The first round's preVerification
// is 30; the final value's extra nonzero calldata bytes need 50 after that,
// while the verification estimate drops to 40, which must not lower the
// ceiling.
func reservePricer(op *userop.UserOperationV07, unstable bool, priced *[][]byte) func() error {
	return func() error {
		*priced = append(*priced, bytes.Clone(op.CallData))
		round := int64(len(*priced))
		pre, verification := int64(30), int64(50)
		if round > 1 {
			pre, verification = 50, 40
		}
		if unstable {
			pre += round
		}
		op.CallGasLimit = big.NewInt(100)
		op.VerificationGasLimit = big.NewInt(verification)
		op.PreVerificationGas = big.NewInt(pre)
		op.MaxFeePerGas = big.NewInt(2)
		op.MaxPriorityFeePerGas = big.NewInt(1)
		return nil
	}
}

func TestSessionNativeReserveConvergesAndWrapsEveryRound(t *testing.T) {
	balance := big.NewInt(1_000_000_000)
	op := &userop.UserOperationV07{}
	var priced [][]byte
	reserve := &NativeReserve{Recipient: reserveRecipient, Balance: balance}
	sized, err := priceSessionNativeReserve(op, reserve, big.NewInt(0), nil, wrappedReserveBuild(t), reservePricer(op, false, &priced))
	require.NoError(t, err)
	require.Len(t, priced, 3, "1 wei, then the first value, then the final value")
	for _, calldata := range priced {
		require.NotEqual(t, calldata, aa.UnwrapExecuteUserOp(calldata), "every round prices wrapped calldata")
	}
	_, values, _, err := aa.UnpackExecuteCalldata(priced[0])
	require.NoError(t, err)
	require.Equal(t, big.NewInt(1), values[0], "estimate with the value at 1 wei")

	// Ceilings only rise: verification stays at the first round's 50.
	require.Equal(t, "50", op.VerificationGasLimit.String())
	require.Equal(t, "50", op.PreVerificationGas.String())
	maxGasCost := MaxOwnerGasCost(op)
	require.Equal(t, "400", maxGasCost.String())
	require.Equal(t, new(big.Int).Sub(balance, maxGasCost), sized.Value)
	require.Equal(t, NativeReserveLimitedByBalance, sized.LimitedBy)

	expected, err := wrappedReserveBuild(t)(sized.Value)
	require.NoError(t, err)
	require.Equal(t, expected, op.CallData, "the signed calldata carries the reported value")
	targets, values, data, err := aa.UnpackExecuteCalldata(op.CallData)
	require.NoError(t, err)
	require.Equal(t, []common.Address{reserveRecipient}, targets)
	require.Equal(t, sized.Value, values[0])
	require.Empty(t, data[0])
}

func TestSessionNativeReserveRefusesWhenItDoesNotConverge(t *testing.T) {
	op := &userop.UserOperationV07{}
	var priced [][]byte
	reserve := &NativeReserve{Recipient: reserveRecipient, Balance: big.NewInt(1_000_000_000)}
	_, err := priceSessionNativeReserve(op, reserve, big.NewInt(0), nil, wrappedReserveBuild(t), reservePricer(op, true, &priced))
	require.ErrorContains(t, err, "did not converge")
}

func TestSessionNativeReserveSizedByPermissionAndDeposit(t *testing.T) {
	op := &userop.UserOperationV07{}
	var priced [][]byte
	balance := big.NewInt(1_000_000)
	// G settles at 400. The deposit covers 150 of it, so the balance term is
	// 1,000,000 − 250; the limit leaves 10,000 − 400 to send.
	deposit, remaining := big.NewInt(150), big.NewInt(10_000)
	reserve := &NativeReserve{Recipient: reserveRecipient, Balance: balance, GrantedLimit: big.NewInt(50_000)}
	sized, err := priceSessionNativeReserve(op, reserve, deposit, remaining, wrappedReserveBuild(t), reservePricer(op, false, &priced))
	require.NoError(t, err)
	require.Equal(t, "9600", sized.Value.String())
	require.Equal(t, "250", sized.GasReserve.String())
	require.Equal(t, NativeReserveLimitedByPermission, sized.LimitedBy)
	require.Equal(t, "990150", sized.Left.String())
	require.NoError(t, checkNativeReserveCovered(op, sized, balance, deposit, remaining))
}

func TestSessionNativeReserveRefusesShortBalanceOrPermission(t *testing.T) {
	for _, tc := range []struct {
		balance, remaining *big.Int
		code               string
	}{
		{big.NewInt(300), nil, NativeBalanceBelowFeeCode},
		{big.NewInt(1_000_000), big.NewInt(360), NativeCapBelowFeeCode},
	} {
		t.Run(tc.code, func(t *testing.T) {
			op := &userop.UserOperationV07{}
			var priced [][]byte
			reserve := &NativeReserve{Recipient: reserveRecipient, Balance: tc.balance}
			_, err := priceSessionNativeReserve(op, reserve, big.NewInt(0), tc.remaining, wrappedReserveBuild(t), reservePricer(op, false, &priced))
			requireNativeReserveError(t, err, tc.code)
			require.Len(t, priced, 1, "refused on the first priced round, before anything is signed")
		})
	}
}

func TestNativeReserveCoverageCheckBeforeSigning(t *testing.T) {
	op := &userop.UserOperationV07{
		CallGasLimit: big.NewInt(100), VerificationGasLimit: big.NewInt(50), PreVerificationGas: big.NewInt(50),
		MaxFeePerGas: big.NewInt(2), MaxPriorityFeePerGas: big.NewInt(1),
	}
	balance, remaining := big.NewInt(1000), big.NewInt(1000)
	sized, err := nativeReserveValue(balance, MaxOwnerGasCost(op), big.NewInt(0), remaining)
	require.NoError(t, err)
	require.NoError(t, checkNativeReserveCovered(op, sized, balance, big.NewInt(0), remaining))

	// A value sized for other gas fields is refused.
	op.PreVerificationGas = big.NewInt(60)
	require.ErrorContains(t, checkNativeReserveCovered(op, sized, balance, big.NewInt(0), remaining), "different maximum gas cost")
	op.PreVerificationGas = big.NewInt(50)

	tooMuch := *sized
	tooMuch.Value = new(big.Int).Add(sized.Value, big.NewInt(1))
	requireNativeReserveError(t, checkNativeReserveCovered(op, &tooMuch, balance, big.NewInt(0), nil), NativeBalanceBelowFeeCode)
	requireNativeReserveError(t, checkNativeReserveCovered(op, &tooMuch, big.NewInt(2000), big.NewInt(0), remaining), NativeCapBelowFeeCode)
}

// reserveReadCaller answers the two reads a reserve makes: the EntryPoint
// deposit and NativeTokenLimitModule.limits.
type reserveReadCaller struct {
	deposit, limit       []byte
	depositErr, limitErr error
	limitReads           int
	limitEntity          *big.Int
}

func (c *reserveReadCaller) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	switch {
	case *call.To == EntryPointV07() && bytes.Equal(call.Data[:4], crypto.Keccak256([]byte("balanceOf(address)"))[:4]):
		return c.deposit, c.depositErr
	case *call.To == aa.NativeTokenLimitModuleAddress() && bytes.Equal(call.Data[:4], crypto.Keccak256([]byte("limits(uint256,address)"))[:4]):
		c.limitReads++
		c.limitEntity = new(big.Int).SetBytes(call.Data[4:36])
		return c.limit, c.limitErr
	}
	return nil, fmt.Errorf("unexpected call to %s", call.To.Hex())
}

func word(value int64) []byte {
	return common.LeftPadBytes(big.NewInt(value).Bytes(), 32)
}

func TestNativeReserveReadsDepositAndRemainingLimit(t *testing.T) {
	sender := common.HexToAddress("0x2222222222222222222222222222222222222222")
	installed := &SessionAuthorization{EntityID: 7}
	installing := &SessionAuthorization{EntityID: 7, DeferredData: []byte{1}, OwnerSignature: []byte{2}}
	granted := big.NewInt(5000)
	ctx := context.Background()

	t.Run("installed grant reads what is left", func(t *testing.T) {
		caller := &reserveReadCaller{deposit: word(40), limit: word(1200)}
		deposit, remaining, err := readNativeReserveInputs(ctx, caller, &NativeReserve{GrantedLimit: granted}, installed, sender)
		require.NoError(t, err)
		require.Equal(t, "40", deposit.String())
		require.Equal(t, "1200", remaining.String(), "the remaining limit, not the granted cap")
		require.Equal(t, "7", caller.limitEntity.String())
	})
	t.Run("a failed deposit read takes zero", func(t *testing.T) {
		caller := &reserveReadCaller{depositErr: fmt.Errorf("rpc down"), limit: word(1200)}
		deposit, remaining, err := readNativeReserveInputs(ctx, caller, &NativeReserve{GrantedLimit: granted}, installed, sender)
		require.NoError(t, err)
		require.Equal(t, "0", deposit.String())
		require.Equal(t, "1200", remaining.String())
	})
	t.Run("a failed limit read refuses", func(t *testing.T) {
		caller := &reserveReadCaller{deposit: word(40), limitErr: fmt.Errorf("rpc down")}
		_, _, err := readNativeReserveInputs(ctx, caller, &NativeReserve{GrantedLimit: granted}, installed, sender)
		require.ErrorContains(t, err, "remaining native limit")
	})
	t.Run("a short limit read refuses", func(t *testing.T) {
		caller := &reserveReadCaller{deposit: word(40), limit: []byte{1}}
		_, _, err := readNativeReserveInputs(ctx, caller, &NativeReserve{GrantedLimit: granted}, installed, sender)
		require.ErrorContains(t, err, "remaining native limit")
	})
	t.Run("no native limit reads none", func(t *testing.T) {
		caller := &reserveReadCaller{deposit: word(40)}
		_, remaining, err := readNativeReserveInputs(ctx, caller, &NativeReserve{}, installed, sender)
		require.NoError(t, err)
		require.Nil(t, remaining)
		require.Zero(t, caller.limitReads)
	})
	t.Run("the install starts from the granted limit", func(t *testing.T) {
		caller := &reserveReadCaller{deposit: word(40), limit: word(0)}
		_, remaining, err := readNativeReserveInputs(ctx, caller, &NativeReserve{GrantedLimit: granted}, installing, sender)
		require.NoError(t, err)
		require.Equal(t, granted, remaining)
		require.NotSame(t, granted, remaining)
		require.Zero(t, caller.limitReads, "the limit does not exist on chain before the install")
	})
}

func TestSendNativeReserveRefusesBeforeAnyRPC(t *testing.T) {
	selfFunded := &config.SmartWalletConfig{ChainID: 11155111, DisableGasSponsorship: true}
	sponsored := &config.SmartWalletConfig{ChainID: 11155111, AlchemyPaymasterPolicyID: "test-policy", BundlerProvider: config.BundlerProviderAlchemy}
	require.NotEmpty(t, sponsored.SponsorshipPolicyID())
	valid := &NativeReserve{Recipient: reserveRecipient, Balance: big.NewInt(1)}
	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")

	_, _, _, err := SendNativeReserveMAv2(sponsored, owner, valid, nil, nil, nil)
	require.ErrorContains(t, err, "self-funded")
	_, _, _, err = SendNativeReserveMAv2(selfFunded, owner, nil, nil, nil, nil)
	require.ErrorContains(t, err, "missing native reserve")
	for _, invalid := range []*NativeReserve{
		{Balance: big.NewInt(1)},
		{Recipient: reserveRecipient},
		{Recipient: reserveRecipient, Balance: big.NewInt(0)},
		{Recipient: reserveRecipient, Balance: big.NewInt(1), GrantedLimit: big.NewInt(-1)},
	} {
		_, _, _, err = SendNativeReserveMAv2(selfFunded, owner, invalid, nil, nil, nil)
		require.Error(t, err)
	}
}
