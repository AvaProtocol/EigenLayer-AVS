package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/services"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// FeeEstimator handles comprehensive fee estimation for workflows
type FeeEstimator struct {
	logger            sdklogging.Logger
	ethClient         *ethclient.Client
	tenderlyClient    *TenderlyClient
	smartWalletConfig *config.SmartWalletConfig

	// Chain ID caching with thread safety
	chainIDMu sync.RWMutex
	chainID   int64 // Cached after first detection

	// Price service for USD conversion
	priceService PriceService

	// Fee configuration
	automationRates *AutomationFeeRates
	discountRules   *DiscountRules
}

// PriceService interface for getting token prices
type PriceService interface {
	GetNativeTokenPriceUSD(chainID int64) (*big.Float, error)
	GetNativeTokenSymbol(chainID int64) string
}

// AutomationFeeRates defines pricing for different trigger types
type AutomationFeeRates struct {
	// Base fees (one-time per workflow)
	BaseFeeUSD float64 // $0.00 initially

	// Monitoring fees (per minute)
	ManualMonitoringFeeUSDPerMinute    float64 // $0.00
	FixedTimeMonitoringFeeUSDPerMinute float64 // $0.000017 (~$0.01/day)
	CronMonitoringFeeUSDPerMinute      float64 // $0.000033 (~$0.02/day)
	BlockMonitoringFeeUSDPerMinute     float64 // $0.000033 (~$0.02/day)
	EventMonitoringFeeUSDPerMinute     float64 // $0.000083 (~$0.05/day base)

	// Per-execution fees
	ManualExecutionFeeUSD    float64 // $0.00
	ScheduledExecutionFeeUSD float64 // $0.005
	BlockExecutionFeeUSD     float64 // $0.01
	EventExecutionFeeUSD     float64 // $0.01

	// Event monitoring scaling factors
	EventAddressFeeUSDPerMinute float64 // $0.000008 (~$0.005/day per address)
	EventTopicFeeUSDPerMinute   float64 // $0.000003 (~$0.002/day per topic group)
}

// DiscountRules defines promotional discount rules
type DiscountRules struct {
	// Beta program discount
	BetaProgramAutomationDiscount float64 // 100% (1.0)
	BetaProgramGasDiscount        float64 // 0% (0.0)

	// New user discount
	NewUserAutomationDiscount float64 // 80% (0.8)
	NewUserGasDiscount        float64 // 0% (0.0)

	// Volume discount thresholds
	VolumeDiscountThreshold  int     // Number of workflows for volume discount
	VolumeDiscountPercentage float64 // Discount percentage for volume users
}

// GasEstimationResult holds gas estimation data for a single operation
type GasEstimationResult struct {
	NodeID           string
	OperationType    string // "contract_write", "eth_transfer", "smart_wallet_creation"
	MethodName       string
	GasUnits         *big.Int
	GasPrice         *big.Int
	TotalCost        *big.Int
	EstimationMethod string // "rpc_estimate", "tenderly_simulation", "fallback"
	Success          bool
	Error            string
}

// NewFeeEstimator creates a new fee estimator instance
// Chain ID will be detected automatically from the eth client
func NewFeeEstimator(
	logger sdklogging.Logger,
	ethClient *ethclient.Client,
	tenderlyClient *TenderlyClient,
	smartWalletConfig *config.SmartWalletConfig,
	priceService PriceService,
) *FeeEstimator {
	return &FeeEstimator{
		logger:            logger,
		ethClient:         ethClient,
		tenderlyClient:    tenderlyClient,
		smartWalletConfig: smartWalletConfig,
		chainID:           0, // Will be detected on first use
		priceService:      priceService,
		automationRates:   getDefaultAutomationRates(),
		discountRules:     getDefaultDiscountRules(),
	}
}

// getChainID detects and caches the chain ID from the eth client
func (fe *FeeEstimator) getChainID(ctx context.Context) (int64, error) {
	// Check cached value with read lock first
	fe.chainIDMu.RLock()
	if fe.chainID != 0 {
		cachedID := fe.chainID
		fe.chainIDMu.RUnlock()
		return cachedID, nil
	}
	fe.chainIDMu.RUnlock()

	// Need to detect chain ID, acquire write lock
	fe.chainIDMu.Lock()
	defer fe.chainIDMu.Unlock()

	// Double-check in case another goroutine set it while we waited for the lock
	if fe.chainID != 0 {
		return fe.chainID, nil
	}

	// Detect chain ID from eth client
	chainID, err := fe.ethClient.ChainID(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get chain ID from eth client: %w", err)
	}

	// Cache the result
	fe.chainID = chainID.Int64()
	fe.logger.Info("🔗 Detected chain ID from eth client", "chain_id", fe.chainID)

	return fe.chainID, nil
}

