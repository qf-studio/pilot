package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// executorCalls counts the calls executor.NAME(...) in a source file of this
// package.
func executorCalls(t *testing.T, file string) map[string]int {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	calls := map[string]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "executor" {
				calls[sel.Sel.Name]++
			}
		}
		return true
	})
	return calls
}

// TestOneShotCommandsOrphanedWorktreeSweepKeepsAgentWorkspaces is item 2c of
// TASK-651 (issue 6387): the one-shot commands (commands.go) sweep the
// worktree orphans without the agent workspaces; only the daemon (main.go, at
// start) sweeps those.
func TestOneShotCommandsOrphanedWorktreeSweepKeepsAgentWorkspaces(t *testing.T) {
	tests := []struct {
		file string
		want map[string]int // name -> exact count; 0 forbids the call
	}{
		{"commands.go", map[string]int{
			"CleanupOrphanedWorktrees":                    0,
			"PruneOrphanedWorktreeForTask":                0,
			"CleanupOrphanedWorktreesKeepAgentWorkspaces": 2,
		}},
		{"main.go", map[string]int{
			"CleanupOrphanedWorktrees":                    2,
			"CleanupOrphanedWorktreesKeepAgentWorkspaces": 0,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			calls := executorCalls(t, tc.file)
			for name, want := range tc.want {
				if got := calls[name]; got != want {
					t.Errorf("%s calls executor.%s %d times, want %d", tc.file, name, got, want)
				}
			}
		})
	}
}
