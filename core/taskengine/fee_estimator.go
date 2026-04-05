package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/services"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// FeeEstimator handles comprehensive fee estimation for workflows
type FeeEstimator struct {
	logger            sdklogging.Logger
	ethClient         *ethclient.Client
	tenderlyClient    *TenderlyClient
	smartWalletConfig *config.SmartWalletConfig
	chainID           int64 // Cached after first detection

	// Price service for USD conversion
	priceService PriceService

	// Node-tier fee configuration
	feeRates      *FeeRates
	discountRules *DiscountRules
}

// PriceService interface for getting token prices
type PriceService interface {
	GetNativeTokenPriceUSD(chainID int64) (*big.Float, error)
	GetNativeTokenSymbol(chainID int64) string
}

// FeeRates holds the fee configuration for the estimator.
// Three components: execution_fee (flat) + COGS (per-node) + value_fee (workflow-level tier %).
type FeeRates struct {
	// Flat per-execution platform fee
	ExecutionFeeUSD float64 // Default: $0.02

	// Value-capture tier percentages (applied at workflow level, not per-node)
	// Decision rule: failure → loss → Tier 3 | improves outcome → Tier 2 | simple → Tier 1
	Tier1FeePercentage float64 // Default: 0.03%
	Tier2FeePercentage float64 // Default: 0.09%
	Tier3FeePercentage float64 // Default: 0.18%
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
		feeRates:          getDefaultFeeRates(),
		discountRules:     getDefaultDiscountRules(),
	}
}

// NewFeeEstimatorWithConfig creates a new fee estimator instance with configurable rates
// Chain ID will be detected automatically from the eth client
func NewFeeEstimatorWithConfig(
	logger sdklogging.Logger,
	ethClient *ethclient.Client,
	tenderlyClient *TenderlyClient,
	smartWalletConfig *config.SmartWalletConfig,
	priceService PriceService,
	feeRatesConfig *config.FeeRatesConfig,
) *FeeEstimator {
	return &FeeEstimator{
		logger:            logger,
		ethClient:         ethClient,
		tenderlyClient:    tenderlyClient,
		smartWalletConfig: smartWalletConfig,
		chainID:           0, // Will be detected on first use
		priceService:      priceService,
		feeRates:          convertFeeRatesConfig(feeRatesConfig),
		discountRules:     getDefaultDiscountRules(),
	}
}

