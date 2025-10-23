package bundler

import (
	"log"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// NonceManager manages nonce tracking for UserOperations to prevent conflicts
// with pending transactions in the bundler's mempool.
// It maintains an in-memory cache of the next expected nonce per sender,
// combining on-chain state with knowledge of submitted-but-not-yet-mined UserOps.
type NonceManager struct {
	// pendingNonces tracks the next nonce to use for each sender
	// Key: sender address (lowercase), Value: next nonce to use
	pendingNonces map[string]*big.Int
	mu            sync.RWMutex
}

// NewNonceManager creates a new NonceManager instance
func NewNonceManager() *NonceManager {
	return &NonceManager{
		pendingNonces: make(map[string]*big.Int),
	}
}

// GetNextNonce returns the next nonce to use for a sender.
// It returns max(on-chain nonce, cached pending nonce).
// This ensures we never reuse a nonce that's already pending in the bundler.
func (nm *NonceManager) GetNextNonce(
	client *ethclient.Client,
	sender common.Address,
	onChainNonceFetcher func() (*big.Int, error),
) (*big.Int, error) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	senderKey := sender.Hex() // Use hex for consistent key format

	// Fetch on-chain nonce
	onChainNonce, err := onChainNonceFetcher()
	if err != nil {
		return nil, err
	}

	// Get cached pending nonce (if any)
	cachedNonce, hasCached := nm.pendingNonces[senderKey]

	var nextNonce *big.Int
	if !hasCached {
		// First time seeing this sender - use on-chain nonce
		nextNonce = new(big.Int).Set(onChainNonce)
		log.Printf("🔢 NONCE MANAGER: First UserOp for sender %s, using on-chain nonce %s",
			sender.Hex(), nextNonce.String())
	} else {
		// Use max(on-chain, cached) to handle cases where:
		// 1. Pending UserOp was mined (on-chain advanced)
		// 2. Bundler dropped our UserOp (on-chain is correct)
		// 3. We have pending UserOps (cached is ahead)
		if onChainNonce.Cmp(cachedNonce) > 0 {
			// On-chain advanced (previous UserOps mined or dropped)
			nextNonce = new(big.Int).Set(onChainNonce)
			log.Printf("🔢 NONCE MANAGER: On-chain nonce (%s) > cached (%s), using on-chain for sender %s",
				onChainNonce.String(), cachedNonce.String(), sender.Hex())
		} else {
			// Cached is ahead or equal (pending UserOps)
			nextNonce = new(big.Int).Set(cachedNonce)
			log.Printf("🔢 NONCE MANAGER: Using cached nonce %s (on-chain: %s) for sender %s",
				nextNonce.String(), onChainNonce.String(), sender.Hex())
		}
	}

	return nextNonce, nil
}

// IncrementNonce increments the cached nonce after successfully submitting a UserOp.
// This allows sequential UserOps to use nonce+1, nonce+2, etc. even before the first is mined.
func (nm *NonceManager) IncrementNonce(sender common.Address, currentNonce *big.Int) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	senderKey := sender.Hex()
	nextNonce := new(big.Int).Add(currentNonce, big.NewInt(1))
	nm.pendingNonces[senderKey] = nextNonce

	log.Printf("🔢 NONCE MANAGER: Incremented nonce for sender %s: %s -> %s",
		sender.Hex(), currentNonce.String(), nextNonce.String())
}

// ResetNonce clears the cached nonce for a sender, forcing the next GetNextNonce
// to fetch fresh state from the chain. Use this when nonce conflicts occur.
func (nm *NonceManager) ResetNonce(sender common.Address) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	senderKey := sender.Hex()
	delete(nm.pendingNonces, senderKey)

	log.Printf("🔢 NONCE MANAGER: Reset cached nonce for sender %s (will fetch fresh from chain)",
		sender.Hex())
}

// SetNonce explicitly sets the cached nonce for a sender.
// Use this when you know the correct next nonce (e.g., after conflict resolution).
func (nm *NonceManager) SetNonce(sender common.Address, nonce *big.Int) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	senderKey := sender.Hex()
	nm.pendingNonces[senderKey] = new(big.Int).Set(nonce)

	log.Printf("🔢 NONCE MANAGER: Set cached nonce for sender %s to %s",
		sender.Hex(), nonce.String())
}

// GetCachedNonce returns the cached nonce for a sender without fetching from chain.
// Returns (nonce, true) if cached, (nil, false) if not cached.
func (nm *NonceManager) GetCachedNonce(sender common.Address) (*big.Int, bool) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	senderKey := sender.Hex()
	nonce, exists := nm.pendingNonces[senderKey]
	if !exists {
		return nil, false
	}
	return new(big.Int).Set(nonce), true
}
