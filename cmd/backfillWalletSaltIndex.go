package cmd

import (
	"fmt"
	"math/big"
	"os"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	avsconfig "github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/spf13/cobra"
)

var (
	backfillWalletSaltIndexDryRun  bool
	backfillWalletSaltIndexVerbose bool

	backfillWalletSaltIndexCmd = &cobra.Command{
		Use:   "backfill-wallet-salt-index",
		Short: "Backfill the (owner, factory, salt) → wallet address secondary index",
		Long: `Walks every persisted wallet record and either:

  1. Writes a (owner, factory, salt) → address secondary index entry, when
     the live derivation against the smart-wallet RPC still matches the
     stored address (canonical row); or
  2. Marks the row stale (StaleDerivation = true, IsHidden = true) when the
     derivation differs — i.e. the factory's account implementation has been
     upgraded since the row was first persisted.

This subcommand reads db_path and smart_wallet.eth_rpc_url from the same
aggregator YAML the running aggregator uses, so it must be invoked with
--config pointing at that file.

OPERATIONAL CONSTRAINT: BadgerDB is single-writer. The aggregator must be
stopped before this command runs, otherwise opening the database will fail
with a lock error. Always run with --dry-run first against a fresh backup.

Production usage (Docker):

  docker stop aggregator-base
  docker run --rm --volumes-from aggregator-base avaprotocol/ap-avs:latest \
    backfill-wallet-salt-index --config /app/config/aggregator-base.yaml --dry-run
  # inspect summary, then re-run without --dry-run
  docker start aggregator-base
`,
		Run: func(cmd *cobra.Command, args []string) {
			if err := runBackfillWalletSaltIndex(config); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		},
	}
)

func init() {
	backfillWalletSaltIndexCmd.Flags().BoolVar(&backfillWalletSaltIndexDryRun, "dry-run", false, "Print what would change without writing to BadgerDB")
	backfillWalletSaltIndexCmd.Flags().BoolVar(&backfillWalletSaltIndexVerbose, "verbose", false, "Print one line per wallet processed")
	rootCmd.AddCommand(backfillWalletSaltIndexCmd)
}

func runBackfillWalletSaltIndex(configPath string) error {
	nodeConfig, err := avsconfig.NewConfig(configPath)
	if err != nil {
		return fmt.Errorf("parse config %s: %w", configPath, err)
	}
	if nodeConfig.DbPath == "" {
		return fmt.Errorf("config %s has no db_path", configPath)
	}
	if nodeConfig.SmartWallet == nil || nodeConfig.SmartWallet.EthRpcUrl == "" {
		return fmt.Errorf("config %s has no smart_wallet.eth_rpc_url", configPath)
	}

	fmt.Printf("Config:        %s\n", configPath)
	fmt.Printf("DB path:       %s\n", nodeConfig.DbPath)
	// Redact: RPC URLs from Tenderly/Alchemy/Infura embed API keys in
	// the path or query — printing the raw URL leaks secrets to terminal
	// scrollback and CI logs.
	fmt.Printf("RPC URL:       %s\n", taskengine.RedactRPCURL(nodeConfig.SmartWallet.EthRpcUrl))
	fmt.Printf("Mode:          %s\n", backfillModeLabel(backfillWalletSaltIndexDryRun))
	fmt.Println()

	rpcClient, err := ethclient.Dial(nodeConfig.SmartWallet.EthRpcUrl)
	if err != nil {
		return fmt.Errorf("dial smart-wallet RPC %s: %w", nodeConfig.SmartWallet.EthRpcUrl, err)
	}
	defer rpcClient.Close()

	db, err := storage.NewWithPath(nodeConfig.DbPath)
	if err != nil {
		return fmt.Errorf("open BadgerDB at %s: %w\n\nIs the aggregator still running? It must be stopped first.", nodeConfig.DbPath, err)
	}
	defer db.Close()

	deriver := func(owner common.Address, factory common.Address, salt *big.Int) (common.Address, error) {
		addr, derr := aa.GetSenderAddressForFactory(rpcClient, owner, factory, salt)
		if derr != nil {
			return common.Address{}, derr
		}
		if addr == nil {
			return common.Address{}, fmt.Errorf("derived nil address")
		}
		return *addr, nil
	}

	stats, err := taskengine.BackfillWalletSaltIndex(db, deriver, taskengine.WalletSaltIndexBackfillOptions{
		DryRun:  backfillWalletSaltIndexDryRun,
		Verbose: backfillWalletSaltIndexVerbose,
		Logf: func(format string, args ...any) {
			fmt.Printf(format+"\n", args...)
		},
	})
	if err != nil {
		return fmt.Errorf("backfill: %w", err)
	}

	fmt.Println()
	fmt.Println("Summary")
	fmt.Println("-------")
	fmt.Printf("Total wallet rows scanned:        %d\n", stats.Total)
	fmt.Printf("  Canonical (live derive matches): %d\n", stats.CanonicalConfirmed)
	fmt.Printf("    Secondary index newly written:   %d\n", stats.SecondaryIndexWritten)
	fmt.Printf("    Secondary index already correct: %d\n", stats.SecondaryIndexExisting)
	fmt.Printf("  Stale (live derive differs):     %d\n", stats.NewlyMarkedStale+stats.AlreadyStale)
	fmt.Printf("    Newly marked stale:              %d\n", stats.NewlyMarkedStale)
	fmt.Printf("    Already marked stale:            %d\n", stats.AlreadyStale)
	fmt.Printf("  Skipped — missing factory:       %d\n", stats.SkippedMissingFactory)
	fmt.Printf("  Skipped — missing salt:          %d\n", stats.SkippedMissingSalt)
	fmt.Printf("  Skipped — RPC derive error:      %d\n", stats.SkippedDeriveError)
	if backfillWalletSaltIndexDryRun {
		fmt.Println()
		fmt.Println("DRY RUN — no changes written. Re-run without --dry-run to apply.")
	}
	return nil
}

func backfillModeLabel(dryRun bool) string {
	if dryRun {
		return "DRY RUN"
	}
	return "APPLY"
}
