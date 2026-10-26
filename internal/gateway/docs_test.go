package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- fixture helpers ---------------------------------------------------

func docsWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// buildDocsFixture lays out a representative .agent/ tree under a fresh
// t.TempDir(): one file per subtree, a non-.md file (must be excluded), a
// graph.json (must never be listed/served — it's an index, not a doc), one
// file per knowledge/memories kind, and one file directly under .agent/
// that is outside every allowlisted subtree (used by the outside-subtree
// traversal case). Returns the project root (the directory containing
// .agent/, not .agent/ itself).
func buildDocsFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	agent := filepath.Join(root, ".agent")

	docsWriteFile(t, filepath.Join(agent, "DEVELOPMENT-README.md"), "# readme")
	docsWriteFile(t, filepath.Join(agent, "system", "saas-roadmap.md"), "# roadmap")
	docsWriteFile(t, filepath.Join(agent, "system", "notes.txt"), "not markdown")
	docsWriteFile(t, filepath.Join(agent, "sops", "onboarding", "new-project.md"), "# sop")
	docsWriteFile(t, filepath.Join(agent, "tasks", "TASK-1-foo.md"), "# task")
	docsWriteFile(t, filepath.Join(agent, "knowledge", "graph.json"), `{"nodes":{}}`)
	docsWriteFile(t, filepath.Join(agent, "knowledge", "memories", "patterns", "p1.md"), "# pattern 1")
	docsWriteFile(t, filepath.Join(agent, "knowledge", "memories", "patterns", "p2.md"), "# pattern 2")
	docsWriteFile(t, filepath.Join(agent, "knowledge", "memories", "pitfalls", "bug1.md"), "# pitfall")
	docsWriteFile(t, filepath.Join(agent, "knowledge", "memories", "learnings", "l1.md"), "# learning")
	docsWriteFile(t, filepath.Join(agent, "knowledge", "memories", "decisions", "d1.md"), "# decision")
	docsWriteFile(t, filepath.Join(agent, "random.md"), "# outside every subtree")

	return root
}

func newTestServerWithDocsProject(projectPath string) *Server {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 9090})
	s.docsProjectPath = projectPath
	return s
}

// --- handleDocsTree ------------------------------------------------------

func TestHandleDocsTree_Success_ClassificationAndCounts(t *testing.T) {
	root := buildDocsFixture(t)
	s := newTestServerWithDocsProject(root)

	req := httpTestRequest(t, http.MethodGet, "/api/v1/docs/tree", nil)
	w := newTestResponseRecorder()
	s.handleDocsTree(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
	}

	var got docsTreeResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.body.String())
	}

	if got.Root != ".agent" {
		t.Errorf("Root = %q, want .agent", got.Root)
	}

	// 9 .md files total: README, system/1, sops/1, tasks/1, patterns/2,
	// pitfalls/1, learnings/1, decisions/1. notes.txt, graph.json, and
	// random.md (outside every subtree) must all be absent.
	if len(got.Entries) != 9 {
		t.Fatalf("len(Entries) = %d, want 9: %+v", len(got.Entries), got.Entries)
	}

	byPath := make(map[string]docsTreeEntry, len(got.Entries))
	for _, e := range got.Entries {
		byPath[e.Path] = e
	}

	for _, unwanted := range []string{"system/notes.txt", "knowledge/graph.json", "random.md"} {
		if _, ok := byPath[unwanted]; ok {
			t.Errorf("entry %q must not be listed", unwanted)
		}
	}

	cases := []struct {
		path     string
		wantType string
		wantKind string // "" means nil
	}{
		{"DEVELOPMENT-README.md", "readme", ""},
		{"system/saas-roadmap.md", "system", ""},
		{"sops/onboarding/new-project.md", "sop", ""},
		{"tasks/TASK-1-foo.md", "task", ""},
		{"knowledge/memories/patterns/p1.md", "knowledge", "pattern"},
		{"knowledge/memories/pitfalls/bug1.md", "knowledge", "pitfall"},
		{"knowledge/memories/learnings/l1.md", "knowledge", "learning"},
		{"knowledge/memories/decisions/d1.md", "knowledge", "decision"},
	}
	for _, c := range cases {
		entry, ok := byPath[c.path]
		if !ok {
			t.Errorf("missing entry %q", c.path)
			continue
		}
		if entry.Type != c.wantType {
			t.Errorf("entry %q Type = %q, want %q", c.path, entry.Type, c.wantType)
		}
		gotKind := ""
		if entry.Kind != nil {
			gotKind = *entry.Kind
		}
		if gotKind != c.wantKind {
			t.Errorf("entry %q Kind = %q, want %q", c.path, gotKind, c.wantKind)
		}
		if entry.SizeBytes <= 0 {
			t.Errorf("entry %q SizeBytes = %d, want > 0", c.path, entry.SizeBytes)
		}
	}

	if got.Counts != (docsCounts{Patterns: 2, Pitfalls: 1, Learnings: 1, Decisions: 1}) {
		t.Errorf("Counts = %+v, want {2 1 1 1}", got.Counts)
	}
}

