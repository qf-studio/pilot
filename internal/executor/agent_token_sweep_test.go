package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TASK-651: the sweep of the token directories at the daemon start keeps the
// directory of a live run of another daemon process; the refresh makes a lost
// directory again or stops.

// tokenLogLines parses the JSON log lines of out.
func tokenLogLines(t *testing.T, logged string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logged), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// backdateTokenDir sets the modification time of dir two minutes back, past
// agentTokenSweepMinAge.
func backdateTokenDir(t *testing.T, dir string) {
	t.Helper()
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
}

// makeLeftTokenDir makes a token directory in tmp as a killed daemon leaves
// it, older than agentTokenSweepMinAge; with lock it also holds a lock file
// that no process locks.
func makeLeftTokenDir(t *testing.T, tmp, name string, lock bool) string {
	t.Helper()
	dir := filepath.Join(tmp, name)
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, agentTokenFileName), []byte("test-read-token-left"), 0o640); err != nil {
		t.Fatal(err)
	}
	if lock {
		if err := os.WriteFile(filepath.Join(dir, agentTokenLockName), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	backdateTokenDir(t, dir)
	return dir
}

// TestAgentTokenSweepKeepsLiveRun: the sweep keeps the token directory whose
// lock a live run holds, and its token file stays readable; one Info line
// names it. A left directory with no lock file or with a free lock goes. After
// the end of the run a second sweep finds nothing. Mutation pin: remove the
// agentTokenDirLocked check of sweepAgentTokenDirs and this fails.
func TestAgentTokenSweepKeepsLiveRun(t *testing.T) {
	_, tmp := tokenTestRunAs(t, true)
	p := &fakeReadTokenProvider{expiry: time.Hour}
	setAgentReadTokenProviderForTest(t, p.provide)

	f := startAgentTokenFile(context.Background(), slog.Default(), "qf-studio/pilot")
	if f == nil {
		t.Fatal("no token file")
	}
	defer f.Stop()
	backdateTokenDir(t, f.dir)
	noLock := makeLeftTokenDir(t, tmp, "pilot-ghtoken-nolock", false)
	freeLock := makeLeftTokenDir(t, tmp, "pilot-ghtoken-freelock", true)

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	if n := SweepAgentTokenDirs(log); n != 2 {
		t.Errorf("sweep removed %d, want 2 (the two left directories)", n)
	}
	if _, err := os.Stat(f.dir); err != nil {
		t.Fatalf("the token directory of the live run was removed: %v", err)
	}
	if got := readTokenFile(t, f.path); got != "test-read-token-1" {
		t.Errorf("token file of the live run = %q, want test-read-token-1", got)
	}
	for _, dir := range []string{noLock, freeLock} {
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("left directory %s still there: %v", filepath.Base(dir), err)
		}
	}
	var kept []string
	for _, rec := range tokenLogLines(t, buf.String()) {
		if rec["msg"] == "run_as_user: token directory of a live run kept" {
			if rec["level"] != "INFO" {
				t.Errorf("kept line level = %v, want INFO", rec["level"])
			}
			dir, _ := rec["dir"].(string)
			kept = append(kept, dir)
		}
	}
	if len(kept) != 1 || kept[0] != f.dir {
		t.Errorf("kept lines name %q, want one line for %s", kept, f.dir)
	}

	f.Stop()
	if n := SweepAgentTokenDirs(log); n != 0 {
		t.Errorf("second sweep after the end of the run removed %d, want 0", n)
	}
	if got := tokenDirs(t, tmp); len(got) != 0 {
		t.Errorf("token dirs after the end of the run: %v", got)
	}
}

