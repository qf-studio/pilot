package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
)

const (
	// maxLearningReasonBytes caps a reject reason (POST /api/v1/learnings and
	// the approval decision body), GH-6011.
	maxLearningReasonBytes = 2048
	// maxLearningBodyBytes bounds the learnings request body: a max-size
	// reason plus the other fields with generous headroom.
	maxLearningBodyBytes = 16 << 10
	// learningDedupWindow is how long an identical learning (same kind,
	// project, subject and reason) is treated as a repeat of the first.
	learningDedupWindow = 24 * time.Hour

	learningKindReviewReject = "review_reject"
	learningKindDraftReject  = "draft_reject"
)

// LearningStore is the write seam behind POST /api/v1/learnings and reject
// reasons on approval decisions (GH-6011) — satisfied by *memory.KnowledgeStore.
type LearningStore interface {
	AddMemoryOnce(m *memory.Memory, dedupKey string, window time.Duration) (created bool, err error)
}

// SetLearningStore wires the learning writer and the project registry used to
// validate (and key) learnings. Must be called at both gateway construction
// sites (polling mode in cmd/pilot/main.go, gateway mode in
// internal/pilot/pilot.go); until then the learnings route 503s and reject
// reasons on approval decisions are logged and dropped.
func (s *Server) SetLearningStore(store LearningStore, projects comms.ProjectSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.learningStore = store
	s.learningProjects = projects
}

// learningSubject identifies what a console reviewer rejected. All optional.
type learningSubject struct {
	CardID  string `json:"cardId"`
	DraftID string `json:"draftId"`
	TaskID  string `json:"taskId"`
	PRURL   string `json:"prUrl"`
}

// learningRequestBody is the POST /api/v1/learnings body.
type learningRequestBody struct {
	Kind    string          `json:"kind"`
	Reason  string          `json:"reason"`
	Project string          `json:"project"`
	By      string          `json:"by"`
	Subject learningSubject `json:"subject"`
}

// learningProject is a resolved project: Name is the registry name (tagged),
// ID is the key memories are scoped by. ID is filepath.Base(path) — the same
// projectID the executor derives from task.ProjectPath when it reads memories.
type learningProject struct {
	Name string
	ID   string
}

func (s *Server) learningProjectByName(projects comms.ProjectSource, name string) (learningProject, bool) {
	if projects == nil {
		return learningProject{}, false
	}
	info := projects.GetProjectByName(name)
	if info == nil {
		return learningProject{}, false
	}
	return projectFromInfo(info.Name, info.Path), true
}

func projectFromInfo(name, path string) learningProject {
	id := name
	if path != "" {
		id = filepath.Base(path)
	}
	return learningProject{Name: name, ID: id}
}

// handleLearnings serves POST /api/v1/learnings (GH-6011): a console reject
// with a reason becomes a learning injected into later prompts for the project.
func (s *Server) handleLearnings(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	store := s.learningStore
	projects := s.learningProjects
	s.mu.RUnlock()
	if store == nil {
		http.Error(w, "learning store not configured", http.StatusServiceUnavailable)
		return
	}

	var body learningRequestBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLearningBodyBytes)).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	body.Kind = strings.TrimSpace(body.Kind)
	body.Reason = strings.TrimSpace(body.Reason)
	body.Project = strings.TrimSpace(body.Project)

	switch {
	case body.Kind == "":
		http.Error(w, "missing kind", http.StatusBadRequest)
		return
	case body.Kind != learningKindReviewReject && body.Kind != learningKindDraftReject:
		http.Error(w, `kind must be "review_reject" or "draft_reject"`, http.StatusBadRequest)
		return
	case body.Reason == "":
		http.Error(w, "missing reason", http.StatusBadRequest)
		return
	case len(body.Reason) > maxLearningReasonBytes:
		http.Error(w, "reason exceeds 2048 bytes", http.StatusBadRequest)
		return
	case body.Project == "":
		http.Error(w, "missing project", http.StatusBadRequest)
		return
	}

	project, ok := s.learningProjectByName(projects, body.Project)
	if !ok {
		http.Error(w, "unknown project", http.StatusNotFound)
		return
	}

	id, _, err := writeConsoleLearning(store, body.Kind, body.Reason, strings.TrimSpace(body.By), project, body.Subject)
	if err != nil {
		logging.WithComponent("gateway").Warn("failed to write console learning",
			slog.String("project", project.Name), slog.Any("error", err))
		http.Error(w, "failed to write learning", http.StatusInternalServerError)
		return
	}
	writeJSON(w, struct {
		ID int64 `json:"id"`
	}{id})
}

