package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/memory"
)

// --- fake DecisionRecorder for unit tests ---------------------------------

type fakeDecisionRecorder struct {
	calls []fakeDecisionCall
	err   error
}

type fakeDecisionCall struct {
	requestID string
	decision  approval.Decision
	by        string
}

func (f *fakeDecisionRecorder) RecordDecision(_ context.Context, requestID string, decision approval.Decision, by string) error {
	f.calls = append(f.calls, fakeDecisionCall{requestID: requestID, decision: decision, by: by})
	return f.err
}

// --- handleApprovals unit tests --------------------------------------------

func TestHandleApprovals_SeededStoreMatches(t *testing.T) {
	now := time.Now()
	store := &mockDashboardStore{
		pendingApprovals: []*memory.PendingApproval{
			{ID: "req-1", TaskID: "GH-1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		},
		execByApprovalReqID: map[string]*memory.Execution{
			"req-1": {ID: "exec-1", ProjectPath: "/tmp/proj-a", PRUrl: "https://github.com/org/repo/pull/42"},
		},
	}
	s := newTestServerWithDashboard(store)

	req := httpTestRequest(t, http.MethodGet, "/api/v1/approvals", nil)
	w := newTestResponseRecorder()
	s.handleApprovals(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
	}
	var got []approvalResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.body.String())
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	entry := got[0]
	if entry.RequestID != "req-1" {
		t.Errorf("RequestID = %q, want req-1", entry.RequestID)
	}
	if entry.TaskID != "GH-1" {
		t.Errorf("TaskID = %q, want GH-1", entry.TaskID)
	}
	if entry.ExecutionID == nil || *entry.ExecutionID != "exec-1" {
		t.Errorf("ExecutionID = %v, want exec-1", entry.ExecutionID)
	}
	if entry.ProjectPath == nil || *entry.ProjectPath != "/tmp/proj-a" {
		t.Errorf("ProjectPath = %v, want /tmp/proj-a", entry.ProjectPath)
	}
	if entry.PRNumber == nil || *entry.PRNumber != 42 {
		t.Errorf("PRNumber = %v, want 42", entry.PRNumber)
	}
	if entry.PRUrl == nil || *entry.PRUrl != "https://github.com/org/repo/pull/42" {
		t.Errorf("PRUrl = %v, want the seeded PR URL", entry.PRUrl)
	}
}

func TestHandleApprovals_ExpiredExcluded(t *testing.T) {
	now := time.Now()
	store := &mockDashboardStore{
		pendingApprovals: []*memory.PendingApproval{
			{ID: "req-expired", TaskID: "GH-2", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)},
		},
	}
	s := newTestServerWithDashboard(store)

	req := httpTestRequest(t, http.MethodGet, "/api/v1/approvals", nil)
	w := newTestResponseRecorder()
	s.handleApprovals(w, req)

	var got []approvalResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0 (expired row should be excluded)", len(got))
	}
}