// EstimateFees provides comprehensive fee estimation for a workflow
func (fe *FeeEstimator) EstimateFees(ctx context.Context, req *avsproto.EstimateFeesReq) (*avsproto.EstimateFeesResp, error) {
	// Detect chain ID first
	chainID, err := fe.getChainID(ctx)
	if err != nil {
		return &avsproto.EstimateFeesResp{
			Success:   false,
			Error:     fmt.Sprintf("Failed to detect chain ID: %v", err),
			ErrorCode: avsproto.ErrorCode_INVALID_REQUEST,
		}, nil
	}

	fe.logger.Info("🔍 Starting comprehensive fee estimation",
		"trigger_type", req.Trigger.Type.String(),
		"nodes_count", len(req.Nodes),
		"runner", req.Runner,
		"chain_id", chainID)

	// Step 1: Resolve runner address and detect smart wallet creation needs
	runnerAddress, smartWalletFee, err := fe.estimateSmartWalletCreation(ctx, req)
	if err != nil {
		return &avsproto.EstimateFeesResp{
			Success:   false,
			Error:     fmt.Sprintf("Failed to resolve runner address: %v", err),
			ErrorCode: avsproto.ErrorCode_SMART_WALLET_NOT_FOUND,
		}, nil
	}

	// Step 2: Estimate gas costs for all blockchain operations
	gasFees, err := fe.estimateGasFees(ctx, req, runnerAddress)
	if err != nil {
		return &avsproto.EstimateFeesResp{
			Success:   false,
			Error:     fmt.Sprintf("Failed to estimate gas fees: %v", err),
			ErrorCode: avsproto.ErrorCode_SIMULATION_ERROR,
		}, nil
	}

	// Step 3: Calculate automation fees based on trigger type and duration
	automationFee, err := fe.calculateAutomationFees(req)
	if err != nil {
		return &avsproto.EstimateFeesResp{
			Success:   false,
			Error:     fmt.Sprintf("Failed to calculate automation fees: %v", err),
			ErrorCode: avsproto.ErrorCode_INVALID_REQUEST,
		}, nil
	}

	// Step 4: Calculate total fees
	totalFees := fe.calculateTotalFees(gasFees, automationFee, smartWalletFee)

	// Step 5: Apply discounts and promotions
	discounts, totalDiscounts, finalTotal := fe.applyDiscounts(gasFees, automationFee, smartWalletFee, totalFees)

	// Step 6: Get price data metadata
	priceDataSource := "moralis"
	priceDataAge := int64(0)

	// Get price data age if available
	if moralisService, ok := fe.priceService.(*services.MoralisService); ok {
		priceDataAge = moralisService.GetPriceDataAge(chainID)
		priceDataSource = "moralis"
	} else {
		priceDataSource = "fallback"
	}

	fe.logger.Info("✅ Fee estimation completed successfully",
		"total_gas_cost_wei", totalFees.NativeTokenAmount,
		"total_usd", totalFees.UsdAmount,
		"final_total_usd", finalTotal.UsdAmount)

	return &avsproto.EstimateFeesResp{
		Success:             true,
		GasFees:             gasFees,
		AutomationFees:      automationFee,
		CreationFees:        smartWalletFee,
		TotalFees:           totalFees,
		Discounts:           discounts,
		TotalDiscounts:      totalDiscounts,
		FinalTotal:          finalTotal,
		EstimatedAt:         time.Now().UnixMilli(),
		ChainId:             fmt.Sprintf("%d", chainID),
		PriceDataSource:     priceDataSource,
		PriceDataAgeSeconds: priceDataAge,
		Warnings:            fe.generateWarnings(gasFees),
		Recommendations:     fe.generateRecommendations(req, gasFees, automationFee),
	}, nil
}

