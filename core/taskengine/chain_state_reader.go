package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// ChainStateReader is the small set of chain-RPC operations the gateway
// needs to perform on a chain that's NOT the gateway's own AVS chain.
// The gateway used to dial each chain's RPC directly to satisfy these
// calls; this interface lets us route them through the chain worker's
// gRPC instead (see WorkerChainStateReader below).
//
// Methods are intentionally narrow — only what FeeEstimator and the REST
// nonce handler actually call. Adding a method here requires extending
// worker.proto + worker/server.go too, so prefer adding to existing
// methods or composing them when possible.
//
// Context is propagated so graceful-shutdown cancellation can short-
// circuit in-flight chain reads — unlike the original TokenEnrichmentService
// fetcher (which we deferred ctx propagation on as a Phase A tradeoff),
// FeeEstimator already takes ctx everywhere it makes chain calls, so we
// thread it through here cleanly from the start.
type ChainStateReader interface {
	// ChainID returns the chain this reader is bound to. Cached/known
	// a priori in the worker-routed implementation; detected on first
	// call in the direct-RPC implementation.
	ChainID(ctx context.Context) (int64, error)

	// SuggestGasPrice returns a suggested gas price in wei for this chain.
	SuggestGasPrice(ctx context.Context) (*big.Int, error)

	// EstimateGas returns the estimated gas units for the given message.
	EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error)

	// CodeAt returns the contract bytecode at addr, latest block.
	// nil-equivalent blockNumber per ethclient semantics.
	CodeAt(ctx context.Context, addr common.Address) ([]byte, error)

	// GetEntryPointNonce reads the ERC-4337 EntryPoint nonce for the
	// given smart wallet address. key is the 192-bit nonce-space key
	// (typically 0 for the default space). Mirrors aa.GetNonce.
	GetEntryPointNonce(ctx context.Context, walletAddr common.Address, key *big.Int) (*big.Int, error)
}

// ---------------------------------------------------------------------
// Direct-RPC implementation
// ---------------------------------------------------------------------

// directChainStateReader is the legacy path: the gateway holds an
// ethclient.Client per chain and calls each method directly. This is
// what the gateway currently does for every chain it knows about. Kept
// for non-gateway callers (single-chain mode, tests) and as the
// fall-back when the worker for a chain isn't reachable.
type directChainStateReader struct {
	client  *ethclient.Client
	chainID int64 // 0 means "detect on first call"
}

// NewDirectChainStateReader wraps an existing ethclient.Client.
// chainID may be 0 (will be detected on first ChainID call); pass a
// known chainID to avoid the round-trip if you have one.
func NewDirectChainStateReader(client *ethclient.Client, chainID int64) ChainStateReader {
	return &directChainStateReader{client: client, chainID: chainID}
}

func (d *directChainStateReader) ChainID(ctx context.Context) (int64, error) {
	if d.chainID != 0 {
		return d.chainID, nil
	}
	id, err := d.client.ChainID(ctx)
	if err != nil {
		return 0, fmt.Errorf("ChainID: %w", err)
	}
	d.chainID = id.Int64()
	return d.chainID, nil
}

func (d *directChainStateReader) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	return d.client.SuggestGasPrice(ctx)
}

func (d *directChainStateReader) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	return d.client.EstimateGas(ctx, msg)
}

func (d *directChainStateReader) CodeAt(ctx context.Context, addr common.Address) ([]byte, error) {
	return d.client.CodeAt(ctx, addr, nil)
}

func (d *directChainStateReader) GetEntryPointNonce(_ context.Context, walletAddr common.Address, key *big.Int) (*big.Int, error) {
	// aa.GetNonce uses bind.CallOpts internally; it doesn't take ctx
	// (legacy signature). Context cancellation here is therefore a
	// best-effort no-op — the underlying eth_call may still be in
	// flight when the caller cancels. Worth fixing in aa as a
	// separate follow-up; out of scope for this layer.
	return getEntryPointNonceDirect(d.client, walletAddr, key)
}

// ---------------------------------------------------------------------
// Worker-routed implementation
// ---------------------------------------------------------------------

// workerChainStateReader routes each call to the chain's worker via
// gRPC. This is what the gateway uses in gateway mode — the gateway
// itself no longer holds chain-RPC connections (other than its own
// AVS chain).
//
// chainID is explicit because the worker doesn't expose a ChainID RPC
// (would be wasteful — the gateway already knows which chain it's
// dialing). Construct one of these per chain via NewWorkerChainStateReader.
type workerChainStateReader struct {
	client  avsproto.ChainWorkerClient
	chainID int64
	timeout time.Duration
}

