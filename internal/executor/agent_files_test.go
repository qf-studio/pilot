package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TASK-651 (design 4.2): the files the daemon writes into the workspace of a
// run go through the helpers of agent_files.go. No test needs root. The
// fixture shows the boundary: the workspace root is not writable for the test
// process, and the mock sudo makes it writable only while its command runs.

// agentFilesFixture is a workspace root the test process cannot write, one
// workspace in it, the option set for that root, and the mock sudo of
// mockSudoWritable.
type agentFilesFixture struct {
	root, ws string
	sudoLog  string
}

// mockSudoWritable puts a fake sudo first on PATH that appends its argv (one
// line per call) to the returned file, makes root writable, runs the command
// after "--" as the present user and makes root read-only again. extra is
// shell code that runs before the command (its arguments are "$@").
func mockSudoWritable(t *testing.T, root, extra string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "sudo-calls")
	body := fmt.Sprintf(`#!/bin/sh
echo "$*" >> '%[1]s'
while [ $# -gt 0 ]; do
  a=$1
  shift
  [ "$a" = "--" ] && break
done
%[3]s
chmod -R u+w '%[2]s'
"$@"
rc=$?
chmod -R a-w '%[2]s'
exit $rc
`, logPath, root, extra)
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(body), 0o755); err != nil { //nolint:gosec // mock must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// newAgentFilesFixture builds the fixture with the workspace still writable:
// seed it, then call lock.
func newAgentFilesFixture(t *testing.T, extra string) *agentFilesFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock sudo is a shell script")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the mode bits the fixture relies on")
	}
	stubRunAsGOOS(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Runs before the removal of the TempDir (cleanups run last in, first out).
	t.Cleanup(func() { _ = exec.Command("chmod", "-R", "u+w", root).Run() })
	ws := filepath.Join(root, agentWorkspacePrefix+"0123abcd-GH-1-1")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &agentFilesFixture{root: root, ws: ws}
	f.sudoLog = mockSudoWritable(t, root, extra)
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")
	return f
}

// lock makes the workspace root read-only for the test process.
func (f *agentFilesFixture) lock(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("chmod", "-R", "a-w", f.root).CombinedOutput(); err != nil {
		t.Fatalf("chmod: %v: %s", err, out)
	}
}

// asAgent runs fn with the workspace writable, as the session of the agent
// user would write it.
func (f *agentFilesFixture) asAgent(t *testing.T, fn func()) {
	t.Helper()
	if out, err := exec.Command("chmod", "-R", "u+w", f.root).CombinedOutput(); err != nil {
		t.Fatalf("chmod: %v: %s", err, out)
	}
	fn()
	f.lock(t)
}

func (f *agentFilesFixture) calls(t *testing.T) []string { return sudoCalls(t, f.sudoLog) }

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func assertNoPath(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s exists (err %v), want it absent", path, err)
	}
}

