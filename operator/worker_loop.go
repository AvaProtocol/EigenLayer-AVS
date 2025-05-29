package operator

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	gocron "github.com/go-co-op/gocron/v2"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	triggerengine "github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/trigger"
	avspb "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/version"
)

const (
	retryIntervalSecond = 15
)

// runWorkLoop is main entrypoint where we sync data with aggregator. It performs these op
//   - subscribe to server to receive update. act on these update to update local storage
//   - spawn a loop to check triggering condition
func (o *Operator) runWorkLoop(ctx context.Context) error {
	blockTasksMap := make(map[int64][]string)
	blockTasksMutex := &sync.Mutex{}

	// Initialize the scheduler for managing periodic tasks
	var schedulerErr error
	o.scheduler, schedulerErr = gocron.NewScheduler()
	if schedulerErr != nil {
		return fmt.Errorf("failed to initialize scheduler: %w", schedulerErr)
	}
	o.scheduler.Start()

	_, err := o.scheduler.NewJob(
		gocron.DurationJob(time.Minute*10),
		gocron.NewTask(func() {
			blockTasksMutex.Lock()
			defer blockTasksMutex.Unlock()

			if len(blockTasksMap) > 10 {
				var blocks []int64
				for block := range blockTasksMap {
					blocks = append(blocks, block)
				}

				sort.Slice(blocks, func(i, j int) bool {
					return blocks[i] < blocks[j]
				})

				for i := 0; i < len(blocks)-10; i++ {
					delete(blockTasksMap, blocks[i])
				}
			}
		}),
	)
	if err != nil {
		o.logger.Error("Failed to create cleanup job for block tasks map", "error", err)
	}

	o.scheduler.NewJob(gocron.DurationJob(time.Second*5), gocron.NewTask(o.PingServer))

	macros.SetRpc(o.config.TargetChain.EthWsUrl)
	taskengine.SetRpc(o.config.TargetChain.EthRpcUrl)
	taskengine.SetWsRpc(o.config.TargetChain.EthWsUrl)
	taskengine.SetLogger(o.logger)

	var metricsErrChan <-chan error
	if o.config.EnableMetrics {
		metricsErrChan = o.metrics.Start(ctx, o.metricsReg)
	} else {
		metricsErrChan = make(chan error, 1)
	}

	rpcConfig := triggerengine.RpcOption{
		RpcURL:   o.config.TargetChain.EthRpcUrl,
		WsRpcURL: o.config.TargetChain.EthWsUrl,
	}

	blockTriggerCh := make(chan triggerengine.TriggerMetadata[int64], 1000)
	o.blockTrigger = triggerengine.NewBlockTrigger(&rpcConfig, blockTriggerCh, o.logger)

	eventTriggerCh := make(chan triggerengine.TriggerMetadata[triggerengine.EventMark], 1000)
	o.eventTrigger = triggerengine.NewEventTrigger(&rpcConfig, eventTriggerCh, o.logger)

	timeTriggerCh := make(chan triggerengine.TriggerMetadata[uint64], 1000)
	o.timeTrigger = triggerengine.NewTimeTrigger(timeTriggerCh, o.logger)

	o.blockTrigger.Run(ctx)
	o.timeTrigger.Run(ctx)

	// Event trigger can be costly, so we require an opt-in
	if o.config.EnabledFeatures.EventTrigger {
		o.eventTrigger.Run(ctx)
	} else {
		o.logger.Info("event trigger not enable, skip initialize event monitoring")
	}

	// Establish a connection with gRPC server where new task will be pushed automatically
	o.logger.Info("open channel to grpc to receive check")
	go o.StreamMessages()

	for {
		select {
		case <-ctx.Done():
			return nil
		case triggerItem := <-timeTriggerCh:
			o.logger.Info("time trigger", "task_id", triggerItem.TaskID, "marker", triggerItem.Marker)

			if _, err := o.nodeRpcClient.NotifyTriggers(context.Background(), &avspb.NotifyTriggersReq{
				Address:   o.config.OperatorAddress,
				Signature: "pending",
				TaskId:    triggerItem.TaskID,
				Reason: &avspb.TriggerReason{
					Epoch: uint64(triggerItem.Marker),
					Type:  avspb.TriggerType_TRIGGER_TYPE_CRON,
				},
			}); err == nil {
				o.logger.Debug("Successfully notify aggregator for task hit", "taskid", triggerItem.TaskID)
			} else {
				o.logger.Errorf("task trigger is in alert condition but failed to sync to aggregator", err, "taskid", triggerItem.TaskID)
			}
		case triggerItem := <-blockTriggerCh:
			o.logger.Debug("block trigger details", "task_id", triggerItem.TaskID, "marker", triggerItem.Marker)

			blockTasksMutex.Lock()
			blockNum := triggerItem.Marker
			blockTasksMap[blockNum] = append(blockTasksMap[blockNum], triggerItem.TaskID)

			taskCount := len(blockTasksMap[blockNum])
			if taskCount == 1 || taskCount%5 == 0 {
				o.logger.Info("block trigger summary", "block", blockNum, "task_count", taskCount)
			}
			blockTasksMutex.Unlock()

			if _, err := o.nodeRpcClient.NotifyTriggers(context.Background(), &avspb.NotifyTriggersReq{
				Address:   o.config.OperatorAddress,
				Signature: "pending",
				TaskId:    triggerItem.TaskID,
				Reason: &avspb.TriggerReason{
					BlockNumber: uint64(triggerItem.Marker),
					Type:        avspb.TriggerType_TRIGGER_TYPE_BLOCK,
				},
			}); err == nil {
				o.logger.Debug("Successfully notify aggregator for task hit", "taskid", triggerItem.TaskID)
			} else {
				o.logger.Errorf("task trigger is in alert condition but failed to sync to aggregator", err, "taskid", triggerItem.TaskID)
			}

		case triggerItem := <-eventTriggerCh:
			o.logger.Info("event trigger", "task_id", triggerItem.TaskID, "marker", triggerItem.Marker)

			if _, err := o.nodeRpcClient.NotifyTriggers(context.Background(), &avspb.NotifyTriggersReq{
				Address:   o.config.OperatorAddress,
				Signature: "pending",
				TaskId:    triggerItem.TaskID,
				Reason: &avspb.TriggerReason{
					BlockNumber: uint64(triggerItem.Marker.BlockNumber),
					LogIndex:    uint64(triggerItem.Marker.LogIndex),
					TxHash:      triggerItem.Marker.TxHash,
					Type:        avspb.TriggerType_TRIGGER_TYPE_EVENT,
				},
			}); err == nil {
				o.logger.Debug("Successfully notify aggregator for task hit", "taskid", triggerItem.TaskID)
			} else {
				o.logger.Errorf("task trigger is in alert condition but failed to sync to aggregator", err, "taskid", triggerItem.TaskID)
			}
		case err := <-metricsErrChan:
			// TODO: handle gracefully
			o.logger.Fatal("Error in metrics server", "err", err)
		}
	}
}