// estimateSmartWalletCreation determines if smart wallet creation is needed and estimates costs
func (fe *FeeEstimator) estimateSmartWalletCreation(ctx context.Context, req *avsproto.EstimateFeesReq) (common.Address, *avsproto.SmartWalletCreationFee, error) {
	var runnerAddress common.Address
	var err error

	// Try to get runner from request field first, then from input_variables
	if req.Runner != "" {
		if !common.IsHexAddress(req.Runner) {
			return common.Address{}, nil, fmt.Errorf("invalid runner address format: %s", req.Runner)
		}
		runnerAddress = common.HexToAddress(req.Runner)
	} else {
		// Try to extract from input_variables.workflowContext.runner
		if workflowCtx, ok := req.InputVariables["workflowContext"]; ok {
			if ctxMap, ok := workflowCtx.AsInterface().(map[string]interface{}); ok {
				if runner, ok := ctxMap["runner"].(string); ok && runner != "" {
					if !common.IsHexAddress(runner) {
						return common.Address{}, nil, fmt.Errorf("invalid runner address in workflowContext: %s", runner)
					}
					runnerAddress = common.HexToAddress(runner)
				}
			}
		}
	}

	// If no runner found, return error as we need it for gas estimation
	if (runnerAddress == common.Address{}) {
		return common.Address{}, nil, fmt.Errorf("runner address not found in request or input_variables")
	}

	// Check if smart wallet already exists
	code, err := fe.ethClient.CodeAt(ctx, runnerAddress, nil)
	if err != nil {
		fe.logger.Warn("Failed to check smart wallet deployment status", "address", runnerAddress.Hex(), "error", err)
		// Continue with assumption that wallet exists to avoid blocking fee estimation
		return runnerAddress, &avsproto.SmartWalletCreationFee{
			CreationRequired: false,
			WalletAddress:    runnerAddress.Hex(),
		}, nil
	}

	creationRequired := len(code) == 0
	smartWalletFee := &avsproto.SmartWalletCreationFee{
		CreationRequired: creationRequired,
		WalletAddress:    runnerAddress.Hex(),
	}

	if creationRequired {
		fe.logger.Info("Smart wallet creation required", "address", runnerAddress.Hex())

		// Estimate gas for smart wallet creation
		creationGas, gasPrice, err := fe.estimateWalletCreationGas(ctx, runnerAddress)
		if err != nil {
			fe.logger.Warn("Failed to estimate wallet creation gas", "error", err)
			// Use fallback values
			creationGas = big.NewInt(200000) // Conservative estimate
			gasPrice = big.NewInt(int64(DefaultGasPrice))
		}

		creationCostWei := new(big.Int).Mul(creationGas, gasPrice)
		creationFee, err := fe.convertToFeeAmount(creationCostWei)
		if err != nil {
			return common.Address{}, nil, fmt.Errorf("failed to convert creation fee: %w", err)
		}

		// Recommend initial funding (creation cost + buffer for first execution)
		fundingAmountWei := new(big.Int).Mul(creationCostWei, big.NewInt(2)) // 2x buffer
		initialFunding, err := fe.convertToFeeAmount(fundingAmountWei)
		if err != nil {
			return common.Address{}, nil, fmt.Errorf("failed to convert initial funding amount: %w", err)
		}

		smartWalletFee.CreationFee = creationFee
		smartWalletFee.InitialFunding = initialFunding
	} else {
		fe.logger.Info("Smart wallet already exists", "address", runnerAddress.Hex())

		// No creation needed, set zero fees
		zeroFee, _ := fe.convertToFeeAmount(big.NewInt(0))
		smartWalletFee.CreationFee = zeroFee
		smartWalletFee.InitialFunding = zeroFee
	}

	return runnerAddress, smartWalletFee, nil
}

// estimateWalletCreationGas estimates gas needed for smart wallet creation
func (fe *FeeEstimator) estimateWalletCreationGas(ctx context.Context, walletAddress common.Address) (*big.Int, *big.Int, error) {
	// TODO: Implement actual gas estimation for factory.createAccount()
	// For now, use conservative estimates

	// Get current gas price
	gasPrice, err := fe.ethClient.SuggestGasPrice(ctx)
	if err != nil {
		fe.logger.Warn("Failed to get current gas price", "error", err)
		gasPrice = big.NewInt(int64(DefaultGasPrice))
	}

	// Conservative estimate for smart wallet creation
	creationGas := big.NewInt(200000)

	return creationGas, gasPrice, nil
}

