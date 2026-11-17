package taskengine

import (
	"context"
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestBudgetSigningRequiresExplicitMainnetRollout(t *testing.T) {
	n := &Engine{config: &config.Config{WalletBudgetPreparationEnabled: true, WalletBudgetSigningEnabled: true}}
	for _, chain := range []int64{1, 8453} {
		require.ErrorContains(t, n.requireBudgetSigning(chain), "not enabled on this network")
	}
	n.config.WalletBudgetSigningChains = []int64{11155111, 1, 8453, 56}
	for _, chain := range []int64{11155111, 1, 8453} {
		require.NoError(t, n.requireBudgetSigning(chain))
	}
	require.Error(t, n.requireBudgetSigning(56), "configuration cannot qualify an unsupported deployment")
	n.config.WalletBudgetPreparationEnabled = false
	require.Error(t, n.requireBudgetSigning(1))
}

func TestBudgetResetQualifiedDeploymentsRemainAuthoritative(t *testing.T) {
	wallet, spender := common.HexToAddress("0x1111"), common.HexToAddress("0x2222")
	caller := budgetCaller(func(call ethereum.CallMsg) ([]byte, error) { return budgetWord(123), nil })
	for chain, tokens := range budgetAutomaticResetTokens {
		for _, token := range tokens {
			t.Run(fmt.Sprintf("%d/%s", chain, token), func(t *testing.T) {
				require.NoError(t, refuseUnsupportedBudgetRoutes(chain, SessionPermissions{
					AllowedActions: []model.AllowedAction{{Target: &token, Selectors: []string{selectorApprove, selectorTransfer}}},
				}), "a reset deployment must also be present in the released canonical catalog")
				reads, resets, err := readBudgetApprovals(context.Background(), caller, wallet, chain,
					[]BudgetApprovalPair{{Token: token, Spender: spender}}, map[common.Address]bool{spender: true})
				require.NoError(t, err)
				require.Len(t, reads, 1)
				require.Equal(t, "123", reads[0].Amount)
				require.Equal(t, reads, resets)
			})
		}
	}
	for _, tc := range []struct {
		chain int64
		token string
	}{
		{1, "0x19de6b897Ed14A376Dda0Fe53a5420D2aC828a28"}, // retired BGB
		{1, "0xB8c77482e45F1F44dE1745F52C74426C631bDD52"}, // BNB: approve(spender, 0) reverts
		{1, "0xE66747a101bFF2dBA3697199DCcE5b743b454759"}, // GT: approve(spender, 0) reverts
		{1, "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"}, // another chain's USDC
		{56, "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"},
	} {
		_, _, err := readBudgetApprovals(context.Background(), caller, wallet, tc.chain,
			[]BudgetApprovalPair{{Token: common.HexToAddress(tc.token), Spender: spender}}, map[common.Address]bool{spender: true})
		require.ErrorContains(t, err, "approval scan")
	}
}
