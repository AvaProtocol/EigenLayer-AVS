package taskengine

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func signBudgetDiscovery(t *testing.T, key *ecdsa.PrivateKey, d *BudgetApprovalDiscovery) {
	t.Helper()
	hash, err := BudgetDiscoveryDigest(*d)
	require.NoError(t, err)
	d.Signature = signDigest(t, key, hash)
}

func TestBudgetDiscoveryIndependentEIP712Golden(t *testing.T) {
	d := BudgetApprovalDiscovery{Owner: common.HexToAddress("0x1234"), Wallet: common.HexToAddress("0x5678"), ChainID: 11155111, Source: "moralis", FetchedAt: 1700000000000, Complete: true, AllowedContracts: []common.Address{common.HexToAddress("0x40"), common.HexToAddress("0x30")}, Candidates: []BudgetApprovalPair{
		{Token: common.HexToAddress("0x20"), Spender: common.HexToAddress("0x30")},
		{Token: common.HexToAddress("0x10"), Spender: common.HexToAddress("0x40")},
	}}
	hash, err := BudgetDiscoveryDigest(d)
	require.NoError(t, err)
	// Independently computed with ethers TypedDataEncoder, shared with Studio.
	require.Equal(t, "0x0da18575b030f7da745735e26a5a45d5e2cf19e5599b150536d715ee132b0c1f", hash.Hex())
	d.Candidates = append(d.Candidates, d.Candidates[0])
	other, err := BudgetDiscoveryDigest(d)
	require.NoError(t, err)
	require.Equal(t, hash, other)
}

func TestBudgetDiscoveryRefusesTamperingReplayAndPartialInventory(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	signer := crypto.PubkeyToAddress(key.PublicKey)
	now := time.Now()
	owner, wallet := common.HexToAddress("0x1234"), common.HexToAddress("0x5678")
	for _, tc := range []struct {
		name   string
		change func(*BudgetApprovalDiscovery)
		resign bool
		want   string
	}{
		{name: "valid"}, {name: "owner", change: func(d *BudgetApprovalDiscovery) { d.Owner = wallet }, want: BudgetDiscoveryRequiredCode},
		{name: "wallet", change: func(d *BudgetApprovalDiscovery) { d.Wallet = owner }, want: BudgetDiscoveryRequiredCode},
		{name: "chain", change: func(d *BudgetApprovalDiscovery) { d.ChainID = 1 }, want: BudgetDiscoveryRequiredCode},
		{name: "stale", change: func(d *BudgetApprovalDiscovery) { d.FetchedAt = now.Add(-3 * time.Minute).UnixMilli() }, resign: true, want: BudgetDiscoveryRequiredCode},
		{name: "future", change: func(d *BudgetApprovalDiscovery) { d.FetchedAt = now.Add(time.Second).UnixMilli() }, resign: true, want: BudgetDiscoveryRequiredCode},
		{name: "source", change: func(d *BudgetApprovalDiscovery) { d.Source = "caller" }, resign: true, want: BudgetDiscoveryRequiredCode},
		{name: "scope", change: func(d *BudgetApprovalDiscovery) { d.AllowedContracts = []common.Address{wallet} }, want: BudgetDiscoveryRequiredCode},
		{name: "pairs", change: func(d *BudgetApprovalDiscovery) { d.Candidates[0].Token = wallet }, want: BudgetDiscoveryRequiredCode},
		{name: "partial signed", change: func(d *BudgetApprovalDiscovery) { d.Complete = false }, resign: true, want: BudgetDiscoveryUnavailableCode},
		{name: "false complete", change: func(d *BudgetApprovalDiscovery) { d.Complete = false }, want: BudgetDiscoveryRequiredCode},
		{name: "signature", change: func(d *BudgetApprovalDiscovery) { d.Signature[0] ^= 1 }, want: BudgetDiscoveryRequiredCode},
		{name: "bad v", change: func(d *BudgetApprovalDiscovery) { d.Signature[64] = 2 }, want: BudgetDiscoveryRequiredCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &BudgetApprovalDiscovery{Owner: owner, Wallet: wallet, ChainID: 11155111, Source: "moralis", FetchedAt: now.UnixMilli(), Complete: true, Candidates: []BudgetApprovalPair{{Token: testTokenAddr, Spender: testRouterAddr}}}
			signBudgetDiscovery(t, key, d)
			if tc.change != nil {
				tc.change(d)
			}
			if tc.resign {
				signBudgetDiscovery(t, key, d)
			}
			_, verificationErr := verifyBudgetDiscovery(d, owner, wallet, signer, 11155111, now)
			if tc.want == "" {
				require.NoError(t, verificationErr)
			} else {
				var e *BudgetPolicyError
				require.ErrorAs(t, verificationErr, &e)
				require.Equal(t, tc.want, e.Code)
			}
		})
	}
	_, err = verifyBudgetDiscovery(nil, owner, wallet, signer, 11155111, now)
	require.Error(t, err)
}

