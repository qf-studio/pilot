package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// makeTag builds a Tag with the given name and commit SHA.
func makeTag(name, sha string) *Tag {
	t := &Tag{Name: name}
	t.Commit.SHA = sha
	return t
}

// TestGetTagForSHA_OldTagBeyond20 verifies that GetTagForSHA finds a tag even
// when it lives beyond the first 20 entries — the window previously used. (GH-3558)
func TestGetTagForSHA_OldTagBeyond20(t *testing.T) {
	const targetSHA = "sha-at-position-25"
	const perPage = 100

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			_, _ = fmt.Sscanf(p, "%d", &page)
		}

		w.Header().Set("Content-Type", "application/json")
		switch page {
		case 1:
			// First page: 100 tags, none at targetSHA.
			tags := make([]*Tag, perPage)
			for i := range tags {
				tags[i] = makeTag(fmt.Sprintf("v1.0.%d", i), fmt.Sprintf("sha-%d", i))
			}
			_ = json.NewEncoder(w).Encode(tags)
		case 2:
			// Second page: 25 tags, the last one is targetSHA.
			tags := make([]*Tag, 25)
			for i := range tags {
				sha := fmt.Sprintf("sha-page2-%d", i)
				if i == 24 {
					sha = targetSHA
				}
				tags[i] = makeTag(fmt.Sprintf("v2.0.%d", i), sha)
			}
			_ = json.NewEncoder(w).Encode(tags)
		default:
			_ = json.NewEncoder(w).Encode([]*Tag{})
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	tag, err := client.GetTagForSHA(context.Background(), "owner", "repo", targetSHA)
	if err != nil {
		t.Fatalf("GetTagForSHA returned error: %v", err)
	}
	if tag == "" {
		t.Fatal("expected tag name, got empty string — old 20-tag window bug may still be present")
	}
	if tag != "v2.0.24" {
		t.Errorf("tag = %q, want %q", tag, "v2.0.24")
	}
}

// TestGetTagForSHA_NotFound verifies that GetTagForSHA returns an empty string
// and no error when no tag points to the given SHA.
func TestGetTagForSHA_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return a page with tags none of which match the queried SHA.
		tags := []*Tag{
			makeTag("v1.0.0", "some-other-sha"),
			makeTag("v1.0.1", "another-sha"),
		}
		_ = json.NewEncoder(w).Encode(tags)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	tag, err := client.GetTagForSHA(context.Background(), "owner", "repo", "missing-sha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "" {
		t.Errorf("expected empty string for missing SHA, got %q", tag)
	}
}

// TestGetTagForSHA_PaginatesUntilFound verifies that GetTagForSHA stops paginating
// as soon as a match is found (does not fetch extra pages).
func TestGetTagForSHA_PaginatesUntilFound(t *testing.T) {
	const targetSHA = "target-sha"
	const perPage = 100
	pagesRequested := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		pagesRequested++

		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			_, _ = fmt.Sscanf(p, "%d", &page)
		}

		if page == 1 {
			// Full page — no match, forces a second request.
			tags := make([]*Tag, perPage)
			for i := range tags {
				tags[i] = makeTag(fmt.Sprintf("v1.%d.0", i), fmt.Sprintf("sha-%d", i))
			}
			_ = json.NewEncoder(w).Encode(tags)
			return
		}
		// Second page contains the target (first element).
		_ = json.NewEncoder(w).Encode([]*Tag{makeTag("v2.0.0", targetSHA)})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	tag, err := client.GetTagForSHA(context.Background(), "owner", "repo", targetSHA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "v2.0.0" {
		t.Errorf("tag = %q, want %q", tag, "v2.0.0")
	}
	if pagesRequested != 2 {
		t.Errorf("pages requested = %d, want 2", pagesRequested)
	}
}
