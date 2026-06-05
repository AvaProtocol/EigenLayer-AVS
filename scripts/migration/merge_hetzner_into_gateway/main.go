// Standalone CLI wrapper around core/migration/hetznermerge. Operators
// who want to run the merge without installing the full ap-avs binary
// can do:
//
//	go run scripts/migration/merge_hetzner_into_gateway \
//	    --donor-path   ./donors/ethereum-2026-06-04 \
//	    --donor-chain-id 1 \
//	    --gateway-path /data \
//	    --dry-run [--verbose]
//
// All the actual merge logic, per-prefix policy, and stats lives in
// core/migration/hetznermerge — this file is intentionally a thin
// flag-parsing wrapper. The Cobra subcommand `ap-avs migrate-hetzner`
// at cmd/migrateHetzner.go shares the same library entry point and is
// the recommended way to run this in production.
//
// For chain IDs, operational constraints, the per-prefix policy matrix,
// and the full production runbook, see:
//   - core/migration/hetznermerge/handlers.go (policy matrix)
//   - scripts/migration/merge_hetzner_into_gateway/README.md
//   - avs-infra/docs/changes/20260604-hetzner-data-restore-plan.md
//   - avs-infra/docs/changes/20260605-hetzner-merge-production-runbook.md
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/AvaProtocol/EigenLayer-AVS/core/migration/hetznermerge"
)

func main() {
	donorPath := flag.String("donor-path", "", "Path to the donor BadgerDB directory (the Hetzner aggregator's db_path)")
	donorChainID := flag.Int64("donor-chain-id", 0, "Chain ID for the donor — must match the chain the donor aggregator was serving (1 / 8453 / 11155111 / 84532)")
	gatewayPath := flag.String("gateway-path", "", "Path to the Railway gateway BadgerDB directory (the merge destination)")
	dryRun := flag.Bool("dry-run", true, "Print what would change without writing to the gateway. Defaults to TRUE; pass --dry-run=false to apply.")
	verbose := flag.Bool("verbose", false, "Print one line per key processed")
	failOnUnknownPrefix := flag.Bool("fail-on-unknown-prefix", true, "Hard-fail if the donor has keys with a prefix this tool does not recognize. Default true — disabling is for emergency-recovery investigation only.")
	flag.Parse()

	opts := hetznermerge.Options{
		DonorPath:           *donorPath,
		DonorChainID:        *donorChainID,
		GatewayPath:         *gatewayPath,
		DryRun:              *dryRun,
		Verbose:             *verbose,
		FailOnUnknownPrefix: *failOnUnknownPrefix,
	}

	if err := opts.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		fmt.Fprintln(os.Stderr)
		flag.Usage()
		os.Exit(2)
	}

	if _, err := hetznermerge.Run(opts, hetznermerge.NewStdoutWriter()); err != nil {
		log.Fatalf("merge failed: %v", err)
	}
}
