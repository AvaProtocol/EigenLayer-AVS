// Standalone tool to merge a single Hetzner per-chain aggregator BadgerDB
// (the "donor") into the unified Railway gateway BadgerDB. Used once
// during the 2026-07-01 Hetzner decom to bring forward the ~3 weeks of
// production data that Studio wrote to the Hetzner aggregators after the
// May 2026 Railway migration but before this cutover.
//
// Run the tool ONCE PER DONOR — sequentially, not in parallel — passing
// the donor's chain ID:
//
//	go run scripts/migration/merge_hetzner_into_gateway \
//	    --donor-path   ./donors/ethereum-2026-06-04 \
//	    --donor-chain-id 1 \
//	    --gateway-path /data \
//	    --dry-run [--verbose]
//
// Chain IDs:
//
//	1         Ethereum mainnet
//	8453      Base mainnet
//	11155111  Sepolia testnet
//	84532     Base-Sepolia testnet
//
// Operational constraints
// -----------------------
//
//   - Stop the Railway gateway service before running with --dry-run=false.
//     BadgerDB is single-writer; the tool opens the same db_path the
//     gateway uses.
//   - ALWAYS run with --dry-run first against an rsync'd copy of the
//     gateway DB. Inspect per-prefix counts and collisions before
//     touching the real volume.
//   - The tool uses skip-if-exists semantics: every Set is preceded by
//     Exist; nothing the gateway has already written is ever overwritten.
//     Do NOT replace this with storage.Storage.Load — Load is bulk upsert
//     and will silently clobber live data.
//   - Take a fresh pre-merge backup of the gateway volume immediately
//     before the merge runs. That backup is the only rollback target.
//
// Per-prefix policy
// -----------------
// Documented in docs/Operator.md and in the avs-infra change-log
// `docs/changes/20260604-hetzner-data-restore-plan.md`. The dispatch
// table in handlers.go is the source of truth for what each prefix
// does — refer there before editing this comment.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// supportedChainIDs gates --donor-chain-id to avoid fat-finger merges
// (e.g. accidentally importing the Sepolia donor with chain-id 1).
var supportedChainIDs = map[int64]string{
	1:        "ethereum",
	8453:     "base",
	11155111: "sepolia",
	84532:    "base-sepolia",
}

