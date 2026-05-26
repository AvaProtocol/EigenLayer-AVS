package taskengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAlternateSpelling(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// snake -> camel
		{"chain_id", "chainId"},
		{"smart_wallet_address", "smartWalletAddress"},
		{"max_events_per_block", "maxEventsPerBlock"},
		{"single", ""},       // already simple, no alias
		{"alreadylower", ""}, // ditto
		{"_private", "_private"},
		{"trailing_", "trailing"},
		// camel -> snake
		{"chainId", "chain_id"},
		{"smartWalletAddress", "smart_wallet_address"},
		{"HTTPRequest", "http_request"},
		{"X-Trigger-Version", ""}, // hyphens are not alphanumeric — no alias attempt
		{"", ""},
	}
	for _, tc := range cases {
		got := alternateSpelling(tc.in)
		assert.Equal(t, tc.want, got, "alternateSpelling(%q)", tc.in)
	}
}

func TestExpandCaseAliases_TopLevel(t *testing.T) {
	in := map[string]any{
		"chain_id":    int64(11155111),
		"smartWallet": "0xabc",
		"already_set": "snake",
		"alreadySet":  "camel", // both forms supplied — neither gets overwritten
	}
	out := expandCaseAliases(in)

	// Snake -> camel alias is added.
	assert.Equal(t, int64(11155111), out["chain_id"])
	assert.Equal(t, int64(11155111), out["chainId"])

	// Camel -> snake alias is added.
	assert.Equal(t, "0xabc", out["smartWallet"])
	assert.Equal(t, "0xabc", out["smart_wallet"])

	// When both forms exist on input, the original values stay put.
	assert.Equal(t, "snake", out["already_set"])
	assert.Equal(t, "camel", out["alreadySet"])
}

func TestExpandCaseAliases_NestedObjects(t *testing.T) {
	in := map[string]any{
		"settings": map[string]any{
			"chain_id": int64(11155111),
			"runner":   "0xabc",
			"address_list": []any{
				map[string]any{"token_address": "0xUSDC"},
			},
		},
	}
	out := expandCaseAliases(in)

	settings, ok := out["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings is not a map: %T", out["settings"])
	}
	assert.Equal(t, int64(11155111), settings["chain_id"])
	assert.Equal(t, int64(11155111), settings["chainId"], "nested snake key should have camel alias")

	// Lists recurse — map elements inside slices get aliased too.
	list, ok := settings["address_list"].([]any)
	if !ok {
		t.Fatalf("address_list is not a slice: %T", settings["address_list"])
	}
	elem, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("address_list[0] is not a map: %T", list[0])
	}
	assert.Equal(t, "0xUSDC", elem["token_address"])
	assert.Equal(t, "0xUSDC", elem["tokenAddress"])

	// Camel alias is also added at the parent (settings -> address_list / addressList).
	assert.Contains(t, settings, "addressList")
}

func TestExpandCaseAliases_PreservesNonMapValues(t *testing.T) {
	in := map[string]any{
		"count":    int64(42),
		"name":     "hello",
		"enabled":  true,
		"missing":  nil,
		"weird":    struct{ X int }{X: 1}, // pass-through
		"chain_id": 11155111,
	}
	out := expandCaseAliases(in)
	assert.Equal(t, int64(42), out["count"])
	assert.Equal(t, "hello", out["name"])
	assert.Equal(t, true, out["enabled"])
	assert.Nil(t, out["missing"])
	assert.NotNil(t, out["weird"]) // unchanged
	assert.Equal(t, 11155111, out["chainId"])
}

func TestExpandCaseAliases_EmptyAndNil(t *testing.T) {
	assert.Equal(t, map[string]any(nil), expandCaseAliases(nil))
	assert.Equal(t, map[string]any{}, expandCaseAliases(map[string]any{}))
}
