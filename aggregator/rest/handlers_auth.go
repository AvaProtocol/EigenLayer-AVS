package rest

import "github.com/labstack/echo/v4"

// AuthExchange — POST /api/v1/auth:exchange
//
// Verify an EIP-191 personal_sign signature and issue a JWT bound to the
// signer's EOA. The signed message must use the canonical template with
// the EigenLayer registration chain id (chain-agnostic auth). SDKs
// construct the message locally; there is no GetSignatureFormat endpoint.
func (s *Server) AuthExchange(ctx echo.Context) error {
	return s.notImplemented(ctx, "auth:exchange")
}
