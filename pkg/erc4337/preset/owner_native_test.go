package preset

import (
	"context"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"
)

func nativeOwnerBatch(captured *big.Int) OwnerBatch {
	return OwnerBatch{Calls: []aa.Call{{Target: common.HexToAddress("0x3333333333333333333333333333333333333333"), Value: captured, Data: []byte{}}}, ReserveNativeGas: true}
}

func TestOwnerNativeReservePricesFinalCalldataAndNeverBroadcasts(t *testing.T) {
	for _, sponsored := range []bool{false, true} {
		t.Run(map[bool]string{false: "self funded", true: "sponsored"}[sponsored], func(t *testing.T) {
			f, sw := newOwnerRPCFixture(t)
			if sponsored {
				sw.DisableGasSponsorship = false
				sw.AlchemyPaymasterPolicyID = "test-policy"
				sw.BundlerProvider = config.BundlerProviderAlchemy
			}
			captured := new(big.Int).SetUint64(1_000_000_000_000_000_000)
			batch := nativeOwnerBatch(captured)
			batch.Calls = append(ownerTestCalls(), batch.Calls...)
			p, err := prepareNativeTestBatch(t, f, sw, batch, sponsored)
			require.NoError(t, err)
			resolved, err := batch.Resolve(p.Operation)
			require.NoError(t, err)
			_, values, data, err := aa.UnpackExecuteCalldata(p.Operation.CallData)
			require.NoError(t, err)
			reserve := MaxOwnerGasCost(p.Operation)
			if sponsored {
				reserve.SetInt64(0)
			}
			require.Equal(t, new(big.Int).Sub(captured, reserve), values[len(values)-1])
			require.Empty(t, data[len(data)-1])
			require.Equal(t, captured, batch.Calls[len(batch.Calls)-1].Value, "captured cap is immutable")
			packed, err := aa.PackExecuteBatchMAv2(resolved)
			require.NoError(t, err)
			require.Equal(t, packed, p.Operation.CallData)
			require.Empty(t, p.Operation.Signature)
			require.Zero(t, f.sendCount)
			require.NoError(t, ValidateOwnerUserOp(context.Background(), sw, f.owner, big.NewInt(0), p))
		})
	}
}

func TestOwnerNativeFundingIncludesTransfersEvenWhenSponsored(t *testing.T) {
	for _, sponsored := range []bool{false, true} {
		t.Run(map[bool]string{false: "self funded", true: "sponsored"}[sponsored], func(t *testing.T) {
			f, sw := newOwnerRPCFixture(t)
			if sponsored {
				sw.DisableGasSponsorship = false
				sw.AlchemyPaymasterPolicyID = "test-policy"
				sw.BundlerProvider = config.BundlerProviderAlchemy
			}
			tooMuch := new(big.Int).SetUint64(2_000_000_000_000_000_000)
			_, err := prepareNativeTestBatch(t, f, sw, nativeOwnerBatch(tooMuch), sponsored)
			require.ErrorContains(t, err, "native transfers need")
			require.Zero(t, f.sendCount)
		})
	}
	f, sw := newOwnerRPCFixture(t)
	_, err := PrepareOwnerBatch(context.Background(), sw, f.owner, f.sender, big.NewInt(0), nativeOwnerBatch(big.NewInt(1)), nil)
	require.ErrorContains(t, err, "maximum network fee")
	// A fixed-value call that empties the balance must still cover its gas.
	batch := nativeOwnerBatch(new(big.Int).SetUint64(1_000_000_000_000_000_000))
	batch.ReserveNativeGas = false
	_, err = PrepareOwnerBatch(context.Background(), sw, f.owner, f.sender, big.NewInt(0), batch, nil)
	require.ErrorContains(t, err, "native gas prefund")
}

