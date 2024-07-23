package operator

import (
	"io"
	"context"
	"encoding/hex"
	"time"

	"github.com/AvaProtocol/ap-avs/version"
	pb "github.com/AvaProtocol/ap-avs/protobuf"
)

// runWorkLoop is main entrypoint where we sync data with aggregator
func (o *Operator) runWorkLoop(ctx context.Context) error {
	timer := time.NewTicker(5 * time.Second)

	go o.FetchTasks()

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


// FetchTasks setup a streaming connection to receive task from server, and also
// increase metric once we got data
func (o *Operator) FetchTasks() {
	id := hex.EncodeToString(o.operatorId[:])
	go func() {
		for {
	      req := &pb.SyncTasksReq{
	      	Address: o.config.OperatorAddress,
	      	Id:      id,
	      	// TODO: generate signature with ecda/alias key
	      	Signature: "pending",
	      }

	      stream, err := o.aggregatorRpcClient.SyncTasks(context.Background(), req)
	      if err != nil {
			  o.logger.Errorf("error open a stream to aggregator, retry in 15 seconds. error: %v", err)
		  	time.Sleep(time.Duration(15) * time.Second)
			o.retryConnect()
			continue
	      }

		  for {
		  	resp, err := stream.Recv()
		  	if err == io.EOF {
		  		return
		  	}
		  	if err != nil {
				o.logger.Errorf("cannot receive task data from server stream, retry in 15 seconds. error: %v", err)
		  		time.Sleep(time.Duration(15) * time.Second)
		  		break
		  	}
		  	o.metrics.IncNumTasksReceived(resp.TaskType)
		  	o.logger.Info("received new task", "id", resp.Id, "type", resp.TaskType)
		  }
		}
	}()
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
