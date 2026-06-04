// Package avsclient defines the in-process surface that consumers of the
// Ava Protocol AVS depend on. Today the only consumer is the REST API
// (aggregator/rest); the contract lives here — not inside that package —
// so a future split that moves REST (or any other consumer) out of this
// repo can do so by depending on this module rather than dragging the
// contract definitions along.
//
// Naming: the package is `avsclient` because callers consume what the
// AVS exposes. The AVS itself (currently the aggregator package)
// implements these interfaces; consumers just hold them as fields.
package avsclient

import (
	"context"

	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
)

// OperatorLister is the minimal surface a consumer needs to enumerate
// registered operators. Kept as an interface so the consumer doesn't
// import the aggregator package (which would couple it to internal
// types like *OperatorNode) and so tests can inject a fake.
type OperatorLister interface {
	List() []OperatorView
}

// OperatorView is the chain-agnostic, consumer-facing shape of an
// operator. The aggregator adapts its internal *OperatorNode into this
// shape when wiring the Surface so internal columns (RemoteIP,
// MetricsPort, etc.) stay private.
type OperatorView struct {
	Address           string
	Name              string
	Version           string
	BlockNumber       int64
	EventCount        int64
	LastPingEpochMs   int64
	SupportedChainIDs []int64
}

// WithdrawService abstracts the bundler-driven smart-wallet withdrawal
// path (UserOp build → paymaster → submit → optional receipt wait).
// Consumers supply the authed user + request shape and get back the
// hashes + status, with all bundler/paymaster/WebSocket plumbing kept
// behind the interface.
type WithdrawService interface {
	Withdraw(ctx context.Context, req WithdrawRequest) (WithdrawResult, error)
}

// WithdrawRequest is the chain-agnostic shape a consumer hands to
// WithdrawService. Mirrors the OpenAPI WithdrawRequest plus the
// resolved owner address (e.g. from a JWT) and the smart wallet
// address.
type WithdrawRequest struct {
	Owner              string
	SmartWalletAddress string
	RecipientAddress   string
	Amount             string
	Token              string
	ChainID            int64
}

// WithdrawResult is what WithdrawService returns once the UserOp has
// been submitted (and optionally awaited). Consumers render it however
// they want (e.g. REST renders into the OpenAPI WithdrawResponse).
type WithdrawResult struct {
	UserOpHash      string
	TransactionHash string
	Status          string
	Message         string
	SubmittedAt     int64
}

// Surface bundles every dependency a consumer needs to drive the AVS.
// Replaces the old `rest.ServerDeps` — same shape, neutral home.
//
// Each field is optional. Consumers that need a missing dependency
// should fail explicitly (e.g. a 501) rather than crash, so partial
// wiring during rollout is safe.
type Surface struct {
	Operators OperatorLister

	// SmartWalletRpc is the single-chain RPC client. In gateway/
	// multi-chain mode, leave this as the default-chain client and use
	// SmartWalletRpcByChain for per-chain dispatch.
	SmartWalletRpc *ethclient.Client

	// SmartWalletRpcByChain holds per-chain RPC clients keyed by chain
	// id. Populated by gateway mode; nil in single-chain mode (callers
	// fall back to SmartWalletRpc).
	SmartWalletRpcByChain map[int64]*ethclient.Client

	// PriceService resolves token prices for the summarizer + cost
	// estimation paths. taskengine owns the interface definition; we
	// just pass it through.
	PriceService taskengine.PriceService

	// WithdrawSvc handles smart-wallet → EOA fund withdrawal via the
	// bundler. Optional; if nil, WithdrawWallet returns 501.
	WithdrawSvc WithdrawService
}