// StreamMessages setup a streaming connection to receive task from server
func (o *Operator) StreamMessages() {
	id := hex.EncodeToString(o.operatorId[:])
	ctx := context.Background()
	o.logger.Info("Subscribe to aggregator to get check")

	for {
		epoch := time.Now().Unix()
		blsSignature, err := o.GetSignature(ctx, []byte(fmt.Sprintf("operator connection: %s %s %d", o.config.OperatorAddress, id, epoch)))
		if err != nil {
			panic("cannot get signature")
		}

		req := &avspb.SyncMessagesReq{
			Address: o.config.OperatorAddress,
			Id:      id,

			MonotonicClock: epoch,
			Signature:      blsSignature.Serialize(),
		}

		stream, err := o.nodeRpcClient.SyncMessages(ctx, req)
		if err != nil {
			o.logger.Errorf("error open a stream to aggregator, retry in 15 seconds. error: %v", err)
			time.Sleep(time.Duration(retryIntervalSecond) * time.Second)
			o.retryConnect()
			continue
		}

		defer stream.CloseSend()
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
			o.metrics.IncNumTasksReceived(resp.Id)

			switch resp.Op {
			case avspb.MessageOp_CancelTask, avspb.MessageOp_DeleteTask:
				o.processMessage(resp)
			case avspb.MessageOp_MonitorTaskTrigger:
				if trigger := resp.TaskMetadata.GetTrigger().GetEvent(); trigger != nil {
					o.logger.Info("received new event trigger", "id", resp.Id, "type", resp.TaskMetadata.Trigger)
					if err := o.eventTrigger.AddCheck(resp.TaskMetadata); err != nil {
						o.logger.Info("add trigger to monitor error", err)
					} else {
						o.logger.Info("successfully monitor", "task_id", resp.Id, "component", "eventTrigger")
					}
				} else if trigger := resp.TaskMetadata.Trigger.GetBlock(); trigger != nil {
					o.logger.Info("received new block trigger", "id", resp.Id, "interval", resp.TaskMetadata.Trigger)
					if err := o.blockTrigger.AddCheck(resp.TaskMetadata); err != nil {
						o.logger.Errorf("add trigger to monitor error", err, "task_id", resp.Id)
					} else {
						o.logger.Info("successfully monitor", "task_id", resp.Id, "component", "blockTrigger")
					}
				} else if trigger := resp.TaskMetadata.Trigger.GetCron(); trigger != nil {
					o.logger.Info("received new cron trigger", "id", resp.Id, "cron", resp.TaskMetadata.Trigger)
					if err := o.timeTrigger.AddCheck(resp.TaskMetadata); err != nil {
						o.logger.Errorf("add trigger to monitor error", err, "task_id", resp.Id)
					} else {
						o.logger.Info("successfully monitor", "task_id", resp.Id, "component", "timeTrigger")
					}
				} else if trigger := resp.TaskMetadata.Trigger.GetFixedTime(); trigger != nil {
					o.logger.Info("received new fixed time trigger", "id", resp.Id, "fixedtime", resp.TaskMetadata.Trigger)
					if err := o.timeTrigger.AddCheck(resp.TaskMetadata); err != nil {
						o.logger.Errorf("add trigger to monitor error", err, "task_id", resp.Id)
					} else {
						o.logger.Info("successfully monitor", "task_id", resp.Id, "component", "timeTrigger")
					}
				}
			}
		}
	}
}

