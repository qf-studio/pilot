package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const fakeSecretValue = "fake-secret-value-not-real"

func secretScanningPage(start, n int) string {
	var items []string
	for i := 0; i < n; i++ {
		items = append(items, fmt.Sprintf(
			`{"number":%d,"state":"open","secret_type":"telegram_bot_token","secret_type_display_name":"Telegram Bot Token","secret":%q,"created_at":"2026-04-07T10:00:00Z","html_url":"https://github.com/o/r/security/secret-scanning/%d","push_protection_bypassed":true}`,
			start+i, fakeSecretValue, start+i))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func TestListSecretScanningAlerts_PaginatesAndDecodes(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(secretScanningPage(1, 100)))
		default:
			_, _ = w.Write([]byte(secretScanningPage(101, 2)))
		}
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-github-token", srv.URL)
	got, err := c.ListSecretScanningAlerts(context.Background(), "o", "r", "open")
	if err != nil {
		t.Fatalf("ListSecretScanningAlerts: %v", err)
	}
	if len(got) != 102 {
		t.Fatalf("got %d alerts, want 102", len(got))
	}
	if len(queries) != 2 || queries[0] != "/repos/o/r/secret-scanning/alerts?state=open&per_page=100&page=1" {
		t.Errorf("unexpected requests: %v", queries)
	}
	a := got[0]
	if a.Number != 1 || a.SecretType != "telegram_bot_token" || a.SecretTypeDisplayName != "Telegram Bot Token" ||
		a.HTMLURL != "https://github.com/o/r/security/secret-scanning/1" || !a.PushProtectionBypassed || a.CreatedAt.IsZero() {
		t.Errorf("decoded alert = %+v", a)
	}
	// The secret field is never decoded: it cannot survive a round trip.
	out, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), fakeSecretValue) {
		t.Errorf("secret value present in decoded alerts")
	}
}

func TestListSecretScanningAlerts_ForbiddenIsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-github-token", srv.URL)
	_, err := c.ListSecretScanningAlerts(context.Background(), "o", "r", "open")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("err = %v, want *APIError 403", err)
	}
}
