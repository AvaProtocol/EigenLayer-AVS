package rest

import (
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
)

// /wallets/{address}/policies — the grant screen's backend.
//
// Granting is prepare → sign → submit (master doc §7.5 as reshaped by
// §7.4a): prepare allocates and returns the EIP-712 payload, the owner's
// wallet signs it, submit verifies and persists. Prepare stores nothing;
// submit trusts nothing — the grant is recomputed from the echoed fields and
// the signature is the only authority.
//
// Every handler leads with refusePartnerAssertion: a policy is fund
// authority, and the refusal must be explicit rather than a handler that
// silently never consults the header.

const policyCapability = "Session-policy management"

// policyChainID resolves the chain: explicit value wins, then the JWT's
// audience chain. Zero means unresolvable (single-chain deployments have an
// aud; a request with neither is malformed).
func policyChainID(ctx echo.Context, explicit *int64) int64 {
	if explicit != nil && *explicit > 0 {
		return *explicit
	}
	if authed := restmw.UserFromContext(ctx); authed != nil {
		return authed.ChainID
	}
	return 0
}

// policyAuth is the shared preamble: partner refusal, then user auth, then
// path-address validation.
func (s *Server) policyAuth(ctx echo.Context, address generated.EthereumAddress) (*model.User, common.Address, error) {
	if err := refusePartnerAssertion(ctx, policyCapability); err != nil {
		return nil, common.Address{}, err
	}
	user, err := s.requireUser(ctx)
	if err != nil {
		return nil, common.Address{}, err
	}
	if !common.IsHexAddress(string(address)) {
		return nil, common.Address{}, badRequest("POLICIES_BAD_ADDRESS", "Invalid wallet address",
			"The {address} path parameter must be a valid 0x-prefixed hex address.")
	}
	return user, common.HexToAddress(string(address)), nil
}

// PrepareWalletPolicy allocates a grant and returns the payload to sign.
func (s *Server) PrepareWalletPolicy(ctx echo.Context, address generated.EthereumAddress) error {
	user, wallet, err := s.policyAuth(ctx, address)
	if err != nil {
		return err
	}
	var req generated.PreparePolicyRequest
	if err := ctx.Bind(&req); err != nil {
		return badRequest("POLICIES_BAD_BODY", "Invalid request body", err.Error())
	}
	// Matches the published contract (openapi.yaml minimum: 60): a
	// sub-minute grant expires before its first operation can mine.
	if req.ExpiresInSeconds < 60 {
		return badRequest("POLICIES_BAD_EXPIRY", "Invalid expiry", "expiresInSeconds must be at least 60.")
	}
	perms, err := permissionsFromAPI(req.AllowedActions, &req.Erc20SpendCap, nowMs()+req.ExpiresInSeconds*1000)
	if err != nil {
		return badRequest("POLICIES_BAD_PERMISSIONS", "Invalid permissions", err.Error())
	}

	prepared, err := s.engine.PrepareSessionPolicy(user, taskengine.SessionPolicyInput{
		Wallet:        wallet,
		ChainID:       int64(req.ChainId),
		AgentLabel:    req.AgentLabel,
		Justification: deref(req.Justification),
		Permissions:   perms,
	})
	if err != nil {
		return mapPolicyError(err)
	}

	policy := prepared.Policy
	typedData, err := typedDataJSON(policy)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, generated.PreparedPolicy{
		PolicyId:      generated.Ulid(policy.ID),
		ChainId:       generated.ChainId(policy.ChainID),
		EntityId:      int64(policy.EntityID),
		SessionSigner: generated.EthereumAddress(policy.SessionSigner.Hex()),
		Deadline:      int64(policy.Grant.Deadline),
		ValidUntil:    policy.ValidUntil,
		Digest:        prepared.Digest.Hex(),
		TypedData:     typedData,
	})
}

