package taskengine

import (
	"os"
	"testing"
)

// chdirToRepoRoot mirrors the pattern in token_metadata_engine_test.go —
// the catalog reads `token_whitelist/` relative to cwd, which only
// resolves correctly from the repo root.
func chdirToRepoRoot(t *testing.T) {
	t.Helper()
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir("../.."); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWd) })
}

// Sanity test: the cross-chain catalog can resolve a token whose
// address lives on a chain the local TokenEnrichmentService isn't
// bound to. Mirrors the dev-environment bug where the Sepolia-only
// gateway saw mainnet USDC and emitted "UNKNOWN".
func TestLookupTokenInCatalog_CrossChain(t *testing.T) {
	chdirToRepoRoot(t)
	resetTokenCatalogForTesting()
	t.Cleanup(resetTokenCatalogForTesting)

	// Mainnet USDC — lives in token_whitelist/ethereum.json.
	const mainnetUSDC = "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"

	// Pretend we're calling from a Sepolia-bound service (chain 11155111).
	got := LookupTokenInCatalog(11155111, mainnetUSDC, nil)
	if got == nil {
		t.Fatalf("expected catalog to resolve mainnet USDC across chains, got nil")
	}
	if got.Symbol != "USDC" {
		t.Errorf("expected symbol USDC, got %q", got.Symbol)
	}
	if got.Decimals != 6 {
		t.Errorf("expected decimals 6, got %d", got.Decimals)
	}
}

func TestLookupTokenInCatalog_ChainSpecificMatchPreferred(t *testing.T) {
	chdirToRepoRoot(t)
	resetTokenCatalogForTesting()
	t.Cleanup(resetTokenCatalogForTesting)

	// Sepolia USDC at its own address — should resolve with chain hint.
	const sepoliaUSDC = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
	got := LookupTokenInCatalog(11155111, sepoliaUSDC, nil)
	if got == nil {
		t.Fatalf("expected catalog to resolve Sepolia USDC, got nil")
	}
	if got.Symbol != "USDC" {
		t.Errorf("expected symbol USDC, got %q", got.Symbol)
	}
}

func TestLookupTokenInCatalog_UnknownAddressReturnsNil(t *testing.T) {
	chdirToRepoRoot(t)
	resetTokenCatalogForTesting()
	t.Cleanup(resetTokenCatalogForTesting)

	got := LookupTokenInCatalog(1, "0x0000000000000000000000000000000000000000", nil)
	if got != nil {
		t.Errorf("expected nil for zero address, got %+v", got)
	}
}

func TestLookupTokenInCatalog_EmptyInputs(t *testing.T) {
	chdirToRepoRoot(t)
	resetTokenCatalogForTesting()
	t.Cleanup(resetTokenCatalogForTesting)

	if LookupTokenInCatalog(0, "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", nil) == nil {
		t.Errorf("expected cross-chain scan to succeed even when chainID is 0")
	}
	if LookupTokenInCatalog(1, "", nil) != nil {
		t.Errorf("expected nil for empty address")
	}
}

func TestIsUnknownTokenMetadata(t *testing.T) {
	cases := []struct {
		name string
		in   *TokenMetadata
		want bool
	}{
		{"nil is unknown", nil, true},
		{"explicit UNKNOWN is unknown", &TokenMetadata{Symbol: "UNKNOWN"}, true},
		{"lowercase unknown is unknown", &TokenMetadata{Symbol: "unknown"}, true},
		{"whitespace-padded UNKNOWN is unknown", &TokenMetadata{Symbol: " UNKNOWN "}, true},
		{"known symbol is not unknown", &TokenMetadata{Symbol: "USDC"}, false},
		{"empty symbol is not unknown (preserve existing semantics)", &TokenMetadata{Symbol: ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUnknownTokenMetadata(tc.in); got != tc.want {
				t.Errorf("isUnknownTokenMetadata(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
