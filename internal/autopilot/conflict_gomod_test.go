package autopilot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsGoModSumOnlyConflict(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{"empty", nil, false},
		{"go.mod only", []string{"go.mod"}, true},
		{"go.sum only", []string{"go.sum"}, true},
		{"both", []string{"go.mod", "go.sum"}, true},
		{"includes source file", []string{"go.mod", "main.go"}, false},
		{"source file only", []string{"main.go"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGoModSumOnlyConflict(tc.files); got != tc.want {
				t.Fatalf("isGoModSumOnlyConflict(%v) = %v, want %v", tc.files, got, tc.want)
			}
		})
	}
}

func TestResolveGoModRequireUnion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")

	content := strings.Join([]string{
		"module fixture",
		"",
		"go 1.25",
		"",
		"replace example.com/dep-a => ./localdeps/depa",
		"",
		"replace example.com/dep-b => ./localdeps/depb",
		"<<<<<<< HEAD",
		"require example.com/dep-b v0.0.0-00010101000000-000000000000",
		"=======",
		"require example.com/dep-a v0.0.0-00010101000000-000000000000",
		">>>>>>> feature/dep-a",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	if err := resolveGoModRequireUnion(path); err != nil {
		t.Fatalf("resolveGoModRequireUnion: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read resolved go.mod: %v", err)
	}
	resolved := string(got)

	if strings.Contains(resolved, "<<<<<<<") || strings.Contains(resolved, "=======") || strings.Contains(resolved, ">>>>>>>") {
		t.Fatalf("resolved go.mod still contains conflict markers:\n%s", resolved)
	}
	if !strings.Contains(resolved, "require example.com/dep-a v0.0.0-00010101000000-000000000000") {
		t.Fatalf("resolved go.mod missing dep-a require line:\n%s", resolved)
	}
	if !strings.Contains(resolved, "require example.com/dep-b v0.0.0-00010101000000-000000000000") {
		t.Fatalf("resolved go.mod missing dep-b require line:\n%s", resolved)
	}
}

func TestResolveGoModRequireUnion_NonRequireLineBails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")

	// One side of the hunk is a real merge decision (an `exclude` directive),
	// not a pure require addition. resolveGoModRequireUnion must reject the
	// whole file rather than guess, leaving it untouched (still conflicted)
	// for the caller to fall through to close-and-reexecute.
	content := strings.Join([]string{
		"module fixture",
		"",
		"go 1.25",
		"",
		"<<<<<<< HEAD",
		"exclude example.com/dep-a v2.0.0",
		"=======",
		"require example.com/dep-a v1.0.0",
		">>>>>>> feature/dep-a",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	if err := resolveGoModRequireUnion(path); err == nil {
		t.Fatalf("expected error for hunk containing a non-require line")
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read go.mod after failed resolution: %v", readErr)
	}
	if string(got) != content {
		t.Fatalf("go.mod should be left untouched on error\nwant:\n%s\ngot:\n%s", content, string(got))
	}
}

// newLocalReplaceModule creates a tiny standalone Go module under
// <local>/localdeps/<name> so go.mod requires can be satisfied via a
// filesystem `replace` directive — no network/module-proxy access needed for
// `go mod tidy` to resolve it.
func newLocalReplaceModule(t *testing.T, local, name, pkg string) {
	t.Helper()
	dir := filepath.Join(local, "localdeps", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	writeFixtureFile(t, dir, "go.mod", "module example.com/"+name+"\n\ngo 1.25\n")
	writeFixtureFile(t, dir, name+".go", "package "+pkg+"\n\nfunc Hello() string { return \""+pkg+"\" }\n")
}

// TestResolveGoModSumConflict_PureRequireUnion reproduces the exact GH-4328
// scenario end to end: two sibling branches each add a require for a
// different (locally-replaced, so no network needed) module. The merge
// conflicts on go.mod only; resolveGoModSumConflict must union the two
// require lines, run `go mod tidy`, commit, and push the fix to the PR
// branch.
func TestResolveGoModSumConflict_PureRequireUnion(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	newLocalReplaceModule(t, local, "depa", "depa")
	newLocalReplaceModule(t, local, "depb", "depb")

	writeFixtureFile(t, local, "go.mod", strings.Join([]string{
		"module fixture",
		"",
		"go 1.25",
		"",
		"replace example.com/depa => ./localdeps/depa",
		"",
		"replace example.com/depb => ./localdeps/depb",
		"",
	}, "\n"))
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "scaffold local replace modules")
	runFixtureGit(t, local, "push", "origin", "main")

	runFixtureGit(t, local, "checkout", "-b", "feature/dep-a")
	appendFixtureFile(t, local, "go.mod", "require example.com/depa v0.0.0-00010101000000-000000000000\n")
	writeFixtureFile(t, local, "usea.go", "package fixture\n\nimport \"example.com/depa\"\n\nvar UseDepA = depa.Hello()\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "add depa")
	runFixtureGit(t, local, "push", "origin", "feature/dep-a")

	runFixtureGit(t, local, "checkout", "main")
	appendFixtureFile(t, local, "go.mod", "require example.com/depb v0.0.0-00010101000000-000000000000\n")
	writeFixtureFile(t, local, "useb.go", "package fixture\n\nimport \"example.com/depb\"\n\nvar UseDepB = depb.Hello()\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "add depb")
	runFixtureGit(t, local, "push", "origin", "main")

	result, cleanup, err := attemptLocalMerge(ctx, local, "feature/dep-a", "main")
	defer cleanup()
	if err != nil {
		t.Fatalf("attemptLocalMerge: %v", err)
	}
	if !result.Conflicted() {
		t.Fatalf("expected go.mod conflict, got clean merge")
	}
	if !isGoModSumOnlyConflict(result.ConflictedFiles) {
		t.Fatalf("expected conflict confined to go.mod/go.sum, got: %v", result.ConflictedFiles)
	}

	if err := resolveGoModSumConflict(ctx, result.WorktreePath, "feature/dep-a", result.ConflictedFiles); err != nil {
		t.Fatalf("resolveGoModSumConflict: %v", err)
	}

	// Verify the pushed branch on origin has a clean, conflict-marker-free
	// go.mod carrying both requires, and the mechanical-resolution commit
	// message.
	logOut := runFixtureGit(t, local, "log", "-1", "--format=%s", "origin/feature/dep-a")
	if strings.TrimSpace(logOut) != mechanicalResolutionCommitMessage {
		t.Fatalf("expected top commit message %q, got %q", mechanicalResolutionCommitMessage, strings.TrimSpace(logOut))
	}

	goModOut := runFixtureGit(t, local, "show", "origin/feature/dep-a:go.mod")
	if strings.Contains(goModOut, "<<<<<<<") {
		t.Fatalf("pushed go.mod still contains conflict markers:\n%s", goModOut)
	}
	if !strings.Contains(goModOut, "example.com/depa") || !strings.Contains(goModOut, "example.com/depb") {
		t.Fatalf("pushed go.mod missing one of the unioned requires:\n%s", goModOut)
	}
}

// TestResolveGoModSumConflict_BuildGateCatchesBrokenUnion reproduces the
// rarer failure this rung's post-tidy `go build ./...` gate exists for: two
// branches each add a require that individually resolves and passes `go mod
// tidy`, but the resulting source doesn't actually compile once combined
// (here, a type mismatch introduced by the depb side). resolveGoModSumConflict
// must fail without committing or pushing anything, leaving the caller to
// fall through to closeAndReexecute.
func TestResolveGoModSumConflict_BuildGateCatchesBrokenUnion(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	newLocalReplaceModule(t, local, "depa", "depa")
	newLocalReplaceModule(t, local, "depb", "depb")

	writeFixtureFile(t, local, "go.mod", strings.Join([]string{
		"module fixture",
		"",
		"go 1.25",
		"",
		"replace example.com/depa => ./localdeps/depa",
		"",
		"replace example.com/depb => ./localdeps/depb",
		"",
	}, "\n"))
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "scaffold local replace modules")
	runFixtureGit(t, local, "push", "origin", "main")

	runFixtureGit(t, local, "checkout", "-b", "feature/dep-a")
	appendFixtureFile(t, local, "go.mod", "require example.com/depa v0.0.0-00010101000000-000000000000\n")
	writeFixtureFile(t, local, "usea.go", "package fixture\n\nimport \"example.com/depa\"\n\nvar UseDepA = depa.Hello()\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "add depa")
	runFixtureGit(t, local, "push", "origin", "feature/dep-a")

	runFixtureGit(t, local, "checkout", "main")
	appendFixtureFile(t, local, "go.mod", "require example.com/depb v0.0.0-00010101000000-000000000000\n")
	// Type mismatch (string + int): loads and resolves fine for `go mod tidy`'s
	// import-graph purposes, but fails `go build`.
	writeFixtureFile(t, local, "useb.go", "package fixture\n\nimport \"example.com/depb\"\n\nvar UseDepB = depb.Hello() + 1\n")
	runFixtureGit(t, local, "add", ".")
	runFixtureGit(t, local, "commit", "-m", "add depb")
	runFixtureGit(t, local, "push", "origin", "main")

	result, cleanup, err := attemptLocalMerge(ctx, local, "feature/dep-a", "main")
	defer cleanup()
	if err != nil {
		t.Fatalf("attemptLocalMerge: %v", err)
	}
	if !isGoModSumOnlyConflict(result.ConflictedFiles) {
		t.Fatalf("expected conflict confined to go.mod/go.sum, got: %v", result.ConflictedFiles)
	}

	if err := resolveGoModSumConflict(ctx, result.WorktreePath, "feature/dep-a", result.ConflictedFiles); err == nil {
		t.Fatal("expected resolveGoModSumConflict to fail the build gate")
	} else if !strings.Contains(err.Error(), "go build") {
		t.Fatalf("expected build-gate error, got: %v", err)
	}

	// Nothing should have been pushed — origin/feature/dep-a is unchanged.
	logOut := runFixtureGit(t, local, "log", "-1", "--format=%s", "origin/feature/dep-a")
	if strings.TrimSpace(logOut) == mechanicalResolutionCommitMessage {
		t.Fatalf("origin/feature/dep-a should not have received the mechanical-resolution commit")
	}
}

func appendFixtureFile(t *testing.T, dir, name, content string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s for append: %v", name, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("append %s: %v", name, err)
	}
}
