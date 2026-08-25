package rest

import (
	"fmt"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"

	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// Permission levels are the minimal credential class an operation needs.
// Handlers call ensurePermission(op); they do not pick requireUser vs
// partner helpers ad hoc. See GATEWAY_CHANGE_REQUIRED_PARTNER_GATED_READS.md.
type Level string

const (
	// LevelPublic — no credentials (health, auth exchange).
	LevelPublic Level = "public"

	// LevelPartnerRead — registered partner with scope "read" only.
	// Token metadata and similar non-secret chain reads. No user JWT.
	LevelPartnerRead Level = "partner_read"

	// LevelPartnerWalletPreview — partner assertion (scope "read") with a
	// concrete EOA `sub`, OR a user JWT. List + create/derive wallets for
	// preview runner resolution.
	LevelPartnerWalletPreview Level = "partner_wallet_preview"

	// LevelUser — wallet-signed Bearer JWT required.
	// Simulate/runNode, workflows, secrets, fund-adjacent wallet ops, etc.
	LevelUser Level = "user"

	// LevelUserRefusePartner — user JWT required; partner assertion header
	// is categorically rejected (session policies / fund authority).
	LevelUserRefusePartner Level = "user_refuse_partner"
)

// OpenAPI operationId constants — keys in permissionMap.
const (
	OpGetHealth    = "getHealth"
	OpAuthExchange = "authExchange"

	OpCreateWorkflow       = "createWorkflow"
	OpListWorkflows        = "listWorkflows"
	OpGetWorkflow          = "getWorkflow"
	OpCancelWorkflow       = "cancelWorkflow"
	OpPauseWorkflow        = "pauseWorkflow"
	OpResumeWorkflow       = "resumeWorkflow"
	OpTriggerWorkflow      = "triggerWorkflow"
	OpSimulateWorkflow     = "simulateWorkflow"
	OpEstimateWorkflowFees = "estimateWorkflowFees"
	OpCountWorkflows       = "countWorkflows"

	OpListExecutions            = "listExecutions"
	OpListExecutionsForWorkflow = "listExecutionsForWorkflow"
	OpGetExecution              = "getExecution"
	OpGetExecutionStatus        = "getExecutionStatus"
	OpSignalExecution           = "signalExecution"
	OpStreamExecution           = "streamExecution"
	OpCountExecutions           = "countExecutions"
	OpExecutionStats            = "executionStats"

	OpListWallets    = "listWallets"
	OpCreateWallet   = "createWallet"
	OpUpdateWallet   = "updateWallet"
	OpWithdrawWallet = "withdrawWallet"
	OpGetWalletNonce = "getWalletNonce"

	OpPrepareWalletPolicy = "prepareWalletPolicy"
	OpSubmitWalletPolicy  = "submitWalletPolicy"
	OpListWalletPolicies  = "listWalletPolicies"
	OpGetWalletPolicy     = "getWalletPolicy"
	OpRevokeWalletPolicy  = "revokeWalletPolicy"

	OpListSecrets  = "listSecrets"
	OpPutSecret    = "putSecret"
	OpDeleteSecret = "deleteSecret"

	OpGetToken = "getToken"

	OpRunNode    = "runNode"
	OpGetUserOp  = "getUserOp"
	OpRunTrigger = "runTrigger"

	OpListOperators = "listOperators"
)

// permissionMap is the single source of truth: operationId → minimal level.
// Fail closed: ensurePermission rejects unknown ops.
var permissionMap = map[string]Level{
	OpGetHealth:    LevelPublic,
	OpAuthExchange: LevelPublic,

	// Partner-gated public-ish reads (no user JWT).
	OpGetToken: LevelPartnerRead,

	// Preview wallet resolve: list + create/derive under partner (or JWT).
	OpListWallets:  LevelPartnerWalletPreview,
	OpCreateWallet: LevelPartnerWalletPreview,

	// Simulate family: user JWT only (partner alone insufficient).
	OpSimulateWorkflow: LevelUser,
	OpRunNode:          LevelUser,
	OpGetUserOp:        LevelUser,
	OpRunTrigger:       LevelUser,

	// User-owned workflows / executions / secrets / wallet fund surface.
	OpCreateWorkflow:            LevelUser,
	OpListWorkflows:             LevelUser,
	OpGetWorkflow:               LevelUser,
	OpCancelWorkflow:            LevelUser,
	OpPauseWorkflow:             LevelUser,
	OpResumeWorkflow:            LevelUser,
	OpTriggerWorkflow:           LevelUser,
	OpEstimateWorkflowFees:      LevelUser,
	OpCountWorkflows:            LevelUser,
	OpListExecutions:            LevelUser,
	OpListExecutionsForWorkflow: LevelUser,
	OpGetExecution:              LevelUser,
	OpGetExecutionStatus:        LevelUser,
	OpSignalExecution:           LevelUser,
	OpStreamExecution:           LevelUser,
	OpCountExecutions:           LevelUser,
	OpExecutionStats:            LevelUser,
	OpListSecrets:               LevelUser,
	OpPutSecret:                 LevelUser,
	OpDeleteSecret:              LevelUser,
	OpUpdateWallet:              LevelUser,
	OpWithdrawWallet:            LevelUser,
	OpGetWalletNonce:            LevelUser,

	// Fund authority: user JWT + explicit partner refusal.
	OpPrepareWalletPolicy: LevelUserRefusePartner,
	OpSubmitWalletPolicy:  LevelUserRefusePartner,
	OpListWalletPolicies:  LevelUserRefusePartner,
	OpGetWalletPolicy:     LevelUserRefusePartner,
	OpRevokeWalletPolicy:  LevelUserRefusePartner,

	// Monitoring: remains open (matches today's handler; product may lock later).
	OpListOperators: LevelPublic,
}

// Principal is the result of ensurePermission: who may act and at which level.
type Principal struct {
	// User is the engine-facing subject. Non-nil for every level except
	// LevelPublic (and may be a zero-address User for partner-only metadata
	// when assertion `sub` is empty).
	User *model.User
	// PartnerID is set when a partner assertion authorized the call.
	PartnerID string
	Level     Level
}

// ensurePermission authorizes the request for the given OpenAPI operationId
// using permissionMap. Handlers should call this first and use Principal.User
// for engine calls.
func (s *Server) ensurePermission(ctx echo.Context, op string) (*Principal, error) {
	level, ok := permissionMap[op]
	if !ok {
		return nil, &restmw.HTTPError{
			Status: http.StatusInternalServerError,
			Code:   "AUTHZ_UNMAPPED",
			Title:  "Operation not in permission map",
			Detail: fmt.Sprintf("operation %q has no permission level; add it to permissionMap", op),
		}
	}

	switch level {
	case LevelPublic:
		return &Principal{Level: level}, nil

	case LevelPartnerRead:
		return s.authPartnerRead(ctx, level)

	case LevelPartnerWalletPreview:
		return s.authPartnerWalletPreview(ctx, level)

	case LevelUser:
		user, err := s.requireUser(ctx)
		if err != nil {
			return nil, err
		}
		return &Principal{User: user, Level: level}, nil

	case LevelUserRefusePartner:
		if err := refusePartnerAssertion(ctx, op); err != nil {
			return nil, err
		}
		user, err := s.requireUser(ctx)
		if err != nil {
			return nil, err
		}
		return &Principal{User: user, Level: level}, nil

	default:
		return nil, &restmw.HTTPError{
			Status: http.StatusInternalServerError,
			Code:   "AUTHZ_UNKNOWN_LEVEL",
			Title:  "Unknown permission level",
			Detail: fmt.Sprintf("level %q is not implemented", level),
		}
	}
}

// authPartnerRead requires a valid partner assertion with scope "read".
// User JWT alone is not enough (partner-gated gateway traffic).
func (s *Server) authPartnerRead(ctx echo.Context, level Level) (*Principal, error) {
	pp, err := s.verifyPartnerAssertion(ctx, scopeRead)
	if err != nil {
		return nil, err
	}
	if pp == nil {
		return nil, &restmw.HTTPError{
			Status: http.StatusUnauthorized,
			Code:   "PARTNER_REQUIRED",
			Title:  "Partner assertion required",
			Detail: "This endpoint requires a partner assertion (X-Partner-Assertion) with scope \"read\".",
		}
	}
	user := userFromPartnerSubject(pp.subject)
	s.logger.Info("partner-gated read call",
		"partner_id", pp.partnerID,
		"subject", pp.subject,
		"level", string(level),
	)
	return &Principal{User: user, PartnerID: pp.partnerID, Level: level}, nil
}

// authPartnerWalletPreview: user JWT if present, else partner read + EOA sub.
func (s *Server) authPartnerWalletPreview(ctx echo.Context, level Level) (*Principal, error) {
	if authed := restmw.UserFromContext(ctx); authed != nil {
		user, err := s.requireUser(ctx)
		if err != nil {
			return nil, err
		}
		return &Principal{User: user, Level: level}, nil
	}

	pp, err := s.verifyPartnerAssertion(ctx, scopeRead)
	if err != nil {
		return nil, err
	}
	if pp == nil {
		return nil, &restmw.HTTPError{
			Status: http.StatusUnauthorized,
			Code:   "AUTH_REQUIRED",
			Title:  "Authentication required",
			Detail: "This endpoint requires a Bearer JWT (POST /api/v1/auth:exchange) or a partner assertion (X-Partner-Assertion) with scope \"read\" and sub set to the owner EOA.",
		}
	}
	// Concrete owner EOA required: opaque ids and the zero address are
	// rejected (zero would collapse every misconfigured partner call onto
	// one shared non-user owner). Mirrors the pre-permission-map check.
	if !common.IsHexAddress(pp.subject) || common.HexToAddress(pp.subject) == (common.Address{}) {
		return nil, &restmw.HTTPError{
			Status: http.StatusBadRequest,
			Code:   "PARTNER_SUBJECT_REQUIRED",
			Title:  "Owner address required",
			Detail: "Wallet derivation requires the assertion `sub` to be the end-user's non-zero 0x EOA address.",
		}
	}
	user := userFromPartnerSubject(pp.subject)
	s.logger.Info("partner-delegated wallet preview call",
		"partner_id", pp.partnerID,
		"subject", pp.subject,
	)
	return &Principal{User: user, PartnerID: pp.partnerID, Level: level}, nil
}

func userFromPartnerSubject(subject string) *model.User {
	user := &model.User{}
	if common.IsHexAddress(subject) {
		user.Address = common.HexToAddress(subject)
	}
	return user
}
