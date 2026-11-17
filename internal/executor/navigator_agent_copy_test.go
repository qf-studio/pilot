package executor

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TASK-651 (design 4.2): the Navigator copy with run_as_user set, on the
// fixture of agent_files_test.go (the workspace root is writable only while
// the mock sudo runs its command). No test needs root.

// navigatorTreeEntry is one entry of a .agent tree: type, permission bits,
// content and modification time.
type navigatorTreeEntry struct {
	dir     bool
	perm    os.FileMode
	content string
	mtime   time.Time
}

// navigatorTree returns the entries below root/.agent by slash path.
func navigatorTree(t *testing.T, root string) map[string]navigatorTreeEntry {
	t.Helper()
	tree := map[string]navigatorTreeEntry{}
	base := filepath.Join(root, ".agent")
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		e := navigatorTreeEntry{dir: d.IsDir(), perm: info.Mode().Perm()}
		if !d.IsDir() {
			e.content = readTestFile(t, path)
			e.mtime = info.ModTime()
		}
		tree[filepath.ToSlash(rel)] = e
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk %s: %v", base, err)
	}
	return tree
}

// newNavigatorSource is a git repository with a .agent directory: one file
// that git tracks and files that git does not track, with old mtimes.
func newNavigatorSource(t *testing.T) string {
	t.Helper()
	src, _ := newCopyBackRepos(t)
	old := time.Now().Add(-72 * time.Hour).Truncate(time.Second).Add(123456789)
	writeCopyBackFile(t, src, ".agent/DEVELOPMENT-README.md", "tracked readme\n", old)
	runGit(t, src, "add", ".agent")
	runGit(t, src, "commit", "-m", "track readme")
	writeCopyBackFile(t, src, ".agent/.context-markers/marker.md", "untracked marker\n", old.Add(time.Minute))
	writeCopyBackFile(t, src, ".agent/sops/custom/sop.md", "untracked sop\n", old.Add(2*time.Minute))
	script := writeCopyBackFile(t, src, ".agent/scripts/run.sh", "#!/bin/sh\n", old)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return src
}

// TestNavigatorCopyIn_RunAsUser is item 1: with the option set the copy into
// the workspace is one tar process as the agent user and gives the files of
// today's copy, the ones git does not track too.
func TestNavigatorCopyIn_RunAsUser(t *testing.T) {
	tests := []struct {
		name     string
		noSource bool
		// seed writes into a workspace before the copy (both workspaces).
		seed     func(t *testing.T, ws string)
		wantSudo int
	}{
		{name: "tracked and untracked files", wantSudo: 1},
		{
			name: "workspace already holds .agent from git",
			seed: func(t *testing.T, ws string) {
				writeCopyBackFile(t, ws, ".agent/DEVELOPMENT-README.md", "from the clone\n", time.Now())
				writeCopyBackFile(t, ws, ".agent/tasks/only-in-ws.md", "kept\n", time.Now())
			},
			wantSudo: 1,
		},
		{name: "source without .agent: nothing to copy, no tar", noSource: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var src string
			if tc.noSource {
				src, _ = newCopyBackRepos(t)
			} else {
				src = newNavigatorSource(t)
			}
			// Today's copy, into a plain directory.
			want := t.TempDir()
			if tc.seed != nil {
				tc.seed(t, want)
			}
			if err := CopyNavigatorToWorktree(src, want); err != nil {
				t.Fatal(err)
			}

			f := newAgentFilesFixture(t, "")
			if tc.seed != nil {
				tc.seed(t, f.ws)
			}
			f.lock(t)
			if err := EnsureNavigatorInWorktree(src, f.ws); err != nil {
				t.Fatalf("EnsureNavigatorInWorktree: %v", err)
			}

			// The lock of the fixture takes the write bits; asAgent gives the
			// owner's back, as the files of the source have them.
			wantTree := navigatorTree(t, want)
			var gotTree map[string]navigatorTreeEntry
			f.asAgent(t, func() { gotTree = navigatorTree(t, f.ws) })
			if tc.seed != nil {
				// The seeded files differ in mtime between the two workspaces.
				for _, rel := range []string{".agent/tasks/only-in-ws.md"} {
					w, g := wantTree[rel], gotTree[rel]
					w.mtime, g.mtime = time.Time{}, time.Time{}
					wantTree[rel], gotTree[rel] = w, g
				}
			}
			for rel, w := range wantTree {
				g, ok := gotTree[rel]
				switch {
				case !ok:
					t.Errorf("%s missing in the workspace", rel)
				case g.dir != w.dir || g.perm != w.perm || g.content != w.content:
					t.Errorf("%s = %+v, want %+v", rel, g, w)
				case !g.mtime.Equal(w.mtime):
					t.Errorf("%s mtime = %v, want %v", rel, g.mtime, w.mtime)
				}
			}
			for rel := range gotTree {
				if _, ok := wantTree[rel]; !ok {
					t.Errorf("%s in the workspace, not in today's copy", rel)
				}
			}
			if !tc.noSource {
				if got := gotTree[".agent/.context-markers/marker.md"].content; got != "untracked marker\n" {
					t.Errorf("untracked file = %q", got)
				}
			}

			calls := f.calls(t)
			if len(calls) != tc.wantSudo {
				t.Fatalf("sudo ran %d times (%q), want %d", len(calls), calls, tc.wantSudo)
			}
			for _, c := range calls {
				if !strings.HasSuffix(c, "-- tar -C "+f.ws+" -x -f -") {
					t.Errorf("sudo call %q, want tar into the workspace", c)
				}
			}
		})
	}
}

