package operator

import (
	"context"
	"encoding/hex"
	"time"

	pb "github.com/AvaProtocol/ap-avs/protobuf"
)

func (o *Operator) runWorkLoop(ctx context.Context) error {
	timer := time.NewTicker(1 * time.Second)

	var metricsErrChan <-chan error
	if o.config.EnableMetrics {
		metricsErrChan = o.metrics.Start(ctx, o.metricsReg)
	} else {
		metricsErrChan = make(chan error, 1)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-metricsErrChan:
			// TODO: handle gracefully
			o.logger.Fatal("Error in metrics server", "err", err)
		case <-timer.C:
			// Check in
			o.PingServer()
		}
	}
}

func (o *Operator) PingServer() {
	id := hex.EncodeToString(o.operatorId[:])

	// TODO: Implement task and queue depth to detect performance
	_, err := o.aggregatorRpcClient.Ping(context.Background(), &pb.Checkin{
		Address: o.config.OperatorAddress,
		Id:      id,
		// TODO: generate signature with bls key
		Signature: "pending",
	})

	if err != nil {
		o.logger.Infof("Error pinging aggregator: %v", err)
	}
}
