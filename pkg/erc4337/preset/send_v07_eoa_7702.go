package preset

import (
	"context"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

// EOA7702AccountLookup reports whether (chain, owner, sender) is a stored
// kind=eoa_7702 wallet. The send path never opens Badger itself; the gateway
// installs this at the same time as the session resolver.
type EOA7702AccountLookup func(chainID int64, owner, sender common.Address) (bool, error)

var (
	eoa7702LookupMu sync.RWMutex
	eoa7702Lookup   EOA7702AccountLookup
)

// SetEOA7702AccountLookup installs the stored-wallet lookup for Track B send.
func SetEOA7702AccountLookup(fn EOA7702AccountLookup) {
	eoa7702LookupMu.Lock()
	defer eoa7702LookupMu.Unlock()
	eoa7702Lookup = fn
}

func storedEOA7702Account(chainID int64, owner, sender common.Address) (bool, error) {
	eoa7702LookupMu.RLock()
	fn := eoa7702Lookup
	eoa7702LookupMu.RUnlock()
	if fn == nil {
		return false, nil
	}
	return fn(chainID, owner, sender)
}

// eoa7702SendAllowed is the B5 derivation-check exception.
//
// Returns (true, nil) only when every gate passes: flag on, Sepolia/Base pin,
// stored kind=eoa_7702, sender==owner, K13. A sender that is not the owner
// is not an EOA attempt — (false, nil) so the CREATE2 derivation check runs.
// An EOA-as-sender that fails a gate is an error, not a fall-through to
// derivation (that message would claim a factory mismatch).
func eoa7702SendAllowed(
	cfg *config.SmartWalletConfig,
	owner, sender common.Address,
	stored bool,
	eoaCode, implCode []byte,
) (bool, error) {
	if sender != owner {
		return false, nil
	}
	if cfg == nil || !cfg.EOA7702Execute {
		return false, fmt.Errorf(
			"sender %s is the owner EOA; eoa_7702_execute is false — UserOps stay on the derived smart wallet",
			sender.Hex())
	}
	if !config.SMA7702FirstChain(cfg.ChainID) {
		return false, fmt.Errorf(
			"chain_id=%d eoa_7702_execute is true, but Track B first chains are Sepolia (%d) and Base (%d)",
			cfg.ChainID, config.SMA7702ChainSepolia, config.SMA7702ChainBase)
	}
	if !cfg.HasSMA7702Pin() {
		return false, fmt.Errorf(
			"chain_id=%d eoa_7702_execute is true but sma_7702_delegate/sma_7702_impl_hash are unset",
			cfg.ChainID)
	}
	if !stored {
		return false, fmt.Errorf(
			"sender %s is the owner EOA but no eoa_7702 wallet record on chain %d; POST /wallets/{eoa}/delegation:submit first — do not fall back from a derived runner",
			sender.Hex(), cfg.ChainID)
	}
	if err := cfg.AssertSMA7702Designation(eoaCode, implCode); err != nil {
		return false, fmt.Errorf("EOA_DELEGATION_MISSING: %w", err)
	}
	return true, nil
}

func allowEOA7702Sender(ctx context.Context, cfg *config.SmartWalletConfig, chain *ethclient.Client, owner, sender common.Address) (bool, error) {
	if sender != owner {
		return false, nil
	}
	stored, err := storedEOA7702Account(cfg.ChainID, owner, sender)
	if err != nil {
		return false, fmt.Errorf("looking up eoa_7702 wallet: %w", err)
	}
	var eoaCode, implCode []byte
	if cfg != nil && cfg.EOA7702Execute && stored && chain != nil {
		var cErr error
		eoaCode, cErr = chain.CodeAt(ctx, sender, nil)
		if cErr != nil {
			return false, fmt.Errorf("EOA_DELEGATION_MISSING: reading code(%s): %w", sender.Hex(), cErr)
		}
		implCode, cErr = chain.CodeAt(ctx, cfg.SMA7702Delegate, nil)
		if cErr != nil {
			return false, fmt.Errorf("EOA_DELEGATION_MISSING: reading SMA-7702 impl: %w", cErr)
		}
	}
	return eoa7702SendAllowed(cfg, owner, sender, stored, eoaCode, implCode)
}
