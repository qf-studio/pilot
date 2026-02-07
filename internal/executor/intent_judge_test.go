package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIntentJudge_Pass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"content":[{"text":"VERDICT:PASS\nThe diff correctly implements the requested feature.\nCONFIDENCE:0.95"}]}`)
	}))
	defer server.Close()

	judge := newIntentJudgeWithURL("fake-api-key", server.URL)
	verdict, err := judge.Judge(context.Background(), "Add login button", "Add a login button to the header", "diff --git a/header.go\n+func LoginButton()")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Passed {
		t.Error("expected PASS verdict")
	}
	if verdict.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", verdict.Confidence)
	}
}

func TestIntentJudge_Fail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"content":[{"text":"VERDICT:FAIL\nThe diff adds database migration but the issue only asked for a UI change.\nCONFIDENCE:0.85"}]}`)
	}))
	defer server.Close()

	judge := newIntentJudgeWithURL("fake-api-key", server.URL)
	verdict, err := judge.Judge(context.Background(), "Fix button color", "Change the submit button to blue", "diff --git a/db/migration.sql\n+ALTER TABLE users ADD COLUMN theme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Passed {
		t.Error("expected FAIL verdict")
	}
	if verdict.Reason == "" {
		t.Error("expected non-empty reason")
	}
	if verdict.Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", verdict.Confidence)
	}
}

func TestIntentJudge_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	judge := newIntentJudgeWithURL("fake-api-key", server.URL)
	_, err := judge.Judge(context.Background(), "title", "body", "some diff")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention status 500, got: %v", err)
	}
}

func TestIntentJudge_MalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"content":[{"text":"I think this looks good but I'm not sure."}]}`)
	}))
	defer server.Close()

	judge := newIntentJudgeWithURL("fake-api-key", server.URL)
	_, err := judge.Judge(context.Background(), "title", "body", "some diff")
	if err == nil {
		t.Fatal("expected error for malformed response")
	}
	if !strings.Contains(err.Error(), "VERDICT") {
		t.Errorf("expected error about missing VERDICT, got: %v", err)
	}
}

func TestIntentJudge_EmptyDiff(t *testing.T) {
	judge := NewIntentJudge("fake-api-key")
	_, err := judge.Judge(context.Background(), "title", "body", "")
	if err == nil {
		t.Fatal("expected error for empty diff")
	}
	if !strings.Contains(err.Error(), "empty diff") {
		t.Errorf("expected 'empty diff' error, got: %v", err)
	}
}

func TestIntentJudge_DiffTruncation(t *testing.T) {
	var receivedContent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req haikuRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && len(req.Messages) > 0 {
			receivedContent = req.Messages[0].Content
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"content":[{"text":"VERDICT:PASS\nLooks good.\nCONFIDENCE:0.9"}]}`)
	}))
	defer server.Close()

	// Create a diff larger than maxDiffCharsDefault (8000)
	largeDiff := strings.Repeat("x", 10000)

	judge := newIntentJudgeWithURL("fake-api-key", server.URL)
	verdict, err := judge.Judge(context.Background(), "title", "body", largeDiff)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Passed {
		t.Error("expected PASS verdict")
	}
	if !strings.Contains(receivedContent, "...[truncated]") {
		t.Error("expected diff to be truncated")
	}
}

func TestIntentJudge_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	judge := newIntentJudgeWithURL("fake-api-key", server.URL)
	_, err := judge.Judge(ctx, "title", "body", "some diff")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestNewIntentJudge(t *testing.T) {
	judge := NewIntentJudge("test-key")
	if judge.apiKey != "test-key" {
		t.Errorf("expected apiKey 'test-key', got %q", judge.apiKey)
	}
	if judge.apiURL != "https://api.anthropic.com/v1/messages" {
		t.Errorf("unexpected apiURL: %s", judge.apiURL)
	}
	if judge.model != "claude-haiku-4-5-20251001" {
		t.Errorf("unexpected model: %s", judge.model)
	}
	if judge.httpClient == nil {
		t.Error("expected non-nil httpClient")
	}
}
