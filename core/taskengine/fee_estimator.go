package taskengine

import (
	"context"
	"fmt"
	"math/big"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
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
	discountRules *DiscountRules // Planned: promotional discounts. Safe when nil/empty — no effect on totals.
}

// PriceService interface for getting token prices
type PriceService interface {
	GetNativeTokenPriceUSD(chainID int64) (*big.Float, error)
	GetNativeTokenSymbol(chainID int64) string
	// GetERC20PriceUSD returns the USD price for an ERC20 contract on the
	// given chain. Implementations should short-circuit hard-coded stablecoins
	// (LookupStablecoinSymbol) before hitting the network. Returns an error
	// when the price isn't available — callers render "$?" rather than
	// fabricate a number.
	GetERC20PriceUSD(chainID int64, contractAddress string) (*big.Float, error)
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

// DiscountRules defines promotional discount rules (planned, not yet applied).
// When all fields are zero the discount pass is a no-op and totals are unaffected.
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

// EstimateFees provides per-execution fee estimation for a workflow.
// Response: execution_fee (USD) + cogs[] (WEI) + value_fee (PERCENTAGE)
// No totals — client computes. All fees are per-execution.
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

	// Step 1: Resolve runner address for gas estimation
	runnerAddress, walletCreationNeeded, walletCreationGasWei, err := fe.resolveRunnerAndWalletCreation(ctx, req)
	if err != nil {
		return &avsproto.EstimateFeesResp{
			Success:   false,
			Error:     fmt.Sprintf("Failed to resolve runner address: %v", err),
			ErrorCode: avsproto.ErrorCode_SMART_WALLET_NOT_FOUND,
		}, nil
	}

	// Step 2: COGS — per-node operational costs
	cogs, err := fe.estimateCOGS(ctx, req, runnerAddress)
	if err != nil {
		return &avsproto.EstimateFeesResp{
			Success:   false,
			Error:     fmt.Sprintf("Failed to estimate COGS: %v", err),
			ErrorCode: avsproto.ErrorCode_SIMULATION_ERROR,
		}, nil
	}

	// Add wallet creation as a COGS entry if needed
	if walletCreationNeeded {
		cogs = append(cogs, &avsproto.NodeCOGS{
			NodeId:   "_wallet_creation",
			CostType: "wallet_creation",
			Fee:      &avsproto.Fee{Amount: walletCreationGasWei.String(), Unit: "WEI"},
		})
	}

	// Step 3: Value fee — workflow-level classification
	valueFee := fe.classifyWorkflowValue(req)

	nativeTokenSymbol := "ETH"
	if fe.priceService != nil {
		if sym := fe.priceService.GetNativeTokenSymbol(chainID); sym != "" {
			nativeTokenSymbol = sym
		}
	}

	fe.logger.Info("✅ Fee estimation completed",
		"execution_fee_usd", fe.feeRates.ExecutionFeeUSD,
		"cogs_count", len(cogs),
		"value_fee_tier", valueFee.Tier.String())

	return &avsproto.EstimateFeesResp{
		Success: true,
		ChainId: fmt.Sprintf("%d", chainID),
		NativeToken: &avsproto.NativeToken{
			Symbol:   nativeTokenSymbol,
			Decimals: 18,
		},
		ExecutionFee: &avsproto.Fee{
			Amount: fmt.Sprintf("%.6f", fe.feeRates.ExecutionFeeUSD),
			Unit:   "USD",
		},
		Cogs:         cogs,
		ValueFee:     valueFee,
		Discounts:    []*avsproto.FeeDiscount{},
		PricingModel: "v1",
		Warnings:     fe.generateWarnings(cogs),
	}, nil
}

