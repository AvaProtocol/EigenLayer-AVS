package aggregator

/*
TASK ENGINE ARCHITECTURE DOCUMENTATION

This document clarifies the architecture of Triggers and Nodes in the task engine system.

1. TRIGGERS vs NODES:
   - Triggers are always the FIRST element in a task execution flow
   - Nodes are subsequent elements that process data from preceding triggers/nodes
   - Triggers do NOT have preceding nodes (they are entry points)
   - Nodes CAN have preceding nodes and receive their output as input

2. CONFIGURATION vs INPUT:
   - Config: Static parameters provided at creation time, cannot be changed afterwards
   - Input: Runtime variables passed from preceding nodes or via runNodeWithInputs RPC

3. TRIGGER ARCHITECTURE:
   - Triggers have Config (static parameters set at creation)
   - Triggers do NOT take Input (since they have no preceding nodes)
   - Example: FixedTimeTrigger.Config.epochs = [1000, 2000, 3000] (set at creation)

4. NODE ARCHITECTURE:
   - Nodes have Config (static parameters set at creation)
   - Nodes take Input (runtime variables from preceding nodes or runNodeWithInputs)
   - Example: CustomCodeNode.Config.lang = "JavaScript" (set at creation)
   - Example: CustomCodeNode receives Input variables from preceding RestAPINode output

5. RUNTIME EXECUTION:
   - Normal execution: Triggers fire → pass output to next node → node processes with its config + input
   - Testing execution: runNodeWithInputs RPC allows testing any trigger/node by providing input variables directly

6. NAMING CONVENTIONS:
   - Triggers: *Trigger (e.g., FixedTimeTrigger, BlockTrigger, EventTrigger)
   - Nodes: *Node (e.g., CustomCodeNode, RestAPINode, ContractReadNode)
   - Config: Static configuration set at creation time
   - Input: Runtime variables (from preceding nodes or runNodeWithInputs)
   - Output: Result data passed to next node in the chain

This architecture ensures clear separation between static configuration and dynamic runtime data.
*/

import (
	"context"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/apqueue"
	"github.com/AvaProtocol/EigenLayer-AVS/core/services"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/ethclient"
)

func (agg *Aggregator) stopTaskEngine() {
	agg.logger.Infof("Stopping task engine")
	agg.engine.Stop()
}

