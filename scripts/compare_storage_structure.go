// compare_storage_structure.go detects BadgerDB storage-compatibility risks
// between an old git ref and the current working tree.
//
// It answers one question before a staging->main release: "will the new code
// read the data the old code wrote?" Two things break that:
//
//  1. Storage KEY layout changes. Every persisted record lives under a string
//     key built from a format literal like "w:%d:%s" or "history:%d:%s:%s".
//     If a key template is removed or its shape changes, data written under the
//     old key is orphaned (unreadable) until migrated.
//
//  2. Persisted VALUE shape changes. Records in model/ are serialized into the
//     value bytes. Removing a struct field or changing its type can break
//     deserialization of already-stored records.
//
// Rather than hardcode a key/file list (the reason the previous version rotted
// when #538 made storage multi-chain), this walks the Go AST of EVERY tracked
// .go file in both refs and discovers key literals and model structs
// dynamically.
//
// Usage:
//
//	go run scripts/compare_storage_structure.go <old_ref>
//	go run scripts/compare_storage_structure.go origin/main
//
// Exit code: 0 when changes are additive or none; 1 when a breaking change
// (removed/changed key template, removed field, or changed field type) is
// detected, so it can gate CI.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// keyLikePrefix matches a string literal that looks like a storage key:
// a short lowercase namespace token followed by a colon (e.g. "u:", "history:",
// "wsalt:"). The follow-up filter rejects anything containing characters that
// never appear in a key template (whitespace, URL/path punctuation, the
// "<chainID>" doc placeholders that show up in comments-as-strings).
var keyLikePrefix = regexp.MustCompile(`^[a-z][a-zA-Z0-9]{0,12}:`)

func isKeyLike(s string) bool {
	if !keyLikePrefix.MatchString(s) {
		return false
	}
	if strings.ContainsAny(s, " \t\n/.<>(){}[]?#@") {
		return false
	}
	// Require structure beyond the bare prefix: a format verb or a second
	// colon. Excludes accidental matches like a stray "todo:".
	return strings.Contains(s, "%") || strings.Count(s, ":") >= 2 || strings.HasSuffix(s, ":")
}

func keyPrefix(literal string) string {
	if before, _, found := strings.Cut(literal, ":"); found {
		return before
	}
	return literal
}

// fieldInfo captures the persisted shape of a model struct field.
type fieldInfo struct {
	typ       string
	omitempty bool
}

// refSnapshot is everything we extracted from one git ref.
type refSnapshot struct {
	// keyLiterals maps a key template literal -> a sample "file:func" site.
	keyLiterals map[string]string
	// structs maps "package.Struct" -> field name -> fieldInfo (model pkg only).
	structs map[string]map[string]fieldInfo
}

func main() {
	if len(os.Args) != 2 {
		fmt.Println("Usage: go run scripts/compare_storage_structure.go <old_ref>")
		fmt.Println("Example: go run scripts/compare_storage_structure.go origin/main")
		os.Exit(2)
	}
	oldRef := os.Args[1]

	if !refExists(oldRef) {
		fmt.Printf("Error: ref %q not found. Try: git fetch origin\n", oldRef)
		os.Exit(2)
	}

	fmt.Printf("Comparing storage structure: %s -> working tree\n\n", oldRef)

	oldSnap := snapshotRef(oldRef)
	newSnap := snapshotWorkingTree()

	breaking := false
	breaking = reportKeyChanges(oldSnap, newSnap) || breaking
	breaking = reportStructChanges(oldSnap, newSnap) || breaking

	fmt.Println()
	if breaking {
		fmt.Println("⚠️  Breaking storage changes detected — a data migration is required before merging to main.")
		fmt.Println("   Generate one with: go run scripts/migration/create_migration.go " + oldRef)
		os.Exit(1)
	}
	fmt.Println("✅ No breaking storage changes detected (additive-only or none).")
}

// ---- extraction ----------------------------------------------------------

func snapshotRef(ref string) refSnapshot {
	snap := newSnapshot()
	for _, path := range goFilesInRef(ref) {
		src, err := gitShow(ref, path)
		if err != nil {
			continue
		}
		extract(path, src, snap)
	}
	return snap
}

func snapshotWorkingTree() refSnapshot {
	snap := newSnapshot()
	for _, path := range goFilesInWorkingTree() {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		extract(path, string(src), snap)
	}
	return snap
}

func newSnapshot() refSnapshot {
	return refSnapshot{
		keyLiterals: map[string]string{},
		structs:     map[string]map[string]fieldInfo{},
	}
}

// extract parses one Go source file and records its key literals and (for the
// model package) its struct field shapes. Parse failures are skipped quietly —
// a single unparseable file must not abort the whole comparison.
func extract(path, src string, snap refSnapshot) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return
	}

	isModel := isModelFile(path)

	// Track the enclosing function name so a key literal can be attributed.
	var currentFunc string
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			currentFunc = node.Name.Name
		case *ast.BasicLit:
			if node.Kind == token.STRING {
				if s, err := strconv.Unquote(node.Value); err == nil && isKeyLike(s) {
					if _, seen := snap.keyLiterals[s]; !seen {
						site := path
						if currentFunc != "" {
							site = path + ":" + currentFunc
						}
						snap.keyLiterals[s] = site
					}
				}
			}
		}
		return true
	})

	if !isModel {
		return
	}
	pkg := file.Name.Name
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			name := pkg + "." + ts.Name.Name
			fields := map[string]fieldInfo{}
			for _, f := range st.Fields.List {
				typeStr := exprString(fset, f.Type)
				omit := f.Tag != nil && strings.Contains(f.Tag.Value, "omitempty")
				for _, fn := range f.Names {
					if fn.IsExported() {
						fields[fn.Name] = fieldInfo{typ: typeStr, omitempty: omit}
					}
				}
			}
			if len(fields) > 0 {
				snap.structs[name] = fields
			}
		}
	}
}

