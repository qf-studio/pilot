package main

import (
	"os"
	"strings"
	"testing"
)

// TestDocsProjectPathWiredAtBothDaemonConstructionSites is a GH-5003
// regression tripwire: gateway.Server.SetDocsProjectPath (backing
// GET /api/v1/docs/tree and /docs/file) must be called at BOTH daemon
// construction sites in this file — gateway mode (p.Gateway()...) and
// polling mode (gwServer...) — or the routes silently 404 in whichever
// mode is missing the call, since docsProjectPath defaults to "" (no
// known project) rather than falling back to "." the way gitGraphPath
// does. Mirrors gh4778_test.go's source-grep pattern and asserts parity
// with SetGitGraphPath's own two call sites, the sibling wiring this
// mirrors (both added at the same two spots for the same reason).
func TestDocsProjectPathWiredAtBothDaemonConstructionSites(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(content)

	gitGraphCount := strings.Count(src, "SetGitGraphPath(")
	docsCount := strings.Count(src, "SetDocsProjectPath(")

	if gitGraphCount != 2 {
		t.Fatalf("SetGitGraphPath( occurs %d times in main.go, want 2 — this test's baseline assumption is stale, update it", gitGraphCount)
	}
	if docsCount != gitGraphCount {
		t.Errorf("SetDocsProjectPath( occurs %d times in main.go, want %d (parity with SetGitGraphPath's gateway-mode and polling-mode call sites) — "+
			"a contract wired only one way silently doesn't exist in the other mode (the GH-4784 class; pilot#4835 §wiring precedent)",
			docsCount, gitGraphCount)
	}

	if !strings.Contains(src, "p.Gateway().SetDocsProjectPath(") {
		t.Error("gateway mode must call p.Gateway().SetDocsProjectPath(projectPath)")
	}
	if !strings.Contains(src, "gwServer.SetDocsProjectPath(") {
		t.Error("polling mode must call gwServer.SetDocsProjectPath(projectPath)")
	}
}
