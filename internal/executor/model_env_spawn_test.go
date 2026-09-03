package executor

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// GH-5279: regression guard for GH-5275/GH-5276/GH-5278. Every subprocess
// that invokes the Claude Code or OpenCode binary must build cmd.Env via the
// modelSubprocessEnv scrub helper (model_env.go) — otherwise the daemon's
// ambient environment (adapter bot tokens, API keys, webhook secrets) leaks
// straight into a model-controlled process that runs with
// --dangerously-skip-permissions and Bash in its tool allowlist.
//
// TestModelSpawnSitesUseScrubHelper statically walks every non-test .go file
// directly in this package (mirrors the site enumeration in GH-5275's
// Context section — see also prompt_leak_test.go for the same
// walk-and-assert idiom) looking for exec.Command/exec.CommandContext calls
// that target the claude/OpenCode binaries, and fails if any such call's
// cmd.Env isn't traceable to a modelSubprocessEnv call. This is deliberately
// pure syntax analysis (go/ast, no go/types/go/packages) so it stays cheap
// and has no dependency on whether the package currently compiles.
//
// TestSpawnSiteDetectionLogic exercises the same detection helpers against
// synthetic snippets so the checker's own bypass detection is proven
// correct independent of the current state of this package's source.

// modelBinaryLiterals are string-literal command names that unambiguously
// identify a direct claude/OpenCode CLI invocation.
var modelBinaryLiterals = map[string]bool{
	"claude":   true,
	"opencode": true,
}

// modelBackendReceivers are struct types whose methods spawn the model CLI
// through a configured, non-literal command path (e.g. b.config.Command,
// parts[0] parsed from a configured server command) rather than a string
// literal containing "claude"/"opencode". QwenCodeBackend was originally
// excluded here as out of scope for GH-5275/GH-5276/GH-5278, but GH-5302
// closed that gap — it spawns the same kind of Bash-capable,
// permissions-skipping model CLI and now routes through modelSubprocessEnv
// too (backend_qwencode.go), so it belongs in this guard like the others.
var modelBackendReceivers = map[string]bool{
	"ClaudeCodeBackend": true,
	"OpenCodeBackend":   true,
	"QwenCodeBackend":   true,
}

// receiverTypeName returns the (de-pointered) receiver type name of fn, or
// "" if fn has no receiver (a package-level function).
func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if ident, ok := t.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// isModelBinarySpawn reports whether arg (the command argument of an
// exec.Command/exec.CommandContext call) targets the claude or OpenCode
// binary. String literals are matched exactly against modelBinaryLiterals
// (so "git", "gh", "bash", "dmesg" etc. are never flagged regardless of
// context). Non-literal arguments (identifiers, selectors, index
// expressions) are matched by rendering their source text and looking for
// "claude"/"opencode" case-insensitively (catches claudeCmd, j.claudeCmd,
// p.claudeCmd, ...), falling back to the enclosing receiver type for the
// cases where the command comes from a generically-named config field
// (b.config.Command in ClaudeCodeBackend, parts[0] in OpenCodeBackend).
func isModelBinarySpawn(arg ast.Expr, fset *token.FileSet, recv string) bool {
	if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		val, err := strconv.Unquote(lit.Value)
		if err != nil {
			return false
		}
		return modelBinaryLiterals[val]
	}

	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, arg); err == nil {
		text := strings.ToLower(buf.String())
		if strings.Contains(text, "claude") || strings.Contains(text, "opencode") {
			return true
		}
	}

	return modelBackendReceivers[recv]
}

// findEnvAssignRHS searches body for an assignment to `<varName>.Env` and
// returns its right-hand side expression, or nil if Env is never assigned.
func findEnvAssignRHS(body ast.Node, varName string) ast.Expr {
	var result ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		if result != nil {
			return false
		}
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Env" {
				continue
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Name != varName {
				continue
			}
			if i < len(as.Rhs) {
				result = as.Rhs[i]
			}
		}
		return true
	})
	return result
}

