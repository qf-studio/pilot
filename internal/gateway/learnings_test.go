package gateway

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/memory"

	_ "modernc.org/sqlite"
)

// fakeProjects is a comms.ProjectSource over a fixed project list.
type fakeProjects struct{ list []*comms.ProjectInfo }

func (f fakeProjects) GetProjectByName(name string) *comms.ProjectInfo {
	for _, p := range f.list {
		if p.Name == name {
			return p
		}
	}
	return nil
}

func (f fakeProjects) GetProjectByPath(path string) *comms.ProjectInfo {
	for _, p := range f.list {
		if p.Path == path {
			return p
		}
	}
	return nil
}

func (f fakeProjects) GetDefaultProject() *comms.ProjectInfo {
	if len(f.list) == 0 {
		return nil
	}
	return f.list[0]
}

func (f fakeProjects) ListProjects() []*comms.ProjectInfo { return f.list }

func newLearningsTestServer(t *testing.T) (*Server, *memory.KnowledgeStore) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ks := memory.NewKnowledgeStore(db)
	if err := ks.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	s := NewServer(&Config{Host: "127.0.0.1", Port: 9090})
	s.SetLearningStore(ks, fakeProjects{list: []*comms.ProjectInfo{{Name: "alpha", Path: "/work/alpha-repo"}}})
	return s, ks
}

func postLearning(t *testing.T, s *Server, body string) *testResponseRecorder {
	t.Helper()
	req := httpTestRequest(t, http.MethodPost, "/api/v1/learnings", bytes.NewBufferString(body))
	w := newTestResponseRecorder()
	s.handleLearnings(w, req)
	return w
}

func TestLearnings_WritesConsoleReviewLearning(t *testing.T) {
	s, ks := newLearningsTestServer(t)

	w := postLearning(t, s, `{"kind":"review_reject","reason":"use the existing helper","project":"alpha","by":"alice",
		"subject":{"cardId":"card-1","taskId":"GH-7","prUrl":"https://github.com/o/r/pull/9"}}`)
	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
	}
	var resp struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.body.Bytes(), &resp); err != nil || resp.ID == 0 {
		t.Fatalf("response = %s (err %v), want {id>0}", w.body.String(), err)
	}

	m, err := ks.GetMemory(resp.ID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if m.Type != memory.MemoryTypeLearning || m.Source != "console-review" {
		t.Errorf("type=%q source=%q, want learning/console-review", m.Type, m.Source)
	}
	if m.ProjectID != "alpha-repo" {
		t.Errorf("ProjectID = %q, want alpha-repo (base of the project path)", m.ProjectID)
	}
	for _, want := range []string{"review_reject", "alpha", "card-1", "GH-7", "https://github.com/o/r/pull/9"} {
		if !containsTag(m.Tags, want) {
			t.Errorf("tags %v missing %q", m.Tags, want)
		}
	}
	header, reason, _ := strings.Cut(m.Content, "\n")
	if reason != "use the existing helper" || !strings.Contains(header, "GH-7") {
		t.Errorf("content = %q, want subject header line then the reason", m.Content)
	}
}

func TestLearnings_Validation(t *testing.T) {
	s, _ := newLearningsTestServer(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{"missing kind", `{"reason":"r","project":"alpha"}`, http.StatusBadRequest},
		{"bad kind", `{"kind":"other","reason":"r","project":"alpha"}`, http.StatusBadRequest},
		{"missing reason", `{"kind":"draft_reject","project":"alpha"}`, http.StatusBadRequest},
		{"blank reason", `{"kind":"draft_reject","reason":"  ","project":"alpha"}`, http.StatusBadRequest},
		{"missing project", `{"kind":"draft_reject","reason":"r"}`, http.StatusBadRequest},
		{"reason too long", `{"kind":"draft_reject","reason":"` + strings.Repeat("x", 2049) + `","project":"alpha"}`, http.StatusBadRequest},
		{"unknown project", `{"kind":"draft_reject","reason":"r","project":"nope"}`, http.StatusNotFound},
		{"invalid json", `{`, http.StatusBadRequest},
		{"max size reason ok", `{"kind":"draft_reject","reason":"` + strings.Repeat("x", 2048) + `","project":"alpha"}`, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := postLearning(t, s, tt.body); w.status != tt.want {
				t.Errorf("status = %d, want %d (body=%s)", w.status, tt.want, w.body.String())
			}
		})
	}
}

func TestLearnings_NotConfigured(t *testing.T) {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 9090})
	if w := postLearning(t, s, `{"kind":"review_reject","reason":"r","project":"alpha"}`); w.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.status)
	}
}

