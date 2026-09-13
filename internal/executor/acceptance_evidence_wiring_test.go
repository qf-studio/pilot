package executor

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// GH-5438: wiring pin for the PR-body evidence hook. GH-5437 (#5439) added
// the safety controls to the acceptance-evidence gate (allowlist, sanitised
// environment, redaction, worktree confinement, timeout), but nothing pinned
// that every PR-body assembly site in runner.go actually calls
// appendAcceptanceEvidence before the body reaches CreatePR/prCreator — a
// future edit could drop one of the `prBody = r.appendAcceptanceEvidence(...)`
// reassignments (runner.go currently has five: the epic-PR path, the
// timeout-salvage path, and the three SDK/non-GitHub/gh-CLI branches of the
// direct path) and go build/go vet/go test would all stay green; the PR
// would just silently ship without the Evidence/Not-verified sections.
//
// TestPRBodyAssemblySitesCallAcceptanceEvidenceHook statically walks
// runner.go's AST (same go/ast walk-and-assert idiom as
// TestModelSpawnSitesUseScrubHelper in model_env_spawn_test.go and
// TestPromptLeakage in prompt_leak_test.go) looking for every `prBody :=
// ...` declaration and asserting the very next statement in its enclosing
// block reassigns prBody from a call to appendAcceptanceEvidence — the shape
// every current call site uses. It is deliberately pure syntax analysis (no
// go/types), so it stays cheap and doesn't depend on whether the package
// happens to compile.
//
// TestWiringSiteDetectionLogic proves the checker itself actually catches a
// removed/altered hook call, independent of runner.go's current source.

// reassignsPRBodyFromEvidenceHook reports whether stmt is `prBody =
// <expr>.appendAcceptanceEvidence(...)`. The receiver is matched by method
// name only (not by identifier, e.g. requiring "r") so the detector isn't
// brittle to an unrelated rename of the Runner receiver variable.
func reassignsPRBodyFromEvidenceHook(stmt ast.Stmt) bool {
	as, ok := stmt.(*ast.AssignStmt)
	if !ok || as.Tok != token.ASSIGN || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
		return false
	}
	id, ok := as.Lhs[0].(*ast.Ident)
	if !ok || id.Name != "prBody" {
		return false
	}
	call, ok := as.Rhs[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == "appendAcceptanceEvidence"
}

// isPRBodyDefine reports whether stmt is a `prBody := ...` short variable
// declaration — a PR-body assembly site.
func isPRBodyDefine(stmt ast.Stmt) bool {
	as, ok := stmt.(*ast.AssignStmt)
	if !ok || as.Tok != token.DEFINE || len(as.Lhs) != 1 {
		return false
	}
	id, ok := as.Lhs[0].(*ast.Ident)
	return ok && id.Name == "prBody"
}

// findPRBodyWiringGaps walks file's AST and returns one failure message per
// `prBody := ...` assembly site whose enclosing block does not immediately
// reassign prBody from an appendAcceptanceEvidence call, plus a diagnostic
// failure if it finds zero such sites at all (detector miscalibration, or
// the assembly pattern changed out from under it).
func findPRBodyWiringGaps(fset *token.FileSet, filename string, file *ast.File) []string {
	var failures []string
	sitesFound := 0

	ast.Inspect(file, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, stmt := range block.List {
			if !isPRBodyDefine(stmt) {
				continue
			}
			sitesFound++
			pos := fset.Position(stmt.Pos())

			if i+1 >= len(block.List) || !reassignsPRBodyFromEvidenceHook(block.List[i+1]) {
				failures = append(failures, fmt.Sprintf(
					"%s:%d: prBody assembled here but the next statement doesn't reassign it via appendAcceptanceEvidence — this PR body would skip the acceptance-evidence gate",
					filename, pos.Line))
			}
		}
		return true
	})

	if sitesFound == 0 {
		failures = append(failures, fmt.Sprintf(
			"%s: found 0 `prBody := ...` assembly sites — detector is miscalibrated or the assembly pattern changed", filename))
	}

	return failures
}

// TestPRBodyAssemblySitesCallAcceptanceEvidenceHook is the live regression
// guard: it fails if runner.go currently has any `prBody := ...` site whose
// PR body isn't run through appendAcceptanceEvidence before use.
func TestPRBodyAssemblySitesCallAcceptanceEvidenceHook(t *testing.T) {
	root := mustResolve(t, ".")
	path := filepath.Join(root, "runner.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	for _, f := range findPRBodyWiringGaps(fset, "runner.go", file) {
		t.Error(f)
	}
}

// TestWiringSiteDetectionLogic exercises findPRBodyWiringGaps against
// synthetic snippets so the checker's own gap detection is proven correct
// independent of runner.go's current source — including the exact failure
// mode GH-5438 asks the wiring pin to catch: removing the
// appendAcceptanceEvidence reassignment.
func TestWiringSiteDetectionLogic(t *testing.T) {
	tests := []struct {
		name        string
		src         string
		wantFailure bool
	}{
		{
			name: "wired site is compliant",
			src: `package executor

func f(r *Runner) string {
	prBody := "x"
	prBody = r.appendAcceptanceEvidence(nil, nil, "", prBody)
	return prBody
}
`,
			wantFailure: false,
		},
		{
			name: "removed hook call (the GH-5438 mutation) is caught",
			src: `package executor

func f(r *Runner) string {
	prBody := "x"
	return prBody
}
`,
			wantFailure: true,
		},
		{
			name: "reassignment to an unrelated expression is caught",
			src: `package executor

func f(r *Runner) string {
	prBody := "x"
	prBody = prBody + "y"
	return prBody
}
`,
			wantFailure: true,
		},
		{
			name: "reassignment calling a different method is caught",
			src: `package executor

func f(r *Runner) string {
	prBody := "x"
	prBody = r.someOtherMethod(prBody)
	return prBody
}
`,
			wantFailure: true,
		},
		{
			name: "multiple sites in separate if/else blocks are each checked independently",
			src: `package executor

func f(r *Runner, useSDK bool) string {
	var prURL string
	if useSDK {
		prBody := "sdk"
		prBody = r.appendAcceptanceEvidence(nil, nil, "", prBody)
		prURL = prBody
	} else {
		prBody := "cli"
		prURL = prBody
	}
	return prURL
}
`,
			wantFailure: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "snippet.go", tt.src, 0)
			if err != nil {
				t.Fatalf("parse snippet: %v", err)
			}
			failures := findPRBodyWiringGaps(fset, "snippet.go", file)
			if tt.wantFailure && len(failures) == 0 {
				t.Errorf("expected a wiring-gap failure, got none")
			}
			if !tt.wantFailure && len(failures) != 0 {
				t.Errorf("expected no failures, got: %v", failures)
			}
		})
	}
}
