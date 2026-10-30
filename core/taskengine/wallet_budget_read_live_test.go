//go:build integration

package taskengine

import (
	"context"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

func TestWalletBudgetReadBeforeAfterSpend_Sepolia(t *testing.T) {
	if testing.Short() || os.Getenv("WALLET_BUDGET_READ_LIVE") != "1" {
		t.Skip("set WALLET_BUDGET_READ_LIVE=1 for the isolated Sepolia probe")
	}
	t.Setenv("WALLET_BUDGET_FEASIBILITY", "1")
	f := newBudgetSpike(t, true)
	const entity = uint32(201)
	until := uint64(time.Now().Add(time.Hour).Unix())
	install := f.install(t, entity, big.NewInt(500), until, nil)
	_, err := f.ownerBatch(t, []aa.Call{f.call(f.wallet, install)})
	require.NoError(t, err)
	signer := crypto.PubkeyToAddress(f.sessionKey.PublicKey)
	token := common.HexToAddress(SEPOLIA_USDC)
	policy := &model.SessionPolicy{ID: "01budgetreadlive", Owner: &f.owner, Runner: &f.wallet, ChainID: 11155111, EntityID: entity, SessionSigner: &signer,
		Status: model.SessionPolicyActive, ValidUntil: int64(until) * 1000,
		ERC20SpendCap:  &model.ERC20SpendCap{Token: &token, Amount: "500", GrantedCap: "500"},
		AllowedActions: []model.AllowedAction{{Target: &token, Selectors: []string{"0xa9059cbb", "0x095ea7b3"}}}, Grant: &model.SessionGrantAuthorization{InstallCall: install, AppliedAt: 1}}
	before, err := ReadWalletBudget(f.ctx, policy, nil, budgetBlockReader{f.chain, f.readBlock}, time.Now())
	require.NoError(t, err)
	require.Equal(t, "500", before.ERC20[0].Remaining)
	require.Equal(t, "0", before.ERC20[0].Used)
	// Approvals are charged in full when made, even before tokens move. This
	// consumes ten base units under the real stock Allowlist execution hook.
	_, err = f.session(t, entity, []aa.Call{f.call(token, budgetTokenCall("approve(address,uint256)", common.HexToAddress(SEPOLIA_SWAPROUTER), big.NewInt(10)))}, nil)
	require.NoError(t, err)
	after, err := ReadWalletBudget(f.ctx, policy, nil, budgetBlockReader{f.chain, f.readBlock}, time.Now())
	require.NoError(t, err)
	require.Equal(t, "490", after.ERC20[0].Remaining)
	require.Equal(t, "10", after.ERC20[0].Used)
	registered, err := aa.EntityRegisteredOnChain(f.ctx, budgetBlockReader{f.chain, f.readBlock}, f.wallet, entity)
	require.NoError(t, err)
	require.True(t, registered)
	uninstall, err := aa.SessionSignerUninstallFromInstall(entity, install)
	require.NoError(t, err)
	_, err = f.ownerBatch(t, []aa.Call{f.call(f.wallet, uninstall)})
	require.NoError(t, err)
	registered, err = aa.EntityRegisteredOnChain(f.ctx, budgetBlockReader{f.chain, f.readBlock}, f.wallet, entity)
	require.NoError(t, err)
	require.False(t, registered)
	clear, err := aa.EntityModuleStorageCleared(f.ctx, budgetBlockReader{f.chain, f.readBlock}, f.wallet, entity, install)
	require.NoError(t, err)
	require.True(t, clear)
	t.Logf("budget read: wallet=%s remaining 500 → 490, used 0 → 10; retirement registration and residue clear", f.wallet)
}

func TestWalletBudgetRetirementCleanup_SepoliaFork(t *testing.T) {
	if testing.Short() || os.Getenv("WALLET_BUDGET_RETIREMENT_FORK") != "1" {
		t.Skip("set WALLET_BUDGET_RETIREMENT_FORK=1 for the Sepolia fork probe")
	}
	forge, err := exec.LookPath("forge")
	require.NoError(t, err)
	require.NoError(t, testutil.LoadDotEnv())
	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
	require.NoError(t, err)
	require.EqualValues(t, 11155111, cfg.SmartWallet.ChainID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, forge, "test", "--root", "testdata/wallet_budget_retirement", "--match-test", "testRetirementAfterFailingHook", "-vv")
	command.Env = append(os.Environ(), "WALLET_BUDGET_FORK_RPC_URL="+cfg.SmartWallet.EthRpcUrl)
	out, err := command.CombinedOutput()
	log := strings.ReplaceAll(string(out), cfg.SmartWallet.EthRpcUrl, "[Sepolia RPC]")
	t.Log(log)
	require.NoError(t, err, "fork regression failed")
}
