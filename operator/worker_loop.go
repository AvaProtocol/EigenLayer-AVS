package operator

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/AvaProtocol/ap-avs/version"
	pb "github.com/AvaProtocol/ap-avs/protobuf"
)

func (o *Operator) runWorkLoop(ctx context.Context) error {
	timer := time.NewTicker(5 * time.Second)

	var metricsErrChan <-chan error
	if o.config.EnableMetrics {
		metricsErrChan = o.metrics.Start(ctx, o.metricsReg)
	} else {
		metricsErrChan = make(chan error, 1)
	}

	for {
		o.metrics.IncWorkerLoop()
		elapse := o.elapsing.Report()
		o.metrics.AddUptime(float64(elapse.Milliseconds()))

		select {
		case <-ctx.Done():
			return nil
		case err := <-metricsErrChan:
			// TODO: handle gracefully
			o.logger.Fatal("Error in metrics server", "err", err)
		case <-timer.C:
			o.PingServer()
		}
	}
}

func (o *Operator) PingServer() {
	id := hex.EncodeToString(o.operatorId[:])
	start := time.Now()
	// TODO: Implement task and queue depth to detect performance
	_, err := o.aggregatorRpcClient.Ping(context.Background(), &pb.Checkin{
		Address: o.config.OperatorAddress,
		Id:      id,
		// TODO: generate signature with bls key
		Signature: "pending",
		Version: version.Get(),
		MetricsPort: o.metricsPort,
	})

	elapsed := time.Now().Sub(start)
	if err == nil {
		o.metrics.IncPing("success")
		o.logger.Infof("operator update status succesfully in %d ms", elapsed.Milliseconds())
	} else {
		o.metrics.IncPing("error")
		o.logger.Infof("error update status %v", err)
	}
}
