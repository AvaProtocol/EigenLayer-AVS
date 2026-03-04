package aggregator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Layr-Labs/eigensdk-go/logging"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// ChainEntry holds the gRPC connection and client for a single chain worker.
type ChainEntry struct {
	Config *config.ChainConfig
	conn   *grpc.ClientConn
	Client avsproto.ChainWorkerClient
}

// ChainRegistry manages connections to per-chain workers in gateway mode.
type ChainRegistry struct {
	mu             sync.RWMutex
	chains         map[int64]*ChainEntry
	defaultChainID int64
	logger         logging.Logger
}

// NewChainRegistry creates a registry from the gateway's chain configs.
func NewChainRegistry(chains []*config.ChainConfig, defaultChainID int64, logger logging.Logger) *ChainRegistry {
	registry := &ChainRegistry{
		chains:         make(map[int64]*ChainEntry, len(chains)),
		defaultChainID: defaultChainID,
		logger:         logger,
	}

	for _, chainCfg := range chains {
		registry.chains[chainCfg.ChainID] = &ChainEntry{
			Config: chainCfg,
		}
	}

	return registry
}

// Connect attempts to establish gRPC connections to all registered workers.
// Connections that fail are retried in the background — this method never
// returns an error so that the gateway can finish startup and serve health
// checks even when workers are not yet available.
func (r *ChainRegistry) Connect(ctx context.Context) {
	r.mu.Lock()

	for chainID, entry := range r.chains {
		if err := r.connectEntry(ctx, entry); err != nil {
			r.logger.Warn("Worker not ready, will retry in background",
				"chain_id", chainID,
				"chain_name", entry.Config.Name,
				"worker_addr", entry.Config.WorkerAddr,
				"error", err)
		} else {
			r.logger.Info("Connected to chain worker",
				"chain_id", chainID,
				"chain_name", entry.Config.Name,
				"worker_addr", entry.Config.WorkerAddr)
		}
	}

	r.mu.Unlock()

	// Start background reconnection loop for any workers that failed.
	go r.reconnectLoop(ctx)
}

// reconnectLoop periodically retries connections for workers that are not
// yet connected. It stops when all workers are connected or the context
// is cancelled (gateway shutdown).
func (r *ChainRegistry) reconnectLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		r.mu.Lock()
		allConnected := true
		for chainID, entry := range r.chains {
			if entry.Client != nil {
				continue
			}
			allConnected = false

			if err := r.connectEntry(ctx, entry); err != nil {
				r.logger.Warn("Worker reconnect failed, will retry",
					"chain_id", chainID,
					"chain_name", entry.Config.Name,
					"error", err)
			} else {
				r.logger.Info("Connected to chain worker (retry)",
					"chain_id", chainID,
					"chain_name", entry.Config.Name,
					"worker_addr", entry.Config.WorkerAddr)
			}
		}
		r.mu.Unlock()

		if allConnected {
			r.logger.Info("All chain workers connected")
			return
		}
	}
}

func (r *ChainRegistry) connectEntry(ctx context.Context, entry *ChainEntry) error {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(
		dialCtx,
		entry.Config.WorkerAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", entry.Config.WorkerAddr, err)
	}

	entry.conn = conn
	entry.Client = avsproto.NewChainWorkerClient(conn)
	return nil
}

// GetWorker returns the ChainEntry for the given chain ID.
// If chainID is 0, it returns the default chain's entry.
func (r *ChainRegistry) GetWorker(chainID int64) (*ChainEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if chainID == 0 {
		chainID = r.defaultChainID
	}

	entry, ok := r.chains[chainID]
	if !ok {
		return nil, fmt.Errorf("no worker registered for chain %d", chainID)
	}

	if entry.Client == nil {
		return nil, fmt.Errorf("worker for chain %d is not connected", chainID)
	}

	return entry, nil
}

// ResolveChainID returns the effective chain ID, using the default if the input is 0.
func (r *ChainRegistry) ResolveChainID(chainID int64) int64 {
	if chainID == 0 {
		return r.defaultChainID
	}
	return chainID
}

// SupportedChainIDs returns all registered chain IDs.
func (r *ChainRegistry) SupportedChainIDs() []int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]int64, 0, len(r.chains))
	for id := range r.chains {
		ids = append(ids, id)
	}
	return ids
}

// GetChainConfig returns the ChainConfig for a given chain ID.
func (r *ChainRegistry) GetChainConfig(chainID int64) (*config.ChainConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if chainID == 0 {
		chainID = r.defaultChainID
	}

	entry, ok := r.chains[chainID]
	if !ok {
		return nil, fmt.Errorf("no config for chain %d", chainID)
	}

	return entry.Config, nil
}

// Close shuts down all gRPC connections.
func (r *ChainRegistry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for chainID, entry := range r.chains {
		if entry.conn != nil {
			if err := entry.conn.Close(); err != nil {
				r.logger.Warn("Error closing worker connection",
					"chain_id", chainID,
					"error", err)
			}
			entry.conn = nil
			entry.Client = nil
		}
	}
}
