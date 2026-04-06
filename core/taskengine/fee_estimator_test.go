package taskengine

import (
	"context"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockPriceService struct{}

func (mock *mockPriceService) GetNativeTokenPriceUSD(chainID int64) (*big.Float, error) {
	switch chainID {
	case 11155111:
		return big.NewFloat(2500.0), nil
	default:
		return big.NewFloat(2000.0), nil
	}
}

func (mock *mockPriceService) GetNativeTokenSymbol(chainID int64) string {
	return "ETH"
}

func TestFeeEstimator_ChainIDDetection(t *testing.T) {
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err)

	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	ethClient := testutil.GetRpcClient()
	defer ethClient.Close()

	feeEstimator := NewFeeEstimator(logger, ethClient, nil, smartWalletConfig, &mockPriceService{})

	assert.Equal(t, int64(0), feeEstimator.chainID)

	ctx := context.Background()
	detectedChainID, err := feeEstimator.getChainID(ctx)
	require.NoError(t, err)
	assert.True(t, detectedChainID > 0)
	assert.Equal(t, detectedChainID, feeEstimator.chainID)
}

func TestIsOnChainNode(t *testing.T) {
	assert.True(t, isOnChainNode(&avsproto.TaskNode{TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{}}}))
	assert.True(t, isOnChainNode(&avsproto.TaskNode{TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{}}}))
	assert.True(t, isOnChainNode(&avsproto.TaskNode{TaskType: &avsproto.TaskNode_Loop{Loop: &avsproto.LoopNode{}}}))

	assert.False(t, isOnChainNode(&avsproto.TaskNode{TaskType: &avsproto.TaskNode_ContractRead{ContractRead: &avsproto.ContractReadNode{}}}))
	assert.False(t, isOnChainNode(&avsproto.TaskNode{TaskType: &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{}}}))
	assert.False(t, isOnChainNode(&avsproto.TaskNode{TaskType: &avsproto.TaskNode_Branch{Branch: &avsproto.BranchNode{}}}))
	assert.False(t, isOnChainNode(&avsproto.TaskNode{}))
}

func TestDefaultFeeRates(t *testing.T) {
	rates := getDefaultFeeRates()

	assert.Equal(t, 0.02, rates.ExecutionFeeUSD)
	assert.Equal(t, 0.03, rates.Tier1FeePercentage)
	assert.Equal(t, 0.09, rates.Tier2FeePercentage)
	assert.Equal(t, 0.18, rates.Tier3FeePercentage)
}

func TestConvertFeeRatesConfig(t *testing.T) {
	t.Run("nil config returns defaults", func(t *testing.T) {
		rates := convertFeeRatesConfig(nil)
		defaults := getDefaultFeeRates()
		assert.Equal(t, defaults, rates)
	})

	t.Run("custom config is applied", func(t *testing.T) {
		customConfig := &config.FeeRatesConfig{
			ExecutionFeeUSD:    0.05,
			Tier1FeePercentage: 0.10,
			Tier2FeePercentage: 0.20,
			Tier3FeePercentage: 0.50,
		}
		rates := convertFeeRatesConfig(customConfig)
		assert.Equal(t, 0.05, rates.ExecutionFeeUSD)
		assert.Equal(t, 0.10, rates.Tier1FeePercentage)
	})
}

func TestClassifyWorkflowValue_WithOnChainNodes(t *testing.T) {
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err)

	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	ethClient := testutil.GetRpcClient()
	defer ethClient.Close()

	feeEstimator := NewFeeEstimator(logger, ethClient, nil, smartWalletConfig, &mockPriceService{})

	req := &avsproto.EstimateFeesReq{
		Nodes: []*avsproto.TaskNode{
			{Id: "read1", TaskType: &avsproto.TaskNode_ContractRead{ContractRead: &avsproto.ContractReadNode{}}},
			{Id: "write1", TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{}}},
		},
	}

	valueFee := feeEstimator.classifyWorkflowValue(req)

	assert.Equal(t, avsproto.ExecutionTier_EXECUTION_TIER_1, valueFee.Tier)
	require.NotNil(t, valueFee.Fee)
	assert.Equal(t, "0.03", valueFee.Fee.Amount)
	assert.Equal(t, "PERCENTAGE", valueFee.Fee.Unit)
	assert.Equal(t, "input_token_value", valueFee.ValueBase)
	assert.Equal(t, "rule_based", valueFee.ClassificationMethod)
	assert.Equal(t, float32(1.0), valueFee.Confidence)
}

