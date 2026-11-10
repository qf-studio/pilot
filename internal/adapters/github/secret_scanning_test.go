package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestListSecretScanningAlertsCapped_ReportsCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(secretScanningPage(1, 100)))
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-github-token", srv.URL)
	got, capped, err := c.ListSecretScanningAlertsCapped(context.Background(), "o", "r", "open")
	if err != nil {
		t.Fatalf("ListSecretScanningAlertsCapped: %v", err)
	}
	if !capped || len(got) != maxSecretScanningPages*100 {
		t.Errorf("capped=%v len=%d, want capped with %d alerts", capped, len(got), maxSecretScanningPages*100)
	}
}

// TestListSecretScanningAlertsCapped_CapOnlyWhenMoreRemain: a repo with exactly
// the capped number of alerts is complete; only an alert on the page past the cap
// makes the list partial (GH-6279).
func TestListSecretScanningAlertsCapped_CapOnlyWhenMoreRemain(t *testing.T) {
	tests := []struct {
		name         string
		fullPages    int // pages of 100 alerts
		extra        int // alerts on the page after the last full one
		wantCapped   bool
		wantAlerts   int
		wantRequests int
	}{
		{"short list", 2, 5, false, 205, 3},
		{"exactly the cap", maxSecretScanningPages, 0, false, maxSecretScanningPages * 100, maxSecretScanningPages + 1},
		{"one past the cap", maxSecretScanningPages, 1, true, maxSecretScanningPages * 100, maxSecretScanningPages + 1},
		{"far past the cap", maxSecretScanningPages + 5, 0, true, maxSecretScanningPages * 100, maxSecretScanningPages + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests++
				mu.Unlock()
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				switch {
				case page <= tt.fullPages:
					_, _ = w.Write([]byte(secretScanningPage((page-1)*100+1, 100)))
				case page == tt.fullPages+1:
					_, _ = w.Write([]byte(secretScanningPage(page*100, tt.extra)))
				default:
					_, _ = w.Write([]byte("[]"))
				}
			}))
			defer srv.Close()

			c := NewClientWithBaseURL("test-github-token", srv.URL)
			got, capped, err := c.ListSecretScanningAlertsCapped(context.Background(), "o", "r", "open")
			if err != nil {
				t.Fatalf("ListSecretScanningAlertsCapped: %v", err)
			}
			if capped != tt.wantCapped || len(got) != tt.wantAlerts || requests != tt.wantRequests {
				t.Errorf("capped=%v alerts=%d requests=%d, want capped=%v alerts=%d requests=%d",
					capped, len(got), requests, tt.wantCapped, tt.wantAlerts, tt.wantRequests)
			}
		})
	}
}
