package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

// computeSlackSig produces a valid Slack request signature.
// Base string: "v0:{timestamp}:{body}"; result: "v0=" + HMAC-SHA256(base, secret).
func computeSlackSig(signingSecret, timestamp, body string) string {
	base := fmt.Sprintf("v0:%s:%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte(base))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// nowTS returns the current Unix timestamp as a string.
func nowTS() string {
	return fmt.Sprintf("%d", time.Now().Unix())
}

// shiftedTS returns now shifted by delta seconds as a string.
func shiftedTS(delta int64) string {
	return fmt.Sprintf("%d", time.Now().Unix()+delta)
}

func TestNewInteractionHandler(t *testing.T) {
	h := NewInteractionHandler(testutil.FakeWebhookSecret)
	if h == nil {
		t.Fatal("NewInteractionHandler returned nil")
	}
	if h.signingSecret != testutil.FakeWebhookSecret {
		t.Errorf("signingSecret = %q, want %q", h.signingSecret, testutil.FakeWebhookSecret)
	}
	if h.log == nil {
		t.Error("log must not be nil")
	}
}

func TestOnAction(t *testing.T) {
	h := NewInteractionHandler("")
	if h.onAction != nil {
		t.Error("onAction should be nil before registration")
	}
	h.OnAction(func(a *InteractionAction) bool { return true })
	if h.onAction == nil {
		t.Error("OnAction did not register callback")
	}
}

// TestVerifySignature exercises the HMAC gate directly.
// Note: verifySignature is only invoked when signingSecret != ""; the empty-secret
// dev bypass lives in ServeHTTP and is covered by TestServeHTTP_DevModeBypass.
func TestVerifySignature(t *testing.T) {
	secret := testutil.FakeWebhookSecret
	body := "payload=test-body-content"
	ts := nowTS()
	validSig := computeSlackSig(secret, ts, body)

	tests := []struct {
		name      string
		timestamp string
		body      string
		signature string
		want      bool
	}{
		{
			name:      "valid signature",
			timestamp: ts,
			body:      body,
			signature: validSig,
			want:      true,
		},
		{
			name:      "tampered body",
			timestamp: ts,
			body:      body + "-tampered",
			signature: validSig, // sig was for original body
			want:      false,
		},
		{
			name:      "tampered signature",
			timestamp: ts,
			body:      body,
			signature: "v0=" + strings.Repeat("0", 64),
			want:      false,
		},
		{
			name:      "expired timestamp (>5 min ago)",
			timestamp: shiftedTS(-301),
			body:      body,
			signature: computeSlackSig(secret, shiftedTS(-301), body),
			want:      false,
		},
		{
			name:      "future timestamp beyond window (>5 min ahead)",
			timestamp: shiftedTS(+301),
			body:      body,
			signature: computeSlackSig(secret, shiftedTS(+301), body),
			want:      false,
		},
		{
			name:      "non-numeric timestamp",
			timestamp: "not-a-timestamp",
			body:      body,
			signature: "v0=anything",
			want:      false,
		},
		{
			name:      "timestamp within window (4m59s ago)",
			timestamp: shiftedTS(-299),
			body:      body,
			signature: computeSlackSig(secret, shiftedTS(-299), body),
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &InteractionHandler{signingSecret: secret}
			got := h.verifySignature(tt.timestamp, tt.body, tt.signature)
			if got != tt.want {
				t.Errorf("verifySignature() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- ServeHTTP helpers ---

// buildSlackRequest constructs a signed POST request with a form-encoded payload.
func buildSlackRequest(t *testing.T, signingSecret string, payloadJSON []byte) *http.Request {
	t.Helper()
	form := url.Values{}
	form.Set("payload", string(payloadJSON))
	rawBody := form.Encode()

	ts := nowTS()
	sig := computeSlackSig(signingSecret, ts, rawBody)

	req := httptest.NewRequest(http.MethodPost, "/slack/interaction", strings.NewReader(rawBody))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", sig)
	return req
}

// blockActionsPayload returns a minimal block_actions JSON payload.
func blockActionsPayload(actionID, value string) []byte {
	p := InteractionPayload{
		Type:        "block_actions",
		ResponseURL: "https://hooks.slack.test/actions/FAKE",
		User:        &InteractionUser{ID: "U001", Username: "tester"},
		Channel:     &InteractionChannel{ID: "C001"},
		Message:     &InteractionMessage{TS: "1234567890.000001"},
		Actions:     []InteractionActionDef{{ActionID: actionID, Value: value}},
	}
	b, err := json.Marshal(p)
	if err != nil {
		panic(fmt.Sprintf("blockActionsPayload: %v", err))
	}
	return b
}

// --- ServeHTTP tests ---

func TestServeHTTP_ValidSignedRequest(t *testing.T) {
	h := NewInteractionHandler(testutil.FakeWebhookSecret)

	var gotAction *InteractionAction
	h.OnAction(func(a *InteractionAction) bool {
		gotAction = a
		return true
	})

	req := buildSlackRequest(t, testutil.FakeWebhookSecret, blockActionsPayload("approve", "pr-42"))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if gotAction == nil {
		t.Fatal("onAction was not called")
	}
	if gotAction.ActionID != "approve" {
		t.Errorf("ActionID = %q, want approve", gotAction.ActionID)
	}
	if gotAction.Value != "pr-42" {
		t.Errorf("Value = %q, want pr-42", gotAction.Value)
	}
}

func TestServeHTTP_ForgedSignature(t *testing.T) {
	h := NewInteractionHandler(testutil.FakeWebhookSecret)

	called := false
	h.OnAction(func(a *InteractionAction) bool {
		called = true
		return true
	})

	form := url.Values{}
	form.Set("payload", string(blockActionsPayload("approve", "pr-42")))
	rawBody := form.Encode()

	req := httptest.NewRequest(http.MethodPost, "/slack/interaction", strings.NewReader(rawBody))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", nowTS())
	req.Header.Set("X-Slack-Signature", "v0="+strings.Repeat("a", 64))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if called {
		t.Error("onAction must not be called on a forged request")
	}
}

func TestServeHTTP_DevModeBypass(t *testing.T) {
	// Empty signing secret: ServeHTTP skips signature verification entirely.
	h := NewInteractionHandler("")

	called := false
	h.OnAction(func(a *InteractionAction) bool {
		called = true
		return true
	})

	form := url.Values{}
	form.Set("payload", string(blockActionsPayload("reject", "pr-7")))

	req := httptest.NewRequest(http.MethodPost, "/slack/interaction", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// No signature headers — should pass through with empty signingSecret.

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !called {
		t.Error("onAction should be called in dev-mode bypass")
	}
}

func TestServeHTTP_MethodNotAllowed(t *testing.T) {
	h := NewInteractionHandler(testutil.FakeWebhookSecret)

	req := httptest.NewRequest(http.MethodGet, "/slack/interaction", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestServeHTTP_NonBlockActionsIgnored(t *testing.T) {
	h := NewInteractionHandler("")

	called := false
	h.OnAction(func(a *InteractionAction) bool {
		called = true
		return true
	})

	p := InteractionPayload{Type: "shortcut"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	form := url.Values{}
	form.Set("payload", string(b))

	req := httptest.NewRequest(http.MethodPost, "/slack/interaction", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if called {
		t.Error("onAction must not be called for non-block_actions interaction type")
	}
}

func TestServeHTTP_ActionFieldMapping(t *testing.T) {
	// Verifies InteractionAction is populated correctly, including the
	// Username→Name fallback when Username is empty.
	h := NewInteractionHandler("")

	var gotAction *InteractionAction
	h.OnAction(func(a *InteractionAction) bool {
		gotAction = a
		return true
	})

	p := InteractionPayload{
		Type:        "block_actions",
		ResponseURL: "https://hooks.slack.test/actions/XYZ",
		User:        &InteractionUser{ID: "UABC", Username: "", Name: "Alice"},
		Channel:     &InteractionChannel{ID: "CABC"},
		Message:     &InteractionMessage{TS: "9999.111"},
		Actions:     []InteractionActionDef{{ActionID: "merge", Value: "pr-99"}},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	form := url.Values{}
	form.Set("payload", string(b))

	req := httptest.NewRequest(http.MethodPost, "/slack/interaction", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if gotAction == nil {
		t.Fatal("onAction was not called")
	}
	if gotAction.ActionID != "merge" {
		t.Errorf("ActionID = %q, want merge", gotAction.ActionID)
	}
	if gotAction.Value != "pr-99" {
		t.Errorf("Value = %q, want pr-99", gotAction.Value)
	}
	if gotAction.UserID != "UABC" {
		t.Errorf("UserID = %q, want UABC", gotAction.UserID)
	}
	if gotAction.Username != "Alice" {
		t.Errorf("Username = %q, want Alice (fallback to Name)", gotAction.Username)
	}
	if gotAction.ChannelID != "CABC" {
		t.Errorf("ChannelID = %q, want CABC", gotAction.ChannelID)
	}
	if gotAction.MessageTS != "9999.111" {
		t.Errorf("MessageTS = %q, want 9999.111", gotAction.MessageTS)
	}
	if gotAction.ResponseURL != "https://hooks.slack.test/actions/XYZ" {
		t.Errorf("ResponseURL = %q, want https://hooks.slack.test/actions/XYZ", gotAction.ResponseURL)
	}
}

func TestServeHTTP_NoActionCallback(t *testing.T) {
	// onAction == nil should not panic; handler still returns 200.
	h := NewInteractionHandler("")
	// No OnAction registered.

	form := url.Values{}
	form.Set("payload", string(blockActionsPayload("approve", "pr-1")))

	req := httptest.NewRequest(http.MethodPost, "/slack/interaction", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}