// TestAgentWriteFileBoundary is items 1 and 5 for the write helper: inside the
// root the write runs as the agent user with the data on standard input; a
// direct os.WriteFile into the workspace fails in the fixture (control case);
// outside the root and with the option empty it is the os call; an error of
// agentCommand and a link out of the workspace are errors without a write.
func TestAgentWriteFileBoundary(t *testing.T) {
	const data = "content-on-stdin-7f3a"
	tests := []struct {
		name string
		// run gets the fixture (locked) and an outside directory; it returns
		// the path written and the error.
		run       func(t *testing.T, f *agentFilesFixture, outside string) (string, error)
		wantErr   string // "" is success
		wantData  bool
		wantSudo  int
		forbidden bool // the path must not exist afterwards
	}{
		{
			name: "control: direct os.WriteFile into the workspace fails",
			run: func(t *testing.T, f *agentFilesFixture, _ string) (string, error) {
				p := filepath.Join(f.ws, "direct.txt")
				return p, os.WriteFile(p, []byte(data), 0o644)
			},
			wantErr: "permission denied", forbidden: true,
		},
		{
			name: "inside the root: as the agent user",
			run: func(t *testing.T, f *agentFilesFixture, _ string) (string, error) {
				p := filepath.Join(f.ws, "a.txt")
				return p, agentWriteFile(context.Background(), p, []byte(data), 0o644)
			},
			wantData: true, wantSudo: 1,
		},
		{
			name: "inside the root, replaces an existing file",
			run: func(t *testing.T, f *agentFilesFixture, _ string) (string, error) {
				p := filepath.Join(f.ws, "seeded.txt")
				return p, agentWriteFile(context.Background(), p, []byte(data), 0o644)
			},
			wantData: true, wantSudo: 1,
		},
		{
			name: "outside the root: the os call",
			run: func(t *testing.T, _ *agentFilesFixture, outside string) (string, error) {
				p := filepath.Join(outside, "b.txt")
				return p, agentWriteFile(context.Background(), p, []byte(data), 0o644)
			},
			wantData: true,
		},
		{
			name: "option empty: the os call, which the fixture refuses",
			run: func(t *testing.T, f *agentFilesFixture, _ string) (string, error) {
				setRunAsStateForTest(t, nil, "")
				p := filepath.Join(f.ws, "c.txt")
				return p, agentWriteFile(context.Background(), p, []byte(data), 0o644)
			},
			wantErr: "permission denied", forbidden: true,
		},
		{
			name: "option empty outside the fixture: the os call",
			run: func(t *testing.T, _ *agentFilesFixture, outside string) (string, error) {
				setRunAsStateForTest(t, nil, "")
				p := filepath.Join(outside, "d.txt")
				return p, agentWriteFile(context.Background(), p, []byte(data), 0o644)
			},
			wantData: true,
		},
		{
			name: "agentCommand error: returned, no write",
			run: func(t *testing.T, f *agentFilesFixture, _ string) (string, error) {
				setRunAsStateForTest(t, gitRouteSettings(t, f.root), "user exists")
				p := filepath.Join(f.ws, "e.txt")
				return p, agentWriteFile(context.Background(), p, []byte(data), 0o644)
			},
			wantErr: "run_as_user preflight failed", forbidden: true,
		},
		{
			name: "a link in the workspace that leaves the root: error, no write",
			run: func(t *testing.T, f *agentFilesFixture, outside string) (string, error) {
				return filepath.Join(outside, "f.txt"), agentWriteFile(context.Background(), filepath.Join(f.ws, "out", "f.txt"), []byte(data), 0o644)
			},
			wantErr: "inside the workspace root", forbidden: true,
		},
		{
			name: "the workspace root itself: error",
			run: func(t *testing.T, f *agentFilesFixture, _ string) (string, error) {
				return f.root + "-never", agentWriteFile(context.Background(), f.root, []byte(data), 0o644)
			},
			wantErr: "inside the workspace root", forbidden: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentFilesFixture(t, "")
			outside := t.TempDir()
			writeTestFile(t, filepath.Join(f.ws, "seeded.txt"), "old content that is longer than the new one\n")
			if err := os.Symlink(outside, filepath.Join(f.ws, "out")); err != nil {
				t.Fatal(err)
			}
			f.lock(t)

			path, err := tc.run(t, f, outside)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("err = %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want one with %q", err, tc.wantErr)
			}
			if tc.wantData {
				if got := readTestFile(t, path); got != data {
					t.Errorf("%s holds %q, want %q", path, got, data)
				}
			}
			if tc.forbidden {
				assertNoPath(t, path)
			}
			calls := f.calls(t)
			if len(calls) != tc.wantSudo {
				t.Errorf("sudo ran %d times (%q), want %d", len(calls), calls, tc.wantSudo)
			}
			for _, c := range calls {
				if strings.Contains(c, data) {
					t.Errorf("the data is in the sudo argv: %q", c)
				}
			}
		})
	}
}

