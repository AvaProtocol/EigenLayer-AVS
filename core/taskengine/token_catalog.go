package taskengine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
)

// Cross-chain token catalog. Each TokenEnrichmentService is bound to
// the single chain its RPC client reports, so a service that boots on
// Sepolia can only resolve addresses listed in token_whitelist/sepolia.json.
// When a workflow targets one chain but is simulated against a gateway
// bound to a different chain — happens routinely in dev when the
// gateway runs Sepolia + Base-Sepolia but the workflow declares
// settings.chain_id=1 — the bound service returns symbol="UNKNOWN".
//
// `tokenCatalog` lazily loads every token_whitelist/<chain>.json file
// on the first call to `LookupTokenInCatalog` (gated by
// `tokenCatalogOnce`) so callers that know the workflow's chain ID can
// do a metadata-only lookup against the right chain's whitelist
// regardless of which service is bound to which RPC. Lazy loading
// keeps package init free of filesystem reads — important for tests
// that vendor a different whitelist tree via `os.Chdir` before
// triggering the first lookup.
//
// Format is the same per-chain JSON shape the gateway already uses
// (`{id, name, symbol, decimals}` arrays). Filenames map to chain IDs
// via the same convention as `LoadWhitelist`: ethereum.json → 1,
// sepolia.json → 11155111, etc.
//
// Long term this data sources from the @avaprotocol/protocols package
// (the `dist/tokens/<chain>.json` sidecar). For now the catalog reads
// directly from the checked-in token_whitelist/ tree so the migration
// is a path swap, not a runtime behaviour change.

var (
	tokenCatalog      = make(map[uint64]map[string]*TokenMetadata)
	tokenCatalogMutex sync.RWMutex
	tokenCatalogOnce  sync.Once
)

// catalogFileNameToChainID mirrors the LoadWhitelist filename → chain
// mapping. Add new entries here when a new chain ships a whitelist
// file; the catalog walker uses this to decide which chain a file
// describes.
var catalogFileNameToChainID = map[string]uint64{
	"ethereum.json":     ChainIDEthereum,
	"sepolia.json":      ChainIDSepolia,
	"base.json":         ChainIDBase,
	"base-sepolia.json": ChainIDBaseSepolia,
}

// loadTokenCatalog walks the on-disk whitelist directory and populates
// the global cross-chain map. Idempotent — runs at most once per
// process lifetime via tokenCatalogOnce.
func loadTokenCatalog(logger sdklogging.Logger) {
	tokenCatalogOnce.Do(func() {
		dir := "token_whitelist"
		entries, err := os.ReadDir(dir)
		if err != nil {
			if logger != nil {
				logger.Warn("Token catalog: whitelist directory unreadable, cross-chain fallback disabled",
					"dir", dir, "error", err)
			}
			return
		}

		tokenCatalogMutex.Lock()
		defer tokenCatalogMutex.Unlock()

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			chainID, ok := catalogFileNameToChainID[entry.Name()]
			if !ok {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				if logger != nil {
					logger.Warn("Token catalog: failed to read whitelist file",
						"file", path, "error", err)
				}
				continue
			}
			var tokens []TokenMetadata
			if err := json.Unmarshal(data, &tokens); err != nil {
				if logger != nil {
					logger.Warn("Token catalog: failed to parse whitelist file",
						"file", path, "error", err)
				}
				continue
			}

			byAddr := make(map[string]*TokenMetadata, len(tokens))
			for _, token := range tokens {
				addr := strings.ToLower(token.Id)
				if addr == "" {
					continue
				}
				byAddr[addr] = &TokenMetadata{
					Id:       addr,
					Name:     token.Name,
					Symbol:   token.Symbol,
					Decimals: token.Decimals,
					Source:   "catalog",
				}
			}
			tokenCatalog[chainID] = byAddr
			if logger != nil {
				logger.Debug("Token catalog: loaded chain whitelist",
					"file", path, "chainID", chainID, "count", len(byAddr))
			}
		}
	})
}

// LookupTokenInCatalog looks up a token by chain ID and contract
// address in the cross-chain catalog. Returns nil when the address
// isn't registered anywhere in the catalog.
//
// Lookup strategy:
//  1. If `chainID` is provided and the address is registered for that
//     chain, return that entry.
//  2. Otherwise, scan every chain in the catalog for the address.
//     Most ERC-20 addresses are globally unique (mainnet USDC only
//     appears at 0xA0b8…eB48 on chain 1), so this collapses to a
//     single match in practice. When the same address appears on
//     more than one chain (OP-stack predeploys like WETH at
//     0x4200…0006), any match is correct — the symbol is what callers
//     need; chain-specific decimals don't differ across deployments
//     of the same well-known token.
//
// Lazily triggers catalog load on first call. `logger` may be nil
// (load warnings will be silent).
func LookupTokenInCatalog(chainID uint64, contractAddress string, logger sdklogging.Logger) *TokenMetadata {
	loadTokenCatalog(logger)
	if contractAddress == "" {
		return nil
	}
	addr := strings.ToLower(contractAddress)
	tokenCatalogMutex.RLock()
	defer tokenCatalogMutex.RUnlock()

	// Returns a copy rather than the live cache pointer so a future
	// caller writing `result.Symbol = "..."` can't corrupt the catalog
	// for the rest of the process lifetime. TokenMetadata is a small
	// value type — the copy is cheap and the safety boundary is worth
	// it for an exported function.
	if chainID != 0 {
		if byAddr, ok := tokenCatalog[chainID]; ok {
			if hit := byAddr[addr]; hit != nil {
				cp := *hit
				return &cp
			}
		}
	}
	// Cross-chain scan — necessary when the caller is bound to a
	// different chain than the address actually lives on.
	for _, byAddr := range tokenCatalog {
		if hit := byAddr[addr]; hit != nil {
			cp := *hit
			return &cp
		}
	}
	return nil
}

// resetTokenCatalogForTesting clears the catalog and re-arms the
// load-once gate. Tests that mutate the on-disk whitelist tree or
// want to verify load behaviour use this; production code never calls
// it. Both the catalog map AND the Once reassignment must happen under
// the mutex — a concurrent LookupTokenInCatalog → loadTokenCatalog →
// tokenCatalogOnce.Do racing with the Once reassignment is a data
// race the race detector flags.
func resetTokenCatalogForTesting() {
	tokenCatalogMutex.Lock()
	tokenCatalog = make(map[uint64]map[string]*TokenMetadata)
	tokenCatalogOnce = sync.Once{}
	tokenCatalogMutex.Unlock()
}

// isUnknownTokenMetadata returns true when the bound TokenEnrichmentService
// either failed to find the token (nil) or only resurfaced the
// fetchTokenMetadataFromRPC default fallback (symbol="UNKNOWN").
// Catalog lookup is the right next step in both cases.
func isUnknownTokenMetadata(m *TokenMetadata) bool {
	if m == nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(m.Symbol), "UNKNOWN")
}
