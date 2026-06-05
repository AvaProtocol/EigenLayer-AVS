package hetznermerge

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestDB returns a fresh in-memory BadgerStorage backed by a t.TempDir.
// Caller does not need to defer Close — t.Cleanup handles it.
func newTestDB(t *testing.T) storage.Storage {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.NewWithPath(dir)
	require.NoError(t, err, "open BadgerDB at %s", dir)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func kvItem(key, value string) *storage.KeyValueItem {
	return &storage.KeyValueItem{Key: []byte(key), Value: []byte(value)}
}

// -------------------------------------------------------------------------
// setIfAbsent — the only write path. Single, focused tests.
// -------------------------------------------------------------------------

func TestSetIfAbsent_WritesWhenKeyAbsent(t *testing.T) {
	gw := newTestDB(t)
	stat := &prefixStats{}

	require.NoError(t, setIfAbsent(gw, []byte("t:1:a:abc"), []byte("body"), stat, false))
	assert.Equal(t, 1, stat.copied)
	assert.Equal(t, 0, stat.skippedExists)

	got, err := gw.GetKey([]byte("t:1:a:abc"))
	require.NoError(t, err)
	assert.Equal(t, "body", string(got))
}

func TestSetIfAbsent_SkipsWhenKeyPresent(t *testing.T) {
	gw := newTestDB(t)
	require.NoError(t, gw.Set([]byte("t:1:a:abc"), []byte("gateway-version")))

	stat := &prefixStats{}
	require.NoError(t, setIfAbsent(gw, []byte("t:1:a:abc"), []byte("donor-version"), stat, false))
	assert.Equal(t, 0, stat.copied)
	assert.Equal(t, 1, stat.skippedExists)

	// CRITICAL: the donor's value MUST NOT have overwritten the gateway's.
	got, err := gw.GetKey([]byte("t:1:a:abc"))
	require.NoError(t, err)
	assert.Equal(t, "gateway-version", string(got),
		"setIfAbsent must never overwrite the gateway value (this is the core safety property of the merge tool)")
}

func TestSetIfAbsent_DryRunDoesNotWrite(t *testing.T) {
	gw := newTestDB(t)
	stat := &prefixStats{}

	require.NoError(t, setIfAbsent(gw, []byte("t:1:a:abc"), []byte("body"), stat, true))
	assert.Equal(t, 1, stat.copied)

	_, err := gw.GetKey([]byte("t:1:a:abc"))
	assert.True(t, isKeyNotFoundError(err), "dry-run must not actually write; got %v", err)
}

// -------------------------------------------------------------------------
// handleStampChainID — handles both legacy (chain-implicit) and
// already-chain-scoped donor keys. Validates mis-stamp protection.
// -------------------------------------------------------------------------

func TestHandleStampChainID_PassesThroughMatchingChainScoped(t *testing.T) {
	donor := newTestDB(t)
	gw := newTestDB(t)
	stat := &prefixStats{}

	// Donor key is already chain-scoped with the donor's chain ID — pass through.
	err := handleStampChainID(donor, gw, 1, "t:", kvItem("t:1:a:taskABC", "body"), stat, false, false)
	require.NoError(t, err)
	assert.Equal(t, 1, stat.copied)
	assert.Equal(t, 0, stat.stampedChain, "key was already chain-scoped — must not count as a new stamp")
}

func TestHandleStampChainID_RejectsWrongChainScoped(t *testing.T) {
	donor := newTestDB(t)
	gw := newTestDB(t)
	stat := &prefixStats{}

	// Donor key embeds chain 8453 but we're merging as chain 1 — refuse
	// to silently mis-stamp (would produce t:1:8453:a:taskABC, which
	// would be wrong).
	err := handleStampChainID(donor, gw, 1, "t:", kvItem("t:8453:a:taskABC", "body"), stat, false, false)
	require.Error(t, err, "must refuse to mis-stamp a key whose embedded chain ID disagrees with --donor-chain-id")
	assert.Contains(t, err.Error(), "embeds chain ID 8453")
}

func TestHandleStampChainID_StampsLegacyTaskStatusKey(t *testing.T) {
	donor := newTestDB(t)
	gw := newTestDB(t)
	stat := &prefixStats{}

	// Legacy form: `t:a:taskABC` (status code 'a', no numeric chain segment).
	err := handleStampChainID(donor, gw, 11155111, "t:", kvItem("t:a:taskABC", "body"), stat, false, false)
	require.NoError(t, err)
	assert.Equal(t, 1, stat.copied)
	assert.Equal(t, 1, stat.stampedChain)

	got, err := gw.GetKey([]byte("t:11155111:a:taskABC"))
	require.NoError(t, err)
	assert.Equal(t, "body", string(got))
}

// -------------------------------------------------------------------------
// handleStampChainID — rewrites chain-implicit keys.
// -------------------------------------------------------------------------

func TestHandleStampChainID_StampsLegacyKey(t *testing.T) {
	donor := newTestDB(t)
	gw := newTestDB(t)
	stat := newStats().forPrefix("w:")

	donorKV := kvItem("w:0xowner:0xwallet", "wallet-record")
	require.NoError(t, handleStampChainID(donor, gw, 8453, "w:", donorKV, stat, false, false))

	assert.Equal(t, 1, stat.copied, "should have copied to gateway")
	assert.Equal(t, 1, stat.stampedChain, "should have counted the chain stamp")

	// Verify the new key was written and the legacy key was NOT (since the
	// donor row is never moved — that's the donor's problem).
	stampedKey := []byte("w:8453:0xowner:0xwallet")
	got, err := gw.GetKey(stampedKey)
	require.NoError(t, err)
	assert.Equal(t, "wallet-record", string(got))

	_, err = gw.GetKey([]byte("w:0xowner:0xwallet"))
	assert.True(t, isKeyNotFoundError(err), "legacy unstamped key should NOT have been written to the gateway")
}

func TestHandleStampChainID_DetectsAlreadyStampedDonor(t *testing.T) {
	donor := newTestDB(t)
	gw := newTestDB(t)
	stat := newStats().forPrefix("w:")

	// Donor key is already in chain-scoped form (e.g. a re-run of the tool,
	// or the donor having run a future migration we don't know about).
	donorKV := kvItem("w:1:0xowner:0xwallet", "wallet-record")
	require.NoError(t, handleStampChainID(donor, gw, 1, "w:", donorKV, stat, false, false))

	// Should pass through, NOT double-stamp into `w:1:1:0xowner...`.
	assert.Equal(t, 1, stat.copied)
	assert.Equal(t, 0, stat.stampedChain, "donor was already stamped — must not double-stamp")

	got, err := gw.GetKey([]byte("w:1:0xowner:0xwallet"))
	require.NoError(t, err)
	assert.Equal(t, "wallet-record", string(got))

	_, err = gw.GetKey([]byte("w:1:1:0xowner:0xwallet"))
	assert.True(t, isKeyNotFoundError(err), "must not have produced the double-stamped key")
}

// -------------------------------------------------------------------------
// handleMaxOnCollision — execution_index_counter:.
// -------------------------------------------------------------------------

func TestHandleMaxOnCollision_DonorLarger(t *testing.T) {
	donor := newTestDB(t)
	gw := newTestDB(t)
	require.NoError(t, gw.Set([]byte("execution_index_counter:taskA"), []byte("5")))

	stat := newStats().forPrefix("execution_index_counter:")
	donorKV := kvItem("execution_index_counter:taskA", "12")
	require.NoError(t, handleMaxOnCollision(donor, gw, 1, "execution_index_counter:", donorKV, stat, false, false))

	assert.Equal(t, 1, stat.collisionResolved, "donor was larger — collision resolved")
	assert.Equal(t, 1, stat.copied, "collision-resolved overwrites count toward Copied so summary totals reconcile (CollRes is a subset of Copied)")

	got, err := gw.GetKey([]byte("execution_index_counter:taskA"))
	require.NoError(t, err)
	assert.Equal(t, "12", string(got), "gateway should now hold donor's larger value")
}

func TestHandleMaxOnCollision_GatewayLarger(t *testing.T) {
	donor := newTestDB(t)
	gw := newTestDB(t)
	require.NoError(t, gw.Set([]byte("execution_index_counter:taskA"), []byte("100")))

	stat := newStats().forPrefix("execution_index_counter:")
	donorKV := kvItem("execution_index_counter:taskA", "12")
	require.NoError(t, handleMaxOnCollision(donor, gw, 1, "execution_index_counter:", donorKV, stat, false, false))

	assert.Equal(t, 1, stat.skippedExists, "gateway was larger — donor's value ignored")
	assert.Equal(t, 0, stat.collisionResolved)

	got, err := gw.GetKey([]byte("execution_index_counter:taskA"))
	require.NoError(t, err)
	assert.Equal(t, "100", string(got), "gateway's larger value preserved")
}

func TestHandleMaxOnCollision_NoExisting(t *testing.T) {
	donor := newTestDB(t)
	gw := newTestDB(t)

	stat := newStats().forPrefix("execution_index_counter:")
	donorKV := kvItem("execution_index_counter:taskA", "7")
	require.NoError(t, handleMaxOnCollision(donor, gw, 1, "execution_index_counter:", donorKV, stat, false, false))

	assert.Equal(t, 1, stat.copied)

	got, err := gw.GetKey([]byte("execution_index_counter:taskA"))
	require.NoError(t, err)
	assert.Equal(t, "7", string(got))
}

// -------------------------------------------------------------------------
// handleDrop — the no-op path.
// -------------------------------------------------------------------------

func TestHandleDrop_NeverWrites(t *testing.T) {
	donor := newTestDB(t)
	gw := newTestDB(t)

	for _, key := range []string{"ct:cw:0xeoa", "pending:taskA:exec1", "trigger:taskA:exec1", "migration:foo"} {
		stat := &prefixStats{}
		require.NoError(t, handleDrop(donor, gw, 1, "", kvItem(key, "value"), stat, false, false))
		assert.Equal(t, 1, stat.dropped, "key %q should have been counted as dropped", key)

		_, err := gw.GetKey([]byte(key))
		assert.True(t, isKeyNotFoundError(err), "key %q must NOT have been written", key)
	}
}

// -------------------------------------------------------------------------
// dispatch — the routing layer.
// -------------------------------------------------------------------------

func TestDispatch_RoutesEachPrefixToItsHandler(t *testing.T) {
	donor := newTestDB(t)
	gw := newTestDB(t)
	st := newStats()

	cases := []struct {
		prefix string
		key    string
		value  string
	}{
		// t:, history: now decode the body as proto JSON, so the test
		// values need to be valid proto JSON for their respective
		// messages (avsproto.Task / avsproto.Execution). The body
		// cleaner re-marshals, so the exact contents don't matter
		// beyond being decodable.
		{"t:", "t:1:a:taskA", `{"id":"taskA"}`},
		{"u:", "u:1:0xowner:0xwallet:taskA", "taskA"},
		{"history:", "history:1:taskA:exec1", `{"id":"exec1"}`},
		{"w:", "w:0xowner:0xwallet", "wallet"},
		{"wsalt:", "wsalt:0xowner:0xfactory:0", "0xwallet"},
		{"fl:", "fl:0xowner", "balance-blob"},
		// fr: now decodes the body as a FeeRecord struct (stdlib json)
		// — needs valid JSON. Body cleaner sets ChainID from --donor-chain-id.
		{"fr:", "fr:0xowner:exec1", `{"execution_id":"exec1","owner":"0xowner"}`},
		{"secret:", "secret:_:0xowner:_:apikey", "supersecret"},
		{"execution_index_counter:", "execution_index_counter:taskA", "1"},
		{"ct:cw:", "ct:cw:0xeoa", "5"},
		{"pending:", "pending:taskA:exec1", ""},
		{"trigger:", "trigger:taskA:exec1", "status"},
		{"migration:", "migration:foo", "1"},
		// Per-aggregator operational state added when tonight's
		// rehearsal surfaced 10M+ q: keys on the base donor — both
		// must route to drop, not stamp.
		{"operator:", "operator:0xoperator1", "registered"},
		{"q:", "q:t:c:1:taskA", "queue-entry"},
		// t:seq is a sequence-counter key that LOOKS like a t: row.
		// Dispatch order is load-bearing: t:seq must match the t:seq
		// drop handler BEFORE t: tries to stamp it. Without the right
		// ordering in prefixHandlers, this key would get rewritten to
		// `t:1:seq` and corrupt the Badger sequence semantics.
		{"t:seq", "t:seq", "42"},
		// q:seq: is the queue's sequence counter. Same ordering
		// concern as t:seq — must match q:seq: drop before q:.
		{"q:seq:", "q:seq:taskA", "7"},
	}
	for _, c := range cases {
		stat := st.forPrefix(c.prefix)
		stat.scanned++
		require.NoError(t, dispatch(donor, gw, 1, kvItem(c.key, c.value), stat, false, false), "dispatch(%q)", c.key)
	}

	assert.Equal(t, 1, st.perPrefix["t:"].copied)
	assert.Equal(t, 1, st.perPrefix["u:"].copied)
	assert.Equal(t, 1, st.perPrefix["history:"].copied)
	assert.Equal(t, 1, st.perPrefix["w:"].copied)
	assert.Equal(t, 1, st.perPrefix["w:"].stampedChain)
	assert.Equal(t, 1, st.perPrefix["secret:"].copied)
	assert.Equal(t, 1, st.perPrefix["ct:cw:"].dropped)
	assert.Equal(t, 1, st.perPrefix["pending:"].dropped)
	assert.Equal(t, 1, st.perPrefix["trigger:"].dropped)
	assert.Equal(t, 1, st.perPrefix["migration:"].dropped)
	assert.Equal(t, 1, st.perPrefix["operator:"].dropped)
	assert.Equal(t, 1, st.perPrefix["q:"].dropped)
	// t:seq must route to its own drop bucket, NOT t:'s stamp bucket.
	// Confirms both that t:seq is in the dispatch table BEFORE t:
	// AND that the t:seq handler is drop, not stamp.
	assert.Equal(t, 1, st.perPrefix["t:seq"].dropped)
	assert.Equal(t, 0, st.perPrefix["t:seq"].copied, "t:seq must never be copied — it's a Badger sequence counter")
	assert.Equal(t, 0, st.perPrefix["t:seq"].stampedChain, "t:seq must never be stamped")
	assert.Equal(t, 1, st.perPrefix["q:seq:"].dropped)
	assert.Equal(t, 0, st.perPrefix["q:seq:"].copied)
}

// -------------------------------------------------------------------------
// parseChainIDFromKey — round-trip.
// -------------------------------------------------------------------------

func TestParseChainIDFromKey(t *testing.T) {
	id, err := parseChainIDFromKey([]byte("t:1:a:taskA"))
	require.NoError(t, err)
	assert.Equal(t, int64(1), id)

	id, err = parseChainIDFromKey([]byte("history:8453:taskA:exec1"))
	require.NoError(t, err)
	assert.Equal(t, int64(8453), id)

	_, err = parseChainIDFromKey([]byte("t:a:taskA"))
	assert.Error(t, err, "non-numeric second segment is legacy form")

	_, err = parseChainIDFromKey([]byte("t:seq"))
	assert.Error(t, err, "single-segment form is legacy")
}

func TestSupportedChainList_IsDeterministic(t *testing.T) {
	first := supportedChainList()
	for i := 0; i < 5; i++ {
		assert.Equal(t, first, supportedChainList())
	}
	// Sanity: the chains we actually support appear.
	for _, want := range []string{"ethereum", "base", "sepolia", "base-sepolia"} {
		assert.Contains(t, first, want)
	}
}