// TestAgentTokenSweepKeepsDirOfAnotherOwner: the sweep removes no directory of
// another owner. The owner of a directory comes from the agentTokenDirOwned
// seam, since a test cannot make a directory of another user. Mutation pin:
// remove the owner check of sweepAgentTokenDirs and this fails.
func TestAgentTokenSweepKeepsDirOfAnotherOwner(t *testing.T) {
	_, tmp := tokenTestRunAs(t, true)
	other := makeLeftTokenDir(t, tmp, "pilot-ghtoken-other", false)
	mine := makeLeftTokenDir(t, tmp, "pilot-ghtoken-mine", false)
	prev := agentTokenDirOwned
	agentTokenDirOwned = func(fi os.FileInfo) bool {
		return fi.Name() != filepath.Base(other) && prev(fi)
	}
	t.Cleanup(func() { agentTokenDirOwned = prev })

	if n := SweepAgentTokenDirs(slog.Default()); n != 1 {
		t.Errorf("sweep removed %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(other, agentTokenFileName)); err != nil {
		t.Errorf("the directory of another owner was removed: %v", err)
	}
	if _, err := os.Lstat(mine); !os.IsNotExist(err) {
		t.Errorf("the left directory of the daemon user is still there: %v", err)
	}
}

// fireAgentTokenTimer replaces the timer of the refresh with one the test
// fires: each send on the returned channel ends one wait.
func fireAgentTokenTimer(t *testing.T) chan<- time.Time {
	t.Helper()
	fire := make(chan time.Time)
	prev := agentTokenTimer
	agentTokenTimer = func(time.Duration) (<-chan time.Time, func() bool) {
		return fire, func() bool { return true }
	}
	t.Cleanup(func() { agentTokenTimer = prev })
	return fire
}

// fireRefresh ends the next wait of the refresh, false when the refresh no
// longer waits.
func fireRefresh(fire chan<- time.Time, within time.Duration) bool {
	select {
	case fire <- time.Now():
		return true
	case <-time.After(within):
		return false
	}
}

// TestAgentTokenRefreshRemakesLostDir: a refresh after the token directory was
// removed makes it again with the same path, mode and group, with its lock
// (a later sweep keeps it) and its helper script, and writes the new token.
func TestAgentTokenRefreshRemakesLostDir(t *testing.T) {
	s, _ := tokenTestRunAs(t, true)
	fire := fireAgentTokenTimer(t)
	p := &fakeReadTokenProvider{expiry: time.Hour}
	setAgentReadTokenProviderForTest(t, p.provide)

	f := startAgentTokenFile(context.Background(), slog.Default(), "qf-studio/pilot")
	if f == nil {
		t.Fatal("no token file")
	}
	defer f.Stop()
	if f.helper == "" {
		t.Fatal("no helper script")
	}
	if err := os.RemoveAll(f.dir); err != nil {
		t.Fatal(err)
	}
	if !fireRefresh(fire, 5*time.Second) {
		t.Fatal("the refresh does not wait")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if raw, err := os.ReadFile(f.path); err == nil && string(raw) == "test-read-token-2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the refresh did not make the lost directory again")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The new token shows before remake writes the helper script; write holds
	// f.mu over the whole remake, so taking it waits for the remake to end.
	f.mu.Lock()
	f.mu.Unlock() //nolint:staticcheck // SA2001: the empty section waits for the write in flight
	for path, mode := range map[string]os.FileMode{f.dir: 0o750, f.path: 0o640, f.helper: 0o750} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		if fi.Mode().Perm() != mode {
			t.Errorf("%s mode = %o, want %o", filepath.Base(path), fi.Mode().Perm(), mode)
		}
		if gid := fileGID(t, path); gid != s.gid {
			t.Errorf("%s gid = %d, want %d", filepath.Base(path), gid, s.gid)
		}
	}
	if locked, err := agentTokenDirLocked(f.dir); !locked || err != nil {
		t.Errorf("the directory made again holds no held lock (err %v)", err)
	}
	backdateTokenDir(t, f.dir)
	if n := SweepAgentTokenDirs(slog.Default()); n != 0 {
		t.Errorf("a sweep removed %d directories, want the remade one kept", n)
	}
	if !fireRefresh(fire, 5*time.Second) {
		t.Error("the refresh stopped after the directory was made again")
	}
}