// writeConsoleLearning stores one console-review learning and returns its ID
// plus whether a new row was created (false = an identical learning from the
// last 24 h already existed and its ID is returned instead).
func writeConsoleLearning(store LearningStore, kind, reason, by string, project learningProject, subject learningSubject) (int64, bool, error) {
	tags := []string{kind, project.Name}
	for _, v := range []string{subject.CardID, subject.DraftID, subject.TaskID, subject.PRURL} {
		if v = strings.TrimSpace(v); v != "" {
			tags = append(tags, v)
		}
	}

	context := "console review"
	if by != "" {
		context += " by " + by
	}
	m := &memory.Memory{
		Type:      memory.MemoryTypeLearning,
		Content:   learningSubjectHeader(kind, subject) + "\n" + reason,
		Context:   context,
		ProjectID: project.ID,
		Source:    memory.SourceConsoleReview,
		Tags:      tags,
	}
	created, err := store.AddMemoryOnce(m, learningDedupKey(kind, project.ID, subject, reason), learningDedupWindow)
	return m.ID, created, err
}

// learningSubjectHeader is the one-line header above the reason, e.g.
// "review_reject — task GH-1, pr https://…".
func learningSubjectHeader(kind string, subject learningSubject) string {
	var parts []string
	for _, p := range []struct{ label, v string }{
		{"card", subject.CardID}, {"draft", subject.DraftID}, {"task", subject.TaskID}, {"pr", subject.PRURL},
	} {
		if v := strings.TrimSpace(p.v); v != "" {
			parts = append(parts, p.label+" "+v)
		}
	}
	if len(parts) == 0 {
		return kind
	}
	return kind + " — " + strings.Join(parts, ", ")
}

// learningDedupKey hashes (kind, project, subject, reason): a repeated submit
// of the same reject within the window must not stack duplicate learnings.
func learningDedupKey(kind, projectID string, subject learningSubject, reason string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		kind, projectID, subject.CardID, subject.DraftID, subject.TaskID, subject.PRURL, reason,
	}, "\x00")))
	return "console-learning:" + hex.EncodeToString(h[:])
}

// recordRejectLearning writes the learning for a rejected approval decision.
// Best-effort: the decision is already recorded, so a failure is logged, not
// surfaced to the caller.
func (s *Server) recordRejectLearning(requestID string, pending *memory.PendingApproval, exec *memory.Execution, reason, by string) {
	s.mu.RLock()
	store := s.learningStore
	projects := s.learningProjects
	s.mu.RUnlock()

	log := logging.WithComponent("gateway")
	if store == nil {
		log.Warn("reject reason dropped: learning store not configured", slog.String("request_id", requestID))
		return
	}

	path := pending.Project
	if path == "" && exec != nil {
		path = exec.ProjectPath
	}
	if path == "" {
		log.Warn("reject reason dropped: approval has no project", slog.String("request_id", requestID))
		return
	}
	project := projectFromInfo(filepath.Base(path), path)
	if projects != nil {
		if info := projects.GetProjectByPath(path); info != nil {
			project = projectFromInfo(info.Name, info.Path)
		}
	}

	subject := learningSubject{CardID: requestID, TaskID: pending.TaskID}
	if exec != nil {
		subject.PRURL = exec.PRUrl
	}
	if _, _, err := writeConsoleLearning(store, learningKindReviewReject, reason, by, project, subject); err != nil {
		log.Warn("failed to write reject learning", slog.String("request_id", requestID), slog.Any("error", err))
	}
}