func TestClassifyWorkflowValue_NoOnChainNodes(t *testing.T) {
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err)

	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	ethClient := testutil.GetRpcClient()
	defer ethClient.Close()

	feeEstimator := NewFeeEstimator(logger, ethClient, nil, smartWalletConfig, &mockPriceService{})

	req := &avsproto.EstimateFeesReq{
		Nodes: []*avsproto.TaskNode{
			{Id: "read1", TaskType: &avsproto.TaskNode_ContractRead{ContractRead: &avsproto.ContractReadNode{}}},
			{Id: "branch1", TaskType: &avsproto.TaskNode_Branch{Branch: &avsproto.BranchNode{}}},
		},
	}

	valueFee := feeEstimator.classifyWorkflowValue(req)

	assert.Equal(t, avsproto.ExecutionTier_EXECUTION_TIER_UNSPECIFIED, valueFee.Tier)
	assert.Equal(t, "0", valueFee.Fee.Amount)
	assert.Equal(t, "", valueFee.ValueBase)
}

func TestEstimateCOGS_MixedNodes(t *testing.T) {
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err)

	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	ethClient := testutil.GetRpcClient()
	defer ethClient.Close()

	feeEstimator := NewFeeEstimator(logger, ethClient, nil, smartWalletConfig, &mockPriceService{})

	req := &avsproto.EstimateFeesReq{
		Nodes: []*avsproto.TaskNode{
			{Id: "read1", TaskType: &avsproto.TaskNode_ContractRead{ContractRead: &avsproto.ContractReadNode{}}},
			{Id: "branch1", TaskType: &avsproto.TaskNode_Branch{Branch: &avsproto.BranchNode{}}},
			{Id: "write1", TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{}}},
			{Id: "transfer1", TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{}}},
		},
	}

	_, err = feeEstimator.getChainID(context.Background())
	require.NoError(t, err)

	runnerAddr := testutil.GetTestSmartWalletConfig().ControllerAddress
	cogs, err := feeEstimator.estimateCOGS(context.Background(), req, runnerAddr)
	require.NoError(t, err)

	// Only on-chain nodes should have COGS entries
	assert.Len(t, cogs, 2)
	assert.Equal(t, "write1", cogs[0].NodeId)
	assert.Equal(t, "gas", cogs[0].CostType)
	assert.Equal(t, "WEI", cogs[0].Fee.Unit)
	assert.Equal(t, "transfer1", cogs[1].NodeId)

	// Fee amount should be a non-zero WEI string
	weiAmount, ok := new(big.Int).SetString(cogs[0].Fee.Amount, 10)
	assert.True(t, ok)
	assert.True(t, weiAmount.Cmp(big.NewInt(0)) > 0)
}

func TestEstimateCOGS_NoOnChainNodes(t *testing.T) {
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err)

	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	ethClient := testutil.GetRpcClient()
	defer ethClient.Close()

	feeEstimator := NewFeeEstimator(logger, ethClient, nil, smartWalletConfig, &mockPriceService{})

	req := &avsproto.EstimateFeesReq{
		Nodes: []*avsproto.TaskNode{
			{Id: "read1", TaskType: &avsproto.TaskNode_ContractRead{ContractRead: &avsproto.ContractReadNode{}}},
			{Id: "api1", TaskType: &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{}}},
		},
	}

	_, err = feeEstimator.getChainID(context.Background())
	require.NoError(t, err)

	runnerAddr := testutil.GetTestSmartWalletConfig().ControllerAddress
	cogs, err := feeEstimator.estimateCOGS(context.Background(), req, runnerAddr)
	require.NoError(t, err)

	assert.Len(t, cogs, 0, "Non on-chain nodes should produce no COGS")
}

