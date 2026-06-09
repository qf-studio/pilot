package main

import (
	"context"
	"errors"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
)

// mockBoardSyncer is a test double for projectBoardSyncer.
type mockBoardSyncer struct {
	called    bool
	nodeID    string
	status    string
	returnErr error
}

func (m *mockBoardSyncer) UpdateProjectItemStatus(_ context.Context, nodeID, status string) error {
	m.called = true
	m.nodeID = nodeID
	m.status = status
	return m.returnErr
}

func TestApplySpecGuard_BoardSync(t *testing.T) {
	tests := []struct {
		name          string
		boardSync     projectBoardSyncer
		nodeID        string
		failedStatus  string
		boardCallWant bool
		wantNodeID    string
		wantStatus    string
	}{
		{
			name:          "happy-path write",
			boardSync:     &mockBoardSyncer{},
			nodeID:        "NODE_42",
			failedStatus:  "Failed",
			boardCallWant: true,
			wantNodeID:    "NODE_42",
			wantStatus:    "Failed",
		},
		{
			name:          "nil syncer skip",
			boardSync:     nil,
			nodeID:        "NODE_42",
			failedStatus:  "Failed",
			boardCallWant: false,
		},
		{
			name:          "empty NodeID skip",
			boardSync:     &mockBoardSyncer{},
			nodeID:        "",
			failedStatus:  "Failed",
			boardCallWant: false,
		},
		{
			name:          "empty failedStatus skip",
			boardSync:     &mockBoardSyncer{},
			nodeID:        "NODE_42",
			failedStatus:  "",
			boardCallWant: false,
		},
		{
			name:          "transport-error swallow",
			boardSync:     &mockBoardSyncer{returnErr: errors.New("graphql: timeout")},
			nodeID:        "NODE_42",
			failedStatus:  "Failed",
			boardCallWant: true,
			wantNodeID:    "NODE_42",
			wantStatus:    "Failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var labelsAdded []string
			srv := newSpecTestServer(t, `[]`, &labelsAdded)
			defer srv.Close()

			client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
			issue := &github.Issue{Number: 42, NodeID: tc.nodeID}

			result := applySpecGuard(
				context.Background(), client, "owner", "repo",
				issue, []string{"body too short"},
				tc.boardSync, tc.failedStatus,
			)

			if !result {
				t.Fatal("expected applySpecGuard to return true (guard fired)")
			}

			// For nil syncer, there is no mock to inspect.
			if tc.boardSync == nil {
				return
			}
			mock, ok := tc.boardSync.(*mockBoardSyncer)
			if !ok {
				return
			}
			if mock.called != tc.boardCallWant {
				t.Errorf("UpdateProjectItemStatus called=%v, want %v", mock.called, tc.boardCallWant)
			}
			if tc.boardCallWant {
				if mock.nodeID != tc.wantNodeID {
					t.Errorf("nodeID=%q, want %q", mock.nodeID, tc.wantNodeID)
				}
				if mock.status != tc.wantStatus {
					t.Errorf("status=%q, want %q", mock.status, tc.wantStatus)
				}
			}
		})
	}
}
