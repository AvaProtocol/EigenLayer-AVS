package operator

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	gocron "github.com/go-co-op/gocron/v2"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/macros"
	triggerengine "github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/trigger"
	avspb "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/version"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	retryIntervalSecond      = 60
	errorLogDebounceInterval = 3 * time.Minute // Only log same error type every 3 minutes

	// Exponential backoff for EOF reconnection to prevent reconnection storms
	eofBackoffInitial    = 1 * time.Second
	eofBackoffMax        = 60 * time.Second
	eofBackoffMultiplier = 2.0
	eofBackoffResetAfter = 2 * time.Minute // Reset backoff after healthy connection
)

// backoffWithJitter adds up to 25% random jitter to a duration to prevent
// synchronized reconnection bursts (thundering herd).
func backoffWithJitter(d time.Duration) time.Duration {
	jitter := time.Duration(rand.Int63n(int64(d) / 4))
	return d + jitter
}

// shouldLogError determines if we should log an error based on debouncing rules
// Returns true if we should log, false if we should skip to reduce spam
func (o *Operator) shouldLogError(errorType string, isStreamError bool) bool {
	now := time.Now()

	if isStreamError {
		// Check if this is a different error type or enough time has passed
		timeSinceLastLog := now.Sub(o.lastStreamErrorTime)
		isDifferentError := o.lastStreamErrorType != errorType
		shouldLog := isDifferentError || timeSinceLastLog >= errorLogDebounceInterval

		if shouldLog {
			o.lastStreamErrorType = errorType
			o.lastStreamErrorTime = now
			return true
		}
	} else {
		// Check if this is a different error type or enough time has passed for ping errors
		timeSinceLastLog := now.Sub(o.lastPingErrorTime)
		isDifferentError := o.lastPingErrorType != errorType
		shouldLog := isDifferentError || timeSinceLastLog >= errorLogDebounceInterval

		if shouldLog {
			o.lastPingErrorType = errorType
			o.lastPingErrorTime = now
			return true
		}
	}

	return false
}

// categorizePingError maps a PingServer error to a category used for debouncing and
// deciding whether to recreate the gRPC connection. Exported for testing.
func categorizePingError(errMsg string) string {
	if strings.Contains(errMsg, "connection refused") {
		return "ping_connection_refused"
	} else if strings.Contains(errMsg, "context deadline exceeded") || strings.Contains(errMsg, "timeout") {
		return "ping_timeout"
	} else if strings.Contains(errMsg, "name resolver error") {
		// DNS/name resolution failures indicate the gRPC target address
		// cannot be resolved — recreate the connection to re-trigger resolution.
		return "ping_connection_closing"
	} else if strings.Contains(errMsg, "Unavailable") {
		return "ping_service_unavailable"
	} else if strings.Contains(errMsg, "Canceled") || strings.Contains(errMsg, "connection is closing") {
		return "ping_connection_closing"
	}
	return "ping_other_error"
}

