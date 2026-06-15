package taskengine

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc20"
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

	// GetBalance reads the native-coin balance (wei) at addr, latest block.
	// Mirrors ethclient.BalanceAt(addr, nil).
	GetBalance(ctx context.Context, addr common.Address) (*big.Int, error)

	// GetTokenBalance reads the raw ERC-20 balance (no decimals applied)
	// of owner for the given token contract. Mirrors erc20.BalanceOf.
	GetTokenBalance(ctx context.Context, token, owner common.Address) (*big.Int, error)

	// GetSmartWalletAddress derives the CREATE2 smart-wallet address for
	// (owner, salt) under the given factory on this chain. Mirrors
	// aa.GetSenderAddressForFactory.
	GetSmartWalletAddress(ctx context.Context, owner, factory common.Address, salt *big.Int) (common.Address, error)

	// CallContract executes a read-only contract call (eth_call) at the
	// given block (nil = latest). Mirrors ethclient.CallContract.
	CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)

	// HeaderByNumber returns the block's number, hash, and timestamp for
	// the given height (nil = latest). Returns the subset of header fields
	// the gateway reads — a full types.Header can't be carried over gRPC
	// faithfully (its hash depends on every field).
	HeaderByNumber(ctx context.Context, number *big.Int) (*BlockHeader, error)

	// GetBlockNumber returns the latest block number. Mirrors
	// ethclient.BlockNumber.
	GetBlockNumber(ctx context.Context) (uint64, error)

	// FindMatchingWalletSalt scans salts [0, maxSalts) for the one whose
	// CREATE2 smart-wallet address (under factory) equals target. Returns
	// found=false when none matches. The worker-routed implementation runs
	// the scan server-side so a wide range is a single round-trip.
	FindMatchingWalletSalt(ctx context.Context, owner, factory, target common.Address, maxSalts int64) (found bool, salt int64, err error)

	// GetTransactionReceipt reads a tx receipt by hash. Returns (nil, nil)
	// when the receipt isn't available yet (pending). Only the gas / status /
	// block fields are populated — not logs. Mirrors
	// ethclient.TransactionReceipt with NotFound mapped to a nil receipt.
	GetTransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)

	// GetStorageAt reads a contract storage slot (latest block). Mirrors
	// ethclient.StorageAt.
	GetStorageAt(ctx context.Context, account common.Address, slot common.Hash) ([]byte, error)
}

// BlockHeader is the subset of block-header fields the gateway reads:
// number + hash (receipt stamping), time (event-timestamp enrichment), and
// parentHash/difficulty/gasLimit/gasUsed (block-trigger output). A full
// types.Header can't be carried over gRPC faithfully (its hash depends on
// every field), so these are the explicit fields callers consume.
type BlockHeader struct {
	Number     uint64
	Hash       common.Hash
	Time       uint64
	ParentHash common.Hash
	Difficulty *big.Int
	GasLimit   uint64
	GasUsed    uint64
}

// ---------------------------------------------------------------------
// Direct-RPC implementation
// ---------------------------------------------------------------------

// directChainStateReader is the legacy path: the gateway holds an
// ethclient.Client per chain and calls each method directly. This is
// what the gateway currently does for every chain it knows about. Kept
// for non-gateway callers (single-chain mode, tests) and as the
// fall-back when the worker for a chain isn't reachable.
//
// Instances are stored in the global registry and shared across
// concurrent HTTP requests, so the lazy-ChainID cache must be
// goroutine-safe — see detectOnce.
type directChainStateReader struct {
	client *ethclient.Client

	// chainID + detectOnce guard the lazy chain-ID detection. Once
	// set (either at construction or by the first detect-on-demand
	// call), chainID is immutable. detectOnce ensures only one
	// goroutine ever issues the eth_chainId RPC.
	chainID    int64
	detectOnce sync.Once
	detectErr  error
}

// NewDirectChainStateReader wraps an existing ethclient.Client.
// chainID may be 0 (will be detected on first ChainID call); pass a
// known chainID to avoid the round-trip if you have one.
func NewDirectChainStateReader(client *ethclient.Client, chainID int64) ChainStateReader {
	r := &directChainStateReader{client: client, chainID: chainID}
	if chainID != 0 {
		// Pre-marking detectOnce as done lets ChainID return the
		// constructor-supplied value without entering the once-block.
		r.detectOnce.Do(func() {})
	}
	return r
}

