package aggregator

import (
	"context"
	"fmt"
	"time"

	"github.com/AvaProtocol/ap-avs/core/apqueue"
)

func (agg *Aggregator) startExecutionEngine(ctx context.Context) {
	agg.logger.Info("Start execution engine",
		"bundler", agg.config.SmartWallet.BundlerURL,
		"factory", agg.config.SmartWallet.FactoryAddress,
		"entrypoint", agg.config.SmartWallet.EntrypointAddress,
	)

	q := apqueue.New(agg.db)

	q.MustStart()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case t := <-ticker.C:
			if j, e := q.Enqueue("dummy timer", fmt.Sprintf("%s", t), []byte("foo123")); e == nil {
				fmt.Println("job id", j)
			}

		}
	}
}