func TestOwnerNativeReserveConvergesBeforeSigningOrRefuses(t *testing.T) {
	for _, unstable := range []bool{false, true} {
		t.Run(map[bool]string{false: "converges", true: "unstable"}[unstable], func(t *testing.T) {
			batch := nativeOwnerBatch(big.NewInt(1_000_000_000))
			calls, err := batch.estimationCalls()
			require.NoError(t, err)
			packed, err := aa.PackExecuteBatchMAv2(calls)
			require.NoError(t, err)
			op := &userop.UserOperationV07{CallData: packed}
			rounds := 0
			err = priceOwnerBatch(batch, op, false, func() error {
				rounds++
				// The final value's extra nonzero calldata bytes require a larger ceiling.
				pre := int64(30)
				if rounds > 1 {
					pre = 50
				}
				if unstable {
					pre += int64(rounds)
				}
				op.CallGasLimit = big.NewInt(100)
				op.VerificationGasLimit = big.NewInt(50)
				op.PreVerificationGas = big.NewInt(pre)
				op.MaxFeePerGas = big.NewInt(2)
				op.MaxPriorityFeePerGas = big.NewInt(1)
				return nil
			})
			if unstable {
				require.ErrorContains(t, err, "did not converge")
				return
			}
			require.NoError(t, err)
			require.Equal(t, 3, rounds)
			resolved, err := batch.Resolve(op)
			require.NoError(t, err)
			expected, err := aa.PackExecuteBatchMAv2(resolved)
			require.NoError(t, err)
			require.Equal(t, expected, op.CallData)
			require.Equal(t, "999999600", resolved[0].Value.String())
		})
	}
}

func TestOwnerNativeReserveRejectsAmbiguousAndInvalidCaps(t *testing.T) {
	for _, captured := range []*big.Int{nil, big.NewInt(-1), big.NewInt(0), new(big.Int).Lsh(big.NewInt(1), 256)} {
		_, err := nativeOwnerBatch(captured).estimationCalls()
		require.Error(t, err)
	}
	batch := nativeOwnerBatch(big.NewInt(1000))
	batch.Calls[0].Data = []byte{1}
	_, err := batch.estimationCalls()
	require.ErrorContains(t, err, "terminal native")
	batch = nativeOwnerBatch(big.NewInt(1000))
	batch.Calls = append([]aa.Call{{Target: batch.Calls[0].Target, Value: big.NewInt(1)}}, batch.Calls...)
	_, err = batch.estimationCalls()
	require.ErrorContains(t, err, "another native")
}

func TestOwnerNativeReserveKeepsOneFeeQuoteAcrossFinalCalldataEstimates(t *testing.T) {
	f, sw := newOwnerRPCFixture(t)
	f.risingBaseFee = true
	p, err := PrepareOwnerBatch(context.Background(), sw, f.owner, f.sender, big.NewInt(0), nativeOwnerBatch(big.NewInt(1_000_000_000_000_000_000)), nil)
	require.NoError(t, err)
	require.Equal(t, 1, f.headerReads)
	require.Equal(t, 2, f.tipReads, "quote chain and bundler priority fees once each")
	require.GreaterOrEqual(t, f.estimateReads, 2, "still estimate the actual final transfer calldata")
	require.Empty(t, p.Operation.Signature)
	require.Zero(t, f.sendCount)
}