func TestHandleDocsTree_Limit_TruncatesEntriesNotCounts(t *testing.T) {
	root := buildDocsFixture(t)
	s := newTestServerWithDocsProject(root)

	req := httpTestRequest(t, http.MethodGet, "/api/v1/docs/tree?limit=2", nil)
	w := newTestResponseRecorder()
	s.handleDocsTree(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
	}
	var got docsTreeResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Errorf("len(Entries) = %d, want 2", len(got.Entries))
	}
	if got.Counts != (docsCounts{Patterns: 2, Pitfalls: 1, Learnings: 1, Decisions: 1}) {
		t.Errorf("Counts = %+v, want full corpus counts regardless of limit", got.Counts)
	}
}

func TestHandleDocsTree_MalformedLimit_400(t *testing.T) {
	root := buildDocsFixture(t)
	s := newTestServerWithDocsProject(root)

	for _, limit := range []string{"abc", "-1", "1.5"} {
		req := httpTestRequest(t, http.MethodGet, "/api/v1/docs/tree?limit="+limit, nil)
		w := newTestResponseRecorder()
		s.handleDocsTree(w, req)
		if w.status != http.StatusBadRequest {
			t.Errorf("limit=%q: status = %d, want 400", limit, w.status)
		}
	}
}

func TestHandleDocsTree_ProjectScoping(t *testing.T) {
	root := buildDocsFixture(t)
	s := newTestServerWithDocsProject(root)

	t.Run("matching project query", func(t *testing.T) {
		req := httpTestRequest(t, http.MethodGet, "/api/v1/docs/tree?project="+root, nil)
		w := newTestResponseRecorder()
		s.handleDocsTree(w, req)
		if w.status != http.StatusOK {
			t.Errorf("status = %d, want 200 (body=%s)", w.status, w.body.String())
		}
	})

	t.Run("mismatched project query is unknown", func(t *testing.T) {
		req := httpTestRequest(t, http.MethodGet, "/api/v1/docs/tree?project=/nonexistent/other-project", nil)
		w := newTestResponseRecorder()
		s.handleDocsTree(w, req)
		if w.status != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.status)
		}
		var body docsErrorBody
		if err := json.Unmarshal(w.body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if body.Error != docsNotFoundMsg {
			t.Errorf("Error = %q, want %q", body.Error, docsNotFoundMsg)
		}
	})

	t.Run("unconfigured server has no known project", func(t *testing.T) {
		unconfigured := NewServer(&Config{Host: "127.0.0.1", Port: 9091})
		req := httpTestRequest(t, http.MethodGet, "/api/v1/docs/tree", nil)
		w := newTestResponseRecorder()
		unconfigured.handleDocsTree(w, req)
		if w.status != http.StatusNotFound {
			t.Errorf("status = %d, want 404", w.status)
		}
	})
}