// exprUsesScrubHelper reports whether expr's subtree calls modelSubprocessEnv
// directly (e.g. `modelSubprocessEnv(os.Environ())`), or transitively
// through any identifier it references. Identifiers are resolved against
// EVERY assignment ever made to that name in the function (not just the
// most recent one) because the real accumulator idiom used here reassigns
// the same variable repeatedly — e.g.
//
//	env := append(modelSubprocessEnv(os.Environ()), "PILOT_EXECUTOR=1")
//	env = append(env, "ANTHROPIC_BASE_URL="+base)
//	env = prependPathEnv(env, shimDir)
//	cmd.Env = env
//
// — where the scrub call only appears in the FIRST assignment to `env`, and
// every later assignment self-references `env` on its own right-hand side.
// Checking only the last write would miss the scrub call entirely. The
// visited set guards against infinite recursion on that self-reference.
func exprUsesScrubHelper(expr ast.Expr, varAssigns map[string][]ast.Expr, depth int) bool {
	return searchForScrubHelper(expr, varAssigns, map[string]bool{}, depth)
}

func searchForScrubHelper(expr ast.Expr, varAssigns map[string][]ast.Expr, visited map[string]bool, depth int) bool {
	if expr == nil || depth > 20 {
		return false
	}

	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		switch v := n.(type) {
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "modelSubprocessEnv" {
				found = true
				return false
			}
		case *ast.Ident:
			if rhss, ok := varAssigns[v.Name]; ok && !visited[v.Name] {
				visited[v.Name] = true
				for _, rhs := range rhss {
					if searchForScrubHelper(rhs, varAssigns, visited, depth+1) {
						found = true
						break
					}
				}
				if found {
					return false
				}
			}
		}
		return true
	})
	return found
}

// checkFuncSpawnSites inspects a single function body for
// exec.Command/exec.CommandContext calls that target the claude/OpenCode
// binary and returns one failure message per call whose cmd.Env cannot be
// traced back to modelSubprocessEnv.
func checkFuncSpawnSites(fset *token.FileSet, filename string, fn *ast.FuncDecl) []string {
	var failures []string

	// First pass: record every `x := <expr>` / `x = <expr>` in this function
	// so later we can (a) map an exec.Command call to the variable it's
	// bound to, and (b) resolve identifier aliasing for the cmd.Env RHS
	// (the `env := append(modelSubprocessEnv(...), ...)` shape, including
	// through further reassignments of the same accumulator variable).
	callVar := map[*ast.CallExpr]string{}
	varAssigns := map[string][]ast.Expr{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, rhs := range as.Rhs {
			if i >= len(as.Lhs) {
				continue
			}
			id, ok := as.Lhs[i].(*ast.Ident)
			if !ok {
				continue
			}
			varAssigns[id.Name] = append(varAssigns[id.Name], rhs)
			if ce, ok := rhs.(*ast.CallExpr); ok {
				callVar[ce] = id.Name
			}
		}
		return true
	})

	recv := receiverTypeName(fn)

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "exec" {
			return true
		}

		var cmdArgIdx int
		switch sel.Sel.Name {
		case "Command":
			cmdArgIdx = 0
		case "CommandContext":
			cmdArgIdx = 1
		default:
			return true
		}
		if len(call.Args) <= cmdArgIdx {
			return true
		}

		if !isModelBinarySpawn(call.Args[cmdArgIdx], fset, recv) {
			return true
		}

		pos := fset.Position(call.Pos())

		varName, ok := callVar[call]
		if !ok {
			failures = append(failures, fmt.Sprintf(
				"%s:%d: model spawn via exec.%s is not bound to a simple `cmd := ...` variable, cannot verify it routes through modelSubprocessEnv",
				filename, pos.Line, sel.Sel.Name))
			return true
		}

		envRHS := findEnvAssignRHS(fn.Body, varName)
		if envRHS == nil {
			failures = append(failures, fmt.Sprintf(
				"%s:%d: %s.Env is never set — model subprocess would inherit the full ambient environment instead of routing through modelSubprocessEnv",
				filename, pos.Line, varName))
			return true
		}

		if !exprUsesScrubHelper(envRHS, varAssigns, 0) {
			failures = append(failures, fmt.Sprintf(
				"%s:%d: %s.Env is set but doesn't call modelSubprocessEnv — this spawn site bypasses the env scrub helper",
				filename, pos.Line, varName))
		}

		return true
	})

	return failures
}

