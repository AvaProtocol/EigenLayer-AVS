package rest

import (
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
)

// Secrets resource — see api/openapi.yaml `tags: [Secrets]`.
//
// Secret values are write-only — list/get responses return metadata only.

// ListSecrets — GET /api/v1/secrets
func (s *Server) ListSecrets(ctx echo.Context, params generated.ListSecretsParams) error {
	return s.notImplemented(ctx, "secrets.list")
}

// PutSecret — PUT /api/v1/secrets/{name}
//
// Idempotent create-or-replace. The request body's `value` field is
// write-only; never echoed back in any response.
func (s *Server) PutSecret(ctx echo.Context, name string) error {
	return s.notImplemented(ctx, "secrets.put")
}

// DeleteSecret — DELETE /api/v1/secrets/{name}
func (s *Server) DeleteSecret(ctx echo.Context, name string, params generated.DeleteSecretParams) error {
	return s.notImplemented(ctx, "secrets.delete")
}
