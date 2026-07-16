package executor

import (
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
