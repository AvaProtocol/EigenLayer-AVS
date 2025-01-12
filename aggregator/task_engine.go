package aggregator

import (
	"context"

	"github.com/AvaProtocol/ap-avs/core/apqueue"
	"github.com/AvaProtocol/ap-avs/core/taskengine"
	"github.com/AvaProtocol/ap-avs/core/taskengine/macros"
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
	taskExecutor := taskengine.NewExecutor(agg.db, agg.logger)
	taskengine.SetMacroVars(agg.config.MacroVars)
	taskengine.SetMacroSecrets(agg.config.MacroSecrets)
	taskengine.SetCache(agg.cache)
	macros.SetRpc(agg.config.SmartWallet.EthRpcUrl)

	agg.worker.RegisterProcessor(
		taskengine.JobTypeExecuteTask,
		taskExecutor,
	)

	agg.engine = taskengine.New(
		agg.db,
		agg.config,
		agg.queue,
		agg.logger,
	)
	agg.engine.MustStart()

	agg.queue.MustStart()
	agg.worker.MustStart()
}
