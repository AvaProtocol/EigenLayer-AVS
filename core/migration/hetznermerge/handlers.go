package hetznermerge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"google.golang.org/protobuf/encoding/protojson"
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
	// dropOnly marks entries whose handler discards the value and
	// returns immediately. Lets the outer scan loop use
	// IterateKeysOnly (constant memory, no value fetch) instead of
	// GetByPrefix (materializes every K/V into a slice). Critical for
	// the q: family which has 7–10M entries on mainnet donors.
	dropOnly bool
}

// prefixHandlers is the dispatch table. The dispatcher walks it in order
// and picks the first matching prefix — so the order matters when one
// prefix is the prefix of another. In particular, `t:seq` (Badger
// sequence counter) is listed BEFORE `t:` so the generic t-handler
// doesn't try to chain-stamp the sequence-counter key, and `q:seq:`
// before `q:` for the same reason.
//
// IMPORTANT: keep this table aligned with the per-prefix policy matrix in
// `docs/changes/20260604-hetzner-data-restore-plan.md` (avs-infra).
// Editing one without the other invites silent divergence.
var prefixHandlers = []prefixHandlerEntry{
	// Per-DB Badger sequence counters. Per-aggregator operational state;
	// the gateway maintains its own sequences. Must be matched BEFORE
	// the generic `t:` / `q:` handlers below — otherwise handleStampChainID
	// would corrupt them into `t:<chainID>:seq` etc.
	{"t:seq", handleDrop, "drop (Badger sequence counter, per-aggregator)", true},
	{"q:seq:", handleDrop, "drop (Badger sequence counter, per-aggregator)", true},

	// Workflow + execution history. On every Hetzner donor (which predates
	// chain-scoped storage) these are in legacy form (t:<status>:<task>,
	// history:<task>:<execution>). We:
	//
	//   1. stamp the chain ID into the KEY (the equivalent of running
	//      MigrateKeysToChainScoped on the donor, folded into the merge)
	//   2. decode the VALUE with protojson DiscardUnknown:true to tolerate
	//      proto fields that were renamed/removed over time
	//      (expression/epochs/totalExecution/interval/input — surfaced by
	//      the 2026-06-04 rehearsal where ~46% of mainnet tasks
	//      would have failed strict decode)
	//   3. set the body's chain_id field to the donor chain ID (Task
	//      proto only) so subsequent updates don't default it to the
	//      gateway's defaultChainID and silently re-key the task
	//   4. re-marshal with the current proto schema, which drops the
	//      unknown fields permanently — one-time data cleanup
	//
	// `u:` is just a status byte slice (no body to clean) and stays on
	// the plain key-stamp handler.
	{"t:", handleStampTaskBody, "stamp chain ID + clean Task body (drop unknown proto fields, set chain_id)", false},
	{"u:", handleStampChainID, "stamp chain ID (status byte slice, no body)", false},
	{"history:", handleStampExecutionBody, "stamp chain ID + clean Execution body (drop unknown proto fields)", false},

	// Chain-implicit on the donor. Stamp `--donor-chain-id` into the key
	// when writing to gateway.
	{"w:", handleStampChainID, "stamp chain ID", false},
	{"wsalt:", handleStampChainID, "stamp chain ID (rebuild wsalt index post-merge)", false},
	{"fl:", handleStampChainID, "stamp chain ID", false},
	{"fr:", handleStampFeeRecordBody, "stamp chain ID + set FeeRecord.ChainID", false},

	// Already user/org/workflow scoped — chain doesn't apply.
	{"secret:", handleImportAsIs, "import as-is", false},

	// Monotonic counters keyed by taskID — taskID is globally unique, so
	// no chain stamping. On collision, keep the larger value.
	{"execution_index_counter:", handleMaxOnCollision, "max-on-collision", false},

	// Drop policies — cosmetic, transient, reconstructible, or
	// per-aggregator operational state. dropOnly=true lets the outer
	// scan loop stream keys via IterateKeysOnly (constant memory)
	// instead of materializing every K/V slice — critical for the
	// q: family which has 7–10M entries on mainnet donors.
	{"ct:cw:", handleDrop, "drop (cosmetic)", true},
	{"pending:", handleDrop, "drop (transient)", true},
	{"trigger:", handleDrop, "drop (reconstructible from history)", true},
	{"operator:", handleDrop, "drop (per-aggregator operator pool registry)", true},
	{"q:", handleDrop, "drop (per-aggregator async execution queue)", true},

	// Donor migration markers describe the donor's history, not the
	// gateway's — never import.
	{"migration:", handleDrop, "drop (donor history not gateway's)", true},
}

