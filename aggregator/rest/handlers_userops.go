package rest

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
)

// UserOps resource — see api/openapi.yaml `tags: [UserOps]`.

// GetUserOp — GET /api/v1/userops/{userOpHash}
//
// Re-poll a UserOp this user submitted (G3 / N14.a). Partner assertion
// alone is not enough. Unknown hashes and hashes whose sender is not one
// of the caller's smart wallets both 404 — this is not a public explorer.
func (s *Server) GetUserOp(ctx echo.Context, userOpHash generated.Bytes32, params generated.GetUserOpParams) error {
	p, err := s.ensurePermission(ctx, OpGetUserOp)
	if err != nil {
		return err
	}
	user := p.User

	var chainID int64
	if params.ChainId != nil {
		chainID = int64(*params.ChainId)
	} else if authed := restmw.UserFromContext(ctx); authed != nil && authed.ChainID != 0 {
		chainID = authed.ChainID
	}

	status, err := s.engine.LookupUserOpStatus(ctx.Request().Context(), user, string(userOpHash), chainID)
	if err != nil {
		return mapUserOpLookupError(err)
	}
	return ctx.JSON(http.StatusOK, userOpStatusToOpenAPI(status))
}

// mapUserOpLookupError translates engine lookup sentinels to the REST
// problem codes. errors.Is (not string match) so a `%w` wrap upstream
// still maps; a `%v` wrap would fall through as 500.
func mapUserOpLookupError(err error) error {
	if errors.Is(err, taskengine.ErrUserOpHashInvalid) {
		return badRequest("USEROP_BAD_HASH", "Invalid userOpHash", err.Error())
	}
	if errors.Is(err, taskengine.ErrUserOpChainUnsupported) {
		return badRequest("USEROP_BAD_CHAIN", "Unsupported chain", err.Error())
	}
	if errors.Is(err, taskengine.ErrUserOpNotFound) {
		return &restmw.HTTPError{
			Status: http.StatusNotFound,
			Code:   "USEROP_NOT_FOUND",
			Title:  "User operation not found",
			Detail: "No UserOp with this hash for the authenticated user.",
		}
	}
	return err
}

func userOpStatusToOpenAPI(in *taskengine.UserOpStatus) generated.UserOpStatusResponse {
	out := generated.UserOpStatusResponse{
		UserOpHash:      generated.Bytes32(in.UserOpHash),
		ExecutionStatus: generated.UserOpStatusResponseExecutionStatus(in.ExecutionStatus),
	}
	if in.Sender != "" {
		sender := generated.EthereumAddress(in.Sender)
		out.Sender = &sender
	}
	if in.TransactionHash != "" {
		tx := generated.Hex(in.TransactionHash)
		out.TransactionHash = &tx
	}
	if in.BlockNumber != "" {
		bn := in.BlockNumber
		out.BlockNumber = &bn
	}
	if in.Success != nil {
		out.Success = in.Success
	}
	if len(in.Calls) > 0 {
		calls := make([]generated.UserOpInnerCall, len(in.Calls))
		for i, c := range in.Calls {
			calls[i] = innerCallToOpenAPI(c)
		}
		out.Calls = &calls
	}
	if in.FailedCall != nil {
		fc := innerCallToOpenAPI(*in.FailedCall)
		out.FailedCall = &fc
	}
	return out
}

func innerCallToOpenAPI(c taskengine.InnerCall) generated.UserOpInnerCall {
	return generated.UserOpInnerCall{
		To:       generated.EthereumAddress(c.To),
		Value:    c.Value,
		Selector: c.Selector,
		Data:     generated.Hex(c.Data),
	}
}
