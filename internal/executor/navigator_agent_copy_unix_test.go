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

// GH-6428 (TASK-651): the copy out holds one descriptor of the workspace from
// its start to its end and skips a hard link, a file over the size limit and
// every entry that is not a regular file; the copy in follows no link. No test
// needs root.

// navigatorWarns returns the Warn lines of a text log.
func navigatorWarns(logs string) []string {
	var warns []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "level=WARN") {
			warns = append(warns, line)
		}
	}
	return warns
}

// TestNavigatorCopyOut_RunAsUserDescriptor is items 1, 3 and 4 and the
// regular-file rule of the copy out.
func TestNavigatorCopyOut_RunAsUserDescriptor(t *testing.T) {
	const (
		taskID = "GH-6428-copy-out"
		graph  = ".agent/knowledge/graph.json"
		secret = "secret of the daemon\n"
	)
	big := strings.Repeat("a", navigatorCopyOutFileLimit)
	tests := []struct {
		name string
		// setup writes into the workspace root (still writable) and returns
		// the workspace path of the copy; outside is a directory outside the
		// root with secret at copyBackMemoryRel and a git repository.
		setup func(t *testing.T, f *agentFilesFixture, outside string) string
		// sudoExtra is shell code of the mock sudo before each command of the
		// agent user, with WS, ROOT and OUTSIDE set.
		sudoExtra  string
		maxFiles   int // 0 is the default limit
		wantCopied map[string]string
		wantAbsent []string
		wantWarns  []string // substrings, one per Warn line in order
		wantNoGit  bool
	}{
		{
			name: "the workspace name is a link to an outside directory",
			setup: func(t *testing.T, f *agentFilesFixture, outside string) string {
				link := filepath.Join(f.root, agentWorkspacePrefix+"0123abcd-GH-2-2")
				if err := os.Symlink(outside, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			wantAbsent: []string{copyBackMemoryRel},
			wantWarns:  []string{"Navigator copy-back incomplete"},
			wantNoGit:  true,
		},
		{
			name:       "a workspace path outside the workspace root",
			setup:      func(_ *testing.T, _ *agentFilesFixture, outside string) string { return outside },
			wantAbsent: []string{copyBackMemoryRel},
			wantWarns:  []string{"not directly below the workspace root"},
			wantNoGit:  true,
		},
		{
			name: "the workspace is renamed and replaced by a link after the open",
			setup: func(t *testing.T, f *agentFilesFixture, _ string) string {
				writeCopyBackFile(t, f.ws, copyBackMemoryRel, "learned\n", time.Now())
				writeCopyBackFile(t, f.ws, graph, `{"nodes":{"ws":{}}}`, time.Now())
				return f.ws
			},
			// The agent user swaps its workspace during the last list of
			// the daemon (ls-files -m), after the open of the workspace and
			// after the git route check of that list, before any read.
			sudoExtra:  `case " $* " in *" -m "*) chmod u+w "$ROOT" && mv "$WS" "$WS-moved" && ln -s "$OUTSIDE" "$WS" ;; esac`,
			wantCopied: map[string]string{copyBackMemoryRel: "learned\n", graph: `{"nodes":{"ws":{}}}`},
		},
		{
			name: "a file of the size limit is copied, one byte more is skipped",
			setup: func(t *testing.T, f *agentFilesFixture, _ string) string {
				writeCopyBackFile(t, f.ws, ".agent/sops/at-limit.md", big, time.Now())
				writeCopyBackFile(t, f.ws, ".agent/sops/over-limit.md", big+"a", time.Now())
				return f.ws
			},
			wantCopied: map[string]string{".agent/sops/at-limit.md": big},
			wantAbsent: []string{".agent/sops/over-limit.md"},
			wantWarns:  []string{"over the size limit"},
		},
		{
			name: "a hard link is skipped",
			setup: func(t *testing.T, f *agentFilesFixture, outside string) string {
				dst := filepath.Join(f.ws, filepath.FromSlash(copyBackMemoryRel))
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(outside, filepath.FromSlash(copyBackMemoryRel)), dst); err != nil {
					t.Skipf("hard link: %v", err)
				}
				return f.ws
			},
			wantAbsent: []string{copyBackMemoryRel},
			wantWarns:  []string{"more than one link"},
		},
		{
			name: "a directory at a file path is skipped",
			setup: func(t *testing.T, f *agentFilesFixture, _ string) string {
				writeTestFile(t, filepath.Join(f.ws, filepath.FromSlash(graph), "inner"), "x")
				return f.ws
			},
			wantAbsent: []string{graph},
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
				return f.ws
			},
			wantAbsent: []string{graph},
		},
		{
			name: "one file more than the file limit stops the copy",
			setup: func(t *testing.T, f *agentFilesFixture, _ string) string {
				for i := 0; i < 3; i++ {
					writeCopyBackFile(t, f.ws, fmt.Sprintf(".agent/knowledge/memories/many/m-%d.md", i), "m\n", time.Now())
				}
				writeCopyBackFile(t, f.ws, graph, `{"nodes":{"ws":{}}}`, time.Now())
				return f.ws
			},
			maxFiles: 2,
			wantCopied: map[string]string{
				".agent/knowledge/memories/many/m-0.md": "m\n",
				".agent/knowledge/memories/many/m-1.md": "m\n",
			},
			wantAbsent: []string{".agent/knowledge/memories/many/m-2.md", graph},
			wantWarns:  []string{"more than 2 files"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outside := t.TempDir()
			writeCopyBackFile(t, outside, copyBackMemoryRel, secret, time.Now())
			runGit(t, outside, "init", "-q", "-b", "main")
			// The mock sudo runs git with the environment of the agent user:
			// it reads WS, ROOT and OUTSIDE from a file, written once the
			// fixture has named them.
			vars, extra := filepath.Join(t.TempDir(), "vars"), ""
			if tc.sudoExtra != "" {
				extra = ". '" + vars + "'\n" + tc.sudoExtra
			}
			f := newAgentFilesFixture(t, extra)
			writeTestFile(t, vars, fmt.Sprintf("WS='%s'\nROOT='%s'\nOUTSIDE='%s'\n", f.ws, f.root, outside))
			if tc.maxFiles > 0 {
				prev := navigatorCopyOutMaxFiles
				navigatorCopyOutMaxFiles = tc.maxFiles
				t.Cleanup(func() { navigatorCopyOutMaxFiles = prev })
			}
			clone, _ := newCopyBackRepos(t)
			runGit(t, f.ws, "init", "-q", "-b", "main")
			wsPath := tc.setup(t, f, outside)
			f.lock(t)

			logs := captureLog(t)
			runner := NewRunnerWithBackend(&ctxRespectingBackend{})
			runner.log = slog.Default()
			runner.copyNavigatorBack(taskID, wsPath, clone)

			for rel, want := range tc.wantCopied {
				if got := readCopyBackFile(t, clone, rel); got != want {
					t.Errorf("%s = %.40q, want %.40q", rel, got, want)
				}
			}
			for _, rel := range tc.wantAbsent {
				assertNoPath(t, filepath.Join(clone, filepath.FromSlash(rel)))
			}
			if tc.sudoExtra != "" {
				if _, err := os.Lstat(f.ws + "-moved"); err != nil {
					t.Fatalf("the mock sudo did not move the workspace: %v", err)
				}
			}
			warns := navigatorWarns(logs.String())
			if len(warns) != len(tc.wantWarns) {
				t.Fatalf("Warn lines %q, want %d", warns, len(tc.wantWarns))
			}
			for i, want := range tc.wantWarns {
				if !strings.Contains(warns[i], want) {
					t.Errorf("Warn line %q, want %q", warns[i], want)
				}
			}
			if calls := f.calls(t); tc.wantNoGit && len(calls) != 0 {
				t.Errorf("sudo ran %q, want no command for a refused workspace", calls)
			}
		})
	}
}

