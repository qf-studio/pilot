package executor

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/workflow"
)

// TASK-651: with run_as_user set the gh shim runs a copy of the daemon binary
// in its own directory, and the daemon reads .pilot/workflow.yaml and
// .agent/DEVELOPMENT-README.md of a workspace as the agent user. No test needs
// root; the reads use the mock sudo of agentFilesFixture.

func TestRunAsUserGhGuardBinaryCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell shim")
	}
	sameSize := func(t *testing.T, a, b string) {
		t.Helper()
		ai, err := os.Stat(a)
		if err != nil {
			t.Fatal(err)
		}
		bi, err := os.Stat(b)
		if err != nil {
			t.Fatal(err)
		}
		if ai.Size() != bi.Size() {
			t.Errorf("%s has %d bytes, the source %s has %d", a, ai.Size(), b, bi.Size())
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		set  bool
		// selfExe replaces ghGuardSelfExe; "" keeps the default.
		selfExe func(t *testing.T) string
		wantErr string
		// wantSource is the file the copy must match; "" when no copy.
		wantSource func(t *testing.T) string
	}{
		{
			name:       "option set: the script runs a copy in the shim directory",
			set:        true,
			wantSource: func(t *testing.T) string { return selfExeOrExecutable(t) },
		},
		{
			name: "option set, no /proc/self/exe: the os.Executable path is copied",
			set:  true,
			selfExe: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing")
			},
			wantSource: func(t *testing.T) string { return exe },
		},
		{
			name: "option set, a source the daemon cannot read: the set-up fails",
			set:  true,
			selfExe: func(t *testing.T) string {
				if os.Geteuid() == 0 {
					t.Skip("root reads a file with mode 0")
				}
				p := filepath.Join(t.TempDir(), "unreadable")
				if err := os.WriteFile(p, []byte("x"), 0o000); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErr: "ghguard: open pilot binary",
		},
		{
			name: "option empty: the script runs the daemon binary",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				setRunAsStateForTest(t, testRunAsSettings(t), "")
			} else {
				setRunAsStateForTest(t, nil, "")
			}
			if tc.selfExe != nil {
				prev := ghGuardSelfExe
				ghGuardSelfExe = tc.selfExe(t)
				t.Cleanup(func() { ghGuardSelfExe = prev })
			}
			before, _ := filepath.Glob(filepath.Join(os.TempDir(), "pilot-ghguard-*"))
			shimDir, _, cleanup, err := setupGhGuardShim("")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				after, _ := filepath.Glob(filepath.Join(os.TempDir(), "pilot-ghguard-*"))
				if len(after) > len(before) {
					t.Errorf("a failed set-up left a shim directory: %v", after)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			script := readTestFile(t, filepath.Join(shimDir, "gh"))
			copyPath := filepath.Join(shimDir, ghGuardBinaryName)

			if tc.wantSource == nil {
				want, err := filepath.EvalSymlinks(exe)
				if err != nil {
					want = exe
				}
				if script != fmt.Sprintf(ghGuardShimScript, want) {
					t.Errorf("script = %q, want it to run the daemon binary %q", script, want)
				}
				assertNoPath(t, copyPath)
				return
			}
			if script != fmt.Sprintf(ghGuardShimScript, copyPath) {
				t.Errorf("script = %q, want it to run the copy %q", script, copyPath)
			}
			fi, err := os.Stat(copyPath)
			if err != nil {
				t.Fatalf("no copy in the shim directory: %v", err)
			}
			if fi.Mode().Perm() != 0o750 {
				t.Errorf("copy mode = %o, want 750", fi.Mode().Perm())
			}
			sameSize(t, copyPath, tc.wantSource(t))

			cleanup()
			assertNoPath(t, copyPath)
		})
	}
}

// testGuardGroup is a real group of the test process for a fixture of the
// binary copy: a supplementary group when the process has one, so the group
// of the copy differs from the one the file gets at creation; the effective
// group otherwise. The second value is false for the effective group.
func testGuardGroup(t *testing.T) (gid int, differs bool) {
	t.Helper()
	egid := os.Getegid()
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g != egid {
			return g, true
		}
	}
	return egid, false
}

