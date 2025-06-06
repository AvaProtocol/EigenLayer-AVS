package trigger

import (
	"context"
	"fmt"
	"sync"

	"math/big"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

type TriggerMetadata[T any] struct {
	TaskID string

	Marker T
}

type BlockTrigger struct {
	*CommonTrigger

	schedule map[int64]map[string]bool

	// Smart monitoring state
	minInterval        int64                 // Minimum interval across all tasks
	subscription       ethereum.Subscription // Active block subscription
	subscriptionActive bool                  // Whether we're currently subscribed
	mu                 sync.RWMutex          // Use RWMutex for better concurrency

	// channel that we will push the trigger information back
	triggerCh chan TriggerMetadata[int64]
}

func NewBlockTrigger(o *RpcOption, triggerCh chan TriggerMetadata[int64], logger sdklogging.Logger) *BlockTrigger {
	var err error

	//logger, err := sdklogging.NewZapLogger(sdklogging.Production)
	b := BlockTrigger{
		CommonTrigger: &CommonTrigger{
			done:      make(chan bool),
			shutdown:  false,
			rpcOption: o,

			logger: logger,
			mu:     sync.Mutex{},
		},
		schedule:           make(map[int64]map[string]bool),
		minInterval:        0,
		subscriptionActive: false,
		triggerCh:          triggerCh,
	}

	b.ethClient, err = ethclient.Dial(o.RpcURL)
	if err != nil {
		panic(err)
	}

	b.wsEthClient, err = ethclient.Dial(o.WsRpcURL)

	if err != nil {
		panic(err)
	}

	return &b
}

// calculateMinInterval finds the minimum interval among all registered tasks
func (b *BlockTrigger) calculateMinInterval() int64 {
	minInterval := int64(0)

	for interval := range b.schedule {
		if len(b.schedule[interval]) > 0 { // Only consider intervals with active tasks
			if minInterval == 0 || interval < minInterval {
				minInterval = interval
			}
		}
	}

	return minInterval
}

// shouldCheckAtBlock determines if we should perform checks at the given block number
// Using checkpoint system starting from block 0: check at minInterval, 2*minInterval, 3*minInterval, etc.
func (b *BlockTrigger) shouldCheckAtBlock(blockNumber int64) bool {
	if b.minInterval <= 0 {
		return false
	}

	return blockNumber%b.minInterval == 0
}

func (b *BlockTrigger) AddCheck(check *avsproto.SyncMessagesResp_TaskMetadata) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	interval := check.GetTrigger().GetBlock().GetConfig().GetInterval()

	// Validate interval to prevent division by zero
	if interval <= 0 {
		return fmt.Errorf("invalid block trigger interval %d for task %s: interval must be greater than 0", interval, check.TaskId)
	}

	// Add task to schedule
	if _, ok := b.schedule[interval]; !ok {
		b.schedule[interval] = map[string]bool{
			check.TaskId: true,
		}
		b.logger.Info("📦 First task registered for block interval", "interval", interval, "task_id", check.TaskId)
	} else {
		b.schedule[interval][check.TaskId] = true
		b.logger.Debug("📦 Additional task registered for block interval", "interval", interval, "task_id", check.TaskId, "total_tasks_for_interval", len(b.schedule[interval]))
	}

	// Recalculate minimum interval
	newMinInterval := b.calculateMinInterval()

	if newMinInterval != b.minInterval {
		oldMinInterval := b.minInterval
		b.minInterval = newMinInterval

		b.logger.Info("🔄 Block monitoring minimum interval updated",
			"old_min_interval", oldMinInterval,
			"new_min_interval", newMinInterval,
			"total_intervals", len(b.schedule))

		// Log active intervals for debugging
		activeIntervals := make([]int64, 0)
		for interval, tasks := range b.schedule {
			if len(tasks) > 0 {
				activeIntervals = append(activeIntervals, interval)
			}
		}
		b.logger.Debug("📊 Active block intervals", "intervals", activeIntervals, "min_interval", newMinInterval)
	}

	return nil
}

func (b *BlockTrigger) RemoveCheck(taskID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	removedFromInterval := int64(0)

	for interval, tasks := range b.schedule {
		if _, exists := tasks[taskID]; exists {
			delete(b.schedule[interval], taskID)
			removedFromInterval = interval

			b.logger.Debug("🗑️ Removed block task", "task_id", taskID, "interval", interval, "remaining_tasks_for_interval", len(tasks)-1)

			// Clean up empty intervals
			if len(b.schedule[interval]) == 0 {
				delete(b.schedule, interval)
				b.logger.Info("🧹 Cleaned up empty block interval", "interval", interval)
			}
			break
		}
	}

	// Recalculate minimum interval after removal
	newMinInterval := b.calculateMinInterval()

	if newMinInterval != b.minInterval {
		oldMinInterval := b.minInterval
		b.minInterval = newMinInterval

		b.logger.Info("🔄 Block monitoring minimum interval updated after removal",
			"removed_from_interval", removedFromInterval,
			"old_min_interval", oldMinInterval,
			"new_min_interval", newMinInterval,
			"remaining_intervals", len(b.schedule))

		// If no more tasks, we'll stop subscription in the main loop
		if newMinInterval == 0 {
			b.logger.Info("⏹️ No more block monitoring tasks - will stop subscription")
		}
	}

	return nil
}

