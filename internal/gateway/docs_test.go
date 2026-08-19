package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
