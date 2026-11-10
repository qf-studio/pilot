package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// readGuardModes are the two ways the guard script parses the hook JSON: jq
// when it is on PATH, otherwise python3.
var readGuardModes = []string{"jq", "python3"}

// readGuardPath builds a PATH dir holding only what the guard script runs:
// cat, realpath and the one JSON tool of the mode, so the jq run really has no
// python3 to fall back on and the python3 run has no jq.
func readGuardPath(t *testing.T, mode string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("guard script is POSIX sh")
	}
	dir := t.TempDir()
	for _, bin := range []string{"cat", "realpath", mode} {
		src, err := exec.LookPath(bin)
		if err != nil {
			t.Skipf("%s not installed", bin)
		}
		if err := os.Symlink(src, filepath.Join(dir, bin)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runReadGuard runs the guard script in dir with the given PATH and HOME and
// returns its exit code and stderr.
func runReadGuard(t *testing.T, script, pathDir, home, dir, pilotHome, cfgPath string, stdin []byte) (int, string) {
	t.Helper()
	cmd := exec.Command(script, pilotHome, cfgPath)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + pathDir, "HOME=" + home, "PWD=" + dir}
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), stderr.String()
	default:
		t.Fatalf("run read guard: %v", err)
		return -1, ""
	}
}

func bashHook(command string) map[string]any {
	return map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": command}}
}

func grepHook(path, pattern string) map[string]any {
	in := map[string]any{"pattern": pattern}
	if path != "" {
		in["path"] = path
	}
	return map[string]any{"tool_name": "Grep", "tool_input": in}
}

func globHook(path, pattern string) map[string]any {
	in := map[string]any{"pattern": pattern}
	if path != "" {
		in["path"] = path
	}
	return map[string]any{"tool_name": "Glob", "tool_input": in}
}

func TestReadGuardScript(t *testing.T) {
	for _, mode := range readGuardModes {
		t.Run(mode, func(t *testing.T) { testReadGuardScript(t, mode) })
	}
}

