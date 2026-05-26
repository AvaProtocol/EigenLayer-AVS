package rest

import (
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
)

// Tokens resource — see api/openapi.yaml `tags: [Tokens]`.

// GetToken — GET /api/v1/tokens/{address}
//
// Multi-chain: chainId query param selects which chain's token
// enrichment service answers. Returns `found: false` if the token is not
// in the whitelist and the on-chain lookup fails.
func (s *Server) GetToken(ctx echo.Context, address generated.EthereumAddress, params generated.GetTokenParams) error {
	return s.notImplemented(ctx, "tokens.retrieve")
}
