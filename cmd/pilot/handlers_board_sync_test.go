package main

import (
	"context"
	"testing"

	"github.com/alekspetrov/pilot/internal/adapters/github"
)

func TestSyncBoardStatus_NilBoardSync(t *testing.T) {
	// Should not panic when boardSync is nil
	syncBoardStatus(context.Background(), nil, "NODE_123", "In Progress")
}

func TestSyncBoardStatus_EmptyStatus(t *testing.T) {
	// Should return early when status is empty, even with non-nil boardSync
	client := github.NewClient("test-token")
	bs := github.NewProjectBoardSync(client, &github.ProjectBoardConfig{
		Enabled:       true,
		ProjectNumber: 1,
	}, "owner")
	// Empty status → no-op (no HTTP calls made, so no panic)
	syncBoardStatus(context.Background(), bs, "NODE_123", "")
}

func TestSyncBoardStatus_NilBoardSyncAndEmptyStatus(t *testing.T) {
	// Double nil-safety
	syncBoardStatus(context.Background(), nil, "", "")
}