func TestHandleDocsTree_MissingAgentDir_404DistinguishableBody(t *testing.T) {
	root := t.TempDir() // no .agent/ under it
	s := newTestServerWithDocsProject(root)

	req := httpTestRequest(t, http.MethodGet, "/api/v1/docs/tree", nil)
	w := newTestResponseRecorder()
	s.handleDocsTree(w, req)

	if w.status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body=%s)", w.status, w.body.String())
	}
	var body docsErrorBody
	if err := json.Unmarshal(w.body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Error != "no .agent directory" {
		t.Errorf("Error = %q, want %q", body.Error, "no .agent directory")
	}
}

func TestHandleDocsTree_MethodNotAllowed(t *testing.T) {
	root := buildDocsFixture(t)
	s := newTestServerWithDocsProject(root)

	req := httpTestRequest(t, http.MethodPost, "/api/v1/docs/tree", nil)
	w := newTestResponseRecorder()
	s.handleDocsTree(w, req)

	if w.status != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.status)
	}
}

// --- handleDocsFile --------------------------------------------------------

func TestHandleDocsFile_ValidationLadder(t *testing.T) {
	root := buildDocsFixture(t)
	agent := filepath.Join(root, ".agent")

	// Symlink-escape fixture: a secret file outside .agent/, linked from
	// inside an allowlisted subtree.
	secretDir := t.TempDir()
	secretFile := filepath.Join(secretDir, "secret.md")
	docsWriteFile(t, secretFile, "# secret")
	escapeLink := filepath.Join(agent, "system", "escape.md")
	if err := os.Symlink(secretFile, escapeLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Oversized fixture: > 512KB under an allowlisted subtree.
	big := strings.Repeat("a", docsMaxFileBytes+1)
	docsWriteFile(t, filepath.Join(agent, "system", "big.md"), big)

	s := newTestServerWithDocsProject(root)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{"missing path", http.MethodGet, "", http.StatusBadRequest},
		{"non-md extension", http.MethodGet, "system/notes.txt", http.StatusBadRequest},
		{"graph.json rejected by extension check", http.MethodGet, "knowledge/graph.json", http.StatusBadRequest},
		{"dotdot traversal", http.MethodGet, "../../../etc/passwd.md", http.StatusNotFound},
		{"absolute path", http.MethodGet, "/etc/passwd.md", http.StatusNotFound},
		{"symlink escape", http.MethodGet, "system/escape.md", http.StatusNotFound},
		{"outside every subtree", http.MethodGet, "random.md", http.StatusNotFound},
		{"nonexistent file inside allowlisted subtree", http.MethodGet, "system/does-not-exist.md", http.StatusNotFound},
		{"oversized file", http.MethodGet, "system/big.md", http.StatusRequestEntityTooLarge},
		{"happy path subtree file", http.MethodGet, "system/saas-roadmap.md", http.StatusOK},
		{"happy path readme", http.MethodGet, "DEVELOPMENT-README.md", http.StatusOK},
		{"method not allowed", http.MethodPost, "system/saas-roadmap.md", http.StatusMethodNotAllowed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := "/api/v1/docs/file"
			if tc.path != "" {
				target += "?path=" + tc.path
			}
			req := httpTestRequest(t, tc.method, target, nil)
			w := newTestResponseRecorder()
			s.handleDocsFile(w, req)
			if w.status != tc.wantStatus {
				t.Errorf("status = %d, want %d (body=%s)", w.status, tc.wantStatus, w.body.String())
			}
		})
	}
}

