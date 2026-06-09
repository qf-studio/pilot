package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
)

// stubBoardSyncer is a minimal projectBoardSyncer for testing.
type stubBoardSyncer struct {
	err   error
	calls []string // accumulated nodeID+status pairs, joined as "nodeID:status"
}

func (s *stubBoardSyncer) UpdateProjectItemStatus(_ context.Context, nodeID, status string) error {
	s.calls = append(s.calls, nodeID+":"+status)
	return s.err
}

// newSpecGuardServer returns a minimal HTTP server that satisfies the GitHub API
// calls made by applySpecGuard: list comments (returns empty list) and accept
// POST label / comment requests without error.
func newSpecGuardServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"ok"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
}

func TestApplySpecGuard_BoardSync(t *testing.T) {
	cases := []struct {
		name         string
		nodeID       string
		failedStatus string
		syncer       *stubBoardSyncer // nil means pass nil interface
		wantCalls    int              // expected number of UpdateProjectItemStatus calls
		wantErr      bool             // syncer.err set to non-nil
	}{
		{
			name:         "happy-path write",
			nodeID:       "I_node1",
			failedStatus: "Failed",
			syncer:       &stubBoardSyncer{},
			wantCalls:    1,
		},
		{
			name:         "nil syncer skip",
			nodeID:       "I_node1",
			failedStatus: "Failed",
			syncer:       nil,
			wantCalls:    0,
		},
		{
			name:         "empty NodeID skip",
			nodeID:       "",
			failedStatus: "Failed",
			syncer:       &stubBoardSyncer{},
			wantCalls:    0,
		},
		{
			name:         "empty failedStatus skip",
			nodeID:       "I_node1",
			failedStatus: "",
			syncer:       &stubBoardSyncer{},
			wantCalls:    0,
		},
		{
			name:         "transport-error swallow",
			nodeID:       "I_node1",
			failedStatus: "Failed",
			syncer:       &stubBoardSyncer{err: errors.New("network timeout")},
			wantCalls:    1,
			wantErr:      true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSpecGuardServer(t)
			defer srv.Close()

			client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
			issue := &github.Issue{Number: 42, NodeID: tc.nodeID}

			var syncer projectBoardSyncer
			if tc.syncer != nil {
				syncer = tc.syncer
			}

			got := applySpecGuard(context.Background(), client, "owner", "repo", issue, []string{"thin body"}, syncer, tc.failedStatus)
			if !got {
				t.Fatal("applySpecGuard returned false (no-skip); expected true")
			}

			if tc.syncer == nil {
				return
			}
			if len(tc.syncer.calls) != tc.wantCalls {
				t.Errorf("UpdateProjectItemStatus called %d times, want %d", len(tc.syncer.calls), tc.wantCalls)
			}
			if tc.wantCalls > 0 && len(tc.syncer.calls) > 0 {
				want := tc.nodeID + ":" + tc.failedStatus
				if tc.syncer.calls[0] != want {
					t.Errorf("UpdateProjectItemStatus called with %q, want %q", tc.syncer.calls[0], want)
				}
			}
		})
	}
}