// TestAgentTokenRefreshStopsWhenTheDirCannotBeRemade: when the token directory
// is gone and cannot be made again, the refresh logs one Error line that names
// the run and stops: no more provider calls.
func TestAgentTokenRefreshStopsWhenTheDirCannotBeRemade(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a directory of mode 0500")
	}
	_, tmp := tokenTestRunAs(t, true)
	fire := fireAgentTokenTimer(t)
	p := &fakeReadTokenProvider{expiry: time.Hour}
	setAgentReadTokenProviderForTest(t, p.provide)
	var buf syncBuffer
	projectPath := "/srv/a/name"
	r := &Runner{log: slog.New(slog.NewJSONHandler(&buf, nil)), agentTokenRepoLookup: func(string) (string, bool) {
		return "qf-studio/pilot", true
	}}

	f := r.startRunAgentTokenFile(context.Background(), &Task{ID: "GH-6641", ProjectPath: projectPath})
	if f == nil {
		t.Fatal("no token file")
	}
	defer f.Stop()
	if err := os.RemoveAll(f.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tmp, 0o700) })
	if !fireRefresh(fire, 5*time.Second) {
		t.Fatal("the refresh does not wait")
	}
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh did not stop")
	}
	if got := p.callCount(); got != 2 {
		t.Errorf("provider calls = %d, want 2 (the start and the one refresh)", got)
	}
	if fireRefresh(fire, 50*time.Millisecond) {
		t.Error("the refresh still waits after it stopped")
	}
	var errs []map[string]any
	for _, rec := range tokenLogLines(t, buf.String()) {
		if rec["level"] == "ERROR" {
			errs = append(errs, rec)
		}
	}
	if len(errs) != 1 {
		t.Fatalf("error lines = %v, want one", errs)
	}
	if errs[0]["task_id"] != "GH-6641" || errs[0]["project_path"] != projectPath || errs[0]["dir"] != f.dir {
		t.Errorf("error line = %v, want task_id, project_path and dir of the run", errs[0])
	}
}

// TestAgentTokenRunLinesNameTheRun: each Warn line of the token file of a run
// has the fields task_id and project_path.
func TestAgentTokenRunLinesNameTheRun(t *testing.T) {
	tokenTestRunAs(t, true)
	const projectPath = "/srv/b/name"
	for _, tc := range []struct {
		name     string
		lookup   func(string) (string, bool)
		provider AgentReadTokenProvider
		task     *Task
	}{
		{"provider error", func(string) (string, bool) { return "OWNER-B/NAME", true },
			(&fakeReadTokenProvider{err: errors.New("test-provider-failure")}).provide, &Task{}},
		{"repository of another owner", func(string) (string, bool) { return "OWNER-B/NAME", true },
			(&repoTokenProvider{}).provide, &Task{SourceRepo: "OWNER-A/NAME"}},
		{"no config entry", func(string) (string, bool) { return "", false },
			(&repoTokenProvider{}).provide, &Task{}},
		{"no provider", func(string) (string, bool) { return "OWNER-B/NAME", true }, nil, &Task{}},
	} {
		setAgentReadTokenProviderForTest(t, tc.provider)
		var buf bytes.Buffer
		r := &Runner{log: slog.New(slog.NewJSONHandler(&buf, nil)), agentTokenRepoLookup: tc.lookup}
		tc.task.ID, tc.task.ProjectPath = "GH-6641", projectPath
		if f := r.startRunAgentTokenFile(context.Background(), tc.task); f != nil {
			f.Stop()
			t.Errorf("%s: a token file", tc.name)
		}
		lines := tokenLogLines(t, buf.String())
		if len(lines) == 0 {
			t.Errorf("%s: no Warn line", tc.name)
		}
		for _, rec := range lines {
			if rec["level"] == "WARN" && (rec["task_id"] != "GH-6641" || rec["project_path"] != projectPath) {
				t.Errorf("%s: Warn line %v lacks task_id or project_path", tc.name, rec)
			}
		}
	}
}

