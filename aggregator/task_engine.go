package aggregator

import (
	"context"

	"github.com/AvaProtocol/ap-avs/core/apqueue"
	"github.com/AvaProtocol/ap-avs/core/taskengine"
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
	x := taskengine.NewExecutor(agg.db, agg.logger)
	agg.worker.RegisterProcessor(
		taskengine.ExecuteTask,
		x,
		//taskengine.NewProcessor(agg.db, agg.config.SmartWallet, agg.logger),
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

	x.Perform(&apqueue.Job{
		Type: "x",
		Name: "01JE3MB0RHQPZHWATSW8SQQJV6",
		Data: []byte(`{"block_number":7180996,"log_index":82,"tx_hash":"0x8f7c1f698f03d6d32c996b679ea1ebad45bbcdd9aa95d250dda74763cc0f508d"}`),
	})

}
