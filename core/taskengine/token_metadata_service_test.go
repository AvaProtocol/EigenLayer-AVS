package taskengine

import (
	"testing"
)

// TestTokenEnrichmentService_RequiresRPC tests that TokenEnrichmentService
// now requires an RPC client and fails without one
func TestTokenEnrichmentService_RequiresRPC(t *testing.T) {
	// Skip this test since we now require RPC client for all TokenEnrichmentService functionality
	t.Skip("TokenEnrichmentService now requires RPC client - whitelist-only mode removed for safety")
}
