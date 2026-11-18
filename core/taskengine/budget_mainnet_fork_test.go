//go:build integration

package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

// budgetForkBatchSize keeps each forge run's case list, which travels as one
// hex environment string, under Linux's 128 KiB limit on a single string
// (MAX_ARG_STRLEN; macOS has no such limit). A token's cases encode to about
// 9.5 KB, so ten are about 95 KB. budgetForkCasesMaxBytes fails the proof
// with a clear message if the encoding grows, instead of E2BIG from exec.
const budgetForkBatchSize = 10

const budgetForkCasesMaxBytes = 120 << 10

// Only read RPC requests reach mainnet. All signatures, balance changes and
// transactions execute on disposable Foundry forks, using public fixture keys.
// This is contract proof, not proof of live bundler acceptance or sponsorship.
func TestBudgetAtomicResetAndInstall_MainnetForks(t *testing.T) {
	if testing.Short() || os.Getenv("WALLET_BUDGET_MAINNET_FORK") != "1" {
		t.Skip("set WALLET_BUDGET_MAINNET_FORK=1 for read-only Ethereum/Base fork proofs")
	}
	require.NoError(t, testutil.LoadDotEnv())
	forge, err := exec.LookPath("forge")
	require.NoError(t, err)
	ownerKey, err := crypto.HexToECDSA(strings.Repeat("0", 59) + "a11ce") // public fork-only key
	require.NoError(t, err)
	owner := crypto.PubkeyToAddress(ownerKey.PublicKey)
	sessionKey, err := crypto.HexToECDSA(strings.Repeat("0", 61) + "b0b") // public fork-only key
	require.NoError(t, err)
	for _, network := range []struct {
		chain  int64
		rpcEnv string
	}{
		{1, "ETHEREUM_RPC_URL"},
		{8453, "BASE_RPC_URL"},
	} {
		t.Run(fmt.Sprint(network.chain), func(t *testing.T) {
			url := os.Getenv(network.rpcEnv)
			if url == "" && os.Getenv("ALCHEMY_API_KEY") != "" {
				host := map[int64]string{1: "eth-mainnet", 8453: "base-mainnet"}[network.chain]
				url = "https://" + host + ".g.alchemy.com/v2/" + os.Getenv("ALCHEMY_API_KEY")
			}
			require.NotEmpty(t, url, network.rpcEnv+" is required")
			checkRPC := func(err error) {
				t.Helper()
				if err != nil {
					require.FailNow(t, strings.ReplaceAll(err.Error(), url, "[RPC URL]"))
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			client, err := ethclient.DialContext(ctx, url)
			checkRPC(err)
			defer client.Close()
			chain, err := client.ChainID(ctx)
			checkRPC(err)
			require.EqualValues(t, network.chain, chain.Int64(), "wrong-chain RPC cannot qualify a deployment")
			header, err := client.HeaderByNumber(ctx, nil)
			checkRPC(err)
			salt := big.NewInt(time.Now().UnixNano())
			wallet, err := aa.GetSenderAddressMAv2WithContext(ctx, client, owner, salt)
			checkRPC(err)
			spender := common.HexToAddress("0x2222222222222222222222222222222222222222")
			// One forge run per batch: every case travels in one environment
			// variable, and the full catalog would exceed the OS argument limit.
			// Each batch forks the same pinned block.
			all := budgetAutomaticResetTokens[network.chain]
			for first := 0; first < len(all); first += budgetForkBatchSize {
				batch := all[first:min(first+budgetForkBatchSize, len(all))]
				var tokens []common.Address
				var positive, badReset, badInstall [][]byte
				for i, token := range batch {
					entity := uint32(i + 7)
					permissions := SessionPermissions{
						AllowedActions: []model.AllowedAction{{Target: &token, Selectors: []string{selectorApprove, selectorTransfer}}},
						SpendCap:       &model.ERC20SpendCap{Token: &token, Amount: "500"},
						NativeSpendCap: &model.NativeSpendCap{Amount: "1000000000000000000"},
						ValidUntilMs:   int64(header.Time+86400) * 1000,
					}
					hooks, err := permissions.HooksFor(entity)
					require.NoError(t, err)
					install, err := aa.PackSessionSignerInstall(aa.SessionGrant{
						EntityID: entity, Signer: crypto.PubkeyToAddress(sessionKey.PublicKey), Global: true, Hooks: hooks,
					})
					require.NoError(t, err)
					resets := []BudgetApprovalReset{{Token: token, Spender: spender, Amount: "123"}}
					pack := func(rows []BudgetApprovalReset, payload []byte) []byte {
						calls, err := budgetPreparationCalls(*wallet, rows, nil, payload)
						require.NoError(t, err)
						data, err := aa.PackExecuteBatchMAv2(calls)
						require.NoError(t, err)
						return data
					}
					tokens = append(tokens, token)
					positive = append(positive, pack(resets, install))
					badReset = append(badReset, pack(append(resets, BudgetApprovalReset{
						Token: common.HexToAddress("0xf00d"), Spender: spender, Amount: "1",
					}), install))
					badInstall = append(badInstall, pack(resets, install[:4])) // ABI decoding must revert
				}
				addressArray, err := abi.NewType("address[]", "", nil)
				require.NoError(t, err)
				bytesArray, err := abi.NewType("bytes[]", "", nil)
				require.NoError(t, err)
				cases, err := (abi.Arguments{{Type: addressArray}, {Type: bytesArray}, {Type: bytesArray}, {Type: bytesArray}}).Pack(tokens, positive, badReset, badInstall)
				require.NoError(t, err)
				casesEnv := "BUDGET_FORK_CASES=0x" + common.Bytes2Hex(cases)
				require.Less(t, len(casesEnv), budgetForkCasesMaxBytes,
					"a batch's cases outgrew one environment string; lower budgetForkBatchSize")
				command := exec.CommandContext(ctx, forge, "test", "--root", "testdata/wallet_budget_rollout", "--match-test", "testAtomicBudgetMainnet", "-vv")
				command.Env = append(os.Environ(),
					"BUDGET_FORK_RPC="+url, "BUDGET_FORK_BLOCK="+header.Number.String(),
					"BUDGET_FORK_CHAIN="+chain.String(), "BUDGET_FORK_SALT="+salt.String(),
					"BUDGET_FORK_WALLET="+wallet.Hex(), casesEnv)
				out, err := command.CombinedOutput()
				// Never expose provider credentials in an assertion or child output.
				output := strings.ReplaceAll(string(out), url, "[RPC URL]")
				require.NoError(t, err, "%s", output)
			}
			t.Logf("chain=%d pinnedBlock=%s tokens=%d: Go-encoded reset/install, rollback and signed owner/session operations passed", network.chain, header.Number, len(all))
		})
	}
}
