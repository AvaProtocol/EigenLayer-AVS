package rest

import (
	"net/http"
	"sort"

	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
)

// ListNetworks — GET /api/v1/networks
//
// Public and read-only: the chains this gateway serves, and whether an owner
// can sign a new or changed wallet budget on each right now. Clients offer
// budget review only where `walletBudgetSigning` is true. Unsigned
// preparation does not apply the signing chain list, so without this read a
// client could show a review the owner then signs and submission refuses.
// The answer is the same `BudgetSigningEnabledForChain` rule submission
// enforces, so the two cannot drift.
func (s *Server) ListNetworks(ctx echo.Context) error {
	if _, err := s.ensurePermission(ctx, OpListNetworks); err != nil {
		return err
	}
	chains := s.servedChainIDs()
	data := make([]generated.NetworkInfo, 0, len(chains))
	for _, chainID := range chains {
		data = append(data, generated.NetworkInfo{
			ChainId:             chainID,
			WalletBudgetSigning: s.config.BudgetSigningEnabledForChain(chainID),
		})
	}
	return ctx.JSON(http.StatusOK, generated.NetworkList{Data: data})
}

// servedChainIDs lists each configured chain once, sorted: the gateway's
// `chains`, or the single smart-wallet chain of a non-gateway aggregator.
func (s *Server) servedChainIDs() []int64 {
	if s.config == nil {
		return []int64{}
	}
	seen := map[int64]bool{}
	out := []int64{}
	add := func(chainID int64) {
		if chainID > 0 && !seen[chainID] {
			seen[chainID] = true
			out = append(out, chainID)
		}
	}
	for _, chain := range s.config.Chains {
		if chain != nil {
			add(chain.ChainID)
		}
	}
	if len(out) == 0 && s.config.SmartWallet != nil {
		add(s.config.SmartWallet.ChainID)
	}
	sort.Slice(out, func(left, right int) bool { return out[left] < out[right] })
	return out
}
