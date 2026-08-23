package aggregator

import (
	"context"
	"fmt"
	"sync"
	"time"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/apconfig"
)

// Operators are not required to run with the key behind their registered
// EigenLayer address. They may declare an alias — a hot key held by the
// running node — against that address in the APConfig contract, keeping
// the registered key cold. Two of the three operators approved on
// mainnet do exactly this, so the aggregator cannot authenticate the
// operator plane by comparing a recovered signature to the claimed
// address alone; it has to resolve the same mapping the operator
// declared.

const (
	// aliasCacheTTL bounds how stale a mapping may get. Declaring an
	// alias is an on-chain transaction, so it changes rarely, and this
	// keeps a per-RPC chain call off the auth path.
	aliasCacheTTL = 5 * time.Minute

	// aliasLookupTimeout caps a single APConfig call so a slow RPC
	// endpoint cannot stall operator authentication.
	aliasLookupTimeout = 10 * time.Second
)

type aliasEntry struct {
	alias     common.Address
	fetchedAt time.Time
}

type operatorAliasResolver struct {
	contract *apconfig.APConfig
	logger   sdklogging.Logger

	mu    sync.Mutex
	cache map[common.Address]aliasEntry
}

func newOperatorAliasResolver(contract *apconfig.APConfig, logger sdklogging.Logger) *operatorAliasResolver {
	return &operatorAliasResolver{
		contract: contract,
		logger:   logger,
		cache:    make(map[common.Address]aliasEntry),
	}
}

// aliasFor returns the alias key an operator declared, or the zero
// address when it declared none and is expected to sign with its own key.
func (r *operatorAliasResolver) aliasFor(ctx context.Context, operator common.Address) (common.Address, error) {
	r.mu.Lock()
	entry, cached := r.cache[operator]
	r.mu.Unlock()

	if cached && time.Since(entry.fetchedAt) < aliasCacheTTL {
		return entry.alias, nil
	}

	if r.contract == nil {
		return common.Address{}, fmt.Errorf("APConfig contract is not configured")
	}

	lookupCtx, cancel := context.WithTimeout(ctx, aliasLookupTimeout)
	defer cancel()

	alias, err := r.contract.GetAlias(&bind.CallOpts{Context: lookupCtx}, operator)
	if err != nil {
		// An RPC blip must not disconnect every alias-key operator at
		// once. A stale mapping is still the one the operator declared —
		// changing it takes a deliberate on-chain transaction — so
		// serving it is safer than refusing the whole fleet.
		if cached {
			r.logger.Warn("APConfig alias lookup failed, falling back to cached mapping",
				"operator", operator.Hex(), "error", err)
			return entry.alias, nil
		}
		return common.Address{}, fmt.Errorf("looking up alias for %s: %w", operator.Hex(), err)
	}

	r.mu.Lock()
	r.cache[operator] = aliasEntry{alias: alias, fetchedAt: time.Now()}
	r.mu.Unlock()

	return alias, nil
}
