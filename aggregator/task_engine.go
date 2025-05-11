package aggregator

import (
	"context"

	"github.com/AvaProtocol/EigenLayer-AVS/core/apqueue"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
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
	)

	agg.queue = apqueue.New(agg.db, agg.logger, &apqueue.QueueOption{
		Prefix: "default",
	})
	agg.worker = apqueue.NewWorker(agg.queue, agg.db)
	taskExecutor := taskengine.NewExecutor(agg.config.SmartWallet, agg.db, agg.logger)
	taskengine.SetMacroVars(agg.config.MacroVars)
	taskengine.SetMacroSecrets(agg.config.MacroSecrets)
	taskengine.SetCache(agg.cache)
	macros.SetRpc(agg.config.SmartWallet.EthRpcUrl)

	if err := agg.worker.RegisterProcessor(
		taskengine.JobTypeExecuteTask,
		taskExecutor,
	); err != nil {
		agg.logger.Error("failed to register task processor", "error", err)
	}

	agg.engine = taskengine.New(
		agg.db,
		agg.config,
		agg.queue,
		agg.logger,
	)
	if err := agg.engine.MustStart(); err != nil {
		agg.logger.Error("failed to start task engine", "error", err)
	}

	queueErr := agg.queue.MustStart()
	if queueErr != nil {
		agg.logger.Error("failed to start task queue", "error", queueErr)
	}
	
	agg.worker.MustStart()
}
