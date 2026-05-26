package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Secrets resource — see api/openapi.yaml `tags: [Secrets]`.
//
// Secret values are write-only — list/get responses return metadata only.

// ListSecrets — GET /api/v1/secrets
func (s *Server) ListSecrets(ctx echo.Context, params generated.ListSecretsParams) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	req := &avsproto.ListSecretsReq{IncludeTimestamps: true}
	if params.WorkflowId != nil {
		req.WorkflowId = string(*params.WorkflowId)
	}
	if params.After != nil {
		req.After = string(*params.After)
	}
	if params.Before != nil {
		req.Before = string(*params.Before)
	}
	if params.Limit != nil {
		req.Limit = int64(*params.Limit)
	}

	resp, err := s.engine.ListSecrets(user, req)
	if err != nil {
		return err
	}

	out := generated.SecretList{
		Data:     make([]generated.Secret, 0, len(resp.GetItems())),
		PageInfo: protoPageInfoToOpenAPI(resp.GetPageInfo()),
	}
	for _, item := range resp.GetItems() {
		out.Data = append(out.Data, protoSecretToOpenAPI(item))
	}
	return ctx.JSON(http.StatusOK, out)
}

// PutSecret — PUT /api/v1/secrets/{name}
//
// Idempotent create-or-replace. The engine's CreateSecret returns false
// if a secret with the same (name, scope) already exists; in that case
// we fall through to UpdateSecret so PUT semantics hold.
func (s *Server) PutSecret(ctx echo.Context, name string) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	var body generated.PutSecretRequest
	if err := ctx.Bind(&body); err != nil {
		return badRequest("SECRETS_BAD_REQUEST", "Invalid request body", err.Error())
	}
	if body.Value == "" {
		return badRequest("SECRETS_BAD_VALUE", "Secret value cannot be empty", "PutSecret expects a non-empty value.")
	}

	req := &avsproto.CreateOrUpdateSecretReq{
		Name:   name,
		Secret: body.Value,
	}
	if body.WorkflowId != nil {
		req.WorkflowId = string(*body.WorkflowId)
	}
	if body.OrgId != nil {
		req.OrgId = *body.OrgId
	}

	created, err := s.engine.CreateSecret(user, req)
	if err == nil && created {
		return ctx.NoContent(http.StatusCreated)
	}
	// CreateSecret returns (false, nil) when the secret already exists.
	// Fall back to UpdateSecret to honor PUT idempotency.
	if _, updateErr := s.engine.UpdateSecret(user, req); updateErr != nil {
		if err != nil {
			return err
		}
		return updateErr
	}
	return ctx.NoContent(http.StatusNoContent)
}

// DeleteSecret — DELETE /api/v1/secrets/{name}
func (s *Server) DeleteSecret(ctx echo.Context, name string, params generated.DeleteSecretParams) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	req := &avsproto.DeleteSecretReq{Name: name}
	if params.WorkflowId != nil {
		req.WorkflowId = string(*params.WorkflowId)
	}
	if params.OrgId != nil {
		req.OrgId = *params.OrgId
	}

	if _, err := s.engine.DeleteSecret(user, req); err != nil {
		return err
	}
	return ctx.NoContent(http.StatusNoContent)
}

// protoSecretToOpenAPI maps the proto Secret (metadata only) to the
// OpenAPI Secret shape. The secret value is never carried through —
// it's write-only.
func protoSecretToOpenAPI(in *avsproto.Secret) generated.Secret {
	out := generated.Secret{
		Name:  in.GetName(),
		Scope: generated.SecretScope(in.GetScope()),
	}
	if v := in.GetCreatedAt(); v != 0 {
		out.CreatedAt = &v
	}
	if v := in.GetUpdatedAt(); v != 0 {
		out.UpdatedAt = &v
	}
	if w := in.GetWorkflowId(); w != "" {
		wid := generated.Ulid(w)
		out.WorkflowId = &wid
	}
	if o := in.GetOrgId(); o != "" {
		out.OrgId = &o
	}
	return out
}