// isDropOnly returns whether the dispatch entry for `prefix` is a
// drop-only handler (no value needed, no write). Used by the outer
// scan loop to switch from GetByPrefix (materializes K/V slice) to
// IterateKeysOnly (constant memory).
func isDropOnly(prefix string) bool {
	for _, h := range prefixHandlers {
		if h.prefix == prefix {
			return h.dropOnly
		}
	}
	return false
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

// stampedKey applies the same key-stamping rules handleStampChainID uses,
// returning either the original kv.Key (when already chain-scoped on the
// donor) or a new key with `<donorChainID>:` inserted after matchedPrefix
// (when legacy). The bool says whether stamping happened (so callers can
// increment stat.stampedChain only when the key actually changed).
//
// Returns a wrong-chain-mis-stamp error when the donor key is already
// chain-scoped but with a chain ID that disagrees with --donor-chain-id.
func stampedKey(donorChainID int64, matchedPrefix string, kv *storage.KeyValueItem) (newKey []byte, stamped bool, err error) {
	if matchedPrefix == "" {
		return nil, false, fmt.Errorf("internal: stampedKey called without a matched prefix for key %q", string(kv.Key))
	}
	embedded, parseErr := parseChainIDFromKey(kv.Key)
	if parseErr == nil {
		if embedded != donorChainID {
			return nil, false, fmt.Errorf("donor key %q embeds chain ID %d but --donor-chain-id is %d — refuse to mis-stamp", string(kv.Key), embedded, donorChainID)
		}
		return kv.Key, false, nil
	}
	if !errors.Is(parseErr, taskengine.ErrLegacyKey) {
		return nil, false, fmt.Errorf("parse chain ID from %q: %w", string(kv.Key), parseErr)
	}
	rest := string(kv.Key[len(matchedPrefix):])
	return []byte(fmt.Sprintf("%s%d:%s", matchedPrefix, donorChainID, rest)), true, nil
}

// cleanTaskBody decodes a stored Task with DiscardUnknown=true so old
// proto fields (renamed/removed: expression, epochs, totalExecution,
// interval, input, …) don't reject the whole task, then sets the body's
// chain_id field to donorChainID and re-marshals. Drops the unknown
// fields permanently. Returns the cleaned body or an error if even the
// loose decoder couldn't make sense of it (rare — a handful of
// genuinely malformed rows out of 200-400 per chain).
func cleanTaskBody(value []byte, donorChainID int64) ([]byte, error) {
	var task avsproto.Task
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(value, &task); err != nil {
		return nil, fmt.Errorf("decode task with DiscardUnknown: %w", err)
	}
	task.ChainId = donorChainID
	out, err := protojson.Marshal(&task)
	if err != nil {
		return nil, fmt.Errorf("re-marshal cleaned task: %w", err)
	}
	return out, nil
}

// cleanExecutionBody mirrors cleanTaskBody for the Execution proto. The
// Execution message has no chain_id field of its own (chain is implicit
// in the history:<chainID>:<taskID>:<executionID> key), so this is
// purely a re-serialize that drops unknown fields like "success" (which
// got moved into Step) and others.
func cleanExecutionBody(value []byte) ([]byte, error) {
	var execution avsproto.Execution
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(value, &execution); err != nil {
		return nil, fmt.Errorf("decode execution with DiscardUnknown: %w", err)
	}
	out, err := protojson.Marshal(&execution)
	if err != nil {
		return nil, fmt.Errorf("re-marshal cleaned execution: %w", err)
	}
	return out, nil
}

// cleanFeeRecordBody decodes the donor's FeeRecord (stdlib json), sets
// ChainID to donorChainID (it may have been 0 on old donors since
// FeeRecord.ChainID was added later), and re-marshals. RecordValueFee
// now requires ChainID != 0 to write the chain-scoped fl:/fr: keys.
func cleanFeeRecordBody(value []byte, donorChainID int64) ([]byte, error) {
	// We intentionally use stdlib json (not protojson) because FeeRecord
	// is a plain Go struct, not a proto message. stdlib json ignores
	// unknown fields by default, which is the behavior we want here too.
	var record taskengine.FeeRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return nil, fmt.Errorf("decode fee record: %w", err)
	}
	record.ChainID = donorChainID
	out, err := json.Marshal(&record)
	if err != nil {
		return nil, fmt.Errorf("re-marshal cleaned fee record: %w", err)
	}
	return out, nil
}