// TestNavigatorTarStream pins the stream: names below .agent/, directories
// first, permission bits and modification times of the source.
func TestNavigatorTarStream(t *testing.T) {
	src := newNavigatorSource(t)
	var buf bytes.Buffer
	if err := navigatorTarStream(&buf, filepath.Join(src, ".agent")); err != nil {
		t.Fatal(err)
	}
	seen := map[string]*tar.Header{}
	var order []string
	tr := tar.NewReader(&buf)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen[h.Name] = h
		order = append(order, h.Name)
	}
	if len(order) == 0 || order[0] != ".agent/" {
		t.Fatalf("first entry %q, want .agent/", order)
	}
	tests := []struct {
		name string
		typ  byte
		mode int64
		rel  string
	}{
		{".agent/scripts/", tar.TypeDir, 0o755, ".agent/scripts"},
		{".agent/scripts/run.sh", tar.TypeReg, 0o755, ".agent/scripts/run.sh"},
		{".agent/.context-markers/marker.md", tar.TypeReg, 0o644, ".agent/.context-markers/marker.md"},
		{".agent/DEVELOPMENT-README.md", tar.TypeReg, 0o644, ".agent/DEVELOPMENT-README.md"},
	}
	for _, tc := range tests {
		h := seen[tc.name]
		if h == nil {
			t.Errorf("%s not in the stream (%q)", tc.name, order)
			continue
		}
		if h.Typeflag != tc.typ || h.Mode != tc.mode {
			t.Errorf("%s: type %c mode %o, want %c %o", tc.name, h.Typeflag, h.Mode, tc.typ, tc.mode)
		}
		info, err := os.Stat(filepath.Join(src, filepath.FromSlash(tc.rel)))
		if err != nil {
			t.Fatal(err)
		}
		if tc.typ == tar.TypeReg && !h.ModTime.Equal(info.ModTime()) {
			t.Errorf("%s: mtime %v, want %v", tc.name, h.ModTime, info.ModTime())
		}
		if strings.HasPrefix(h.Name, "/") || strings.Contains(h.Name, "..") {
			t.Errorf("unsafe name %q", h.Name)
		}
	}
}

