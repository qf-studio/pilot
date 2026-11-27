package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	planeSDK "github.com/qf-studio/studio-sdk/sdk/integrations/plane"
)

const (
	planeDoneWorkspace = "test-workspace"
	planeDoneProject   = "plane-proj"
	planeDoneWorkItem  = "plane-wi"
	planeDonePRURL     = "https://github.com/acme/web/pull/42"
)

// planeDoneServer is a fake Plane API for the merge leg: it serves the
// states of one project and records the state change and comment requests.
type planeDoneServer struct {
	statesBody  string
	stateStatus int // status of the state change request (0 = 200)

	mu            sync.Mutex
	stateBodies   []map[string]any
	commentBodies []map[string]any
}

func (s *planeDoneServer) handler(t *testing.T) http.HandlerFunc {
	base := "/api/v1/workspaces/" + planeDoneWorkspace + "/projects/" + planeDoneProject
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base+"/states/":
			_, _ = w.Write([]byte(s.statesBody))
		case r.Method == http.MethodPatch && r.URL.Path == base+"/work-items/"+planeDoneWorkItem+"/":
			s.mu.Lock()
			s.stateBodies = append(s.stateBodies, decodeBody(t, r.Body))
			s.mu.Unlock()
			if s.stateStatus != 0 {
				w.WriteHeader(s.stateStatus)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == base+"/work-items/"+planeDoneWorkItem+"/comments/":
			_, _ = w.Write([]byte(`{"results":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == base+"/work-items/"+planeDoneWorkItem+"/comments/":
			s.mu.Lock()
			s.commentBodies = append(s.commentBodies, decodeBody(t, r.Body))
			s.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

func decodeBody(t *testing.T, body io.Reader) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(body).Decode(&m); err != nil {
		t.Errorf("decode request body: %v", err)
	}
	return m
}

func newTestPlaneDoneNotifier(t *testing.T, s *planeDoneServer) *planeDoneNotifier {
	t.Helper()
	srv := httptest.NewServer(s.handler(t))
	t.Cleanup(srv.Close)
	return newPlaneDoneNotifier(planeSDK.NewClient(srv.URL, "fake-api-key"), planeDoneWorkspace)
}

// TestPlaneDoneNotifier_MovesToCompletedAndComments: the state request
// carries the ID of the first state of the completed group, and exactly one
// comment is posted with the PR link and the merged external ID.
func TestPlaneDoneNotifier_MovesToCompletedAndComments(t *testing.T) {
	s := &planeDoneServer{statesBody: `{"results":[
		{"id":"state-backlog","name":"Backlog","group":"backlog"},
		{"id":"state-started","name":"In Progress","group":"started"},
		{"id":"state-done","name":"Done","group":"completed"},
		{"id":"state-shipped","name":"Shipped","group":"completed"}
	]}`}
	n := newTestPlaneDoneNotifier(t, s)

	if err := n.NotifyMerged(context.Background(), planeDoneProject, planeDoneWorkItem, planeDonePRURL); err != nil {
		t.Fatalf("NotifyMerged: %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stateBodies) != 1 {
		t.Fatalf("state requests = %d, want 1", len(s.stateBodies))
	}
	if got := s.stateBodies[0]["state"]; got != "state-done" {
		t.Errorf("state request state = %v, want %q (the first completed-group state)", got, "state-done")
	}
	if len(s.commentBodies) != 1 {
		t.Fatalf("comment requests = %d, want 1", len(s.commentBodies))
	}
	c := s.commentBodies[0]
	if html, _ := c["comment_html"].(string); !strings.Contains(html, planeDonePRURL) {
		t.Errorf("comment %q does not hold the PR link %q", html, planeDonePRURL)
	}
	if got, want := c["external_id"], "pilot-merged-"+planeDoneWorkItem; got != want {
		t.Errorf("comment external_id = %v, want %q", got, want)
	}
	if got := c["external_source"]; got != "pilot" {
		t.Errorf("comment external_source = %v, want %q", got, "pilot")
	}
}

// TestPlaneDoneNotifier_NoCompletedState: a project with no completed-group
// state returns an error that names the project; no state change and no
// comment.
func TestPlaneDoneNotifier_NoCompletedState(t *testing.T) {
	s := &planeDoneServer{statesBody: `{"results":[
		{"id":"state-backlog","name":"Backlog","group":"backlog"},
		{"id":"state-started","name":"In Progress","group":"started"}
	]}`}
	n := newTestPlaneDoneNotifier(t, s)

	err := n.NotifyMerged(context.Background(), planeDoneProject, planeDoneWorkItem, planeDonePRURL)
	if err == nil {
		t.Fatal("NotifyMerged returned nil for a project with no completed state")
	}
	if !strings.Contains(err.Error(), planeDoneProject) {
		t.Errorf("error %q does not name the project %q", err, planeDoneProject)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stateBodies) != 0 {
		t.Errorf("state requests = %d, want 0", len(s.stateBodies))
	}
	if len(s.commentBodies) != 0 {
		t.Errorf("comment requests = %d, want 0", len(s.commentBodies))
	}
}

// TestPlaneDoneNotifier_StateErrorNoComment: a failed state change is
// returned and no comment is posted.
func TestPlaneDoneNotifier_StateErrorNoComment(t *testing.T) {
	s := &planeDoneServer{
		statesBody:  `{"results":[{"id":"state-done","name":"Done","group":"completed"}]}`,
		stateStatus: http.StatusBadRequest,
	}
	n := newTestPlaneDoneNotifier(t, s)

	if err := n.NotifyMerged(context.Background(), planeDoneProject, planeDoneWorkItem, planeDonePRURL); err == nil {
		t.Fatal("NotifyMerged returned nil although the state change failed")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stateBodies) != 1 {
		t.Errorf("state requests = %d, want 1", len(s.stateBodies))
	}
	if len(s.commentBodies) != 0 {
		t.Errorf("comment requests = %d after a failed state change, want 0", len(s.commentBodies))
	}
}