func TestHandleApprovals_ProjectScoped_ExcludesMismatch(t *testing.T) {
	now := time.Now()
	store := &mockDashboardStore{
		pendingApprovals: []*memory.PendingApproval{
			{ID: "req-a", TaskID: "GH-3", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
			{ID: "req-b", TaskID: "GH-4", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		},
		execByApprovalReqID: map[string]*memory.Execution{
			"req-a": {ID: "exec-a", ProjectPath: "/tmp/proj-a"},
			"req-b": {ID: "exec-b", ProjectPath: "/tmp/proj-b"},
		},
	}
	s := newTestServerWithDashboard(store)
	s.dashboardProjectPath = "/tmp/proj-a"

	req := httpTestRequest(t, http.MethodGet, "/api/v1/approvals", nil)
	w := newTestResponseRecorder()
	s.handleApprovals(w, req)

	var got []approvalResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0].RequestID != "req-a" {
		t.Fatalf("got = %+v, want only req-a", got)
	}
}

func TestHandleApprovals_ProjectScoped_ExcludesUnjoinable(t *testing.T) {
	now := time.Now()
	store := &mockDashboardStore{
		pendingApprovals: []*memory.PendingApproval{
			{ID: "req-orphan", TaskID: "GH-5", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		},
	}
	s := newTestServerWithDashboard(store)
	s.dashboardProjectPath = "/tmp/proj-a"

	req := httpTestRequest(t, http.MethodGet, "/api/v1/approvals", nil)
	w := newTestResponseRecorder()
	s.handleApprovals(w, req)

	var got []approvalResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0 (unjoinable row must be excluded under project scoping)", len(got))
	}
}

// TestHandleApprovals_ProjectScoped_UnjoinableRowIncludedViaProjectColumn is
// the GH-4773 regression test for the PR#4752 finding: a row with no
// execution linkage (join misses) must still be included under project
// scoping when its own `project` column matches the scope, instead of being
// dropped entirely.
func TestHandleApprovals_ProjectScoped_UnjoinableRowIncludedViaProjectColumn(t *testing.T) {
	now := time.Now()
	store := &mockDashboardStore{
		pendingApprovals: []*memory.PendingApproval{
			{ID: "req-attributed", TaskID: "GH-6", Project: "/tmp/proj-a", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		},
	}
	s := newTestServerWithDashboard(store)
	s.dashboardProjectPath = "/tmp/proj-a"

	req := httpTestRequest(t, http.MethodGet, "/api/v1/approvals", nil)
	w := newTestResponseRecorder()
	s.handleApprovals(w, req)

	var got []approvalResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0].RequestID != "req-attributed" {
		t.Fatalf("got = %+v, want only req-attributed (included via project column)", got)
	}
	if got[0].Project == nil || *got[0].Project != "/tmp/proj-a" {
		t.Errorf("Project = %v, want /tmp/proj-a", got[0].Project)
	}
	if got[0].ExecutionID != nil {
		t.Errorf("ExecutionID = %v, want nil (no execution linkage)", got[0].ExecutionID)
	}
}

// TestHandleApprovals_ProjectScoped_ProjectColumnMismatchExcluded verifies a
// row whose own project column names a different project than the scope is
// still excluded, even with no execution linkage to contradict it.
func TestHandleApprovals_ProjectScoped_ProjectColumnMismatchExcluded(t *testing.T) {
	now := time.Now()
	store := &mockDashboardStore{
		pendingApprovals: []*memory.PendingApproval{
			{ID: "req-other", TaskID: "GH-7", Project: "/tmp/proj-b", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		},
	}
	s := newTestServerWithDashboard(store)
	s.dashboardProjectPath = "/tmp/proj-a"

	req := httpTestRequest(t, http.MethodGet, "/api/v1/approvals", nil)
	w := newTestResponseRecorder()
	s.handleApprovals(w, req)

	var got []approvalResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0 (project column names a different project)", len(got))
	}
}

// TestHandleApprovals_UnscopedMode_EmitsProjectField verifies the GET
// response carries the new `project` JSON field (from approval_pending's own
// column) even when the gateway is not project-scoped.
func TestHandleApprovals_UnscopedMode_EmitsProjectField(t *testing.T) {
	now := time.Now()
	store := &mockDashboardStore{
		pendingApprovals: []*memory.PendingApproval{
			{ID: "req-1", TaskID: "GH-1", Project: "/tmp/proj-a", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
			{ID: "req-2", TaskID: "GH-2", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, // legacy, no project
		},
	}
	s := newTestServerWithDashboard(store)

	req := httpTestRequest(t, http.MethodGet, "/api/v1/approvals", nil)
	w := newTestResponseRecorder()
	s.handleApprovals(w, req)

	var got []approvalResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	byID := make(map[string]approvalResponse, len(got))
	for _, r := range got {
		byID[r.RequestID] = r
	}
	if p := byID["req-1"].Project; p == nil || *p != "/tmp/proj-a" {
		t.Errorf("req-1.Project = %v, want /tmp/proj-a", p)
	}
	if p := byID["req-2"].Project; p != nil {
		t.Errorf("req-2.Project = %v, want nil (legacy row)", p)
	}
}

func TestHandleApprovals_StoreNil_503(t *testing.T) {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 0})

	req := httpTestRequest(t, http.MethodGet, "/api/v1/approvals", nil)
	w := newTestResponseRecorder()
	s.handleApprovals(w, req)

	if w.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.status)
	}
}

