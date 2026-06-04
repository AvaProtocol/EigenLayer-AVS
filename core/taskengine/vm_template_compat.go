package taskengine

import (
	"strings"
	"unicode"
)

// expandCaseAliases walks an input variable tree and, for every map entry,
// adds a sibling under the alternate spelling (snake_case <-> camelCase).
// Existing keys win — if both forms are already set, neither is overwritten.
//
// The REST migration accepts only camelCase on the wire, but stored
// workflows persisted under the gRPC era reference template variables in
// snake_case (e.g. `{{settings.chain_id}}`). Routing both spellings through
// the resolver keeps those workflows running without a per-record migration.
//
// The mutation is in-place on nested maps when possible (cheap) and
// returns a new map for the top level (so callers see the alias entries
// without having to read them back from a side effect).
func expandCaseAliases(in map[string]any) map[string]any {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]any, len(in)*2)
	for k, v := range in {
		out[k] = expandCaseAliasesValue(v)
	}
	// Second pass to add aliases. Split into two passes so the first
	// pass's recursion doesn't see (and re-alias) the aliases we add.
	for k, v := range out {
		alias := alternateSpelling(k)
		if alias != "" && alias != k {
			if _, exists := out[alias]; !exists {
				out[alias] = v
			}
		}
	}
	return out
}

// expandCaseAliasesValue is the recursive form — it processes a single
// value rather than a top-level map. Maps become alias-expanded maps;
// slices recurse into each element; scalars pass through.
// (`any` and `interface{}` are the same type in Go so the type switch
// only needs one case for each container shape.)
func expandCaseAliasesValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return expandCaseAliases(x)
	case []any:
		out := make([]any, len(x))
		for i, elem := range x {
			out[i] = expandCaseAliasesValue(elem)
		}
		return out
	default:
		return v
	}
}

// alternateSpelling returns the snake_case form of a camelCase identifier
// (or vice versa). If the input is in neither form (all lowercase, mixed
// with digits and no underscores or capitals) it returns "" so callers
// can skip aliasing.
//
// The function is deliberately small and deterministic — only ASCII
// letters and digits are considered, leading/trailing underscores are
// preserved, and double underscores compact to one. Anything outside
// `[A-Za-z0-9_]` returns "" because we can't be sure the rewrite is safe.
func alternateSpelling(name string) string {
	if name == "" {
		return ""
	}
	for _, r := range name {
		if r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return ""
	}
	if strings.ContainsRune(name, '_') {
		return snakeToCamel(name)
	}
	if hasUpperLetter(name) {
		return camelToSnake(name)
	}
	return ""
}

func hasUpperLetter(s string) bool {
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

// snakeToCamel converts `foo_bar_baz` -> `fooBarBaz`. Leading underscores
// survive (private convention), runs of underscores collapse to one
// boundary, trailing underscores are dropped.
func snakeToCamel(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	upperNext := false
	leading := true
	for _, r := range s {
		if r == '_' {
			if leading {
				b.WriteRune(r)
				continue
			}
			upperNext = true
			continue
		}
		leading = false
		if upperNext {
			if r >= 'a' && r <= 'z' {
				r = r - 'a' + 'A'
			}
			upperNext = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// camelToSnake converts `fooBarBaz` -> `foo_bar_baz`. Consecutive
// uppercase letters are treated as an acronym boundary at the last
// uppercase (`HTTPRequest` -> `http_request`).
func camelToSnake(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	runes := []rune(s)
	for i, r := range runes {
		if r >= 'A' && r <= 'Z' {
			isStart := i == 0
			prev := rune(0)
			if i > 0 {
				prev = runes[i-1]
			}
			next := rune(0)
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			// Insert underscore at the boundary between lowercase/digit
			// and uppercase, OR between two uppercases when the next
			// char is lowercase (acronym end: HTTPRequest -> http_request).
			needsUnderscore := !isStart && (isLowerOrDigit(prev) || (prev >= 'A' && prev <= 'Z' && next >= 'a' && next <= 'z'))
			if needsUnderscore {
				b.WriteRune('_')
			}
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isLowerOrDigit(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}