// mutation pin: the copy of the binary gets a plain chmod, not applyGuardPerm, so it keeps the group of the daemon and each gh call of the agent fails -> this test fails
func TestRunAsUserGhGuardBinaryCopyGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	gid, differs := testGuardGroup(t)
	s := testRunAsSettings(t)
	s.group, s.gid = "pilot-agents", gid
	setRunAsStateForTest(t, s, "")
	// The chown is the real one; the record shows the group change of the
	// copy also when the process has no group but its effective one.
	var chowned []string
	prev := guardChown
	guardChown = func(path string, uid, g int) error {
		chowned = append(chowned, fmt.Sprintf("%s:%d", filepath.Base(path), g))
		return os.Chown(path, uid, g)
	}
	t.Cleanup(func() { guardChown = prev })
	// A small source: the group and mode of the copy are under test, not its
	// content, and the test binary is large.
	prevOpen := ghGuardOpenBinary
	ghGuardOpenBinary = func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("#!/bin/sh\n")), nil
	}
	t.Cleanup(func() { ghGuardOpenBinary = prevOpen })

	shimDir, _, cleanup, err := setupGhGuardShim("")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	copyPath := filepath.Join(shimDir, ghGuardBinaryName)
	fi, err := os.Stat(copyPath)
	if err != nil {
		t.Fatalf("no copy in the shim directory: %v", err)
	}
	if fi.Mode().Perm() != 0o750 {
		t.Errorf("copy mode = %04o, want 0750", fi.Mode().Perm())
	}
	if got := fileGID(t, copyPath); got != gid {
		t.Errorf("copy group = %d, want the agent group %d (a supplementary group of the process: %v)", got, gid, differs)
	}
	want := fmt.Sprintf("%s:%d", ghGuardBinaryName, gid)
	if !strings.Contains(" "+strings.Join(chowned, " ")+" ", " "+want+" ") {
		t.Errorf("group changes %v, want %s", chowned, want)
	}
}

// earlyEndReader gives n bytes and then an error, as a source binary that
// ends in the middle of the copy.
type earlyEndReader struct{ n int }

func (r *earlyEndReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, fmt.Errorf("source ended early")
	}
	k := min(len(p), r.n)
	for i := range p[:k] {
		p[i] = 'x'
	}
	r.n -= k
	return k, nil
}

func (r *earlyEndReader) Close() error { return nil }

// TestRunAsUserGhGuardBinaryCopyFails: a source that ends in the middle of the
// copy fails the set-up and leaves no file with the name of the copy, in the
// shim directory of the copy and with no shim directory left behind.
func TestRunAsUserGhGuardBinaryCopyFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell shim")
	}
	setRunAsStateForTest(t, testRunAsSettings(t), "")
	prev := ghGuardOpenBinary
	ghGuardOpenBinary = func(string) (io.ReadCloser, error) { return &earlyEndReader{n: 64 << 10}, nil }
	t.Cleanup(func() { ghGuardOpenBinary = prev })

	dir := t.TempDir()
	got, err := installGhGuardBinary(dir)
	if err == nil || !strings.Contains(err.Error(), "ghguard: copy pilot binary") || got != "" {
		t.Fatalf("installGhGuardBinary = %q, %v; want no path and the failed copy", got, err)
	}
	assertNoPath(t, filepath.Join(dir, ghGuardBinaryName))

	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "pilot-ghguard-*"))
	shimDir, _, _, err := setupGhGuardShim("")
	if err == nil || !strings.Contains(err.Error(), "source ended early") {
		t.Fatalf("setupGhGuardShim = %q, %v; want the failed copy", shimDir, err)
	}
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), "pilot-ghguard-*"))
	if len(after) > len(before) {
		t.Errorf("a failed copy left a shim directory: %v", after)
	}
}

