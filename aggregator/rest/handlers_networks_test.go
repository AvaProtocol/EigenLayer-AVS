package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func listNetworks(t *testing.T, cfg *config.Config) generated.NetworkList {
	t.Helper()
	e := echo.New()
	s := &Server{config: cfg, logger: testutil.GetLogger()}
	s.Mount(e)
	// Public: no JWT, no partner assertion.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/networks", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out generated.NetworkList
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func gatewayChains(ids ...int64) []*config.ChainConfig {
	out := make([]*config.ChainConfig, 0, len(ids))
	for _, id := range ids {
		out = append(out, &config.ChainConfig{ChainID: id})
	}
	return out
}

func TestListNetworksReportsBudgetSigningPerChain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     *config.Config
		signing map[int64]bool
	}{
		{
			name: "empty list keeps Sepolia only",
			cfg: &config.Config{
				Chains:                         gatewayChains(8453, 1, 11155111, 84532),
				WalletBudgetPreparationEnabled: true,
				WalletBudgetSigningEnabled:     true,
			},
			signing: map[int64]bool{1: false, 8453: false, 84532: false, 11155111: true},
		},
		{
			name: "activated mainnets",
			cfg: &config.Config{
				Chains:                         gatewayChains(8453, 1, 11155111, 84532),
				WalletBudgetPreparationEnabled: true,
				WalletBudgetSigningEnabled:     true,
				WalletBudgetSigningChains:      []int64{11155111, 1, 8453},
			},
			signing: map[int64]bool{1: true, 8453: true, 84532: false, 11155111: true},
		},
		{
			name: "an unqualified chain in the list stays off",
			cfg: &config.Config{
				Chains:                         gatewayChains(84532),
				WalletBudgetPreparationEnabled: true,
				WalletBudgetSigningEnabled:     true,
				WalletBudgetSigningChains:      []int64{84532},
			},
			signing: map[int64]bool{84532: false},
		},
		{
			name: "signing switched off",
			cfg: &config.Config{
				Chains:                         gatewayChains(1, 11155111),
				WalletBudgetPreparationEnabled: true,
				WalletBudgetSigningChains:      []int64{11155111, 1},
			},
			signing: map[int64]bool{1: false, 11155111: false},
		},
		{
			name: "preparation switched off",
			cfg: &config.Config{
				Chains:                     gatewayChains(11155111),
				WalletBudgetSigningEnabled: true,
			},
			signing: map[int64]bool{11155111: false},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := listNetworks(t, tc.cfg).Data
			require.Len(t, got, len(tc.signing))
			for index, row := range got {
				if index > 0 {
					require.Less(t, got[index-1].ChainId, row.ChainId, "sorted by chain ID")
				}
				want, ok := tc.signing[row.ChainId]
				require.True(t, ok, "unexpected chain %d", row.ChainId)
				require.Equal(t, want, row.WalletBudgetSigning, "chain %d", row.ChainId)
			}
		})
	}
}

func TestListNetworksSingleChainAggregator(t *testing.T) {
	got := listNetworks(t, &config.Config{
		SmartWallet:                    &config.SmartWalletConfig{ChainID: 11155111},
		WalletBudgetPreparationEnabled: true,
		WalletBudgetSigningEnabled:     true,
	}).Data
	require.Equal(t, []generated.NetworkInfo{{ChainId: 11155111, WalletBudgetSigning: true}}, got)
}

func TestListNetworksWithoutConfigIsEmpty(t *testing.T) {
	require.Empty(t, listNetworks(t, &config.Config{}).Data)
}
