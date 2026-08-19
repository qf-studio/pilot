package gateway

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// docsMaxFileBytes is the hard size cap for GET /api/v1/docs/file (founder
// decision 2026-08-19: hard-reject with 413, no partial-content semantics).
const docsMaxFileBytes = 512 * 1024

// docsReadmeName is the one top-level file served alongside the four walked
// subtrees (GH-5003 contract).
const docsReadmeName = "DEVELOPMENT-README.md"

// docsNotFoundMsg is the single body shared by "unknown project", "file not
// found", and "path outside allowlist" (GH-5003 error ladder: same body —
// no existence leak, the orgOwnershipCheck philosophy).
const docsNotFoundMsg = "not found"

// docsSubtree describes one of the .agent subdirectories handleDocsTree
// walks, mapping its directory name (relative to .agent/) to the "type"
// value in the tree response.
type docsSubtree struct {
	dir      string
	docsType string
}

// docsSubtrees are the four subtrees the GH-5003 contract walks recursively.
// knowledge/memories (not all of knowledge/) is deliberate: it naturally
// excludes .agent/knowledge/graph.json, which the contract says must never
// be listed or served in v1 (it's an index, not a doc — founder call).
var docsSubtrees = []docsSubtree{
	{"system", "system"},
	{"sops", "sop"},
	{"tasks", "task"},
	{filepath.Join("knowledge", "memories"), "knowledge"},
}

// docsMemoryKinds maps the immediate child directory under
// knowledge/memories to the "kind" value on knowledge entries, mirroring
// memory_writer.py's {type}s/{slug}.md convention (nav-graph skill).
var docsMemoryKinds = map[string]string{
	"patterns":  "pattern",
	"pitfalls":  "pitfall",
	"learnings": "learning",
	"decisions": "decision",
}

// docsErrorBody is the JSON shape for every docs-route error response,
// including the distinguishable "no .agent directory" body the contract
// requires so the console can render "not a Navigator-managed repo".
type docsErrorBody struct {
	Error string `json:"error"`
}

func docsError(w http.ResponseWriter, status int, msg string) {
	writeJSONStatus(w, status, docsErrorBody{Error: msg})
}

// docsTreeEntry is one file in the GET /api/v1/docs/tree response.
type docsTreeEntry struct {
	Path       string    `json:"path"`
	Type       string    `json:"type"`
	Kind       *string   `json:"kind"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

// docsCounts is the knowledge/memories/{patterns,pitfalls,learnings,decisions}
// .md file counts — computed by counting files, never by parsing graph.json
// (founder call, same as the listing rule above).
type docsCounts struct {
	Patterns  int `json:"patterns"`
	Pitfalls  int `json:"pitfalls"`
	Learnings int `json:"learnings"`
	Decisions int `json:"decisions"`
}

type docsTreeResponse struct {
	Root    string          `json:"root"`
	Entries []docsTreeEntry `json:"entries"`
	Counts  docsCounts      `json:"counts"`
}

type docsFileResponse struct {
	Path       string    `json:"path"`
	Content    string    `json:"content"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

// resolveDocsAgentRoot resolves the request's ?project= against the
// daemon's configured docsProjectPath and returns the .agent/ directory to
// serve from. Mirrors the dashboard/events.go convention (dashboard.go:
// SetDashboardProjectPath, events.go's handleTaskEvents doc comment): a
// deployment is scoped to a single project directory, so a caller may omit
// ?project= entirely; if it names one, it must canonicalize (the #4297
// lesson: compare canonicalized forms, not raw strings) to the configured
// path or the request is treated as an unknown project.
//
// Returns ok=false with the status/body already decided by the caller's
// contract: unknown project and "no .agent directory" are distinguished
// (the former shares docsNotFoundMsg with file-not-found; the latter has
// its own body so the console can render "not a Navigator-managed repo").
func (s *Server) resolveDocsAgentRoot(r *http.Request) (agentRoot string, status int, msg string, ok bool) {
	s.mu.RLock()
	configured := s.docsProjectPath
	s.mu.RUnlock()

	projectPath := configured
	if reqProject := r.URL.Query().Get("project"); reqProject != "" {
		if configured == "" || memory.CanonicalizeProjectPath(reqProject) != memory.CanonicalizeProjectPath(configured) {
			return "", http.StatusNotFound, docsNotFoundMsg, false
		}
		projectPath = configured
	}
	if projectPath == "" {
		return "", http.StatusNotFound, docsNotFoundMsg, false
	}

	agentRoot = filepath.Join(projectPath, ".agent")
	info, err := os.Stat(agentRoot)
	if err != nil || !info.IsDir() {
		return "", http.StatusNotFound, "no .agent directory", false
	}

	return agentRoot, 0, "", true
}

// handleDocsTree serves GET /api/v1/docs/tree?project=<path>&limit=<n>
// (GH-5003 / TASK-466 read leg). Walks the four Navigator subtrees plus the
// top-level README and returns a flat, sorted entry list with per-kind
// knowledge counts.
func (s *Server) handleDocsTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		docsError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	agentRoot, status, msg, ok := s.resolveDocsAgentRoot(r)
	if !ok {
		docsError(w, status, msg)
		return
	}

	limit := -1
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			docsError(w, http.StatusBadRequest, "malformed limit")
			return
		}
		limit = n
	}

	entries, counts, err := walkDocsTree(agentRoot)
	if err != nil {
		docsError(w, http.StatusInternalServerError, "failed to walk docs tree")
		return
	}

	if limit >= 0 && limit < len(entries) {
		entries = entries[:limit]
	}

	writeJSON(w, docsTreeResponse{
		Root:    ".agent",
		Entries: entries,
		Counts:  counts,
	})
}