// TestAgentFilesOperations is item 1 for the other helpers: inside the root
// each runs as the agent user and does what the os call does, a missing path
// is an error that os.IsNotExist sees; outside the root no sudo runs.
func TestAgentFilesOperations(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		run   func(t *testing.T, base string) error
		check func(t *testing.T, base string)
	}{
		{
			name: "read",
			run: func(t *testing.T, base string) error {
				got, err := agentReadFile(ctx, filepath.Join(base, "seeded.txt"))
				if err == nil && string(got) != "seed\n" {
					return fmt.Errorf("read %q", got)
				}
				return err
			},
		},
		{
			name: "read of a missing file is not-exist",
			run: func(t *testing.T, base string) error {
				_, err := agentReadFile(ctx, filepath.Join(base, "missing.txt"))
				if !os.IsNotExist(err) {
					return fmt.Errorf("err = %v, want not-exist", err)
				}
				return nil
			},
		},
		{
			name: "mkdir all",
			run:  func(t *testing.T, base string) error { return agentMkdirAll(ctx, filepath.Join(base, "x", "y", "z"), 0o755) },
			check: func(t *testing.T, base string) {
				if fi, err := os.Stat(filepath.Join(base, "x", "y", "z")); err != nil || !fi.IsDir() {
					t.Errorf("x/y/z is not a directory: %v", err)
				}
			},
		},
		{
			name:  "remove a file",
			run:   func(t *testing.T, base string) error { return agentRemove(ctx, filepath.Join(base, "seeded.txt")) },
			check: func(t *testing.T, base string) { assertNoPath(t, filepath.Join(base, "seeded.txt")) },
		},
		{
			name:  "remove an empty directory",
			run:   func(t *testing.T, base string) error { return agentRemove(ctx, filepath.Join(base, "empty")) },
			check: func(t *testing.T, base string) { assertNoPath(t, filepath.Join(base, "empty")) },
		},
		{
			name: "remove of a missing file is not-exist",
			run: func(t *testing.T, base string) error {
				if err := agentRemove(ctx, filepath.Join(base, "missing.txt")); !os.IsNotExist(err) {
					return fmt.Errorf("err = %v, want not-exist", err)
				}
				return nil
			},
		},
		{
			name: "remove a directory only when it is empty",
			run: func(t *testing.T, base string) error {
				if err := agentRemoveDirIfEmpty(ctx, filepath.Join(base, "sub")); err != nil {
					return err
				}
				if err := agentRemoveDirIfEmpty(ctx, filepath.Join(base, "missing")); err != nil {
					return err
				}
				return agentRemoveDirIfEmpty(ctx, filepath.Join(base, "empty"))
			},
			check: func(t *testing.T, base string) {
				assertNoPath(t, filepath.Join(base, "empty"))
				readTestFile(t, filepath.Join(base, "sub", "keep.txt"))
			},
		},
	}
	for _, tc := range tests {
		for _, inside := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/inside=%v", tc.name, inside), func(t *testing.T) {
				f := newAgentFilesFixture(t, "")
				base := f.ws
				if !inside {
					base = t.TempDir()
				}
				writeTestFile(t, filepath.Join(base, "seeded.txt"), "seed\n")
				writeTestFile(t, filepath.Join(base, "sub", "keep.txt"), "keep\n")
				if err := os.MkdirAll(filepath.Join(base, "empty"), 0o755); err != nil {
					t.Fatal(err)
				}
				f.lock(t)
				if err := tc.run(t, base); err != nil {
					t.Fatal(err)
				}
				if tc.check != nil {
					tc.check(t, base)
				}
				if n := len(f.calls(t)); inside != (n > 0) {
					t.Errorf("sudo ran %d times, want it to run only inside the root", n)
				}
			})
		}
	}
}

// workspaceWriteSite is one site of item 2 that uses the helpers: seed (as
// the test, before the lock), run (the site function, as the daemon) and
// check (the files it produced).
type workspaceWriteSite struct {
	name  string
	seed  func(t *testing.T, ws string)
	run   func(t *testing.T, ws string) error
	check func(t *testing.T, ws string)
	// session, when set, runs between seed and run with the workspace
	// writable: what the session of the agent user changed.
	session func(t *testing.T, ws string)
}

const staleHookSettings = `{
  "model": "keep-me",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/nonexistent/pilot-hooks-x/pilot-bash-guard.sh"}]}
    ]
  }
}`

func gitInitCommitted(t *testing.T, ws string, files map[string]string) {
	t.Helper()
	wsGit(t, ws, "init", "-q")
	for p, c := range files {
		writeTestFile(t, filepath.Join(ws, p), c)
	}
	wsGit(t, ws, "add", "-A")
	wsGit(t, ws, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "-m", "base")
}