// TestModelSpawnSitesUseScrubHelper is the live regression guard: it fails
// if any current or future exec.Command/exec.CommandContext call in this
// package that targets the claude/OpenCode binary builds cmd.Env without
// going through modelSubprocessEnv.
func TestModelSpawnSitesUseScrubHelper(t *testing.T) {
	root := mustResolve(t, ".")
	fset := token.NewFileSet()
	scanned := 0
	var failures []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Scope to the top-level internal/executor package only — this
			// mirrors the exact site enumeration in GH-5275's Context
			// section, all of which live directly in this package.
			// Subpackages (workflow, ghguard, ...) spawn different things
			// (user hook scripts, the gh shim) under different contracts.
			if path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, ferr := parser.ParseFile(fset, path, nil, 0)
		if ferr != nil {
			t.Fatalf("parse %s: %v", path, ferr)
		}
		scanned++

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}

		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			failures = append(failures, checkFuncSpawnSites(fset, rel, fn)...)
			return false
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if scanned == 0 {
		t.Fatalf("scanned 0 files — root misconfigured")
	}

	for _, f := range failures {
		t.Error(f)
	}
	t.Logf("scanned %d .go files in internal/executor for claude/OpenCode spawn sites", scanned)
}

// TestSpawnSiteDetectionLogic proves the checker itself actually catches a
// spawn site that bypasses modelSubprocessEnv (and doesn't false-positive on
// compliant sites or on non-model commands), independent of the current
// state of this package's real source files.
func TestSpawnSiteDetectionLogic(t *testing.T) {
	tests := []struct {
		name        string
		src         string
		wantFailure bool
	}{
		{
			name: "direct modelSubprocessEnv call is compliant",
			src: `package executor

import (
	"context"
	"os"
	"os/exec"
)

func spawnCompliant(ctx context.Context) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "claude", "-p", "hi")
	cmd.Env = modelSubprocessEnv(os.Environ())
	return cmd
}
`,
			wantFailure: false,
		},
		{
			name: "modelSubprocessEnv via intermediate append var is compliant",
			src: `package executor

import (
	"context"
	"os"
	"os/exec"
)

func spawnCompliantAppend(ctx context.Context) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "claude", "-p", "hi")
	env := append(modelSubprocessEnv(os.Environ()), "ANTHROPIC_MODEL=x")
	cmd.Env = env
	return cmd
}
`,
			wantFailure: false,
		},
		{
			name: "raw os.Environ bypasses the scrub helper",
			src: `package executor

import (
	"context"
	"os"
	"os/exec"
)

func spawnBypass(ctx context.Context) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "claude", "-p", "hi")
	cmd.Env = os.Environ()
	return cmd
}
`,
			wantFailure: true,
		},
		{
			name: "cmd.Env never set at all bypasses the scrub helper",
			src: `package executor

import (
	"context"
	"os/exec"
)

func spawnNoEnv(ctx context.Context) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "claude", "-p", "hi")
	return cmd
}
`,
			wantFailure: true,
		},
		{
			name: "non-model command is not required to scrub",
			src: `package executor

import (
	"context"
	"os/exec"
)

func spawnGit(ctx context.Context) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", "status")
	return cmd
}
`,
			wantFailure: false,
		},
		{
			name: "ClaudeCodeBackend receiver spawn via configured command bypasses the scrub helper",
			src: `package executor

import (
	"context"
	"os/exec"
)

type ClaudeCodeBackend struct {
	config struct{ Command string }
}

func (b *ClaudeCodeBackend) spawn(ctx context.Context) *exec.Cmd {
	cmd := exec.CommandContext(ctx, b.config.Command)
	return cmd
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

			var failures []string
			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					return true
				}
				failures = append(failures, checkFuncSpawnSites(fset, "snippet.go", fn)...)
				return false
			})

			if tt.wantFailure && len(failures) == 0 {
				t.Errorf("expected a scrub-bypass failure, got none")
			}
			if !tt.wantFailure && len(failures) != 0 {
				t.Errorf("expected no failures, got: %v", failures)
			}
		})
	}
}
