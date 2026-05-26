package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Wallets resource — see api/openapi.yaml `tags: [Wallets]`.
//
// The engine's wallet API has accumulated a few quirks: ListWallets
// takes a bare address (no user struct), CreateWallet doesn't exist —
// SetWallet doubles as upsert. The handlers below hide those quirks
// behind the REST shape so the SDK stays clean.

// ListWallets — GET /api/v1/wallets
func (s *Server) ListWallets(ctx echo.Context) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	resp, err := s.engine.ListWallets(user.Address, &avsproto.ListWalletReq{})
	if err != nil {
		return err
	}

	out := generated.WalletList{Data: make([]generated.Wallet, 0, len(resp.GetItems()))}
	for _, w := range resp.GetItems() {
		out.Data = append(out.Data, protoSmartWalletToOpenAPI(w))
	}
	return ctx.JSON(http.StatusOK, out)
}

// CreateWallet — POST /api/v1/wallets
//
// Idempotent "ensure exists" — engine.SetWallet derives the CREATE2
// address from (owner, salt, factory) and persists the wallet record
// with IsHidden=false. POST (not GET) because it has a side effect even
// if the address already existed.
func (s *Server) CreateWallet(ctx echo.Context) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	var body generated.CreateWalletRequest
	if err := ctx.Bind(&body); err != nil {
		return badRequest("WALLETS_BAD_REQUEST", "Invalid request body", err.Error())
	}
	if body.Salt == "" {
		return badRequest("WALLETS_BAD_SALT", "salt is required", "Wallet derivation needs a salt to compute the CREATE2 address.")
	}

	req := &avsproto.SetWalletReq{
		Salt:     body.Salt,
		IsHidden: false,
	}
	if body.FactoryAddress != nil {
		req.FactoryAddress = string(*body.FactoryAddress)
	}

	resp, err := s.engine.SetWallet(user.Address, req)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, protoWalletRespToOpenAPI(resp))
}

// UpdateWallet — PATCH /api/v1/wallets/{address}
//
// Today's engine only supports toggling IsHidden; that's the one field
// that makes sense to update post-creation. The full SmartWallet record
// is identified by (salt, factory) — we re-resolve those from the
// address via GetWallet and then SetWallet with the new isHidden.
func (s *Server) UpdateWallet(ctx echo.Context, address generated.EthereumAddress) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	var body struct {
		IsHidden *bool `json:"isHidden"`
	}
	if err := ctx.Bind(&body); err != nil {
		return badRequest("WALLETS_BAD_REQUEST", "Invalid request body", err.Error())
	}
	if body.IsHidden == nil {
		return badRequest("WALLETS_NO_FIELDS", "No updatable fields supplied", "isHidden is the only supported field today.")
	}

	// Look up the stored wallet to recover its (salt, factory).
	stored, err := s.engine.GetWalletFromDB(user.Address, string(address))
	if err != nil || stored == nil {
		return &restmw.HTTPError{
			Status: http.StatusNotFound,
			Code:   "WALLETS_NOT_FOUND",
			Title:  "Wallet not found",
			Detail: "No wallet record found for the supplied address; create it first via POST /wallets.",
		}
	}

	req := &avsproto.SetWalletReq{IsHidden: *body.IsHidden}
	if stored.Salt != nil {
		req.Salt = stored.Salt.String()
	}
	if stored.Factory != nil {
		req.FactoryAddress = stored.Factory.Hex()
	}

	resp, err := s.engine.SetWallet(user.Address, req)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, protoWalletRespToOpenAPI(resp))
}

// WithdrawWallet — POST /api/v1/wallets/{address}:withdraw
//
// Withdraw uses the bundler + paymaster path and depends on the
// smart-wallet RPC client held by the gRPC server, not on the engine
// alone. Stubbed until those dependencies are threaded into the REST
// Server (same plumbing change as EstimateWorkflowFees).
func (s *Server) WithdrawWallet(ctx echo.Context, address generated.EthereumAddress) error {
	return s.notImplemented(ctx, "wallets.withdraw")
}

// GetWalletNonce — GET /api/v1/wallets/{address}:getNonce
//
// Reading the smart wallet's nonce needs the smart-wallet RPC client;
// same deferral as WithdrawWallet.
func (s *Server) GetWalletNonce(ctx echo.Context, address generated.EthereumAddress) error {
	return s.notImplemented(ctx, "wallets.getNonce")
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

func ptrBool(b bool) *bool { return &b }
func intPtr(v int64) *int64 {
	return &v
}