// TestAgentTokenFileCreateFollowsNoLink: a link at the name each create of the
// token directory makes — the temporary token file, the helper script, the
// lock file — fails the create, and its target is not written or made.
//
// O_EXCL does the work: with O_CREAT it refuses any name that exists, a link
// too, with or without a target. O_NOFOLLOW is the second guard and alone
// would refuse the link as well, so the test fails only when both are removed.
func TestAgentTokenFileCreateFollowsNoLink(t *testing.T) {
	tokenTestRunAs(t, true)
	for _, tc := range []struct {
		name   string
		create func(dir string) error
	}{
		{agentTokenFileName + ".tmp", func(dir string) error { return writeAgentTokenFile(dir, "test-read-token") }},
		{agentGitCredentialHelperName, func(dir string) error {
			_, err := writeAgentGitCredentialHelper(dir, filepath.Join(dir, agentTokenFileName))
			return err
		}},
		{agentTokenLockName, func(dir string) error {
			lock, err := lockAgentTokenDir(dir)
			if err == nil {
				_ = lock.Close()
			}
			return err
		}},
	} {
		for _, existing := range []bool{true, false} {
			dir := t.TempDir()
			target := filepath.Join(t.TempDir(), "target")
			if existing {
				if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, filepath.Join(dir, tc.name)); err != nil {
				t.Fatal(err)
			}
			if err := tc.create(dir); err == nil {
				t.Errorf("%s, existing target %v: the create through a link did not fail", tc.name, existing)
			}
			raw, err := os.ReadFile(target)
			switch {
			case existing && (err != nil || string(raw) != "original"):
				t.Errorf("%s: link target = %q, %v, want original", tc.name, raw, err)
			case !existing && err == nil:
				t.Errorf("%s: the create made the link target: %q", tc.name, raw)
			}
		}
	}
}

// TestAgentTokenRefreshStopsAtAForeignPath: when a link to a directory, a file
// or a directory of another owner is at the path of the lost token directory,
// the refresh writes no token file, logs one Error line and stops: no more
// provider calls for the run.
// mutation pin: write of agentTokenFile without the Lstat check before the write -> this test fails
func TestAgentTokenRefreshStopsAtAForeignPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		// put puts something at dir and returns a directory whose entries must
		// stay as they are ("" for none).
		put func(t *testing.T, dir string) string
	}{
		{"link to a directory", func(t *testing.T, dir string) string {
			target := t.TempDir()
			if err := os.Symlink(target, dir); err != nil {
				t.Fatal(err)
			}
			return target
		}},
		{"file", func(t *testing.T, dir string) string {
			if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
			return ""
		}},
		{"directory of another owner", func(t *testing.T, dir string) string {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			prev := agentTokenDirOwned
			agentTokenDirOwned = func(fi os.FileInfo) bool { return fi.Name() != filepath.Base(dir) && prev(fi) }
			t.Cleanup(func() { agentTokenDirOwned = prev })
			return dir
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokenTestRunAs(t, true)
			fire := fireAgentTokenTimer(t)
			p := &fakeReadTokenProvider{expiry: time.Hour}
			setAgentReadTokenProviderForTest(t, p.provide)
			var buf syncBuffer
			f := startAgentTokenFile(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)), "qf-studio/pilot")
			if f == nil {
				t.Fatal("no token file")
			}
			defer f.Stop()
			if err := os.RemoveAll(f.dir); err != nil {
				t.Fatal(err)
			}
			kept := tc.put(t, f.dir)
			if !fireRefresh(fire, 5*time.Second) {
				t.Fatal("the refresh does not wait")
			}
			select {
			case <-f.done:
			case <-time.After(5 * time.Second):
				t.Fatal("the refresh did not stop")
			}
			if got := p.callCount(); got != 2 {
				t.Errorf("provider calls = %d, want 2 (the start and the one refresh)", got)
			}
			if fireRefresh(fire, 50*time.Millisecond) {
				t.Error("the refresh still waits after it stopped")
			}
			if kept != "" {
				entries, err := os.ReadDir(kept)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Errorf("the refresh wrote into %s: %v", kept, entries)
				}
			}
			var errs []map[string]any
			for _, rec := range tokenLogLines(t, buf.String()) {
				if rec["level"] == "ERROR" {
					errs = append(errs, rec)
				}
			}
			if len(errs) != 1 || errs[0]["dir"] != f.dir {
				t.Errorf("error lines = %v, want one that names %s", errs, f.dir)
			}
		})
	}
}