// TestNavigatorTarStream_NoFollow is item 2: a link and a FIFO in the
// daemon's .agent are not in the stream, each with one Warn line that names
// it, and the stream builder returns.
func TestNavigatorTarStream_NoFollow(t *testing.T) {
	tests := []struct {
		name string
		// setup adds to the source .agent; outside is outside the repository.
		setup func(t *testing.T, agent, outside string)
		// start is the directory the stream starts at ("" is agent).
		start      func(t *testing.T, agent, outside string) string
		wantAbsent []string
		wantWarn   string // the path in the one Warn line
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
			wantWarn:   filepath.Join("sops", "config.yaml"),
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
			wantWarn:   filepath.Join(".agent", "system"),
		},
		{
			name: "a FIFO",
			setup: func(t *testing.T, agent, _ string) {
				if err := syscall.Mkfifo(filepath.Join(agent, "sops", "pipe"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantAbsent: []string{".agent/sops/pipe"},
			wantWarn:   filepath.Join("sops", "pipe"),
		},
		{
			name: "a link at .agent",
			start: func(t *testing.T, agent, outside string) string {
				link := filepath.Join(outside, "agent-link")
				if err := os.Symlink(agent, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			wantAbsent: []string{".agent/", ".agent/sops/custom/sop.md"},
			wantWarn:   "agent-link",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			agent := filepath.Join(newNavigatorSource(t), ".agent")
			outside := t.TempDir()
			if tc.setup != nil {
				tc.setup(t, agent, outside)
			}
			start := agent
			if tc.start != nil {
				start = tc.start(t, agent, outside)
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
			if tc.start == nil && !seen[".agent/sops/custom/sop.md"] {
				t.Errorf("the regular file is not in the stream (%v)", seen)
			}
			warns := navigatorWarns(logs.String())
			if len(warns) != 1 || !strings.Contains(warns[0], tc.wantWarn) {
				t.Errorf("Warn lines %q, want one with the path %q", warns, tc.wantWarn)
			}
		})
	}
}

// TestNavigatorCopy_OptionEmpty_LinksAsToday: with the option empty the copy
// in still follows a link in the source .agent and the copy out still copies
// a hard link, and no sudo runs.
func TestNavigatorCopy_OptionEmpty_LinksAsToday(t *testing.T) {
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
