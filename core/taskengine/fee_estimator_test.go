package taskengine

import (
	"context"
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/services"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPriceService implements PriceService interface for testing
type mockPriceService struct{}

func (mock *mockPriceService) GetNativeTokenPriceUSD(chainID int64) (*big.Float, error) {
	switch chainID {
	case 1:
		return big.NewFloat(3000.0), nil
	case 11155111:
		return big.NewFloat(2500.0), nil
	case 8453:
		return big.NewFloat(3000.0), nil
	case 84532:
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

	cachedChainID, err := feeEstimator.getChainID(ctx)
	require.NoError(t, err)
	assert.Equal(t, detectedChainID, cachedChainID)

	testAmount := big.NewInt(1000000000000000000) // 1 ETH in wei
	feeAmount, err := feeEstimator.convertToFeeAmount(testAmount)
	require.NoError(t, err)
	assert.Equal(t, "ETH", feeAmount.NativeTokenSymbol)
	assert.Equal(t, testAmount.String(), feeAmount.NativeTokenAmount)
	assert.NotEmpty(t, feeAmount.UsdAmount)
}

func TestFeeEstimator_MoralisServiceIntegration(t *testing.T) {
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err)

	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	ethClient := testutil.GetRpcClient()
	defer ethClient.Close()

	moralisService := services.GetMoralisService("", logger)

	feeEstimator := NewFeeEstimator(logger, ethClient, nil, smartWalletConfig, moralisService)

	ctx := context.Background()
	chainID, err := feeEstimator.getChainID(ctx)
	require.NoError(t, err)
	assert.True(t, chainID > 0)

	price, err := moralisService.GetNativeTokenPriceUSD(chainID)
	require.NoError(t, err)
	assert.True(t, price.Cmp(big.NewFloat(0)) > 0)

	symbol := moralisService.GetNativeTokenSymbol(chainID)
	assert.Equal(t, "ETH", symbol)

	supportedChains := moralisService.GetSupportedChains()
	assert.Contains(t, supportedChains, int64(1))
	assert.Contains(t, supportedChains, int64(8453))
	assert.Len(t, supportedChains, 4)
}

func TestIsOnChainNode(t *testing.T) {
	assert.True(t, isOnChainNode(&avsproto.TaskNode{TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{}}}))
	assert.True(t, isOnChainNode(&avsproto.TaskNode{TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{}}}))
	assert.True(t, isOnChainNode(&avsproto.TaskNode{TaskType: &avsproto.TaskNode_Loop{Loop: &avsproto.LoopNode{}}}))

	assert.False(t, isOnChainNode(&avsproto.TaskNode{TaskType: &avsproto.TaskNode_ContractRead{ContractRead: &avsproto.ContractReadNode{}}}))
	assert.False(t, isOnChainNode(&avsproto.TaskNode{TaskType: &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{}}}))
	assert.False(t, isOnChainNode(&avsproto.TaskNode{TaskType: &avsproto.TaskNode_Branch{Branch: &avsproto.BranchNode{}}}))
	assert.False(t, isOnChainNode(&avsproto.TaskNode{TaskType: &avsproto.TaskNode_Filter{Filter: &avsproto.FilterNode{}}}))
	assert.False(t, isOnChainNode(&avsproto.TaskNode{}))
}