func TestHandleDocsFile_HappyPath_ContentAndMetadata(t *testing.T) {
	root := buildDocsFixture(t)
	s := newTestServerWithDocsProject(root)

	req := httpTestRequest(t, http.MethodGet, "/api/v1/docs/file?path=system/saas-roadmap.md", nil)
	w := newTestResponseRecorder()
	s.handleDocsFile(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
	}
	var got docsFileResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Path != "system/saas-roadmap.md" {
		t.Errorf("Path = %q, want system/saas-roadmap.md", got.Path)
	}
	if got.Content != "# roadmap" {
		t.Errorf("Content = %q, want %q", got.Content, "# roadmap")
	}
	if got.SizeBytes != int64(len("# roadmap")) {
		t.Errorf("SizeBytes = %d, want %d", got.SizeBytes, len("# roadmap"))
	}
	if got.ModifiedAt.IsZero() {
		t.Error("ModifiedAt is zero, want a real mtime")
	}
}

func TestHandleDocsFile_TraversalAndOutsideAllowlist_SameBodyAsNotFound(t *testing.T) {
	root := buildDocsFixture(t)
	s := newTestServerWithDocsProject(root)

	reqTraversal := httpTestRequest(t, http.MethodGet, "/api/v1/docs/file?path=../../../etc/passwd.md", nil)
	wTraversal := newTestResponseRecorder()
	s.handleDocsFile(wTraversal, reqTraversal)

	reqMissing := httpTestRequest(t, http.MethodGet, "/api/v1/docs/file?path=system/does-not-exist.md", nil)
	wMissing := newTestResponseRecorder()
	s.handleDocsFile(wMissing, reqMissing)

	var bodyTraversal, bodyMissing docsErrorBody
	if err := json.Unmarshal(wTraversal.body.Bytes(), &bodyTraversal); err != nil {
		t.Fatalf("unmarshal traversal body: %v", err)
	}
	if err := json.Unmarshal(wMissing.body.Bytes(), &bodyMissing); err != nil {
		t.Fatalf("unmarshal missing body: %v", err)
	}
	if bodyTraversal.Error != bodyMissing.Error {
		t.Errorf("traversal body %q != file-not-found body %q, want identical (no existence leak)", bodyTraversal.Error, bodyMissing.Error)
	}
}

func TestHandleDocsFile_MissingAgentDir_404DistinguishableBody(t *testing.T) {
	root := t.TempDir()
	s := newTestServerWithDocsProject(root)

	req := httpTestRequest(t, http.MethodGet, "/api/v1/docs/file?path=system/saas-roadmap.md", nil)
	w := newTestResponseRecorder()
	s.handleDocsFile(w, req)

	if w.status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.status)
	}
	var body docsErrorBody
	if err := json.Unmarshal(w.body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Error != "no .agent directory" {
		t.Errorf("Error = %q, want %q", body.Error, "no .agent directory")
	}
}

// --- handleDocsSummary ---------------------------------------------------

// docsGit runs git in dir with the given commit date for both author and
// committer, failing the test on error.
func docsGit(t *testing.T, dir string, when time.Time, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	stamp := when.UTC().Format(time.RFC3339)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func getDocsSummary(t *testing.T, s *Server) (docsSummaryResponse, string) {
	t.Helper()
	req := httpTestRequest(t, http.MethodGet, "/api/v1/docs/summary", nil)
	w := newTestResponseRecorder()
	s.handleDocsSummary(w, req)
	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
	}
	var got docsSummaryResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.body.String())
	}
	return got, w.body.String()
}

