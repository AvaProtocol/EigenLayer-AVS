package rest

import (
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
)

// Wallets resource — see api/openapi.yaml `tags: [Wallets]`.

// ListWallets — GET /api/v1/wallets
func (s *Server) ListWallets(ctx echo.Context) error {
	return s.notImplemented(ctx, "wallets.list")
}

// CreateWallet — POST /api/v1/wallets
//
// Idempotent "ensure exists" — derives the deterministic CREATE2 address
// from (owner, salt, factory) and persists the wallet record. POST (not
// GET) because it has a side effect even if the address already existed.
func (s *Server) CreateWallet(ctx echo.Context) error {
	return s.notImplemented(ctx, "wallets.create")
}

// UpdateWallet — PATCH /api/v1/wallets/{address}
//
// Partial update of wallet properties (e.g., isHidden).
func (s *Server) UpdateWallet(ctx echo.Context, address generated.EthereumAddress) error {
	return s.notImplemented(ctx, "wallets.update")
}

// WithdrawWallet — POST /api/v1/wallets/{address}:withdraw
//
// Sends a UserOp from the smart wallet transferring ETH or an ERC-20 to
// the recipient. Multi-chain: chainId in the request body selects which
// chain's worker executes the UserOp.
func (s *Server) WithdrawWallet(ctx echo.Context, address generated.EthereumAddress) error {
	return s.notImplemented(ctx, "wallets.withdraw")
}

// GetWalletNonce — GET /api/v1/wallets/{address}:getNonce
func (s *Server) GetWalletNonce(ctx echo.Context, address generated.EthereumAddress) error {
	return s.notImplemented(ctx, "wallets.getNonce")
}