// handleStampTaskBody stamps the chain ID into the key AND rewrites the
// stored Task body via cleanTaskBody (drops unknown proto fields + sets
// task.chain_id = donorChainID). See the dispatch-table comment on
// prefixHandlers for the rationale; see cleanTaskBody for what's
// dropped.
func handleStampTaskBody(donor, gateway storage.Storage, donorChainID int64, matchedPrefix string, kv *storage.KeyValueItem, stat *prefixStats, dryRun, verbose bool) error {
	newKey, stamped, err := stampedKey(donorChainID, matchedPrefix, kv)
	if err != nil {
		return err
	}
	cleaned, err := cleanTaskBody(kv.Value, donorChainID)
	if err != nil {
		return err
	}
	if stamped {
		stat.stampedChain++
	}
	return setIfAbsent(gateway, newKey, cleaned, stat, dryRun)
}

// handleStampExecutionBody is the Execution-body analogue.
func handleStampExecutionBody(donor, gateway storage.Storage, donorChainID int64, matchedPrefix string, kv *storage.KeyValueItem, stat *prefixStats, dryRun, verbose bool) error {
	newKey, stamped, err := stampedKey(donorChainID, matchedPrefix, kv)
	if err != nil {
		return err
	}
	cleaned, err := cleanExecutionBody(kv.Value)
	if err != nil {
		return err
	}
	if stamped {
		stat.stampedChain++
	}
	return setIfAbsent(gateway, newKey, cleaned, stat, dryRun)
}

// handleStampFeeRecordBody is the FeeRecord-body analogue. fr: keys are
// always chain-implicit on the donor (chain-scoped form didn't exist
// before this rewrite), so the key is always stamped and the body's
// ChainID is always populated.
func handleStampFeeRecordBody(donor, gateway storage.Storage, donorChainID int64, matchedPrefix string, kv *storage.KeyValueItem, stat *prefixStats, dryRun, verbose bool) error {
	newKey, stamped, err := stampedKey(donorChainID, matchedPrefix, kv)
	if err != nil {
		return err
	}
	cleaned, err := cleanFeeRecordBody(kv.Value, donorChainID)
	if err != nil {
		return err
	}
	if stamped {
		stat.stampedChain++
	}
	return setIfAbsent(gateway, newKey, cleaned, stat, dryRun)
}