func main() {
	donorPath := flag.String("donor-path", "", "Path to the donor BadgerDB directory (the Hetzner aggregator's db_path)")
	donorChainID := flag.Int64("donor-chain-id", 0, "Chain ID for the donor — must match the chain the donor aggregator was serving")
	gatewayPath := flag.String("gateway-path", "", "Path to the Railway gateway BadgerDB directory (the merge destination)")
	dryRun := flag.Bool("dry-run", true, "Print what would change without writing to the gateway. Defaults to TRUE; pass --dry-run=false to apply.")
	verbose := flag.Bool("verbose", false, "Print one line per key processed")
	failOnUnknownPrefix := flag.Bool("fail-on-unknown-prefix", true, "Hard-fail if the donor has keys with a prefix this tool does not recognize. Default true — disabling is for emergency-recovery investigation only.")
	flag.Parse()

	if *donorPath == "" {
		dieUsage("--donor-path is required")
	}
	if *gatewayPath == "" {
		dieUsage("--gateway-path is required")
	}
	if *donorChainID == 0 {
		dieUsage("--donor-chain-id is required")
	}
	chainName, ok := supportedChainIDs[*donorChainID]
	if !ok {
		dieUsage(fmt.Sprintf("--donor-chain-id %d is not a supported chain (allowed: %s)", *donorChainID, supportedChainList()))
	}
	if *donorPath == *gatewayPath {
		log.Fatalf("--donor-path and --gateway-path are the same (%s) — refusing to merge a DB into itself", *donorPath)
	}

	fmt.Println("Hetzner → gateway merge")
	fmt.Println("=======================")
	fmt.Printf("Donor path:     %s\n", *donorPath)
	fmt.Printf("Donor chain:    %d (%s)\n", *donorChainID, chainName)
	fmt.Printf("Gateway path:   %s\n", *gatewayPath)
	fmt.Printf("Mode:           %s\n", modeLabel(*dryRun))
	fmt.Printf("Started at:     %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Println()

	donor, err := storage.NewWithPath(*donorPath)
	if err != nil {
		log.Fatalf("open donor BadgerDB at %s: %v\n\nWas the donor aggregator stopped before snapshotting?", *donorPath, err)
	}
	defer donor.Close()

	gateway, err := storage.NewWithPath(*gatewayPath)
	if err != nil {
		log.Fatalf("open gateway BadgerDB at %s: %v\n\nIs the Railway gateway still running? It must be stopped first for --dry-run=false.", *gatewayPath, err)
	}
	defer gateway.Close()

	mergeStats := newStats()

	// Walk every prefix the dispatcher recognizes. Hard-fail when we see
	// a key with an unknown prefix UNLESS --fail-on-unknown-prefix=false
	// (which only emergency-recovery investigations should ever pass).
	allPrefixes := knownPrefixes()
	sort.Strings(allPrefixes) // deterministic output ordering

	for _, prefix := range allPrefixes {
		stat := mergeStats.forPrefix(prefix)
		items, err := donor.GetByPrefix([]byte(prefix))
		if err != nil {
			log.Fatalf("scan donor prefix %q: %v", prefix, err)
		}
		for _, kv := range items {
			stat.scanned++
			if err := dispatch(donor, gateway, *donorChainID, kv, stat, *dryRun, *verbose); err != nil {
				stat.errored++
				log.Printf("ERROR on key %q: %v", string(kv.Key), err)
			}
		}
	}

	// Unknown-prefix scan: anything in the donor whose key doesn't start
	// with one of the prefixes we know about is by definition unhandled.
	// This catches future schema additions that drop in without the merge
	// tool being updated.
	unknown, err := scanForUnknownPrefixes(donor, allPrefixes)
	if err != nil {
		log.Fatalf("scan for unknown prefixes: %v", err)
	}
	if len(unknown) > 0 {
		fmt.Println()
		fmt.Printf("⚠️  Donor has keys with %d prefix(es) not recognized by this tool:\n", len(unknown))
		for prefix, count := range unknown {
			fmt.Printf("    %q  (%d keys)\n", prefix, count)
		}
		fmt.Println()
		if *failOnUnknownPrefix {
			log.Fatalf("Hard-failing per --fail-on-unknown-prefix=true. Either extend handlers.go with policy for these prefixes, or pass --fail-on-unknown-prefix=false (NOT recommended — silently drops data).")
		}
		fmt.Println("Continuing because --fail-on-unknown-prefix=false. The unknown-prefix keys have been DROPPED from this merge.")
	}

	fmt.Println()
	mergeStats.print(*donorChainID, chainName)

	if *dryRun {
		fmt.Println()
		fmt.Println("DRY RUN — no changes written. Re-run with --dry-run=false to apply.")
	}
}

// scanForUnknownPrefixes walks every key in the donor and bucketizes those
// whose key does not start with any of the prefixes the dispatcher knows
// about. Returns prefix → count of unknown keys (where "prefix" here is
// the first 32 bytes of the unknown key, truncated for display).
//
// Implementation: for each unknown key, take everything up to the first
// ':' as the prefix label. This matches how the schema names its prefixes
// (`t:`, `u:`, `history:`, etc.) and gives a useful aggregate without
// printing every key.
func scanForUnknownPrefixes(donor storage.Storage, knownPrefixes []string) (map[string]int, error) {
	unknown := map[string]int{}
	// GetByPrefix("") returns the whole DB.
	items, err := donor.GetByPrefix([]byte(""))
	if err != nil {
		return nil, err
	}
items:
	for _, kv := range items {
		k := string(kv.Key)
		for _, p := range knownPrefixes {
			if len(k) >= len(p) && k[:len(p)] == p {
				continue items
			}
		}
		// Aggregate by everything up to (and including) the first colon
		// — gives a useful label like "newprefix:" rather than printing
		// the whole arbitrary key.
		label := k
		for i := 0; i < len(k); i++ {
			if k[i] == ':' {
				label = k[:i+1]
				break
			}
		}
		unknown[label]++
	}
	return unknown, nil
}

func supportedChainList() string {
	parts := make([]string, 0, len(supportedChainIDs))
	for id, name := range supportedChainIDs {
		parts = append(parts, fmt.Sprintf("%d=%s", id, name))
	}
	sort.Strings(parts)
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func dieUsage(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	fmt.Fprintln(os.Stderr)
	flag.Usage()
	os.Exit(2)
}

func modeLabel(dryRun bool) string {
	if dryRun {
		return "DRY RUN (no writes)"
	}
	return "APPLY (writes to gateway)"
}
