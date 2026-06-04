package aggregator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/ethclient"
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

	// rpc is lazily dialed by GetRPC. The gateway needs a chain-specific
	// ethclient for direct on-chain reads (e.g. ERC-20 balance checks in
	// the withdraw flow) — not every operation rides through the worker
	// gRPC channel.
	//
	// rpcMu serializes the dial; once `rpc` is non-nil, GetRPC returns it
	// without taking the lock-critical-section path. Dial failures are NOT
	// cached: a transient error (RPC node briefly unreachable) leaves
	// `rpc` nil so the next call retries, instead of permanently disabling
	// the chain until a full gateway restart. This replaces a prior
	// sync.Once that pinned the first error forever.
	rpcMu sync.Mutex
	rpc   *ethclient.Client
}

// GetRPC returns a chain-specific ethclient for direct reads against
// this chain's RPC. Successful dials are cached; failed dials are retried
// on the next call so transient errors don't permanently disable the chain.
func (e *ChainEntry) GetRPC() (*ethclient.Client, error) {
	e.rpcMu.Lock()
	defer e.rpcMu.Unlock()

	if e.rpc != nil {
		return e.rpc, nil
	}

	if e.Config == nil || e.Config.SmartWallet == nil || e.Config.SmartWallet.EthRpcUrl == "" {
		return nil, fmt.Errorf("chain %d has no eth_rpc_url configured", chainConfigID(e.Config))
	}

	client, err := ethclient.Dial(e.Config.SmartWallet.EthRpcUrl)
	if err != nil {
		return nil, fmt.Errorf("dial chain %d RPC: %w", chainConfigID(e.Config), err)
	}
	e.rpc = client
	return e.rpc, nil
}

func chainConfigID(c *config.ChainConfig) int64 {
	if c == nil {
		return 0
	}
	return c.ChainID
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
//
// Note: since the switch from grpc.DialContext (blocking) to grpc.NewClient
// (non-blocking), connectEntry never returns a transient error for a
// reachable URL — it only fails if the URL itself is invalid. The loop now
// typically runs once, sees all clients populated, and exits. Real worker
// reconnections are handled by gRPC's internal connection state machine.
// Kept here for the case of explicit URL-level errors that need re-attempt.
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

// connectEntry creates a non-blocking gRPC client for the worker. The actual
// TCP connection is established lazily on the first RPC; transient worker
// outages are handled by gRPC's internal connection state machine. This
// returns essentially instantly so it is safe to call while holding the
// registry's write lock (previously this dialed synchronously with
// grpc.WithBlock() and a 10s timeout, stalling all GetWorker callers
// waiting on the RLock for up to 10s × number-of-chains on startup).
func (r *ChainRegistry) connectEntry(_ context.Context, entry *ChainEntry) error {
	conn, err := grpc.NewClient(
		entry.Config.WorkerAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("creating client for %s: %w", entry.Config.WorkerAddr, err)
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