func TestHandleApprovals_LoadError_500(t *testing.T) {
	store := &mockDashboardStore{loadPendingApprovalsErr: errors.New("boom")}
	s := newTestServerWithDashboard(store)

	req := httpTestRequest(t, http.MethodGet, "/api/v1/approvals", nil)
	w := newTestResponseRecorder()
	s.handleApprovals(w, req)

	if w.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.status)
	}
}

// --- handleApprovalDecision unit tests --------------------------------------

func decisionServer(store *mockDashboardStore, recorder approval.DecisionRecorder) *Server {
	s := newTestServerWithDashboard(store)
	s.decisionRecorder = recorder
	return s
}

func TestHandleApprovalDecision_Success_Approve(t *testing.T) {
	now := time.Now()
	store := &mockDashboardStore{
		pendingApprovals: []*memory.PendingApproval{
			{ID: "req-1", TaskID: "GH-1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		},
	}
	rec := &fakeDecisionRecorder{}
	s := decisionServer(store, rec)

	body := bytes.NewBufferString(`{"decision":"approve","by":"alice"}`)
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/approvals/req-1/decision", body, "requestId", "req-1")
	w := newTestResponseRecorder()
	s.handleApprovalDecision(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
	}
	if len(rec.calls) != 1 {
		t.Fatalf("recorder calls = %d, want 1", len(rec.calls))
	}
	call := rec.calls[0]
	if call.requestID != "req-1" || call.decision != approval.DecisionApproved || call.by != "alice" {
		t.Errorf("unexpected recorder call: %+v", call)
	}
	if len(store.deletedApprovalIDs) != 1 || store.deletedApprovalIDs[0] != "req-1" {
		t.Errorf("DeletePendingApproval calls = %v, want [req-1]", store.deletedApprovalIDs)
	}

	var resp decisionResponseBody
	if err := json.Unmarshal(w.body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.RequestID != "req-1" || resp.Decision != "approve" || resp.By != "alice" {
		t.Errorf("unexpected response body: %+v", resp)
	}
}

func TestHandleApprovalDecision_Success_Reject(t *testing.T) {
	now := time.Now()
	store := &mockDashboardStore{
		pendingApprovals: []*memory.PendingApproval{
			{ID: "req-2", TaskID: "GH-2", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		},
	}
	rec := &fakeDecisionRecorder{}
	s := decisionServer(store, rec)

	body := bytes.NewBufferString(`{"decision":"reject","by":"bob"}`)
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/approvals/req-2/decision", body, "requestId", "req-2")
	w := newTestResponseRecorder()
	s.handleApprovalDecision(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
	}
	if len(rec.calls) != 1 || rec.calls[0].decision != approval.DecisionRejected {
		t.Fatalf("unexpected recorder calls: %+v", rec.calls)
	}
}

func TestHandleApprovalDecision_InvalidDecisionValue_400(t *testing.T) {
	store := &mockDashboardStore{}
	rec := &fakeDecisionRecorder{}
	s := decisionServer(store, rec)

	body := bytes.NewBufferString(`{"decision":"maybe","by":"alice"}`)
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/approvals/req-1/decision", body, "requestId", "req-1")
	w := newTestResponseRecorder()
	s.handleApprovalDecision(w, req)

	if w.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.status)
	}
	if len(rec.calls) != 0 {
		t.Errorf("recorder should not be called on invalid decision, got %d calls", len(rec.calls))
	}
}

func TestHandleApprovalDecision_InvalidJSON_400(t *testing.T) {
	store := &mockDashboardStore{}
	rec := &fakeDecisionRecorder{}
	s := decisionServer(store, rec)

	body := bytes.NewBufferString(`not json`)
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/approvals/req-1/decision", body, "requestId", "req-1")
	w := newTestResponseRecorder()
	s.handleApprovalDecision(w, req)

	if w.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.status)
	}
}

func TestHandleApprovalDecision_MissingBy_400(t *testing.T) {
	store := &mockDashboardStore{}
	rec := &fakeDecisionRecorder{}
	s := decisionServer(store, rec)

	body := bytes.NewBufferString(`{"decision":"approve","by":""}`)
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/approvals/req-1/decision", body, "requestId", "req-1")
	w := newTestResponseRecorder()
	s.handleApprovalDecision(w, req)

	if w.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.status)
	}
}