func workspaceWriteSites() []workspaceWriteSite {
	quiet := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return []workspaceWriteSite{
		{
			name: "CreateTaskDoc",
			seed: func(t *testing.T, ws string) {
				if err := os.MkdirAll(filepath.Join(ws, ".agent"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			run: func(t *testing.T, ws string) error {
				return CreateTaskDoc(filepath.Join(ws, ".agent"), &Task{ID: "GH-1", Description: "the task body"})
			},
			check: func(t *testing.T, ws string) {
				if got := readTestFile(t, filepath.Join(ws, ".agent", "tasks", "gh-1.md")); !strings.Contains(got, "the task body") {
					t.Errorf("task doc = %q", got)
				}
			},
		},
		{
			name: "maybeInitNavigator scaffold (NavigatorInitializer.Initialize)",
			seed: func(t *testing.T, ws string) {
				writeTestFile(t, filepath.Join(ws, "go.mod"), "module example.com/demoproj\n\ngo 1.22\n")
			},
			run: func(t *testing.T, ws string) error {
				n := &NavigatorInitializer{log: quiet} // embedded templates
				return n.Initialize(ws)
			},
			check: func(t *testing.T, ws string) {
				agent := filepath.Join(ws, ".agent")
				if got := readTestFile(t, filepath.Join(agent, "DEVELOPMENT-README.md")); !strings.Contains(got, "## Key Files") {
					t.Errorf("README lacks the context sections:\n%s", got)
				}
				if got := readTestFile(t, filepath.Join(agent, ".nav-config.json")); !strings.Contains(got, "demoproj") {
					t.Errorf(".nav-config.json = %q, want the name read from go.mod", got)
				}
				for _, p := range []string{"system/FEATURE-MATRIX.md", "knowledge/graph.json", ".gitignore", "tasks/.gitkeep"} {
					readTestFile(t, filepath.Join(agent, p))
				}
			},
		},
		{
			name: "CleanStalePilotHooks in the workspace",
			seed: func(t *testing.T, ws string) {
				writeTestFile(t, filepath.Join(ws, ".claude", "settings.json"), staleHookSettings)
			},
			run: func(t *testing.T, ws string) error {
				return CleanStalePilotHooks(filepath.Join(ws, ".claude", "settings.json"))
			},
			check: func(t *testing.T, ws string) {
				got := readTestFile(t, filepath.Join(ws, ".claude", "settings.json"))
				if strings.Contains(got, "pilot-bash-guard") || !strings.Contains(got, "keep-me") {
					t.Errorf("settings = %s", got)
				}
			},
		},
		{
			name: "MergeWithExisting and its restore of an existing file",
			seed: func(t *testing.T, ws string) {
				writeTestFile(t, filepath.Join(ws, ".claude", "settings.json"), `{"model":"keep-me"}`)
			},
			run: func(t *testing.T, ws string) error {
				p := filepath.Join(ws, ".claude", "settings.json")
				restore, preExisted, err := MergeWithExisting(p, coAuthorSettings())
				if err != nil {
					return err
				}
				var merged map[string]any
				if err := json.Unmarshal([]byte(readTestFile(t, p)), &merged); err != nil {
					return err
				}
				if merged["includeCoAuthoredBy"] != false || merged["model"] != "keep-me" || !preExisted {
					return fmt.Errorf("merged = %v, preExisted %v", merged, preExisted)
				}
				return restore()
			},
			check: func(t *testing.T, ws string) {
				if got := readTestFile(t, filepath.Join(ws, ".claude", "settings.json")); got != `{"model":"keep-me"}` {
					t.Errorf("restored settings = %q", got)
				}
			},
		},
		{
			name: "MergeWithExisting of a new file, its removal and removeClaudeDirIfEmpty",
			run: func(t *testing.T, ws string) error {
				p := filepath.Join(ws, ".claude", "settings.json")
				restore, preExisted, err := MergeWithExisting(p, coAuthorSettings())
				if err != nil {
					return err
				}
				if got := readTestFile(t, p); preExisted || !strings.Contains(got, `"includeCoAuthoredBy": false`) {
					return fmt.Errorf("settings = %q, preExisted %v", got, preExisted)
				}
				if err := restore(); err != nil {
					return err
				}
				return removeClaudeDirIfEmpty(p)
			},
			check: func(t *testing.T, ws string) { assertNoPath(t, filepath.Join(ws, ".claude")) },
		},
		{
			name: "RestoreHookSettings",
			run: func(t *testing.T, ws string) error {
				p := filepath.Join(ws, ".claude", "settings.json")
				scriptDir := t.TempDir() // outside the root: the os call
				hooks := GenerateClaudeSettings(DefaultHooksConfig(), scriptDir)
				restore, preExisted, err := MergeWithExisting(p, hooks)
				if err != nil {
					return err
				}
				if got := readTestFile(t, p); !strings.Contains(got, scriptDir) {
					return fmt.Errorf("hook settings = %q", got)
				}
				return RestoreHookSettings(p, scriptDir, restore, preExisted)
			},
			check: func(t *testing.T, ws string) { assertNoPath(t, filepath.Join(ws, ".claude")) },
		},
		{
			name: "SimplifyModifiedFiles",
			seed: func(t *testing.T, ws string) {
				gitInitCommitted(t, ws, map[string]string{"main.go": "package main\n"})
				writeTestFile(t, filepath.Join(ws, "main.go"), "package main\n\nfunc main() {   \n}\n")
			},
			run: func(t *testing.T, ws string) error {
				files, err := SimplifyModifiedFiles(ws, &SimplifyConfig{Enabled: true, MaxFileSize: 50000})
				if err == nil && (len(files) != 1 || files[0] != "main.go") {
					return fmt.Errorf("simplified %q, want main.go", files)
				}
				return err
			},
			check: func(t *testing.T, ws string) {
				if got := readTestFile(t, filepath.Join(ws, "main.go")); strings.Contains(got, "{   \n") {
					t.Errorf("main.go not simplified: %q", got)
				}
			},
		},
		{
			name: "maybeInitNavigator bootstrap sweep (restore, revert, delete)",
			seed: func(t *testing.T, ws string) {
				gitInitCommitted(t, ws, map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
				writeTestFile(t, filepath.Join(ws, "b.txt"), "dirty before\n")
			},
			session: func(t *testing.T, ws string) {
				writeTestFile(t, filepath.Join(ws, "a.txt"), "changed by the session\n")
				writeTestFile(t, filepath.Join(ws, "b.txt"), "changed again\n")
				writeTestFile(t, filepath.Join(ws, "new", "stray.txt"), "stray\n")
			},
			run: func(t *testing.T, ws string) error {
				// The baseline is taken in seed's state: see the site loop.
				return nil
			},
			check: func(t *testing.T, ws string) {
				if got := readTestFile(t, filepath.Join(ws, "a.txt")); got != "a\n" {
					t.Errorf("a.txt = %q, want the HEAD content", got)
				}
				if got := readTestFile(t, filepath.Join(ws, "b.txt")); got != "dirty before\n" {
					t.Errorf("b.txt = %q, want the content from before the session", got)
				}
				assertNoPath(t, filepath.Join(ws, "new"))
			},
		},
	}
}

// TestWorkspaceWritesSites is item 2 and 5: each site that uses the helpers
// produces its files in the workspace of the fixture, which the test process
// cannot write; with the option empty the same site makes the os call of
// today (no sudo) in a writable directory.
func TestWorkspaceWritesSites(t *testing.T) {
	for _, site := range workspaceWriteSites() {
		for _, mode := range []string{"run_as_user set", "option empty"} {
			t.Run(site.name+"/"+mode, func(t *testing.T) {
				f := newAgentFilesFixture(t, "")
				ws := f.ws
				if mode == "option empty" {
					setRunAsStateForTest(t, nil, "")
					ws = t.TempDir()
				}
				if site.seed != nil {
					site.seed(t, ws)
				}
				var baseline *strayBaseline
				if site.session != nil {
					baseline = snapshotStrayBaseline(context.Background(), ws)
				}
				f.lock(t)
				if site.session != nil {
					f.asAgent(t, func() { site.session(t, ws) })
				}
				if err := site.run(t, ws); err != nil {
					t.Fatalf("site: %v", err)
				}
				if baseline != nil {
					quiet := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
					if stray := baseline.sweepStrayWrites(context.Background(), quiet); len(stray) != 3 {
						t.Errorf("stray = %q, want a.txt, b.txt and new/stray.txt", stray)
					}
				}
				site.check(t, ws)
				calls := f.calls(t)
				if mode == "option empty" && len(calls) != 0 {
					t.Errorf("option empty: sudo ran %d times, first %q", len(calls), calls[0])
				}
				if mode != "option empty" && len(calls) == 0 {
					t.Error("run_as_user set: no command ran as the agent user")
				}
			})
		}
	}
}

// settingsCheckBackend records the project path and the settings file the
// session finds there.
type settingsCheckBackend struct {
	retryPathRecordingBackend
	settings string
}

func (b *settingsCheckBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	if data, err := os.ReadFile(filepath.Join(opts.ProjectPath, ".claude", "settings.json")); err == nil {
		b.mu.Lock()
		b.settings = string(data)
		b.mu.Unlock()
	}
	return b.retryPathRecordingBackend.Execute(ctx, opts)
}

// TestRunAsUserSettingsWriteFailureStopsTheRun is item 3: with the option set,
// a failure of the settings write stops the run before the session, with one
// Error line; with the write working the session finds the settings file in
// the agent workspace.
func TestRunAsUserSettingsWriteFailureStopsTheRun(t *testing.T) {
	// Fails every write of the settings file as the agent user.
	const failSettingsWrite = `case "$*" in
*"cat > "*settings.json*) echo "mock sudo: settings.json: permission denied" >&2; exit 1 ;;
esac`
	tests := []struct {
		name      string
		hooks     bool
		failWrite bool
	}{
		{"co-author settings, write fails", false, true},
		{"hook settings, write fails", true, true},
		{"co-author settings, write works", false, false},
		{"hook settings, write works", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubRunAsGOOS(t)
			repo, remote := setupTestRepoWithRemote(t)
			t.Cleanup(func() { _ = os.RemoveAll(repo); _ = os.RemoveAll(remote) })
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			extra := ""
			if tc.failWrite {
				extra = failSettingsWrite
			}
			_ = mockSudoAgent(t, repo, extra)
			setRunAsStateForTest(t, gitRouteSettings(t, root), "")

			backend := &settingsCheckBackend{}
			runner := NewRunnerWithBackend(backend)
			runner.config = &BackendConfig{}
			if tc.hooks {
				runner.config.Hooks = DefaultHooksConfig()
			}
			runner.SetSkipPreflightChecks(true)
			runner.SetRecordingEnabled(false)
			var buf bytes.Buffer
			runner.log = slog.New(slog.NewTextHandler(&buf, nil))
			task := &Task{ID: "GH-11", Title: "t", Description: "d", ProjectPath: repo, Branch: "pilot/GH-11", CreatePR: true, BaseBranch: "main"}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			res, err := runner.executeWithOptions(ctx, task, true)
			if !tc.failWrite {
				if len(backend.paths()) == 0 {
					t.Fatalf("the backend never ran (err %v):\n%s", err, buf.String())
				}
				if !strings.Contains(backend.settings, `"includeCoAuthoredBy": false`) {
					t.Errorf("the session found settings %q in the workspace", backend.settings)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "claude settings write failed") {
				t.Fatalf("err = %v, want the settings failure", err)
			}
			if res == nil || res.Success || !strings.Contains(res.Error, "settings.json") {
				t.Errorf("result = %+v", res)
			}
			if paths := backend.paths(); len(paths) != 0 {
				t.Errorf("the session ran in %q after the settings write failed", paths)
			}
			if n := strings.Count(buf.String(), "level=ERROR"); n != 1 || !strings.Contains(buf.String(), "Claude settings write failed") {
				t.Errorf("want one Error line for the settings write, got %d:\n%s", n, buf.String())
			}
		})
	}
}

// TestRunAsUserNavigatorDocsSkippedAfterTask is item 2 for the sites that only
// serve the daemon: with the option set the index sync, the task doc archive,
// the feature matrix and the context marker write nothing and log one Info
// line; with the option empty they run as today.
func TestRunAsUserNavigatorDocsSkippedAfterTask(t *testing.T) {
	for _, set := range []bool{true, false} {
		t.Run(fmt.Sprintf("run_as_user set=%v", set), func(t *testing.T) {
			f := newAgentFilesFixture(t, "")
			ws := f.ws
			if !set {
				setRunAsStateForTest(t, nil, "")
				ws = t.TempDir()
			}
			doc := filepath.Join(ws, ".agent", "tasks", "gh-12.md")
			writeTestFile(t, doc, "# GH-12\n")
			writeTestFile(t, filepath.Join(ws, ".agent", "system", "FEATURE-MATRIX.md"), "## Core Execution\n\n| F | S |\n|---|---|\n")
			f.lock(t)

			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			r := &Runner{log: log}
			task := &Task{ID: "GH-12", Title: "feat(x): a feature"}
			r.updateNavigatorDocsAfterTask(log, task, &ExecutionResult{}, nil, ws)

			archived := filepath.Join(ws, ".agent", "tasks", "archive", "gh-12.md")
			if set {
				readTestFile(t, doc)
				assertNoPath(t, archived)
				assertNoPath(t, filepath.Join(ws, ".agent", ".context-markers"))
				if n := strings.Count(buf.String(), "level=INFO"); n != 1 || !strings.Contains(buf.String(), "Navigator files after the task skipped") {
					t.Errorf("want one Info line, got:\n%s", buf.String())
				}
				if calls := f.calls(t); len(calls) != 0 {
					t.Errorf("sudo ran: %q", calls)
				}
				return
			}
			assertNoPath(t, doc)
			readTestFile(t, archived)
			if got := readTestFile(t, filepath.Join(ws, ".agent", "system", "FEATURE-MATRIX.md")); !strings.Contains(got, "GH-12") {
				t.Errorf("feature matrix = %q", got)
			}
		})
	}
}

// mockSudoKill is the extra code of mockSudoWritable that runs the kill of
// killAgentProcessGroup (`sudo ... kill -s KILL -- -PGID`, "$1" is -PGID after
// the "--"), so the Cancel of agentCommand stops the command in the fixture.
const mockSudoKill = `case "$1" in
-[0-9]*) exec kill -9 "$1" ;;
esac`

// agentKillCalls returns the calls of the mock sudo that are a kill of a
// process group.
func agentKillCalls(calls []string) []string {
	var kills []string
	for _, c := range calls {
		if strings.Contains(c, " kill -s KILL -- -") {
			kills = append(kills, c)
		}
	}
	return kills
}

// TestAgentFilesReadLimit is item 1 of GH-6411: the standard output of a read
// as the agent user is bounded by agentFileReadLimit and the standard error of
// every helper command by agentFileStderrLimit. Over a limit the command is
// stopped through the Cancel of agentCommand (the mock records the kill) and
// the helper returns an error that names the path and the limit, no content.
func TestAgentFilesReadLimit(t *testing.T) {
	tests := []struct {
		name string
		// seed runs before the lock and returns the path to read.
		seed func(t *testing.T, ws string) string
		// run is the helper call; nil is agentReadFile of the seeded path.
		run       func(ctx context.Context, path string) ([]byte, error)
		wantErr   string // "" is success
		wantLen   int
		wantKills bool
	}{
		{
			name: "a file of exactly the limit is read",
			seed: func(t *testing.T, ws string) string {
				p := filepath.Join(ws, "at-limit.bin")
				writeTestFile(t, p, strings.Repeat("a", agentFileReadLimit))
				return p
			},
			wantLen: agentFileReadLimit,
		},
		{
			name: "a file over the limit is an error",
			seed: func(t *testing.T, ws string) string {
				p := filepath.Join(ws, "over-limit.bin")
				writeTestFile(t, p, strings.Repeat("a", agentFileReadLimit+1))
				return p
			},
			wantErr:   fmt.Sprintf("standard output of the command is over the limit of %d bytes", agentFileReadLimit),
			wantKills: true,
		},
		{
			name: "a link to an endless source is an error",
			seed: func(t *testing.T, ws string) string {
				p := filepath.Join(ws, ".claude", "settings.json")
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/dev/zero", p); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErr:   fmt.Sprintf("standard output of the command is over the limit of %d bytes", agentFileReadLimit),
			wantKills: true,
		},
		{
			name: "an endless standard error is an error",
			seed: func(t *testing.T, ws string) string { return filepath.Join(ws, "any.txt") },
			run: func(ctx context.Context, path string) ([]byte, error) {
				return runAgentFileScript(ctx, "test", path, `exec yes 1>&2`, nil)
			},
			wantErr:   fmt.Sprintf("standard error of the command is over the limit of %d bytes", agentFileStderrLimit),
			wantKills: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentFilesFixture(t, mockSudoKill)
			path := tc.seed(t, f.ws)
			f.lock(t)
			run := tc.run
			if run == nil {
				run = agentReadFile
			}

			start := time.Now()
			got, err := run(context.Background(), path)
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("the helper returned after %v, want inside 5 seconds", d)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("err = %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), path)):
				t.Fatalf("err = %v, want one with %q and the path %s", err, tc.wantErr, path)
			}
			if tc.wantErr != "" && got != nil {
				t.Errorf("the helper returned %d bytes with the error, want none", len(got))
			}
			if tc.wantErr == "" && len(got) != tc.wantLen {
				t.Errorf("read %d bytes, want %d", len(got), tc.wantLen)
			}
			if kills := agentKillCalls(f.calls(t)); tc.wantKills != (len(kills) > 0) {
				t.Errorf("kill calls = %q, want a kill: %v", kills, tc.wantKills)
			}
		})
	}
}

// TestAgentWriteFileMode is item 3 of GH-6411: a new file written as the agent
// user has the requested permission bits and no more; an existing file keeps
// its mode, as os.WriteFile.
func TestAgentWriteFileMode(t *testing.T) {
	tests := []struct {
		name     string
		existing os.FileMode // 0: the file does not exist
		mode     os.FileMode
		want     os.FileMode
	}{
		{name: "new file 0755", mode: 0o755, want: 0o755},
		{name: "new file 0600", mode: 0o600, want: 0o600},
		{name: "new file 0644", mode: 0o644, want: 0o644},
		{name: "existing 0644 written with 0755 keeps 0644", existing: 0o644, mode: 0o755, want: 0o644},
		{name: "existing 0700 written with 0600 keeps 0700", existing: 0o700, mode: 0o600, want: 0o700},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentFilesFixture(t, "")
			// The mock of the earlier leg without the chmod -R of the
			// fixture's mock, which would clear the write bits of the result.
			sudoLog := mockSudoAgent(t, "", "")
			p := filepath.Join(f.ws, "bin", "tool")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.existing != 0 {
				if err := os.WriteFile(p, []byte("old\n"), tc.existing); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(p, tc.existing); err != nil {
					t.Fatal(err)
				}
			}
			if err := agentWriteFile(context.Background(), p, []byte("#!/bin/sh\n"), tc.mode); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := fi.Mode().Perm(); got != tc.want {
				t.Errorf("mode = %#o, want %#o", got, tc.want)
			}
			if got := readTestFile(t, p); got != "#!/bin/sh\n" {
				t.Errorf("content = %q", got)
			}
			if calls := sudoCalls(t, sudoLog); len(calls) != 1 {
				t.Errorf("sudo ran %d times (%q), want the write as the agent user once", len(calls), calls)
			}
		})
	}
}

// TestRunAsUserSettingsCleanFailureStopsTheRun is item 2 of GH-6411: with the
// option set, a failed write of CleanStalePilotHooks on the settings file of
// the workspace stops the run before the session with one Error line, as a
// failed merge does. Only that first write fails: a Warn line would let the
// merges after it succeed and the session run.
func TestRunAsUserSettingsCleanFailureStopsTheRun(t *testing.T) {
	stubRunAsGOOS(t)
	repo, remote := setupTestRepoWithRemote(t)
	t.Cleanup(func() { _ = os.RemoveAll(repo); _ = os.RemoveAll(remote) })
	// The workspace is a clone: the stale hook entries come with origin/main.
	writeTestFile(t, filepath.Join(repo, ".claude", "settings.json"), staleHookSettings)
	for _, args := range [][]string{
		{"add", ".claude/settings.json"},
		{"-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "-m", "stale hooks"},
		{"push", "-q", "origin", "HEAD:main"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %q: %v: %s", args, err, out)
		}
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	failedOnce := filepath.Join(t.TempDir(), "failed-once")
	failFirstSettingsWrite := fmt.Sprintf(`case "$*" in
*"cat > "*settings.json*)
  if [ ! -e '%[1]s' ]; then
    : > '%[1]s'
    echo "mock sudo: settings.json: permission denied" >&2
    exit 1
  fi ;;
esac`, failedOnce)
	_ = mockSudoAgent(t, repo, failFirstSettingsWrite)
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")

	backend := &settingsCheckBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	var buf bytes.Buffer
	runner.log = slog.New(slog.NewTextHandler(&buf, nil))
	task := &Task{ID: "GH-13", Title: "t", Description: "d", ProjectPath: repo, Branch: "pilot/GH-13", CreatePR: true, BaseBranch: "main"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := runner.executeWithOptions(ctx, task, true)
	if _, statErr := os.Stat(failedOnce); statErr != nil {
		t.Fatalf("the clean-up write never ran as the agent user (err %v):\n%s", err, buf.String())
	}
	if err == nil || !strings.Contains(err.Error(), "claude settings write failed") {
		t.Fatalf("err = %v, want the settings failure", err)
	}
	if res == nil || res.Success || !strings.Contains(res.Error, "settings.json") {
		t.Errorf("result = %+v", res)
	}
	if paths := backend.paths(); len(paths) != 0 {
		t.Errorf("the session ran in %q after the clean-up write failed", paths)
	}
	if n := strings.Count(buf.String(), "level=ERROR"); n != 1 || !strings.Contains(buf.String(), "Claude settings write failed") {
		t.Errorf("want one Error line for the settings write, got %d:\n%s", n, buf.String())
	}
	if strings.Contains(buf.String(), "Failed to clean stale pilot hooks in worktree") {
		t.Errorf("the clean-up failure is a Warn line:\n%s", buf.String())
	}
}
