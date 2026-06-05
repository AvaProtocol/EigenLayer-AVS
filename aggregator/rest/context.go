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
	if !common.IsHexAddress(authed.Subject) {
		return nil, &restmw.HTTPError{
			Status: http.StatusUnauthorized,
			Code:   "AUTH_INVALID_SUBJECT",
			Title:  "Invalid JWT subject",
			Detail: "Subject must be a valid 0x-prefixed EOA address.",
		}
	}
	return &model.User{
		Address: common.HexToAddress(authed.Subject),
		// Propagate the JWT-derived chain ID. The engine's wallet RPC
		// handlers (GetWallet/SetWallet/ListWallets) treat this as
		// authoritative for the chain context; zero falls back to the
		// gateway's default chain (which is also what the gRPC path uses).
		ChainID: authed.ChainID,
	}, nil
}