func TestHandleApprovalDecision_UnknownRequestID_404(t *testing.T) {
	store := &mockDashboardStore{}
	rec := &fakeDecisionRecorder{}
	s := decisionServer(store, rec)

	body := bytes.NewBufferString(`{"decision":"approve","by":"alice"}`)
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/approvals/nonexistent/decision", body, "requestId", "nonexistent")
	w := newTestResponseRecorder()
	s.handleApprovalDecision(w, req)

	if w.status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.status)
	}
	if len(rec.calls) != 0 {
		t.Errorf("recorder should not be called for unknown requestId, got %d calls", len(rec.calls))
	}
}

func TestHandleApprovalDecision_AlreadyDecided_409(t *testing.T) {
	// Not present in the pending list anymore, but the linked execution
	// already carries a recorded decision -- simulates a double-decide.
	store := &mockDashboardStore{
		execByApprovalReqID: map[string]*memory.Execution{
			"req-done": {ID: "exec-done", ApprovalDecision: "approved"},
		},
	}
	rec := &fakeDecisionRecorder{}
	s := decisionServer(store, rec)

	body := bytes.NewBufferString(`{"decision":"approve","by":"alice"}`)
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/approvals/req-done/decision", body, "requestId", "req-done")
	w := newTestResponseRecorder()
	s.handleApprovalDecision(w, req)

	if w.status != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.status)
	}
	if len(rec.calls) != 0 {
		t.Errorf("recorder should not be called for already-decided requestId, got %d calls", len(rec.calls))
	}
}

func TestHandleApprovalDecision_RecorderNil_503(t *testing.T) {
	store := &mockDashboardStore{}
	s := decisionServer(store, nil)

	body := bytes.NewBufferString(`{"decision":"approve","by":"alice"}`)
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/approvals/req-1/decision", body, "requestId", "req-1")
	w := newTestResponseRecorder()
	s.handleApprovalDecision(w, req)

	if w.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.status)
	}
}

func TestHandleApprovalDecision_StoreNil_503(t *testing.T) {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 0})
	s.decisionRecorder = &fakeDecisionRecorder{}

	body := bytes.NewBufferString(`{"decision":"approve","by":"alice"}`)
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/approvals/req-1/decision", body, "requestId", "req-1")
	w := newTestResponseRecorder()
	s.handleApprovalDecision(w, req)

	if w.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.status)
	}
}

func TestHandleApprovalDecision_RecordDecisionError_500(t *testing.T) {
	now := time.Now()
	store := &mockDashboardStore{
		pendingApprovals: []*memory.PendingApproval{
			{ID: "req-err", TaskID: "GH-9", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		},
	}
	rec := &fakeDecisionRecorder{err: errors.New("boom")}
	s := decisionServer(store, rec)

	body := bytes.NewBufferString(`{"decision":"approve","by":"alice"}`)
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/approvals/req-err/decision", body, "requestId", "req-err")
	w := newTestResponseRecorder()
	s.handleApprovalDecision(w, req)

	if w.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.status)
	}
	// Cleanup must not run if the recorder itself failed.
	if len(store.deletedApprovalIDs) != 0 {
		t.Errorf("DeletePendingApproval should not run after a RecordDecision error, got %v", store.deletedApprovalIDs)
	}
}

