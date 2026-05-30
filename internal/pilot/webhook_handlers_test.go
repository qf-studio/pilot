package pilot

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/asana"
	"github.com/qf-studio/pilot/internal/adapters/jira"
	"github.com/qf-studio/pilot/internal/adapters/plane"
	"github.com/qf-studio/pilot/internal/gateway"
	"github.com/qf-studio/pilot/internal/testutil"
)

// computeTestHMAC produces a valid HMAC-SHA256 for handler gating tests.
func computeTestHMAC(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// TestJiraWebhookSignatureGating asserts that Handle is gated on a body-HMAC
// computed over the exact raw request body the gateway buffered (TASK-333),
// mirroring pilot.go's registered jira handler.
func TestJiraWebhookSignatureGating(t *testing.T) {
	router := gateway.NewRouter()
	wh := jira.NewWebhookHandler(nil, testutil.FakeWebhookSecret, "pilot")

	handleCalled := false
	wh.OnIssue(func(_ context.Context, _ *jira.Issue) error {
		handleCalled = true
		return nil
	})

	ctx := context.Background()
	// Mirror pilot.go: verify over payload["_raw_body"], not a re-marshaled map.
	router.RegisterWebhookHandler("jira", func(payload map[string]interface{}) {
		signature, _ := payload["_signature"].(string)
		rawBody, _ := payload["_raw_body"].(string)
		if !wh.VerifySignature([]byte(rawBody), signature) {
			return
		}
		_ = wh.Handle(ctx, payload)
	})

	// Non-canonical body (whitespace + key order json.Marshal would not
	// reproduce). The pre-TASK-333 re-marshal path would reject a valid HMAC
	// over these bytes; raw-body verification accepts it.
	rawBody := `{ "webhookEvent":  "jira:issue_updated", "issue": {"key":"PROJ-1"} }`
	validSig := computeTestHMAC(testutil.FakeWebhookSecret, []byte(rawBody))

	t.Run("bad signature blocks Handle", func(t *testing.T) {
		handleCalled = false
		router.HandleWebhook("jira", map[string]interface{}{
			"_signature": "bad-signature",
			"_raw_body":  rawBody,
		})
		if handleCalled {
			t.Error("Handle must not be called when signature is invalid")
		}
	})

	t.Run("tampered body blocks Handle", func(t *testing.T) {
		handleCalled = false
		router.HandleWebhook("jira", map[string]interface{}{
			"_signature": validSig,
			"_raw_body":  rawBody + " ", // mutated after signing
		})
		if handleCalled {
			t.Error("Handle must not be called when the body was tampered after signing")
		}
	})

	t.Run("valid body-HMAC over raw bytes passes the gate", func(t *testing.T) {
		// The whole point of TASK-333: an HMAC over the exact raw bytes passes.
		if !wh.VerifySignature([]byte(rawBody), validSig) {
			t.Fatal("valid HMAC over the raw body must pass — TASK-333 regression")
		}
		// Decode the buffered bytes into the routed map exactly as server.go does,
		// then route. issue_updated with no changelog returns before any client
		// call, so this asserts the gate passed without panicking.
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(rawBody), &payload); err != nil {
			t.Fatalf("raw body must decode: %v", err)
		}
		payload["_signature"] = validSig
		payload["_raw_body"] = rawBody
		router.HandleWebhook("jira", payload)
	})
}

// TestAsanaWebhookSignatureGating asserts that Handle is NOT reached when the
// signature is invalid — mirroring pilot.go's registered asana handler.
func TestAsanaWebhookSignatureGating(t *testing.T) {
	router := gateway.NewRouter()
	wh := asana.NewWebhookHandler(nil, testutil.FakeAsanaWebhookSecret, "pilot")

	handleCalled := false
	wh.OnTask(func(_ context.Context, _ *asana.Task) error {
		handleCalled = true
		return nil
	})

	ctx := context.Background()
	// Mirror pilot.go: verify over payload["_raw_body"], not a re-marshaled map.
	router.RegisterWebhookHandler("asana", func(payload map[string]interface{}) {
		signature, _ := payload["_signature"].(string)
		rawBody, _ := payload["_raw_body"].(string)
		if !wh.VerifySignature([]byte(rawBody), signature) {
			return
		}
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			return
		}
		var wp asana.WebhookPayload
		if err := json.Unmarshal(payloadBytes, &wp); err != nil {
			return
		}
		_ = wh.Handle(ctx, &wp)
	})

	// Non-canonical body whose re-marshaling would differ from the raw bytes.
	rawBody := `{ "events": [] }`
	validSig := computeTestHMAC(testutil.FakeAsanaWebhookSecret, []byte(rawBody))

	t.Run("bad signature blocks Handle", func(t *testing.T) {
		handleCalled = false
		router.HandleWebhook("asana", map[string]interface{}{
			"_signature": "bad-signature",
			"_raw_body":  rawBody,
		})
		if handleCalled {
			t.Error("Handle must not be called when signature is invalid")
		}
	})

	t.Run("tampered body blocks Handle", func(t *testing.T) {
		handleCalled = false
		router.HandleWebhook("asana", map[string]interface{}{
			"_signature": validSig,
			"_raw_body":  rawBody + " ", // mutated after signing
		})
		if handleCalled {
			t.Error("Handle must not be called when the body was tampered after signing")
		}
	})

	t.Run("valid body-HMAC over raw bytes passes the gate", func(t *testing.T) {
		if !wh.VerifySignature([]byte(rawBody), validSig) {
			t.Fatal("valid HMAC over the raw body must pass — TASK-333 regression")
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(rawBody), &payload); err != nil {
			t.Fatalf("raw body must decode: %v", err)
		}
		payload["_signature"] = validSig
		payload["_raw_body"] = rawBody
		router.HandleWebhook("asana", payload) // gate passed; empty events → no callback
	})
}