func (d *directChainStateReader) ChainID(ctx context.Context) (int64, error) {
	d.detectOnce.Do(func() {
		// Use a background context, not the caller's: the result (success or
		// error) is cached permanently via detectOnce, so a cancelled
		// first-caller context must not poison every later call with a stale
		// "context canceled" error. A short timeout bounds the detection.
		detectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		id, err := d.client.ChainID(detectCtx)
		if err != nil {
			d.detectErr = fmt.Errorf("ChainID: %w", err)
			return
		}
		d.chainID = id.Int64()
	})
	if d.detectErr != nil {
		return 0, d.detectErr
	}
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

func (d *directChainStateReader) GetBalance(ctx context.Context, addr common.Address) (*big.Int, error) {
	return d.client.BalanceAt(ctx, addr, nil)
}

func (d *directChainStateReader) GetTokenBalance(ctx context.Context, token, owner common.Address) (*big.Int, error) {
	contract, err := erc20.NewErc20(token, d.client)
	if err != nil {
		return nil, fmt.Errorf("erc20 binding for %s: %w", token.Hex(), err)
	}
	return contract.BalanceOf(&bind.CallOpts{Context: ctx}, owner)
}

func (d *directChainStateReader) GetSmartWalletAddress(_ context.Context, owner, factory common.Address, salt *big.Int) (common.Address, error) {
	// Normalize nil salt to 0 so the direct and worker-routed paths derive
	// identical addresses (the worker path serializes nil as "0").
	if salt == nil {
		salt = big.NewInt(0)
	}
	addr, err := aa.GetSenderAddressForFactory(d.client, owner, factory, salt)
	if err != nil {
		return common.Address{}, err
	}
	if addr == nil {
		return common.Address{}, fmt.Errorf("nil sender address for owner=%s factory=%s salt=%s", owner.Hex(), factory.Hex(), salt.String())
	}
	return *addr, nil
}

func (d *directChainStateReader) CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error) {
	return d.client.CallContract(ctx, msg, blockNumber)
}

func (d *directChainStateReader) HeaderByNumber(ctx context.Context, number *big.Int) (*BlockHeader, error) {
	h, err := d.client.HeaderByNumber(ctx, number)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, fmt.Errorf("HeaderByNumber returned nil header")
	}
	return &BlockHeader{
		Number:     h.Number.Uint64(),
		Hash:       h.Hash(),
		Time:       h.Time,
		ParentHash: h.ParentHash,
		Difficulty: h.Difficulty,
		GasLimit:   h.GasLimit,
		GasUsed:    h.GasUsed,
	}, nil
}

func (d *directChainStateReader) GetBlockNumber(ctx context.Context) (uint64, error) {
	return d.client.BlockNumber(ctx)
}

func (d *directChainStateReader) GetTransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	receipt, err := d.client.TransactionReceipt(ctx, txHash)
	if errors.Is(err, ethereum.NotFound) {
		return nil, nil
	}
	return receipt, err
}

func (d *directChainStateReader) GetStorageAt(ctx context.Context, account common.Address, slot common.Hash) ([]byte, error) {
	return d.client.StorageAt(ctx, account, slot, nil)
}

// maxDirectWalletSaltScan mirrors worker.maxWalletSaltScan: the direct fallback
// must honor the same bound so a high MaxWalletsPerOwner config can't turn the
// scan into thousands of sequential RPC round-trips.
const maxDirectWalletSaltScan = 2000

