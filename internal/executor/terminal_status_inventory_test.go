package executor

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// executionStatusVocabulary is every executions.status value the codebase
// writes or reads (dispatcher.go's terminalExecutionStatuses plus the
// non-terminal values a hand-rolled "is it still running" set would use).
var executionStatusVocabulary = map[string]bool{
	"queued": true, "pending": true, "running": true,
	"completed": true, "failed": true, "cancelled": true, "declined": true,
	"no_op": true, "rate_limited": true, "skipped": true, "stalled": true, "infra": true,
	"superseded": true,
}

// terminalStatusInventoryAllowFiles lists the source files permitted to
// define a map/set literal keyed on execution-status strings — i.e. the
// single owner of "what counts as terminal," dispatcher.go's
// terminalExecutionStatuses.
var terminalStatusInventoryAllowFiles = map[string]bool{
	"dispatcher.go": true,
}

// TestTerminalStatusInventory_NoStrayStatusSets is the GH-4381 guard for the
// mem-154 pitfall class ("no_op invisible to a dispatch guard's terminal
// check") recurring a 4th time. Prior instances each grew their own
// hand-rolled map of execution-status strings instead of consulting
// dispatcher.go's terminalExecutionStatuses/isTerminalExecutionStatus — most
// recently epic.go's now-removed childExecutionNonTerminalStatuses, which
// didn't know how a fresh "queued" duplicate row could hide an older
// terminal "no_op" row (GH-4381) and silently drifted from the shared
// definition.
//
// This walks every non-test .go file in this package looking for composite
// map literals whose keys are execution-status string constants — if two or
// more distinct status values keys show up in the same literal outside
// dispatcher.go, that's a second (or third, or fourth) copy of the terminal
// classification being grown, and this test fails naming the file:line so
// CI catches it instead of a sandbox canary.
func TestTerminalStatusInventory_NoStrayStatusSets(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if terminalStatusInventoryAllowFiles[name] {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", name, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if _, isMap := lit.Type.(*ast.MapType); !isMap {
				return true
			}

			seen := map[string]bool{}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				keyLit, ok := kv.Key.(*ast.BasicLit)
				if !ok || keyLit.Kind != token.STRING {
					continue
				}
				key, err := strconv.Unquote(keyLit.Value)
				if err != nil {
					continue
				}
				if executionStatusVocabulary[key] {
					seen[key] = true
				}
			}

			if len(seen) >= 2 {
				keys := make([]string, 0, len(seen))
				for k := range seen {
					keys = append(keys, k)
				}
				pos := fset.Position(lit.Pos())
				t.Errorf(
					"%s:%d: map literal keys %v look like a second execution-status terminal/non-terminal classification outside dispatcher.go's terminalExecutionStatuses — consult isTerminalExecutionStatus instead of growing a new copy (mem-154 pitfall class)",
					filepath.Base(pos.Filename), pos.Line, keys,
				)
			}
			return true
		})
	}
}

// terminalExecutionStatusMirror names an unexported package-level
// `var name = []string{...}` in another package that intentionally
// duplicates this package's terminalExecutionStatuses (dispatcher.go) rather
// than importing it — the cross-package-coupling-avoidance pattern each
// mirror's own doc comment explains (see pilotTerminalExecutionStatuses in
// internal/adapters/github/cleanup.go and terminalExecutionStatuses in
// internal/memory/store.go).
type terminalExecutionStatusMirror struct {
	path    string // relative to this package's directory
	varName string
}

var terminalExecutionStatusMirrors = []terminalExecutionStatusMirror{
	{path: "../memory/store.go", varName: "terminalExecutionStatuses"},
	{path: "../adapters/github/cleanup.go", varName: "pilotTerminalExecutionStatuses"},
}

// TestTerminalExecutionStatusSets_Match is the GH-5408 regression guard: PR
// #5402 added "needs_human" to this package's terminalExecutionStatuses
// (dispatcher.go) but not to either of its two intentional duplicates —
// memory.terminalExecutionStatuses (internal/memory/store.go) or
// github.pilotTerminalExecutionStatuses
// (internal/adapters/github/cleanup.go). The result: a needs_human row's CAS
// guard (store.go's UpdateExecutionStatusIfNotTerminal) silently stopped
// protecting itself against a later terminal write clobbering it, and the
// github cleaner's pilot-in-progress staleness gate (GH-5299) stopped
// recognizing a needs_human-parked issue as terminal.
// TestTerminalStatusInventory_NoStrayStatusSets above only scans this
// package's own directory for stray copies, so it never caught either
// drift — this test instead parses the other two files' literal string
// slices directly (without importing them — they're unexported, and that
// cross-package coupling is exactly what the duplication avoids) and diffs
// them against this package's own terminalExecutionStatuses map.
func TestTerminalExecutionStatusSets_Match(t *testing.T) {
	want := make(map[string]bool, len(terminalExecutionStatuses))
	for k := range terminalExecutionStatuses {
		want[k] = true
	}

	for _, mirror := range terminalExecutionStatusMirrors {
		got, err := parseStringSliceVar(mirror.path, mirror.varName)
		if err != nil {
			t.Fatalf("%s: %v", mirror.path, err)
		}

		for status := range want {
			if !got[status] {
				t.Errorf("%s's %s is missing %q, which dispatcher.go's terminalExecutionStatuses treats as terminal (GH-5408 drift class)", mirror.path, mirror.varName, status)
			}
		}
		for status := range got {
			if !want[status] {
				t.Errorf("%s's %s has extra status %q that dispatcher.go's terminalExecutionStatuses does not treat as terminal (GH-5408 drift class)", mirror.path, mirror.varName, status)
			}
		}
	}
}

// parseStringSliceVar extracts the string-literal elements of a top-level
// `var name = []string{"a", "b", ...}` declaration from a Go source file,
// via AST rather than an import — the whole point is inspecting an
// unexported var in a package this one deliberately does not import.
func parseStringSliceVar(path, name string) (map[string]bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("ParseFile: %w", err)
	}

	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.VAR {
			continue
		}
		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || len(valueSpec.Names) != 1 || valueSpec.Names[0].Name != name {
				continue
			}
			if len(valueSpec.Values) != 1 {
				continue
			}
			lit, ok := valueSpec.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			result := map[string]bool{}
			for _, elt := range lit.Elts {
				basicLit, ok := elt.(*ast.BasicLit)
				if !ok || basicLit.Kind != token.STRING {
					continue
				}
				val, unquoteErr := strconv.Unquote(basicLit.Value)
				if unquoteErr != nil {
					continue
				}
				result[val] = true
			}
			return result, nil
		}
	}
	return nil, fmt.Errorf("var %s not found in %s", name, path)
}
