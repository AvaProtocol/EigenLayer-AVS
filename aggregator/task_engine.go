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
	taskExecutor := taskengine.NewExecutor(agg.db, agg.logger)
	taskengine.SetMacro(agg.config.Macros)
	taskengine.SetCache(agg.cache)

	agg.worker.RegisterProcessor(
		taskengine.ExecuteTask,
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

	//agg.engine.AggregateChecksResult("0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d", &avsproto.NotifyTriggersReq{
	//	Address:   "0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d",
	//	Signature: "123",
	//	TaskId:    "01JE8FDNGD1HB3G03FC5BKPBMV",
	//	TriggerMarker: &avsproto.TriggerMark{
	//		BlockNumber: 7180996,
	//		LogIndex:    82,
	//		TxHash:      "0x8f7c1f698f03d6d32c996b679ea1ebad45bbcdd9aa95d250dda74763cc0f508d",
	//	},
	//})
	taskExecutor.Perform(&apqueue.Job{
		Type: taskengine.ExecuteTask,
		Name: "01JE9TKC82EWEQ1E3SM4QB33A0",
		Data: []byte(`{"block_number":7212134,"log_index":50,"tx_hash":"0x5698f1ebbf70a2ebfe748249a84446806876ae4c1484b6ea0c474f83a5e2c1d6"}`),
	})
}
