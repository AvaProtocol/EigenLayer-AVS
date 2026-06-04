// Package hetznermerge is the library implementation of the
// Hetzner -> Railway-gateway data merge tool. It exists in core/ so both
// the standalone script at scripts/migration/merge_hetzner_into_gateway/
// AND the `ap-avs migrate-hetzner` Cobra subcommand can call into it
// without duplicating the dispatch/handler/stats logic.
//
// The actual per-prefix policy lives in handlers.go; per-prefix counters
// + summary printing in stats.go. This file defines the public entry
// point Run() that both callers share.
//
// For an end-to-end overview of WHY this tool exists, what each prefix
// does, and the operational safety rules, see:
//
//   - scripts/migration/merge_hetzner_into_gateway/README.md
//   - avs-infra/docs/changes/20260604-hetzner-data-restore-plan.md
//   - avs-infra/docs/changes/20260605-hetzner-merge-production-runbook.md
package hetznermerge

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// SupportedChainIDs gates Options.DonorChainID to avoid fat-finger
// merges (e.g. accidentally importing the Sepolia donor with chain-id 1).
// Exposed so callers can build their own validation messages without
// re-defining the map.
var SupportedChainIDs = map[int64]string{
	1:        "ethereum",
	8453:     "base",
	11155111: "sepolia",
	84532:    "base-sepolia",
}

// Options is the set of inputs the merge run needs. Mirrors the flags
// of the standalone CLI 1:1 so the standalone wrapper and the Cobra
// subcommand can both populate this struct and call Run.
type Options struct {
	DonorPath           string // BadgerDB dir for the donor (Hetzner aggregator snapshot)
	DonorChainID        int64  // chain ID for the donor; must be in SupportedChainIDs
	GatewayPath         string // BadgerDB dir for the gateway (merge destination)
	DryRun              bool   // print what would change without writing
	Verbose             bool   // print one line per key processed
	FailOnUnknownPrefix bool   // hard-fail when donor has prefixes the dispatch doesn't know
}

// Validate returns an error explaining the first invalid Options field,
// or nil when the inputs are usable. Callers should surface the error
// directly to the user (it's already formatted to match the CLI flag
// names operators see).
func (o Options) Validate() error {
	if o.DonorPath == "" {
		return fmt.Errorf("--donor-path is required")
	}
	if o.GatewayPath == "" {
		return fmt.Errorf("--gateway-path is required")
	}
	if o.DonorChainID == 0 {
		return fmt.Errorf("--donor-chain-id is required")
	}
	if _, ok := SupportedChainIDs[o.DonorChainID]; !ok {
		return fmt.Errorf("--donor-chain-id %d is not a supported chain (allowed: %s)",
			o.DonorChainID, supportedChainList())
	}
	if o.DonorPath == o.GatewayPath {
		return fmt.Errorf("--donor-path and --gateway-path are the same (%s) — refusing to merge a DB into itself",
			o.DonorPath)
	}
	return nil
}

// Result reports what happened. The caller decides whether to surface
// errored == 0 as success or to fail the process; the merge tool's
// historical behavior is to keep going on per-key errors and let the
// summary table reflect the count.
type Result struct {
	Stats               *stats
	UnknownPrefix       map[string]int // label -> count of donor keys under an unrecognized prefix
	HardFailedOnUnknown bool           // true when Run aborted because of unknown prefixes + FailOnUnknownPrefix
}