func TestCustomConfig_AffectsAllFees(t *testing.T) {
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err)

	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	ethClient := testutil.GetRpcClient()
	defer ethClient.Close()

	customConfig := &config.FeeRatesConfig{
		ExecutionFeeUSD:    0.05,
		Tier1FeePercentage: 0.10,
		Tier2FeePercentage: 0.20,
		Tier3FeePercentage: 0.50,
	}

	feeEstimator := NewFeeEstimatorWithConfig(logger, ethClient, nil, smartWalletConfig, &mockPriceService{}, customConfig)

	// Verify execution fee uses custom rate
	assert.Equal(t, 0.05, feeEstimator.feeRates.ExecutionFeeUSD)

	// Verify value fee uses custom tier rate
	req := &avsproto.EstimateFeesReq{
		Nodes: []*avsproto.TaskNode{
			{Id: "w1", TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{}}},
		},
	}
	valueFee := feeEstimator.classifyWorkflowValue(req)
	assert.Equal(t, "0.10", valueFee.Fee.Amount, "Should use custom Tier 1 rate")
}

func TestEstimateFeesResp_Format(t *testing.T) {
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err)

	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	ethClient := testutil.GetRpcClient()
	defer ethClient.Close()

	feeEstimator := NewFeeEstimator(logger, ethClient, nil, smartWalletConfig, &mockPriceService{})

	req := &avsproto.EstimateFeesReq{
		Trigger: &avsproto.TaskTrigger{Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL},
		Nodes: []*avsproto.TaskNode{
			{Id: "read1", TaskType: &avsproto.TaskNode_ContractRead{ContractRead: &avsproto.ContractReadNode{}}},
			{Id: "write1", TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{}}},
		},
		Runner:       "0x0000000000000000000000000000000000000001",
		CreatedAt:    1000000,
		ExpireAt:     2000000,
		MaxExecution: 1,
	}

	resp, err := feeEstimator.EstimateFees(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// execution_fee is Fee{amount, unit}
	require.NotNil(t, resp.ExecutionFee)
	assert.Equal(t, "USD", resp.ExecutionFee.Unit)
	assert.Equal(t, "0.020000", resp.ExecutionFee.Amount)

	// native_token is set
	require.NotNil(t, resp.NativeToken)
	assert.Equal(t, "ETH", resp.NativeToken.Symbol)
	assert.Equal(t, int32(18), resp.NativeToken.Decimals)

	// cogs entries use Fee{amount: WEI}
	// May include wallet_creation COGS if runner address doesn't exist on-chain
	assert.GreaterOrEqual(t, len(resp.Cogs), 1, "Should have at least contract_write COGS")

	// Find the contract_write COGS entry
	var writeCogs *avsproto.NodeCOGS
	for _, cogsEntry := range resp.Cogs {
		if cogsEntry.NodeId == "write1" {
			writeCogs = cogsEntry
			break
		}
	}
	require.NotNil(t, writeCogs, "Should have COGS for write1")
	assert.Equal(t, "gas", writeCogs.CostType)
	assert.Equal(t, "WEI", writeCogs.Fee.Unit)

	// value_fee uses Fee{amount: PERCENTAGE}
	require.NotNil(t, resp.ValueFee)
	assert.Equal(t, "PERCENTAGE", resp.ValueFee.Fee.Unit)
	assert.Equal(t, "0.03", resp.ValueFee.Fee.Amount)
	assert.Equal(t, "input_token_value", resp.ValueFee.ValueBase)

	// discounts is empty array (not nil)
	assert.NotNil(t, resp.Discounts)
	assert.Len(t, resp.Discounts, 0)

	// pricing_model set
	assert.Equal(t, "v1", resp.PricingModel)

	// chain_id set
	assert.NotEmpty(t, resp.ChainId)
}
