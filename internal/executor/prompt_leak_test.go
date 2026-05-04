package executor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoPromptLeakStrings is the cross-prompt invariant guard born from the
// 2026-05-04 OAuth cascade #2 (#2559 recurrence). Cascade #1 fix (PR #2562)
// only patched the planner prompt in epic.go and explicitly skipped
// workflow.go on the (incorrect) reasoning that only the planner generates
// subtask titles. The executor LLM also pattern-matched on the example, and
// because the executor WRITES THE CODE, the second cascade was worse.
//
// This test scans EVERY raw string literal in every .go file under
// internal/executor and internal/autopilot for known leak strings and the
// "concrete commit-message-style example" regex family. ALL_CAPS placeholders
// (e.g. `feat(SCOPE): IMPERATIVE_SUMMARY`) are explicitly allowed; only
// plausible-looking lowercase examples are forbidden.
//
// Adding a new forbidden literal here is the third leg of the
// prompt-leak-fix-checklist SOP (.agent/sops/integrations/prompt-leak-fix-checklist.md).
func TestNoPromptLeakStrings(t *testing.T) {
	roots := []string{
		mustResolve(t, "."),                  // internal/executor
		mustResolve(t, "../autopilot"),       // internal/autopilot
	}

	literalForbidden := []string{
		"feat(auth): add OAuth provider integration",
		"feat(auth): add OAuth session logout endpoint",
		"feat(auth): add GitLab OAuth provider integration",
		"feat(auth): add Microsoft and Discord OAuth provider integration",
		"fix(api): handle nil response in webhook handler",
		"chore(deps): upgrade go modules to latest",
	}
	// Conventional-commit-style example with a lowercase imperative verb.
	// Matches "feat(auth): add ..." but NOT "feat(SCOPE): IMPERATIVE_SUMMARY"
	// because the trailing token must start with [a-z].
	concreteExample := regexp.MustCompile(`(?m)^\s*[-*]?\s*(feat|fix|chore|refactor|perf|docs|test|build|ci|style)\([a-z][a-z0-9_-]*\):\s+[a-z]`)

	fset := token.NewFileSet()
	scanned := 0

	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if info.Name() == "testdata" || strings.HasPrefix(info.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			scanned++

			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}

			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				val := lit.Value
				// Only scan multi-line raw strings (backtick-delimited).
				// Regular "..." strings are too small to host LLM prompts and
				// would generate noise from short user-facing strings.
				if !strings.HasPrefix(val, "`") || !strings.Contains(val, "\n") {
					return true
				}

				rel, _ := filepath.Rel(root, path)
				pos := fset.Position(lit.Pos())

				for _, f := range literalForbidden {
					if strings.Contains(val, f) {
						t.Errorf("%s:%d: prompt-shaped literal contains forbidden concrete example %q (#2559/cascade-2 recurrence guard)", rel, pos.Line, f)
					}
				}
				if m := concreteExample.FindString(val); m != "" {
					t.Errorf("%s:%d: prompt-shaped literal contains concrete commit-message-style example %q — use ALL_CAPS placeholders (e.g. feat(SCOPE): IMPERATIVE_SUMMARY)", rel, pos.Line, strings.TrimSpace(m))
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if scanned == 0 {
		t.Fatalf("scanned 0 files — roots misconfigured")
	}
	t.Logf("scanned %d .go files across %d roots", scanned, len(roots))
}

func mustResolve(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Fatalf("resolve %s: %v", rel, err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("stat %s: %v", abs, err)
	}
	return abs
}