// estimateGasFees estimates gas costs for all blockchain operations in the workflow
func (fe *FeeEstimator) estimateGasFees(ctx context.Context, req *avsproto.EstimateFeesReq, runnerAddress common.Address) (*avsproto.GasFeeBreakdown, error) {
	fe.logger.Info("🔍 Estimating gas fees for workflow operations", "runner", runnerAddress.Hex())

	var operations []*avsproto.GasOperationFee
	totalGasUnits := big.NewInt(0)
	totalGasCostWei := big.NewInt(0)
	estimationAccurate := true
	estimationMethod := "rpc_estimate"

	// Get current gas price
	gasPrice, err := fe.ethClient.SuggestGasPrice(ctx)
	if err != nil {
		fe.logger.Warn("Failed to get current gas price, using fallback", "error", err)
		gasPrice = big.NewInt(int64(DefaultGasPrice))
		estimationAccurate = false
		estimationMethod = "fallback"
	}

	// Estimate gas for each node that requires blockchain operations
	for _, node := range req.Nodes {
		var gasResult *GasEstimationResult

		switch {
		case node.GetContractWrite() != nil:
			gasResult = fe.estimateContractWriteGas(ctx, node, runnerAddress, gasPrice)
		case node.GetEthTransfer() != nil:
			gasResult = fe.estimateETHTransferGas(ctx, node, runnerAddress, gasPrice)
		case node.GetLoop() != nil:
			// Handle loop nodes that might contain contract writes
			gasResult = fe.estimateLoopGas(ctx, node, runnerAddress, gasPrice)
		default:
			// Skip nodes that don't require gas
			continue
		}

		if gasResult != nil {
			if !gasResult.Success {
				estimationAccurate = false
				if gasResult.EstimationMethod != "rpc_estimate" {
					estimationMethod = gasResult.EstimationMethod
				}
			}

			// Convert to FeeAmount
			feeAmount, err := fe.convertToFeeAmount(gasResult.TotalCost)
			if err != nil {
				fe.logger.Warn("Failed to convert gas cost to fee amount", "error", err)
				continue
			}

			operation := &avsproto.GasOperationFee{
				OperationType: gasResult.OperationType,
				NodeId:        gasResult.NodeID,
				MethodName:    gasResult.MethodName,
				Fee:           feeAmount,
				GasUnits:      gasResult.GasUnits.String(),
			}

			operations = append(operations, operation)
			totalGasUnits.Add(totalGasUnits, gasResult.GasUnits)
			totalGasCostWei.Add(totalGasCostWei, gasResult.TotalCost)
		}
	}

	// Convert total gas cost to FeeAmount
	totalGasFees, err := fe.convertToFeeAmount(totalGasCostWei)
	if err != nil {
		return nil, fmt.Errorf("failed to convert total gas fees: %w", err)
	}

	// Convert gas price to Gwei for display
	gasPriceGwei := new(big.Float).Quo(new(big.Float).SetInt(gasPrice), big.NewFloat(1e9))

	return &avsproto.GasFeeBreakdown{
		TotalGasFees:       totalGasFees,
		Operations:         operations,
		GasPriceGwei:       fmt.Sprintf("%.2f", gasPriceGwei),
		TotalGasUnits:      totalGasUnits.String(),
		EstimationAccurate: estimationAccurate,
		EstimationMethod:   estimationMethod,
	}, nil
}

// Placeholder implementations for gas estimation methods
func (fe *FeeEstimator) estimateContractWriteGas(ctx context.Context, node *avsproto.TaskNode, runnerAddress common.Address, gasPrice *big.Int) *GasEstimationResult {
	// TODO: Implement RPC-first gas estimation with Tenderly fallback
	// For now, use conservative estimates

	contractWrite := node.GetContractWrite()
	if contractWrite == nil {
		return nil
	}

	estimatedGas := big.NewInt(150000) // Conservative estimate for contract write
	totalCost := new(big.Int).Mul(estimatedGas, gasPrice)

	methodName := "unknown"
	if len(contractWrite.Config.MethodCalls) > 0 {
		methodName = contractWrite.Config.MethodCalls[0].MethodName
	}

	return &GasEstimationResult{
		NodeID:           node.Id,
		OperationType:    "contract_write",
		MethodName:       methodName,
		GasUnits:         estimatedGas,
		GasPrice:         gasPrice,
		TotalCost:        totalCost,
		EstimationMethod: "fallback",
		Success:          true,
	}
}

func (fe *FeeEstimator) estimateETHTransferGas(ctx context.Context, node *avsproto.TaskNode, runnerAddress common.Address, gasPrice *big.Int) *GasEstimationResult {
	// ETH transfers through smart wallets typically use more gas than direct transfers
	estimatedGas := big.NewInt(50000) // Conservative estimate for smart wallet ETH transfer
	totalCost := new(big.Int).Mul(estimatedGas, gasPrice)

	return &GasEstimationResult{
		NodeID:           node.Id,
		OperationType:    "eth_transfer",
		MethodName:       "execute",
		GasUnits:         estimatedGas,
		GasPrice:         gasPrice,
		TotalCost:        totalCost,
		EstimationMethod: "fallback",
		Success:          true,
	}
}

func (fe *FeeEstimator) estimateLoopGas(ctx context.Context, node *avsproto.TaskNode, runnerAddress common.Address, gasPrice *big.Int) *GasEstimationResult {
	// TODO: Implement loop gas estimation based on loop contents
	// For now, use a multiplier of the base contract write cost
	estimatedGas := big.NewInt(300000) // Conservative estimate for loop operations
	totalCost := new(big.Int).Mul(estimatedGas, gasPrice)

	return &GasEstimationResult{
		NodeID:           node.Id,
		OperationType:    "contract_write",
		MethodName:       "loop_operations",
		GasUnits:         estimatedGas,
		GasPrice:         gasPrice,
		TotalCost:        totalCost,
		EstimationMethod: "fallback",
		Success:          true,
	}
}

