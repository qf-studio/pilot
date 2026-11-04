package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
)

// mockBillingOverrideSource implements BillingOverrideSource for tests.
type mockBillingOverrideSource struct {
	mu         sync.Mutex
	status     executor.BillingOverrideStatus
	setCalls   []string
	clears     []string
	lastUntil  time.Time
	setErr     error
	configMode string
}

func newMockBillingOverrideSource() *mockBillingOverrideSource {
	return &mockBillingOverrideSource{
		configMode: "subscription",
		status:     executor.BillingOverrideStatus{Mode: "subscription", Source: "config", ConfigMode: "subscription"},
	}
}

func (m *mockBillingOverrideSource) BillingOverride() executor.BillingOverrideStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *mockBillingOverrideSource) SetBillingOverride(mode string, until time.Time, setBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.setErr != nil {
		return m.setErr
	}
	m.setCalls = append(m.setCalls, mode+"/"+setBy)
	m.lastUntil = until
	m.status = executor.BillingOverrideStatus{Mode: mode, Source: setBy, Until: until, ConfigMode: m.configMode}
	return nil
}

func (m *mockBillingOverrideSource) ClearBillingOverride(setBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clears = append(m.clears, setBy)
	m.status = executor.BillingOverrideStatus{Mode: m.configMode, Source: "config", ConfigMode: m.configMode}
	return nil
}

const billingModeTestToken = "billing-mode-secret"

// startBillingModeServer starts an authed gateway and returns its base URL.
func startBillingModeServer(t *testing.T, src BillingOverrideSource) string {
	t.Helper()
	port := freePort(t)
	server := NewServerWithAuth(&Config{Host: "127.0.0.1", Port: port}, &AuthConfig{Type: AuthTypeAPIToken, Token: billingModeTestToken})
	if src != nil {
		server.SetBillingOverrideSource(src)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	go func() { _ = server.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func billingModeDo(t *testing.T, method, url, body string, auth bool) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+billingModeTestToken)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(buf)
}

func decodeBillingMode(t *testing.T, body string) billingModeResponse {
	t.Helper()
	var r billingModeResponse
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return r
}

func TestGateway_BillingEndpoint(t *testing.T) {
	src := newMockBillingOverrideSource()
	base := startBillingModeServer(t, src)
	url := base + "/api/v1/billing-mode"

	// Same auth as the other apiMux routes.
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if code, _ := billingModeDo(t, method, url, `{"mode":"api_key"}`, false); code != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d, want 401", method, code)
		}
	}
	if len(src.setCalls) != 0 {
		t.Fatalf("an unauthenticated POST reached the source: %v", src.setCalls)
	}

	// GET with no override: the config decides.
	code, body := billingModeDo(t, http.MethodGet, url, "", true)
	if code != http.StatusOK {
		t.Fatalf("GET = %d %s", code, body)
	}
	if got := decodeBillingMode(t, body); got != (billingModeResponse{Mode: "subscription", Source: "config", Until: "", ConfigMode: "subscription"}) {
		t.Errorf("GET = %+v", got)
	}

	// POST set with an until.
	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	code, body = billingModeDo(t, http.MethodPost, url, fmt.Sprintf(`{"mode":"api_key","until":%q}`, until.Format(time.RFC3339)), true)
	if code != http.StatusOK {
		t.Fatalf("POST set = %d %s", code, body)
	}
	if got := decodeBillingMode(t, body); got.Mode != "api_key" || got.Source != "gateway" || got.Until != until.Format(time.RFC3339) || got.ConfigMode != "subscription" {
		t.Errorf("POST set = %+v", got)
	}
	if len(src.setCalls) != 1 || src.setCalls[0] != "api_key/gateway" || !src.lastUntil.Equal(until) {
		t.Errorf("source saw %v until %v, want api_key/gateway until %v", src.setCalls, src.lastUntil, until)
	}
	if code, body = billingModeDo(t, http.MethodGet, url, "", true); code != http.StatusOK || decodeBillingMode(t, body).Mode != "api_key" {
		t.Errorf("GET after set = %d %s", code, body)
	}

	// POST set without an until.
	if code, body = billingModeDo(t, http.MethodPost, url, `{"mode":"subscription"}`, true); code != http.StatusOK {
		t.Fatalf("POST set (no until) = %d %s", code, body)
	}
	if got := decodeBillingMode(t, body); got.Mode != "subscription" || got.Until != "" || !src.lastUntil.IsZero() {
		t.Errorf("POST set (no until) = %+v, source until %v", got, src.lastUntil)
	}

	// POST config clears.
	code, body = billingModeDo(t, http.MethodPost, url, `{"mode":"config"}`, true)
	if code != http.StatusOK {
		t.Fatalf("POST config = %d %s", code, body)
	}
	if got := decodeBillingMode(t, body); got.Source != "config" || got.Until != "" {
		t.Errorf("POST config = %+v", got)
	}
	if len(src.clears) != 1 || src.clears[0] != "gateway" {
		t.Errorf("clears = %v, want [gateway]", src.clears)
	}

	// Bad requests: 400, and nothing reaches the source.
	sets, clears := len(src.setCalls), len(src.clears)
	for name, reqBody := range map[string]string{
		"unknown mode":    `{"mode":"unset"}`,
		"empty mode":      `{}`,
		"wrong case":      `{"mode":"API_KEY"}`,
		"bad until":       `{"mode":"api_key","until":"tomorrow"}`,
		"until on config": `{"mode":"config","until":"2030-01-01T00:00:00Z"}`,
		"not json":        `mode=api_key`,
	} {
		code, body := billingModeDo(t, http.MethodPost, url, reqBody, true)
		if code != http.StatusBadRequest {
			t.Errorf("%s: POST = %d %s, want 400", name, code, body)
		}
		if name == "unknown mode" && !bytes.Contains([]byte(body), []byte("subscription, api_key, config")) {
			t.Errorf("400 body %q does not list the accepted values", body)
		}
	}
	if len(src.setCalls) != sets || len(src.clears) != clears {
		t.Errorf("a rejected POST reached the source: sets %v clears %v", src.setCalls, src.clears)
	}

	// A source that refuses the request as invalid is a 400 too; any other error a 500.
	src.setErr = fmt.Errorf("%w: until is not in the future", executor.ErrBillingOverrideInvalid)
	if code, body := billingModeDo(t, http.MethodPost, url, `{"mode":"api_key"}`, true); code != http.StatusBadRequest {
		t.Errorf("invalid-request error = %d %s, want 400", code, body)
	}
	src.setErr = errors.New("disk full")
	if code, body := billingModeDo(t, http.MethodPost, url, `{"mode":"api_key"}`, true); code != http.StatusInternalServerError {
		t.Errorf("store error = %d %s, want 500", code, body)
	}

	// Other methods.
	if code, _ := billingModeDo(t, http.MethodDelete, url, "", true); code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE = %d, want 405", code)
	}
}