func testReadGuardScript(t *testing.T, mode string) {
	pathDir := readGuardPath(t, mode)

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
	if err := os.MkdirAll(filepath.Join(worktree, ".pilot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".pilot", "workflow.yaml"), []byte("steps: []\n"), 0o644); err != nil {
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

	run := func(t *testing.T, hook map[string]any, pilotHome, cfgPath string) (int, string) {
		t.Helper()
		raw, err := json.Marshal(hook)
		if err != nil {
			t.Fatal(err)
		}
		got, stderr := runReadGuard(t, script, pathDir, home, worktree, pilotHome, cfgPath, raw)
		if tool, _ := hook["tool_name"].(string); !matcher.MatchString(tool) {
			got = 0
		}
		return got, stderr
	}

	tests := []struct {
		name string
		hook map[string]any
		want int
	}{
		// Path inputs of the file tools.
		{"read config", writeHook("Read", "file_path", cfgPath), 2},
		{"read through file symlink", writeHook("Read", "file_path", link), 2},
		{"read through dir symlink", writeHook("Read", "file_path", filepath.Join(dirLink, "config.yaml")), 2},
		{"read missing file under home", writeHook("Read", "file_path", filepath.Join(pilotHome, "new", "x")), 2},
		{"read missing dir under home via dotdot", writeHook("Read", "file_path", filepath.Join(worktree, "nope", "..", "..", filepath.Base(home), ".pilot", "config.yaml")), 2},
		{"grep path in home", grepHook(pilotHome, "x"), 2},
		{"glob path in home", globHook(pilotHome, "*"), 2},
		{"notebook in home", writeHook("NotebookRead", "notebook_path", filepath.Join(pilotHome, "n.ipynb")), 2},
		{"tilde path", writeHook("Read", "file_path", "~/.pilot/config.yaml"), 2},
		{"read proc self environ", writeHook("Read", "file_path", "/proc/self/environ"), 2},
		// Ancestor rule: a recursive read of a parent of the Pilot home.
		{"grep path is the user home", grepHook(home, "bot_token"), 2},
		{"grep path is tilde", grepHook("~", "bot_token"), 2},
		{"glob path is the user home", globHook(home, "**/*.yaml"), 2},
		{"grep path is root", grepHook("/", "bot_token"), 2},
		{"read the user home directory itself", writeHook("Read", "file_path", home), 0},
		// Pattern and glob text is never a path.
		{"glob pattern into home", globHook("", pilotHome+"/*"), 0},
		{"grep pattern is the home path", grepHook(worktree, pilotHome), 0},
		{"grep pattern is a tilde path", grepHook("", "~/.pilot/config"), 0},
		{"grep with glob into home", map[string]any{"tool_name": "Grep", "tool_input": map[string]any{"pattern": "x", "glob": pilotHome + "/*"}}, 0},
		{"grep path in worktree", grepHook(worktree, "func main"), 0},
		{"grep path is tmp", grepHook("/tmp", "x"), 0},
		{"grep without path", grepHook("", "func main"), 0},
		{"read in worktree", writeHook("Read", "file_path", filepath.Join(worktree, "main.go")), 0},
		{"read relative", writeHook("Read", "file_path", "main.go"), 0},
		{"read worktree .pilot/workflow.yaml", writeHook("Read", "file_path", filepath.Join(worktree, ".pilot", "workflow.yaml")), 0},
		{"read relative .pilot/workflow.yaml", writeHook("Read", "file_path", ".pilot/workflow.yaml"), 0},
		{"read new file through missing dir", writeHook("Read", "file_path", "newdir/../new.go"), 0},
		{"read new file through deeper missing dir", writeHook("Read", "file_path", "a/b/../../new.go"), 0},

		// Bash: reads of the Pilot home.
		{"incident: cat tilde config", bashHook("cat ~/.pilot/config.yaml"), 2},
		{"bash cat absolute config", bashHook("cat " + cfgPath), 2},
		{"bash ls home", bashHook("ls " + pilotHome), 2},
		{"bash ls quoted HOME", bashHook(`ls "$HOME"/.pilot/logs`), 2},
		{"bash ls braced HOME", bashHook("ls ${HOME}/.pilot/logs"), 2},
		{"bash cat HOME", bashHook("cat $HOME/.pilot/config.yaml"), 2},
		{"bash cd tilde then relative", bashHook("cd ~ && cat .pilot/recordings/x"), 2},
		{"bash cd home then relative", bashHook("cd " + home + "; tail -n 5 .pilot/logs/daemon.log"), 2},
		{"bash second command reads", bashHook("echo hi; cat ~/.pilot/config.yaml"), 2},
		{"bash piped read", bashHook("cat ~/.pilot/config.yaml | head -n 3"), 2},
		{"bash command substitution", bashHook("echo $(cat ~/.pilot/config.yaml)"), 2},
		{"bash through symlink", bashHook("cat homelink/config.yaml"), 2},
		{"bash glob into home", bashHook("cat ~/.pilot/*"), 2},
		{"bash glob reaching home", bashHook("cat ~/.p*/config.yaml"), 2},
		{"bash redirect from config", bashHook("wc -c < ~/.pilot/config.yaml"), 2},
		{"bash env assignment", bashHook("LC_ALL=C cat ~/.pilot/config.yaml"), 2},
		{"bash sudo", bashHook("sudo -n cat ~/.pilot/config.yaml"), 2},
		{"bash grep file in home", bashHook("grep bot_token ~/.pilot/config.yaml"), 2},
		{"bash grep -e pattern then home", bashHook("grep -e bot_token ~/.pilot/config.yaml"), 2},
		// Bash: process environments.
		{"bash proc environ", bashHook("cat /proc/123/environ"), 2},
		{"bash proc self environ", bashHook("tr '\\0' '\\n' < /proc/self/environ"), 2},
		// Bash: recursive reads of a parent of the Pilot home.
		{"bash grep -r tilde", bashHook("grep -r bot_token ~"), 2},
		{"bash grep -rn HOME", bashHook("grep -rn bot_token $HOME"), 2},
		{"bash grep -R absolute home", bashHook("grep -R bot_token " + home), 2},
		{"bash grep --recursive", bashHook("grep --recursive bot_token ~"), 2},
		{"bash grep -d recurse", bashHook("grep -d recurse bot_token ~"), 2},
		{"bash rg tilde", bashHook("rg bot_token ~"), 2},
		{"bash find tilde", bashHook("find ~ -name config.yaml"), 2},
		{"bash find root", bashHook("find / -name config.yaml"), 2},
		{"bash ls -R tilde", bashHook("ls -R ~"), 2},
		{"bash ls -lR HOME", bashHook(`ls -lR "$HOME"`), 2},
		{"bash cat glob on home", bashHook("cat ~/*"), 2},
		{"bash cd home then grep -r", bashHook("cd ~ && grep -rn bot_token"), 2},
		{"bash cd home then grep -r dot", bashHook("cd ~ && grep -rn bot_token ."), 2},
		{"bash cd home then star", bashHook("cd " + home + " && cat *"), 2},

		// Bash: text that only mentions the path.
		{"bash grep -rn dotpilot config pattern", bashHook(`grep -rn ".pilot/config" internal/`), 0},
		{"bash grep -rn tilde pattern", bashHook(`grep -rn "~/.pilot" docs/`), 0},
		{"bash grep -rn absolute home pattern", bashHook(`grep -rn "` + pilotHome + `" docs/`), 0},
		{"bash grep -e tilde pattern", bashHook(`grep -rn -e "~/.pilot" docs/`), 0},
		{"bash grep --regexp tilde pattern", bashHook(`grep -rn --regexp "~/.pilot" docs/`), 0},
		{"bash rg -e tilde pattern", bashHook(`rg -e "~/.pilot/config" src/`), 0},
		{"bash rg tilde pattern", bashHook(`rg '$HOME/.pilot' src/`), 0},
		{"bash git commit -m tilde", bashHook(`git commit -m "docs: explain ~/.pilot layout"`), 0},
		{"bash git commit -m absolute home", bashHook(`git commit -m "docs: explain ` + pilotHome + ` layout"`), 0},
		{"bash git commit --message", bashHook(`git commit --message "see ~/.pilot/config.yaml"`), 0},
		{"bash git commit --message=", bashHook(`git commit --message="see ~/.pilot/config.yaml"`), 0},
		{"bash git commit heredoc", bashHook("git commit -m \"$(cat <<'EOF'\nfix: stop reading ~/.pilot/config.yaml\nEOF\n)\""), 0},
		{"bash echo quoted sentence", bashHook(`echo "see ~/.pilot/config.yaml for the layout"`), 0},
		{"bash proc cpuinfo then grep environ", bashHook("cat /proc/cpuinfo; grep -n environ main.go"), 0},
		{"bash proc status", bashHook("cat /proc/self/status"), 0},
		{"bash proc environment word", bashHook("grep -rn /proc/self/environ internal/"), 0},
		// Bash: ordinary work.
		{"bash go test", bashHook("go test ./..."), 0},
		{"bash cat .pilot/workflow.yaml", bashHook("cat .pilot/workflow.yaml"), 0},
		{"bash cat absolute worktree .pilot", bashHook("cat " + worktree + "/.pilot/workflow.yaml"), 0},
		{"bash cat new file through missing dir", bashHook("cat newdir/../new.go"), 0},
		{"bash grep -rn in repo", bashHook("grep -rn func ."), 0},
		{"bash grep -r without path", bashHook("grep -r func"), 0},
		{"bash grep -r in tmp", bashHook("grep -r func /tmp"), 0},
		{"bash rg in repo", bashHook("rg func internal/"), 0},
		{"bash find in repo", bashHook("find . -name '*.go' -path '*/.pilot/*'"), 0},
		{"bash ls repo -R", bashHook("ls -R internal"), 0},
		{"bash ls home not recursive", bashHook("ls ~"), 0},
		{"bash cd home alone", bashHook("cd ~"), 0},
		{"bash cat repo glob", bashHook("cat internal/*/*.go"), 0},
		{"bash cd repo then cat", bashHook("cd internal && cat executor/read_guard.go"), 0},
		{"bash cd dash", bashHook("cd - && ls"), 0},
		{"bash redirect to repo file", bashHook("echo ok > out.txt 2>&1"), 0},
		{"bash comment", bashHook("ls # cat ~/.pilot/config.yaml"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, stderr := run(t, tt.hook, spec.Home, spec.ConfigPath)
			if got != tt.want {
				t.Fatalf("exit = %d, want %d (stderr %q)", got, tt.want, stderr)
			}
			if tt.want == 2 && !strings.Contains(stderr, "read-guard") {
				t.Errorf("stderr %q does not name the rule", stderr)
			}
			if tt.want == 0 && stderr != "" {
				t.Errorf("allowed call wrote to stderr: %q", stderr)
			}
		})
	}

	// ~user: the home of an existing user, taken from the passwd database.
	t.Run("tilde user", func(t *testing.T) {
		u, err := user.Current()
		if err != nil || u.Username == "" || u.HomeDir == "" || strings.ContainsAny(u.Username, "\\/ ") {
			t.Skip("no usable current user")
		}
		out, err := exec.Command("sh", "-c", "printf %s ~"+u.Username).Output()
		if err != nil || string(out) != u.HomeDir {
			t.Skipf("sh does not expand ~%s to %s", u.Username, u.HomeDir)
		}
		userPilot := filepath.Join(u.HomeDir, ".pilot")
		for command, want := range map[string]int{
			"tail ~" + u.Username + "/.pilot/logs/daemon.log": 2,
			"grep -r bot_token ~" + u.Username:                2,
			"tail ~" + u.Username + "/notes.txt":              0,
		} {
			got, stderr := run(t, bashHook(command), userPilot, filepath.Join(userPilot, "config.yaml"))
			if got != want {
				t.Errorf("%q: exit = %d, want %d (stderr %q)", command, got, want, stderr)
			}
		}
	})

	// The Pilot home itself a symlink (the founder box keeps it on another
	// volume): the parent rule follows the configured path, the inside rule the
	// resolved one.
	t.Run("symlinked pilot home", func(t *testing.T) {
		userHome := t.TempDir()
		realHome := filepath.Join(t.TempDir(), "pilot-home")
		if err := os.MkdirAll(realHome, 0o755); err != nil {
			t.Fatal(err)
		}
		linked := filepath.Join(userHome, ".pilot")
		if err := os.Symlink(realHome, linked); err != nil {
			t.Fatal(err)
		}
		for command, want := range map[string]int{
			"grep -r bot_token " + userHome:               2,
			"grep -r bot_token " + filepath.Dir(realHome): 2,
			"cat " + linked + "/config.yaml":              2,
			"cat " + realHome + "/config.yaml":            2,
			"grep -rn x internal/":                        0,
		} {
			raw, err := json.Marshal(bashHook(command))
			if err != nil {
				t.Fatal(err)
			}
			got, stderr := runReadGuard(t, script, pathDir, userHome, worktree, linked, filepath.Join(linked, "config.yaml"), raw)
			if got != want {
				t.Errorf("%q: exit = %d, want %d (stderr %q)", command, got, want, stderr)
			}
		}
	})

	t.Run("unparseable input fails closed with one stderr line", func(t *testing.T) {
		got, stderr := runReadGuard(t, script, pathDir, home, worktree, spec.Home, spec.ConfigPath, []byte("{not json"))
		if got != 2 {
			t.Fatalf("exit = %d, want 2", got)
		}
		if !strings.HasPrefix(stderr, "read blocked: cannot parse hook input") || strings.Count(stderr, "\n") != 1 {
			t.Errorf("stderr = %q, want only the deny line", stderr)
		}
	})
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

func TestReadGuardPreflight(t *testing.T) {
	t.Cleanup(func() { readGuardOffForProcess.Store(false) })
	has := func(tools ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, tool := range tools {
				if tool == name {
					return "/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	off := false
	tests := []struct {
		name        string
		cfg         *ClaudeCodeConfig
		lookPath    func(string) (string, error)
		wantMissing []string
		wantOff     bool
	}{
		{"all tools, jq", nil, has("realpath", "jq"), nil, false},
		{"all tools, python3", nil, has("realpath", "python3"), nil, false},
		{"no realpath", nil, has("jq", "python3"), []string{"realpath"}, true},
		{"neither jq nor python3", nil, has("realpath"), []string{"jq or python3"}, true},
		{"nothing", nil, has(), []string{"realpath", "jq or python3"}, true},
		{"guard off in config", &ClaudeCodeConfig{ReadGuard: &off}, has(), nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readGuardOffForProcess.Store(false)
			if got := readGuardMissingTools(tt.lookPath); strings.Join(got, ",") != strings.Join(tt.wantMissing, ",") && tt.cfg == nil {
				t.Errorf("missing = %v, want %v", got, tt.wantMissing)
			}
			if got := preflightReadGuard(tt.cfg, tt.lookPath); got != tt.wantOff {
				t.Errorf("turned off = %v, want %v", got, tt.wantOff)
			}
			// Off for the process means every config reads as off, and no run
			// gets --settings.
			if enabled := tt.cfg.ReadGuardEnabled(); tt.wantOff && enabled {
				t.Error("ReadGuardEnabled is still true after the preflight turned the guard off")
			}
			if tt.wantOff {
				args, cleanup, err := withReadGuard(nil, []string{"-p"})
				defer cleanup()
				if err != nil || len(args) != 1 {
					t.Errorf("withReadGuard after preflight = %v, %v; want the args unchanged", args, err)
				}
			}
		})
	}
}
