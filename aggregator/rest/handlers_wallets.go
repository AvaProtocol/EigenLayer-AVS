package rest

import (
	"fmt"
	"math/big"
	"net/http"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
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
	// Sort by salt lexicographically so list ordering is stable and
	// predictable across calls (BadgerDB GetByPrefix doesn't guarantee
	// any particular order).
	sort.SliceStable(out.Data, func(i, j int) bool {
		return out.Data[i].Salt < out.Data[j].Salt
	})
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

	req := &avsproto.GetWalletReq{
		Salt: body.Salt,
	}
	if body.FactoryAddress != nil {
		req.FactoryAddress = string(*body.FactoryAddress)
	}

	resp, err := s.engine.GetWallet(user, req)
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
// Routes through the WithdrawService dependency the aggregator wires
// in at startup. The full UserOp + paymaster + balance-checking
// pipeline lives in aggregator package — REST is a thin adapter.
func (s *Server) WithdrawWallet(ctx echo.Context, address generated.EthereumAddress) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}
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
	if body.Token != "ETH" && !common.IsHexAddress(body.Token) {
		return badRequest("WITHDRAW_BAD_TOKEN", "Invalid token",
			"token must be the literal \"ETH\" or a valid 0x-prefixed ERC-20 address.")
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

	req := WithdrawRequest{
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
		return err
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

// GetWalletNonce — GET /api/v1/wallets/{address}:getNonce
//
// Reads the smart wallet's current nonce off-chain via the
// entrypoint's getNonce(sender, key=0) call. Used by SDK callers
// building UserOps client-side.
func (s *Server) GetWalletNonce(ctx echo.Context, address generated.EthereumAddress) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}
	if s.smartWalletRpc == nil {
		return &restmw.HTTPError{
			Status: http.StatusServiceUnavailable,
			Code:   "NONCE_UNAVAILABLE",
			Title:  "Smart wallet RPC not configured",
			Detail: "This aggregator instance was started without a smart-wallet RPC client.",
		}
	}
	if !common.IsHexAddress(string(address)) {
		return badRequest("WALLETS_BAD_ADDRESS", "Invalid wallet address", "The {address} path parameter must be a valid 0x-prefixed hex address.")
	}
	walletAddr := common.HexToAddress(string(address))

	// Ownership check — the engine's GetWalletFromDB lookup is the
	// canonical source of truth that this wallet belongs to the
	// authenticated user.
	if stored, dbErr := s.engine.GetWalletFromDB(user.Address, walletAddr.Hex()); dbErr != nil || stored == nil {
		return &restmw.HTTPError{
			Status: http.StatusNotFound,
			Code:   "WALLETS_NOT_FOUND",
			Title:  "Wallet not found",
			Detail: "No wallet record found for the authenticated user.",
		}
	}

	if s.config == nil || s.config.SmartWallet == nil {
		return &restmw.HTTPError{
			Status: http.StatusInternalServerError,
			Code:   "WALLETS_NO_CONFIG",
			Title:  "Smart wallet config missing",
			Detail: "Aggregator has no smart wallet config; nonce lookup unavailable.",
		}
	}
	nonce, err := aa.GetNonce(s.smartWalletRpc, walletAddr, big.NewInt(0))
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