func TestOwnerNativeReserveDoesNotMutateAliasedEstimatorIntegers(t *testing.T) {
	batch := nativeOwnerBatch(big.NewInt(1_000_000_000))
	calls, err := batch.estimationCalls()
	require.NoError(t, err)
	packed, err := aa.PackExecuteBatchMAv2(calls)
	require.NoError(t, err)
	op := &userop.UserOperationV07{CallData: packed}
	sharedGas := big.NewInt(1)
	sharedFee := big.NewInt(2)
	rounds := 0
	err = priceOwnerBatch(batch, op, false, func() error {
		rounds++
		if rounds == 1 {
			op.CallGasLimit, op.VerificationGasLimit, op.PreVerificationGas = big.NewInt(100), big.NewInt(50), big.NewInt(30)
		} else {
			op.CallGasLimit, op.VerificationGasLimit, op.PreVerificationGas = sharedGas, sharedGas, sharedGas
			sharedFee.SetInt64(1)
		}
		op.MaxFeePerGas, op.MaxPriorityFeePerGas = sharedFee, sharedFee
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, rounds)
	require.Equal(t, "100", op.CallGasLimit.String())
	require.Equal(t, "50", op.VerificationGasLimit.String())
	require.Equal(t, "30", op.PreVerificationGas.String())
	require.Equal(t, "2", op.MaxFeePerGas.String())
	require.Equal(t, "1", sharedGas.String())
	require.Equal(t, "1", sharedFee.String())
	require.NotSame(t, sharedGas, op.CallGasLimit)
	require.NotSame(t, sharedFee, op.MaxFeePerGas)
}

func TestOwnerSponsoredZeroValueSkipsBalanceRPC(t *testing.T) {
	for _, sponsored := range []bool{false, true} {
		for _, native := range []bool{false, true} {
			t.Run(map[bool]string{false: "self-funded", true: "sponsored"}[sponsored]+map[bool]string{false: "/ERC20", true: "/native"}[native], func(t *testing.T) {
				f, sw := newOwnerRPCFixture(t)
				f.balanceUnavailable = true
				calls := ownerTestCalls()
				if native {
					calls = nativeOwnerBatch(big.NewInt(1)).Calls
				}
				packed, err := aa.PackExecuteBatchMAv2(calls)
				require.NoError(t, err)
				op := &userop.UserOperationV07{Sender: f.sender, CallData: packed}
				if sponsored {
					paymaster := common.HexToAddress("0x5555555555555555555555555555555555555555")
					op.Paymaster = &paymaster
				}
				chain, err := ethclient.DialContext(context.Background(), sw.EthRpcUrl)
				require.NoError(t, err)
				defer chain.Close()
				err = checkOwnerPrefund(context.Background(), chain, op)
				if sponsored && !native {
					require.NoError(t, err)
					require.Zero(t, f.balanceReads)
				} else {
					require.ErrorContains(t, err, "balance unavailable")
					require.Equal(t, 1, f.balanceReads)
				}
			})
		}
	}
}

// The production Alchemy URL is derived from its API key. Exercise sponsored
// pricing with the same function but a local RPC fixture, never a live policy.
func prepareNativeTestBatch(t *testing.T, f *ownerRPCFixture, sw *config.SmartWalletConfig, batch OwnerBatch, sponsored bool) (*PreparedOwnerUserOp, error) {
	t.Helper()
	if !sponsored {
		return PrepareOwnerBatch(context.Background(), sw, f.owner, f.sender, big.NewInt(0), batch, nil)
	}
	client, err := rpc.DialContext(context.Background(), sw.EthRpcUrl)
	require.NoError(t, err)
	defer client.Close()
	chain := ethclient.NewClient(client)
	identity, err := inspectOwnerAccount(context.Background(), chain, sw, f.owner, f.sender, big.NewInt(0))
	if err != nil {
		return nil, err
	}
	op := &userop.UserOperationV07{Sender: f.sender, Nonce: f.nonce, CallGasLimit: new(big.Int), VerificationGasLimit: new(big.Int), PreVerificationGas: new(big.Int), MaxFeePerGas: new(big.Int), MaxPriorityFeePerGas: new(big.Int)}
	err = priceOwnerBatch(batch, op, true, func() error {
		return priceOperationV07(context.Background(), chain, client, op, EntryPointV07(), sw, logger.EnsureLogger(nil))
	})
	if err != nil {
		return nil, err
	}
	if err = checkOwnerPrefund(context.Background(), chain, op); err != nil {
		return nil, err
	}
	return &PreparedOwnerUserOp{Operation: op, Identity: *identity}, nil
}

func TestOwnerNativeEOASweepKeepsOwnerSigningIdentity(t *testing.T) {
	f, sw := newOwnerEOARPCFixture(t)
	p, err := PrepareOwnerBatch(context.Background(), sw, f.owner, f.sender, nil, nativeOwnerBatch(big.NewInt(1_000_000_000_000_000_000)), nil)
	require.NoError(t, err)
	require.Equal(t, f.owner, p.Operation.Sender)
	require.Nil(t, p.Operation.Factory)
	require.Empty(t, p.Operation.FactoryData)
	require.Equal(t, config.SMA7702Delegate(), p.Identity.Implementation)
	require.Equal(t, config.SMA7702ImplHash(), p.Identity.ImplementationHash)
	require.NoError(t, ValidateOwnerUserOp(context.Background(), sw, f.owner, nil, p))
	require.Zero(t, f.sendCount)
}