// handleDocsFile serves GET /api/v1/docs/file?project=<path>&path=<relative>
// (GH-5003 / TASK-466 read leg). path is relative to .agent/ and must
// resolve inside one of docsSubtrees or be the top-level README; validation
// is allowlist-shaped, never an arbitrary filesystem read.
func (s *Server) handleDocsFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		docsError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	agentRoot, status, msg, ok := s.resolveDocsAgentRoot(r)
	if !ok {
		docsError(w, status, msg)
		return
	}

	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		docsError(w, http.StatusBadRequest, "missing path")
		return
	}
	if !strings.HasSuffix(relPath, ".md") {
		docsError(w, http.StatusBadRequest, "path must reference a .md file")
		return
	}

	resolved, ok := resolveDocsFilePath(agentRoot, relPath)
	if !ok {
		docsError(w, http.StatusNotFound, docsNotFoundMsg)
		return
	}

	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		docsError(w, http.StatusNotFound, docsNotFoundMsg)
		return
	}

	if info.Size() > docsMaxFileBytes {
		docsError(w, http.StatusRequestEntityTooLarge, "file exceeds 512KB limit")
		return
	}

	content, err := os.ReadFile(resolved)
	if err != nil {
		docsError(w, http.StatusNotFound, docsNotFoundMsg)
		return
	}

	writeJSON(w, docsFileResponse{
		Path:       filepath.ToSlash(filepath.Clean(relPath)),
		Content:    string(content),
		SizeBytes:  info.Size(),
		ModifiedAt: info.ModTime().UTC(),
	})
}

// resolveDocsFilePath validates relPath (already known to end in ".md")
// against the allowlist and returns the real, symlink-resolved absolute
// path to read. Rejects "..", absolute paths, and symlink escapes by
// resolving both agentRoot and the candidate through EvalSymlinks and
// checking the result is still inside agentRoot and inside one of
// docsSubtrees (or is exactly the top-level README) — never an arbitrary
// filesystem read. A candidate that doesn't exist also fails here
// (EvalSymlinks errors on a missing path), which is intentional: "file not
// found" and "path outside allowlist" share the same 404 body upstream.
func resolveDocsFilePath(agentRoot, relPath string) (string, bool) {
	if filepath.IsAbs(relPath) {
		return "", false
	}
	cleaned := filepath.Clean(relPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}

	resolvedRoot, err := filepath.EvalSymlinks(agentRoot)
	if err != nil {
		return "", false
	}

	candidate := filepath.Join(agentRoot, cleaned)
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false
	}

	rel, err := filepath.Rel(resolvedRoot, resolvedCandidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	rel = filepath.ToSlash(rel)

	if rel == docsReadmeName {
		return resolvedCandidate, true
	}
	for _, sub := range docsSubtrees {
		subSlash := filepath.ToSlash(sub.dir)
		if rel == subSlash || strings.HasPrefix(rel, subSlash+"/") {
			return resolvedCandidate, true
		}
	}
	return "", false
}

// walkDocsTree walks the four Navigator subtrees plus the top-level README
// under agentRoot, returning a path-sorted entry list and the
// knowledge/memories per-kind counts. A missing subtree (e.g. a project
// with no .agent/sops yet) is skipped, not an error.
func walkDocsTree(agentRoot string) ([]docsTreeEntry, docsCounts, error) {
	entries := make([]docsTreeEntry, 0, 64)
	var counts docsCounts

	for _, sub := range docsSubtrees {
		subRoot := filepath.Join(agentRoot, sub.dir)
		walkErr := filepath.WalkDir(subRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".md") {
				return nil
			}

			relPath, relErr := filepath.Rel(agentRoot, path)
			if relErr != nil {
				return relErr
			}
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}

			var kind *string
			if sub.docsType == "knowledge" {
				relToMemories, memErr := filepath.Rel(subRoot, path)
				if memErr == nil {
					parts := strings.Split(filepath.ToSlash(relToMemories), "/")
					if len(parts) > 0 {
						if k, known := docsMemoryKinds[parts[0]]; known {
							kv := k
							kind = &kv
							switch parts[0] {
							case "patterns":
								counts.Patterns++
							case "pitfalls":
								counts.Pitfalls++
							case "learnings":
								counts.Learnings++
							case "decisions":
								counts.Decisions++
							}
						}
					}
				}
			}

			entries = append(entries, docsTreeEntry{
				Path:       filepath.ToSlash(relPath),
				Type:       sub.docsType,
				Kind:       kind,
				SizeBytes:  info.Size(),
				ModifiedAt: info.ModTime().UTC(),
			})
			return nil
		})
		if walkErr != nil {
			return nil, docsCounts{}, walkErr
		}
	}

	readmePath := filepath.Join(agentRoot, docsReadmeName)
	if info, err := os.Stat(readmePath); err == nil && !info.IsDir() {
		entries = append(entries, docsTreeEntry{
			Path:       docsReadmeName,
			Type:       "readme",
			Kind:       nil,
			SizeBytes:  info.Size(),
			ModifiedAt: info.ModTime().UTC(),
		})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	return entries, counts, nil
}
