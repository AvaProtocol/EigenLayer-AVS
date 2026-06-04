package hetznermerge

import (
	"sort"
)

// prefixStats tracks what happened to keys under a single prefix during
// one merge run. All counters start at zero.
type prefixStats struct {
	prefix string
	policy string

	scanned           int // donor keys seen under this prefix
	copied            int // written to gateway (or would be, in dry-run); includes collisionResolved
	stampedChain      int // subset of copied where the key was rewritten with a chain prefix
	skippedExists     int // gateway already had the key — preserved
	collisionResolved int // subset of copied where an existing gateway value was overwritten (execution_index_counter: only, when donor counter > gateway counter)
	dropped           int // explicit drop policy
	errored           int // handler returned an error (logged separately)
}

type stats struct {
	perPrefix map[string]*prefixStats
}

func newStats() *stats {
	s := &stats{perPrefix: make(map[string]*prefixStats, len(prefixHandlers))}
	for _, h := range prefixHandlers {
		s.perPrefix[h.prefix] = &prefixStats{prefix: h.prefix, policy: h.policy}
	}
	return s
}

func (s *stats) forPrefix(prefix string) *prefixStats {
	if v, ok := s.perPrefix[prefix]; ok {
		return v
	}
	// Lazy-init for any future prefix added at runtime; should not happen
	// in practice because the keys are seeded from prefixHandlers.
	v := &prefixStats{prefix: prefix}
	s.perPrefix[prefix] = v
	return v
}

func (s *stats) print(w Writer, donorChainID int64, chainName string) {
	w.Println("Summary")
	w.Println("-------")
	w.Printf("Donor chain: %d (%s)\n", donorChainID, chainName)
	w.Println()

	// Sort prefixes by their order in prefixHandlers (the canonical layout)
	// instead of alphabetical — keeps related rows together.
	ordered := make([]string, 0, len(s.perPrefix))
	for _, h := range prefixHandlers {
		ordered = append(ordered, h.prefix)
	}
	// Include any lazy-added prefixes at the bottom in alphabetical order
	// so they're visible if they ever appear.
	extras := []string{}
	for k := range s.perPrefix {
		if _, ok := lookupPrefixIndex(k); !ok {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)
	ordered = append(ordered, extras...)

	// Column widths chosen by the longest values we know about.
	// CollRes is the subset of Copied that overwrote an existing gateway
	// value (execution_index_counter: max-on-collision); brand-new keys
	// show under Copied only.
	w.Printf("%-30s %-50s %8s %8s %8s %8s %8s %8s %8s\n",
		"Prefix", "Policy", "Scanned", "Copied", "CollRes", "Stamped", "Existed", "Dropped", "Errors")
	w.Println("----------------------------- " +
		"-------------------------------------------------- " +
		"-------- -------- -------- -------- -------- -------- --------")

	totals := prefixStats{}
	for _, p := range ordered {
		ps := s.perPrefix[p]
		if ps.scanned == 0 && ps.dropped == 0 && ps.errored == 0 {
			continue // suppress zero rows for readability
		}
		w.Printf("%-30s %-50s %8d %8d %8d %8d %8d %8d %8d\n",
			ps.prefix, ps.policy,
			ps.scanned, ps.copied, ps.collisionResolved, ps.stampedChain, ps.skippedExists, ps.dropped, ps.errored)
		totals.scanned += ps.scanned
		totals.copied += ps.copied
		totals.collisionResolved += ps.collisionResolved
		totals.stampedChain += ps.stampedChain
		totals.skippedExists += ps.skippedExists
		totals.dropped += ps.dropped
		totals.errored += ps.errored
	}
	w.Println("----------------------------- " +
		"-------------------------------------------------- " +
		"-------- -------- -------- -------- -------- -------- --------")
	w.Printf("%-30s %-50s %8d %8d %8d %8d %8d %8d %8d\n",
		"TOTAL", "",
		totals.scanned, totals.copied, totals.collisionResolved, totals.stampedChain, totals.skippedExists, totals.dropped, totals.errored)

	if totals.errored > 0 {
		w.Println()
		w.Printf("⚠️  %d errors occurred during merge — review the ERROR log lines above.\n", totals.errored)
	}
}

// lookupPrefixIndex returns the position of prefix in prefixHandlers and
// whether it was found. Used by print() to detect lazy-added prefixes.
func lookupPrefixIndex(prefix string) (int, bool) {
	for i, h := range prefixHandlers {
		if h.prefix == prefix {
			return i, true
		}
	}
	return -1, false
}