// Run is the merge tool's library entry point. Both
// scripts/migration/merge_hetzner_into_gateway/main.go (the standalone
// CLI) and cmd/migrateHetzner.go (the `ap-avs migrate-hetzner`
// subcommand) call this.
//
// It opens the donor + gateway BadgerDBs in read-write mode (Badger
// requires the write lock even for the dry-run path), walks every
// known prefix on the donor, dispatches each key through the per-prefix
// handler in handlers.go, accumulates per-prefix counters in stats.go,
// and writes results to the gateway via setIfAbsent (skip-if-exists).
//
// Errors from individual donor keys are logged via the writer interface
// (see writer below) and counted in stats.errored — the merge continues.
// The function returns a non-nil error only for setup failures (bad
// options, can't open a DB, can't iterate) or when FailOnUnknownPrefix
// hits.
//
// The writer parameter receives every progress / summary line. Pass
// os.Stdout for CLI usage; tests pass a *bytes.Buffer.
func Run(opts Options, writer Writer) (*Result, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	chainName := SupportedChainIDs[opts.DonorChainID]

	writer.Println("Hetzner → gateway merge")
	writer.Println("=======================")
	writer.Printf("Donor path:     %s\n", opts.DonorPath)
	writer.Printf("Donor chain:    %d (%s)\n", opts.DonorChainID, chainName)
	writer.Printf("Gateway path:   %s\n", opts.GatewayPath)
	writer.Printf("Mode:           %s\n", modeLabel(opts.DryRun))
	writer.Printf("Started at:     %s\n", time.Now().UTC().Format(time.RFC3339))
	writer.Println()

	donor, err := storage.NewWithPath(opts.DonorPath)
	if err != nil {
		return nil, fmt.Errorf("open donor BadgerDB at %s: %w\n\nWas the donor aggregator stopped before snapshotting?", opts.DonorPath, err)
	}
	defer donor.Close()

	gateway, err := storage.NewWithPath(opts.GatewayPath)
	if err != nil {
		return nil, fmt.Errorf("open gateway BadgerDB at %s: %w\n\nThe tool opens Badger in read-write mode (it needs the write lock even for --dry-run, since dry-run still acquires the lock to guarantee no other writer is racing it). If the Railway gateway service is still running against this volume, stop it before re-running.", opts.GatewayPath, err)
	}
	defer gateway.Close()

	mergeStats := newStats()
	result := &Result{Stats: mergeStats}

	// Walk every prefix the dispatcher recognizes. Hard-fail when we see
	// a key with an unknown prefix UNLESS FailOnUnknownPrefix is false
	// (which only emergency-recovery investigations should ever pass).
	//
	// Important: dedupe outer-loop prefixes to skip strict extensions of
	// another known prefix (e.g. `t:seq` is covered by `t:`, `q:seq:` by
	// `q:`). Without this, every t:seq key gets iterated twice: once via
	// GetByPrefix("t:") and once via GetByPrefix("t:seq") — bloating the
	// scanned count and confusingly attributing the second pass to the
	// shorter-prefix row of the summary. The dispatch table inside
	// handlers.go still has the more specific entries first, so the
	// correct (drop) handler still fires.
	allPrefixes := outerLoopPrefixes(knownPrefixes())
	sort.Strings(allPrefixes) // deterministic output ordering

	for _, prefix := range allPrefixes {
		stat := mergeStats.forPrefix(prefix)

		// Drop-only prefixes don't need values — stream keys via the
		// constant-memory IterateKeysOnly to avoid materializing every
		// K/V into a slice. The q: family alone has 7–10M entries per
		// mainnet donor; loading all of them at once is ~GBs of value
		// payload that gets thrown away by handleDrop anyway.
		if isDropOnly(prefix) {
			err := donor.IterateKeysOnly([]byte(prefix), func(key []byte) error {
				stat.scanned++
				stat.dropped++
				if opts.Verbose {
					writer.Printf("  [drop] %q\n", string(key))
				}
				return nil
			})
			if err != nil {
				return result, fmt.Errorf("stream-scan donor prefix %q: %w", prefix, err)
			}
			continue
		}

		items, err := donor.GetByPrefix([]byte(prefix))
		if err != nil {
			return result, fmt.Errorf("scan donor prefix %q: %w", prefix, err)
		}
		for _, kv := range items {
			stat.scanned++
			if err := dispatch(donor, gateway, opts.DonorChainID, kv, stat, opts.DryRun, opts.Verbose); err != nil {
				stat.errored++
				writer.Printf("ERROR on key %q: %v\n", string(kv.Key), err)
			}
		}
	}

	// Unknown-prefix scan: anything in the donor whose key doesn't start
	// with one of the prefixes we know about is by definition unhandled.
	// This catches future schema additions that drop in without the merge
	// tool being updated.
	unknown, err := scanForUnknownPrefixes(donor, allPrefixes)
	if err != nil {
		return result, fmt.Errorf("scan for unknown prefixes: %w", err)
	}
	result.UnknownPrefix = unknown
	if len(unknown) > 0 {
		writer.Println()
		writer.Printf("⚠️  Donor has keys with %d prefix(es) not recognized by this tool:\n", len(unknown))
		for prefix, count := range unknown {
			writer.Printf("    %q  (%d keys)\n", prefix, count)
		}
		writer.Println()
		if opts.FailOnUnknownPrefix {
			result.HardFailedOnUnknown = true
			return result, fmt.Errorf("hard-failing per --fail-on-unknown-prefix=true. Either extend handlers.go with policy for these prefixes, or pass --fail-on-unknown-prefix=false (NOT recommended — silently drops data)")
		}
		writer.Println("Continuing because --fail-on-unknown-prefix=false. The unknown-prefix keys have been DROPPED from this merge.")
	}

	writer.Println()
	mergeStats.print(writer, opts.DonorChainID, chainName)

	if opts.DryRun {
		writer.Println()
		writer.Println("DRY RUN — no changes written. Re-run with --dry-run=false to apply.")
	}

	return result, nil
}