func TestHandleDocsSummary_GitDatesNotMtime(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	agent := filepath.Join(root, ".agent")
	now := time.Now()

	docsWriteFile(t, filepath.Join(agent, "system", "old.md"), "# old")
	docsWriteFile(t, filepath.Join(agent, "system", "edited.md"), "# v1")
	docsWriteFile(t, filepath.Join(agent, "knowledge", "memories", "patterns", "p-old.md"), "# p1")
	docsWriteFile(t, filepath.Join(agent, "knowledge", "memories", "patterns", "p-edited.md"), "# v1")
	docsGit(t, root, now, "init", "-q")
	docsGit(t, root, now.Add(-10*24*time.Hour), "add", "-A")
	docsGit(t, root, now.Add(-10*24*time.Hour), "commit", "-q", "-m", "add")

	// Added yesterday, brand new this week.
	docsWriteFile(t, filepath.Join(agent, "tasks", "TASK-9-new.md"), "# new")
	docsWriteFile(t, filepath.Join(agent, "knowledge", "memories", "learnings", "l-new.md"), "# l")
	docsGit(t, root, now.Add(-24*time.Hour), "add", "-A")
	docsGit(t, root, now.Add(-24*time.Hour), "commit", "-q", "-m", "add new")

	// Added 10 days ago, edited yesterday: updated, not created.
	docsWriteFile(t, filepath.Join(agent, "system", "edited.md"), "# v2")
	docsWriteFile(t, filepath.Join(agent, "knowledge", "memories", "patterns", "p-edited.md"), "# v2")
	docsGit(t, root, now.Add(-24*time.Hour), "commit", "-q", "-am", "edit")

	// Untracked file: no git date, must not count even though its mtime is now.
	docsWriteFile(t, filepath.Join(agent, "system", "untracked.md"), "# u")

	s := newTestServerWithDocsProject(root)
	got, _ := getDocsSummary(t, s)

	if got.Docs.CreatedThisWeek == nil || *got.Docs.CreatedThisWeek != 1 {
		t.Errorf("docs.createdThisWeek = %v, want 1 (only TASK-9-new.md)", got.Docs.CreatedThisWeek)
	}
	if got.Docs.UpdatedThisWeek == nil || *got.Docs.UpdatedThisWeek != 2 {
		t.Errorf("docs.updatedThisWeek = %v, want 2 (edited.md + TASK-9-new.md)", got.Docs.UpdatedThisWeek)
	}
	if got.Memory.Total != 3 {
		t.Errorf("memory.total = %d, want 3", got.Memory.Total)
	}
	if got.Memory.CreatedThisWeek == nil || *got.Memory.CreatedThisWeek != 1 {
		t.Errorf("memory.createdThisWeek = %v, want 1 (l-new.md)", got.Memory.CreatedThisWeek)
	}
	if got.Memory.UpdatedThisWeek == nil || *got.Memory.UpdatedThisWeek != 2 {
		t.Errorf("memory.updatedThisWeek = %v, want 2 (p-edited.md + l-new.md)", got.Memory.UpdatedThisWeek)
	}
}

func TestHandleDocsSummary_FreshCloneNotAllUpdated(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	now := time.Now()
	docsWriteFile(t, filepath.Join(root, ".agent", "system", "a.md"), "# a")
	docsGit(t, root, now, "init", "-q")
	docsGit(t, root, now.Add(-30*24*time.Hour), "add", "-A")
	docsGit(t, root, now.Add(-30*24*time.Hour), "commit", "-q", "-m", "old")
	// mtime of the file is "now" (as after a fresh clone); git says 30 days ago.

	got, _ := getDocsSummary(t, newTestServerWithDocsProject(root))
	if got.Docs.UpdatedThisWeek == nil || *got.Docs.UpdatedThisWeek != 0 {
		t.Errorf("docs.updatedThisWeek = %v, want 0", got.Docs.UpdatedThisWeek)
	}
}