// SubmitWalletPolicy verifies the owner's signature and stores the grant.
func (s *Server) SubmitWalletPolicy(ctx echo.Context, address generated.EthereumAddress) error {
	user, wallet, err := s.policyAuth(ctx, address)
	if err != nil {
		return err
	}
	var req generated.SubmitPolicyRequest
	if err := ctx.Bind(&req); err != nil {
		return badRequest("POLICIES_BAD_BODY", "Invalid request body", err.Error())
	}
	signature := common.FromHex(req.Signature)
	if len(signature) != 65 {
		return badRequest("POLICIES_BAD_SIGNATURE", "Invalid signature",
			"signature must be 65 hex-encoded bytes (r ++ s ++ v).")
	}
	if req.EntityId <= 0 || req.EntityId > int64(^uint32(0)) {
		return badRequest("POLICIES_BAD_ENTITY", "Invalid entity id", "entityId is out of range.")
	}
	if req.Deadline <= 0 {
		return badRequest("POLICIES_BAD_DEADLINE", "Invalid deadline", "deadline must be positive.")
	}
	perms, err := permissionsFromAPI(req.AllowedActions, &req.Erc20SpendCap, req.ValidUntil)
	if err != nil {
		return badRequest("POLICIES_BAD_PERMISSIONS", "Invalid permissions", err.Error())
	}

	policy, err := s.engine.SubmitSessionPolicy(user, taskengine.SessionPolicyInput{
		Wallet:        wallet,
		ChainID:       int64(req.ChainId),
		AgentLabel:    req.AgentLabel,
		Justification: deref(req.Justification),
		Permissions:   perms,
	}, string(req.PolicyId), uint32(req.EntityId), uint64(req.Deadline), signature)
	if err != nil {
		return mapPolicyError(err)
	}
	return ctx.JSON(http.StatusCreated, policyToAPI(policy))
}

// ListWalletPolicies lists the wallet's grants, newest first.
func (s *Server) ListWalletPolicies(ctx echo.Context, address generated.EthereumAddress, params generated.ListWalletPoliciesParams) error {
	user, wallet, err := s.policyAuth(ctx, address)
	if err != nil {
		return err
	}
	chainID := policyChainID(ctx, (*int64)(params.ChainId))
	if chainID <= 0 {
		return badRequest("POLICIES_BAD_CHAIN_ID", "Chain unresolvable",
			"Provide ?chainId= or authenticate with a chain-scoped token.")
	}
	policies, err := s.engine.ListSessionPoliciesForWallet(user, chainID, wallet)
	if err != nil {
		return mapPolicyError(err)
	}
	items := make([]generated.SessionPolicy, 0, len(policies))
	for _, p := range policies {
		items = append(items, policyToAPI(p))
	}
	return ctx.JSON(http.StatusOK, generated.SessionPolicyList{Items: items})
}

// GetWalletPolicy returns one grant.
func (s *Server) GetWalletPolicy(ctx echo.Context, address generated.EthereumAddress, policyID generated.Ulid, params generated.GetWalletPolicyParams) error {
	user, wallet, err := s.policyAuth(ctx, address)
	if err != nil {
		return err
	}
	chainID := policyChainID(ctx, (*int64)(params.ChainId))
	if chainID <= 0 {
		return badRequest("POLICIES_BAD_CHAIN_ID", "Chain unresolvable",
			"Provide ?chainId= or authenticate with a chain-scoped token.")
	}
	policy, err := s.engine.GetSessionPolicyByID(user, chainID, wallet, string(policyID))
	if err != nil {
		return mapPolicyError(err)
	}
	return ctx.JSON(http.StatusOK, policyToAPI(policy))
}

// RevokeWalletPolicy revokes one grant.
func (s *Server) RevokeWalletPolicy(ctx echo.Context, address generated.EthereumAddress, policyID generated.Ulid, params generated.RevokeWalletPolicyParams) error {
	user, wallet, err := s.policyAuth(ctx, address)
	if err != nil {
		return err
	}
	chainID := policyChainID(ctx, (*int64)(params.ChainId))
	if chainID <= 0 {
		return badRequest("POLICIES_BAD_CHAIN_ID", "Chain unresolvable",
			"Provide ?chainId= or authenticate with a chain-scoped token.")
	}
	deleted, cleanupRequired, err := s.engine.RevokeSessionPolicyByID(user, chainID, wallet, string(policyID))
	if err != nil {
		return mapPolicyError(err)
	}
	status := generated.RevokePolicyResponseStatus("revoked")
	if deleted {
		status = generated.RevokePolicyResponseStatus("deleted")
	}
	return ctx.JSON(http.StatusOK, generated.RevokePolicyResponse{
		Status:                 status,
		OnChainCleanupRequired: cleanupRequired,
	})
}

// ── mapping ───────────────────────────────────────────────────────────────