func TestDefaultFeeRates(t *testing.T) {
	rates := getDefaultFeeRates()

	assert.Equal(t, 0.02, rates.ExecutionFeeUSD, "Execution fee should be $0.02")
	assert.Equal(t, 0.03, rates.Tier1FeePercentage, "Tier 1 should be 0.03%")
	assert.Equal(t, 0.09, rates.Tier2FeePercentage, "Tier 2 should be 0.09%")
	assert.Equal(t, 0.18, rates.Tier3FeePercentage, "Tier 3 should be 0.18%")
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
		assert.Equal(t, 0.20, rates.Tier2FeePercentage)
		assert.Equal(t, 0.50, rates.Tier3FeePercentage)
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
	assert.Equal(t, float32(0.03), valueFee.FeePercentage)
	assert.Equal(t, "rule_based", valueFee.ClassificationMethod)
	assert.Equal(t, float32(1.0), valueFee.Confidence)
	assert.NotEmpty(t, valueFee.Reason)
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
			{Id: "api1", TaskType: &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{}}},
		},
	}

	valueFee := feeEstimator.classifyWorkflowValue(req)

	assert.Equal(t, avsproto.ExecutionTier_EXECUTION_TIER_UNSPECIFIED, valueFee.Tier)
	assert.Equal(t, float32(0), valueFee.FeePercentage)
	assert.Contains(t, valueFee.Reason, "no on-chain")
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
			{Id: "read1", Name: "Read Price", TaskType: &avsproto.TaskNode_ContractRead{ContractRead: &avsproto.ContractReadNode{}}},
			{Id: "branch1", Name: "Check", TaskType: &avsproto.TaskNode_Branch{Branch: &avsproto.BranchNode{}}},
			{Id: "write1", Name: "Swap", TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{}}},
			{Id: "transfer1", Name: "Send", TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{}}},
		},
	}

	// Need to detect chain ID first
	_, err = feeEstimator.getChainID(context.Background())
	require.NoError(t, err)

	runnerAddr := testutil.GetTestSmartWalletConfig().ControllerAddress
	cogs, totalCogsWei, err := feeEstimator.estimateCOGS(context.Background(), req, runnerAddr)
	require.NoError(t, err)

	// Only on-chain nodes (write1, transfer1) should have COGS entries
	assert.Len(t, cogs, 2, "Only on-chain nodes should have COGS")
	assert.Equal(t, "write1", cogs[0].NodeId)
	assert.Equal(t, "gas", cogs[0].CostType)
	assert.Equal(t, "transfer1", cogs[1].NodeId)
	assert.True(t, totalCogsWei.Cmp(big.NewInt(0)) > 0, "Total COGS should be non-zero")
}

func TestEstimateExecutionCount(t *testing.T) {
	logger, err := sdklogging.NewZapLogger(sdklogging.Development)
	require.NoError(t, err)

	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	ethClient := testutil.GetRpcClient()
	defer ethClient.Close()

	feeEstimator := NewFeeEstimator(logger, ethClient, nil, smartWalletConfig, &mockPriceService{})

	t.Run("manual trigger defaults to 1", func(t *testing.T) {
		count := feeEstimator.estimateExecutionCount(
			&avsproto.TaskTrigger{Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL}, 1440, 0)
		assert.Equal(t, int64(1), count)
	})

	t.Run("manual trigger respects maxExecution", func(t *testing.T) {
		count := feeEstimator.estimateExecutionCount(
			&avsproto.TaskTrigger{Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL}, 1440, 5)
		assert.Equal(t, int64(5), count)
	})

	t.Run("fixed_time is always 1", func(t *testing.T) {
		count := feeEstimator.estimateExecutionCount(
			&avsproto.TaskTrigger{Type: avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME}, 1440, 0)
		assert.Equal(t, int64(1), count)
	})

	t.Run("cron estimates hourly over 24h", func(t *testing.T) {
		count := feeEstimator.estimateExecutionCount(
			&avsproto.TaskTrigger{Type: avsproto.TriggerType_TRIGGER_TYPE_CRON}, 1440, 0)
		assert.Equal(t, int64(24), count)
	})

	t.Run("cron respects maxExecution cap", func(t *testing.T) {
		count := feeEstimator.estimateExecutionCount(
			&avsproto.TaskTrigger{Type: avsproto.TriggerType_TRIGGER_TYPE_CRON}, 1440, 10)
		assert.Equal(t, int64(10), count)
	})

	t.Run("event defaults to at least 1", func(t *testing.T) {
		count := feeEstimator.estimateExecutionCount(
			&avsproto.TaskTrigger{Type: avsproto.TriggerType_TRIGGER_TYPE_EVENT}, 60, 0)
		assert.Equal(t, int64(1), count)
	})
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
	execFee, _ := feeEstimator.convertUSDToFeeAmount(feeEstimator.feeRates.ExecutionFeeUSD)
	assert.NotEqual(t, "0", execFee.NativeTokenAmount)

	// Verify value fee uses custom tier rate
	req := &avsproto.EstimateFeesReq{
		Nodes: []*avsproto.TaskNode{
			{Id: "w1", TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{}}},
		},
	}
	valueFee := feeEstimator.classifyWorkflowValue(req)
	assert.Equal(t, float32(0.10), valueFee.FeePercentage, "Should use custom Tier 1 rate")
}
