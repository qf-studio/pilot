package executor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func readGuardPrereqs(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("guard script is POSIX sh")
	}
	for _, bin := range []string{"jq", "realpath"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
}

func bashHook(command string) map[string]any {
	return map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": command}}
}

func TestReadGuardScript(t *testing.T) {
	readGuardPrereqs(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	pilotHome := filepath.Join(home, ".pilot")
	if err := os.MkdirAll(pilotHome, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(pilotHome, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("secret: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(worktree, "innocent.yaml")
	if err := os.Symlink(cfgPath, link); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(worktree, "homelink")
	if err := os.Symlink(pilotHome, dirLink); err != nil {
		t.Fatal(err)
	}

	spec := (&ClaudeCodeConfig{}).readGuardSpec()
	if spec == nil || spec.Home != pilotHome || spec.ConfigPath != cfgPath {
		t.Fatalf("spec = %+v, want home %q config %q", spec, pilotHome, cfgPath)
	}
	settings, cleanup, err := setupRunGuards(worktree, "", spec)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	script := filepath.Join(filepath.Dir(settings), readGuardName)

	// Claude Code only runs the hook for tools the settings matcher covers, so
	// a tool outside the matcher is effectively allowed.
	rawSettings, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(rawSettings, &parsed); err != nil || len(parsed.Hooks.PreToolUse) != 1 {
		t.Fatalf("settings: %v %s", err, rawSettings)
	}
	matcher := regexp.MustCompile("^(" + parsed.Hooks.PreToolUse[0].Matcher + ")$")

	tests := []struct {
		name string
		hook map[string]any
		want int
	}{
		{"read config", writeHook("Read", "file_path", cfgPath), 2},
		{"read through file symlink", writeHook("Read", "file_path", link), 2},
		{"read through dir symlink", writeHook("Read", "file_path", filepath.Join(dirLink, "config.yaml")), 2},
		{"read missing file under home", writeHook("Read", "file_path", filepath.Join(pilotHome, "new", "x")), 2},
		{"grep path in home", writeHook("Grep", "path", pilotHome), 2},
		{"glob path in home", writeHook("Glob", "path", pilotHome), 2},
		{"notebook in home", writeHook("NotebookRead", "notebook_path", filepath.Join(pilotHome, "n.ipynb")), 2},
		{"tilde path", writeHook("Read", "file_path", "~/.pilot/config.yaml"), 2},
		{"glob pattern into home", map[string]any{"tool_name": "Glob", "tool_input": map[string]any{"pattern": pilotHome + "/*"}}, 2},
		{"bash cat tilde config", bashHook("cat ~/.pilot/config.yaml"), 2},
		{"bash cat absolute config", bashHook("cat " + cfgPath), 2},
		{"bash ls home", bashHook("ls " + pilotHome), 2},
		{"bash proc environ", bashHook("cat /proc/123/environ"), 2},
		{"bash proc self environ", bashHook("tr '\\0' '\\n' < /proc/self/environ"), 2},
		{"bash proc star environ", bashHook("cat /proc/*/environ"), 2},
		{"bash proc digit glob environ", bashHook("cat /proc/[0-9]*/environ"), 2},
		{"read in worktree", writeHook("Read", "file_path", filepath.Join(worktree, "main.go")), 0},
		{"read relative", writeHook("Read", "file_path", "main.go"), 0},
		{"grep without path", map[string]any{"tool_name": "Grep", "tool_input": map[string]any{"pattern": "func main"}}, 0},
		{"bash go test", bashHook("go test ./..."), 0},
		{"bash proc status", bashHook("cat /proc/self/status"), 0},
		{"bash mentions .pilot/workflow.yaml", bashHook("cat .pilot/workflow.yaml"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, stderr := runGuard(t, script, spec.Home, spec.ConfigPath, tt.hook)
			if tool, _ := tt.hook["tool_name"].(string); !matcher.MatchString(tool) {
				got = 0
			}
			if got != tt.want {
				t.Fatalf("exit = %d, want %d (stderr %q)", got, tt.want, stderr)
			}
			if tt.want == 2 && !strings.Contains(stderr, "read-guard") {
				t.Errorf("stderr %q does not name the rule", stderr)
			}
		})
	}

	t.Run("unparseable input fails closed", func(t *testing.T) {
		cmd := exec.Command(script, spec.Home, spec.ConfigPath)
		cmd.Stdin = strings.NewReader("{not json")
		err := cmd.Run()
		var exitErr *exec.ExitError
		if err == nil || !asExit(err, &exitErr) || exitErr.ExitCode() != 2 {
			t.Fatalf("err = %v, want exit 2", err)
		}
	})
}

// mutation pin: the `*/proc/*environ*` branch of the read guard (read_guard.go:115) denies globbed process-environment reads -> this test fails
// when that branch is removed or narrowed to literal PIDs. It pins the current substring matcher, not a glob-reach fix (#6287).
func TestReadGuardProcEnvironGlob(t *testing.T) {
	readGuardPrereqs(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	spec := &readGuardSpec{Home: filepath.Join(home, ".pilot"), ConfigPath: filepath.Join(home, ".pilot", "config.yaml")}
	settings, cleanup, err := setupRunGuards(t.TempDir(), "", spec)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	script := filepath.Join(filepath.Dir(settings), readGuardName)

	for _, command := range []string{
		"cat /proc/*/environ",
		"cat /proc/[0-9]*/environ",
		"cat /proc/?/environ",
		"cat /proc/*/environ*",
	} {
		got, stderr := runGuard(t, script, spec.Home, spec.ConfigPath, bashHook(command))
		if got != 2 || !strings.Contains(stderr, "process environment") {
			t.Errorf("%q: exit = %d, stderr %q; want exit 2 naming the process environment", command, got, stderr)
		}
	}
	if got, stderr := runGuard(t, script, spec.Home, spec.ConfigPath, bashHook("cat /proc/*/status")); got != 0 {
		t.Errorf("/proc/*/status: exit = %d, want 0 (stderr %q)", got, stderr)
	}
}

// mutation pin: the python3 fallback of the read guard (read_guard.go:50-55) parses hook input when jq is missing -> this test fails
// when the elif python3 branch is removed (every call then dies with "neither jq nor python3").
func TestReadGuardScriptPython3Fallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard script is POSIX sh")
	}
	// A PATH holding only what the script needs, minus jq.
	bin := t.TempDir()
	for _, name := range []string{"python3", "realpath", "cat"} {
		src, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("%s not installed", name)
		}
		if err := os.Symlink(src, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	pilotHome := filepath.Join(home, ".pilot")
	if err := os.MkdirAll(pilotHome, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(pilotHome, "config.yaml")
	worktree := t.TempDir()
	spec := &readGuardSpec{Home: pilotHome, ConfigPath: cfgPath}
	settings, cleanup, err := setupRunGuards(worktree, "", spec)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	script := filepath.Join(filepath.Dir(settings), readGuardName)
	t.Setenv("PATH", bin)

	tests := []struct {
		name string
		hook map[string]any
		want int
		msg  string
	}{
		{"read config", writeHook("Read", "file_path", cfgPath), 2, "inside the Pilot home"},
		{"bash cat config", bashHook("cat " + cfgPath), 2, "touches the Pilot home"},
		{"glob pattern into home", map[string]any{"tool_name": "Glob", "tool_input": map[string]any{"pattern": pilotHome + "/*"}}, 2, "points into the Pilot home"},
		{"bash proc star environ", bashHook("cat /proc/*/environ"), 2, "process environment"},
		{"read in worktree", writeHook("Read", "file_path", filepath.Join(worktree, "main.go")), 0, ""},
		{"bash go test", bashHook("go test ./..."), 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, stderr := runGuard(t, script, spec.Home, spec.ConfigPath, tt.hook)
			if got != tt.want {
				t.Fatalf("exit = %d, want %d (stderr %q)", got, tt.want, stderr)
			}
			if tt.msg != "" && !strings.Contains(stderr, tt.msg) {
				t.Errorf("stderr %q does not contain %q", stderr, tt.msg)
			}
		})
	}
}

func asExit(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

func TestReadGuardSpec(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	disabled := false
	if (&ClaudeCodeConfig{ReadGuard: &disabled}).readGuardSpec() != nil {
		t.Error("read_guard: false still produced a spec")
	}
	if (*ClaudeCodeConfig)(nil).readGuardSpec() == nil {
		t.Error("nil config should enable the guard")
	}
	got := (&ClaudeCodeConfig{PilotHome: "/srv/pilot/", ConfigPath: "/etc/pilot.yaml"}).readGuardSpec()
	if got.Home != "/srv/pilot" || got.ConfigPath != "/etc/pilot.yaml" {
		t.Errorf("explicit spec = %+v", got)
	}
}

// TestReadGuardSettingsWithWriteBoundary checks that a run gets one settings
// file carrying both hooks when a write boundary is set, and only the read
// guard otherwise.
func TestReadGuardSettingsWithWriteBoundary(t *testing.T) {
	spec := &readGuardSpec{Home: "/home/x/.pilot", ConfigPath: "/home/x/.pilot/config.yaml"}
	type hooks struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	load := func(boundary string) hooks {
		path, cleanup, err := setupRunGuards("/proj", boundary, spec)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var h hooks
		if err := json.Unmarshal(raw, &h); err != nil {
			t.Fatalf("settings not JSON: %v", err)
		}
		return h
	}

	both := load(".agent").Hooks.PreToolUse
	if len(both) != 2 {
		t.Fatalf("entries = %d, want 2", len(both))
	}
	if both[0].Matcher != "Read|Grep|Glob|NotebookRead|Bash" || !strings.Contains(both[0].Hooks[0].Command, readGuardName) ||
		!strings.Contains(both[0].Hooks[0].Command, spec.Home) || !strings.Contains(both[0].Hooks[0].Command, spec.ConfigPath) {
		t.Errorf("read entry = %+v", both[0])
	}
	if both[1].Matcher != writeBoundaryMatcher || !strings.Contains(both[1].Hooks[0].Command, writeBoundaryGuardName) {
		t.Errorf("write entry = %+v", both[1])
	}
	if only := load("").Hooks.PreToolUse; len(only) != 1 || only[0].Matcher != readGuardMatcher {
		t.Errorf("read-only entries = %+v", only)
	}
}

// TestReadGuardBackendPassesSettingsOnEveryRun checks that a plain ticket run
// (no write boundary) still gets --settings with the read guard, and that
// read_guard: false removes it.
func TestReadGuardBackendPassesSettingsOnEveryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI test relies on shell scripts")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	argsFile := filepath.Join(tmp, "args")
	cli := filepath.Join(tmp, "fake-claude")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + argsFile + "\nexit 0\n"
	if err := os.WriteFile(cli, []byte(body), 0o755); err != nil { //nolint:gosec // fake CLI must be executable
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := func(cfg *ClaudeCodeConfig) string {
		b := NewClaudeCodeBackend(cfg)
		if _, err := b.Execute(ctx, ExecuteOptions{Prompt: "hi", ProjectPath: t.TempDir(), EventHandler: func(BackendEvent) {}}); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		raw, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	if args := run(&ClaudeCodeConfig{Command: cli}); !strings.Contains(args, "--settings") {
		t.Errorf("default run has no --settings: %q", args)
	}
	off := false
	if args := run(&ClaudeCodeConfig{Command: cli, ReadGuard: &off}); strings.Contains(args, "--settings") {
		t.Errorf("read_guard: false run still has --settings: %q", args)
	}
}

// TestReadGuardEpicPlanningRun checks that the epic planning spawn, which has
// Read/Grep/Glob, gets the read guard hook too, and that read_guard: false
// removes it.
func TestReadGuardEpicPlanningRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI test relies on shell scripts")
	}
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	capture := filepath.Join(tmp, "captured")
	cli := filepath.Join(tmp, "fake-claude")
	body := `#!/bin/sh
printf '%s\n' "$*" > ` + capture + `.args
prev=""
for a in "$@"; do
  if [ "$prev" = "--settings" ]; then cp "$a" ` + capture + `.settings; fi
  prev=$a
done
printf '1. **feat(a): one** - first\n2. **feat(b): two** - second\n'
`
	if err := os.WriteFile(cli, []byte(body), 0o755); err != nil { //nolint:gosec // fake CLI must be executable
		t.Fatal(err)
	}
	plan := func(cfg *ClaudeCodeConfig) {
		_ = os.Remove(capture + ".args")
		_ = os.Remove(capture + ".settings")
		r := newTestRunner(cli)
		r.config.ClaudeCode = cfg
		cfg.Command = cli
		task := &Task{ID: "GH-1", Title: "feat(x): epic", Description: "d", ProjectPath: tmp}
		if _, err := r.PlanEpic(context.Background(), task, tmp); err != nil {
			t.Fatalf("PlanEpic: %v", err)
		}
	}

	plan(&ClaudeCodeConfig{})
	args, err := os.ReadFile(capture + ".args")
	if err != nil || !strings.Contains(string(args), "--settings") {
		t.Fatalf("planning run has no --settings: %q (%v)", args, err)
	}
	settings, err := os.ReadFile(capture + ".settings")
	if err != nil || !strings.Contains(string(settings), readGuardName) {
		t.Errorf("planning settings lack the read guard: %q (%v)", settings, err)
	}

	off := false
	plan(&ClaudeCodeConfig{ReadGuard: &off})
	args, _ = os.ReadFile(capture + ".args")
	if strings.Contains(string(args), "--settings") {
		t.Errorf("read_guard: false planning run still has --settings: %q", args)
	}
}