func TestBudgetApprovalReadsAreAuthoritativeAndFailClosed(t *testing.T) {
	wallet := common.HexToAddress("0x1234")
	weth := common.HexToAddress("0xfff9976782d46cc05630d1f6ebab18b2324d6b14")
	for _, tc := range []struct {
		name              string
		token             common.Address
		amount            int64
		malformed, failed bool
		want              string
	}{
		{name: "USDC live", token: testTokenAddr, amount: 100},
		{name: "USDC zero", token: testTokenAddr},
		{name: "WETH zero", token: weth},
		{name: "WETH live", token: weth, amount: 100, want: BudgetResetUnsupportedCode},
		{name: "malformed", token: testTokenAddr, malformed: true, want: "read"},
		{name: "RPC failed", token: testTokenAddr, failed: true, want: "read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := budgetCaller(func(call ethereum.CallMsg) ([]byte, error) {
				require.Equal(t, tc.token, *call.To)
				require.Equal(t, wallet, common.BytesToAddress(call.Data[4:36]))
				require.Equal(t, testRouterAddr, common.BytesToAddress(call.Data[36:68]))
				if tc.failed {
					return nil, errors.New("down")
				}
				if tc.malformed {
					return []byte{0}, nil
				}
				return budgetWord(tc.amount), nil
			})
			reads, resets, err := readBudgetApprovals(context.Background(), caller, wallet, testPolicyChain, []BudgetApprovalPair{{Token: tc.token, Spender: testRouterAddr}}, map[common.Address]bool{testRouterAddr: true})
			if tc.want == "read" {
				require.ErrorIs(t, err, ErrBudgetReadUnavailable)
				return
			}
			if tc.want != "" {
				var e *BudgetPolicyError
				require.ErrorAs(t, err, &e)
				require.Equal(t, tc.want, e.Code)
				require.Equal(t, tc.token.Hex(), e.Details["token"])
				return
			}
			require.NoError(t, err)
			require.Equal(t, big.NewInt(tc.amount).String(), reads[0].Amount)
			if tc.amount == 0 {
				require.Empty(t, resets)
			} else {
				require.Len(t, resets, 1)
			}
		})
	}
}

func TestBudgetResetScopeDependsOnNewContractsAndOwnerReset(t *testing.T) {
	p, _ := semanticsBudget()
	p.AllowedActions = append(p.AllowedActions, model.AllowedAction{Target: &testRouterAddr, Selectors: []string{"0x04e45aaf"}})
	draft := BudgetPolicyDraft{Permissions: SessionPermissions{AllowedActions: p.AllowedActions}}
	require.Empty(t, budgetResetSpenders(p, draft, nil))
	require.Contains(t, budgetResetSpenders(p, draft, &WalletBudgetInput{StartNewTotals: true}), testRouterAddr)
	other := common.HexToAddress("0x5678")
	draft.Permissions.AllowedActions = append(draft.Permissions.AllowedActions, model.AllowedAction{Target: &other, Selectors: []string{"0x12345678"}})
	require.Equal(t, map[common.Address]bool{other: true}, budgetResetSpenders(p, draft, nil))
	p.Sizing = model.BudgetSizingWorkflow
	require.Len(t, budgetResetSpenders(p, draft, nil), 3)
}

func TestBudgetResetScopeIncludesCallableNativeRecipients(t *testing.T) {
	recipient := common.HexToAddress("0x1234")
	p, _ := semanticsBudget()
	draft := BudgetPolicyDraft{Permissions: SessionPermissions{NativeRecipients: []*common.Address{&recipient}}}
	require.Equal(t, map[common.Address]bool{recipient: true}, budgetResetSpenders(p, draft, nil))
	p.NativeRecipients = []*common.Address{&recipient}
	require.Empty(t, budgetResetSpenders(p, draft, nil))
	require.Contains(t, budgetResetSpenders(p, draft, &WalletBudgetInput{StartNewTotals: true}), recipient)
}

func TestBudgetRemovedTokenCannotBecomeUnrestrictedNativeRecipient(t *testing.T) {
	p := SessionPermissions{budgetMode: true, AllowedActions: []model.AllowedAction{{Target: &testRouterAddr, Selectors: []string{"0x04e45aaf"}}}, SpendCaps: []model.ERC20SpendCap{{Token: &testTokenAddr, Amount: "0"}}, NativeRecipients: []*common.Address{&testTokenAddr}, NativeSpendCap: &model.NativeSpendCap{Amount: "100"}, AllowContractRecipient: true, ValidUntilMs: time.Now().Add(time.Hour).UnixMilli()}
	require.ErrorContains(t, p.Validate(), "budget token cannot also")
}
