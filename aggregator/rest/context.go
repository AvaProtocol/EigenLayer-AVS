package rest

import (
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"

	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// requireUser pulls the JWT-authenticated subject from the Echo context
// and adapts it to the *model.User shape engine methods expect. Returns
// a structured 401 if no JWT was presented (the middleware accepts
// anonymous requests at the framework level so health checks pass; each
// authed handler enforces the requirement here).
//
// SmartAccountAddress is intentionally left nil. The handlers that need
// a derived smart wallet (the /wallets surface) populate it via
// engine.LoadDefaultSmartWallet at call time rather than paying the RPC
// cost on every request — the engine already caches the result.
func (s *Server) requireUser(ctx echo.Context) (*model.User, error) {
	authed := restmw.UserFromContext(ctx)
	if authed == nil || authed.Subject == "" {
		return nil, &restmw.HTTPError{
			Status: http.StatusUnauthorized,
			Code:   "AUTH_REQUIRED",
			Title:  "Authentication required",
			Detail: "This endpoint requires a Bearer JWT. Obtain one via POST /api/v1/auth:exchange.",
		}
	}
	// The zero address is hex-shaped, so IsHexAddress alone let a token
	// bound to it through. Nobody can sign as 0x0, so such a token is
	// either a create-api-key mistake or a forged subject. It used to
	// reach the engine, derive wallets for owner 0x0, and fail deep in the
	// policy prepare path as a 503 (EIGENLAYER-AVS-3C). auth:exchange and
	// the partner wallet-preview path already refuse it; do the same here.
	owner := common.HexToAddress(authed.Subject)
	if !common.IsHexAddress(authed.Subject) || owner == (common.Address{}) {
		return nil, &restmw.HTTPError{
			Status: http.StatusUnauthorized,
			Code:   "AUTH_INVALID_SUBJECT",
			Title:  "Invalid JWT subject",
			Detail: "Subject must be a valid, non-zero 0x-prefixed EOA address.",
		}
	}
	return &model.User{
		Address: owner,
		// Propagate the JWT-derived chain ID. Engine.GetWallet treats
		// this as authoritative for the chain context. SetWallet and
		// ListWallets still use their legacy owner-only signatures and
		// always hit the gateway default — those handlers ignore this
		// field today. Zero falls back to the gateway's default chain.
		ChainID: authed.ChainID,
	}, nil
}
