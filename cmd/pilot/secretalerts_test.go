package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/config"
)

// TestStartSecretAlertWatch_NilEngineWarnsOnce: with no alerts engine the watcher
// does not start and logs one Warn line; with an engine it starts (GH-6279).
func TestStartSecretAlertWatch_NilEngineWarnsOnce(t *testing.T) {
	ghCfg := func(enabled bool) *config.Config {
		return &config.Config{Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: enabled, Token: "test-github-token", Repo: "o/r"},
		}}
	}
	tests := []struct {
		name        string
		cfg         *config.Config
		engine      *alerts.Engine
		wantStarted bool
		wantWarns   int
	}{
		{"nil engine", ghCfg(true), nil, false, 1},
		{"engine present", ghCfg(true), alerts.NewEngine(&alerts.AlertConfig{Enabled: true}), true, 0},
		{"github adapter off: nothing to say", ghCfg(false), nil, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			if got := startSecretAlertWatchWith(ctx, tt.cfg, tt.engine, log); got != tt.wantStarted {
				t.Errorf("started = %v, want %v", got, tt.wantStarted)
			}
			if n := strings.Count(buf.String(), "level=WARN"); n != tt.wantWarns {
				t.Errorf("warns = %d, want %d\n%s", n, tt.wantWarns, buf.String())
			}
			if tt.wantWarns == 1 && !strings.Contains(buf.String(), "no alerts engine") {
				t.Errorf("warn does not name the missing engine:\n%s", buf.String())
			}
		})
	}
}

// TestSecretAlertWatchStartedOutsideEngineCheck: the gateway-only start in main
// calls the helper outside any `if gwAlertsEngine != nil`, so the missing-engine
// Warn is logged there too. The call with the polling-mode engine is the other
// site (GH-6279).
func TestSecretAlertWatchStartedOutsideEngineCheck(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	mentions := func(e ast.Expr, name string) bool {
		found := false
		ast.Inspect(e, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == name {
				found = true
			}
			return !found
		})
		return found
	}

	calls := map[string]bool{} // engine variable -> called outside an engine nil check
	var stack []ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "startSecretAlertWatch" || len(call.Args) != 3 {
			return true
		}
		engine, ok := call.Args[2].(*ast.Ident)
		if !ok {
			return true
		}
		guarded := false
		for _, anc := range stack {
			// Only the body of the if is guarded, not its condition.
			if ifs, ok := anc.(*ast.IfStmt); ok && mentions(ifs.Cond, engine.Name) && inBody(ifs, call) {
				guarded = true
			}
		}
		if prev, seen := calls[engine.Name]; !seen || prev {
			calls[engine.Name] = !guarded
		}
		return true
	})

	for _, engine := range []string{"alertsEngine", "gwAlertsEngine"} {
		outside, called := calls[engine]
		if !called {
			t.Errorf("main.go does not call startSecretAlertWatch with %s", engine)
		} else if !outside {
			t.Errorf("startSecretAlertWatch(…, %s) sits inside an `if %s` check; the missing-engine Warn never logs there", engine, engine)
		}
	}
}

// inBody reports whether n lies inside the if statement's body.
func inBody(ifs *ast.IfStmt, n ast.Node) bool {
	return ifs.Body.Pos() <= n.Pos() && n.End() <= ifs.Body.End()
}
