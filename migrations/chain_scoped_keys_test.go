package migrations

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestMigrateKeysToChainScoped(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()

	// Seed legacy-format keys across all three prefix families.
	const chainID = int64(11155111)
	seed := map[string][]byte{
		// t: tasks
		"t:a:task-enabled-1":   []byte(`{"id":"task-enabled-1"}`),
		"t:a:task-enabled-2":   []byte(`{"id":"task-enabled-2"}`),
		"t:c:task-completed-1": []byte(`{"id":"task-completed-1"}`),
		"t:i:task-disabled-1":  []byte(`{"id":"task-disabled-1"}`),
		// u: user-tasks
		"u:0xowner1:0xwalletA:task-enabled-1": []byte("ref"),
		"u:0xowner2:0xwalletB:task-completed-1": []byte("ref"),
		// history: executions
		"history:task-enabled-1:exec-1":   []byte(`{"id":"exec-1"}`),
		"history:task-enabled-1:exec-2":   []byte(`{"id":"exec-2"}`),
		"history:task-completed-1:exec-1": []byte(`{"id":"exec-1"}`),
		// chain-agnostic keys that must NOT be touched
		"secret:_:0xowner1:_:mysecret": []byte("token"),
		"migration:something":          []byte("done"),
	}
	if err := db.BatchWrite(toBatch(seed)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Run migration.
	n, err := MigrateKeysToChainScoped(db, chainID)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	// We seeded 4 t:, 2 u:, 3 history: keys = 9 expected moves.
	if want := 9; n != want {
		t.Fatalf("moved count: got %d, want %d", n, want)
	}

	// Assert each legacy key was deleted and the chain-scoped equivalent exists.
	expectMoved := map[string]string{
		"t:a:task-enabled-1":                    "t:11155111:a:task-enabled-1",
		"t:a:task-enabled-2":                    "t:11155111:a:task-enabled-2",
		"t:c:task-completed-1":                  "t:11155111:c:task-completed-1",
		"t:i:task-disabled-1":                   "t:11155111:i:task-disabled-1",
		"u:0xowner1:0xwalletA:task-enabled-1":   "u:11155111:0xowner1:0xwalletA:task-enabled-1",
		"u:0xowner2:0xwalletB:task-completed-1": "u:11155111:0xowner2:0xwalletB:task-completed-1",
		"history:task-enabled-1:exec-1":         "history:11155111:task-enabled-1:exec-1",
		"history:task-enabled-1:exec-2":         "history:11155111:task-enabled-1:exec-2",
		"history:task-completed-1:exec-1":       "history:11155111:task-completed-1:exec-1",
	}
	for oldKey, newKey := range expectMoved {
		oldExists, _ := db.Exist([]byte(oldKey))
		if oldExists {
			t.Errorf("legacy key still present: %s", oldKey)
		}
		val, err := db.GetKey([]byte(newKey))
		if err != nil {
			t.Errorf("chain-scoped key missing %s: %v", newKey, err)
			continue
		}
		if string(val) != string(seed[oldKey]) {
			t.Errorf("value mismatch at %s", newKey)
		}
	}

	// Chain-agnostic keys must be untouched.
	for _, k := range []string{"secret:_:0xowner1:_:mysecret", "migration:something"} {
		exists, _ := db.Exist([]byte(k))
		if !exists {
			t.Errorf("chain-agnostic key was wrongly removed: %s", k)
		}
	}
}

func TestMigrateKeysToChainScoped_Idempotent(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()

	const chainID = int64(8453)
	seed := map[string][]byte{
		"t:a:task-1":                          []byte("v"),
		"history:task-1:exec-1":               []byte("v"),
		"u:0xowner:0xwallet:task-1":           []byte("ref"),
	}
	if err := db.BatchWrite(toBatch(seed)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First run: rewrites everything.
	n1, err := MigrateKeysToChainScoped(db, chainID)
	if err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if want := 3; n1 != want {
		t.Fatalf("first run moved: got %d, want %d", n1, want)
	}

	// Second run on an already-migrated DB: must be a no-op (0 moves).
	n2, err := MigrateKeysToChainScoped(db, chainID)
	if err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second run was not idempotent: moved %d keys", n2)
	}

	// And the chain-scoped keys are still there.
	for _, k := range []string{
		"t:8453:a:task-1",
		"history:8453:task-1:exec-1",
		"u:8453:0xowner:0xwallet:task-1",
	} {
		exists, _ := db.Exist([]byte(k))
		if !exists {
			t.Errorf("chain-scoped key missing after idempotent run: %s", k)
		}
	}
}

func TestMigrateKeysToChainScoped_InvalidChainID(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()

	if _, err := MigrateKeysToChainScoped(db, 0); err == nil {
		t.Errorf("want error for chain_id=0, got nil")
	}
	if _, err := MigrateKeysToChainScoped(db, -1); err == nil {
		t.Errorf("want error for negative chain_id, got nil")
	}
}

// toBatch turns a map[string][]byte into the storage.BatchWrite shape (no-op
// here since the types match, but kept for readability if the API changes).
func toBatch(m map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Ensure storage.Storage interface compiles against the test binding.
var _ storage.Storage = (storage.Storage)(nil)