func (o *Operator) PingServer() {
	o.metrics.IncWorkerLoop()
	elapse := o.elapsing.Report()
	o.metrics.AddUptime(float64(elapse.Milliseconds()))

	id := hex.EncodeToString(o.operatorId[:])
	start := time.Now()

	blsSignature, err := o.GetSignature(context.Background(), []byte(fmt.Sprintf("ping from %s ip %s", o.config.OperatorAddress, o.GetPublicIP())))

	if blsSignature == nil {
		o.logger.Error("error generate bls signature", "operator", o.config.OperatorAddress, "error", err)
		return
	}

	str := base64.StdEncoding.EncodeToString(blsSignature.Serialize())

	_, err = o.nodeRpcClient.Ping(context.Background(), &avspb.Checkin{
		Address:     o.config.OperatorAddress,
		Id:          id,
		Signature:   str,
		Version:     version.Get(),
		RemoteIP:    o.GetPublicIP(),
		MetricsPort: o.config.GetPublicMetricPort(),
		BlockNumber: o.blockTrigger.GetProgress(),
		EventCount:  o.eventTrigger.GetProgress(),
	})

	if err != nil {
		o.logger.Error("check in error", "err", err)
	} else {
		o.logger.Debug("check in successfully", "component", "grpc")
	}

	elapsed := time.Since(start)
	if err == nil {
		o.metrics.IncPing("success")
	} else {
		o.metrics.IncPing("error")
		o.logger.Error("error update status", "operator", o.config.OperatorAddress, "error", err)
	}
	o.metrics.SetPingDuration(elapsed.Seconds())
}