// resolveRunnerAndWalletCreation resolves the runner address and checks if wallet creation is needed.
// Returns the runner address, whether creation is needed, and the estimated creation gas cost in wei.
func (fe *FeeEstimator) resolveRunnerAndWalletCreation(ctx context.Context, req *avsproto.EstimateFeesReq) (common.Address, bool, *big.Int, error) {
	var runnerAddress common.Address

	if req.Runner != "" {
		if !common.IsHexAddress(req.Runner) {
			return common.Address{}, false, nil, fmt.Errorf("invalid runner address format: %s", req.Runner)
		}
		runnerAddress = common.HexToAddress(req.Runner)
	} else {
		if settingsVal, ok := req.InputVariables["settings"]; ok {
			if settingsMap, ok := settingsVal.AsInterface().(map[string]interface{}); ok {
				if runner, ok := settingsMap["runner"].(string); ok && runner != "" {
					if !common.IsHexAddress(runner) {
						return common.Address{}, false, nil, fmt.Errorf("invalid runner address in settings: %s", runner)
					}
					runnerAddress = common.HexToAddress(runner)
				}
			}
		}
	}

	if (runnerAddress == common.Address{}) {
		return common.Address{}, false, nil, fmt.Errorf("runner address not found in request or input_variables")
	}

	// Check if smart wallet already exists.
	// On RPC failure, assume wallet exists to avoid blocking fee estimation during transient outages.
	code, err := fe.ethClient.CodeAt(ctx, runnerAddress, nil)
	if err != nil {
		fe.logger.Warn("Failed to check smart wallet deployment status, assuming wallet exists",
			"address", runnerAddress.Hex(), "error", err)
		return runnerAddress, false, nil, nil
	}

	if len(code) > 0 {
		return runnerAddress, false, nil, nil
	}

	// Wallet creation needed — estimate gas
	fe.logger.Info("Smart wallet creation required", "address", runnerAddress.Hex())
	creationGas, gasPrice, err := fe.estimateWalletCreationGas(ctx, runnerAddress)
	if err != nil {
		fe.logger.Warn("Failed to estimate wallet creation gas", "error", err)
		creationGas = big.NewInt(200000)
		gasPrice = big.NewInt(int64(DefaultGasPrice))
	}

	creationCostWei := new(big.Int).Mul(creationGas, gasPrice)
	return runnerAddress, true, creationCostWei, nil
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
// Gas for on-chain nodes (contract_write, eth_transfer) and loops whose inner
// runner is on-chain. Off-chain loops (contract_read, custom_code, etc.) are skipped.
// Future: external API costs for REST API nodes calling paid services.
func (fe *FeeEstimator) estimateCOGS(ctx context.Context, req *avsproto.EstimateFeesReq, runnerAddress common.Address) ([]*avsproto.NodeCOGS, error) {
	fe.logger.Info("🔍 Estimating COGS for workflow", "runner", runnerAddress.Hex())

	var cogsList []*avsproto.NodeCOGS

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
			continue
		}

		if gasResult != nil {
			cogsList = append(cogsList, &avsproto.NodeCOGS{
				NodeId:   gasResult.NodeID,
				CostType: "gas",
				Fee:      &avsproto.Fee{Amount: gasResult.TotalCost.String(), Unit: "WEI"},
				GasUnits: gasResult.GasUnits.String(),
			})
		}
	}

	return cogsList, nil
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

