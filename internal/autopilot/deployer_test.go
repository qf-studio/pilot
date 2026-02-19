package autopilot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alekspetrov/pilot/internal/adapters/github"
)

func TestDeployer_None(t *testing.T) {
	d := NewDeployer(nil, "owner", "repo", &PostMergeConfig{Action: "none"})
	err := d.Deploy(context.Background(), &PRState{PRNumber: 1})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestDeployer_Tag(t *testing.T) {
	d := NewDeployer(nil, "owner", "repo", &PostMergeConfig{Action: "tag"})
	err := d.Deploy(context.Background(), &PRState{PRNumber: 1})
	if err != nil {
		t.Fatalf("expected nil error for tag action (delegated to releaser), got %v", err)
	}
}

func TestDeployer_Webhook(t *testing.T) {
	var receivedBody deployWebhookPayload
	var receivedHeaders http.Header
	var receivedSignature string

	secret := "test-webhook-secret"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header

		bodyBytes, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(bodyBytes, &receivedBody)
		receivedSignature = r.Header.Get("X-Hub-Signature-256")

		// Verify HMAC
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(bodyBytes)
		expectedSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if receivedSignature != expectedSig {
			t.Errorf("HMAC mismatch: got %s, want %s", receivedSignature, expectedSig)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &PostMergeConfig{
		Action:     "webhook",
		WebhookURL: server.URL,
		WebhookHeaders: map[string]string{
			"X-Custom-Header": "custom-value",
		},
		WebhookSecret: secret,
	}

	d := NewDeployer(nil, "owner", "repo", cfg)
	prState := &PRState{
		PRNumber:     42,
		HeadSHA:      "abc1234567890",
		TargetBranch: "main",
	}

	err := d.Deploy(context.Background(), prState)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	// Verify payload
	if receivedBody.Action != "deploy" {
		t.Errorf("expected action 'deploy', got %q", receivedBody.Action)
	}
	if receivedBody.PRNumber != 42 {
		t.Errorf("expected PR number 42, got %d", receivedBody.PRNumber)
	}
	if receivedBody.HeadSHA != "abc1234567890" {
		t.Errorf("expected SHA 'abc1234567890', got %q", receivedBody.HeadSHA)
	}
	if receivedBody.Repo != "owner/repo" {
		t.Errorf("expected repo 'owner/repo', got %q", receivedBody.Repo)
	}

	// Verify custom header
	if receivedHeaders.Get("X-Custom-Header") != "custom-value" {
		t.Errorf("expected custom header 'custom-value', got %q", receivedHeaders.Get("X-Custom-Header"))
	}

	// Verify HMAC signature was present
	if receivedSignature == "" {
		t.Error("expected X-Hub-Signature-256 header to be set")
	}
}

func TestDeployer_BranchPush(t *testing.T) {
	var capturedPath string
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PATCH" && r.URL.Path == "/repos/owner/repo/git/refs/heads/deploy-prod":
			capturedPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL("test-token", server.URL)

	cfg := &PostMergeConfig{
		Action:       "branch-push",
		DeployBranch: "deploy-prod",
	}

	d := NewDeployer(ghClient, "owner", "repo", cfg)
	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234567890",
	}

	err := d.Deploy(context.Background(), prState)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if capturedPath != "/repos/owner/repo/git/refs/heads/deploy-prod" {
		t.Errorf("expected path to refs/heads/deploy-prod, got %q", capturedPath)
	}

	if sha, ok := capturedBody["sha"].(string); !ok || sha != "abc1234567890" {
		t.Errorf("expected SHA 'abc1234567890' in body, got %v", capturedBody["sha"])
	}

	if force, ok := capturedBody["force"].(bool); !ok || !force {
		t.Errorf("expected force=true in body, got %v", capturedBody["force"])
	}
}

func TestDeployer_UnknownAction(t *testing.T) {
	d := NewDeployer(nil, "owner", "repo", &PostMergeConfig{Action: "invalid"})
	err := d.Deploy(context.Background(), &PRState{PRNumber: 1})
	if err == nil {
		t.Fatal("expected error for unknown action, got nil")
	}
	expected := `unknown deploy action: "invalid"`
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

func TestDeployer_BranchPush_NoBranch(t *testing.T) {
	d := NewDeployer(nil, "owner", "repo", &PostMergeConfig{
		Action:       "branch-push",
		DeployBranch: "",
	})
	err := d.Deploy(context.Background(), &PRState{PRNumber: 1})
	if err == nil {
		t.Fatal("expected error for empty deploy branch, got nil")
	}
	if err.Error() != "deploy_branch is required for branch-push deploy action" {
		t.Errorf("unexpected error: %v", err)
	}
}
