package operator

import (
	"context"
	"encoding/hex"
	"io"
	"log"
	"maps"
	"os"
	"sync"
	"time"

	"github.com/AvaProtocol/ap-avs/core/taskengine"
	pb "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/AvaProtocol/ap-avs/version"
	"github.com/expr-lang/expr/vm"
)

const (
	retryIntervalSecond = 15
)

var (
	checkLock  sync.Mutex
	checks     map[string]*pb.SyncTasksResp
	compileExp map[string]*vm.Program
)

// runWorkLoop is main entrypoint where we sync data with aggregator
func (o *Operator) runWorkLoop(ctx context.Context) error {
	// Initialize local storage and cache

	checks = map[string]*pb.SyncTasksResp{}

	// Setup taskengine
	taskengine.SetRpc(os.Getenv("RPC_URL"))

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
			o.RunChecks()
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
				time.Sleep(time.Duration(retryIntervalSecond) * time.Second)
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
					time.Sleep(time.Duration(retryIntervalSecond) * time.Second)
					break
				}
				o.metrics.IncNumTasksReceived(resp.CheckType)
				o.logger.Info("received new task", "id", resp.Id, "type", resp.CheckType)
				checks[resp.Id] = resp
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
		Signature:   "pending",
		Version:     version.Get(),
		RemoteIP:    o.GetPublicIP(),
		MetricsPort: o.config.GetPublicMetricPort(),
	})

	elapsed := time.Now().Sub(start)
	if err == nil {
		o.metrics.IncPing("success")
		o.logger.Infof("operator update status succesfully in %d ms", elapsed.Milliseconds())
	} else {
		o.metrics.IncPing("error")
		o.logger.Infof("error update status %v", err)
	}
	o.metrics.SetPingDuration(elapsed.Seconds())
}

func (o *Operator) RunChecks() {
	hits := []string{}
	hitLookup := map[string]bool{}

	for _, check := range checks {
		switch check.CheckType {
		case "CheckTrigger":
			v, e := taskengine.RunExpressionQuery(check.Trigger.Expression.Expression)
			if e == nil && v == true {
				hits = append(hits, check.Id)
				hitLookup[check.Id] = true
				log.Println("Check hit for ", check.Id)
			} else {
				log.Println("Check miss for ", check.Id)
			}
		case "contract_query_check":
		}
	}

	if _, e := o.aggregatorRpcClient.UpdateChecks(context.Background(), &pb.UpdateChecksReq{
		Address:   o.config.OperatorAddress,
		Signature: "pending",
		Id:        hits,
	}); e == nil {
		// Remove the hit from local cache
		checkLock.Lock()
		defer checkLock.Unlock()
		maps.DeleteFunc(checks, func(k string, v *pb.SyncTasksResp) bool {
			_, ok := hitLookup[k]
			return ok
		})
	}
}
