package webhooks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestManager_Dispatch_SingleEndpoint(t *testing.T) {
	// Create test server
	received := make(chan *Event, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event Event
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Errorf("failed to decode event: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- &event
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create manager with endpoint
	config := &Config{
		Enabled: true,
		Endpoints: []*EndpointConfig{
			{
				ID:      "ep_test",
				Name:    "Test Endpoint",
				URL:     server.URL,
				Secret:  testutil.FakeWebhookSecret,
				Events:  []EventType{EventTaskCompleted},
				Enabled: true,
			},
		},
	}

	manager := NewManager(config, nil)

	// Dispatch event
	event := NewEvent(EventTaskCompleted, &TaskCompletedData{
		TaskID:  "task-123",
		Title:   "Test Task",
		Project: "test-project",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results := manager.Dispatch(ctx, event)

	// Check results
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if !results[0].Success {
		t.Errorf("expected success, got error: %v", results[0].Error)
	}

	if results[0].StatusCode != 200 {
		t.Errorf("expected status 200, got %d", results[0].StatusCode)
	}

	// Verify event was received
	select {
	case evt := <-received:
		if evt.Type != EventTaskCompleted {
			t.Errorf("expected event type %s, got %s", EventTaskCompleted, evt.Type)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for event")
	}
}

func TestManager_Dispatch_FiltersByEventType(t *testing.T) {
	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &Config{
		Enabled: true,
		Endpoints: []*EndpointConfig{
			{
				ID:      "ep_test",
				Name:    "Test Endpoint",
				URL:     server.URL,
				Events:  []EventType{EventTaskCompleted, EventTaskFailed}, // Only these events
				Enabled: true,
			},
		},
	}

	manager := NewManager(config, nil)
	ctx := context.Background()

	// Send task.started (not subscribed)
	manager.Dispatch(ctx, NewEvent(EventTaskStarted, nil))

	// Send task.completed (subscribed)
	manager.Dispatch(ctx, NewEvent(EventTaskCompleted, nil))

	// Small delay for async processing
	time.Sleep(100 * time.Millisecond)

	if atomic.LoadInt32(&callCount) != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
}

func TestManager_Dispatch_RetryOnFailure(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &Config{
		Enabled: true,
		Endpoints: []*EndpointConfig{
			{
				ID:      "ep_test",
				Name:    "Test Endpoint",
				URL:     server.URL,
				Enabled: true,
				Retry: &RetryConfig{
					MaxAttempts:  3,
					InitialDelay: 10 * time.Millisecond,
					MaxDelay:     100 * time.Millisecond,
					Multiplier:   2.0,
				},
			},
		},
	}

	manager := NewManager(config, nil)
	ctx := context.Background()

	results := manager.Dispatch(ctx, NewEvent(EventTaskCompleted, nil))

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	if !results[0].Success {
		t.Errorf("expected success after retries, got error: %v", results[0].Error)
	}

	if results[0].Attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", results[0].Attempts)
	}
}

func TestManager_Dispatch_DisabledWebhooks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("webhook should not be called when disabled")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &Config{
		Enabled: false, // Disabled
		Endpoints: []*EndpointConfig{
			{
				ID:      "ep_test",
				URL:     server.URL,
				Enabled: true,
			},
		},
	}

	manager := NewManager(config, nil)
	ctx := context.Background()

	results := manager.Dispatch(ctx, NewEvent(EventTaskCompleted, nil))

	if results != nil {
		t.Error("expected nil results when webhooks disabled")
	}
}

func TestManager_Sign(t *testing.T) {
	manager := NewManager(nil, nil)

	payload := []byte(`{"type":"task.completed"}`)

	// With secret
	sig := manager.sign(payload, testutil.FakeWebhookSecret)
	if sig == "" {
		t.Error("expected signature, got empty string")
	}
	if len(sig) < 10 || sig[:7] != "sha256=" {
		t.Errorf("invalid signature format: %s", sig)
	}

	// Without secret
	sig = manager.sign(payload, "")
	if sig != "" {
		t.Error("expected empty signature without secret")
	}
}

func TestVerifySignature(t *testing.T) {
	manager := NewManager(nil, nil)
	secret := testutil.FakeWebhookSecret
	payload := []byte(`{"type":"task.completed"}`)

	// Generate signature
	sig := manager.sign(payload, secret)

	// Valid signature
	if !VerifySignature(payload, sig, secret) {
		t.Error("expected valid signature verification")
	}

	// Invalid signature
	if VerifySignature(payload, "sha256=invalid", secret) {
		t.Error("expected invalid signature verification")
	}

	// Modified payload
	if VerifySignature([]byte(`{"type":"task.failed"}`), sig, secret) {
		t.Error("expected verification to fail for modified payload")
	}

	// Empty secret
	if VerifySignature(payload, sig, "") {
		t.Error("expected verification to fail with empty secret")
	}
}

func TestManager_AddRemoveEndpoint(t *testing.T) {
	manager := NewManager(nil, nil)

	// Add endpoint
	ep := &EndpointConfig{
		Name:    "Test",
		URL:     "https://example.com/hook",
		Enabled: true,
	}
	manager.AddEndpoint(ep)

	// Verify ID was generated
	if ep.ID == "" {
		t.Error("expected ID to be generated")
	}

	// Get endpoint
	retrieved := manager.GetEndpoint(ep.ID)
	if retrieved == nil {
		t.Fatal("expected to find endpoint")
	}
	if retrieved.Name != "Test" {
		t.Errorf("expected name 'Test', got '%s'", retrieved.Name)
	}

	// List endpoints
	list := manager.ListEndpoints()
	if len(list) != 1 {
		t.Errorf("expected 1 endpoint, got %d", len(list))
	}

	// Remove endpoint
	if !manager.RemoveEndpoint(ep.ID) {
		t.Error("expected removal to succeed")
	}

	// Verify removed
	if manager.GetEndpoint(ep.ID) != nil {
		t.Error("endpoint should be removed")
	}

	// Remove non-existent
	if manager.RemoveEndpoint("non-existent") {
		t.Error("expected removal of non-existent to return false")
	}
}

func TestManager_Stats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &Config{
		Enabled: true,
		Endpoints: []*EndpointConfig{
			{
				ID:      "ep_test",
				URL:     server.URL,
				Enabled: true,
			},
		},
	}

	manager := NewManager(config, nil)
	ctx := context.Background()

	// Initial stats
	deliveries, failures, retries, lastDelivery := manager.Stats()
	if deliveries != 0 || failures != 0 || retries != 0 || !lastDelivery.IsZero() {
		t.Error("expected zero initial stats")
	}

	// Dispatch successful event
	manager.Dispatch(ctx, NewEvent(EventTaskCompleted, nil))

	deliveries, _, _, lastDelivery = manager.Stats()
	if deliveries != 1 {
		t.Errorf("expected 1 delivery, got %d", deliveries)
	}
	if lastDelivery.IsZero() {
		t.Error("expected lastDelivery to be set")
	}
}

func TestEndpointConfig_SubscribesTo(t *testing.T) {
	tests := []struct {
		name      string
		events    []EventType
		checkType EventType
		expected  bool
	}{
		{
			name:      "empty events means all",
			events:    []EventType{},
			checkType: EventTaskCompleted,
			expected:  true,
		},
		{
			name:      "subscribed event",
			events:    []EventType{EventTaskCompleted, EventTaskFailed},
			checkType: EventTaskCompleted,
			expected:  true,
		},
		{
			name:      "unsubscribed event",
			events:    []EventType{EventTaskCompleted},
			checkType: EventTaskFailed,
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep := &EndpointConfig{Events: tt.events}
			if got := ep.SubscribesTo(tt.checkType); got != tt.expected {
				t.Errorf("SubscribesTo() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestEndpointConfig_GetTimeout(t *testing.T) {
	// With endpoint timeout
	ep := &EndpointConfig{Timeout: 5 * time.Second}
	if got := ep.GetTimeout(nil); got != 5*time.Second {
		t.Errorf("expected 5s, got %v", got)
	}

	// With defaults
	ep = &EndpointConfig{}
	defaults := &EndpointDefaults{Timeout: 10 * time.Second}
	if got := ep.GetTimeout(defaults); got != 10*time.Second {
		t.Errorf("expected 10s, got %v", got)
	}

	// Fallback to default
	ep = &EndpointConfig{}
	if got := ep.GetTimeout(nil); got != 30*time.Second {
		t.Errorf("expected 30s default, got %v", got)
	}
}

func TestNewEvent(t *testing.T) {
	data := &TaskCompletedData{
		TaskID: "task-123",
		Title:  "Test",
	}

	event := NewEvent(EventTaskCompleted, data)

	if event.ID == "" {
		t.Error("expected event ID to be generated")
	}
	if !startsWith(event.ID, "evt_") {
		t.Errorf("expected event ID to start with 'evt_', got '%s'", event.ID)
	}
	if event.Type != EventTaskCompleted {
		t.Errorf("expected type %s, got %s", EventTaskCompleted, event.Type)
	}
	if event.Timestamp.IsZero() {
		t.Error("expected timestamp to be set")
	}
	if event.Data == nil {
		t.Error("expected data to be set")
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestManager_Dispatch_WithSignature(t *testing.T) {
	secret := "my-webhook-secret"
	var receivedSig string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-Pilot-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &Config{
		Enabled: true,
		Endpoints: []*EndpointConfig{
			{
				ID:      "ep_test",
				URL:     server.URL,
				Secret:  secret,
				Enabled: true,
			},
		},
	}

	manager := NewManager(config, nil)
	ctx := context.Background()

	manager.Dispatch(ctx, NewEvent(EventTaskCompleted, nil))

	if receivedSig == "" {
		t.Error("expected X-Pilot-Signature header")
	}
	if !startsWith(receivedSig, "sha256=") {
		t.Errorf("expected signature to start with 'sha256=', got '%s'", receivedSig)
	}
}

func TestManager_Dispatch_HeadersSet(t *testing.T) {
	headers := make(http.Header)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range r.Header {
			headers[k] = v
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &Config{
		Enabled: true,
		Endpoints: []*EndpointConfig{
			{
				ID:      "ep_test",
				URL:     server.URL,
				Enabled: true,
				Headers: map[string]string{
					"X-Custom-Header": "custom-value",
				},
			},
		},
	}

	manager := NewManager(config, nil)
	ctx := context.Background()

	event := NewEvent(EventTaskCompleted, nil)
	manager.Dispatch(ctx, event)

	// Check standard headers
	if headers.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", headers.Get("Content-Type"))
	}
	if headers.Get("X-Pilot-Event") != string(EventTaskCompleted) {
		t.Errorf("expected X-Pilot-Event %s, got %s", EventTaskCompleted, headers.Get("X-Pilot-Event"))
	}
	if headers.Get("X-Pilot-Delivery") == "" {
		t.Error("expected X-Pilot-Delivery header")
	}
	if headers.Get("User-Agent") != "Pilot-Webhooks/1.0" {
		t.Errorf("expected User-Agent Pilot-Webhooks/1.0, got %s", headers.Get("User-Agent"))
	}

	// Check custom header
	if headers.Get("X-Custom-Header") != "custom-value" {
		t.Errorf("expected X-Custom-Header custom-value, got %s", headers.Get("X-Custom-Header"))
	}
}

func TestManager_Dispatch_TimeoutEvent(t *testing.T) {
	// Verify task.timeout events can be dispatched and filtered
	received := make(chan EventType, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event Event
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Errorf("failed to decode event: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- event.Type
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	config := &Config{
		Enabled: true,
		Endpoints: []*EndpointConfig{
			{
				ID:      "ep_timeout",
				Name:    "Timeout Endpoint",
				URL:     server.URL,
				Events:  []EventType{EventTaskTimeout}, // Subscribe only to timeout events
				Enabled: true,
			},
		},
	}

	manager := NewManager(config, nil)
	ctx := context.Background()

	// Dispatch timeout event
	event := NewEvent(EventTaskTimeout, &TaskTimeoutData{
		TaskID:     "task-123",
		Title:      "Test Task",
		Project:    "test-project",
		Duration:   5 * time.Minute,
		Timeout:    5 * time.Minute,
		Complexity: "medium",
		Phase:      "Implementing",
	})

	results := manager.Dispatch(ctx, event)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Success {
		t.Errorf("expected success, got error: %v", results[0].Error)
	}

	// Verify event was received
	select {
	case evtType := <-received:
		if evtType != EventTaskTimeout {
			t.Errorf("expected event type %s, got %s", EventTaskTimeout, evtType)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for event")
	}
}

func TestAllEventTypes_IncludesTimeout(t *testing.T) {
	allTypes := AllEventTypes()

	// Check that task.timeout is included
	found := false
	for _, et := range allTypes {
		if et == EventTaskTimeout {
			found = true
			break
		}
	}

	if !found {
		t.Error("AllEventTypes() should include EventTaskTimeout")
	}
}
