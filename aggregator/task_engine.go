package aggregator

import (
	"context"

	"github.com/AvaProtocol/ap-avs/core/apqueue"
	"github.com/AvaProtocol/ap-avs/core/taskengine"
)

func (agg *Aggregator) stopTaskEngine() {
}

func (agg *Aggregator) startTaskEngine(ctx context.Context) {
	agg.logger.Info("Start execution engine",
		"bundler", agg.config.SmartWallet.BundlerURL,
		"factory", agg.config.SmartWallet.FactoryAddress,
		"entrypoint", agg.config.SmartWallet.EntrypointAddress,
	)

	agg.queue = apqueue.New(agg.db, &apqueue.QueueOption{
		Prefix: "default",
	})
	agg.worker = apqueue.NewWorker(agg.queue, agg.db)
	agg.worker.RegisterProcessor(
		"contract_run",
		taskengine.NewProcessor(agg.db, agg.config.SmartWallet),
	)

	agg.engine = taskengine.New(
		agg.db,
		agg.config,
		agg.queue,
	)
	agg.engine.Start()

	agg.queue.MustStart()
	agg.worker.MustStart()
}