func TestHandleDocsSummary_GraphPresentAndAbsent(t *testing.T) {
	requireGit(t)

	t.Run("with graph.json", func(t *testing.T) {
		root := buildDocsFixture(t)
		docsWriteFile(t, filepath.Join(root, ".agent", "knowledge", "graph.json"), `{
			"updated": "2026-10-15T10:28:29.793377+00:00",
			"last_updated": "2026-10-22T14:45:33.958570+00:00",
			"nodes": {"concepts": {"a": {}, "b": {}}, "tasks": {"TASK-1": {}}, "memories": {"m1": {}}},
			"edges": [{"from":"a","to":"b"}, {"from":"b","to":"a"}],
			"stats": {"total_nodes": 999, "total_edges": 999}
		}`)
		got, _ := getDocsSummary(t, newTestServerWithDocsProject(root))
		if got.Graph == nil {
			t.Fatal("graph = null, want object")
		}
		if got.Graph.Nodes != 4 || got.Graph.Edges != 2 {
			t.Errorf("graph nodes/edges = %d/%d, want 4/2", got.Graph.Nodes, got.Graph.Edges)
		}
		want := time.Date(2026, 10, 22, 14, 45, 33, 958570000, time.UTC)
		if got.Graph.UpdatedAt == nil || !got.Graph.UpdatedAt.Equal(want) {
			t.Errorf("graph.updatedAt = %v, want %v", got.Graph.UpdatedAt, want)
		}
	})

	t.Run("without graph.json", func(t *testing.T) {
		root := buildDocsFixture(t)
		if err := os.Remove(filepath.Join(root, ".agent", "knowledge", "graph.json")); err != nil {
			t.Fatal(err)
		}
		got, body := getDocsSummary(t, newTestServerWithDocsProject(root))
		if got.Graph != nil {
			t.Errorf("graph = %+v, want null", got.Graph)
		}
		if !strings.Contains(body, `"graph":null`) {
			t.Errorf("body must serialize graph as null: %s", body)
		}
	})
}

func TestHandleDocsSummary_ReadCounterIsNull(t *testing.T) {
	requireGit(t)
	root := buildDocsFixture(t)
	got, body := getDocsSummary(t, newTestServerWithDocsProject(root))
	if got.Docs.ReadByPilotThisWeek != nil || got.Memory.ReadByPilotThisWeek != nil {
		t.Errorf("readByPilotThisWeek must be nil: %+v %+v", got.Docs, got.Memory)
	}
	if n := strings.Count(body, `"readByPilotThisWeek":null`); n != 2 {
		t.Errorf("want 2 null readByPilotThisWeek fields, got %d: %s", n, body)
	}
}

func TestHandleDocsSummary_NotGitRepoYieldsNullCounts(t *testing.T) {
	requireGit(t)
	root := buildDocsFixture(t) // t.TempDir(), not a git repo
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	got, body := getDocsSummary(t, newTestServerWithDocsProject(root))
	if got.Docs.CreatedThisWeek != nil || got.Docs.UpdatedThisWeek != nil {
		t.Errorf("docs counts must be null without git: %s", body)
	}
	if got.Memory.Total != 5 {
		t.Errorf("memory.total = %d, want 5", got.Memory.Total)
	}
}

func TestHandleDocsSummary_ErrorLadder(t *testing.T) {
	root := buildDocsFixture(t)
	s := newTestServerWithDocsProject(root)

	w := newTestResponseRecorder()
	s.handleDocsSummary(w, httpTestRequest(t, http.MethodPost, "/api/v1/docs/summary", nil))
	if w.status != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", w.status)
	}

	w = newTestResponseRecorder()
	s.handleDocsSummary(w, httpTestRequest(t, http.MethodGet, "/api/v1/docs/summary?project=/somewhere/else", nil))
	if w.status != http.StatusNotFound {
		t.Errorf("unknown project status = %d, want 404", w.status)
	}
}

func TestParseDocsGitLog(t *testing.T) {
	out := "\x011000\n\nM\tsystem/a.md\n\x01500\n\nA\tsystem/a.md\nA\tsystem/b.md\n"
	h := parseDocsGitLog(out)
	a := h["system/a.md"]
	if a.updated.Unix() != 1000 || a.created.Unix() != 500 {
		t.Errorf("a = %+v, want updated=1000 created=500", a)
	}
	b := h["system/b.md"]
	if b.updated.Unix() != 500 || b.created.Unix() != 500 {
		t.Errorf("b = %+v", b)
	}
}