// calculateAutomationFees calculates automation fees based on trigger type and duration
func (fe *FeeEstimator) calculateAutomationFees(req *avsproto.EstimateFeesReq) (*avsproto.AutomationFee, error) {
	trigger := req.Trigger
	triggerType := trigger.Type.String()

	// Calculate workflow duration in minutes
	durationMinutes := int64(0)
	if req.ExpireAt > req.CreatedAt {
		durationMinutes = (req.ExpireAt - req.CreatedAt) / (1000 * 60) // Convert milliseconds to minutes
	}

	// Estimate number of executions based on trigger type
	estimatedExecutions := fe.estimateExecutionCount(trigger, durationMinutes, req.MaxExecution)

	// Calculate fees
	baseFeeUSD := fe.automationRates.BaseFeeUSD
	monitoringFeeUSD := fe.calculateMonitoringFee(trigger, durationMinutes)
	executionFeeUSD := fe.calculateExecutionFee(trigger, estimatedExecutions)

	// Convert USD amounts to native token
	baseFee, err := fe.convertUSDToFeeAmount(baseFeeUSD)
	if err != nil {
		return nil, fmt.Errorf("failed to convert base fee: %w", err)
	}

	monitoringFee, err := fe.convertUSDToFeeAmount(monitoringFeeUSD)
	if err != nil {
		return nil, fmt.Errorf("failed to convert monitoring fee: %w", err)
	}

	executionFee, err := fe.convertUSDToFeeAmount(executionFeeUSD)
	if err != nil {
		return nil, fmt.Errorf("failed to convert execution fee: %w", err)
	}

	feeCalculationMethod := fe.buildFeeCalculationDescription(trigger, durationMinutes, estimatedExecutions)

	return &avsproto.AutomationFee{
		BaseFee:              baseFee,
		MonitoringFee:        monitoringFee,
		ExecutionFee:         executionFee,
		TriggerType:          triggerType,
		EstimatedExecutions:  estimatedExecutions,
		DurationMinutes:      durationMinutes,
		FeeCalculationMethod: feeCalculationMethod,
	}, nil
}

// Helper methods for automation fee calculation
func (fe *FeeEstimator) estimateExecutionCount(trigger *avsproto.TaskTrigger, durationMinutes, maxExecution int64) int64 {
	switch trigger.Type {
	case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
		if maxExecution > 0 {
			return maxExecution
		}
		return 1 // Default assumption for manual triggers

	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
		return 1 // Fixed time triggers execute once

	case avsproto.TriggerType_TRIGGER_TYPE_CRON:
		// TODO: Parse cron expression to calculate execution frequency
		// For now, assume every hour as default
		executions := durationMinutes / 60
		if maxExecution > 0 && executions > maxExecution {
			return maxExecution
		}
		return executions

	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		// TODO: Calculate based on block interval
		// For now, assume every 10 blocks (approximately every 2 minutes for 12s block time)
		executions := durationMinutes / 2
		if maxExecution > 0 && executions > maxExecution {
			return maxExecution
		}
		return executions

	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		// Event triggers are unpredictable, use conservative estimate
		// Assume 1 execution per day as default
		executions := durationMinutes / (24 * 60)
		if executions < 1 {
			executions = 1
		}
		if maxExecution > 0 && executions > maxExecution {
			return maxExecution
		}
		return executions

	default:
		return 1
	}
}

func (fe *FeeEstimator) calculateMonitoringFee(trigger *avsproto.TaskTrigger, durationMinutes int64) float64 {
	rates := fe.automationRates

	switch trigger.Type {
	case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
		return float64(durationMinutes) * rates.ManualMonitoringFeeUSDPerMinute

	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
		return float64(durationMinutes) * rates.FixedTimeMonitoringFeeUSDPerMinute

	case avsproto.TriggerType_TRIGGER_TYPE_CRON:
		return float64(durationMinutes) * rates.CronMonitoringFeeUSDPerMinute

	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		return float64(durationMinutes) * rates.BlockMonitoringFeeUSDPerMinute

	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		baseFee := float64(durationMinutes) * rates.EventMonitoringFeeUSDPerMinute

		// Add scaling fees based on addresses and topics
		if eventTrigger := trigger.GetEvent(); eventTrigger != nil {
			// Add fee per query configuration
			if config := eventTrigger.GetConfig(); config != nil {
				for _, query := range config.GetQueries() {
					// Add fee per address
					addressCount := len(query.GetAddresses())
					addressFee := float64(durationMinutes) * rates.EventAddressFeeUSDPerMinute * float64(addressCount)
					baseFee += addressFee

					// Add fee per topic group
					topicGroups := len(query.GetTopics())
					topicFee := float64(durationMinutes) * rates.EventTopicFeeUSDPerMinute * float64(topicGroups)
					baseFee += topicFee
				}
			}
		}

		return baseFee

	default:
		return 0.0
	}
}

