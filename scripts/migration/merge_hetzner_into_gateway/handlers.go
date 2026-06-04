package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// handlerFunc processes a single donor key-value, writing zero or one
// keys to the gateway under skip-if-exists semantics. The matched prefix
// (from prefixHandlers) is passed through so handlers that need to strip
// it (handleStampChainID) don't have to re-find it — and so the dispatch
// table can hold function references without inducing an init cycle.
//
// The handler must NEVER overwrite a key the gateway already has.
type handlerFunc func(donor, gateway storage.Storage, donorChainID int64, matchedPrefix string, kv *storage.KeyValueItem, stat *prefixStats, dryRun, verbose bool) error

// prefixHandlerEntry is the row type of the dispatch table.
type prefixHandlerEntry struct {
	prefix string
	handle handlerFunc
	policy string // short label that prints in summary
}

// prefixHandlers is the dispatch table. The dispatcher walks it in order
// and picks the first matching prefix — so put `ct:cw:` BEFORE `ct:` if
// the latter is ever added (currently only `ct:cw:` exists).
//
// IMPORTANT: keep this table aligned with the per-prefix policy matrix in
// `docs/changes/20260604-hetzner-data-restore-plan.md` (avs-infra).
// Editing one without the other invites silent divergence.
var prefixHandlers = []prefixHandlerEntry{
	// Already chain-scoped on disk (MigrateKeysToChainScoped ran on every
	// donor pre-merge). Validate embedded chain ID matches the donor.
	{"t:", handleChainScopedPassThrough, "pass-through (chain-scoped)"},
	{"u:", handleChainScopedPassThrough, "pass-through (chain-scoped)"},
	{"history:", handleChainScopedPassThrough, "pass-through (chain-scoped)"},

	// Chain-implicit on the donor. Stamp `--donor-chain-id` into the key
	// when writing to gateway. Gateway read-path update ships separately.
	{"w:", handleStampChainID, "stamp chain ID"},
	{"wsalt:", handleStampChainID, "stamp chain ID (rebuild wsalt index post-merge)"},
	{"fl:", handleStampChainID, "stamp chain ID"},
	{"fr:", handleStampChainID, "stamp chain ID"},

	// Already user/org/workflow scoped — chain doesn't apply.
	{"secret:", handleImportAsIs, "import as-is"},

	// Monotonic counters keyed by taskID — taskID is globally unique, so
	// no chain stamping. On collision, keep the larger value.
	{"execution_index_counter:", handleMaxOnCollision, "max-on-collision"},

	// Drop policies — cosmetic, transient, or reconstructible.
	{"ct:cw:", handleDrop, "drop (cosmetic)"},
	{"pending:", handleDrop, "drop (transient)"},
	{"trigger:", handleDrop, "drop (reconstructible from history)"},

	// Donor migration markers describe the donor's history, not the
	// gateway's — never import.
	{"migration:", handleDrop, "drop (donor history not gateway's)"},
}

func knownPrefixes() []string {
	out := make([]string, 0, len(prefixHandlers))
	for _, h := range prefixHandlers {
		out = append(out, h.prefix)
	}
	return out
}

func dispatch(donor, gateway storage.Storage, donorChainID int64, kv *storage.KeyValueItem, stat *prefixStats, dryRun, verbose bool) error {
	for _, h := range prefixHandlers {
		if hasPrefix(kv.Key, h.prefix) {
			if verbose {
				fmt.Printf("  [%s] %q\n", h.policy, string(kv.Key))
			}
			return h.handle(donor, gateway, donorChainID, h.prefix, kv, stat, dryRun, verbose)
		}
	}
	return fmt.Errorf("no handler matched key %q (caller should have routed via scanForUnknownPrefixes)", string(kv.Key))
}

func hasPrefix(key []byte, prefix string) bool {
	if len(key) < len(prefix) {
		return false
	}
	return string(key[:len(prefix)]) == prefix
}

// -------------------------------------------------------------------------
// Handlers
// -------------------------------------------------------------------------

// handleChainScopedPassThrough copies a key that's already chain-scoped
// on the donor (per MigrateKeysToChainScoped) straight into the gateway,
// after validating the embedded chain ID matches --donor-chain-id.
//
// Layout: `<prefix><chainID>:<rest...>` — second `:`-delimited segment is
// the chain ID. Legacy (unprefixed) form aborts the merge: the donor was
// supposed to be migrated already.
func handleChainScopedPassThrough(donor, gateway storage.Storage, donorChainID int64, _ string, kv *storage.KeyValueItem, stat *prefixStats, dryRun, verbose bool) error {
	embedded, parseErr := parseChainIDFromKey(kv.Key)
	if errors.Is(parseErr, taskengine.ErrLegacyKey) {
		return fmt.Errorf("donor key %q is in legacy (pre-chain-scoped) form — run the chain-scope migration on the donor before merging", string(kv.Key))
	}
	if parseErr != nil {
		return fmt.Errorf("parse chain ID from %q: %w", string(kv.Key), parseErr)
	}
	if embedded != donorChainID {
		return fmt.Errorf("donor key %q embeds chain ID %d but --donor-chain-id is %d — refuse to mis-stamp", string(kv.Key), embedded, donorChainID)
	}

	return setIfAbsent(gateway, kv.Key, kv.Value, stat, dryRun)
}