// TestAgentTokenSweepLockFileOpen: a FIFO at the lock path does not block the
// sweep; a link at the lock path keeps the directory with one Warn line.
// mutation pin: agentTokenDirLocked without agentTokenNonBlock -> this test fails
func TestAgentTokenSweepLockFileOpen(t *testing.T) {
	t.Run("FIFO", func(t *testing.T) {
		if _, err := exec.LookPath("mkfifo"); err != nil {
			t.Skip("mkfifo not installed")
		}
		_, tmp := tokenTestRunAs(t, true)
		dir := makeLeftTokenDir(t, tmp, "pilot-ghtoken-fifo", false)
		if out, err := exec.Command("mkfifo", filepath.Join(dir, agentTokenLockName)).CombinedOutput(); err != nil {
			t.Fatalf("mkfifo: %v: %s", err, out)
		}
		backdateTokenDir(t, dir)
		done := make(chan int, 1)
		go func() { done <- SweepAgentTokenDirs(slog.Default()) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			// Unblock the open of the sweep so the test process can end.
			if w, err := os.OpenFile(filepath.Join(dir, agentTokenLockName), os.O_WRONLY, 0); err == nil {
				_ = w.Close()
			}
			t.Fatal("the sweep blocks on a FIFO at the lock path")
		}
	})

	t.Run("link", func(t *testing.T) {
		_, tmp := tokenTestRunAs(t, true)
		dir := makeLeftTokenDir(t, tmp, "pilot-ghtoken-link", false)
		target := filepath.Join(t.TempDir(), "lock")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, agentTokenLockName)); err != nil {
			t.Fatal(err)
		}
		backdateTokenDir(t, dir)
		var buf bytes.Buffer
		if n := SweepAgentTokenDirs(slog.New(slog.NewJSONHandler(&buf, nil))); n != 0 {
			t.Errorf("sweep removed %d, want 0", n)
		}
		if _, err := os.Stat(filepath.Join(dir, agentTokenFileName)); err != nil {
			t.Errorf("the directory with a link at its lock path was removed: %v", err)
		}
		var warns []map[string]any
		for _, rec := range tokenLogLines(t, buf.String()) {
			if rec["level"] == "WARN" {
				warns = append(warns, rec)
			}
		}
		if len(warns) != 1 || warns[0]["dir"] != dir {
			t.Errorf("warn lines = %v, want one that names %s", warns, dir)
		}
	})
}

// TestAgentTokenSweepKeepsYoungDir: a token directory younger than
// agentTokenSweepMinAge with no lock file (between its creation and its lock)
// is kept.
// mutation pin: sweepAgentTokenDirs without the age check -> this test fails
func TestAgentTokenSweepKeepsYoungDir(t *testing.T) {
	_, tmp := tokenTestRunAs(t, true)
	dir := makeLeftTokenDir(t, tmp, "pilot-ghtoken-young", false)
	now := time.Now()
	if err := os.Chtimes(dir, now, now); err != nil {
		t.Fatal(err)
	}
	if n := SweepAgentTokenDirs(slog.Default()); n != 0 {
		t.Errorf("sweep removed %d, want 0", n)
	}
	if _, err := os.Stat(filepath.Join(dir, agentTokenFileName)); err != nil {
		t.Errorf("the young directory was removed: %v", err)
	}
}