func (d *directChainStateReader) FindMatchingWalletSalt(ctx context.Context, owner, factory, target common.Address, maxSalts int64) (bool, int64, error) {
	if maxSalts > maxDirectWalletSaltScan {
		return false, 0, fmt.Errorf("max_salts %d exceeds cap %d", maxSalts, maxDirectWalletSaltScan)
	}
	var lastErr error
	errCount := int64(0)
	for salt := int64(0); salt < maxSalts; salt++ {
		if err := ctx.Err(); err != nil {
			return false, 0, err
		}
		addr, err := aa.GetSenderAddressForFactory(d.client, owner, factory, big.NewInt(salt))
		if err != nil {
			lastErr = err
			errCount++
			continue
		}
		if addr != nil && *addr == target {
			return true, salt, nil
		}
	}
	// If every derivation errored, this is a systemic RPC failure, not a
	// genuine "no match" — surface it so connectivity problems aren't masked
	// as not-found (which the worker-routed path would propagate too).
	if maxSalts > 0 && errCount == maxSalts {
		return false, 0, fmt.Errorf("all %d salt derivations failed: %w", maxSalts, lastErr)
	}
	return false, 0, nil
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
	// Refuse to silently fan out a missing To as "contract deployment"
	// — this reader is exclusively used by the fee estimator, which
	// always has a destination (factory address, contract address).
	// A nil To here is a caller bug we'd rather surface than mask.
	if msg.To == nil {
		return 0, fmt.Errorf("EstimateGas: msg.To is required (deployment estimation not supported via this path)")
	}

	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	req := &avsproto.WorkerEstimateGasReq{
		To:   msg.To.Hex(),
		Data: msg.Data,
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

func (w *workerChainStateReader) GetBalance(ctx context.Context, addr common.Address) (*big.Int, error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	resp, err := w.client.GetBalance(ctx, &avsproto.WorkerGetBalanceReq{Address: addr.Hex()})
	if err != nil {
		return nil, fmt.Errorf("worker GetBalance (chain %d): %w", w.chainID, err)
	}
	v, ok := new(big.Int).SetString(resp.BalanceWei, 10)
	if !ok {
		return nil, fmt.Errorf("worker returned malformed balance %q for %s on chain %d", resp.BalanceWei, addr.Hex(), w.chainID)
	}
	return v, nil
}

func (w *workerChainStateReader) GetTokenBalance(ctx context.Context, token, owner common.Address) (*big.Int, error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	resp, err := w.client.GetTokenBalance(ctx, &avsproto.WorkerGetTokenBalanceReq{
		TokenAddress: token.Hex(),
		OwnerAddress: owner.Hex(),
	})
	if err != nil {
		return nil, fmt.Errorf("worker GetTokenBalance (chain %d): %w", w.chainID, err)
	}
	v, ok := new(big.Int).SetString(resp.Balance, 10)
	if !ok {
		return nil, fmt.Errorf("worker returned malformed token balance %q for token=%s owner=%s on chain %d", resp.Balance, token.Hex(), owner.Hex(), w.chainID)
	}
	return v, nil
}

func (w *workerChainStateReader) GetSmartWalletAddress(ctx context.Context, owner, factory common.Address, salt *big.Int) (common.Address, error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	saltStr := "0"
	if salt != nil {
		saltStr = salt.String()
	}
	resp, err := w.client.GetSmartWalletAddress(ctx, &avsproto.WorkerGetSmartWalletAddressReq{
		Owner:          owner.Hex(),
		Salt:           saltStr,
		FactoryAddress: factory.Hex(),
	})
	if err != nil {
		return common.Address{}, fmt.Errorf("worker GetSmartWalletAddress (chain %d): %w", w.chainID, err)
	}
	if resp == nil {
		return common.Address{}, fmt.Errorf("worker returned nil response for owner=%s on chain %d", owner.Hex(), w.chainID)
	}
	if !common.IsHexAddress(resp.Address) {
		return common.Address{}, fmt.Errorf("worker returned malformed address %q for owner=%s on chain %d", resp.Address, owner.Hex(), w.chainID)
	}
	return common.HexToAddress(resp.Address), nil
}

func (w *workerChainStateReader) CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error) {
	// eth_call without a destination is a contract-deployment probe, which
	// the contractRead path never issues. Surface a nil To rather than
	// forwarding an ambiguous request to the worker.
	if msg.To == nil {
		return nil, fmt.Errorf("CallContract: msg.To is required")
	}
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	req := &avsproto.WorkerCallContractReq{
		To:   msg.To.Hex(),
		Data: msg.Data,
	}
	if (msg.From != common.Address{}) {
		req.From = msg.From.Hex()
	}
	if msg.Value != nil {
		req.Value = msg.Value.String()
	}
	if blockNumber != nil {
		req.BlockNumber = blockNumber.String()
	}
	resp, err := w.client.CallContract(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("worker CallContract (chain %d): %w", w.chainID, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("worker returned nil response for CallContract to %s on chain %d", msg.To.Hex(), w.chainID)
	}
	return resp.Result, nil
}

func (w *workerChainStateReader) HeaderByNumber(ctx context.Context, number *big.Int) (*BlockHeader, error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	req := &avsproto.WorkerGetBlockHeaderReq{}
	if number != nil {
		req.BlockNumber = number.String()
	}
	resp, err := w.client.GetBlockHeader(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("worker GetBlockHeader (chain %d): %w", w.chainID, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("worker returned nil response for GetBlockHeader on chain %d", w.chainID)
	}
	// A blank hash means the worker didn't populate it (version skew) —
	// HexToHash would silently coerce "" to the zero hash and propagate an
	// incorrect block-trigger output. Hard-fail instead. (ParentHash is
	// genuinely zero only for genesis, but the worker always serializes it
	// via .Hex(), so an empty string is still skew.)
	if resp.Hash == "" || resp.ParentHash == "" {
		return nil, fmt.Errorf("worker returned incomplete block header (hash=%q parentHash=%q) on chain %d", resp.Hash, resp.ParentHash, w.chainID)
	}
	difficulty, ok := new(big.Int).SetString(resp.Difficulty, 10)
	if !ok {
		// Difficulty is informational (post-merge it's 0); tolerate a
		// missing/blank value rather than failing the whole header read.
		difficulty = big.NewInt(0)
	}
	return &BlockHeader{
		Number:     resp.Number,
		Hash:       common.HexToHash(resp.Hash),
		Time:       resp.Time,
		ParentHash: common.HexToHash(resp.ParentHash),
		Difficulty: difficulty,
		GasLimit:   resp.GasLimit,
		GasUsed:    resp.GasUsed,
	}, nil
}

func (w *workerChainStateReader) GetBlockNumber(ctx context.Context) (uint64, error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	resp, err := w.client.GetBlockNumber(ctx, &avsproto.WorkerGetBlockNumberReq{})
	if err != nil {
		return 0, fmt.Errorf("worker GetBlockNumber (chain %d): %w", w.chainID, err)
	}
	if resp == nil {
		return 0, fmt.Errorf("worker returned nil response for GetBlockNumber on chain %d", w.chainID)
	}
	return resp.Number, nil
}

func (w *workerChainStateReader) GetTransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	resp, err := w.client.GetTransactionReceipt(ctx, &avsproto.WorkerGetTransactionReceiptReq{TxHash: txHash.Hex()})
	if err != nil {
		return nil, fmt.Errorf("worker GetTransactionReceipt (chain %d): %w", w.chainID, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("worker returned nil response for GetTransactionReceipt on chain %d", w.chainID)
	}
	if !resp.Found {
		return nil, nil
	}
	receipt := &types.Receipt{
		Status:      resp.Status,
		GasUsed:     resp.GasUsed,
		TxHash:      common.HexToHash(resp.TxHash),
		BlockNumber: new(big.Int).SetUint64(resp.BlockNumber),
	}
	if egp, ok := new(big.Int).SetString(resp.EffectiveGasPrice, 10); ok {
		receipt.EffectiveGasPrice = egp
	}
	return receipt, nil
}

func (w *workerChainStateReader) GetStorageAt(ctx context.Context, account common.Address, slot common.Hash) ([]byte, error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	resp, err := w.client.GetStorageAt(ctx, &avsproto.WorkerGetStorageAtReq{
		Address: account.Hex(),
		Slot:    slot.Hex(),
	})
	if err != nil {
		return nil, fmt.Errorf("worker GetStorageAt (chain %d): %w", w.chainID, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("worker returned nil response for GetStorageAt on chain %d", w.chainID)
	}
	return resp.Value, nil
}

func (w *workerChainStateReader) FindMatchingWalletSalt(ctx context.Context, owner, factory, target common.Address, maxSalts int64) (bool, int64, error) {
	ctx, cancel := w.withTimeout(ctx)
	defer cancel()
	resp, err := w.client.FindMatchingWalletSalt(ctx, &avsproto.WorkerFindMatchingWalletSaltReq{
		Owner:          owner.Hex(),
		FactoryAddress: factory.Hex(),
		TargetAddress:  target.Hex(),
		MaxSalts:       maxSalts,
	})
	if err != nil {
		return false, 0, fmt.Errorf("worker FindMatchingWalletSalt (chain %d): %w", w.chainID, err)
	}
	if resp == nil {
		return false, 0, fmt.Errorf("worker returned nil response for FindMatchingWalletSalt on chain %d", w.chainID)
	}
	return resp.Found, resp.Salt, nil
}

// withTimeout caps the gRPC call's wall time. context.WithTimeout
// honors whichever deadline is sooner (parent or now+w.timeout), so
// it's always safe to call — we no longer special-case the
// already-has-deadline path. That used to let an HTTP handler's
// 30-60s server-write deadline override our 10s worker-call cap,
// stalling the gateway when a worker hung.
func (w *workerChainStateReader) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
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