func (b *BlockTrigger) Run(ctx context.Context) error {
	b.logger.Info("🚀 Starting smart block trigger monitor")

	go func() {
		var headers chan *types.Header

		for {
			select {
			case <-ctx.Done():
				b.logger.Info("🛑 Block trigger shutting down due to context cancellation")
				return
			case <-b.done:
				b.logger.Info("🛑 Block trigger shutting down due to done signal")
				return
			default:
				// Check if we need to manage subscription
				b.mu.RLock()
				needsSubscription := b.minInterval > 0
				currentlySubscribed := b.subscriptionActive
				b.mu.RUnlock()

				if needsSubscription && !currentlySubscribed {
					// Start subscription
					b.logger.Info("🔄 Starting block subscription", "min_interval", b.minInterval)
					headers = make(chan *types.Header)
					sub, err := b.wsEthClient.SubscribeNewHead(ctx, headers)
					if err != nil {
						b.logger.Error("❌ Failed to start block subscription", "error", err)
						continue
					}

					b.mu.Lock()
					b.subscription = sub
					b.subscriptionActive = true
					b.mu.Unlock()

					b.logger.Info("✅ Block subscription started", "rpc", b.rpcOption.WsRpcURL, "min_interval", b.minInterval)

				} else if !needsSubscription && currentlySubscribed {
					// Stop subscription
					b.logger.Info("⏹️ Stopping block subscription - no tasks remaining")

					b.mu.Lock()
					if b.subscription != nil {
						b.subscription.Unsubscribe()
						b.subscription = nil
					}
					b.subscriptionActive = false
					b.mu.Unlock()

					if headers != nil {
						close(headers)
						headers = nil
					}

					b.logger.Info("✅ Block subscription stopped")
				}

				// Process blocks if subscribed
				if currentlySubscribed && headers != nil {
					select {
					case err := <-b.subscription.Err():
						if err != nil {
							b.logger.Error("🔄 Block subscription error, retrying", "error", err)

							b.mu.Lock()
							if b.subscription != nil {
								b.subscription.Unsubscribe()
							}
							b.subscriptionActive = false
							b.mu.Unlock()

							if b.wsEthClient != nil {
								b.wsEthClient.Close()
							}

							if err := b.retryConnectToRpc(); err != nil {
								b.logger.Error("❌ Failed to reconnect to RPC", "error", err)
							}
						}

					case header := <-headers:
						if header != nil {
							b.processBlock(header)
						}

					default:
						// Non-blocking check - continue the loop
					}
				}
			}
		}
	}()

	return nil
}

func (b *BlockTrigger) processBlock(header *types.Header) {
	blockNumber := header.Number.Int64()
	b.progress = blockNumber

	b.mu.RLock()
	shouldCheck := b.shouldCheckAtBlock(blockNumber)
	minInterval := b.minInterval
	b.mu.RUnlock()

	if !shouldCheck {
		// Skip processing - not a checkpoint block
		return
	}

	b.logger.Debug("📦 Processing checkpoint block",
		"block_number", blockNumber,
		"block_hash", header.Hash().Hex(),
		"min_interval", minInterval)

	b.mu.RLock()
	scheduleSnapshot := make(map[int64]map[string]bool)
	for interval, tasks := range b.schedule {
		scheduleSnapshot[interval] = make(map[string]bool)
		for taskID := range tasks {
			scheduleSnapshot[interval][taskID] = true
		}
	}
	b.mu.RUnlock()

	triggeredTasks := 0

	// Check all intervals to see which ones should trigger at this block
	for interval, tasks := range scheduleSnapshot {
		if len(tasks) == 0 {
			continue
		}

		// Check if this interval should trigger at this block number
		// Using checkpoint system: interval should trigger when blockNumber % interval == 0
		z := new(big.Int)
		if z.Mod(header.Number, big.NewInt(interval)).Cmp(zero) == 0 {
			for taskID := range tasks {
				b.triggerCh <- TriggerMetadata[int64]{
					TaskID: taskID,
					Marker: blockNumber,
				}
				triggeredTasks++
			}

			b.logger.Debug("🎯 Triggered tasks for interval",
				"interval", interval,
				"block_number", blockNumber,
				"tasks_triggered", len(tasks))
		}
	}

	if triggeredTasks > 0 {
		b.logger.Info("✅ Block checkpoint processed",
			"block_number", blockNumber,
			"tasks_triggered", triggeredTasks,
			"min_interval", minInterval)
	}
}