func (fe *FeeEstimator) calculateExecutionFee(trigger *avsproto.TaskTrigger, estimatedExecutions int64) float64 {
	rates := fe.automationRates

	switch trigger.Type {
	case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
		return float64(estimatedExecutions) * rates.ManualExecutionFeeUSD

	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME, avsproto.TriggerType_TRIGGER_TYPE_CRON:
		return float64(estimatedExecutions) * rates.ScheduledExecutionFeeUSD

	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		return float64(estimatedExecutions) * rates.BlockExecutionFeeUSD

	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		return float64(estimatedExecutions) * rates.EventExecutionFeeUSD

	default:
		return 0.0
	}
}

func (fe *FeeEstimator) buildFeeCalculationDescription(trigger *avsproto.TaskTrigger, durationMinutes, estimatedExecutions int64) string {
	triggerType := strings.ToLower(trigger.Type.String()[13:]) // Remove "TRIGGER_TYPE_" prefix

	return fmt.Sprintf("Calculated for %s trigger over %d minutes with %d estimated executions",
		triggerType, durationMinutes, estimatedExecutions)
}

// calculateTotalFees sums all fee components
func (fe *FeeEstimator) calculateTotalFees(gasFees *avsproto.GasFeeBreakdown, automationFee *avsproto.AutomationFee, smartWalletFee *avsproto.SmartWalletCreationFee) *avsproto.FeeAmount {
	// Sum all fee components in wei
	totalWei := big.NewInt(0)

	// Add gas fees
	if gasFees != nil && gasFees.TotalGasFees != nil {
		if gasWei, ok := new(big.Int).SetString(gasFees.TotalGasFees.NativeTokenAmount, 10); ok {
			totalWei.Add(totalWei, gasWei)
		}
	}

	// Add automation fees
	if automationFee != nil {
		if automationFee.BaseFee != nil {
			if baseWei, ok := new(big.Int).SetString(automationFee.BaseFee.NativeTokenAmount, 10); ok {
				totalWei.Add(totalWei, baseWei)
			}
		}
		if automationFee.MonitoringFee != nil {
			if monitoringWei, ok := new(big.Int).SetString(automationFee.MonitoringFee.NativeTokenAmount, 10); ok {
				totalWei.Add(totalWei, monitoringWei)
			}
		}
		if automationFee.ExecutionFee != nil {
			if executionWei, ok := new(big.Int).SetString(automationFee.ExecutionFee.NativeTokenAmount, 10); ok {
				totalWei.Add(totalWei, executionWei)
			}
		}
	}

	// Add smart wallet creation fees
	if smartWalletFee != nil && smartWalletFee.CreationFee != nil {
		if creationWei, ok := new(big.Int).SetString(smartWalletFee.CreationFee.NativeTokenAmount, 10); ok {
			totalWei.Add(totalWei, creationWei)
		}
	}

	// Convert to FeeAmount
	totalFees, err := fe.convertToFeeAmount(totalWei)
	if err != nil {
		fe.logger.Warn("Failed to convert total fees", "error", err)
		// Return zero fees on error
		zeroFees, _ := fe.convertToFeeAmount(big.NewInt(0))
		return zeroFees
	}

	return totalFees
}

