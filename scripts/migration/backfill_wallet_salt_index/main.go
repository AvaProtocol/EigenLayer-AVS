// Standalone wrapper around taskengine.BackfillWalletSaltIndex.
//
// This script exists for local development convenience — it lets you run
// the migration without building the full /ava binary. The same logic is
// also exposed as a subcommand on the production binary:
//
//	/ava backfill-wallet-salt-index --config <aggregator.yaml> [--dry-run] [--verbose]
//
// Use the standalone script when iterating locally, and the /ava
// subcommand when running against a production Docker image (the script
// is not built into the production image).
//
// Operational constraints
// -----------------------
//   - Stop the aggregator first. BadgerDB is single-writer; this script
//     opens the same `db_path` directly.
//   - Always run with --dry-run first against a freshly restored backup
//     and inspect the printed summary before running for real.
//
// Usage
// -----
//
//	go run scripts/migration/backfill_wallet_salt_index/main.go \
//	    --config config/aggregator-base.yaml [--dry-run] [--verbose]
package main

import (
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"gopkg.in/yaml.v3"
)

// minimal subset of aggregator.yaml — only the fields we need.
type aggregatorYAML struct {
	DbPath      string `yaml:"db_path"`
	SmartWallet struct {
		EthRpcUrl string `yaml:"eth_rpc_url"`
	} `yaml:"smart_wallet"`
}

func main() {
	configPath := flag.String("config", "", "Path to aggregator YAML config (e.g. config/aggregator-base.yaml)")
	dryRun := flag.Bool("dry-run", false, "Print what would change without writing to BadgerDB")
	verbose := flag.Bool("verbose", false, "Print one line per wallet processed")
	flag.Parse()

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "--config is required")
		flag.Usage()
		os.Exit(2)
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if cfg.DbPath == "" {
		log.Fatalf("config %s has no db_path", *configPath)
	}
	if cfg.SmartWallet.EthRpcUrl == "" {
		log.Fatalf("config %s has no smart_wallet.eth_rpc_url", *configPath)
	}

	fmt.Printf("Config:        %s\n", *configPath)
	fmt.Printf("DB path:       %s\n", cfg.DbPath)
	// Redact: RPC URLs from Tenderly/Alchemy/Infura embed API keys in
	// the path or query — printing the raw URL leaks secrets to terminal
	// scrollback and CI logs.
	fmt.Printf("RPC URL:       %s\n", taskengine.RedactRPCURL(cfg.SmartWallet.EthRpcUrl))
	fmt.Printf("Mode:          %s\n", modeLabel(*dryRun))
	fmt.Println()

	rpcClient, err := ethclient.Dial(cfg.SmartWallet.EthRpcUrl)
	if err != nil {
		log.Fatalf("dial RPC: %v", err)
	}
	defer rpcClient.Close()

	db, err := storage.NewWithPath(cfg.DbPath)
	if err != nil {
		log.Fatalf("open BadgerDB at %s: %v\n\nIs the aggregator still running? It must be stopped first.", cfg.DbPath, err)
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
		DryRun:  *dryRun,
		Verbose: *verbose,
		Logf: func(format string, args ...any) {
			fmt.Printf(format+"\n", args...)
		},
	})
	if err != nil {
		log.Fatalf("backfill failed: %v", err)
	}

	fmt.Println()
	printStats(stats)
	if *dryRun {
		fmt.Println()
		fmt.Println("DRY RUN — no changes written. Re-run without --dry-run to apply.")
	}
}

func printStats(stats *taskengine.WalletSaltIndexBackfillStats) {
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
}

func loadConfig(path string) (*aggregatorYAML, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &aggregatorYAML{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	return cfg, nil
}

func modeLabel(dryRun bool) string {
	if dryRun {
		return "DRY RUN"
	}
	return "APPLY"
}