func TestLearnings_IdempotentWithin24h(t *testing.T) {
	s, ks := newLearningsTestServer(t)
	const body = `{"kind":"draft_reject","reason":"too vague","project":"alpha","subject":{"draftId":"d1"}}`

	ids := make([]int64, 2)
	for i := range ids {
		w := postLearning(t, s, body)
		if w.status != http.StatusOK {
			t.Fatalf("post #%d status = %d", i+1, w.status)
		}
		var resp struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(w.body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		ids[i] = resp.ID
	}
	if ids[0] != ids[1] {
		t.Errorf("repeat returned id %d, want the first id %d", ids[1], ids[0])
	}
	if n, _ := ks.CountMemories(); n != 1 {
		t.Errorf("memories = %d, want 1", n)
	}

	// A different reason for the same subject is a new learning.
	postLearning(t, s, `{"kind":"draft_reject","reason":"wrong labels","project":"alpha","subject":{"draftId":"d1"}}`)
	if n, _ := ks.CountMemories(); n != 2 {
		t.Errorf("memories = %d, want 2 after a different reason", n)
	}
}

func containsTag(tags []string, want string) bool {
	for _, tg := range tags {
		if tg == want {
			return true
		}
	}
	return false
}

// --- reason on the approval decision ----------------------------------------

func rejectServer(t *testing.T, pending *memory.PendingApproval) (*Server, *memory.KnowledgeStore, *fakeDecisionRecorder) {
	t.Helper()
	s, ks := newLearningsTestServer(t)
	store := &mockDashboardStore{
		pendingApprovals: []*memory.PendingApproval{pending},
		execByApprovalReqID: map[string]*memory.Execution{
			pending.ID: {ID: "exec-1", ProjectPath: pending.Project, PRUrl: "https://github.com/o/r/pull/42"},
		},
	}
	s.dashboardStore = store
	rec := &fakeDecisionRecorder{}
	s.decisionRecorder = rec
	return s, ks, rec
}

func decide(t *testing.T, s *Server, requestID, body string) *testResponseRecorder {
	t.Helper()
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/approvals/"+requestID+"/decision",
		bytes.NewBufferString(body), "requestId", requestID)
	w := newTestResponseRecorder()
	s.handleApprovalDecision(w, req)
	return w
}

func TestApprovalDecisionReason_RejectWritesLearning(t *testing.T) {
	now := time.Now()
	s, ks, rec := rejectServer(t, &memory.PendingApproval{
		ID: "req-1", TaskID: "GH-7", Project: "/work/alpha-repo", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})

	w := decide(t, s, "req-1", `{"decision":"reject","by":"alice","reason":"missing tests"}`)
	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
	}
	if len(rec.calls) != 1 || rec.calls[0].decision != approval.DecisionRejected {
		t.Fatalf("recorder calls = %+v, want one reject", rec.calls)
	}

	got, err := ks.QueryBySource(memory.MemoryTypeLearning, "console-review", "alpha-repo", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("learnings = %v (err %v), want exactly one", got, err)
	}
	m := got[0]
	if m.Source != "console-review" || !containsTag(m.Tags, "alpha") {
		t.Errorf("source=%q tags=%v, want console-review with project tag alpha", m.Source, m.Tags)
	}
	if !strings.Contains(m.Content, "missing tests") || !strings.Contains(m.Content, "https://github.com/o/r/pull/42") {
		t.Errorf("content = %q, want reason and PR url", m.Content)
	}
}

func TestApprovalDecisionReason_NoLearningUnlessRejectWithReason(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"approve with reason", `{"decision":"approve","by":"alice","reason":"looks fine"}`},
		{"reject without reason", `{"decision":"reject","by":"alice"}`},
		{"reject with blank reason", `{"decision":"reject","by":"alice","reason":"   "}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			s, ks, _ := rejectServer(t, &memory.PendingApproval{
				ID: "req-1", Project: "/work/alpha-repo", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
			})
			if w := decide(t, s, "req-1", tt.body); w.status != http.StatusOK {
				t.Fatalf("status = %d (body=%s)", w.status, w.body.String())
			}
			if n, _ := ks.CountMemories(); n != 0 {
				t.Errorf("memories = %d, want 0", n)
			}
		})
	}
}

func TestApprovalDecisionReason_TooLongRejected(t *testing.T) {
	now := time.Now()
	s, ks, rec := rejectServer(t, &memory.PendingApproval{
		ID: "req-1", Project: "/work/alpha-repo", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	w := decide(t, s, "req-1", `{"decision":"reject","by":"alice","reason":"`+strings.Repeat("x", 2049)+`"}`)
	if w.status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.status)
	}
	if len(rec.calls) != 0 {
		t.Errorf("decision recorded despite 400: %+v", rec.calls)
	}
	if n, _ := ks.CountMemories(); n != 0 {
		t.Errorf("memories = %d, want 0", n)
	}
}

func TestApprovalDecisionReason_LostRaceWritesNothing(t *testing.T) {
	now := time.Now()
	s, ks, rec := rejectServer(t, &memory.PendingApproval{
		ID: "req-1", Project: "/work/alpha-repo", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	rec.err = memory.ErrApprovalAlreadyDecided
	if w := decide(t, s, "req-1", `{"decision":"reject","by":"alice","reason":"nope"}`); w.status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.status)
	}
	if n, _ := ks.CountMemories(); n != 0 {
		t.Errorf("memories = %d, want 0 for a lost decision race", n)
	}
}

func TestApprovalDecisionReason_LearningFailureDoesNotFailDecision(t *testing.T) {
	now := time.Now()
	s, _, _ := rejectServer(t, &memory.PendingApproval{
		ID: "req-1", Project: "/work/alpha-repo", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	s.learningStore = nil // not wired: the reason is dropped, the decision stands
	if w := decide(t, s, "req-1", `{"decision":"reject","by":"alice","reason":"nope"}`); w.status != http.StatusOK {
		t.Errorf("status = %d, want 200", w.status)
	}
}
