package rest

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
)

func (s *Server) GetWalletBudget(ctx echo.Context, address generated.EthereumAddress, params generated.GetWalletBudgetParams) error {
	user, wallet, err := s.policyAuth(ctx, OpGetWalletBudget, address)
	if err != nil {
		return err
	}
	chainID := policyChainID(ctx, (*int64)(params.ChainId))
	if chainID <= 0 {
		return badRequest("POLICIES_BAD_CHAIN_ID", "Chain unresolvable", "Provide ?chainId= or authenticate with a chain-scoped token.")
	}
	budget, err := s.engine.GetWalletBudget(ctx.Request().Context(), user, chainID, wallet)
	if errors.Is(err, taskengine.ErrBudgetReadUnavailable) {
		return &restmw.HTTPError{Status: http.StatusServiceUnavailable, Code: taskengine.BudgetReadUnavailableCode,
			Title: "Budget couldn't be read", Detail: "Couldn't read the remaining permission limits. Try again."}
	}
	if err != nil {
		return mapPolicyError(err)
	}
	return ctx.JSON(http.StatusOK, walletBudgetToAPI(budget))
}

func walletBudgetToAPI(b *taskengine.WalletBudget) generated.WalletBudget {
	out := generated.WalletBudget{ChainId: b.ChainID, PolicyId: generated.Ulid(b.PolicyID),
		Status: generated.WalletBudgetStatus(b.Status), Sizing: generated.BudgetSizing(b.Sizing), ValidUntil: b.ValidUntil,
		ReadAt: b.ReadAt, Erc20: []generated.BudgetTokenRow{}, UnmeteredPermissions: []generated.UnmeteredPermission{}}
	if b.StartedAt != 0 {
		out.StartedAt = &b.StartedAt
	}
	for _, row := range b.ERC20 {
		out.Erc20 = append(out.Erc20, generated.BudgetTokenRow{Token: generated.EthereumAddress(row.Token.Hex()), Amount: row.Amount,
			Remaining: row.Remaining, Used: row.Used, Unlimited: row.Unlimited})
	}
	if row := b.Native; row != nil {
		out.Native = &generated.BudgetNativeRow{Amount: row.Amount, Remaining: row.Remaining, Used: row.Used, Unlimited: row.Unlimited}
		if row.GasWei != "" {
			out.Native.GasWei = &row.GasWei
		}
	}
	for _, row := range b.UnmeteredPermissions {
		permission := generated.UnmeteredPermission{Kind: generated.UnmeteredPermissionKind(row.Kind), Target: generated.EthereumAddress(row.Target.Hex()), Selectors: row.Selectors, Added: row.Added}
		if row.Token != nil {
			token := generated.EthereumAddress(row.Token.Hex())
			permission.Token = &token
		}
		out.UnmeteredPermissions = append(out.UnmeteredPermissions, permission)
	}
	if len(b.Retirements) != 0 {
		retirements := make([]generated.BudgetRetirement, 0, len(b.Retirements))
		for _, row := range b.Retirements {
			id := generated.Ulid(row.PolicyID)
			retirement := generated.BudgetRetirement{EntityId: int64(row.EntityID), PolicyId: &id, State: generated.BudgetRetirementState(row.State)}
			if len(row.ERC20) != 0 {
				caps := []generated.Erc20SpendCap{}
				for _, cap := range row.ERC20 {
					if cap.Token != nil {
						caps = append(caps, generated.Erc20SpendCap{Token: generated.EthereumAddress(cap.Token.Hex()), Amount: cap.Amount})
					}
				}
				retirement.Erc20 = &caps
			}
			if row.Native != nil {
				retirement.Native = &generated.NativeSpendCap{Amount: row.Native.Amount}
			}
			retirements = append(retirements, retirement)
		}
		out.Retirements = &retirements
	}
	return out
}