func TestAsanaWebhookHandlerRegistration(t *testing.T) {
	router := gateway.NewRouter()
	wh := asana.NewWebhookHandler(nil, "", "pilot")

	var handlerCalled bool
	wh.OnTask(func(_ context.Context, task *asana.Task) error {
		handlerCalled = true
		return nil
	})

	ctx := context.Background()
	router.RegisterWebhookHandler("asana", func(payload map[string]interface{}) {
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("Failed to marshal payload: %v", err)
		}

		var webhookPayload asana.WebhookPayload
		if err := json.Unmarshal(payloadBytes, &webhookPayload); err != nil {
			t.Fatalf("Failed to unmarshal payload: %v", err)
		}

		if err := wh.Handle(ctx, &webhookPayload); err != nil {
			t.Fatalf("Handle error: %v", err)
		}
	})

	// Verify handler is registered
	router.HandleWebhook("asana", map[string]interface{}{
		"events": []interface{}{},
	})

	// With empty events, the task callback won't fire, but the handler ran without error
	if handlerCalled {
		t.Error("Expected handler not to be called with empty events")
	}
}

func TestPlaneWebhookHandlerRegistration(t *testing.T) {
	router := gateway.NewRouter()
	wh := plane.NewWebhookHandler("", "pilot", nil)

	var handlerCalled bool
	wh.OnWorkItem(func(_ context.Context, _ *plane.WebhookWorkItemData) error {
		handlerCalled = true
		return nil
	})

	ctx := context.Background()
	router.RegisterWebhookHandler("plane", func(payload map[string]interface{}) {
		rawBody, _ := payload["_raw_body"].(string)
		signature, _ := payload["_signature"].(string)

		if err := wh.Handle(ctx, []byte(rawBody), signature); err != nil {
			t.Fatalf("Handle error: %v", err)
		}
	})

	// Simulate gateway-style payload with raw body containing a non-issue event
	rawPayload := `{"event":"module","action":"created","data":{}}`
	router.HandleWebhook("plane", map[string]interface{}{
		"_raw_body":  rawPayload,
		"_signature": "",
	})

	if handlerCalled {
		t.Error("Expected handler not to be called for non-issue event")
	}
}

func TestPlaneWebhookHandlerWithIssueEvent(t *testing.T) {
	router := gateway.NewRouter()
	wh := plane.NewWebhookHandler("", "pilot-label", nil)

	var receivedItem *plane.WebhookWorkItemData
	wh.OnWorkItem(func(_ context.Context, item *plane.WebhookWorkItemData) error {
		receivedItem = item
		return nil
	})

	ctx := context.Background()
	router.RegisterWebhookHandler("plane", func(payload map[string]interface{}) {
		rawBody, _ := payload["_raw_body"].(string)
		signature, _ := payload["_signature"].(string)

		if err := wh.Handle(ctx, []byte(rawBody), signature); err != nil {
			t.Fatalf("Handle error: %v", err)
		}
	})

	rawPayload := `{"event":"issue","action":"created","data":{"id":"item-1","name":"Test Issue","sequence_id":42,"labels":["pilot-label"]}}`
	router.HandleWebhook("plane", map[string]interface{}{
		"_raw_body":  rawPayload,
		"_signature": "",
	})

	if receivedItem == nil {
		t.Fatal("Expected work item callback to be called")
	}
	if receivedItem.ID != "item-1" {
		t.Errorf("Expected ID 'item-1', got %q", receivedItem.ID)
	}
	if receivedItem.Name != "Test Issue" {
		t.Errorf("Expected Name 'Test Issue', got %q", receivedItem.Name)
	}
}