// applyDiscounts applies promotional discounts to the calculated fees
func (fe *FeeEstimator) applyDiscounts(gasFees *avsproto.GasFeeBreakdown, automationFee *avsproto.AutomationFee, smartWalletFee *avsproto.SmartWalletCreationFee, totalFees *avsproto.FeeAmount) ([]*avsproto.FeeDiscount, *avsproto.FeeAmount, *avsproto.FeeAmount) {
	var discounts []*avsproto.FeeDiscount
	totalDiscountUSD := 0.0

	// Apply beta program discount (100% off automation fees)
	if fe.discountRules.BetaProgramAutomationDiscount > 0 {
		automationDiscountUSD := 0.0
		if automationFee != nil {
			// Sum automation fee components
			if automationFee.BaseFee != nil {
				if baseUSD, err := parseFloat(automationFee.BaseFee.UsdAmount); err == nil {
					automationDiscountUSD += baseUSD
				}
			}
			if automationFee.MonitoringFee != nil {
				if monitoringUSD, err := parseFloat(automationFee.MonitoringFee.UsdAmount); err == nil {
					automationDiscountUSD += monitoringUSD
				}
			}
			if automationFee.ExecutionFee != nil {
				if executionUSD, err := parseFloat(automationFee.ExecutionFee.UsdAmount); err == nil {
					automationDiscountUSD += executionUSD
				}
			}
		}

		if automationDiscountUSD > 0 {
			discountAmount := automationDiscountUSD * fe.discountRules.BetaProgramAutomationDiscount
			totalDiscountUSD += discountAmount

			discountFeeAmount, err := fe.convertUSDToFeeAmount(discountAmount)
			if err == nil {
				discounts = append(discounts, &avsproto.FeeDiscount{
					DiscountType:       "beta_program",
					DiscountName:       "Beta Program Launch Discount",
					AppliesTo:          "automation_fees",
					DiscountPercentage: float32(fe.discountRules.BetaProgramAutomationDiscount * 100),
					DiscountAmount:     discountFeeAmount,
					ExpiryDate:         "2025-12-31T23:59:59Z", // TODO: Make configurable
					Terms:              "100% off automation fees during beta program",
				})
			}
		}
	}

	// Convert total discounts to FeeAmount
	totalDiscounts, err := fe.convertUSDToFeeAmount(totalDiscountUSD)
	if err != nil {
		fe.logger.Warn("Failed to convert total discounts", "error", err)
		totalDiscounts, _ = fe.convertToFeeAmount(big.NewInt(0))
	}

	// Calculate final total (total fees - discounts)
	totalFeesWei, _ := new(big.Int).SetString(totalFees.NativeTokenAmount, 10)
	totalDiscountsWei, _ := new(big.Int).SetString(totalDiscounts.NativeTokenAmount, 10)
	finalTotalWei := new(big.Int).Sub(totalFeesWei, totalDiscountsWei)

	// Ensure final total is not negative
	if finalTotalWei.Cmp(big.NewInt(0)) < 0 {
		finalTotalWei = big.NewInt(0)
	}

	finalTotal, err := fe.convertToFeeAmount(finalTotalWei)
	if err != nil {
		fe.logger.Warn("Failed to convert final total", "error", err)
		finalTotal = totalFees // Fallback to original total
	}

	return discounts, totalDiscounts, finalTotal
}

// Helper method to parse float from string
func parseFloat(s string) (float64, error) {
	// Remove any currency formatting and parse
	cleaned := strings.ReplaceAll(s, "$", "")
	cleaned = strings.ReplaceAll(cleaned, ",", "")
	var result float64
	n, err := fmt.Sscanf(cleaned, "%f", &result)
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, fmt.Errorf("unable to parse float from string: %s", s)
	}
	return result, nil
}

// generateWarnings generates warnings about fee estimation accuracy
func (fe *FeeEstimator) generateWarnings(gasFees *avsproto.GasFeeBreakdown) []string {
	var warnings []string

	if gasFees != nil && !gasFees.EstimationAccurate {
		warnings = append(warnings, "Gas estimation used fallback values due to RPC unavailability. Actual costs may vary.")
	}

	if gasFees != nil && gasFees.EstimationMethod == "fallback" {
		warnings = append(warnings, "Conservative gas estimates used. Consider testing with smaller amounts first.")
	}

	return warnings
}

// generateRecommendations generates cost optimization recommendations
func (fe *FeeEstimator) generateRecommendations(req *avsproto.EstimateFeesReq, gasFees *avsproto.GasFeeBreakdown, automationFee *avsproto.AutomationFee) []string {
	var recommendations []string

	// Check if workflow duration is very long
	if req.ExpireAt > req.CreatedAt {
		durationMinutes := (req.ExpireAt - req.CreatedAt) / (1000 * 60)
		if durationMinutes > 30*24*60 { // More than 30 days
			recommendations = append(recommendations, "Consider shorter workflow durations to reduce monitoring costs.")
		}
	}

	// Check for high execution count
	if automationFee != nil && automationFee.EstimatedExecutions > 1000 {
		recommendations = append(recommendations, "High execution count detected. Consider optimizing trigger conditions to reduce costs.")
	}

	// Check trigger type optimization
	if req.Trigger.Type == avsproto.TriggerType_TRIGGER_TYPE_EVENT {
		recommendations = append(recommendations, "Event triggers have higher monitoring costs. Consider manual triggers if suitable for your use case.")
	}

	return recommendations
}