func (agg *Aggregator) startTaskEngine(ctx context.Context) {
	agg.logger.Info("Start execution engine",
		"bundler", agg.config.SmartWallet.BundlerURL,
		"factory", agg.config.SmartWallet.FactoryAddress,
		"entrypoint", agg.config.SmartWallet.EntrypointAddress,
		"paymaster", agg.config.SmartWallet.PaymasterAddress,
	)

	// ChainIDs lets the cleanup loop locate tasks across every chain bucket
	// in chain-scoped storage. Single-chain aggregator: just its own chain.
	queueChainIDs := []int64{}
	if agg.chainID != nil {
		queueChainIDs = append(queueChainIDs, agg.chainID.Int64())
	}
	if agg.config.SmartWallet != nil && agg.config.SmartWallet.ChainID > 0 {
		swChainID := agg.config.SmartWallet.ChainID
		dup := false
		for _, id := range queueChainIDs {
			if id == swChainID {
				dup = true
				break
			}
		}
		if !dup {
			queueChainIDs = append(queueChainIDs, swChainID)
		}
	}
	agg.queue = apqueue.New(agg.db, agg.logger, &apqueue.QueueOption{
		Prefix:   "default",
		ChainIDs: queueChainIDs,
	})
	agg.worker = apqueue.NewWorker(agg.queue, agg.db)

	// Gateway mode: dial one *ethclient.Client per Chains[] entry. The
	// connection is shared between the TokenEnrichmentService (whose RPC
	// fallback wants the right chain's contracts — see "0.1 USDC" vs
	// "0.0000000000001 UNKNOWN") and the REST fee/wallet handlers (whose
	// per-request chainId now routes through smartWalletRpcByChain instead
	// of always landing on agg.smartWalletRpc — which the gateway-mode
	// fallback in config.go binds to chains[0] = mainnet).
	if agg.config.IsGateway {
		agg.smartWalletRpcByChain = make(map[int64]*ethclient.Client, len(agg.config.Chains))
		for _, chain := range agg.config.Chains {
			if chain.SmartWallet == nil || chain.SmartWallet.EthRpcUrl == "" {
				continue
			}
			chainRpc, err := ethclient.Dial(chain.SmartWallet.EthRpcUrl)
			if err != nil {
				agg.logger.Warn("Failed to dial chain RPC",
					"chain", chain.Name, "chain_id", chain.ChainID, "error", err)
				continue
			}
			agg.smartWalletRpcByChain[chain.ChainID] = chainRpc

			chainTokenService, err := taskengine.NewTokenEnrichmentService(chainRpc, agg.logger)
			if err != nil {
				agg.logger.Warn("Failed to initialize chain TokenEnrichmentService",
					"chain", chain.Name, "chain_id", chain.ChainID, "error", err)
				continue
			}
			taskengine.RegisterTokenEnrichmentService(chainTokenService)
			agg.logger.Info("Chain RPC + TokenEnrichmentService registered",
				"chain", chain.Name,
				"chainID", chainTokenService.GetChainID(),
				"whitelistTokens", chainTokenService.GetCacheSize())
		}
	}

	// Initialize global TokenEnrichmentService (used as the engine's default
	// when callers don't pass a chain ID — single-chain mode, or gateway mode
	// for legacy code paths that haven't been migrated yet). In gateway mode
	// this lands on chains[0] because config.go fills SmartWallet.EthRpcUrl
	// from Chains[0].SmartWallet.
	if taskengine.GetTokenEnrichmentService() == nil && agg.config.SmartWallet.EthRpcUrl != "" {
		rpcClient, err := ethclient.Dial(agg.config.SmartWallet.EthRpcUrl)
		if err != nil {
			agg.logger.Warn("Failed to connect to RPC for TokenEnrichmentService", "error", err)
		} else {
			tokenService, err := taskengine.NewTokenEnrichmentService(rpcClient, agg.logger)
			if err != nil {
				agg.logger.Warn("Failed to initialize TokenEnrichmentService", "error", err)
			} else {
				taskengine.SetTokenEnrichmentService(tokenService)
				agg.logger.Info("Global TokenEnrichmentService initialized",
					"chainID", tokenService.GetChainID(),
					"cacheSize", tokenService.GetCacheSize())
			}
		}
	}

	// Create engine first so it can be passed to executor
	// Note: Engine.New() automatically initializes MacroVars and MacroSecrets from config
	agg.engine = taskengine.New(
		agg.db,
		agg.config,
		agg.queue,
		agg.logger,
	)

	// Price service for fee conversion (USD → ETH and ERC20 lookups). When
	// Moralis isn't configured, it stays nil and callers gracefully degrade
	// (notifications render "$?" for unknown prices).
	var priceService taskengine.PriceService
	if agg.config.MoralisApiKey != "" {
		priceService = services.GetMoralisService(agg.config.MoralisApiKey, agg.logger)
	} else {
		agg.logger.Warn("No Moralis API key configured; USD-equivalent fee numbers will be unavailable")
	}

	// Store price service on engine (nil-safe — engine and summarizer handle absence).
	agg.engine.SetPriceService(priceService)
	// Also expose it to the REST layer (estimateFees handler).
	agg.priceService = priceService

	// Create executor with engine reference for atomic execution indexing
	taskExecutor := taskengine.NewExecutor(agg.config.SmartWallet, agg.db, agg.logger, agg.engine, priceService)
	taskengine.SetCache(agg.cache)
	macros.SetRpc(agg.config.SmartWallet.EthRpcUrl)

	if err := agg.worker.RegisterProcessor(
		taskengine.JobTypeExecuteTask,
		taskExecutor,
	); err != nil {
		agg.logger.Error("failed to register task processor", "error", err)
	}
	if err := agg.engine.MustStart(); err != nil {
		agg.logger.Error("failed to start task engine", "error", err)
	}

	queueErr := agg.queue.MustStart()
	if queueErr != nil {
		agg.logger.Error("failed to start task queue", "error", queueErr)
	}

	// Start periodic cleanup with environment-specific intervals
	cleanupInterval := time.Hour // Default: 1 hour for production
	if agg.config.Environment == sdklogging.Development {
		cleanupInterval = 5 * time.Minute // 5 minutes for development
		agg.logger.Info("scheduled periodic queue cleanup every 5 minutes (development mode)")
	} else {
		agg.logger.Info("scheduled periodic queue cleanup every hour (production mode)")
	}
	agg.queue.SchedulePeriodicCleanup(cleanupInterval)

	agg.worker.MustStart()
}