// getChainID detects and caches the chain ID from the eth client
func (fe *FeeEstimator) getChainID(ctx context.Context) (int64, error) {
	// Return cached value if already detected
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

// EstimateFees provides comprehensive fee estimation for a workflow.
// Response structure: execution_fee + cogs[] + value_fee = total
func (fe *FeeEstimator) EstimateFees(ctx context.Context, req *avsproto.EstimateFeesReq) (*avsproto.EstimateFeesResp, error) {
	chainID, err := fe.getChainID(ctx)
	if err != nil {
		return &avsproto.EstimateFeesResp{
			Success:   false,
			Error:     fmt.Sprintf("Failed to detect chain ID: %v", err),
			ErrorCode: avsproto.ErrorCode_INVALID_REQUEST,
		}, nil
	}

	fe.logger.Info("🔍 Starting fee estimation",
		"trigger_type", req.Trigger.Type.String(),
		"nodes_count", len(req.Nodes),
		"runner", req.Runner,
		"chain_id", chainID)

	// Step 1: Smart wallet creation check
	runnerAddress, creationFees, err := fe.estimateSmartWalletCreation(ctx, req)
	if err != nil {
		return &avsproto.EstimateFeesResp{
			Success:   false,
			Error:     fmt.Sprintf("Failed to resolve runner address: %v", err),
			ErrorCode: avsproto.ErrorCode_SMART_WALLET_NOT_FOUND,
		}, nil
	}

	// Step 2: Flat per-execution platform fee
	executionFee, _ := fe.convertUSDToFeeAmount(fe.feeRates.ExecutionFeeUSD)

	// Step 3: COGS — per-node operational costs (gas for on-chain, future: API costs)
	cogs, totalCogsWei, err := fe.estimateCOGS(ctx, req, runnerAddress)
	if err != nil {
		return &avsproto.EstimateFeesResp{
			Success:   false,
			Error:     fmt.Sprintf("Failed to estimate COGS: %v", err),
			ErrorCode: avsproto.ErrorCode_SIMULATION_ERROR,
		}, nil
	}

	// Step 4: Value fee — workflow-level classification (V1: rule-based)
	durationMinutes := int64(0)
	if req.ExpireAt > req.CreatedAt {
		durationMinutes = (req.ExpireAt - req.CreatedAt) / (1000 * 60)
	}
	estimatedExecutions := fe.estimateExecutionCount(req.Trigger, durationMinutes, req.MaxExecution)
	valueFee := fe.classifyWorkflowValue(req)

	// Step 5: Calculate total (execution_fee + COGS + creation; value_fee is % at execution time)
	totalWei := new(big.Int).Set(totalCogsWei)
	if execWei, ok := new(big.Int).SetString(executionFee.NativeTokenAmount, 10); ok {
		totalWei.Add(totalWei, execWei)
	}
	if creationFees != nil && creationFees.CreationFee != nil {
		if createWei, ok := new(big.Int).SetString(creationFees.CreationFee.NativeTokenAmount, 10); ok {
			totalWei.Add(totalWei, createWei)
		}
	}
	totalFees, _ := fe.convertToFeeAmount(totalWei)
	zeroFee, _ := fe.convertUSDToFeeAmount(0)

	// Step 6: Price data metadata
	priceDataSource := "moralis"
	priceDataAge := int64(0)
	if moralisService, ok := fe.priceService.(*services.MoralisService); ok {
		priceDataAge = moralisService.GetPriceDataAge(chainID)
	} else {
		priceDataSource = "fallback"
	}

	fe.logger.Info("✅ Fee estimation completed",
		"execution_fee_usd", executionFee.UsdAmount,
		"cogs_count", len(cogs),
		"value_fee_tier", valueFee.Tier.String(),
		"total_usd", totalFees.UsdAmount)

	return &avsproto.EstimateFeesResp{
		Success:             true,
		ExecutionFee:        executionFee,
		Cogs:                cogs,
		ValueFee:            valueFee,
		CreationFees:        creationFees,
		TotalFees:           totalFees,
		Discounts:           []*avsproto.FeeDiscount{}, // TODO: re-implement discounts for new structure
		TotalDiscounts:      zeroFee,
		FinalTotal:          totalFees, // No discounts applied yet
		EstimatedExecutions: estimatedExecutions,
		EstimatedAt:         time.Now().UnixMilli(),
		ChainId:             fmt.Sprintf("%d", chainID),
		PriceDataSource:     priceDataSource,
		PriceDataAgeSeconds: priceDataAge,
		Warnings:            fe.generateWarnings(cogs),
		Recommendations:     nil,
		PricingModel:        "v1",
	}, nil
}

// estimateSmartWalletCreation determines if smart wallet creation is needed and estimates costs
func (fe *FeeEstimator) estimateSmartWalletCreation(ctx context.Context, req *avsproto.EstimateFeesReq) (common.Address, *avsproto.SmartWalletCreationFee, error) {
	var runnerAddress common.Address

	// Try to get runner from request field first, then from input_variables
	if req.Runner != "" {
		if !common.IsHexAddress(req.Runner) {
			return common.Address{}, nil, fmt.Errorf("invalid runner address format: %s", req.Runner)
		}
		runnerAddress = common.HexToAddress(req.Runner)
	} else {
		// Try to extract from input_variables.settings.runner
		if settingsVal, ok := req.InputVariables["settings"]; ok {
			if settingsMap, ok := settingsVal.AsInterface().(map[string]interface{}); ok {
				if runner, ok := settingsMap["runner"].(string); ok && runner != "" {
					if !common.IsHexAddress(runner) {
						return common.Address{}, nil, fmt.Errorf("invalid runner address in settings: %s", runner)
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
// by calling eth_estimateGas against the factory's createAccount method.
func (fe *FeeEstimator) estimateWalletCreationGas(ctx context.Context, walletAddress common.Address) (*big.Int, *big.Int, error) {
	const fallbackCreationGas = int64(500000) // Conservative fallback for ERC-6900 wallet deployment

	// Get current gas price
	gasPrice, err := fe.ethClient.SuggestGasPrice(ctx)
	if err != nil {
		fe.logger.Warn("Failed to get current gas price for wallet creation", "error", err)
		gasPrice = big.NewInt(int64(DefaultGasPrice))
	}

	// Build createAccount calldata using the factory ABI.
	factoryAddress := fe.smartWalletConfig.FactoryAddress
	initCodeHex, err := aa.GetInitCodeForFactory(walletAddress.Hex(), factoryAddress, big.NewInt(0))
	if err != nil {
		fe.logger.Warn("Failed to build createAccount calldata, using fallback gas estimate",
			"error", err, "fallback_gas", fallbackCreationGas)
		return big.NewInt(fallbackCreationGas), gasPrice, nil
	}

	// initCode = factoryAddress (20 bytes) + calldata, so strip the factory address prefix
	initCodeBytes := common.FromHex(initCodeHex)
	if len(initCodeBytes) <= 20 {
		fe.logger.Warn("InitCode too short, using fallback gas estimate", "fallback_gas", fallbackCreationGas)
		return big.NewInt(fallbackCreationGas), gasPrice, nil
	}
	calldata := initCodeBytes[20:]

	// Estimate gas via eth_estimateGas against the factory contract
	estimatedGas, err := fe.ethClient.EstimateGas(ctx, ethereum.CallMsg{
		To:   &factoryAddress,
		Data: calldata,
	})
	if err != nil {
		fe.logger.Warn("eth_estimateGas for factory.createAccount failed, using fallback",
			"error", err, "factory", factoryAddress.Hex(), "fallback_gas", fallbackCreationGas)
		return big.NewInt(fallbackCreationGas), gasPrice, nil
	}

	// Add 20% buffer to account for estimation variance
	bufferedGas := new(big.Int).SetUint64(estimatedGas)
	buffer := new(big.Int).Div(bufferedGas, big.NewInt(5)) // 20%
	bufferedGas.Add(bufferedGas, buffer)

	fe.logger.Info("Estimated wallet creation gas via eth_estimateGas",
		"raw_gas", estimatedGas, "buffered_gas", bufferedGas, "factory", factoryAddress.Hex())

	return bufferedGas, gasPrice, nil
}

// estimateCOGS estimates cost of goods sold — per-node operational costs.
// Gas for on-chain nodes (contract_write, eth_transfer, loop).
// Future: external API costs for REST API nodes calling paid services.
func (fe *FeeEstimator) estimateCOGS(ctx context.Context, req *avsproto.EstimateFeesReq, runnerAddress common.Address) ([]*avsproto.NodeCOGS, *big.Int, error) {
	fe.logger.Info("🔍 Estimating COGS for workflow", "runner", runnerAddress.Hex())

	var cogsList []*avsproto.NodeCOGS
	totalCostWei := big.NewInt(0)

	// Get current gas price
	gasPrice, err := fe.ethClient.SuggestGasPrice(ctx)
	if err != nil {
		fe.logger.Warn("Failed to get current gas price, using fallback", "error", err)
		gasPrice = big.NewInt(int64(DefaultGasPrice))
	}

	for _, node := range req.Nodes {
		var gasResult *GasEstimationResult

		switch {
		case node.GetContractWrite() != nil:
			gasResult = fe.estimateContractWriteGas(ctx, node, runnerAddress, gasPrice)
		case node.GetEthTransfer() != nil:
			gasResult = fe.estimateETHTransferGas(ctx, node, runnerAddress, gasPrice)
		case node.GetLoop() != nil:
			gasResult = fe.estimateLoopGas(ctx, node, runnerAddress, gasPrice)
		default:
			// Non on-chain nodes have no COGS (in V1)
			// Future: estimate external API costs for rest_api nodes
			continue
		}

		if gasResult != nil {
			cost, err := fe.convertToFeeAmount(gasResult.TotalCost)
			if err != nil {
				fe.logger.Warn("Failed to convert COGS", "error", err)
				continue
			}

			cogsList = append(cogsList, &avsproto.NodeCOGS{
				NodeId:   gasResult.NodeID,
				NodeName: node.Name,
				CostType: "gas",
				Cost:     cost,
				GasUnits: gasResult.GasUnits.String(),
			})
			totalCostWei.Add(totalCostWei, gasResult.TotalCost)
		}
	}

	return cogsList, totalCostWei, nil
}

// Gas estimation methods for individual node types
func (fe *FeeEstimator) estimateContractWriteGas(ctx context.Context, node *avsproto.TaskNode, runnerAddress common.Address, gasPrice *big.Int) *GasEstimationResult {
	contractWrite := node.GetContractWrite()
	if contractWrite == nil {
		return nil
	}

	estimatedGas := big.NewInt(150000) // Conservative estimate for contract write
	totalCost := new(big.Int).Mul(estimatedGas, gasPrice)

	methodName := "unknown"
	if contractWrite.Config != nil && len(contractWrite.Config.MethodCalls) > 0 {
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

// classifyWorkflowValue determines the value-capture tier for the entire workflow.
// This is a single workflow-level classification, not per-node.
//
// Decision rule: "If this workflow fails or is delayed, does the user lose money immediately?"
//   - YES → Tier 3
//   - NO but improves outcome → Tier 2
//   - Simple execution → Tier 1
//
// V1: rule-based, defaults to Tier 1 if workflow has on-chain nodes, UNSPECIFIED otherwise.
// V2: LLM-based classification analyzing workflow purpose and node composition.
func (fe *FeeEstimator) classifyWorkflowValue(req *avsproto.EstimateFeesReq) *avsproto.ValueFee {
	hasOnChainNodes := false
	for _, node := range req.Nodes {
		if isOnChainNode(node) {
			hasOnChainNodes = true
			break
		}
	}

	if !hasOnChainNodes {
		return &avsproto.ValueFee{
			Tier:                 avsproto.ExecutionTier_EXECUTION_TIER_UNSPECIFIED,
			FeePercentage:        0,
			ClassificationMethod: "rule_based",
			Confidence:           1.0,
			Reason:               "Workflow has no on-chain execution nodes — no value-capture fee",
		}
	}

	// V1: all workflows with on-chain nodes default to Tier 1
	// V2: LLM will analyze workflow purpose (e.g., AAVE repay → Tier 3)
	return &avsproto.ValueFee{
		Tier:                 avsproto.ExecutionTier_EXECUTION_TIER_1,
		FeePercentage:        float32(fe.feeRates.Tier1FeePercentage),
		ClassificationMethod: "rule_based",
		Confidence:           1.0,
		Reason:               "V1 default: workflow contains on-chain execution nodes",
	}
}

// isOnChainNode returns true if the node performs on-chain transactions
func isOnChainNode(node *avsproto.TaskNode) bool {
	return node.GetEthTransfer() != nil ||
		node.GetContractWrite() != nil ||
		node.GetLoop() != nil
}

// estimateExecutionCount estimates how many times the workflow will execute
func (fe *FeeEstimator) estimateExecutionCount(trigger *avsproto.TaskTrigger, durationMinutes, maxExecution int64) int64 {
	switch trigger.Type {
	case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
		if maxExecution > 0 {
			return maxExecution
		}
		return 1

	case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
		return 1

	case avsproto.TriggerType_TRIGGER_TYPE_CRON:
		// TODO: Parse cron expression to calculate execution frequency
		executions := durationMinutes / 60
		if maxExecution > 0 && executions > maxExecution {
			return maxExecution
		}
		return executions

	case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
		// Assume every 10 blocks (~2 minutes for 12s block time)
		executions := durationMinutes / 2
		if maxExecution > 0 && executions > maxExecution {
			return maxExecution
		}
		return executions

	case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
		// Conservative: 1 execution per day
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

// generateWarnings generates warnings about fee estimation accuracy
func (fe *FeeEstimator) generateWarnings(cogs []*avsproto.NodeCOGS) []string {
	var warnings []string

	if len(cogs) > 0 {
		warnings = append(warnings, "Gas estimates use conservative fallback values. Actual costs may vary.")
	}

	return warnings
}

// Utility methods for fee conversion
func (fe *FeeEstimator) convertToFeeAmount(weiAmount *big.Int) (*avsproto.FeeAmount, error) {
	chainID := fe.chainID
	if chainID == 0 {
		chainIDResult, err := fe.ethClient.ChainID(context.Background())
		if err == nil {
			chainID = chainIDResult.Int64()
			fe.chainID = chainID
		} else {
			chainID = 1 // Ethereum mainnet as fallback
		}
	}

	nativeTokenSymbol := fe.priceService.GetNativeTokenSymbol(chainID)

	nativeTokenPriceUSD, err := fe.priceService.GetNativeTokenPriceUSD(chainID)
	if err != nil {
		fe.logger.Warn("Failed to get native token price", "error", err)
		nativeTokenPriceUSD = big.NewFloat(2500)
	}

	weiFloat := new(big.Float).SetInt(weiAmount)
	tokenAmount := new(big.Float).Quo(weiFloat, big.NewFloat(1e18))
	usdAmount := new(big.Float).Mul(tokenAmount, nativeTokenPriceUSD)

	return &avsproto.FeeAmount{
		NativeTokenAmount: weiAmount.String(),
		NativeTokenSymbol: nativeTokenSymbol,
		UsdAmount:         fmt.Sprintf("%.6f", usdAmount),
		ApTokenAmount:     "0",
	}, nil
}

func (fe *FeeEstimator) convertUSDToFeeAmount(usdAmount float64) (*avsproto.FeeAmount, error) {
	chainID := fe.chainID
	if chainID == 0 {
		chainIDResult, err := fe.ethClient.ChainID(context.Background())
		if err == nil {
			chainID = chainIDResult.Int64()
			fe.chainID = chainID
		} else {
			chainID = 1
		}
	}

	nativeTokenSymbol := fe.priceService.GetNativeTokenSymbol(chainID)

	nativeTokenPriceUSD, err := fe.priceService.GetNativeTokenPriceUSD(chainID)
	if err != nil {
		fe.logger.Warn("Failed to get native token price", "error", err)
		nativeTokenPriceUSD = big.NewFloat(2500)
	}

	usdFloat := big.NewFloat(usdAmount)
	tokenAmount := new(big.Float).Quo(usdFloat, nativeTokenPriceUSD)
	weiFloat := new(big.Float).Mul(tokenAmount, big.NewFloat(1e18))
	weiAmount, _ := weiFloat.Int(nil)

	return &avsproto.FeeAmount{
		NativeTokenAmount: weiAmount.String(),
		NativeTokenSymbol: nativeTokenSymbol,
		UsdAmount:         fmt.Sprintf("%.6f", usdAmount),
		ApTokenAmount:     "0",
	}, nil
}

// Default configuration
func getDefaultFeeRates() *FeeRates {
	return &FeeRates{
		ExecutionFeeUSD:    0.02,
		Tier1FeePercentage: 0.03,
		Tier2FeePercentage: 0.09,
		Tier3FeePercentage: 0.18,
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

// convertFeeRatesConfig converts configuration-based fee rates to internal tier rates
func convertFeeRatesConfig(configRates *config.FeeRatesConfig) *FeeRates {
	if configRates == nil {
		return getDefaultFeeRates()
	}

	return &FeeRates{
		ExecutionFeeUSD:    configRates.ExecutionFeeUSD,
		Tier1FeePercentage: configRates.Tier1FeePercentage,
		Tier2FeePercentage: configRates.Tier2FeePercentage,
		Tier3FeePercentage: configRates.Tier3FeePercentage,
	}
}