func TestGateway_BillingEndpointWithoutSource(t *testing.T) {
	base := startBillingModeServer(t, nil)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if code, body := billingModeDo(t, method, base+"/api/v1/billing-mode", `{"mode":"api_key"}`, true); code != http.StatusServiceUnavailable {
			t.Errorf("%s with no source = %d %s, want 503", method, code, body)
		}
	}
}

func TestStatus_CarriesBillingFields(t *testing.T) {
	server := NewServer(&Config{Host: "127.0.0.1", Port: 9090})
	get := func(t *testing.T) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		server.handleStatus(w, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
		var r map[string]any
		if err := json.NewDecoder(w.Body).Decode(&r); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return r
	}

	// No source wired: the existing fields stay, the billing ones are absent.
	r := get(t)
	if _, ok := r["billing_mode"]; ok {
		t.Errorf("billing_mode present without a source: %v", r)
	}
	if _, ok := r["admission_paused"]; !ok {
		t.Errorf("existing field admission_paused missing: %v", r)
	}

	src := newMockBillingOverrideSource()
	server.SetBillingOverrideSource(src)
	r = get(t)
	for k, want := range map[string]string{
		"billing_mode":        "subscription",
		"billing_mode_source": "config",
		"billing_mode_until":  "",
		"billing_config_mode": "subscription",
	} {
		if got, ok := r[k]; !ok || got != want {
			t.Errorf("%s = %v (present=%v), want %q", k, got, ok, want)
		}
	}

	until := time.Date(2026, 11, 5, 9, 30, 0, 0, time.UTC)
	src.status = executor.BillingOverrideStatus{Mode: "api_key", Source: "cli", Until: until, ConfigMode: "subscription"}
	r = get(t)
	for k, want := range map[string]string{
		"billing_mode":        "api_key",
		"billing_mode_source": "cli",
		"billing_mode_until":  "2026-11-05T09:30:00Z",
		"billing_config_mode": "subscription",
	} {
		if got := r[k]; got != want {
			t.Errorf("%s = %v, want %q", k, got, want)
		}
	}
}
