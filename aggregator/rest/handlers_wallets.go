package rest

import (
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/walletinventory"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/avsclient"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Wallets resource — see api/openapi.yaml `tags: [Wallets]`.
//
// Derived identity operations use the owner-wide registry. Network hints
// select runtime context, including designated EOA and nonce observations.

// ListWallets — GET /api/v1/wallets
//
// No-fund: lists the smart wallets derived/owned by the authenticated owner
// (the JWT subject, or a partner assertion's `sub`). Scoped to that owner, so
// it never exposes another user's wallets. Partner-delegatable to resolve a
// preview's runner — see ensurePermission (partner_wallet_preview).
//
// Chain routing precedence: ?chainId override → JWT aud → gateway default,
// matching CreateWallet. This selects runtime context only; all supported
// networks return the same derived identities, visibility and owner usage.
func (s *Server) ListWallets(ctx echo.Context, params generated.ListWalletsParams) error {
	p, err := s.ensurePermission(ctx, OpListWallets)
	if err != nil {
		return err
	}
	user := p.User

	if params.ChainId != nil {
		user.ChainID = int64(*params.ChainId)
	}

	rows, usage, err := s.engine.InventoryWallets(user, &avsproto.ListWalletReq{})
	if err != nil {
		return walletInventoryError(err)
	}
	out := generated.WalletList{Data: make([]generated.Wallet, 0, len(rows)), Usage: &generated.WalletUsage{
		Scope: generated.WalletUsageScope(usage.Scope), ActiveCount: usage.ActiveCount, ActiveLimit: usage.ActiveLimit, Revision: strconv.FormatUint(usage.Revision, 10),
	}}
	for _, w := range rows {
		out.Data = append(out.Data, modelWalletToOpenAPI(w))
	}
	return ctx.JSON(http.StatusOK, out)
}

// CreateWallet — POST /api/v1/wallets
//
// Idempotent "ensure exists" — engine.GetWallet derives the CREATE2
// address from (owner, salt, factory) and persists the wallet record
// when one doesn't already exist. POST (not GET) because it has a
// side effect even when the address already existed. SetWallet is
// distinct — it only flips isHidden on an existing record and 404s
// on first use, so it's reserved for UpdateWallet.
//
// Chain routing precedence: body.chainId override → JWT aud → gateway
// default. It selects derivation/runtime context while identity admission
// remains global (the JWT proves chain-independent EOA ownership).
func (s *Server) CreateWallet(ctx echo.Context) error {
	// No-fund: derives the counterfactual CREATE2 address from (owner, salt,
	// factory) and records it. Partner-delegatable so a preview can resolve a
	// social user's runner before any wallet is deployed — see
	// ensurePermission (partner_wallet_preview).
	p, err := s.ensurePermission(ctx, OpCreateWallet)
	if err != nil {
		return err
	}
	user := p.User

	var body generated.CreateWalletRequest
	if err := ctx.Bind(&body); err != nil {
		return badRequest("WALLETS_BAD_REQUEST", "Invalid request body", err.Error())
	}
	if body.Salt == "" {
		return badRequest("WALLETS_BAD_SALT", "salt is required", "Wallet derivation needs a salt to compute the CREATE2 address.")
	}

	if body.ChainId != nil {
		user.ChainID = *body.ChainId
	}

	req := &avsproto.GetWalletReq{
		Salt: body.Salt,
	}
	if body.FactoryAddress != nil {
		req.FactoryAddress = string(*body.FactoryAddress)
	}

	resp, err := s.engine.GetWalletWithContext(ctx.Request().Context(), user, req)
	if err != nil {
		return walletInventoryError(err)
	}
	out := protoWalletRespToOpenAPI(resp)
	stored, err := s.engine.StoredWallet(s.engine.ResolveUserChainID(user), user.Address, resp.Address)
	if err != nil {
		return err
	}
	setWalletIdentity(&out, stored)
	return ctx.JSON(http.StatusCreated, out)
}

// UpdateWallet — PATCH /api/v1/wallets/{address}
//
// Toggle visibility for this exact stored address. Derived visibility and
// restore admission are global; designated EOA visibility stays per network.
func (s *Server) UpdateWallet(ctx echo.Context, address generated.EthereumAddress) error {
	p, err := s.ensurePermission(ctx, OpUpdateWallet)
	if err != nil {
		return err
	}
	user := p.User

	var body struct {
		IsHidden *bool `json:"isHidden"`
	}
	if err := ctx.Bind(&body); err != nil {
		return badRequest("WALLETS_BAD_REQUEST", "Invalid request body", err.Error())
	}
	if body.IsHidden == nil {
		return badRequest("WALLETS_NO_FIELDS", "No updatable fields supplied", "isHidden is the only supported field today.")
	}

	chainID := s.engine.ResolveUserChainID(user)
	updated, err := s.engine.SetStoredWalletHidden(chainID, user.Address, string(address), *body.IsHidden)
	if err != nil {
		return walletInventoryError(err)
	}
	return ctx.JSON(http.StatusOK, modelWalletToOpenAPI(updated))
}

// WithdrawWallet — POST /api/v1/wallets/{address}:withdraw
//
// Routes through the WithdrawService dependency the aggregator wires
// in at startup. The full UserOp + paymaster + balance-checking
// pipeline lives in aggregator package — REST is a thin adapter.
func (s *Server) WithdrawWallet(ctx echo.Context, address generated.EthereumAddress) error {
	p, err := s.ensurePermission(ctx, OpWithdrawWallet)
	if err != nil {
		return err
	}
	user := p.User
	if s.withdraws == nil {
		return &restmw.HTTPError{
			Status: http.StatusServiceUnavailable,
			Code:   "WITHDRAW_UNAVAILABLE",
			Title:  "Withdraw service not configured",
			Detail: "This aggregator instance was started without a withdraw service. Restart with the gRPC bag wired in.",
		}
	}

	var body generated.WithdrawRequest
	if err := ctx.Bind(&body); err != nil {
		return badRequest("WITHDRAW_BAD_REQUEST", "Invalid request body", err.Error())
	}
	if body.RecipientAddress == "" || body.Amount == "" || body.Token == "" {
		return badRequest("WITHDRAW_MISSING_FIELDS", "Missing required fields",
			"recipientAddress, amount, and token are all required.")
	}

	// Surface-level validation so obviously bad input fails as 400
	// before it reaches the bundler/paymaster pipeline (which would
	// otherwise propagate as an opaque 500).
	if !common.IsHexAddress(string(body.RecipientAddress)) {
		return badRequest("WITHDRAW_BAD_RECIPIENT", "Invalid recipient address",
			"recipientAddress must be a valid 0x-prefixed hex address.")
	}
	if body.Token != "ETH" {
		if !common.IsHexAddress(body.Token) {
			return badRequest("WITHDRAW_BAD_TOKEN", "Invalid token",
				"token must be the literal \"ETH\" or a valid 0x-prefixed ERC-20 address.")
		}
		// The zero address passes IsHexAddress but is never an ERC-20 —
		// balanceOf on it fails downstream with the opaque "no contract
		// code at given address" (Sentry EIGENLAYER-AVS-1J). Reject it here
		// as a 400. Native withdrawals use the literal "ETH".
		if common.HexToAddress(body.Token) == (common.Address{}) {
			return badRequest("WITHDRAW_BAD_TOKEN", "Invalid token",
				"token cannot be the zero address; use the literal \"ETH\" to withdraw native currency.")
		}
	}
	if body.Amount != "max" {
		amt, ok := new(big.Int).SetString(body.Amount, 10)
		if !ok {
			return badRequest("WITHDRAW_BAD_AMOUNT", "Invalid amount",
				"amount must be a decimal integer string (wei) or the literal \"max\".")
		}
		if amt.Sign() <= 0 {
			return badRequest("WITHDRAW_BAD_AMOUNT", "Invalid amount",
				"amount must be a positive integer (got 0 or negative).")
		}
	}

	req := avsclient.WithdrawRequest{
		Owner:              user.Address.Hex(),
		SmartWalletAddress: string(address),
		RecipientAddress:   string(body.RecipientAddress),
		Amount:             body.Amount,
		Token:              body.Token,
	}
	if body.ChainId != nil {
		req.ChainID = *body.ChainId
	} else if authed := restmw.UserFromContext(ctx); authed != nil && authed.ChainID != 0 {
		// Default to the JWT's audience chain when the caller didn't pass
		// one — same pattern as RunNode + SimulateWorkflow. Without this,
		// the withdraw service falls through to the default chain and a
		// sepolia ERC-20 token address returns "no contract code".
		req.ChainID = authed.ChainID
	}

	result, err := s.withdraws.Withdraw(ctx.Request().Context(), req)
	if err != nil {
		return mapWithdrawError(err)
	}
	// Echo enough of the request back on the response that callers can
	// correlate the result without needing to re-state it locally.
	recipient := generated.EthereumAddress(body.RecipientAddress)
	smartWallet := generated.EthereumAddress(address)
	amount := body.Amount
	token := body.Token
	out := generated.WithdrawResponse{
		Status:             generated.WithdrawResponseStatus(result.Status),
		SmartWalletAddress: &smartWallet,
		RecipientAddress:   &recipient,
		Amount:             &amount,
		Token:              &token,
	}
	if result.Message != "" {
		m := result.Message
		out.Message = &m
	}
	if result.UserOpHash != "" {
		h := generated.Hex(result.UserOpHash)
		out.UserOpHash = &h
	}
	if result.TransactionHash != "" {
		t := generated.Hex(result.TransactionHash)
		out.TransactionHash = &t
	}
	if result.SubmittedAt > 0 {
		ts := result.SubmittedAt
		out.SubmittedAt = &ts
	}
	return ctx.JSON(http.StatusOK, out)
}

// mapWithdrawError gives a session-grant refusal from ExecuteWithdraw its
// machine-readable code. Anything else is returned as is.
func mapWithdrawError(err error) error {
	// ExecuteWithdraw refuses a native withdraw the account's session
	// hooks cannot validate. It signals that as a gRPC InvalidArgument,
	// which the problem middleware would render as a 400 with an empty
	// `code` — leaving clients to substring-match the detail. Re-wrap it
	// so the machine-readable code travels in the field built for it,
	// alongside the handler's WITHDRAW_BAD_TOKEN and friends.
	if strings.Contains(err.Error(), taskengine.SessionPolicyNativeNotAllowedCode) {
		return badRequest(taskengine.SessionPolicyNativeNotAllowedCode,
			"Native ETH withdraw not authorized",
			status.Convert(err).Message())
	}
	if strings.Contains(err.Error(), taskengine.SessionPolicyRecipientNotAllowedCode) {
		return badRequest(taskengine.SessionPolicyRecipientNotAllowedCode,
			"Recipient not in native allowlist",
			status.Convert(err).Message())
	}
	if strings.Contains(err.Error(), taskengine.SessionPolicyRecipientNotAcknowledgedCode) {
		return badRequest(taskengine.SessionPolicyRecipientNotAcknowledgedCode,
			"Delegated native recipient not acknowledged",
			status.Convert(err).Message())
	}
	if strings.Contains(err.Error(), taskengine.SessionPolicyRecipientDelegateChangedCode) {
		return badRequest(taskengine.SessionPolicyRecipientDelegateChangedCode,
			"Acknowledged delegate changed",
			status.Convert(err).Message())
	}
	if strings.Contains(err.Error(), taskengine.SessionPolicyRecipientNotEOACode) {
		return badRequest(taskengine.SessionPolicyRecipientNotEOACode,
			"Native recipient is a contract",
			status.Convert(err).Message())
	}
	if strings.Contains(err.Error(), taskengine.SessionPolicyNativeCapExceededCode) {
		return badRequest(taskengine.SessionPolicyNativeCapExceededCode,
			"Native ETH cap exceeded",
			status.Convert(err).Message())
	}
	return err
}

// GetWalletNonce — GET /api/v1/wallets/{address}:getNonce
//
// Returns the nonce for the next operation the owner signs: EntryPoint
// v0.7 getNonce(sender, key) at the owner's fallback-signer key (entity
// 0, global validation, so key 0x1). Modular Account v2 picks the
// validation from the nonce key, and key 0 selects none. The value is
// the full nonce, key<<64 | sequence, ready for userOp.nonce. Used by
// SDK callers building UserOps client-side.
//
// Chain routing precedence: ?chainId= override → JWT aud → gateway
// default. Same pattern as CreateWallet / WithdrawWallet.
func (s *Server) GetWalletNonce(ctx echo.Context, address generated.EthereumAddress, params generated.GetWalletNonceParams) error {
	p, err := s.ensurePermission(ctx, OpGetWalletNonce)
	if err != nil {
		return err
	}
	user := p.User
	if !common.IsHexAddress(string(address)) {
		return badRequest("WALLETS_BAD_ADDRESS", "Invalid wallet address", "The {address} path parameter must be a valid 0x-prefixed hex address.")
	}
	walletAddr := common.HexToAddress(string(address))

	// Resolve the chain as GET /wallets does: ?chainId=, then the JWT
	// aud, then the gateway default, and an unconfigured chain falls
	// back to the default. The ownership check and the nonce read both
	// use it, so a record is only read on its own chain.
	if params.ChainId != nil {
		user.ChainID = int64(*params.ChainId)
	}
	chainID := s.engine.ResolveUserChainID(user)
	// Prefer the per-chain ChainStateReader — in gateway mode this is
	// a worker-routed reader so the gateway no longer issues the
	// EntryPoint nonce eth_call itself. Falls back to the direct-RPC
	// reader (single-chain mode) or, if the registry is empty, to a
	// fresh direct reader wrapping resolveSmartWalletForChain's
	// ethclient (transitional belt-and-braces).
	chainReader := taskengine.GetChainStateReaderForChain(uint64(chainID))
	if chainReader == nil {
		rpc, _ := s.resolveSmartWalletForChain(chainID)
		if rpc == nil {
			return &restmw.HTTPError{
				Status: http.StatusServiceUnavailable,
				Code:   "NONCE_UNAVAILABLE",
				Title:  "Smart wallet RPC not configured",
				Detail: "This aggregator instance was started without a smart-wallet RPC client for the requested chain.",
			}
		}
		chainReader = taskengine.NewDirectChainStateReader(rpc, chainID)
	}

	// Ownership check against the record on the chain being read.
	if stored, dbErr := s.engine.StoredWallet(chainID, user.Address, walletAddr.Hex()); dbErr != nil || stored == nil {
		return &restmw.HTTPError{
			Status: http.StatusNotFound,
			Code:   "WALLETS_NOT_FOUND",
			Title:  "Wallet not found",
			Detail: "No wallet record found for the authenticated user.",
		}
	}

	ownerKey, err := userop.NonceKeyMAv2(userop.FallbackSignerEntityID, userop.ValidationOptionGlobal)
	if err != nil {
		return err
	}
	nonce, err := chainReader.GetEntryPointNonce(ctx.Request().Context(), walletAddr, ownerKey)
	if err != nil {
		return fmt.Errorf("entrypoint nonce read failed: %w", err)
	}
	return ctx.JSON(http.StatusOK, generated.NonceResponse{Nonce: nonce.String()})
}

// ---------------------------------------------------------------------
// Wallet helpers
// ---------------------------------------------------------------------

// protoSmartWalletToOpenAPI maps the proto SmartWallet (used by
// ListWallets) into the OpenAPI Wallet shape.
func protoSmartWalletToOpenAPI(in *avsproto.SmartWallet) generated.Wallet {
	out := generated.Wallet{
		Address:  generated.EthereumAddress(in.GetAddress()),
		Salt:     in.GetSalt(),
		IsHidden: ptrBool(in.GetIsHidden()),
	}
	if f := in.GetFactory(); f != "" {
		fa := generated.EthereumAddress(f)
		out.FactoryAddress = &fa
	}
	if k := in.GetKind(); k != "" {
		wk := generated.WalletKind(k)
		out.Kind = &wk
	}
	if d := in.GetDelegate(); d != "" {
		da := generated.EthereumAddress(d)
		out.Delegate = &da
	}
	return out
}

// protoWalletRespToOpenAPI maps the GetWalletResp (used by SetWallet)
// into the OpenAPI Wallet shape, including the per-status workflow
// counts that GetWalletResp carries but SmartWallet does not.
func protoWalletRespToOpenAPI(in *avsproto.GetWalletResp) generated.Wallet {
	out := generated.Wallet{
		Address:                generated.EthereumAddress(in.GetAddress()),
		Salt:                   in.GetSalt(),
		IsHidden:               ptrBool(in.GetIsHidden()),
		TotalWorkflowCount:     intPtr(int64(in.GetTotalTaskCount())),
		EnabledWorkflowCount:   intPtr(int64(in.GetEnabledTaskCount())),
		CompletedWorkflowCount: intPtr(int64(in.GetCompletedTaskCount())),
		FailedWorkflowCount:    intPtr(int64(in.GetFailedTaskCount())),
		DisabledWorkflowCount:  intPtr(int64(in.GetDisabledTaskCount())),
	}
	if f := in.GetFactoryAddress(); f != "" {
		fa := generated.EthereumAddress(f)
		out.FactoryAddress = &fa
	}
	return out
}

func modelWalletToOpenAPI(in *model.SmartWallet) generated.Wallet {
	out := generated.Wallet{
		Address:  generated.EthereumAddress(in.Address.Hex()),
		IsHidden: ptrBool(in.IsHidden),
	}
	setWalletIdentity(&out, in)
	if in.Salt != nil {
		out.Salt = in.Salt.String()
	}
	if in.Factory != nil {
		fa := generated.EthereumAddress(in.Factory.Hex())
		out.FactoryAddress = &fa
	}
	if in.IsEOA7702() {
		k := generated.WalletKind(in.Kind)
		out.Kind = &k
		if in.Delegate != nil {
			d := generated.EthereumAddress(in.Delegate.Hex())
			out.Delegate = &d
		}
	}
	return out
}

func ptrBool(b bool) *bool { return &b }
func intPtr(v int64) *int64 {
	return &v
}

func setWalletIdentity(out *generated.Wallet, w *model.SmartWallet) {
	if w.WalletID != "" {
		out.WalletId = &w.WalletID
	}
	if w.DeploymentProfile != "" {
		out.DeploymentProfile = &w.DeploymentProfile
	}
}
func walletInventoryError(err error) error {
	switch {
	case errors.Is(err, walletinventory.ErrBadAllocation):
		return badRequest("WALLETS_BAD_REQUEST", "Invalid allocation request", err.Error())
	case errors.Is(err, walletinventory.ErrSaltExhausted):
		return &restmw.HTTPError{Status: http.StatusConflict, Code: "WALLET_SALT_EXHAUSTED", Title: "No automatic wallet salts remain"}
	case errors.Is(err, walletinventory.ErrLimit) || status.Code(err) == codes.ResourceExhausted:
		return &restmw.HTTPError{Status: http.StatusTooManyRequests, Code: "WALLETS_LIMIT_REACHED", Title: "Active smart wallet limit reached", Detail: err.Error()}
	case errors.Is(err, walletinventory.ErrKeyReused):
		return &restmw.HTTPError{Status: http.StatusConflict, Code: "IDEMPOTENCY_KEY_REUSED", Title: "Allocation key already used", Detail: err.Error()}
	case errors.Is(err, walletinventory.ErrConflict) || (status.Code(err) == codes.FailedPrecondition && status.Convert(err).Message() == walletinventory.ErrConflict.Error()):
		return &restmw.HTTPError{Status: http.StatusConflict, Code: "WALLET_IDENTITY_CONFLICT", Title: "Wallet identity needs reconciliation", Detail: err.Error()}
	case errors.Is(err, badger.ErrKeyNotFound) || status.Code(err) == codes.NotFound:
		return &restmw.HTTPError{Status: http.StatusNotFound, Code: "WALLETS_NOT_FOUND", Title: "Wallet not found"}
	default:
		return err
	}
}

// AllocateWallet is owner-authenticated; partner preview may ensure an explicit
// salt, but it cannot allocate new identities automatically.
func (s *Server) AllocateWallet(ctx echo.Context, params generated.AllocateWalletParams) error {
	principal, err := s.ensurePermission(ctx, OpAllocateWallet)
	if err != nil {
		return err
	}
	var body generated.AllocateWalletRequest
	if ctx.Request().Body != nil {
		if err := ctx.Bind(&body); err != nil && !errors.Is(err, io.EOF) {
			return badRequest("WALLETS_BAD_REQUEST", "Invalid allocation request", err.Error())
		}
	}
	if !walletinventory.ValidAllocationKey(params.IdempotencyKey) {
		return badRequest("WALLETS_BAD_KEY", "Invalid allocation key", "Use 1 to 256 printable ASCII characters without spaces for Idempotency-Key.")
	}
	profile := ""
	if body.DeploymentProfile != nil {
		profile = *body.DeploymentProfile
	}
	if body.ChainId != nil {
		principal.User.ChainID = int64(*body.ChainId)
	}
	wallet, err := s.engine.AllocateWallet(ctx.Request().Context(), principal.User, params.IdempotencyKey, profile)
	if err != nil {
		return walletInventoryError(err)
	}
	return ctx.JSON(http.StatusCreated, modelWalletToOpenAPI(wallet))
}