// selfExeOrExecutable is the file installGhGuardBinary copies by default.
func selfExeOrExecutable(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat(ghGuardSelfExe); err == nil {
		return ghGuardSelfExe
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// workspaceReadCase is one read of a workspace file of TestRunAsUserWorkflowRead
// and TestRunAsUserProjectContextRead.
type workspaceReadCase struct {
	name string
	// seed is the content at the path: "" for no file, linkOut for a link
	// to a file outside the workspace that holds outsideContent, bigFile for
	// a file one byte over agentFileReadLimit.
	seed  string
	empty bool // the option is empty for the read
}

const (
	linkOut = "<link>"
	bigFile = "<big>"
)

// seedWorkspaceRead builds the fixture with rel seeded per seed. A linked
// target has mode 0 and the mock sudo makes it readable: only the helper
// command reads it, never the daemon process before a sudo call. With the
// option empty the target stays readable (the os call of today).
func seedWorkspaceRead(t *testing.T, tc workspaceReadCase, rel, outsideContent string) (*agentFilesFixture, string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "daemon-file")
	writeTestFile(t, target, outsideContent)
	extra := ""
	if !tc.empty {
		if err := os.Chmod(target, 0o000); err != nil {
			t.Fatal(err)
		}
		extra = fmt.Sprintf("chmod u+r '%s'", target)
	}
	f := newAgentFilesFixture(t, extra)
	p := filepath.Join(f.ws, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	switch tc.seed {
	case "":
	case linkOut:
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	case bigFile:
		writeTestFile(t, p, strings.Repeat("x", agentFileReadLimit+1))
	default:
		writeTestFile(t, p, tc.seed)
	}
	f.lock(t)
	if tc.empty {
		setRunAsStateForTest(t, nil, "")
	}
	return f, p
}

// assertReadAsAgent checks that the mock sudo ran one read of path (the stop
// of a read over the limit adds the kill of its process group), or no command
// with the option empty.
func assertReadAsAgent(t *testing.T, f *agentFilesFixture, path string, empty bool) {
	t.Helper()
	calls := f.calls(t)
	if empty {
		if len(calls) != 0 {
			t.Errorf("option empty: sudo calls %q, want none", calls)
		}
		return
	}
	if len(calls) == 0 || !strings.Contains(calls[0], agentReadScript) || !strings.HasSuffix(calls[0], " "+path) {
		t.Fatalf("sudo calls = %q, want a read of %s as the agent user", calls, path)
	}
	for _, c := range calls[1:] {
		if !strings.Contains(c, " kill -s KILL -- -") {
			t.Errorf("sudo call %q after the read, want only the kill of the read", c)
		}
	}
}

func TestRunAsUserWorkflowRead(t *testing.T) {
	const wf = "---\nversion: 1\nagent:\n  max_turns: 7\n---\nlinked body\n"
	tests := []struct {
		workspaceReadCase
		wantNil   bool
		wantTurns int
		wantErr   string
	}{
		{workspaceReadCase: workspaceReadCase{name: "a link out of the workspace is read as the agent user", seed: linkOut}, wantTurns: 7},
		{workspaceReadCase: workspaceReadCase{name: "a plain file is read as the agent user", seed: wf}, wantTurns: 7},
		{workspaceReadCase: workspaceReadCase{name: "no file gives the defaults", seed: ""}, wantNil: true},
		{workspaceReadCase: workspaceReadCase{name: "a file over the read limit stops with an error", seed: bigFile}, wantErr: "over the limit"},
		{workspaceReadCase: workspaceReadCase{name: "option empty: workflow.Load follows the link", seed: linkOut, empty: true}, wantTurns: 7},
		{workspaceReadCase: workspaceReadCase{name: "option empty: no file gives the defaults", seed: "", empty: true}, wantNil: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, p := seedWorkspaceRead(t, tc.workspaceReadCase, workflow.FilePath, wf)
			got, err := loadRepoWorkflow(t.Context(), f.ws)
			if tc.empty {
				want, wantErr := workflow.Load(f.ws)
				if (err == nil) != (wantErr == nil) || (got == nil) != (want == nil) {
					t.Errorf("loadRepoWorkflow = %v, %v; workflow.Load = %v, %v", got, err, want, wantErr)
				}
			}
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || got != nil {
					t.Fatalf("got %v, err %v, want no workflow and %q", got, err, tc.wantErr)
				}
			case tc.wantNil:
				if err != nil || got != nil {
					t.Fatalf("got %v, err %v, want nil, nil", got, err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				if got == nil || got.Agent.MaxTurns != tc.wantTurns {
					t.Fatalf("workflow = %+v, want max_turns %d", got, tc.wantTurns)
				}
			}
			assertReadAsAgent(t, f, p, tc.empty)
		})
	}
}

func TestRunAsUserProjectContextRead(t *testing.T) {
	const readme = "# Project\n\n## Key Files\n\n- linked-key-file.go\n\n## Other\n"
	tests := []struct {
		workspaceReadCase
		wantCtx  bool
		wantWarn int
	}{
		{workspaceReadCase: workspaceReadCase{name: "a link out of the workspace is read as the agent user", seed: linkOut}, wantCtx: true},
		{workspaceReadCase: workspaceReadCase{name: "no file gives the empty context", seed: ""}},
		{workspaceReadCase: workspaceReadCase{name: "a read error gives the empty context and one Warn line", seed: bigFile}, wantWarn: 1},
		{workspaceReadCase: workspaceReadCase{name: "option empty: os.ReadFile follows the link", seed: linkOut, empty: true}, wantCtx: true},
		{workspaceReadCase: workspaceReadCase{name: "option empty: no file gives the empty context", seed: "", empty: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, p := seedWorkspaceRead(t, tc.workspaceReadCase, filepath.Join(".agent", "DEVELOPMENT-README.md"), readme)
			logs := captureLog(t)
			got := loadProjectContext(filepath.Join(f.ws, ".agent"))
			if hasKey := strings.Contains(got, "linked-key-file.go"); hasKey != tc.wantCtx {
				t.Errorf("context = %q, want the linked README in it: %v", got, tc.wantCtx)
			}
			if !tc.wantCtx && got != "" {
				t.Errorf("context = %q, want empty", got)
			}
			if n := bytes.Count(logs.Bytes(), []byte("level=WARN")); n != tc.wantWarn {
				t.Errorf("%d Warn lines, want %d: %s", n, tc.wantWarn, logs)
			}
			assertReadAsAgent(t, f, p, tc.empty)
		})
	}
}
