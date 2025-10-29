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

	agg.queue = apqueue.New(agg.db, agg.logger, &apqueue.QueueOption{
		Prefix: "default",
	})
	agg.worker = apqueue.NewWorker(agg.queue, agg.db)

	// Initialize global TokenEnrichmentService if not already set
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

	// Create executor with engine reference for atomic execution indexing
	taskExecutor := taskengine.NewExecutor(agg.config.SmartWallet, agg.db, agg.logger, agg.engine)
	taskengine.SetCache(agg.cache)
	macros.SetRpc(agg.config.SmartWallet.EthRpcUrl)

	if err := agg.worker.RegisterProcessor(
		taskengine.JobTypeExecuteTask,
		taskExecutor,
	); err != nil {
		agg.logger.Error("failed to register task processor", "error", err.Error())
	}
	if err := agg.engine.MustStart(); err != nil {
		agg.logger.Error("failed to start task engine", "error", err.Error())
	}

	queueErr := agg.queue.MustStart()
	if queueErr != nil {
		agg.logger.Error("failed to start task queue", "error", queueErr.Error())
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
