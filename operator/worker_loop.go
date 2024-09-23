package operator

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"maps"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/expr-lang/expr/vm"
	"github.com/go-co-op/gocron/v2"

	"github.com/AvaProtocol/ap-avs/core/chainio/signer"
	"github.com/AvaProtocol/ap-avs/core/taskengine"
	pb "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/AvaProtocol/ap-avs/version"
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
	// Setup taskengine, initialize local storage and cache, establish rpc
	checks = map[string]*pb.SyncTasksResp{}

	var err error
	o.scheduler, err = gocron.NewScheduler()
	if err != nil {
		panic(err)
	}

	taskengine.SetRpc(o.config.TargetChain.EthRpcUrl)
	taskengine.SetWsRpc(o.config.TargetChain.EthWsUrl)
	taskengine.SetLogger(o.logger)

	var metricsErrChan <-chan error
	if o.config.EnableMetrics {
		metricsErrChan = o.metrics.Start(ctx, o.metricsReg)
	} else {
		metricsErrChan = make(chan error, 1)
	}

	// Establish a connection with gRPC server where new task will be pushed
	// automatically
	o.logger.Info("open channel to grpc to receive check")
	go o.StreamChecks()

	// Register a subscriber on new block event and perform our code such as
	// reporting time and perform check result
	// TODO: Initialize time based task checking
	go taskengine.RegisterBlockListener(ctx, o.RunChecks)
	o.scheduler.Start()
	o.scheduler.NewJob(gocron.DurationJob(time.Second*5), gocron.NewTask(o.PingServer))

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-metricsErrChan:
			// TODO: handle gracefully
			o.logger.Fatal("Error in metrics server", "err", err)
		}
	}
}

// StreamChecks setup a streaming connection to receive task from server, and also
// increase metric once we got data
func (o *Operator) StreamChecks() {
	id := hex.EncodeToString(o.operatorId[:])
	for {
		req := &pb.SyncTasksReq{
			Address: o.config.OperatorAddress,
			Id:      id,
			// TODO: generate signature with ecda/alias key
			Signature: "pending",

			// TODO: use lambort clock
			MonotonicClock: time.Now().Unix(),
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
}

func (o *Operator) PingServer() {
	o.metrics.IncWorkerLoop()
	elapse := o.elapsing.Report()
	o.metrics.AddUptime(float64(elapse.Milliseconds()))

	id := hex.EncodeToString(o.operatorId[:])
	start := time.Now()

	blsSignature := signer.SignBlsMessage(o.blsKeypair, []byte(fmt.Sprintf("ping from %s ip %s", o.config.OperatorAddress, o.GetPublicIP())))
	if blsSignature == nil {
		o.logger.Error("error generate bls signature", "operator", o.config.OperatorAddress)
	}

	str := base64.StdEncoding.EncodeToString(blsSignature.Serialize())

	_, err := o.aggregatorRpcClient.Ping(context.Background(), &pb.Checkin{
		Address:     o.config.OperatorAddress,
		Id:          id,
		Signature:   str,
		Version:     version.Get(),
		RemoteIP:    o.GetPublicIP(),
		MetricsPort: o.config.GetPublicMetricPort(),
	})

	if err != nil {
		o.logger.Error("check in error", "err", err)
	} else {
		o.logger.Debug("check in succesfully")
	}

	elapsed := time.Now().Sub(start)
	if err == nil {
		o.metrics.IncPing("success")
	} else {
		o.metrics.IncPing("error")
		o.logger.Error("error update status", "operator", o.config.OperatorAddress, "error", err)
	}
	o.metrics.SetPingDuration(elapsed.Seconds())
}

func (o *Operator) RunChecks(block *types.Block) error {
	hits := []string{}
	hitLookup := map[string]bool{}

	for _, check := range checks {
		switch check.CheckType {
		case "CheckTrigger":
			v, e := taskengine.RunExpressionQuery(check.Trigger.Expression.Expression)
			if e == nil && v == true {
				hits = append(hits, check.Id)
				hitLookup[check.Id] = true
				o.logger.Debug("Check hit", "taskID", check.Id)
			} else {
				log.Println("Check miss for ", check.Id)
			}
		case "contract_query_check":
		}
	}

	if len(hits) >= 0 {
		if _, err := o.aggregatorRpcClient.UpdateChecks(context.Background(), &pb.UpdateChecksReq{
			Address:   o.config.OperatorAddress,
			Signature: "pending",
			Id:        hits,
		}); err == nil {
			// Remove the hit from local cache
			checkLock.Lock()
			defer checkLock.Unlock()
			maps.DeleteFunc(checks, func(k string, v *pb.SyncTasksResp) bool {
				_, ok := hitLookup[k]
				return ok
			})
		} else {
			o.logger.Error("error pushing checks result to aggregator", "error", err)
		}
	}

	return nil
}
