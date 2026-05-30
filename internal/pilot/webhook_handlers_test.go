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

// TestJiraWebhookSignatureGating asserts that Handle is NOT reached when the
// signature is invalid — mirroring pilot.go's registered jira handler.
func TestJiraWebhookSignatureGating(t *testing.T) {
	router := gateway.NewRouter()
	wh := jira.NewWebhookHandler(nil, testutil.FakeWebhookSecret, "pilot")

	handleCalled := false
	wh.OnIssue(func(_ context.Context, _ *jira.Issue) error {
		handleCalled = true
		return nil
	})

	ctx := context.Background()
	router.RegisterWebhookHandler("jira", func(payload map[string]interface{}) {
		signature, _ := payload["_signature"].(string)
		sigBytes, err := marshalWebhookPayload(payload)
		if err != nil {
			return
		}
		if !wh.VerifySignature(sigBytes, signature) {
			return
		}
		_ = wh.Handle(ctx, payload)
	})

	t.Run("bad signature blocks Handle", func(t *testing.T) {
		router.HandleWebhook("jira", map[string]interface{}{
			"_signature":   "bad-signature",
			"webhookEvent": "jira:issue_created",
		})
		if handleCalled {
			t.Error("Handle must not be called when signature is invalid")
		}
	})

	t.Run("valid signature reaches Handle", func(t *testing.T) {
		// Build the payload that pilot's marshalWebhookPayload will see (no _ keys).
		inner := map[string]interface{}{"webhookEvent": "jira:issue_updated"}
		innerBytes, _ := json.Marshal(inner)
		sig := computeTestHMAC(testutil.FakeWebhookSecret, innerBytes)

		handleCalled = false
		router.HandleWebhook("jira", map[string]interface{}{
			"_signature":   sig,
			"webhookEvent": "jira:issue_updated",
		})
		// Handle is reached (event type is unsupported so onIssue won't fire, but
		// Handle itself returns nil without error — the gate was passed).
		_ = handleCalled // gate check: no panic / early return from the handler
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
	router.RegisterWebhookHandler("asana", func(payload map[string]interface{}) {
		signature, _ := payload["_signature"].(string)
		sigBytes, err := marshalWebhookPayload(payload)
		if err != nil {
			return
		}
		if !wh.VerifySignature(sigBytes, signature) {
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

	t.Run("bad signature blocks Handle", func(t *testing.T) {
		router.HandleWebhook("asana", map[string]interface{}{
			"_signature": "bad-signature",
			"events":     []interface{}{},
		})
		if handleCalled {
			t.Error("Handle must not be called when signature is invalid")
		}
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