// TestNavigatorCopyOut_RunAsUser is item 2: with the option set the copy out
// of the workspace copies a regular file, copies nothing through a link at
// .agent, at a directory below it or at a file, and stops with one Warn line
// that names the task when the tracked-path list of the workspace fails.
func TestNavigatorCopyOut_RunAsUser(t *testing.T) {
	const (
		taskID  = "GH-6417-copy-out"
		leakRel = ".agent/knowledge/memories/patterns/leak.md"
		graph   = ".agent/knowledge/graph.json"
	)
	// outsideAgent fills dir (outside the workspace) with what a link would
	// expose: a memory and a graph, as an .agent directory would hold them.
	outsideAgent := func(t *testing.T, dir string) {
		writeTestFile(t, filepath.Join(dir, "knowledge", "memories", "patterns", "leak.md"), "secret of the daemon\n")
		writeTestFile(t, filepath.Join(dir, "knowledge", "graph.json"), `{"nodes":{"leak":{}}}`)
	}
	tests := []struct {
		name string
		// setup writes into the workspace (still writable); outside is a
		// directory outside the workspace root.
		setup      func(t *testing.T, ws, outside string)
		noGit      bool
		wantCopied map[string]string
		wantAbsent []string
		wantWarn   string // "" is no Warn line
	}{
		{
			name: "a regular file is copied",
			setup: func(t *testing.T, ws, _ string) {
				writeCopyBackFile(t, ws, copyBackMemoryRel, "learned\n", time.Now())
				writeCopyBackFile(t, ws, graph, `{"nodes":{"ws":{}}}`, time.Now())
			},
			wantCopied: map[string]string{copyBackMemoryRel: "learned\n", graph: `{"nodes":{"ws":{}}}`},
		},
		{
			name: "a link at .agent",
			setup: func(t *testing.T, ws, outside string) {
				outsideAgent(t, outside)
				if err := os.Symlink(outside, filepath.Join(ws, ".agent")); err != nil {
					t.Fatal(err)
				}
			},
			wantAbsent: []string{leakRel, graph},
		},
		{
			name: "a link at a directory below .agent",
			setup: func(t *testing.T, ws, outside string) {
				outsideAgent(t, outside)
				if err := os.MkdirAll(filepath.Join(ws, ".agent"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "knowledge"), filepath.Join(ws, ".agent", "knowledge")); err != nil {
					t.Fatal(err)
				}
			},
			wantAbsent: []string{leakRel, graph},
		},
		{
			name: "a link at a file",
			setup: func(t *testing.T, ws, outside string) {
				outsideAgent(t, outside)
				dir := filepath.Join(ws, ".agent", "knowledge", "memories", "patterns")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "knowledge", "memories", "patterns", "leak.md"), filepath.Join(dir, "leak.md")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "knowledge", "graph.json"), filepath.Join(ws, filepath.FromSlash(graph))); err != nil {
					t.Fatal(err)
				}
			},
			wantAbsent: []string{leakRel, graph},
		},
		{
			name:  "a failed tracked-path list copies nothing",
			noGit: true,
			setup: func(t *testing.T, ws, _ string) {
				writeCopyBackFile(t, ws, copyBackMemoryRel, "learned\n", time.Now())
			},
			wantAbsent: []string{copyBackMemoryRel},
			wantWarn:   "list tracked navigator files in",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clone, _ := newCopyBackRepos(t)
			outside := t.TempDir()
			f := newAgentFilesFixture(t, "")
			if !tc.noGit {
				runGit(t, f.ws, "init", "-q", "-b", "main")
			}
			tc.setup(t, f.ws, outside)
			f.lock(t)

			var logs bytes.Buffer
			runner := NewRunnerWithBackend(&ctxRespectingBackend{})
			runner.log = slog.New(slog.NewTextHandler(&logs, nil))
			runner.copyNavigatorBack(taskID, f.ws, clone)

			for rel, want := range tc.wantCopied {
				if got := readCopyBackFile(t, clone, rel); got != want {
					t.Errorf("%s = %q, want %q", rel, got, want)
				}
			}
			for _, rel := range tc.wantAbsent {
				assertNoPath(t, filepath.Join(clone, filepath.FromSlash(rel)))
			}
			var warns []string
			for _, line := range strings.Split(logs.String(), "\n") {
				if strings.Contains(line, "level=WARN") {
					warns = append(warns, line)
				}
			}
			if tc.wantWarn == "" {
				if len(warns) != 0 {
					t.Errorf("Warn lines %q, want none", warns)
				}
			} else if len(warns) != 1 || !strings.Contains(warns[0], "task_id="+taskID) || !strings.Contains(warns[0], tc.wantWarn) {
				t.Errorf("Warn lines %q, want one with task_id=%s and %q", warns, taskID, tc.wantWarn)
			}
			calls := f.calls(t)
			if len(calls) == 0 {
				t.Fatal("no git command ran as the agent user")
			}
			for _, c := range calls {
				if !strings.Contains(c, "-- git -C "+f.ws+" ls-files") {
					t.Errorf("sudo call %q, want git ls-files in the workspace", c)
				}
			}
		})
	}
}

// TestNavigatorCopyDest_RunAsUser: a destination that leaves the .agent
// directory of the clone, as given or through a link, is rejected.
func TestNavigatorCopyDest_RunAsUser(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".agent", "sops"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, ".agent", "system")); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		rel     string
		wantErr bool
	}{
		{".agent/sops/a.md", false},
		{".agent/knowledge/memories/new/b.md", false},
		{".agent/system/c.md", true},
		{".agent/../x.md", true},
		{".agent", true},
	}
	for _, tc := range tests {
		t.Run(tc.rel, func(t *testing.T) {
			dst, err := navigatorCopyBackDest(repo, tc.rel)
			if (err != nil) != tc.wantErr {
				t.Fatalf("navigatorCopyBackDest(%q) = %q, %v; want error %v", tc.rel, dst, err, tc.wantErr)
			}
		})
	}
}

// TestNavigatorCopy_OptionEmpty_NoAgentCommand: with the option empty both
// copies are the calls of today, no sudo runs.
func TestNavigatorCopy_OptionEmpty_NoAgentCommand(t *testing.T) {
	f := newAgentFilesFixture(t, "")
	setRunAsStateForTest(t, nil, "")
	src := newNavigatorSource(t)
	wt := t.TempDir()
	if err := EnsureNavigatorInWorktree(src, wt); err != nil {
		t.Fatal(err)
	}
	var got []string
	for rel := range navigatorTree(t, wt) {
		got = append(got, rel)
	}
	sort.Strings(got)
	if !strings.Contains(strings.Join(got, " "), ".agent/.context-markers/marker.md") {
		t.Errorf("copy in = %q, want the untracked marker", got)
	}

	clone, _ := newCopyBackRepos(t)
	writeCopyBackFile(t, wt, copyBackMemoryRel, "learned\n", time.Now())
	if _, err := CopyNavigatorBackFromWorktree(wt, clone); err != nil {
		t.Fatal(err)
	}
	if got := readCopyBackFile(t, clone, copyBackMemoryRel); got != "learned\n" {
		t.Errorf("copy out = %q", got)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("sudo ran %q with the option empty", calls)
	}
}