// permissionsFromAPI translates wire permissions into the engine's set.
func permissionsFromAPI(actions []generated.AllowedAction, spendCap *generated.Erc20SpendCap, validUntilMs int64) (taskengine.SessionPermissions, error) {
	perms := taskengine.SessionPermissions{ValidUntilMs: validUntilMs}
	for _, a := range actions {
		if !common.IsHexAddress(string(a.Target)) {
			return perms, errors.New("allowed action target is not a valid address")
		}
		target := common.HexToAddress(string(a.Target))
		perms.AllowedActions = append(perms.AllowedActions, model.AllowedAction{
			Target:    &target,
			Selectors: a.Selectors,
		})
	}
	if spendCap != nil {
		if !common.IsHexAddress(string(spendCap.Token)) {
			return perms, errors.New("spend cap token is not a valid address")
		}
		token := common.HexToAddress(string(spendCap.Token))
		perms.SpendCap = &model.ERC20SpendCap{Token: &token, Amount: spendCap.Amount}
	}
	return perms, nil
}

// policyToAPI renders a policy for responses. Grant material — calldata,
// nonce, the owner's signature — is secret-grade and never leaves storage.
func policyToAPI(p *model.SessionPolicy) generated.SessionPolicy {
	out := generated.SessionPolicy{
		Id:         generated.Ulid(p.ID),
		ChainId:    generated.ChainId(p.ChainID),
		Status:     generated.SessionPolicyStatus(p.Status),
		EntityId:   int64(p.EntityID),
		AgentLabel: p.AgentLabel,
		ValidUntil: p.ValidUntil,
		CreatedAt:  p.CreatedAt,
	}
	if p.Runner != nil {
		out.Runner = generated.EthereumAddress(p.Runner.Hex())
	}
	if p.SessionSigner != nil {
		out.SessionSigner = generated.EthereumAddress(p.SessionSigner.Hex())
	}
	if p.Justification != "" {
		out.Justification = &p.Justification
	}
	if len(p.AllowedActions) > 0 {
		actions := make([]generated.AllowedAction, 0, len(p.AllowedActions))
		for _, a := range p.AllowedActions {
			if a.Target == nil {
				continue
			}
			actions = append(actions, generated.AllowedAction{
				Target:    generated.EthereumAddress(a.Target.Hex()),
				Selectors: a.Selectors,
			})
		}
		out.AllowedActions = &actions
	}
	if p.ERC20SpendCap != nil && p.ERC20SpendCap.Token != nil {
		out.Erc20SpendCap = &generated.Erc20SpendCap{
			Token:  generated.EthereumAddress(p.ERC20SpendCap.Token.Hex()),
			Amount: p.ERC20SpendCap.Amount,
		}
	}
	return out
}

// typedDataJSON renders the eth_signTypedData_v4 payload for the prepare
// response — built from the SAME fields the digest was computed over, so the
// wallet signs exactly what submit will verify.
func typedDataJSON(policy *model.SessionPolicy) (map[string]interface{}, error) {
	typed := userop.DeferredActionTypedData(
		bigChainID(policy.ChainID), *policy.Runner,
		policy.Grant.CarrierNonce, policy.Grant.Deadline, policy.Grant.InstallCall)
	raw, err := json.Marshal(typed)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// mapPolicyError translates engine sentinels into REST problems.
func mapPolicyError(err error) error {
	switch {
	case errors.Is(err, taskengine.ErrWalletNotOwned), errors.Is(err, taskengine.ErrSessionPolicyNotFound):
		// Existence is not confirmed to non-owners: both cases read as 404.
		return &restmw.HTTPError{Status: http.StatusNotFound, Code: "POLICIES_NOT_FOUND",
			Title: "Not found", Detail: "No such wallet or policy for this account."}
	case errors.Is(err, taskengine.ErrSessionEntityTaken):
		return &restmw.HTTPError{Status: http.StatusConflict, Code: "POLICIES_ENTITY_TAKEN",
			Title: "Grant allocation superseded", Detail: err.Error() + " Prepare the grant again."}
	case strings.Contains(err.Error(), "signed by"):
		return badRequest("POLICIES_BAD_SIGNATURE", "Signature does not match", err.Error())
	default:
		return badRequest("POLICIES_REJECTED", "Policy request rejected", err.Error())
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nowMs() int64 { return time.Now().UnixMilli() }

func bigChainID(chainID int64) *big.Int { return big.NewInt(chainID) }
