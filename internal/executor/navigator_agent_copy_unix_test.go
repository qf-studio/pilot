//go:build unix

package executor

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// GH-6428 (TASK-651): the Navigator copy with run_as_user set holds one
// descriptor of the workspace for the whole copy out, follows no link in the
// copy in, and skips a file over the size limit and a hard link. No test needs
// root.

// warnLines returns the Warn lines of a text log.
func warnLines(logs string) []string {
	var warns []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "level=WARN") {
			warns = append(warns, line)
		}
	}
	return warns
}

// TestNavigatorCopyOut_RunAsUserWorkspaceFD: every read of the copy out comes
// from the one descriptor of the workspace, and a file that is not a regular
// file with one link and at most the size limit is skipped.
func TestNavigatorCopyOut_RunAsUserWorkspaceFD(t *testing.T) {
	const (
		taskID = "GH-6428-copy-out"
		graph  = ".agent/knowledge/graph.json"
	)
	// outsideAgent is an .agent directory outside the workspace root with a
	// memory at copyBackMemoryRel and a graph.
	outsideAgent := func(t *testing.T, dir string) {
		writeCopyBackFile(t, dir, copyBackMemoryRel, "secret of the daemon\n", time.Now())
		writeCopyBackFile(t, dir, graph, `{"nodes":{"leak":{}}}`, time.Now())
	}
	tests := []struct {
		name string
		// setup writes into the workspace (still writable) and returns the
		// workspace path of the copy ("" is f.ws); outside is a directory
		// outside the workspace root.
		setup func(t *testing.T, f *agentFilesFixture, outside string) string
		// beforeWalk runs after the workspace is open, before any read.
		beforeWalk func(t *testing.T, f *agentFilesFixture, outside string)
		wantCopied map[string]string
		wantAbsent []string
		wantFiles  int    // regular files below the memories of the clone, -1 is not checked
		wantWarn   string // "" is no Warn line
		wantNoGit  bool
	}{
		{
			name: "the workspace name is a link to an outside directory",
			setup: func(t *testing.T, f *agentFilesFixture, outside string) string {
				outsideAgent(t, outside)
				runGit(t, outside, "init", "-q", "-b", "main")
				link := filepath.Join(f.root, agentWorkspacePrefix+"0123abcd-GH-2-2")
				if err := os.Symlink(outside, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			wantAbsent: []string{copyBackMemoryRel, graph},
			wantFiles:  -1,
			wantWarn:   "Navigator copy-back incomplete",
			wantNoGit:  true,
		},
		{
			name: "a workspace path outside the workspace root",
			setup: func(t *testing.T, _ *agentFilesFixture, outside string) string {
				outsideAgent(t, outside)
				runGit(t, outside, "init", "-q", "-b", "main")
				return outside
			},
			wantAbsent: []string{copyBackMemoryRel, graph},
			wantFiles:  -1,
			wantWarn:   "not a directory directly below the workspace root",
			wantNoGit:  true,
		},
		{
			name: "the workspace is renamed and replaced by a link after the open",
			setup: func(t *testing.T, f *agentFilesFixture, outside string) string {
				outsideAgent(t, outside)
				writeCopyBackFile(t, f.ws, copyBackMemoryRel, "learned\n", time.Now())
				writeCopyBackFile(t, f.ws, graph, `{"nodes":{"ws":{}}}`, time.Now())
				return ""
			},
			beforeWalk: func(t *testing.T, f *agentFilesFixture, outside string) {
				f.asAgent(t, func() {
					if err := os.Rename(f.ws, f.ws+"-moved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, f.ws); err != nil {
						t.Fatal(err)
					}
				})
			},
			wantCopied: map[string]string{copyBackMemoryRel: "learned\n", graph: `{"nodes":{"ws":{}}}`},
			wantFiles:  1,
		},
		{
			name: "a file of the size limit is copied, one byte more is skipped",
			setup: func(t *testing.T, f *agentFilesFixture, _ string) string {
				writeCopyBackFile(t, f.ws, ".agent/sops/at-limit.md", strings.Repeat("a", navigatorCopyOutFileLimit), time.Now())
				writeCopyBackFile(t, f.ws, ".agent/sops/over-limit.md", strings.Repeat("a", navigatorCopyOutFileLimit+1), time.Now())
				return ""
			},
			wantCopied: map[string]string{".agent/sops/at-limit.md": strings.Repeat("a", navigatorCopyOutFileLimit)},
			wantAbsent: []string{".agent/sops/over-limit.md"},
			wantFiles:  -1,
			wantWarn:   "over the size limit",
		},
		{
			name: "a hard link is skipped",
			setup: func(t *testing.T, f *agentFilesFixture, outside string) string {
				outsideAgent(t, outside)
				dst := filepath.Join(f.ws, filepath.FromSlash(copyBackMemoryRel))
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(outside, filepath.FromSlash(copyBackMemoryRel)), dst); err != nil {
					t.Skipf("hard link: %v", err)
				}
				return ""
			},
			wantAbsent: []string{copyBackMemoryRel},
			wantFiles:  0,
			wantWarn:   "more than one link",
		},
		{
			name: "a directory at a file path is skipped",
			setup: func(t *testing.T, f *agentFilesFixture, _ string) string {
				writeTestFile(t, filepath.Join(f.ws, filepath.FromSlash(graph), "inner"), "x")
				return ""
			},
			wantAbsent: []string{graph},
			wantFiles:  -1,
		},
		{
			name: "a FIFO at a file path is skipped",
			setup: func(t *testing.T, f *agentFilesFixture, _ string) string {
				p := filepath.Join(f.ws, filepath.FromSlash(graph))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(p, 0o644); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			wantAbsent: []string{graph},
			wantFiles:  -1,
		},
		{
			name: "more than the file limit stops the copy",
			setup: func(t *testing.T, f *agentFilesFixture, _ string) string {
				dir := filepath.Join(f.ws, ".agent", "knowledge", "memories", "many")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				for i := 0; i <= navigatorCopyOutMaxFiles; i++ {
					if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("m-%04d.md", i)), []byte("m\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				writeCopyBackFile(t, f.ws, graph, `{"nodes":{"ws":{}}}`, time.Now())
				return ""
			},
			wantAbsent: []string{graph},
			wantFiles:  navigatorCopyOutMaxFiles,
			wantWarn:   fmt.Sprintf("more than %d files", navigatorCopyOutMaxFiles),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clone, _ := newCopyBackRepos(t)
			outside := t.TempDir()
			f := newAgentFilesFixture(t, "")
			runGit(t, f.ws, "init", "-q", "-b", "main")
			wsPath := tc.setup(t, f, outside)
			if wsPath == "" {
				wsPath = f.ws
			}
			f.lock(t)
			if tc.beforeWalk != nil {
				navigatorCopyOutBeforeWalk = func(string) { tc.beforeWalk(t, f, outside) }
				t.Cleanup(func() { navigatorCopyOutBeforeWalk = nil })
			}

			var logs bytes.Buffer
			runner := NewRunnerWithBackend(&ctxRespectingBackend{})
			runner.log = slog.New(slog.NewTextHandler(&logs, nil))
			runner.copyNavigatorBack(taskID, wsPath, clone)

			for rel, want := range tc.wantCopied {
				if got := readCopyBackFile(t, clone, rel); got != want {
					t.Errorf("%s = %.40q, want %.40q", rel, got, want)
				}
			}
			for _, rel := range tc.wantAbsent {
				assertNoPath(t, filepath.Join(clone, filepath.FromSlash(rel)))
			}
			if tc.wantFiles >= 0 {
				n := 0
				_ = filepath.Walk(filepath.Join(clone, ".agent", "knowledge", "memories"), func(_ string, info os.FileInfo, err error) error {
					if err == nil && info.Mode().IsRegular() {
						n++
					}
					return nil
				})
				if n != tc.wantFiles {
					t.Errorf("%d files copied below the memories, want %d", n, tc.wantFiles)
				}
			}
			warns := warnLines(logs.String())
			if tc.wantWarn == "" {
				if len(warns) != 0 {
					t.Errorf("Warn lines %q, want none", warns)
				}
			} else if len(warns) != 1 || !strings.Contains(warns[0], "task_id="+taskID) || !strings.Contains(warns[0], tc.wantWarn) {
				t.Errorf("Warn lines %q, want one with task_id=%s and %q", warns, taskID, tc.wantWarn)
			}
			calls := f.calls(t)
			if tc.wantNoGit && len(calls) != 0 {
				t.Errorf("sudo ran %q, want no command for a refused workspace", calls)
			}
		})
	}
}

// TestNavigatorTarStream_NoFollow: a link and a FIFO in the daemon's .agent
// are not in the stream, each with one Warn line that names it, and the
// stream builder returns.
func TestNavigatorTarStream_NoFollow(t *testing.T) {
	tests := []struct {
		name string
		// setup adds entries to the source .agent; outside is a directory
		// outside the repository.
		setup      func(t *testing.T, agent, outside string)
		rootLink   bool // the stream starts at a link to .agent
		wantAbsent []string
		wantWarns  []string
	}{
		{
			name: "a link to a file",
			setup: func(t *testing.T, agent, outside string) {
				writeTestFile(t, filepath.Join(outside, "config.yaml"), "secret: daemon\n")
				if err := os.Symlink(filepath.Join(outside, "config.yaml"), filepath.Join(agent, "sops", "config.yaml")); err != nil {
					t.Fatal(err)
				}
			},
			wantAbsent: []string{".agent/sops/config.yaml"},
			wantWarns:  []string{filepath.Join("sops", "config.yaml")},
		},
		{
			name: "a link to a directory",
			setup: func(t *testing.T, agent, outside string) {
				writeTestFile(t, filepath.Join(outside, "secrets", "key"), "k\n")
				if err := os.Symlink(filepath.Join(outside, "secrets"), filepath.Join(agent, "system")); err != nil {
					t.Fatal(err)
				}
			},
			wantAbsent: []string{".agent/system/", ".agent/system/key"},
			wantWarns:  []string{filepath.Join(".agent", "system")},
		},
		{
			name: "a FIFO",
			setup: func(t *testing.T, agent, _ string) {
				if err := syscall.Mkfifo(filepath.Join(agent, "sops", "pipe"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantAbsent: []string{".agent/sops/pipe"},
			wantWarns:  []string{filepath.Join("sops", "pipe")},
		},
		{
			name:       "a link at .agent",
			setup:      func(*testing.T, string, string) {},
			rootLink:   true,
			wantAbsent: []string{".agent/", ".agent/sops/custom/sop.md"},
			wantWarns:  []string{"agent-link"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			src := newNavigatorSource(t)
			agent := filepath.Join(src, ".agent")
			outside := t.TempDir()
			tc.setup(t, agent, outside)
			start := agent
			if tc.rootLink {
				start = filepath.Join(outside, "agent-link")
				if err := os.Symlink(agent, start); err != nil {
					t.Fatal(err)
				}
			}

			var buf bytes.Buffer
			done := make(chan error, 1)
			go func() { done <- navigatorTarStream(&buf, start) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("navigatorTarStream: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("navigatorTarStream did not return")
			}

			seen := map[string]bool{}
			tr := tar.NewReader(&buf)
			for {
				h, err := tr.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				seen[h.Name] = true
				if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
					t.Errorf("%s: type %c, want a directory or a regular file", h.Name, h.Typeflag)
				}
			}
			for _, name := range tc.wantAbsent {
				if seen[name] {
					t.Errorf("%s in the stream", name)
				}
			}
			if !tc.rootLink && !seen[".agent/sops/custom/sop.md"] {
				t.Errorf("regular file missing from the stream (%v)", seen)
			}
			warns := warnLines(logs.String())
			if len(warns) != len(tc.wantWarns) {
				t.Fatalf("Warn lines %q, want %d", warns, len(tc.wantWarns))
			}
			for i, want := range tc.wantWarns {
				if !strings.Contains(warns[i], want) {
					t.Errorf("Warn line %q, want the path %q", warns[i], want)
				}
			}
		})
	}
}

// TestNavigatorCopy_OptionEmpty_FollowsAsToday: with the option empty a link
// in the source .agent is followed by the copy in and a hard link in the
// worktree is copied out, as before, and no sudo runs.
func TestNavigatorCopy_OptionEmpty_FollowsAsToday(t *testing.T) {
	f := newAgentFilesFixture(t, "")
	setRunAsStateForTest(t, nil, "")
	src := newNavigatorSource(t)
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "linked.md"), "through the link\n")
	if err := os.Symlink(filepath.Join(outside, "linked.md"), filepath.Join(src, ".agent", "sops", "linked.md")); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	if err := EnsureNavigatorInWorktree(src, wt); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, filepath.Join(wt, ".agent", "sops", "linked.md")); got != "through the link\n" {
		t.Errorf("copy in of a link = %q", got)
	}

	clone, _ := newCopyBackRepos(t)
	writeTestFile(t, filepath.Join(outside, "hard.md"), "hard link\n")
	dst := filepath.Join(wt, filepath.FromSlash(copyBackMemoryRel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(outside, "hard.md"), dst); err != nil {
		t.Skipf("hard link: %v", err)
	}
	if _, err := CopyNavigatorBackFromWorktree(wt, clone); err != nil {
		t.Fatal(err)
	}
	if got := readCopyBackFile(t, clone, copyBackMemoryRel); got != "hard link\n" {
		t.Errorf("copy out of a hard link = %q", got)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("sudo ran %q with the option empty", calls)
	}
}
