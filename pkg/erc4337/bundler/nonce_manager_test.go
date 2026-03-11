package bundler

import (
	"fmt"
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
)

var testSender = common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

// mockOnChainNonce returns a fetcher that always returns the given nonce.
func mockOnChainNonce(n int64) func() (*big.Int, error) {
	return func() (*big.Int, error) {
		return big.NewInt(n), nil
	}
}

// mockOnChainNonceErr returns a fetcher that always returns an error.
func mockOnChainNonceErr() func() (*big.Int, error) {
	return func() (*big.Int, error) {
		return nil, fmt.Errorf("rpc error: node unavailable")
	}
}

func TestNonceManager_BasicFetch_FirstTime(t *testing.T) {
	nm := NewNonceManager(logger.NewNoOpLogger())

	// First time for a sender: should return on-chain nonce
	nonce, err := nm.GetNextNonce(nil, testSender, mockOnChainNonce(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nonce.Int64() != 5 {
		t.Fatalf("expected nonce 5, got %d", nonce.Int64())
	}
}

func TestNonceManager_CachedAheadOfOnChain(t *testing.T) {
	nm := NewNonceManager(logger.NewNoOpLogger())

	// Simulate: we sent a UserOp at nonce 5, incremented cache to 6
	nm.SetNonce(testSender, big.NewInt(6))

	// On-chain is still 5 (pending tx not mined yet)
	nonce, err := nm.GetNextNonce(nil, testSender, mockOnChainNonce(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nonce.Int64() != 6 {
		t.Fatalf("expected cached nonce 6 (ahead of on-chain 5), got %d", nonce.Int64())
	}
}

func TestNonceManager_OnChainAheadOfCache(t *testing.T) {
	nm := NewNonceManager(logger.NewNoOpLogger())

	// Cache is at 3 (set earlier)
	nm.SetNonce(testSender, big.NewInt(3))

	// On-chain advanced to 5 (previous txs mined)
	nonce, err := nm.GetNextNonce(nil, testSender, mockOnChainNonce(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nonce.Int64() != 5 {
		t.Fatalf("expected on-chain nonce 5 (ahead of cached 3), got %d", nonce.Int64())
	}
}

func TestNonceManager_IncrementNonce(t *testing.T) {
	nm := NewNonceManager(logger.NewNoOpLogger())

	nm.IncrementNonce(testSender, big.NewInt(7))

	cached, ok := nm.GetCachedNonce(testSender)
	if !ok {
		t.Fatal("expected cached nonce after IncrementNonce")
	}
	if cached.Int64() != 8 {
		t.Fatalf("expected cached nonce 8 (7+1), got %d", cached.Int64())
	}
}

func TestNonceManager_SetNonce(t *testing.T) {
	nm := NewNonceManager(logger.NewNoOpLogger())

	nm.SetNonce(testSender, big.NewInt(42))

	cached, ok := nm.GetCachedNonce(testSender)
	if !ok {
		t.Fatal("expected cached nonce after SetNonce")
	}
	if cached.Int64() != 42 {
		t.Fatalf("expected cached nonce 42, got %d", cached.Int64())
	}
}

func TestNonceManager_ResetNonce(t *testing.T) {
	nm := NewNonceManager(logger.NewNoOpLogger())

	nm.SetNonce(testSender, big.NewInt(10))
	nm.ResetNonce(testSender)

	_, ok := nm.GetCachedNonce(testSender)
	if ok {
		t.Fatal("expected no cached nonce after ResetNonce")
	}

	// After reset, GetNextNonce should return on-chain value
	nonce, err := nm.GetNextNonce(nil, testSender, mockOnChainNonce(3))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nonce.Int64() != 3 {
		t.Fatalf("expected on-chain nonce 3 after reset, got %d", nonce.Int64())
	}
}

func TestNonceManager_GetNextNonce_DoesNotMutateCache(t *testing.T) {
	nm := NewNonceManager(logger.NewNoOpLogger())

	// Set cache to 5
	nm.SetNonce(testSender, big.NewInt(5))

	// Call GetNextNonce multiple times — cache should remain at 5
	for i := 0; i < 3; i++ {
		nonce, err := nm.GetNextNonce(nil, testSender, mockOnChainNonce(3))
		if err != nil {
			t.Fatalf("unexpected error on call %d: %v", i, err)
		}
		if nonce.Int64() != 5 {
			t.Fatalf("call %d: expected nonce 5, got %d", i, nonce.Int64())
		}
	}

	// Verify cache is still 5
	cached, _ := nm.GetCachedNonce(testSender)
	if cached.Int64() != 5 {
		t.Fatalf("cache mutated by GetNextNonce: expected 5, got %d", cached.Int64())
	}
}

func TestNonceManager_ConcurrentAccess(t *testing.T) {
	nm := NewNonceManager(logger.NewNoOpLogger())

	var wg sync.WaitGroup
	goroutines := 50

	// Concurrent IncrementNonce and GetNextNonce calls should not panic or race
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				nm.IncrementNonce(testSender, big.NewInt(int64(idx)))
			} else {
				_, _ = nm.GetNextNonce(nil, testSender, mockOnChainNonce(int64(idx)))
			}
		}(i)
	}
	wg.Wait()

	// Just verify we can still read without panic
	_, _ = nm.GetCachedNonce(testSender)
}

func TestNonceManager_FetcherError(t *testing.T) {
	nm := NewNonceManager(logger.NewNoOpLogger())

	_, err := nm.GetNextNonce(nil, testSender, mockOnChainNonceErr())
	if err == nil {
		t.Fatal("expected error from failing fetcher")
	}
}

func TestNonceManager_AA25IncidentScenario(t *testing.T) {
	// Reproduces the exact AA25 incident scenario:
	// 1. UserOp sent at nonce N, IncrementNonce sets cache to N+1
	// 2. Previous tx still pending (on-chain still at N)
	// 3. Next execution calls GetNextNonce — should return N+1 from cache, not N from chain
	// 4. If AA25 occurs, retry should increment to N+2 (not reset and poll on-chain)

	nm := NewNonceManager(logger.NewNoOpLogger())

	// Step 1: First UserOp sent at nonce 1, cache incremented
	nm.IncrementNonce(testSender, big.NewInt(1))

	// Step 2: On-chain is still 1 (tx pending)
	nonce, err := nm.GetNextNonce(nil, testSender, mockOnChainNonce(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nonce.Int64() != 2 {
		t.Fatalf("expected cached nonce 2 (pending tx at 1), got %d", nonce.Int64())
	}

	// Step 3: Simulate AA25 conflict at nonce 2 — the correct retry behavior is to
	// call GetNextNonce (returns 2, same as what we tried), then increment to 3.
	retryNonce, err := nm.GetNextNonce(nil, testSender, mockOnChainNonce(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if retryNonce.Int64() != 2 {
		t.Fatalf("expected GetNextNonce to return 2 (cached, on-chain still 1), got %d", retryNonce.Int64())
	}

	// Since retryNonce == current nonce (2), the retry logic should increment to 3
	// and update cache to 4 (for subsequent UserOps)
	newNonce := new(big.Int).Add(retryNonce, big.NewInt(1))
	nm.SetNonce(testSender, new(big.Int).Add(newNonce, big.NewInt(1)))

	if newNonce.Int64() != 3 {
		t.Fatalf("expected retry nonce 3, got %d", newNonce.Int64())
	}

	cached, _ := nm.GetCachedNonce(testSender)
	if cached.Int64() != 4 {
		t.Fatalf("expected cache updated to 4 for future ops, got %d", cached.Int64())
	}

	// Step 4: Verify the OLD broken behavior would fail.
	// If we had called ResetNonce instead, the cache would be gone and GetNextNonce
	// would return on-chain nonce (1), which is stale.
	nmBroken := NewNonceManager(logger.NewNoOpLogger())
	nmBroken.IncrementNonce(testSender, big.NewInt(1))
	nmBroken.ResetNonce(testSender) // This is what the old code did

	brokenNonce, err := nmBroken.GetNextNonce(nil, testSender, mockOnChainNonce(1))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After reset, it falls back to on-chain nonce 1 — the stale value that causes the loop
	if brokenNonce.Int64() != 1 {
		t.Fatalf("expected broken path to return stale on-chain nonce 1, got %d", brokenNonce.Int64())
	}
}