// TestHandleApprovalDecision_RaceLoss_409 simulates the loser of a decision
// race: the request is still listed pending (findPendingApproval succeeds),
// but by the time RecordDecision runs, Store.SetApprovalDecision's atomic
// guard has already rejected the write because another decider won first.
// The handler must surface 409 (not the previous last-writer-wins 200) and
// still clean up the now-stale pending row (GH-4757 acceptance criterion 1).
func TestHandleApprovalDecision_RaceLoss_409(t *testing.T) {
	now := time.Now()
	store := &mockDashboardStore{
		pendingApprovals: []*memory.PendingApproval{
			{ID: "req-race", TaskID: "GH-10", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		},
	}
	rec := &fakeDecisionRecorder{err: memory.ErrApprovalAlreadyDecided}
	s := decisionServer(store, rec)

	body := bytes.NewBufferString(`{"decision":"approve","by":"alice"}`)
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/approvals/req-race/decision", body, "requestId", "req-race")
	w := newTestResponseRecorder()
	s.handleApprovalDecision(w, req)

	if w.status != http.StatusConflict {
		t.Errorf("status = %d, want 409 (body=%s)", w.status, w.body.String())
	}
	if len(store.deletedApprovalIDs) != 1 || store.deletedApprovalIDs[0] != "req-race" {
		t.Errorf("DeletePendingApproval calls = %v, want [req-race] (stale pending row must still be cleaned up)", store.deletedApprovalIDs)
	}
}

// TestHandleApprovalDecision_UnlinkedRequest_ResolvesInsteadOf500 covers the
// second integrity gap: a pending row with no execution linkage (
// SetApprovalRequestID never ran) makes Store.SetApprovalDecision return
// sql.ErrNoRows. The handler must align with Telegram/Slack channel
// semantics — warn-log and still resolve the request — instead of the
// previous 500 that left the row undecidable forever over HTTP
// (GH-4757 acceptance criterion 2).
func TestHandleApprovalDecision_UnlinkedRequest_ResolvesInsteadOf500(t *testing.T) {
	now := time.Now()
	store := &mockDashboardStore{
		pendingApprovals: []*memory.PendingApproval{
			{ID: "req-unlinked", TaskID: "GH-11", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		},
		// No execByApprovalReqID entry for "req-unlinked" — mirrors GET
		// listing it with executionId: null.
	}
	rec := &fakeDecisionRecorder{err: sql.ErrNoRows}
	s := decisionServer(store, rec)

	body := bytes.NewBufferString(`{"decision":"reject","by":"bob"}`)
	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/approvals/req-unlinked/decision", body, "requestId", "req-unlinked")
	w := newTestResponseRecorder()
	s.handleApprovalDecision(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
	}
	if len(store.deletedApprovalIDs) != 1 || store.deletedApprovalIDs[0] != "req-unlinked" {
		t.Errorf("DeletePendingApproval calls = %v, want [req-unlinked]", store.deletedApprovalIDs)
	}
	var resp decisionResponseBody
	if err := json.Unmarshal(w.body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.RequestID != "req-unlinked" || resp.Decision != "reject" || resp.By != "bob" {
		t.Errorf("unexpected response body: %+v", resp)
	}
}

// --- composed / integration test: real store + real manager + real mux ----

func TestApprovalsAPI_Composed(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	mgr := approval.NewManager(approval.DefaultConfig()).WithStateWriter(store)

	const execID = "exec-composed-1"
	const reqID = "req-composed-1"
	if err := store.SaveExecution(&memory.Execution{
		ID:                execID,
		TaskID:            "GH-4748",
		ProjectPath:       "/tmp/composed-proj",
		Status:            "running",
		ApprovalRequestID: reqID,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.InsertPendingApproval(&memory.PendingApproval{
		ID:        reqID,
		TaskID:    "GH-4748",
		Stage:     "pre_merge",
		Title:     "Composed test approval",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("InsertPendingApproval: %v", err)
	}

	config := &Config{Host: "127.0.0.1", Port: 19094}
	authConfig := &AuthConfig{Type: AuthTypeAPIToken, Token: "composed-secret"}
	server := NewServerWithAuth(config, authConfig)
	server.SetDashboardStore(store)
	server.SetDecisionRecorder(mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://127.0.0.1:19094"

	// 1. Auth rejected without a bearer token, on both endpoints.
	getReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/approvals", nil)
	getResp, err := client.Do(getReq)
	if err != nil {
		t.Fatalf("GET (no auth): %v", err)
	}
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET no-auth status = %d, want 401", getResp.StatusCode)
	}

	postReq, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/approvals/"+reqID+"/decision",
		bytes.NewBufferString(`{"decision":"approve","by":"alice"}`))
	postResp, err := client.Do(postReq)
	if err != nil {
		t.Fatalf("POST (no auth): %v", err)
	}
	_ = postResp.Body.Close()
	if postResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST no-auth status = %d, want 401", postResp.StatusCode)
	}

	// 2. GET with a valid token returns the seeded pending entry.
	getReq2, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/approvals", nil)
	getReq2.Header.Set("Authorization", "Bearer composed-secret")
	getResp2, err := client.Do(getReq2)
	if err != nil {
		t.Fatalf("GET (auth): %v", err)
	}
	defer func() { _ = getResp2.Body.Close() }()
	if getResp2.StatusCode != http.StatusOK {
		t.Fatalf("GET auth status = %d, want 200", getResp2.StatusCode)
	}
	var listed []approvalResponse
	if err := json.NewDecoder(getResp2.Body).Decode(&listed); err != nil {
		t.Fatalf("decode GET body: %v", err)
	}
	if len(listed) != 1 || listed[0].RequestID != reqID {
		t.Fatalf("listed = %+v, want single entry with RequestID %q", listed, reqID)
	}
	if listed[0].ExecutionID == nil || *listed[0].ExecutionID != execID {
		t.Errorf("listed[0].ExecutionID = %v, want %q", listed[0].ExecutionID, execID)
	}

	// 3. POST with a valid token records a decision, persisting identically
	// to a channel decision (assert approval_decision/_at/_by columns).
	postReq2, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/approvals/"+reqID+"/decision",
		bytes.NewBufferString(`{"decision":"approve","by":"alice"}`))
	postReq2.Header.Set("Authorization", "Bearer composed-secret")
	postReq2.Header.Set("Content-Type", "application/json")
	postResp2, err := client.Do(postReq2)
	if err != nil {
		t.Fatalf("POST (auth): %v", err)
	}
	_ = postResp2.Body.Close()
	if postResp2.StatusCode != http.StatusOK {
		t.Fatalf("POST auth status = %d, want 200", postResp2.StatusCode)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution after decision: %v", err)
	}
	if exec.ApprovalDecision != "approved" {
		t.Errorf("ApprovalDecision = %q, want approved", exec.ApprovalDecision)
	}
	if exec.ApprovalDecisionBy != "alice" {
		t.Errorf("ApprovalDecisionBy = %q, want alice", exec.ApprovalDecisionBy)
	}
	if exec.ApprovalDecisionAt == nil {
		t.Error("ApprovalDecisionAt should be set after the decision")
	}

	// 4. Double-decide on the same requestId returns 404 or 409.
	postReq3, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/approvals/"+reqID+"/decision",
		bytes.NewBufferString(`{"decision":"approve","by":"alice"}`))
	postReq3.Header.Set("Authorization", "Bearer composed-secret")
	postReq3.Header.Set("Content-Type", "application/json")
	postResp3, err := client.Do(postReq3)
	if err != nil {
		t.Fatalf("POST (double-decide): %v", err)
	}
	_ = postResp3.Body.Close()
	if postResp3.StatusCode != http.StatusNotFound && postResp3.StatusCode != http.StatusConflict {
		t.Errorf("double-decide status = %d, want 404 or 409", postResp3.StatusCode)
	}
}

// --- small test helpers ------------------------------------------------

// newTestResponseRecorder avoids importing net/http/httptest just for a
// minimal ResponseWriter; kept local so this file has no new dependencies
// beyond what the rest of the package already uses.
type testResponseRecorder struct {
	status int
	body   *bytes.Buffer
	header http.Header
}

func newTestResponseRecorder() *testResponseRecorder {
	return &testResponseRecorder{status: http.StatusOK, body: &bytes.Buffer{}, header: http.Header{}}
}

func (r *testResponseRecorder) Header() http.Header { return r.header }
func (r *testResponseRecorder) Write(b []byte) (int, error) {
	return r.body.Write(b)
}
func (r *testResponseRecorder) WriteHeader(statusCode int) { r.status = statusCode }

func httpTestRequest(t *testing.T, method, target string, body *bytes.Buffer) *http.Request {
	t.Helper()
	var b *bytes.Buffer
	if body == nil {
		b = &bytes.Buffer{}
	} else {
		b = body
	}
	req, err := http.NewRequest(method, target, b)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return req
}

func httpTestRequestWithPathValue(t *testing.T, method, target string, body *bytes.Buffer, key, value string) *http.Request {
	t.Helper()
	req := httpTestRequest(t, method, target, body)
	req.SetPathValue(key, value)
	return req
}
