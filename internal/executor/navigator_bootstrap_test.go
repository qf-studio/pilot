package executor

import (
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// bootstrapFakeBackend stands in for the Claude Code CLI. A scripted session
// writes through session.write, which enforces opts.AllowedTools the way the
// CLI does: a path is writable only when a bare tool entry or a scoped
// "Tool(dir/**)" entry covers it. A denied write still emits its tool_use
// event and leaves no file behind.
type bootstrapFakeBackend struct {
	mu     sync.Mutex
	calls  []ExecuteOptions
	script func(s *bootstrapSession)
}

type bootstrapSession struct {
	opts ExecuteOptions
}

func (b *bootstrapFakeBackend) Name() string      { return "bootstrap-fake" }
func (b *bootstrapFakeBackend) IsAvailable() bool { return true }

func (b *bootstrapFakeBackend) Execute(_ context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.calls = append(b.calls, opts)
	b.mu.Unlock()
	if b.script != nil {
		b.script(&bootstrapSession{opts: opts})
	}
	return &BackendResult{Success: true, Output: "done", SessionID: "boot-1", TokensInput: 1000, TokensOutput: 500}, nil
}

func (b *bootstrapFakeBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

// toolAllows mirrors the CLI permission rule match for Write and Edit.
func toolAllows(allowed []string, tool, rel string) bool {
	rel = path.Clean(filepath.ToSlash(rel))
	for _, entry := range allowed {
		if entry == tool {
			return true
		}
		scope, ok := strings.CutPrefix(entry, tool+"(")
		if !ok {
			continue
		}
		dir, ok := strings.CutSuffix(scope, "/**)")
		if ok && (rel == dir || strings.HasPrefix(rel, dir+"/")) {
			return true
		}
	}
	return false
}

func (s *bootstrapSession) write(rel, content string) bool {
	if s.opts.EventHandler != nil {
		s.opts.EventHandler(BackendEvent{
			Type:      EventTypeToolUse,
			ToolName:  "Write",
			ToolInput: map[string]interface{}{"file_path": filepath.Join(s.opts.ProjectPath, rel)},
		})
	}
	if !toolAllows(s.opts.AllowedTools, "Write", rel) {
		return false
	}
	full := filepath.Join(s.opts.ProjectPath, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return false
	}
	return os.WriteFile(full, []byte(content), 0o644) == nil
}

// fixtureClone returns a scaffolded legacy clone: source files plus a fresh
// Navigator scaffold whose three context sections are still placeholders.
func fixtureClone(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/legacy\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Decision: build the initializer with an empty templatesPath (embedded
	// templates only). NewNavigatorInitializer prefers the Navigator plugin
	// cache under $HOME when present, which made the suite depend on the
	// developer machine: plugin 8.3.5 nests "### Key Components" under
	// "## Project Structure", unlike the embedded README.
	init := &NavigatorInitializer{templatesPath: "", log: logging.WithComponent("test")}
	if err := init.Initialize(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// assertEmbeddedReadmeLayout fails when the fixture README was not built from
// the embedded template. The embedded README keeps "### Key Components" under
// "## Architecture"; the Navigator plugin nests it under "## Project
// Structure", so a fixture that picked up $HOME's plugin cache differs here.
func assertEmbeddedReadmeLayout(t *testing.T, dir string) {
	t.Helper()
	raw, err := os.ReadFile(readmePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	parent := ""
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			parent = strings.TrimSpace(line)
		case strings.TrimSpace(line) == "### Key Components":
			if parent != "## Architecture" {
				t.Fatalf("fixture README nests Key Components under %q, want \"## Architecture\": fixtureClone must use the embedded templates, not the plugin cache", parent)
			}
			return
		}
	}
	t.Fatal("fixture README lacks ### Key Components")
}

func readmePath(dir string) string { return filepath.Join(dir, ".agent", "DEVELOPMENT-README.md") }

// filledReadme replaces the placeholder body of the three context sections.
func filledReadme(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(readmePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return fillReadmeText(t, string(raw))
}

// fillReadmeText replaces only the direct body of each context section (text
// up to the next heading of any level), so a nested subsection such as
// "### Key Components" under "## Project Structure" keeps its header.
func fillReadmeText(t *testing.T, text string) string {
	t.Helper()
	anyHeading := regexp.MustCompile(`(?m)^#{1,6} `)
	for _, sec := range contextInjectionSections {
		loc := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(sec.header) + `[ \t]*$`).FindStringIndex(text)
		if loc == nil {
			t.Fatalf("fixture README lacks %q", sec.header)
		}
		end := len(text)
		if next := anyHeading.FindStringIndex(text[loc[1]:]); next != nil {
			end = loc[1] + next[0]
		}
		real := "\nReal content for " + strings.TrimLeft(sec.header, "# ") + ": src/main.go is the entry point.\n\n"
		text = text[:loc[1]] + real + text[end:]
	}
	return text
}

// fillingScript is a scripted bootstrap that fills the README and writes an
// architecture note and a graph through the allowlist.
func fillingScript(t *testing.T, dir string) func(*bootstrapSession) {
	readme := filledReadme(t, dir)
	return func(s *bootstrapSession) {
		s.write(".agent/DEVELOPMENT-README.md", readme)
		s.write(".agent/system/architecture.md", "# Architecture\n")
		s.write(".agent/knowledge/graph.json", `{"version":1,"nodes":{"concepts":{"src":{"label":"src"}},"memories":{}},"edges":[]}`)
	}
}

func newBootstrapRunner(b Backend, mode string) *Runner {
	r := NewRunnerWithBackend(b)
	r.config = &BackendConfig{Navigator: &NavigatorConfig{AutoInit: true, Bootstrap: mode}}
	return r
}

func TestNavigatorBootstrap_OutsideWriteRejectedByAllowlist(t *testing.T) {
	dir := fixtureClone(t)
	var outsideOK, insideOK bool
	b := &bootstrapFakeBackend{script: func(s *bootstrapSession) {
		outsideOK = s.write("src/main.go", "package hacked\n")
		_ = s.write("go.mod", "module hacked\n")
		insideOK = s.write(".agent/system/architecture.md", "# Architecture\n")
	}}

	res, err := newBootstrapRunner(b, "").ExecuteNavigatorBootstrap(context.Background(), dir)
	if err != nil {
		t.Fatalf("ExecuteNavigatorBootstrap: %v", err)
	}

	if outsideOK {
		t.Error("write outside the Navigator directory was accepted by the allowlist")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "src", "main.go"))
	if string(got) != "package main\n" {
		t.Errorf("tracked file was modified: %q", got)
	}
	mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	if strings.Contains(string(mod), "hacked") {
		t.Errorf("go.mod was modified: %q", mod)
	}
	if !insideOK {
		t.Error("write under the Navigator directory was rejected")
	}
	if want := []string{".agent/system/architecture.md"}; !reflect.DeepEqual(res.FilesWritten, want) {
		t.Errorf("FilesWritten = %v, want %v (denied paths must not be listed)", res.FilesWritten, want)
	}
	for _, tool := range b.calls[0].AllowedTools {
		if tool == "Write" || tool == "Edit" || tool == "MultiEdit" || tool == "NotebookEdit" {
			t.Errorf("allowlist contains unscoped tool %q", tool)
		}
	}
}

func TestNavigatorBootstrap_AllowlistShapeAndLimits(t *testing.T) {
	dir := fixtureClone(t)
	b := &bootstrapFakeBackend{}
	r := newBootstrapRunner(b, "")
	r.config.Type = BackendTypeOpenCode
	r.config.DefaultModel = "chat-model-x"
	if _, err := r.ExecuteNavigatorBootstrap(context.Background(), dir); err != nil {
		t.Fatalf("ExecuteNavigatorBootstrap: %v", err)
	}
	got := b.calls[0]

	want := append(append([]string(nil), ChatReadOnlyTools...), "Write(.agent/**)", "Edit(.agent/**)")
	if !reflect.DeepEqual(got.AllowedTools, want) {
		t.Errorf("AllowedTools = %v, want %v", got.AllowedTools, want)
	}
	if got.MaxTurns != 60 {
		t.Errorf("MaxTurns = %d, want 60", got.MaxTurns)
	}
	if got.Model != "chat-model-x" {
		t.Errorf("Model = %q, want the chat model", got.Model)
	}
	if navigatorBootstrapTimeout != 10*time.Minute {
		t.Errorf("timeout = %v, want 10m", navigatorBootstrapTimeout)
	}
	if got.ProjectPath != dir {
		t.Errorf("ProjectPath = %q, want %q", got.ProjectPath, dir)
	}
}

func TestNavigatorBootstrap_FillsContextSections(t *testing.T) {
	dir := fixtureClone(t)
	if !navigatorReadmeHasPlaceholder(dir) {
		t.Fatal("fixture README must start with placeholder sections")
	}
	assertEmbeddedReadmeLayout(t, dir)
	b := &bootstrapFakeBackend{script: fillingScript(t, dir)}

	res, err := newBootstrapRunner(b, "").ExecuteNavigatorBootstrap(context.Background(), dir)
	if err != nil {
		t.Fatalf("ExecuteNavigatorBootstrap: %v", err)
	}

	raw, err := os.ReadFile(readmePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range contextInjectionSections {
		body, ok := readmeSectionBody(string(raw), sec.header)
		if !ok {
			t.Fatalf("section %q missing after bootstrap", sec.header)
		}
		flat := strings.ToLower(strings.Join(strings.Fields(body), " "))
		for _, phrase := range navigatorPlaceholderPhrases {
			if strings.Contains(flat, phrase) {
				t.Errorf("section %q still has placeholder %q", sec.header, phrase)
			}
		}
		if !strings.Contains(body, "src/main.go") {
			t.Errorf("section %q lacks real content: %q", sec.header, body)
		}
	}
	// What the executor injects into every prompt must carry the real content.
	if ctx := loadProjectContext(filepath.Join(dir, ".agent")); strings.Contains(strings.ToLower(ctx), "fill in") {
		t.Errorf("injected project context still has placeholder text:\n%s", ctx)
	}
	if res.PlaceholdersRemain {
		t.Error("PlaceholdersRemain = true after a filling session")
	}
	wantFiles := []string{".agent/DEVELOPMENT-README.md", ".agent/knowledge/graph.json", ".agent/system/architecture.md"}
	if !reflect.DeepEqual(res.FilesWritten, wantFiles) {
		t.Errorf("FilesWritten = %v, want %v", res.FilesWritten, wantFiles)
	}
	if res.TokensInput != 1000 || res.TokensOutput != 500 || res.CostUSD <= 0 || res.Duration < 0 {
		t.Errorf("usage not returned: %+v", res)
	}
}

func TestNavigatorBootstrap_AutoDoesNotRunTwice(t *testing.T) {
	dir := fixtureClone(t)
	b := &bootstrapFakeBackend{script: fillingScript(t, dir)}
	r := newBootstrapRunner(b, "auto")

	if err := r.maybeInitNavigator(context.Background(), dir); err != nil {
		t.Fatalf("first maybeInitNavigator: %v", err)
	}
	if n := b.callCount(); n != 1 {
		t.Fatalf("first task: bootstrap calls = %d, want 1", n)
	}
	// Even with a fresh runner (daemon restart) the filled README is the guard.
	r2 := newBootstrapRunner(b, "auto")
	if err := r2.maybeInitNavigator(context.Background(), dir); err != nil {
		t.Fatalf("second maybeInitNavigator: %v", err)
	}
	if n := b.callCount(); n != 1 {
		t.Errorf("second task: bootstrap calls = %d, want still 1", n)
	}
}

func TestNavigatorBootstrap_AutoSkipsFilledReadme(t *testing.T) {
	dir := fixtureClone(t)
	if err := os.WriteFile(readmePath(dir), []byte(filledReadme(t, dir)), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &bootstrapFakeBackend{}
	if err := newBootstrapRunner(b, "auto").maybeInitNavigator(context.Background(), dir); err != nil {
		t.Fatalf("maybeInitNavigator: %v", err)
	}
	if n := b.callCount(); n != 0 {
		t.Errorf("bootstrap calls = %d, want 0 for an already filled README", n)
	}
}

func TestNavigatorBootstrap_AutoRetriesOnlyOncePerProcess(t *testing.T) {
	dir := fixtureClone(t)
	b := &bootstrapFakeBackend{} // never fills the README
	r := newBootstrapRunner(b, "auto")
	for i := 0; i < 3; i++ {
		if err := r.maybeInitNavigator(context.Background(), dir); err != nil {
			t.Fatalf("maybeInitNavigator: %v", err)
		}
	}
	if n := b.callCount(); n != 1 {
		t.Errorf("bootstrap calls = %d, want 1 (a non-filling run must not loop per task)", n)
	}
}

func TestNavigatorBootstrap_ManualAndOffDoNothingAutomatically(t *testing.T) {
	for _, mode := range []string{"", "manual", "off", "bogus"} {
		dir := fixtureClone(t)
		b := &bootstrapFakeBackend{}
		if err := newBootstrapRunner(b, mode).maybeInitNavigator(context.Background(), dir); err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if n := b.callCount(); n != 0 {
			t.Errorf("mode %q: bootstrap calls = %d, want 0", mode, n)
		}
	}
}

func TestNavigatorBootstrap_Mode(t *testing.T) {
	cases := map[string]string{
		"": "manual", "manual": "manual", "AUTO": "auto", " auto ": "auto", "off": "off", "weird": "manual",
	}
	for in, want := range cases {
		if got := (&NavigatorConfig{Bootstrap: in}).BootstrapMode(); got != want {
			t.Errorf("BootstrapMode(%q) = %q, want %q", in, got, want)
		}
	}
	if got := (*NavigatorConfig)(nil).BootstrapMode(); got != "manual" {
		t.Errorf("nil config mode = %q, want manual", got)
	}
}

func TestNavigatorBootstrap_RecordsExecutionRow(t *testing.T) {
	t.Setenv(KeySourceEnv, "platform")
	store, cleanup := setupTestStore(t)
	defer cleanup()
	dir := fixtureClone(t)
	b := &bootstrapFakeBackend{script: fillingScript(t, dir)}
	r := newBootstrapRunner(b, "")
	r.SetLogStore(store)

	res, err := r.ExecuteNavigatorBootstrap(context.Background(), dir)
	if err != nil {
		t.Fatalf("ExecuteNavigatorBootstrap: %v", err)
	}
	wantID := "bootstrap-" + time.Now().Format("2006-01-02")
	if res.TaskID != wantID {
		t.Errorf("TaskID = %q, want %q", res.TaskID, wantID)
	}
	row, err := store.GetLatestExecutionByTaskID(wantID, dir)
	if err != nil {
		t.Fatalf("no executions row for %s: %v", wantID, err)
	}
	if row.KeySource != "platform" {
		t.Errorf("key_source = %q, want platform", row.KeySource)
	}
	if row.Status != "completed" {
		t.Errorf("status = %q, want completed", row.Status)
	}

	// A second run the same day claims the next generation instead of failing.
	res2, err := r.ExecuteNavigatorBootstrap(context.Background(), dir)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res2.ExecutionID == "" || res2.ExecutionID == res.ExecutionID {
		t.Errorf("second run execution id = %q (first %q), want a new row", res2.ExecutionID, res.ExecutionID)
	}
}

func TestNavigatorBootstrap_BackendFailureIsReturned(t *testing.T) {
	dir := fixtureClone(t)
	b := &failingBootstrapBackend{}
	_, err := newBootstrapRunner(b, "").ExecuteNavigatorBootstrap(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the backend failure", err)
	}
}

type failingBootstrapBackend struct{}

func (failingBootstrapBackend) Name() string      { return "failing" }
func (failingBootstrapBackend) IsAvailable() bool { return true }
func (failingBootstrapBackend) Execute(context.Context, ExecuteOptions) (*BackendResult, error) {
	return &BackendResult{Success: false, Error: "boom"}, nil
}

func TestBuildNavigatorBootstrapPrompt(t *testing.T) {
	p := BuildNavigatorBootstrapPrompt("2026-10-14")
	for _, want := range []string{
		"Navigator research assistant",
		"Research method:",
		"Write ONLY under .agent/",
		"Never touch tracked files",
		"Never open a PR",
		"### Key Components", "## Key Files", "## Project Structure",
		".agent/system/architecture.md",
		"three to ten pattern memories",
		".agent/knowledge/memories/patterns/",
		".agent/knowledge/graph.json",
		"one concepts node per top-level package or module",
		"type: pattern",
		"2026-10-14",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "You never edit files") {
		t.Error("prompt keeps the chat persona's read-only clause")
	}
}

func TestNavigatorReadmeHasPlaceholder(t *testing.T) {
	dir := fixtureClone(t)
	if !navigatorReadmeHasPlaceholder(dir) {
		t.Error("fresh scaffold must report a placeholder")
	}
	if err := os.WriteFile(readmePath(dir), []byte(filledReadme(t, dir)), 0o644); err != nil {
		t.Fatal(err)
	}
	if navigatorReadmeHasPlaceholder(dir) {
		t.Error("filled README must not report a placeholder")
	}
	// One section left as skeleton is enough.
	raw, _ := os.ReadFile(readmePath(dir))
	text := strings.Replace(string(raw), "## Key Files\n", "## Key Files\n\n_List the files a new session should read first — fill in as the project grows._\n", 1)
	if err := os.WriteFile(readmePath(dir), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if !navigatorReadmeHasPlaceholder(dir) {
		t.Error("a single placeholder section must be detected")
	}
	if navigatorReadmeHasPlaceholder(t.TempDir()) {
		t.Error("a project without a README must not be bootstrapped automatically")
	}
}

// The Navigator plugin (8.3.5) nests "### Key Components" under
// "## Project Structure". Detection and filling must work on that layout.
func TestNavigatorReadmeHasPlaceholder_PluginNestedLayout(t *testing.T) {
	var nested strings.Builder
	nested.WriteString("# Project\n\n## Project Structure\n")
	for _, sec := range contextInjectionSections {
		if sec.header == "## Project Structure" {
			nested.WriteString(sec.body)
		}
	}
	nested.WriteString("\n### Key Components\n")
	for _, sec := range contextInjectionSections {
		if sec.header == "### Key Components" {
			nested.WriteString(sec.body)
		}
	}
	nested.WriteString("\n## Key Files\n")
	for _, sec := range contextInjectionSections {
		if sec.header == "## Key Files" {
			nested.WriteString(sec.body)
		}
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(readmePath(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readmePath(dir), []byte(nested.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if !navigatorReadmeHasPlaceholder(dir) {
		t.Fatal("nested plugin layout with skeleton text must report a placeholder")
	}

	filled := fillReadmeText(t, nested.String())
	if !strings.Contains(filled, "\n### Key Components\n") {
		t.Fatalf("filling swallowed the nested Key Components header:\n%s", filled)
	}
	if err := os.WriteFile(readmePath(dir), []byte(filled), 0o644); err != nil {
		t.Fatal(err)
	}
	if navigatorReadmeHasPlaceholder(dir) {
		t.Error("filled nested layout must not report a placeholder")
	}

	// Skeleton left only in the nested subsection is still detected.
	partial := strings.Replace(filled, "Real content for Key Components: src/main.go is the entry point.", strings.TrimSpace(contextInjectionSections[0].body), 1)
	if err := os.WriteFile(readmePath(dir), []byte(partial), 0o644); err != nil {
		t.Fatal(err)
	}
	if !navigatorReadmeHasPlaceholder(dir) {
		t.Error("placeholder left in the nested Key Components must be detected")
	}
}

// gitFixtureClone is fixtureClone committed into a fresh git repository.
func gitFixtureClone(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := fixtureClone(t)
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// GH-5688: --allowedTools is inert under --dangerously-skip-permissions, so a
// session can still change files outside .agent/ (the guard hook only covers
// the file tools). The post-session sweep puts the tree back.
func TestNavigatorBootstrap_StrayWriteReverted(t *testing.T) {
	dir := gitFixtureClone(t)
	var gotBoundary string
	b := &bootstrapFakeBackend{script: func(s *bootstrapSession) {
		gotBoundary = s.opts.WriteBoundary
		must := func(err error) {
			if err != nil {
				t.Error(err)
			}
		}
		must(os.MkdirAll(filepath.Join(dir, "newdir", "deep"), 0o755))
		must(os.WriteFile(filepath.Join(dir, "newdir", "deep", "stray.txt"), []byte("x"), 0o644))
		must(os.WriteFile(filepath.Join(dir, "src", "stray.txt"), []byte("stray"), 0o644))
		must(os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module hacked\n"), 0o644))
		must(os.Remove(filepath.Join(dir, "src", "main.go")))
		s.write(".agent/system/architecture.md", "# Architecture\n")
	}}

	res, err := newBootstrapRunner(b, "").ExecuteNavigatorBootstrap(context.Background(), dir)
	if err != nil {
		t.Fatalf("ExecuteNavigatorBootstrap: %v", err)
	}

	if gotBoundary != ".agent" {
		t.Errorf("WriteBoundary = %q, want .agent", gotBoundary)
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "stray.txt")); !os.IsNotExist(err) {
		t.Errorf("src/stray.txt still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "newdir")); !os.IsNotExist(err) {
		t.Errorf("newdir/ (only holding a stray file) still exists: %v", err)
	}
	if mod, _ := os.ReadFile(filepath.Join(dir, "go.mod")); string(mod) != "module example.com/legacy\n\ngo 1.21\n" {
		t.Errorf("go.mod not restored: %q", mod)
	}
	if main, _ := os.ReadFile(filepath.Join(dir, "src", "main.go")); string(main) != "package main\n" {
		t.Errorf("deleted src/main.go not restored: %q", main)
	}
	want := []string{"go.mod", "newdir/deep/stray.txt", "src/main.go", "src/stray.txt"}
	if !reflect.DeepEqual(res.StrayWrites, want) {
		t.Errorf("StrayWrites = %v, want %v", res.StrayWrites, want)
	}
	if want := []string{".agent/system/architecture.md"}; !reflect.DeepEqual(res.FilesWritten, want) {
		t.Errorf("FilesWritten = %v, want %v (Navigator writes must survive the sweep)", res.FilesWritten, want)
	}
	if _, err := os.Stat(filepath.Join(dir, ".agent", "system", "architecture.md")); err != nil {
		t.Errorf("Navigator write was swept: %v", err)
	}
}

// The sweep must not discard the developer's own uncommitted work, which
// predates the session.
func TestNavigatorBootstrap_SweepKeepsPreexistingChanges(t *testing.T) {
	dir := gitFixtureClone(t)
	const mine = "module example.com/legacy\n\ngo 1.22 // mine\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &bootstrapFakeBackend{}
	res, err := newBootstrapRunner(b, "").ExecuteNavigatorBootstrap(context.Background(), dir)
	if err != nil {
		t.Fatalf("ExecuteNavigatorBootstrap: %v", err)
	}
	if len(res.StrayWrites) != 0 {
		t.Errorf("StrayWrites = %v, want none", res.StrayWrites)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "go.mod")); string(got) != mine {
		t.Errorf("pre-existing change to go.mod was reverted: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "notes.txt")); string(got) != "mine" {
		t.Errorf("pre-existing untracked file was removed: %q", got)
	}
}
