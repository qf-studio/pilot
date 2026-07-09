package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// signGithubPayload computes a GitHub-style "sha256=<hex hmac>" signature,
// matching what the studio-sdk github.VerifyWebhookSignature expects.
func signGithubPayload(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// TestGithubWebhookSignatureVerification is a GH-4156 regression suite: the
// gateway now calls studio-sdk's github.VerifyWebhookSignature instead of the
// in-tree internal/adapters/github implementation. It pins that swap did not
// change accept/reject behavior, and that the gateway's own
// s.githubWebhookSecret != "" guard (server.go handleGithubWebhook) still
// short-circuits verification when no secret is configured — masking the
// SDK function's own fail-open ("" secret == always valid) semantics behind
// an explicit, auditable gateway-level decision.
func TestGithubWebhookSignatureVerification(t *testing.T) {
	payload := `{"action":"opened","issue":{"number":1}}`

	t.Run("valid signature with configured secret is accepted", func(t *testing.T) {
		config := &Config{Host: "127.0.0.1", Port: 9090, GithubWebhookSecret: "test-github-secret"}
		server := NewServer(config)

		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(payload))
		req.Header.Set("X-GitHub-Event", "issues")
		req.Header.Set("X-Hub-Signature-256", signGithubPayload("test-github-secret", []byte(payload)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleGithubWebhook(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("invalid signature with configured secret is rejected", func(t *testing.T) {
		config := &Config{Host: "127.0.0.1", Port: 9090, GithubWebhookSecret: "test-github-secret"}
		server := NewServer(config)

		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(payload))
		req.Header.Set("X-GitHub-Event", "issues")
		req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleGithubWebhook(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("empty configured secret short-circuits verification", func(t *testing.T) {
		config := &Config{Host: "127.0.0.1", Port: 9090} // GithubWebhookSecret left empty
		server := NewServer(config)

		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(payload))
		req.Header.Set("X-GitHub-Event", "issues")
		req.Header.Set("X-Hub-Signature-256", "sha256=not-even-hex-garbage")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		server.handleGithubWebhook(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 (verification skipped), got %d: %s", w.Code, w.Body.String())
		}
	})
}