// NewWorkerChainStateReader returns a ChainStateReader that delegates
// every call to the supplied chain worker. timeout caps each gRPC
// call's wall time; 10s is a reasonable default — workers are on
// Railway's private network, so well under a second is typical.
func NewWorkerChainStateReader(client avsproto.ChainWorkerClient, chainID int64, timeout time.Duration) ChainStateReader {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &workerChainStateReader{client: client, chainID: chainID, timeout: timeout}
}

func (w *workerChainStateReader) ChainID(_ context.Context) (int64, error) {
	if w.chainID == 0 {
		return 0, fmt.Errorf("worker-routed reader constructed without chainID")
	}
	return w.chainID, nil
}

func (w *workerChainStateReader) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	resp, err := w.client.SuggestGasPrice(ctx, &avsproto.WorkerSuggestGasPriceReq{})
	if err != nil {
		return nil, fmt.Errorf("worker SuggestGasPrice (chain %d): %w", w.chainID, err)
	}
	v, ok := new(big.Int).SetString(resp.GasPriceWei, 10)
	if !ok {
		return nil, fmt.Errorf("worker returned malformed gas price %q for chain %d", resp.GasPriceWei, w.chainID)
	}
	return v, nil
}

func (w *workerChainStateReader) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	req := &avsproto.WorkerEstimateGasReq{
		Data: msg.Data,
	}
	if msg.To != nil {
		req.To = msg.To.Hex()
	}
	if (msg.From != common.Address{}) {
		req.From = msg.From.Hex()
	}
	if msg.Value != nil {
		req.Value = msg.Value.String()
	}
	resp, err := w.client.EstimateGas(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("worker EstimateGas (chain %d): %w", w.chainID, err)
	}
	return resp.Gas, nil
}

func (w *workerChainStateReader) CodeAt(ctx context.Context, addr common.Address) ([]byte, error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	resp, err := w.client.GetCode(ctx, &avsproto.WorkerGetCodeReq{Address: addr.Hex()})
	if err != nil {
		return nil, fmt.Errorf("worker GetCode (chain %d): %w", w.chainID, err)
	}
	return resp.Code, nil
}

func (w *workerChainStateReader) GetEntryPointNonce(ctx context.Context, walletAddr common.Address, key *big.Int) (*big.Int, error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	keyStr := "0"
	if key != nil {
		keyStr = key.String()
	}
	resp, err := w.client.GetNonceByAddress(ctx, &avsproto.WorkerGetNonceByAddressReq{
		WalletAddress: walletAddr.Hex(),
		NonceKey:      keyStr,
	})
	if err != nil {
		return nil, fmt.Errorf("worker GetNonceByAddress (chain %d): %w", w.chainID, err)
	}
	v, ok := new(big.Int).SetString(resp.Nonce, 10)
	if !ok {
		return nil, fmt.Errorf("worker returned malformed nonce %q for %s on chain %d", resp.Nonce, walletAddr.Hex(), w.chainID)
	}
	return v, nil
}

func (w *workerChainStateReader) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, w.timeout)
}

// ---------------------------------------------------------------------
// Per-chain registry
// ---------------------------------------------------------------------

// chainStateReaderRegistry holds ChainStateReader instances keyed by chain ID.
// Populated at startup by the aggregator: in gateway mode it gets one
// worker-routed reader per chain in chains[]; in single-chain mode it gets
// exactly one direct-RPC reader. Mirrors tokenServiceRegistry so callers
// that already know which chain a workflow targets can resolve the right
// reader via GetChainStateReaderForChain.
var (
	chainStateReaderRegistry      = make(map[uint64]ChainStateReader)
	chainStateReaderRegistryMutex sync.RWMutex
)

// RegisterChainStateReader stores reader under chainID so callers can look
// it up later via GetChainStateReaderForChain. Idempotent: re-registering
// the same chain replaces the prior entry. chainID 0 is rejected.
func RegisterChainStateReader(chainID uint64, reader ChainStateReader) {
	if reader == nil || chainID == 0 {
		return
	}
	chainStateReaderRegistryMutex.Lock()
	chainStateReaderRegistry[chainID] = reader
	chainStateReaderRegistryMutex.Unlock()
}

// GetChainStateReaderForChain returns the registered reader for the given
// chain ID, or nil when none is registered. Returns nil for chainID 0.
func GetChainStateReaderForChain(chainID uint64) ChainStateReader {
	if chainID == 0 {
		return nil
	}
	chainStateReaderRegistryMutex.RLock()
	defer chainStateReaderRegistryMutex.RUnlock()
	return chainStateReaderRegistry[chainID]
}

// ClearChainStateReaderRegistry wipes the registry. Test-only helper —
// production code never calls this. Mirrors the lifecycle of
// tokenServiceRegistry which assumes startup-once registration.
func ClearChainStateReaderRegistry() {
	chainStateReaderRegistryMutex.Lock()
	chainStateReaderRegistry = make(map[uint64]ChainStateReader)
	chainStateReaderRegistryMutex.Unlock()
}