// Writer is the minimal output sink the package emits to. Both the CLI
// (os.Stdout) and tests (*bytes.Buffer) satisfy it.
type Writer interface {
	Println(args ...any)
	Printf(format string, args ...any)
}

// scanForUnknownPrefixes walks every key in the donor via the storage
// streaming iterator (KeysOnly — values are never fetched and no
// per-key slice is built, so memory stays constant in the size of the
// donor). Bucketizes any key whose prefix is not in `knownPrefixes`.
//
// Returned label is everything up to and including the first ':' in the
// unknown key (e.g. "newprefix:") — matches the schema's naming
// convention. Keys without a colon are bucketed under the full key.
//
// Avoids `string(key)` on every key — that would allocate millions of
// short strings just to immediately discard them when the key matches
// a known prefix (which is the overwhelmingly common case). Compares
// against pre-encoded []byte prefixes and only converts to string when
// bucketing an actual unknown key.
func scanForUnknownPrefixes(donor storage.Storage, knownPrefixes []string) (map[string]int, error) {
	knownPrefixesBytes := make([][]byte, len(knownPrefixes))
	for i, p := range knownPrefixes {
		knownPrefixesBytes[i] = []byte(p)
	}

	unknown := map[string]int{}
	err := donor.IterateKeysOnly([]byte(""), func(key []byte) error {
		for _, pb := range knownPrefixesBytes {
			if bytes.HasPrefix(key, pb) {
				return nil
			}
		}
		// Unknown — pay the string-alloc cost only for the bucketing label.
		k := string(key)
		label := k
		for i := 0; i < len(k); i++ {
			if k[i] == ':' {
				label = k[:i+1]
				break
			}
		}
		unknown[label]++
		return nil
	})
	if err != nil {
		return nil, err
	}
	return unknown, nil
}

// outerLoopPrefixes returns the subset of `all` that should drive the
// outer GetByPrefix loop in Run: for any two prefixes A and B in the
// input where A is a strict prefix of B, only A is returned. This keeps
// keys from being processed twice when the dispatch table contains
// both a general and a more-specific entry for the same family (e.g.
// `t:` and `t:seq`).
func outerLoopPrefixes(all []string) []string {
	out := make([]string, 0, len(all))
nextCandidate:
	for _, candidate := range all {
		for _, other := range all {
			if other == candidate {
				continue
			}
			if len(other) < len(candidate) && candidate[:len(other)] == other {
				// `other` is a strict prefix of `candidate` — skip `candidate`.
				continue nextCandidate
			}
		}
		out = append(out, candidate)
	}
	return out
}

func supportedChainList() string {
	parts := make([]string, 0, len(SupportedChainIDs))
	for id, name := range SupportedChainIDs {
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

func modeLabel(dryRun bool) string {
	if dryRun {
		return "DRY RUN (no writes)"
	}
	return "APPLY (writes to gateway)"
}
