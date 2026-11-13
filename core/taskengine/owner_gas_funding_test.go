package taskengine

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const ownerTestPolicy = "test-policy"

// Give the fixture chain an Alchemy Gas Manager policy, as on mainnets.
func sponsorOwnerFixtureChain(t *testing.T, n *Engine) {
	t.Helper()
	sw := n.ResolveSmartWalletConfig(11155111)
	sw.DisableGasSponsorship, sw.AlchemyPaymasterPolicyID = false, ownerTestPolicy
	require.Equal(t, ownerTestPolicy, sw.SponsorshipPolicyID())
}

// An ETH-only sweep with reserveGas, the shape Studio's Sweep Skill prepares.
func ownerSweepPrepare(n *Engine, user *model.User, funding OwnerGasFunding) (*OwnerWorkflowPreparation, error) {
	trigger, nodes, edges, input := ownerWorkflowFixture()
	nodes[0].GetCustomCode().Config.Source = "return {native:'1000000000000000000'};"
	nodes = append(nodes[:1], ownerNativeNode())
	edges = append(edges[:1], &avsproto.TaskEdge{Id: "nativeEdge", Source: "amounts", Target: "native"})
	return n.PrepareOwnerWorkflow(context.Background(), user, trigger, nodes, edges, input, 11155111, funding)
}

func prepareOwnerSweep(t *testing.T, n *Engine, user *model.User, funding OwnerGasFunding) *OwnerWorkflowPreparation {
	t.Helper()
	p, err := ownerSweepPrepare(n, user, funding)
	require.NoError(t, err)
	return p
}

func lastCallValue(t *testing.T, p *OwnerWorkflowPreparation) *big.Int {
	t.Helper()
	_, values, _, err := aa.UnpackExecuteCalldata(p.Operation.CallData)
	require.NoError(t, err)
	return values[len(values)-1]
}

func TestOwnerGasFundingOmittedKeepsChainSponsorship(t *testing.T) {
	for _, funding := range []OwnerGasFunding{OwnerGasFundingChainDefault, OwnerGasFundingSponsored} {
		t.Run(string(funding), func(t *testing.T) {
			n, user, _, backend := ownerEngineFixture(t)
			sponsorOwnerFixtureChain(t, n)
			p := prepareOwnerSweep(t, n, user, funding)
			require.EqualValues(t, 1, backend.prepared.Load())
			require.Zero(t, backend.selfFunded.Load())
			require.Equal(t, ownerTestPolicy, backend.pricedWith.SponsorshipPolicyID(), "the chain configuration reaches pricing unchanged")
			require.NotNil(t, p.Operation.Paymaster)
			require.Equal(t, "1000000000000000000", lastCallValue(t, p).String(), "a sponsored sweep keeps no fee reserve")
		})
	}
	// Without a policy the default stays self-funded through the same path.
	n, user, _, backend := ownerEngineFixture(t)
	p := prepareOwnerSweep(t, n, user, OwnerGasFundingChainDefault)
	require.EqualValues(t, 1, backend.prepared.Load())
	require.Zero(t, backend.selfFunded.Load())
	require.Nil(t, p.Operation.Paymaster)
}

func TestOwnerGasFundingSourceNeverSponsorsOnPolicyChain(t *testing.T) {
	n, user, key, backend := ownerEngineFixture(t)
	sponsorOwnerFixtureChain(t, n)
	before := time.Now()
	p := prepareOwnerSweep(t, n, user, OwnerGasFundingSource)
	after := time.Now()
	require.Zero(t, backend.prepared.Load())
	require.EqualValues(t, 1, backend.selfFunded.Load())
	require.Empty(t, backend.pricedWith.SponsorshipPolicyID())
	require.Empty(t, backend.pricedWith.AlchemyPaymasterPolicyID)
	require.True(t, backend.pricedWith.DisableGasSponsorship)
	require.Equal(t, ownerTestPolicy, n.ResolveSmartWalletConfig(11155111).SponsorshipPolicyID(), "the shared chain configuration is not mutated")
	op := p.Operation
	require.Nil(t, op.Paymaster)
	require.Empty(t, op.PaymasterData)
	require.Nil(t, op.PaymasterVerificationGasLimit)
	require.Nil(t, op.PaymasterPostOpGasLimit)
	reserve := preset.MaxOwnerGasCost(op)
	require.Positive(t, reserve.Sign())
	require.Equal(t, new(big.Int).Sub(big.NewInt(1_000_000_000_000_000_000), reserve), lastCallValue(t, p), "the source keeps the maximum fee back")
	require.GreaterOrEqual(t, p.ExpiresAt, before.Add(ownerPreparationTTL).UnixMilli(), "source uses the same signing window")
	require.LessOrEqual(t, p.ExpiresAt, after.Add(ownerPreparationTTL).UnixMilli())
	_, err := n.SubmitOwnerExecution(context.Background(), user, p.Execution.Id, signOwnerFixture(t, p, key))
	require.NoError(t, err)
	require.Nil(t, backend.signed.Paymaster, "the stored self-funded operation is what is relayed")
}

func TestOwnerGasFundingSourceRefusesSponsoredPricing(t *testing.T) {
	n, user, _, backend := ownerEngineFixture(t)
	sponsorOwnerFixtureChain(t, n)
	backend.selfFundedPaymaster = true
	_, err := ownerSweepPrepare(n, user, OwnerGasFundingSource)
	require.ErrorContains(t, err, "paymaster")
	count, err := n.countPreparedOwnerExecutions(user.Address)
	require.NoError(t, err)
	require.Zero(t, count, "a refused preparation is not stored")
}

func TestOwnerGasFundingSourceReportsFundingRefusal(t *testing.T) {
	fee := &preset.OwnerFundingError{Required: big.NewInt(3_000_000_000_000), Available: big.NewInt(1)}
	n, user, _, backend := ownerEngineFixture(t)
	sponsorOwnerFixtureChain(t, n)
	backend.prepareError = fee
	_, err := ownerSweepPrepare(n, user, OwnerGasFundingSource)
	var refusal *preset.OwnerFundingError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, fee.Required, refusal.Required)
	require.Equal(t, fee.Available, refusal.Available)

	// The chain default keeps today's prose-only FailedPrecondition.
	_, err = ownerSweepPrepare(n, user, OwnerGasFundingChainDefault)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.False(t, errors.As(err, &refusal))

	// Other self-funded pricing failures are not reported as funding.
	backend.prepareError = errors.New("bundler unavailable")
	_, err = ownerSweepPrepare(n, user, OwnerGasFundingSource)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.False(t, errors.As(err, &refusal))
}

func TestOwnerGasFundingRejectsUnknownOrUnavailableChoice(t *testing.T) {
	n, user, _, backend := ownerEngineFixture(t)
	_, err := ownerSweepPrepare(n, user, OwnerGasFunding("free"))
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	// The fixture chain has no policy: an explicit sponsorship request is
	// refused instead of quietly charging the source.
	_, err = ownerSweepPrepare(n, user, OwnerGasFundingSponsored)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "not available on this chain")
	require.Zero(t, backend.prepared.Load())
	require.Zero(t, backend.selfFunded.Load())
}