// ---- reporting -----------------------------------------------------------

func reportKeyChanges(old, new refSnapshot) bool {
	var removed, added []string
	for lit := range old.keyLiterals {
		if _, ok := new.keyLiterals[lit]; !ok {
			removed = append(removed, lit)
		}
	}
	for lit := range new.keyLiterals {
		if _, ok := old.keyLiterals[lit]; !ok {
			added = append(added, lit)
		}
	}
	sort.Strings(removed)
	sort.Strings(added)

	fmt.Println("== Storage key templates ==")
	if len(removed) == 0 && len(added) == 0 {
		fmt.Println("  ✅ unchanged")
		return false
	}

	// A prefix that has BOTH a removed and an added literal is a reshaped key
	// (same namespace, new layout) — the clearest migration signal.
	removedByPrefix := map[string]bool{}
	for _, lit := range removed {
		removedByPrefix[keyPrefix(lit)] = true
	}
	addedByPrefix := map[string]bool{}
	for _, lit := range added {
		addedByPrefix[keyPrefix(lit)] = true
	}

	breaking := false
	for _, lit := range removed {
		p := keyPrefix(lit)
		if addedByPrefix[p] {
			fmt.Printf("  ⚠️  reshaped  %q (namespace %q) — old records orphaned; migrate\n", lit, p)
		} else {
			fmt.Printf("  ⚠️  removed   %q (was %s) — old records orphaned; migrate or backfill\n", lit, old.keyLiterals[lit])
		}
		breaking = true
	}
	for _, lit := range added {
		p := keyPrefix(lit)
		if removedByPrefix[p] {
			continue // already reported as "reshaped" above
		}
		fmt.Printf("  ✅ added     %q (%s) — new namespace, additive\n", lit, new.keyLiterals[lit])
	}
	return breaking
}

func reportStructChanges(old, new refSnapshot) bool {
	fmt.Println("\n== Persisted model structs (model/) ==")
	names := map[string]bool{}
	for n := range old.structs {
		names[n] = true
	}
	for n := range new.structs {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	breaking := false
	clean := true
	for _, name := range sorted {
		oldFields, inOld := old.structs[name]
		newFields, inNew := new.structs[name]
		if inOld && !inNew {
			fmt.Printf("  ⚠️  struct %s removed — verify nothing persisted under it\n", name)
			clean = false
			breaking = true
			continue
		}
		if !inOld && inNew {
			continue // new struct, additive
		}
		var fieldNames []string
		for f := range oldFields {
			fieldNames = append(fieldNames, f)
		}
		for f := range newFields {
			if _, ok := oldFields[f]; !ok {
				fieldNames = append(fieldNames, f)
			}
		}
		sort.Strings(fieldNames)
		for _, f := range fieldNames {
			ov, inOldF := oldFields[f]
			nv, inNewF := newFields[f]
			switch {
			case inOldF && !inNewF:
				fmt.Printf("  ⚠️  %s.%s removed (was %s) — breaks deserialization of stored records\n", name, f, ov.typ)
				clean = false
				breaking = true
			case !inOldF && inNewF:
				note := "backward-compatible"
				if !nv.omitempty {
					note = "no omitempty — confirm zero value is acceptable for old records"
				}
				fmt.Printf("  ✅ %s.%s added (%s) — %s\n", name, f, nv.typ, note)
				clean = false
			case ov.typ != nv.typ:
				fmt.Printf("  ⚠️  %s.%s type changed %s -> %s — breaks deserialization\n", name, f, ov.typ, nv.typ)
				clean = false
				breaking = true
			}
		}
	}
	if clean {
		fmt.Println("  ✅ unchanged")
	}
	return breaking
}

// ---- git / fs helpers ----------------------------------------------------

func refExists(ref string) bool {
	return exec.Command("git", "rev-parse", "--verify", "--quiet", ref+"^{commit}").Run() == nil
}

func gitShow(ref, path string) (string, error) {
	out, err := exec.Command("git", "show", ref+":"+path).Output()
	return string(out), err
}

func goFilesInRef(ref string) []string {
	out, err := exec.Command("git", "ls-tree", "-r", "--name-only", ref).Output()
	if err != nil {
		return nil
	}
	return filterGoFiles(strings.Split(strings.TrimSpace(string(out)), "\n"))
}

func goFilesInWorkingTree() []string {
	out, err := exec.Command("git", "ls-files").Output()
	if err != nil {
		return nil
	}
	return filterGoFiles(strings.Split(strings.TrimSpace(string(out)), "\n"))
}

// filterGoFiles keeps hand-written .go sources and drops tests, generated
// protobuf, the scripts/ tooling itself, and vendored code — none of which
// define the persisted storage contract.
func filterGoFiles(paths []string) []string {
	var out []string
	for _, p := range paths {
		if !strings.HasSuffix(p, ".go") {
			continue
		}
		if strings.HasSuffix(p, "_test.go") ||
			strings.HasSuffix(p, ".pb.go") ||
			strings.HasPrefix(p, "scripts/") ||
			strings.HasPrefix(p, "vendor/") {
			continue
		}
		out = append(out, p)
	}
	return out
}

func isModelFile(path string) bool {
	return strings.HasPrefix(path, "model/")
}

func exprString(fset *token.FileSet, e ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, e); err != nil {
		return "?"
	}
	return buf.String()
}
