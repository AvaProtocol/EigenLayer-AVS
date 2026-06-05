package cmd

import (
	"fmt"
	"os"

	"github.com/AvaProtocol/EigenLayer-AVS/core/migration/hetznermerge"
	"github.com/spf13/cobra"
)

var (
	migrateHetznerDonorPath           string
	migrateHetznerDonorChainID        int64
	migrateHetznerGatewayPath         string
	migrateHetznerDryRun              bool
	migrateHetznerVerbose             bool
	migrateHetznerFailOnUnknownPrefix bool

	migrateHetznerCmd = &cobra.Command{
		Use:   "migrate-hetzner",
		Short: "Merge a Hetzner per-chain aggregator BadgerDB into the unified Railway gateway BadgerDB",
		Long: `One-shot tool to bring a Hetzner per-chain aggregator's data forward into
the unified Railway gateway. Used during the Hetzner -> Railway cutover
to preserve the ~3 weeks of production workflows, executions, secrets,
and fee records that accumulated on the Hetzner aggregators between the
May 2026 Railway migration and the actual data merge.

Run this once per donor (one Hetzner aggregator's BadgerDB), sequentially,
passing the donor's chain ID:

  1         Ethereum mainnet
  8453      Base mainnet
  11155111  Sepolia testnet
  84532     Base-Sepolia testnet

Per-prefix policy is in core/migration/hetznermerge/handlers.go. Full
context + safety rules + production runbook in the avs-infra change-log:
  - 20260604-hetzner-data-restore-plan.md
  - 20260605-hetzner-merge-production-runbook.md

OPERATIONAL CONSTRAINTS:

  * Stop the Railway gateway service before running with --dry-run=false.
    BadgerDB is single-writer; the tool opens the same db_path the
    gateway uses.
  * ALWAYS run with --dry-run=true first against an rsync'd copy of the
    gateway DB. Inspect per-prefix counts and collisions before touching
    the real volume.
  * Take a fresh pre-merge backup of the gateway volume immediately
    before the merge runs. That backup is the only rollback target.
  * After all four donors are merged, run "ap-avs backfill-wallet-salt-index
    --chain-id <N>" once per chain to rebuild the wsalt: secondary index.

Production usage (Railway, via CMD override on the gateway service):

  # 1. Stop the gateway, take a snapshot of the gateway-volume.
  # 2. Upload donor tarballs to /data/donors/<chain>/db/ on the volume.
  # 3. Override the gateway service's start CMD to run this subcommand:
  ap-avs migrate-hetzner \
      --donor-path     /data/donors/ethereum/db \
      --donor-chain-id 1 \
      --gateway-path   /data/gateway \
      --dry-run=false
  # 4. Repeat for each chain, then restore the default CMD + restart.
`,
		Run: func(cmd *cobra.Command, args []string) {
			opts := hetznermerge.Options{
				DonorPath:           migrateHetznerDonorPath,
				DonorChainID:        migrateHetznerDonorChainID,
				GatewayPath:         migrateHetznerGatewayPath,
				DryRun:              migrateHetznerDryRun,
				Verbose:             migrateHetznerVerbose,
				FailOnUnknownPrefix: migrateHetznerFailOnUnknownPrefix,
			}
			if err := opts.Validate(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			if _, err := hetznermerge.Run(opts, hetznermerge.NewStdoutWriter()); err != nil {
				fmt.Fprintf(os.Stderr, "migrate-hetzner failed: %v\n", err)
				os.Exit(1)
			}
		},
	}
)

func init() {
	migrateHetznerCmd.Flags().StringVar(&migrateHetznerDonorPath, "donor-path", "",
		"Path to the donor BadgerDB directory (the Hetzner aggregator's db_path)")
	migrateHetznerCmd.Flags().Int64Var(&migrateHetznerDonorChainID, "donor-chain-id", 0,
		"Chain ID for the donor — must match the chain the donor aggregator was serving (1 / 8453 / 11155111 / 84532)")
	migrateHetznerCmd.Flags().StringVar(&migrateHetznerGatewayPath, "gateway-path", "",
		"Path to the Railway gateway BadgerDB directory (the merge destination)")
	migrateHetznerCmd.Flags().BoolVar(&migrateHetznerDryRun, "dry-run", true,
		"Print what would change without writing to the gateway. Defaults to TRUE; pass --dry-run=false to apply.")
	migrateHetznerCmd.Flags().BoolVar(&migrateHetznerVerbose, "verbose", false,
		"Print one line per key processed")
	migrateHetznerCmd.Flags().BoolVar(&migrateHetznerFailOnUnknownPrefix, "fail-on-unknown-prefix", true,
		"Hard-fail if the donor has keys with a prefix this tool does not recognize. Default true.")
	rootCmd.AddCommand(migrateHetznerCmd)
}