// runWorkLoop is main entrypoint where we sync data with aggregator. It performs these op
//   - subscribe to server to receive update. act on these update to update local storage
//   - spawn a loop to check triggering condition
func (o *Operator) runWorkLoop(ctx context.Context) error {
	// Completely disable stack traces globally
	debug.SetTraceback("none")
	os.Setenv("GOTRACEBACK", "none")

	blockTasksMap := make(map[int64][]string)
	blockTasksMutex := &sync.Mutex{}

	// Initialize the scheduler for managing periodic tasks
	var schedulerErr error

	// Configure scheduler to suppress excessive logging
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

	// Wrap PingServer to handle errors gracefully without stack traces
	_, err = o.scheduler.NewJob(
		gocron.DurationJob(time.Second*5),
		gocron.NewTask(func() {
			// Simple wrapper that just calls PingServer - all error handling is internal
			o.PingServer()
		}),
	)
	if err != nil {
		o.logger.Error("Failed to create ping job", "error", err)
	}

	// Resolve the chains this operator monitors. EffectiveChains returns the
	// chains[] array (multi-chain config) or a single chain derived from
	// TargetChain/EthRpcUrl (legacy config).
	chains := o.config.EffectiveChains()
	if len(chains) == 0 {
		return fmt.Errorf("operator has no chains configured")
	}

	// The package-global RPC setters are single-valued; point them at the
	// primary (first) chain. With a multi-chain config TargetChain is empty,
	// so deriving from TargetChain here would set these to "" and silently
	// break macro / task-engine code that reads them — use the primary chain.
	primaryChain := chains[0]
	macros.SetRpc(primaryChain.EthWsUrl)
	taskengine.SetRpc(primaryChain.EthRpcUrl)
	taskengine.SetWsRpc(primaryChain.EthWsUrl)
	taskengine.SetLogger(o.logger)

	var metricsErrChan <-chan error
	if o.config.EnableMetrics && o.metrics != nil {
		metricsErrChan = o.metrics.Start(ctx, o.metricsReg)
	} else {
		metricsErrChan = make(chan error, 1)
		if o.config.EnableMetrics && o.metrics == nil {
			o.logger.Error("🚨 CRITICAL: metrics is nil but EnableMetrics is true - this should never happen in production",
				"solution", "This indicates a serious initialization problem that needs investigation")
		}
	}

	// Build per-chain trigger engines from the resolved chains list (above).
	// Each chain gets its own BlockTrigger and EventTrigger speaking to that
	// chain's RPC + WS; their output channels are fanned-in (tagged with
	// chain_id) to a shared consumer loop below. Scale the shared buffers by
	// chain count so a single busy chain can't starve the others' headroom.
	sharedBlockCh := make(chan chainTaggedBlockEvent, len(chains)*1000)
	sharedEventCh := make(chan chainTaggedEventEvent, len(chains)*1000)

	o.chainTriggers = make(map[int64]*ChainTriggerSet, len(chains))
	o.chainOrder = make([]int64, 0, len(chains))

	// skippedChains accumulates per-chain init failures so we can surface
	// them at the end as a single structured log line. Each entry is a
	// chain name + reason; the aggregator only learns about successfully-
	// dialed chains via supportedChainIDs(), so misconfigured chains are
	// silently excluded from task routing rather than crashing the whole
	// operator. A genuinely empty result (no chains dialed successfully)
	// still hard-fails below.
	type skippedChain struct {
		name   string
		reason string
	}
	var skipped []skippedChain

	for i, chainCfg := range chains {
		// Per-chain HTTP RPC client used for both chain_id detection and the
		// consumer loop's header fetches (HeaderByNumber for block-trigger
		// payload enrichment). Sharing a single client across chains would
		// silently route header fetches to the wrong chain.
		perChainEthClient, err := ethclient.Dial(chainCfg.EthRpcUrl)
		if err != nil {
			o.logger.Warn("chain RPC unreachable — skipping chain (will not advertise capability)",
				"chain_index", i, "chain_name", chainCfg.Name, "rpc", chainCfg.EthRpcUrl, "error", err)
			skipped = append(skipped, skippedChain{name: chainCfg.Name, reason: "dial failed"})
			continue
		}

		// Detect the chain_id from RPC and validate against the config (when
		// the config specified one). Mismatch means the RPC URL is pointing
		// at a different chain than the operator thinks — skip rather than
		// risk silently routing tasks to the wrong chain.
		detectChainCtx, detectCancel := context.WithTimeout(ctx, 10*time.Second)
		detectedBig, err := perChainEthClient.ChainID(detectChainCtx)
		detectCancel()
		if err != nil {
			perChainEthClient.Close()
			o.logger.Warn("chain_id detection failed — skipping chain",
				"chain_index", i, "chain_name", chainCfg.Name, "rpc", chainCfg.EthRpcUrl, "error", err)
			skipped = append(skipped, skippedChain{name: chainCfg.Name, reason: "chain_id detect failed"})
			continue
		}
		detectedID := detectedBig.Int64()
		if chainCfg.ChainID != 0 && chainCfg.ChainID != detectedID {
			perChainEthClient.Close()
			o.logger.Warn("chain_id mismatch between config and RPC — skipping chain",
				"chain_index", i, "chain_name", chainCfg.Name,
				"config_chain_id", chainCfg.ChainID, "rpc_chain_id", detectedID, "rpc", chainCfg.EthRpcUrl)
			skipped = append(skipped, skippedChain{name: chainCfg.Name, reason: fmt.Sprintf("chain_id mismatch %d vs %d", chainCfg.ChainID, detectedID)})
			continue
		}
		// Duplicate chain_id is a deterministic config error (two yaml
		// entries resolved to the same chain), not a transient one — keep
		// it fatal so the operator owner notices the bad config at boot.
		if _, dup := o.chainTriggers[detectedID]; dup {
			perChainEthClient.Close()
			return fmt.Errorf("chain %d (%s): duplicate chain_id %d in operator chains[]",
				i, chainCfg.Name, detectedID)
		}

		rpcOpt := triggerengine.RpcOption{
			RpcURL:   chainCfg.EthRpcUrl,
			WsRpcURL: chainCfg.EthWsUrl,
		}

		perChainBlockCh := make(chan triggerengine.TriggerMetadata[int64], 1000)
		perChainEventCh := make(chan triggerengine.TriggerMetadata[triggerengine.EventMark], 1000)

		// NewBlockTrigger / NewEventTrigger dial the chain's WS endpoint in
		// their constructors and panic on failure. In a multi-chain operator
		// one unreachable per-chain WS must not crash the whole process and
		// take down every other chain — construct under recovery and soft-skip
		// the chain, the same treatment a failed HTTP dial gets above.
		var bt *triggerengine.BlockTrigger
		var et *triggerengine.EventTrigger
		constructErr := func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("%v", r)
				}
			}()
			bt = triggerengine.NewBlockTrigger(&rpcOpt, perChainBlockCh, o.logger)
			et = triggerengine.NewEventTrigger(&rpcOpt, perChainEventCh, o.logger,
				o.config.GetMaxEventsPerQueryPerBlock(), o.config.GetMaxTotalEventsPerBlock(), o.config.OperatorAddress)
			return nil
		}()
		if constructErr != nil {
			perChainEthClient.Close()
			// NewBlockTrigger may have succeeded before NewEventTrigger
			// panicked; close its already-dialed clients so they don't leak.
			// (No et check: a panic in NewEventTrigger means Go never assigned
			// its return value, so et is nil here — any WS clients it dialed
			// before panicking are a known leak, tracked as a follow-up.)
			if bt != nil {
				bt.Close()
			}
			o.logger.Warn("chain WS unreachable — skipping chain (will not advertise capability)",
				"chain_index", i, "chain_name", chainCfg.Name, "ws", chainCfg.EthWsUrl, "error", constructErr)
			skipped = append(skipped, skippedChain{name: chainCfg.Name, reason: "ws dial failed"})
			continue
		}
		et.SetSubscriptionMetrics(
			func(active, desired int) {
				o.metrics.SetEventSubscriptions(detectedID, active, desired)
			},
			func() {
				o.metrics.IncEventSubscriptionRebuildFailures(detectedID)
			},
		)
		et.SetOverloadAlertCallback(func(alert *avspb.EventOverloadAlert) {
			o.logger.Warn("🚨 Sending event overload alert to aggregator",
				"task_id", alert.TaskId,
				"chain_id", detectedID,
				"events_detected", alert.EventsDetected,
				"safety_limit", alert.SafetyLimit)
			if _, err := o.nodeRpcClient.ReportEventOverload(ctx, alert); err != nil {
				o.logger.Error("❌ Failed to send overload alert to aggregator",
					"task_id", alert.TaskId, "chain_id", detectedID, "error", err)
			} else {
				o.logger.Info("✅ Successfully sent overload alert to aggregator",
					"task_id", alert.TaskId, "chain_id", detectedID)
			}
		})

		// Install the trigger set in chainTriggers BEFORE spawning the
		// fan-in goroutine. The goroutine closes over the *ChainTriggerSet
		// directly (not via map lookup on every event), which:
		//   - avoids a map race against later iterations writing other
		//     chains' entries to chainTriggers
		//   - skips a per-event lookup
		// The order matters: bt.Run(ctx) is called below, after the map
		// is updated, so events can't arrive before the pointer is
		// usable; but the write still needs to happen before goroutine
		// spawn to satisfy the Go memory model under -race.
		triggerSet := &ChainTriggerSet{
			ChainID:       detectedID,
			Name:          chainCfg.Name,
			BlockTrigger:  bt,
			EventTrigger:  et,
			EthClient:     perChainEthClient,
			hasBlockTasks: bt.HasBlockTasks,
			// Seed the watermark to "now" so the chain is advertised
			// immediately at boot. The fan-in goroutine refreshes it
			// on every block; an unhealthy chain will fall out of the
			// watermark window on its own.
			lastHeadSeenAt: time.Now(),
		}
		o.chainTriggers[detectedID] = triggerSet
		o.chainOrder = append(o.chainOrder, detectedID)

		// Fan-in: stamp every event from this chain's raw channel with
		// detectedID, refresh the liveness watermark, and forward to the
		// shared channel the consumer loop reads. Exits when ctx is done
		// (channel close is left to the trigger engines themselves on
		// shutdown).
		go func(chainID int64, in <-chan triggerengine.TriggerMetadata[int64], set *ChainTriggerSet) {
			for {
				select {
				case <-ctx.Done():
					return
				case item, ok := <-in:
					if !ok {
						return
					}
					set.markHeadSeen()
					sharedBlockCh <- chainTaggedBlockEvent{chainID: chainID, metadata: item}
				}
			}
		}(detectedID, perChainBlockCh, triggerSet)

		go func(chainID int64, in <-chan triggerengine.TriggerMetadata[triggerengine.EventMark]) {
			for {
				select {
				case <-ctx.Done():
					return
				case item, ok := <-in:
					if !ok {
						return
					}
					sharedEventCh <- chainTaggedEventEvent{chainID: chainID, metadata: item}
				}
			}
		}(detectedID, perChainEventCh)

		o.logger.Info("🔗 Chain trigger engine ready",
			"chain_id", detectedID, "name", chainCfg.Name, "rpc", chainCfg.EthRpcUrl)

		bt.Run(ctx)
		if o.config.EnabledFeatures.EventTrigger {
			et.Run(ctx)
		}
	}

	// All configured chains failed to come up. The operator has nothing
	// chain-bound to advertise; this is a real config error, not a
	// soft-fail case, so surface it loudly. (TimeTrigger is chain-agnostic
	// but on its own can't satisfy block/event tasks, so the operator
	// would be advertising-but-useless without at least one chain.)
	if len(o.chainTriggers) == 0 {
		return fmt.Errorf("operator started with %d configured chain(s) but none initialized successfully — check RPC endpoints", len(chains))
	}

	timeTriggerCh := make(chan triggerengine.TriggerMetadata[uint64], 1000)
	o.timeTrigger = triggerengine.NewTimeTrigger(timeTriggerCh, o.logger)
	o.timeTrigger.Run(ctx)

	eventStatus := "disabled"
	if o.config.EnabledFeatures.EventTrigger {
		eventStatus = "enabled"
	}
	o.logger.Infof("📊 Monitoring Status: Block ✅ | Time ✅ | Event %s | Chains advertised: %v | Skipped: %d",
		eventStatus, o.chainOrder, len(skipped))
	if len(skipped) > 0 {
		for _, s := range skipped {
			o.logger.Warn("chain excluded from advertised capability",
				"chain_name", s.name, "reason", s.reason)
		}
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

			triggerRequestID := fmt.Sprintf("%s:%d", triggerItem.TaskID, triggerItem.Marker)
			if resp, err := o.nodeRpcClient.NotifyTriggers(ctx, &avspb.NotifyTriggersReq{
				Address:          o.config.OperatorAddress,
				Signature:        "pending",
				TaskId:           triggerItem.TaskID,
				TriggerType:      avspb.TriggerType_TRIGGER_TYPE_CRON,
				TriggerRequestId: triggerRequestID,
				TriggerOutput: &avspb.NotifyTriggersReq_CronTrigger{
					CronTrigger: &avspb.CronTrigger_Output{
						Data: func() *structpb.Value {
							cronData := map[string]interface{}{
								"timestamp":    uint64(triggerItem.Marker),
								"timestampIso": time.Unix(0, int64(triggerItem.Marker)*1000000).UTC().Format("2006-01-02T15:04:05.000Z"),
							}
							dataValue, _ := structpb.NewValue(cronData)
							return dataValue
						}(),
					},
				},
			}); err == nil {
				o.logger.Debug("Successfully notify aggregator for task hit",
					"taskid", triggerItem.TaskID,
					"remaining_executions", resp.RemainingExecutions,
					"task_still_enabled", resp.TaskStillEnabled,
					"status", resp.Status)

				// Check if task should stop being monitored
				if !resp.TaskStillEnabled || resp.RemainingExecutions == 0 {
					o.logger.Info("🛑 Task exhausted, requesting stop monitoring",
						"task_id", triggerItem.TaskID,
						"remaining_executions", resp.RemainingExecutions,
						"status", resp.Status,
						"message", resp.Message)

					// Remove task from time trigger monitoring
					if o.timeTrigger != nil {
						if err := o.timeTrigger.RemoveCheck(triggerItem.TaskID); err != nil {
							o.logger.Warn("Failed to remove exhausted task from time monitoring",
								"task_id", triggerItem.TaskID,
								"error", err)
						} else {
							o.logger.Info("✅ Removed exhausted task from time monitoring",
								"task_id", triggerItem.TaskID)
						}
					}
				}
			} else {
				// Use debounced logging for trigger notification errors to prevent stack traces
				var errorType string
				if strings.Contains(err.Error(), "connection refused") {
					errorType = "trigger_notify_connection_refused"
				} else if strings.Contains(err.Error(), "Unavailable") {
					errorType = "trigger_notify_unavailable"
				} else {
					errorType = "trigger_notify_other_error"
				}

				if o.shouldLogError(errorType, false) {
					o.logger.Info("❌ Failed to notify aggregator of task trigger",
						"task_id", triggerItem.TaskID,
						"trigger_type", "time",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"solution", "Check aggregator connectivity - task will be retried",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}
			}
		case wrapped := <-sharedBlockCh:
			triggerItem := wrapped.metadata
			blockChainID := wrapped.chainID
			o.logger.Debug("block trigger details", "task_id", triggerItem.TaskID, "marker", triggerItem.Marker, "chain_id", blockChainID)

			blockTasksMutex.Lock()
			blockNum := triggerItem.Marker
			blockTasksMap[blockNum] = append(blockTasksMap[blockNum], triggerItem.TaskID)

			taskCount := len(blockTasksMap[blockNum])
			// Log at 10, 20, 40, 80, 160...
			threshold := 10
			for threshold <= taskCount {
				if taskCount == threshold {
					o.logger.Info("block trigger summary", "block", blockNum, "task_count", taskCount, "chain_id", blockChainID)
					break
				}
				threshold *= 2
			}
			blockTasksMutex.Unlock()

			// Fetch real block data from RPC to match runBlockTriggerImmediately behavior
			blockData := map[string]interface{}{
				"blockNumber": uint64(triggerItem.Marker),
				"blockHash":   "",
				"timestamp":   uint64(0),
				"parentHash":  "",
				"difficulty":  "",
				"gasLimit":    uint64(0),
				"gasUsed":     uint64(0),
			}

			// Fetch full block data using the per-chain RPC client. Sharing one
			// client across chains would silently route the header fetch to the
			// wrong chain.
			var chainEthClient *ethclient.Client
			if set, ok := o.triggersForChain(blockChainID); ok && set.EthClient != nil {
				chainEthClient = set.EthClient
			}
			if chainEthClient != nil {
				var header *types.Header
				var fetchErr error
				const blockFetchRetries = 3
				const blockFetchRetryDelay = 150 * time.Millisecond
				for attempt := 0; attempt < blockFetchRetries; attempt++ {
					h, err := chainEthClient.HeaderByNumber(ctx, big.NewInt(triggerItem.Marker))
					if err == nil && h != nil {
						header = h
						break
					}
					if err != nil {
						fetchErr = err
					} else {
						fetchErr = fmt.Errorf("header is nil for block %d", triggerItem.Marker)
					}
					if attempt < blockFetchRetries-1 {
						time.Sleep(blockFetchRetryDelay)
					}
				}
				if header != nil {
					// Populate with real blockchain data
					blockData["blockHash"] = header.Hash().Hex()
					blockData["timestamp"] = header.Time
					blockData["parentHash"] = header.ParentHash.Hex()
					blockData["difficulty"] = header.Difficulty.String()
					blockData["gasLimit"] = header.GasLimit
					blockData["gasUsed"] = header.GasUsed

					o.logger.Debug("✅ Fetched real block data for trigger",
						"task_id", triggerItem.TaskID,
						"block_number", triggerItem.Marker,
						"block_hash", header.Hash().Hex(),
						"timestamp", header.Time)
				} else {
					// RPC propagation lag: block not yet available - expected, handled with fallback
					errStr := "nil"
					if fetchErr != nil {
						errStr = fetchErr.Error()
					}
					o.logger.Debug("Block header not available from RPC (propagation lag), using minimal block data",
						"task_id", triggerItem.TaskID,
						"block_number", triggerItem.Marker,
						"error", errStr)
				}
			} else {
				o.logger.Warn("⚠️ Target RPC client not available, using minimal block data",
					"task_id", triggerItem.TaskID,
					"block_number", triggerItem.Marker)
			}

			blockTriggerRequestID := fmt.Sprintf("%s:%d", triggerItem.TaskID, triggerItem.Marker)
			if resp, err := o.nodeRpcClient.NotifyTriggers(ctx, &avspb.NotifyTriggersReq{
				Address:          o.config.OperatorAddress,
				Signature:        "pending",
				TaskId:           triggerItem.TaskID,
				TriggerType:      avspb.TriggerType_TRIGGER_TYPE_BLOCK,
				TriggerRequestId: blockTriggerRequestID,
				ChainId:          blockChainID,
				TriggerOutput: &avspb.NotifyTriggersReq_BlockTrigger{
					BlockTrigger: &avspb.BlockTrigger_Output{
						Data: func() *structpb.Value {
							dataValue, _ := structpb.NewValue(blockData)
							return dataValue
						}(),
					},
				},
			}); err == nil {
				o.logger.Debug("Successfully notify aggregator for task hit",
					"taskid", triggerItem.TaskID,
					"remaining_executions", resp.RemainingExecutions,
					"task_still_enabled", resp.TaskStillEnabled,
					"status", resp.Status)

				// Check if task should stop being monitored
				if !resp.TaskStillEnabled || resp.RemainingExecutions == 0 {
					o.logger.Info("🛑 Task exhausted, requesting stop monitoring",
						"task_id", triggerItem.TaskID,
						"remaining_executions", resp.RemainingExecutions,
						"status", resp.Status,
						"message", resp.Message)

					// Remove task from block trigger monitoring across every chain
					// the operator monitors. Idempotent for chains that never
					// registered this task.
					if err := o.removeBlockCheck(triggerItem.TaskID); err != nil {
						o.logger.Warn("Failed to remove exhausted task from block monitoring",
							"task_id", triggerItem.TaskID,
							"error", err)
					} else {
						o.logger.Info("✅ Removed exhausted task from block monitoring",
							"task_id", triggerItem.TaskID)
					}
				}
			} else {
				// Use debounced logging for trigger notification errors to prevent stack traces
				var errorType string
				if strings.Contains(err.Error(), "connection refused") {
					errorType = "trigger_notify_connection_refused"
				} else if strings.Contains(err.Error(), "Unavailable") {
					errorType = "trigger_notify_unavailable"
				} else {
					errorType = "trigger_notify_other_error"
				}

				if o.shouldLogError(errorType, false) {
					o.logger.Info("❌ Failed to notify aggregator of task trigger",
						"task_id", triggerItem.TaskID,
						"trigger_type", "block",
						"block_number", triggerItem.Marker,
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"solution", "Check aggregator connectivity - task will be retried",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}
			}

		case wrappedEvt := <-sharedEventCh:
			triggerItem := wrappedEvt.metadata
			eventChainID := wrappedEvt.chainID
			hasEnrichedData := triggerItem.Marker.EnrichedData != nil
			o.logger.Info("event trigger",
				"task_id", triggerItem.TaskID,
				"chain_id", eventChainID,
				"marker", triggerItem.Marker,
				"has_enriched_data", hasEnrichedData)

			// Create structured data for the event trigger
			var eventDataMap map[string]interface{}

			if triggerItem.Marker.EnrichedData != nil {
				// Use enriched data if available (includes token metadata, addresses, etc.)
				eventDataMap = triggerItem.Marker.EnrichedData

				// Ensure basic fields are still available for backward compatibility
				if _, exists := eventDataMap["blockNumber"]; !exists {
					eventDataMap["blockNumber"] = triggerItem.Marker.BlockNumber
				}
				if _, exists := eventDataMap["logIndex"]; !exists {
					eventDataMap["logIndex"] = triggerItem.Marker.LogIndex
				}
				if _, exists := eventDataMap["transactionHash"]; !exists {
					eventDataMap["transactionHash"] = triggerItem.Marker.TxHash
				}

				o.logger.Debug("✅ Using enriched event data for task execution",
					"task_id", triggerItem.TaskID,
					"enriched_fields", len(eventDataMap))
			} else {
				// Fall back to basic metadata if no enriched data
				eventDataMap = map[string]interface{}{
					"blockNumber":     triggerItem.Marker.BlockNumber,
					"logIndex":        triggerItem.Marker.LogIndex,
					"transactionHash": triggerItem.Marker.TxHash,
				}

				o.logger.Debug("📋 Using basic event metadata (no enrichment available)",
					"task_id", triggerItem.TaskID)
			}

			eventData, err := structpb.NewStruct(eventDataMap)
			if err != nil {
				o.logger.Error("Failed to create structured event data", "error", err)
				continue
			}

			eventTriggerRequestID := fmt.Sprintf("%s:%s:%d", triggerItem.TaskID, triggerItem.Marker.TxHash, triggerItem.Marker.LogIndex)
			if resp, err := o.nodeRpcClient.NotifyTriggers(ctx, &avspb.NotifyTriggersReq{
				Address:          o.config.OperatorAddress,
				Signature:        "pending",
				TaskId:           triggerItem.TaskID,
				TriggerType:      avspb.TriggerType_TRIGGER_TYPE_EVENT,
				TriggerRequestId: eventTriggerRequestID,
				ChainId:          eventChainID,
				TriggerOutput: &avspb.NotifyTriggersReq_EventTrigger{
					EventTrigger: &avspb.EventTrigger_Output{
						Data: structpb.NewStructValue(eventData),
					},
				},
			}); err == nil {
				o.logger.Debug("Successfully notify aggregator for task hit",
					"taskid", triggerItem.TaskID,
					"remaining_executions", resp.RemainingExecutions,
					"task_still_enabled", resp.TaskStillEnabled,
					"status", resp.Status)

				// Check if task should stop being monitored
				if !resp.TaskStillEnabled || resp.RemainingExecutions == 0 {
					o.logger.Info("🛑 Task exhausted, requesting stop monitoring",
						"task_id", triggerItem.TaskID,
						"remaining_executions", resp.RemainingExecutions,
						"status", resp.Status,
						"message", resp.Message)

					// Remove task from event trigger monitoring across every chain.
					if err := o.removeEventCheck(triggerItem.TaskID); err != nil {
						o.logger.Warn("Failed to remove exhausted task from event monitoring",
							"task_id", triggerItem.TaskID,
							"error", err)
					} else {
						o.logger.Info("✅ Removed exhausted task from event monitoring",
							"task_id", triggerItem.TaskID)
					}
				}
			} else {
				// Use debounced logging for trigger notification errors to prevent stack traces
				var errorType string
				if strings.Contains(err.Error(), "connection refused") {
					errorType = "trigger_notify_connection_refused"
				} else if strings.Contains(err.Error(), "Unavailable") {
					errorType = "trigger_notify_unavailable"
				} else {
					errorType = "trigger_notify_other_error"
				}

				if o.shouldLogError(errorType, false) {
					o.logger.Info("❌ Failed to notify aggregator of task trigger",
						"task_id", triggerItem.TaskID,
						"trigger_type", "event",
						"block_number", triggerItem.Marker.BlockNumber,
						"log_index", triggerItem.Marker.LogIndex,
						"tx_hash", triggerItem.Marker.TxHash,
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"solution", "Check aggregator connectivity - task will be retried",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}
			}
		case err := <-metricsErrChan:
			// Handle metrics server errors gracefully without crashing the operator
			if strings.Contains(err.Error(), "address already in use") {
				o.logger.Info("❌ Metrics server port conflict - another service is using the port",
					"port", o.config.GetPublicMetricPort(),
					"solution", "Either stop the conflicting service or change the metrics port in config",
					"impact", "Metrics collection disabled, but operator will continue running",
					"raw_error", fmt.Sprintf("%v", err))
			} else {
				o.logger.Info("❌ Metrics server encountered an error",
					"port", o.config.GetPublicMetricPort(),
					"solution", "Check metrics server configuration and port availability",
					"impact", "Metrics collection disabled, but operator will continue running",
					"raw_error", fmt.Sprintf("%v", err))
			}
			// Continue operation without metrics instead of crashing
		}
	}
}

// StreamMessages setup a streaming connection to receive task from server
func (o *Operator) StreamMessages() {
	id := hex.EncodeToString(o.operatorId[:])
	ctx := context.Background()
	o.logger.Info("Subscribe to aggregator to get check")

	// Exponential backoff state for EOF/rate-limit reconnections
	eofBackoff := eofBackoffInitial
	var lastConnectTime time.Time

	for {
		// Reset backoff if previous connection was healthy for long enough
		if !lastConnectTime.IsZero() && time.Since(lastConnectTime) >= eofBackoffResetAfter {
			eofBackoff = eofBackoffInitial
		}
		lastConnectTime = time.Now()
		epoch := time.Now().Unix()
		blsSignature, err := o.GetSignature(ctx, []byte(fmt.Sprintf("operator connection: %s %s %d", o.config.OperatorAddress, id, epoch)))
		if err != nil {
			o.logger.Info("❌ Failed to generate BLS signature for stream connection",
				"operator", o.config.OperatorAddress,
				"solution", "Check BLS key configuration and permissions - will retry in 15 seconds",
				"raw_error", fmt.Sprintf("%v", err))
			time.Sleep(time.Duration(retryIntervalSecond) * time.Second)
			continue
		}

		req := &avspb.SyncMessagesReq{
			Address: o.config.OperatorAddress,
			Id:      id,

			MonotonicClock: epoch,
			Signature:      blsSignature.Serialize(),
			Capabilities: &avspb.SyncMessagesReq_Capabilities{
				EventMonitoring: true, // This operator supports event monitoring
				BlockMonitoring: true, // This operator supports block monitoring
				TimeMonitoring:  true, // This operator supports time/cron monitoring
			},
			// Advertise which chains this operator can monitor so the
			// gateway can filter per-chain task assignment. Empty list
			// means "single-chain operator on the aggregator default chain"
			// for backward compat with pre-multi-chain operators.
			SupportedChainIds: o.supportedChainIDs(),
		}

		stream, err := o.nodeRpcClient.SyncMessages(ctx, req)
		if err != nil {
			var errorType string
			var shouldLog bool

			// Categorize and debounce stream error logging - check more specific patterns first
			grpcCode := status.Code(err)
			if grpcCode == codes.ResourceExhausted {
				errorType = "stream_rate_limited"
				shouldLog = o.shouldLogError(errorType, true)
				if shouldLog {
					o.logger.Info("⏳ Aggregator rate-limited this connection - backing off",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"backoff", eofBackoff.String(),
						"solution", "Reconnecting too fast, waiting before retry")
				}
				time.Sleep(backoffWithJitter(eofBackoff))
				eofBackoff = time.Duration(float64(eofBackoff) * eofBackoffMultiplier)
				if eofBackoff > eofBackoffMax {
					eofBackoff = eofBackoffMax
				}
				continue
			} else if strings.Contains(err.Error(), "connection refused") {
				errorType = "stream_connection_refused"
				shouldLog = o.shouldLogError(errorType, true)
				if shouldLog {
					o.logger.Info("❌ Cannot establish stream to aggregator - service not reachable",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"solution", "Ensure aggregator service is running and accessible",
						"retry_in", "60 seconds",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}

				// Try to recreate connection even for connection refused - aggregator might have restarted
				if recreateErr := o.recreateGrpcConnection(); recreateErr != nil {
					o.logger.Debug("🔄 Connection recreation failed (aggregator likely still down)",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"raw_error", recreateErr)
					// Continue to sleep and retry
				} else {
					o.logger.Info("✅ Connection recreated successfully after connection refused - retrying immediately",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress)
					continue
				}
			} else if grpcCode == codes.Unavailable {
				errorType = "stream_service_unavailable"
				shouldLog = o.shouldLogError(errorType, true)
				if shouldLog {
					o.logger.Info("❌ Aggregator streaming service unavailable",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"solution", "Aggregator may be overloaded or experiencing issues",
						"retry_in", "60 seconds",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}

				// Try to recreate connection for unavailable service - aggregator might have restarted
				if recreateErr := o.recreateGrpcConnection(); recreateErr != nil {
					o.logger.Debug("🔄 Connection recreation failed (aggregator likely still unavailable)",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"raw_error", recreateErr)
					// Continue to sleep and retry
				} else {
					o.logger.Info("✅ Connection recreated successfully after service unavailable - retrying immediately",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress)
					continue
				}
			} else if grpcCode == codes.Canceled || strings.Contains(err.Error(), "connection is closing") {
				errorType = "stream_connection_closing"
				shouldLog = o.shouldLogError(errorType, true)
				if shouldLog {
					o.logger.Info("❌ gRPC connection is closing - will recreate connection",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"solution", "Recreating gRPC client to establish fresh connection",
						"retry_in", "15 seconds",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}

				// Recreate the gRPC connection when it's in closing state
				if recreateErr := o.recreateGrpcConnection(); recreateErr != nil {
					o.logger.Warn("❌ Failed to recreate gRPC connection",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"raw_error", recreateErr)
					// Still sleep and retry even if recreation failed
				} else {
					o.logger.Info("✅ gRPC connection recreated successfully - attempting to reconnect immediately",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress)
					// Skip the sleep and try immediately with the new connection
					continue
				}
			} else {
				errorType = "stream_other_error"
				shouldLog = o.shouldLogError(errorType, true)
				if shouldLog {
					o.logger.Info("❌ Failed to open task stream to aggregator",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"solution", "Check network connectivity and aggregator service status",
						"retry_in", "15 seconds",
						"next_log_in", "3 minutes if error persists",
						"raw_error", fmt.Sprintf("%v", err))
				}
			}
			time.Sleep(time.Duration(retryIntervalSecond) * time.Second)
			continue
		}

		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				o.logger.Info("📡 Stream closed by aggregator (EOF) - will reconnect",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"backoff", eofBackoff.String(),
					"solution", "Stream closed normally, backing off before retry")

				stream.CloseSend()
				time.Sleep(backoffWithJitter(eofBackoff))
				eofBackoff = time.Duration(float64(eofBackoff) * eofBackoffMultiplier)
				if eofBackoff > eofBackoffMax {
					eofBackoff = eofBackoffMax
				}

				break // Break out of inner loop to retry connection in outer loop
			}
			if err != nil {
				var errorType string
				var shouldLog bool

				// Categorize and debounce stream receive error logging
				recvCode := status.Code(err)
				if recvCode == codes.Unavailable || strings.Contains(err.Error(), "connection") {
					errorType = "stream_lost_connection"
					shouldLog = o.shouldLogError(errorType, true)
					if shouldLog {
						o.logger.Info("❌ Lost connection to aggregator stream",
							"aggregator_address", o.config.AggregatorServerIpPortAddress,
							"operator", o.config.OperatorAddress,
							"solution", "Will attempt to reconnect to aggregator",
							"retry_in", "15 seconds",
							"next_log_in", "3 minutes if error persists",
							"raw_error", fmt.Sprintf("%v", err))
					}
				} else if recvCode == codes.Canceled || strings.Contains(err.Error(), "connection is closing") {
					errorType = "stream_receive_connection_closing"
					shouldLog = o.shouldLogError(errorType, true)
					if shouldLog {
						o.logger.Info("❌ Stream connection is closing - will recreate gRPC client",
							"aggregator_address", o.config.AggregatorServerIpPortAddress,
							"operator", o.config.OperatorAddress,
							"solution", "Recreating gRPC client to establish fresh connection",
							"retry_in", "15 seconds",
							"next_log_in", "3 minutes if error persists",
							"raw_error", fmt.Sprintf("%v", err))
					}

					// Recreate the gRPC connection when stream receive detects closing state
					if recreateErr := o.recreateGrpcConnection(); recreateErr != nil {
						o.logger.Error("❌ Failed to recreate gRPC connection during stream receive",
							"aggregator_address", o.config.AggregatorServerIpPortAddress,
							"operator", o.config.OperatorAddress,
							"raw_error", recreateErr)
					}
				} else {
					errorType = "stream_receive_error"
					shouldLog = o.shouldLogError(errorType, true)
					if shouldLog {
						o.logger.Info("❌ Error receiving task data from aggregator",
							"aggregator_address", o.config.AggregatorServerIpPortAddress,
							"operator", o.config.OperatorAddress,
							"solution", "Will retry stream connection",
							"retry_in", "15 seconds",
							"next_log_in", "3 minutes if error persists",
							"raw_error", fmt.Sprintf("%v", err))
					}
				}
				stream.CloseSend()
				time.Sleep(time.Duration(retryIntervalSecond) * time.Second)
				break
			}
			// Add nil check for resp to prevent segmentation fault
			if resp == nil {
				o.logger.Warn("❌ Received nil response from aggregator stream",
					"solution", "This may indicate a protocol mismatch or aggregator issue")
				continue
			}
			// Add nil check for metrics to prevent segmentation fault
			if o.metrics != nil {
				o.metrics.IncNumTasksReceived(resp.Id)
			} else {
				o.logger.Error("🚨 CRITICAL: metrics is nil in StreamMessages - this should never happen in production",
					"task_id", resp.Id,
					"solution", "This indicates a serious initialization problem that needs investigation")
			}

			switch resp.Op {
			case avspb.MessageOp_DisableTask, avspb.MessageOp_DeleteTask:
				o.processMessage(resp)
			case avspb.MessageOp_MonitorTaskTrigger:
				// Add nil check to prevent segmentation fault
				if resp.TaskMetadata == nil {
					o.logger.Warn("❌ Received MonitorTaskTrigger message with nil TaskMetadata",
						"task_id", resp.Id,
						"solution", "This may indicate a protocol mismatch or aggregator issue")
					continue
				}

				// Additional nil check for Trigger field
				triggerObj := resp.TaskMetadata.GetTrigger()
				if triggerObj == nil {
					o.logger.Warn("❌ Received MonitorTaskTrigger message with nil Trigger",
						"task_id", resp.Id,
						"solution", "This may indicate a protocol mismatch or aggregator issue")
					continue
				}

				// TaskMetadata.ChainId is the trigger's MONITORING chain (G2),
				// set by the aggregator from the event/block trigger's own
				// config — which may differ from where the task's nodes act.
				monitorChainID := resp.TaskMetadata.GetChainId()

				if trigger := triggerObj.GetEvent(); trigger != nil {
					o.logger.Info("📥 Monitoring event trigger", "task_id", resp.Id, "chain_id", monitorChainID)

					set, ok := o.triggersForChain(monitorChainID)
					if !ok {
						o.logger.Warn("⚠️ Dropping event task — operator does not monitor this chain",
							"task_id", resp.Id, "chain_id", monitorChainID,
							"supported", o.supportedChainIDs())
						continue
					}

					// Safely call AddCheck with panic recovery
					func() {
						defer func() {
							if r := recover(); r != nil {
								o.logger.Error("🚨 CRITICAL: eventTrigger.AddCheck() caused segmentation fault",
									"task_id", resp.Id,
									"panic", r,
									"solution", "resp.TaskMetadata is corrupted - cannot add event trigger")
							}
						}()
						if err := set.EventTrigger.AddCheck(resp.TaskMetadata); err != nil {
							o.logger.Info("❌ Failed to add event trigger to monitoring", "error", err, "task_id", resp.Id, "solution", "Task may not be monitored for events")
						}
					}()
				} else if trigger := triggerObj.GetBlock(); trigger != nil {
					o.logger.Info("📦 Monitoring block trigger", "task_id", resp.Id, "chain_id", monitorChainID, "interval", trigger.Config.GetInterval())

					set, ok := o.triggersForChain(monitorChainID)
					if !ok {
						o.logger.Warn("⚠️ Dropping block task — operator does not monitor this chain",
							"task_id", resp.Id, "chain_id", monitorChainID,
							"supported", o.supportedChainIDs())
						continue
					}

					// Safely call AddCheck with panic recovery
					func() {
						defer func() {
							if r := recover(); r != nil {
								o.logger.Error("🚨 CRITICAL: blockTrigger.AddCheck() caused segmentation fault",
									"task_id", resp.Id,
									"panic", r,
									"solution", "resp.TaskMetadata is corrupted - cannot add block trigger")
							}
						}()
						if err := set.BlockTrigger.AddCheck(resp.TaskMetadata); err != nil {
							o.logger.Info("❌ Failed to add block trigger to monitoring", "error", err, "task_id", resp.Id, "solution", "Task may not be monitored for blocks")
						}
					}()
					// Adding a block task (re)starts the head subscription if it
					// was idle. Reset the liveness watermark so the freshly
					// started subscription gets a full staleness window to
					// produce its first head before supportedChainIDs() could
					// consider the chain stalled.
					set.markHeadSeen()
				} else if trigger := triggerObj.GetCron(); trigger != nil {
					scheduleInfo := "unknown"
					if trigger.Config != nil && trigger.Config.Schedules != nil {
						scheduleInfo = strings.Join(trigger.Config.Schedules, ", ")
					}
					o.logger.Info("⏰ Monitoring cron trigger", "task_id", resp.Id, "schedule", scheduleInfo)

					// Safely call AddCheck with panic recovery
					func() {
						defer func() {
							if r := recover(); r != nil {
								o.logger.Error("🚨 CRITICAL: timeTrigger.AddCheck() caused segmentation fault",
									"task_id", resp.Id,
									"panic", r,
									"solution", "resp.TaskMetadata is corrupted - cannot add cron trigger")
							}
						}()
						if err := o.timeTrigger.AddCheck(resp.TaskMetadata); err != nil {
							o.logger.Info("❌ Failed to add cron trigger to monitoring", "error", err, "task_id", resp.Id, "solution", "Task may not be monitored for scheduled execution")
						}
					}()
				} else if trigger := triggerObj.GetFixedTime(); trigger != nil {
					epochInfo := "unknown"
					if trigger.Config != nil && trigger.Config.Epochs != nil {
						epochCount := len(trigger.Config.Epochs)
						if epochCount == 1 {
							epochInfo = fmt.Sprintf("epoch: %d", trigger.Config.Epochs[0])
						} else {
							epochInfo = fmt.Sprintf("%d epochs", epochCount)
						}
					}
					o.logger.Info("📅 Monitoring fixed time trigger", "task_id", resp.Id, "epoch_info", epochInfo)

					// Safely call AddCheck with panic recovery
					func() {
						defer func() {
							if r := recover(); r != nil {
								o.logger.Error("🚨 CRITICAL: timeTrigger.AddCheck() caused segmentation fault",
									"task_id", resp.Id,
									"panic", r,
									"solution", "resp.TaskMetadata is corrupted - cannot add fixed time trigger")
							}
						}()
						if err := o.timeTrigger.AddCheck(resp.TaskMetadata); err != nil {
							o.logger.Info("❌ Failed to add fixed time trigger to monitoring", "error", err, "task_id", resp.Id, "solution", "Task may not be monitored for fixed time execution")
						}
					}()
				} else {
					o.logger.Debug("⏭️ Trigger type not monitored by operator",
						"task_id", resp.Id,
						"solution", "Likely a manual or future-typed trigger; ignored")
				}
			}
		}
	}
}

func (o *Operator) PingServer() {
	// Ensure this function never panics or appears to fail to gocron
	defer func() {
		if r := recover(); r != nil {
			// Silent recovery - don't even log to avoid triggering gocron stack traces
		}
	}()

	// Add nil check for metrics to prevent segmentation fault
	if o.metrics != nil {
		o.metrics.IncWorkerLoop()
		elapse := o.elapsing.Report()
		o.metrics.AddUptime(float64(elapse.Milliseconds()))
	} else {
		o.logger.Error("🚨 CRITICAL: metrics is nil in PingServer - this should never happen in production",
			"solution", "This indicates a serious initialization problem that needs investigation")
	}

	id := hex.EncodeToString(o.operatorId[:])
	start := time.Now()

	blsSignature, err := o.GetSignature(context.Background(), []byte(fmt.Sprintf("ping from %s ip %s", o.config.OperatorAddress, o.GetPublicIP())))

	if blsSignature == nil {
		// Use debounced logging for BLS signature errors
		if o.shouldLogError("bls_signature_error", false) {
			o.logger.Info("❌ Failed to generate BLS signature for ping",
				"operator", o.config.OperatorAddress,
				"solution", "Check BLS key configuration and permissions",
				"next_log_in", "3 minutes if error persists",
				"raw_error", fmt.Sprintf("%v", err))
		}
		return
	}

	str := base64.StdEncoding.EncodeToString(blsSignature.Serialize())

	// Re-advertise the currently-live chain set on every ping. The
	// aggregator updates its per-operator capability snapshot from this
	// field so a stalled subscription drops out of routing within one
	// Ping interval, without forcing a SyncMessages reconnect. See
	// supportedChainIDs() for the freshness watermark logic.
	liveChains := o.supportedChainIDs()

	// Emit per-chain capability + head-lag gauges. Iterate the
	// CONFIGURED set so a dropped chain still reports lag (its gauge
	// keeps growing until the subscription recovers or the operator
	// restarts), which is what an alert wants to see.
	if o.metrics != nil {
		live := make(map[int64]bool, len(liveChains))
		for _, id := range liveChains {
			live[id] = true
		}
		for _, id := range o.allConfiguredChainIDs() {
			o.metrics.SetChainAdvertised(id, live[id])
			if set, ok := o.chainTriggers[id]; ok && set != nil {
				set.lastHeadMu.RLock()
				lag := time.Since(set.lastHeadSeenAt).Seconds()
				set.lastHeadMu.RUnlock()
				o.metrics.SetChainHeadLagSeconds(id, lag)
			}
		}
	}
	_, err = o.nodeRpcClient.Ping(context.Background(), &avspb.Checkin{
		Address:           o.config.OperatorAddress,
		Id:                id,
		Signature:         str,
		Version:           version.Get(),
		RemoteIP:          o.GetPublicIP(),
		MetricsPort:       o.config.GetPublicMetricPort(),
		BlockNumber:       o.primaryBlockProgress(),
		EventCount:        o.primaryEventProgress(),
		SupportedChainIds: liveChains,
	})

	// If the live set has narrowed from the configured set, surface
	// it — observability hook for "Base subscription stalled, dropped
	// from routing". Only emit on:
	//   - a successful ping (don't conflate this signal with ping-error
	//     debouncing below), and
	//   - a *change* in the narrowed set vs the last logged value, so
	//     a steady-state degraded operator doesn't flood the log every
	//     5 seconds.
	if err == nil {
		if cfg := o.allConfiguredChainIDs(); len(liveChains) != len(cfg) {
			o.lastAdvertisedChainsMu.Lock()
			changed := !int64SlicesEqual(o.lastAdvertisedChainsLogged, liveChains)
			if changed {
				o.lastAdvertisedChainsLogged = append([]int64(nil), liveChains...)
			}
			o.lastAdvertisedChainsMu.Unlock()
			if changed {
				o.logger.Warn("operator advertising fewer chains than configured — chain subscription may be stalled",
					"configured", cfg, "advertised", liveChains)
			}
		} else {
			// Recovered to full coverage; clear so a subsequent
			// re-narrowing logs again.
			o.lastAdvertisedChainsMu.Lock()
			o.lastAdvertisedChainsLogged = nil
			o.lastAdvertisedChainsMu.Unlock()
		}
	}

	if err != nil {
		var errorType string
		var shouldLog bool

		errorType = categorizePingError(err.Error())

		shouldLog = o.shouldLogError(errorType, false)
		if shouldLog {
			switch errorType {
			case "ping_connection_refused":
				o.logger.Info("❌ Cannot connect to aggregator service - is the aggregator running?",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Please ensure the aggregator service is running and accessible",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))
			case "ping_timeout":
				o.logger.Info("❌ Connection to aggregator timed out",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Check network connectivity and aggregator response time",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))
			case "ping_service_unavailable":
				o.logger.Info("❌ Aggregator service is unavailable",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Check if the aggregator is overloaded or experiencing issues",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))
			case "ping_connection_closing":
				o.logger.Info("❌ Connection to aggregator was closed - recreating gRPC client",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Recreating gRPC client to establish fresh connection",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))

				// Recreate the gRPC connection when ping detects closing state
				if recreateErr := o.recreateGrpcConnection(); recreateErr != nil {
					o.logger.Warn("❌ Failed to recreate gRPC connection during ping",
						"aggregator_address", o.config.AggregatorServerIpPortAddress,
						"operator", o.config.OperatorAddress,
						"raw_error", recreateErr)
				}
			default:
				o.logger.Info("❌ Failed to ping aggregator service",
					"aggregator_address", o.config.AggregatorServerIpPortAddress,
					"operator", o.config.OperatorAddress,
					"solution", "Check aggregator configuration and network connectivity",
					"next_log_in", "3 minutes if error persists",
					"raw_error", fmt.Sprintf("%v", err))
			}
		}
	} else {
		// Log successful ping status with tiered intervals
		now := time.Now()
		timeSinceLastSuccess := now.Sub(o.lastPingSuccessTime)

		// Always log the first success after any error (status change)
		wasInErrorState := o.lastPingSuccessTime.IsZero() || timeSinceLastSuccess >= errorLogDebounceInterval

		// Debug logging every 5 minutes, Info logging every 30 minutes
		debugInterval := 5 * time.Minute
		infoInterval := 30 * time.Minute
		timeSinceLastLog := now.Sub(o.lastPingSuccessTime)
		shouldLogDebug := timeSinceLastLog >= debugInterval
		shouldLogInfo := timeSinceLastLog >= infoInterval

		if wasInErrorState {
			// Status change: error -> success (always INFO level)
			o.logger.Info("Successfully pinged aggregator", "aggregator_address", o.config.AggregatorServerIpPortAddress)
			o.lastPingSuccessTime = now
		} else if shouldLogInfo {
			// Periodic health confirmation at INFO level (every 30 minutes)
			o.logger.Info("Successfully pinged aggregator", "aggregator_address", o.config.AggregatorServerIpPortAddress)
			o.lastPingSuccessTime = now
		} else if shouldLogDebug {
			// More frequent logging at DEBUG level (every 5 minutes)
			o.logger.Debug("Successfully pinged aggregator", "aggregator_address", o.config.AggregatorServerIpPortAddress)
			o.lastPingSuccessTime = now
		}
	}

	elapsed := time.Since(start)
	// Add nil check for metrics to prevent segmentation fault
	if o.metrics != nil {
		if err == nil {
			o.metrics.IncPing("success")
		} else {
			o.metrics.IncPing("error")
			// Don't log additional errors here - they were already logged above with proper debouncing
			// Duplicate logging here with different error types breaks the debounce logic
		}
		o.metrics.SetPingDuration(elapsed.Seconds())
	} else {
		o.logger.Error("🚨 CRITICAL: metrics is nil in PingServer end - this should never happen in production",
			"solution", "This indicates a serious initialization problem that needs investigation")
	}
}