// handleStampChainID handles both legacy (chain-implicit) and already-
// chain-scoped donor keys:
//
//   - If the second `:`-delimited segment parses as a numeric chain ID,
//     the key is already chain-scoped. Validate it matches the donor's
//     chain ID and pass it through untouched.
//   - If the second segment is non-numeric (or absent), the key is in
//     legacy form. Insert `<donorChainID>:` right after the matched
//     prefix and write the stamped key.
//
// This is the equivalent of running MigrateKeysToChainScoped on the
// donor — folded into the merge so we don't have to mutate donor data
// before merging, and so donors that have a MIX of legacy and chain-
// scoped keys (because an in-place migration ran partway) still merge
// correctly.
//
// Wrong-chain mis-stamp protection: a donor key like `t:8453:c:taskID`
// merged with --donor-chain-id=1 will hard-fail, not get re-stamped
// into `t:1:8453:c:taskID`.
func handleStampChainID(donor, gateway storage.Storage, donorChainID int64, matchedPrefix string, kv *storage.KeyValueItem, stat *prefixStats, dryRun, verbose bool) error {
	if matchedPrefix == "" {
		return fmt.Errorf("internal: handleStampChainID called without a matched prefix for key %q", string(kv.Key))
	}

	embedded, parseErr := parseChainIDFromKey(kv.Key)
	if parseErr == nil {
		// Already chain-scoped. Refuse to merge a donor key whose
		// embedded chain ID disagrees with --donor-chain-id (e.g.
		// somebody pointed the wrong chain's DB at the wrong --donor-
		// chain-id flag).
		if embedded != donorChainID {
			return fmt.Errorf("donor key %q embeds chain ID %d but --donor-chain-id is %d — refuse to mis-stamp", string(kv.Key), embedded, donorChainID)
		}
		return setIfAbsent(gateway, kv.Key, kv.Value, stat, dryRun)
	}
	if !errors.Is(parseErr, taskengine.ErrLegacyKey) {
		return fmt.Errorf("parse chain ID from %q: %w", string(kv.Key), parseErr)
	}

	// Legacy form — stamp the donor chain ID inline.
	rest := string(kv.Key[len(matchedPrefix):])
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
	exists, err := gateway.Exist(kv.Key)
	if err != nil {
		return fmt.Errorf("Exist check for %q: %w", string(kv.Key), err)
	}
	if !exists {
		stat.copied++
		if dryRun {
			return nil
		}
		return gateway.Set(kv.Key, kv.Value)
	}
	// Both sides have it. Compare counters and take the larger.
	existing, err := gateway.GetKey(kv.Key)
	if err != nil {
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
		// Count as BOTH a write (CollRes is a subset of total writes) and
		// a collision-resolved overwrite, so the summary totals reconcile.
		stat.copied++
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
//
// NOTE: dispatch() never actually calls handleDrop for prefixes that have
// dropOnly: true in their dispatch table entry — the outer scan loop in
// main.go takes a fast path via IterateKeysOnly for those, counting
// stat.scanned++ / stat.dropped++ inline without invoking dispatch at
// all (avoids materializing the full K/V slice for 10M+ q: keys per
// mainnet donor). handleDrop is reached only if a future entry sets
// handleDrop without dropOnly: true. If you add such an entry, make
// sure either the outer loop or this handler does the accounting, not
// both.
func handleDrop(donor, gateway storage.Storage, donorChainID int64, _ string, kv *storage.KeyValueItem, stat *prefixStats, dryRun, verbose bool) error {
	stat.dropped++
	return nil
}

// -------------------------------------------------------------------------
// Lower-level helpers
// -------------------------------------------------------------------------

// setIfAbsent is the skip-if-exists write path used by every handler
// EXCEPT handleMaxOnCollision. When this function writes, the gateway
// definitely did not have the key beforehand — no overwrite is possible
// through this path. handleMaxOnCollision is the one deliberate
// exception: it calls gateway.Set directly when the donor counter is
// larger than the gateway's, because re-issuing already-consumed
// execution indices would be worse than overwriting one uint64.
//
// If you add a new handler, route writes through setIfAbsent unless you
// have a documented reason for collision-overwrite semantics like
// handleMaxOnCollision does.
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

// isKeyNotFoundError lets us treat "key not present" as a non-error case
// without importing badger directly. The storage package surfaces the
// badger error verbatim from GetKey.
func isKeyNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Key not found")
}