// handleStampChainID stamps `--donor-chain-id` into the key. For prefix
// `p:` the donor key is `p:<rest>` and the gateway key becomes
// `p:<chainID>:<rest>`. Used for chain-implicit families (w:, wsalt:,
// fl:, fr:) that don't have their own chain_scoped_keys-style migration
// yet.
func handleStampChainID(donor, gateway storage.Storage, donorChainID int64, matchedPrefix string, kv *storage.KeyValueItem, stat *prefixStats, dryRun, verbose bool) error {
	if matchedPrefix == "" {
		return fmt.Errorf("internal: handleStampChainID called without a matched prefix for key %q", string(kv.Key))
	}
	rest := string(kv.Key[len(matchedPrefix):])

	// Safety: if the donor key is ALREADY stamped (rest starts with the
	// chain ID followed by ':'), skip the additional stamp and treat as
	// chain-scoped pass-through. This guards against double-running the
	// tool or against donors that ran a future migration we don't know
	// about.
	if alreadyStamped(rest, donorChainID) {
		return setIfAbsent(gateway, kv.Key, kv.Value, stat, dryRun)
	}
	stamped := []byte(fmt.Sprintf("%s%d:%s", matchedPrefix, donorChainID, rest))
	stat.stampedChain++
	return setIfAbsent(gateway, stamped, kv.Value, stat, dryRun)
}

// handleImportAsIs copies the key verbatim. Used for secret:, which is
// already user/org/workflow scoped and where chain doesn't apply.
func handleImportAsIs(donor, gateway storage.Storage, donorChainID int64, _ string, kv *storage.KeyValueItem, stat *prefixStats, dryRun, verbose bool) error {
	return setIfAbsent(gateway, kv.Key, kv.Value, stat, dryRun)
}

// handleMaxOnCollision is used for execution_index_counter:<taskID>. The
// counter is a monotonic per-task uint64. If both donor and gateway have
// a counter for the same taskID, take the larger — a smaller value would
// cause the gateway to re-issue execution indices that already exist on
// the donor.
//
// taskID is globally unique across chains in our schema, so no chain
// stamping is needed.
func handleMaxOnCollision(donor, gateway storage.Storage, donorChainID int64, _ string, kv *storage.KeyValueItem, stat *prefixStats, dryRun, verbose bool) error {
	existing, err := gateway.GetKey(kv.Key)
	if err != nil {
		if isKeyNotFoundError(err) {
			stat.copied++
			if dryRun {
				return nil
			}
			return gateway.Set(kv.Key, kv.Value)
		}
		return fmt.Errorf("get gateway %q: %w", string(kv.Key), err)
	}
	donorVal, derr := strconv.ParseUint(string(kv.Value), 10, 64)
	if derr != nil {
		return fmt.Errorf("parse donor counter %q as uint64: %w", string(kv.Value), derr)
	}
	gwVal, gerr := strconv.ParseUint(string(existing), 10, 64)
	if gerr != nil {
		return fmt.Errorf("parse gateway counter %q as uint64: %w", string(existing), gerr)
	}
	if donorVal > gwVal {
		stat.collisionResolved++
		if dryRun {
			return nil
		}
		return gateway.Set(kv.Key, kv.Value)
	}
	stat.skippedExists++
	return nil
}

// handleDrop ignores the donor key entirely. Used for cosmetic / transient /
// gateway-own-history prefixes per the matrix.
func handleDrop(donor, gateway storage.Storage, donorChainID int64, _ string, kv *storage.KeyValueItem, stat *prefixStats, dryRun, verbose bool) error {
	stat.dropped++
	return nil
}

// -------------------------------------------------------------------------
// Lower-level helpers
// -------------------------------------------------------------------------

// setIfAbsent is the ONLY write path in this tool. It guarantees that
// nothing the gateway already has gets overwritten. Updates stats based
// on the path taken.
func setIfAbsent(gateway storage.Storage, key, value []byte, stat *prefixStats, dryRun bool) error {
	exists, err := gateway.Exist(key)
	if err != nil {
		return fmt.Errorf("Exist check for %q: %w", string(key), err)
	}
	if exists {
		stat.skippedExists++
		return nil
	}
	stat.copied++
	if dryRun {
		return nil
	}
	return gateway.Set(key, value)
}

// parseChainIDFromKey extracts the chain ID from a chain-scoped key like
// `t:1:a:taskID` or `history:1:taskID:executionID`. Returns
// taskengine.ErrLegacyKey if the second segment isn't a numeric chain ID.
func parseChainIDFromKey(key []byte) (int64, error) {
	s := string(key)
	first := strings.IndexByte(s, ':')
	if first < 0 {
		return 0, taskengine.ErrLegacyKey
	}
	rest := s[first+1:]
	second := strings.IndexByte(rest, ':')
	if second < 0 {
		return 0, taskengine.ErrLegacyKey
	}
	candidate := rest[:second]
	id, err := strconv.ParseInt(candidate, 10, 64)
	if err != nil {
		return 0, taskengine.ErrLegacyKey
	}
	return id, nil
}

// alreadyStamped returns true when the donor key fragment (everything
// after the matched prefix) appears to already be chain-stamped with the
// given chain ID — i.e. it starts with `<chainID>:`. Used by
// handleStampChainID to skip double-stamping if the donor ran a future
// migration the tool doesn't know about.
func alreadyStamped(rest string, donorChainID int64) bool {
	prefix := strconv.FormatInt(donorChainID, 10) + ":"
	return strings.HasPrefix(rest, prefix)
}

// isKeyNotFoundError lets us treat "key not present" as a non-error case
// without importing badger directly. The storage package surfaces the
// badger error verbatim from GetKey.
func isKeyNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Key not found")
}
