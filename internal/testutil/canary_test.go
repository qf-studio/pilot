package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// recorder is a testing.TB that records failures instead of failing the test.
type recorder struct {
	testing.TB
	errors []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func TestCanary_AssertNoCanaryLeakNamesTheLeakingText(t *testing.T) {
	rec := &recorder{TB: t}
	AssertNoCanaryLeak(rec, CanaryToken, "clean", "has "+CanaryToken, "also clean", CanaryToken)
	if len(rec.errors) != 2 {
		t.Fatalf("errors = %d, want 2: %v", len(rec.errors), rec.errors)
	}
	if !strings.HasPrefix(rec.errors[0], "text 1 ") || !strings.HasPrefix(rec.errors[1], "text 3 ") {
		t.Errorf("errors do not name the leaking indexes: %v", rec.errors)
	}

	clean := &recorder{TB: t}
	AssertNoCanaryLeak(clean, CanaryToken, "a", "b", "")
	AssertNoCanaryLeak(clean, CanaryToken)
	if len(clean.errors) != 0 {
		t.Errorf("clean texts failed: %v", clean.errors)
	}
}

func newCanaryRequest(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+CanaryToken)
	req.Header.Set("X-Other", "value")
	return req
}

// A FailingTransport that omits the headers makes every client test vacuous,
// so the helper itself must prove the error carries URL and headers.
func TestCanary_FailingTransportErrorHoldsURLAndAllHeaders(t *testing.T) {
	req := newCanaryRequest(t, "https://api.example.test/path/"+CanaryToken)
	resp, err := FailingTransport().RoundTrip(req)
	if err == nil {
		t.Fatal("expected an error")
	}
	if resp != nil {
		t.Errorf("response = %v, want nil", resp)
	}
	for _, want := range []string{
		"https://api.example.test/path/" + CanaryToken,
		"Authorization: Bearer " + CanaryToken,
		"X-Other: value",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

func TestCanary_EchoServerAnswersStatusAndEchoesHeadersAndBody(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		srv := EchoServer(t, status)
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			srv.URL+"/x", strings.NewReader("request-body-text"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+CanaryToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != status {
			t.Errorf("status = %d, want %d", resp.StatusCode, status)
		}
		for _, want := range []string{"POST /x", "Authorization: Bearer " + CanaryToken, "request-body-text"} {
			if !strings.Contains(string(body), want) {
				t.Errorf("echo lacks %q: %s", want, body)
			}
		}
	}
}

func TestCanary_EchoServerJSONFieldsCarryTheEcho(t *testing.T) {
	tests := []struct {
		name   string
		fields []string
	}{
		{"one field", []string{"error"}},
		{"two fields", []string{"error", "description"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := EchoServer(t, http.StatusUnauthorized, EchoJSONFields(tt.fields...))
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
				srv.URL+"/x", strings.NewReader("request-body-text"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+CanaryToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("content type = %q", ct)
			}
			var got map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatalf("body is not a JSON object of strings: %v", err)
			}
			if len(got) != len(tt.fields) {
				t.Errorf("fields = %v, want %v", got, tt.fields)
			}
			for _, f := range tt.fields {
				for _, want := range []string{"POST /x", "Authorization: Bearer " + CanaryToken, "request-body-text"} {
					if !strings.Contains(got[f], want) {
						t.Errorf("field %q lacks %q: %s", f, want, got[f])
					}
				}
			}
		})
	}
}