// estimateLoopGas estimates gas for a loop node based on its inner runner.
// Only on-chain runners (contract_write, eth_transfer) incur gas; off-chain runners are skipped.
func (fe *FeeEstimator) estimateLoopGas(ctx context.Context, node *avsproto.TaskNode, runnerAddress common.Address, gasPrice *big.Int) *GasEstimationResult {
	loopNode := node.GetLoop()
	if loopNode == nil {
		return nil
	}

	// Determine gas based on the inner runner type
	var perIterationGas int64
	var operationType string

	switch {
	case loopNode.GetContractWrite() != nil:
		perIterationGas = 150000
		operationType = "loop_contract_write"
	case loopNode.GetEthTransfer() != nil:
		perIterationGas = 50000
		operationType = "loop_eth_transfer"
	default:
		// Off-chain runner (contract_read, custom_code, rest_api, graphql) — no gas cost
		return nil
	}

	// Conservative estimate: assume multiple iterations
	estimatedGas := big.NewInt(perIterationGas)
	totalCost := new(big.Int).Mul(estimatedGas, gasPrice)

	return &GasEstimationResult{
		NodeID:           node.Id,
		OperationType:    operationType,
		MethodName:       "loop_iteration",
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
// hasOnChainNodes reports whether the workflow definition contains any node
// that performs on-chain execution. A Loop counts when its inner runner is
// itself an on-chain node (ETHTransfer or ContractWrite). Used by both
// classifyWorkflowValue (pre-flight EstimateFees) and buildValueFee
// (post-execution simulate/run) so the two paths cannot drift.
//
// Note: this only inspects the immediate Loop runner, not nested Loops. The
// current proto definition does not allow a Loop's runner to be another Loop,
// so this is sufficient today. If nesting becomes possible in the future, this
// function must recurse or it will silently under-charge nested-loop workflows.
func hasOnChainNodes(nodes []*avsproto.TaskNode) bool {
	for _, node := range nodes {
		switch {
		case node.GetEthTransfer() != nil, node.GetContractWrite() != nil:
			return true
		case node.GetLoop() != nil:
			loop := node.GetLoop()
			if loop.GetEthTransfer() != nil || loop.GetContractWrite() != nil {
				return true
			}
		}
	}
	return false
}

// V1: rule-based, defaults to Tier 1 if workflow has on-chain nodes, UNSPECIFIED otherwise.
// V2: LLM-based classification analyzing workflow purpose and node composition.
func (fe *FeeEstimator) classifyWorkflowValue(req *avsproto.EstimateFeesReq) *avsproto.ValueFee {
	if !hasOnChainNodes(req.Nodes) {
		return &avsproto.ValueFee{
			Fee:                  &avsproto.Fee{Amount: "0", Unit: "PERCENTAGE"},
			Tier:                 avsproto.ExecutionTier_EXECUTION_TIER_UNSPECIFIED,
			ValueBase:            "",
			ClassificationMethod: "rule_based",
			Confidence:           1.0,
			Reason:               "Workflow has no on-chain execution nodes — no value-capture fee",
		}
	}

	// V1: all workflows with on-chain nodes default to Tier 1
	// V2: LLM will analyze workflow purpose (e.g., AAVE repay → Tier 3)
	return &avsproto.ValueFee{
		Fee:                  &avsproto.Fee{Amount: fmt.Sprintf("%.2f", fe.feeRates.Tier1FeePercentage), Unit: "PERCENTAGE"},
		Tier:                 avsproto.ExecutionTier_EXECUTION_TIER_1,
		ValueBase:            "input_token_value",
		ClassificationMethod: "rule_based",
		Confidence:           1.0,
		Reason:               "V1 default: workflow contains on-chain execution nodes",
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

// ConvertUSDToWei converts a USD amount to Wei using the price service.
// Used by the billing system to convert execution_fee (USD) to native token at execution time.
func ConvertUSDToWei(usdAmount float64, priceService PriceService, chainID int64) (*big.Int, error) {
	ethPrice, err := priceService.GetNativeTokenPriceUSD(chainID)
	if err != nil {
		return nil, fmt.Errorf("failed to get native token price: %w", err)
	}

	if ethPrice == nil || ethPrice.Sign() <= 0 {
		return nil, fmt.Errorf("invalid native token price: must be greater than zero")
	}

	usdFloat := big.NewFloat(usdAmount)
	ethAmount := new(big.Float).Quo(usdFloat, ethPrice)
	weiMultiplier := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	weiFloat := new(big.Float).Mul(ethAmount, weiMultiplier)
	weiAmount, _ := weiFloat.Int(nil)

	return weiAmount, nil
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

// buildExecutionFee returns the execution fee from config (or default).
func buildExecutionFee(feeRatesConfig *config.FeeRatesConfig) *avsproto.Fee {
	rates := convertFeeRatesConfig(feeRatesConfig)
	return &avsproto.Fee{
		Amount: fmt.Sprintf("%.6f", rates.ExecutionFeeUSD),
		Unit:   "USD",
	}
}

// buildValueFee classifies the workflow and returns the value fee.
// Classification is based on the workflow definition (nodes), not on runtime
// gas presence — otherwise simulation runs (Tenderly, no real gas) would be
// classified differently from production runs of the same workflow.
func buildValueFee(nodes []*avsproto.TaskNode, feeRatesConfig *config.FeeRatesConfig) *avsproto.ValueFee {
	rates := convertFeeRatesConfig(feeRatesConfig)

	if !hasOnChainNodes(nodes) {
		return &avsproto.ValueFee{
			Fee:                  &avsproto.Fee{Amount: "0", Unit: "PERCENTAGE"},
			Tier:                 avsproto.ExecutionTier_EXECUTION_TIER_UNSPECIFIED,
			ClassificationMethod: "rule_based",
			Confidence:           1.0,
			Reason:               "Workflow has no on-chain execution nodes",
		}
	}

	return &avsproto.ValueFee{
		Fee:                  &avsproto.Fee{Amount: fmt.Sprintf("%.2f", rates.Tier1FeePercentage), Unit: "PERCENTAGE"},
		Tier:                 avsproto.ExecutionTier_EXECUTION_TIER_1,
		ValueBase:            "input_token_value",
		ClassificationMethod: "rule_based",
		Confidence:           1.0,
		Reason:               "V1 default: workflow contains on-chain execution nodes",
	}
}

// buildCOGSFromSteps converts per-step gas data from execution logs into NodeCOGS entries.
// This is used after execution to build the actual COGS from real gas receipts.
func buildCOGSFromSteps(steps []*avsproto.Execution_Step) []*avsproto.NodeCOGS {
	var cogsList []*avsproto.NodeCOGS

	for _, step := range steps {
		// Only on-chain steps have gas data
		if step.TotalGasCost == "" || step.TotalGasCost == "0" {
			continue
		}

		stepType := step.Type
		if stepType != avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String() &&
			stepType != avsproto.NodeType_NODE_TYPE_ETH_TRANSFER.String() &&
			stepType != avsproto.NodeType_NODE_TYPE_LOOP.String() {
			continue
		}

		cogsList = append(cogsList, &avsproto.NodeCOGS{
			NodeId:   step.Id,
			CostType: "gas",
			Fee:      &avsproto.Fee{Amount: step.TotalGasCost, Unit: "WEI"},
			GasUnits: step.GasUsed,
		})
	}

	return cogsList
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