// Utility methods for fee conversion
func (fe *FeeEstimator) convertToFeeAmount(weiAmount *big.Int) (*avsproto.FeeAmount, error) {
	// Use cached chain ID if available, otherwise detect it with proper synchronization
	fe.chainIDMu.RLock()
	chainID := fe.chainID
	fe.chainIDMu.RUnlock()

	if chainID == 0 {
		// Try to detect chain ID, but don't fail if context is not available
		// This is a fallback for cases where convertToFeeAmount is called without context
		chainIDResult, err := fe.ethClient.ChainID(context.Background())
		if err == nil {
			chainID = chainIDResult.Int64()
			// Cache for next time with proper synchronization
			fe.chainIDMu.Lock()
			if fe.chainID == 0 { // Double-check to avoid overwriting
				fe.chainID = chainID
			}
			fe.chainIDMu.Unlock()
		} else {
			// Use a reasonable default
			chainID = 1 // Ethereum mainnet as fallback
		}
	}

	nativeTokenSymbol := fe.priceService.GetNativeTokenSymbol(chainID)

	// Convert wei to USD
	nativeTokenPriceUSD, err := fe.priceService.GetNativeTokenPriceUSD(chainID)
	if err != nil {
		fe.logger.Warn("Failed to get native token price", "error", err)
		nativeTokenPriceUSD = big.NewFloat(2500) // Fallback price
	}

	// Convert wei to token amount (divide by 10^18)
	weiFloat := new(big.Float).SetInt(weiAmount)
	tokenAmount := new(big.Float).Quo(weiFloat, big.NewFloat(1e18))

	// Calculate USD amount
	usdAmount := new(big.Float).Mul(tokenAmount, nativeTokenPriceUSD)

	return &avsproto.FeeAmount{
		NativeTokenAmount: weiAmount.String(),
		NativeTokenSymbol: nativeTokenSymbol,
		UsdAmount:         fmt.Sprintf("%.6f", usdAmount),
		ApTokenAmount:     "0", // TODO: Implement AP token conversion
	}, nil
}

func (fe *FeeEstimator) convertUSDToFeeAmount(usdAmount float64) (*avsproto.FeeAmount, error) {
	// Use cached chain ID if available, otherwise detect it with proper synchronization
	fe.chainIDMu.RLock()
	chainID := fe.chainID
	fe.chainIDMu.RUnlock()

	if chainID == 0 {
		// Try to detect chain ID, but don't fail if context is not available
		chainIDResult, err := fe.ethClient.ChainID(context.Background())
		if err == nil {
			chainID = chainIDResult.Int64()
			// Cache for next time with proper synchronization
			fe.chainIDMu.Lock()
			if fe.chainID == 0 { // Double-check to avoid overwriting
				fe.chainID = chainID
			}
			fe.chainIDMu.Unlock()
		} else {
			// Use a reasonable default
			chainID = 1 // Ethereum mainnet as fallback
		}
	}

	nativeTokenSymbol := fe.priceService.GetNativeTokenSymbol(chainID)

	// Get native token price
	nativeTokenPriceUSD, err := fe.priceService.GetNativeTokenPriceUSD(chainID)
	if err != nil {
		fe.logger.Warn("Failed to get native token price", "error", err)
		nativeTokenPriceUSD = big.NewFloat(2500) // Fallback price
	}

	// Convert USD to token amount
	usdFloat := big.NewFloat(usdAmount)
	tokenAmount := new(big.Float).Quo(usdFloat, nativeTokenPriceUSD)

	// Convert to wei (multiply by 10^18)
	weiFloat := new(big.Float).Mul(tokenAmount, big.NewFloat(1e18))
	weiAmount, _ := weiFloat.Int(nil)

	return &avsproto.FeeAmount{
		NativeTokenAmount: weiAmount.String(),
		NativeTokenSymbol: nativeTokenSymbol,
		UsdAmount:         fmt.Sprintf("%.6f", usdAmount),
		ApTokenAmount:     "0", // TODO: Implement AP token conversion
	}, nil
}

// Default configuration
func getDefaultAutomationRates() *AutomationFeeRates {
	return &AutomationFeeRates{
		BaseFeeUSD: 0.0,

		ManualMonitoringFeeUSDPerMinute:    0.0,
		FixedTimeMonitoringFeeUSDPerMinute: 0.000017, // ~$0.01/day
		CronMonitoringFeeUSDPerMinute:      0.000033, // ~$0.02/day
		BlockMonitoringFeeUSDPerMinute:     0.000033, // ~$0.02/day
		EventMonitoringFeeUSDPerMinute:     0.000083, // ~$0.05/day base

		ManualExecutionFeeUSD:    0.0,
		ScheduledExecutionFeeUSD: 0.005,
		BlockExecutionFeeUSD:     0.01,
		EventExecutionFeeUSD:     0.01,

		EventAddressFeeUSDPerMinute: 0.000008, // ~$0.005/day per address
		EventTopicFeeUSDPerMinute:   0.000003, // ~$0.002/day per topic group
	}
}

func getDefaultDiscountRules() *DiscountRules {
	return &DiscountRules{
		BetaProgramAutomationDiscount: 1.0, // 100% off automation fees
		BetaProgramGasDiscount:        0.0, // 0% off gas fees

		NewUserAutomationDiscount: 0.8, // 80% off automation fees
		NewUserGasDiscount:        0.0, // 0% off gas fees

		VolumeDiscountThreshold:  10,  // 10+ workflows
		VolumeDiscountPercentage: 0.1, // 10% discount
	}
}
