package github

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// jwtRecorder remembers the App JWT of the requests that pass through it. The
// JWT is minted per call, so the test cannot know it beforehand; it is the
// credential of the exchange and must not reach an error or a log.
type jwtRecorder struct {
	inner http.RoundTripper

	mu   sync.Mutex
	jwts []string
}

func (r *jwtRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if jwt := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "); jwt != "" {
		r.mu.Lock()
		r.jwts = append(r.jwts, jwt)
		r.mu.Unlock()
	}
	return r.inner.RoundTrip(req)
}

func (r *jwtRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.jwts...)
}

// TestCanary_AppTokenExchangeDoesNotLeakJWT drives the installation-token
// exchange through a transport that fails with the full request in its error,
// and through servers that answer with the request echoed back.
func TestCanary_AppTokenExchangeDoesNotLeakJWT(t *testing.T) {
	logs := testutil.CaptureSlog(t)
	ctx := context.Background()

	legs := []struct {
		name  string
		inner http.RoundTripper
		url   func(t *testing.T) string
	}{
		{"transport error", testutil.FailingTransport(), func(*testing.T) string { return "https://api.github.test" }},
		{"401 echo", http.DefaultTransport, func(t *testing.T) string { return testutil.EchoServer(t, http.StatusUnauthorized).URL }},
		{"500 echo", http.DefaultTransport, func(t *testing.T) string { return testutil.EchoServer(t, http.StatusInternalServerError).URL }},
	}
	for _, leg := range legs {
		t.Run(leg.name, func(t *testing.T) {
			ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), leg.url(t))
			if err != nil {
				t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
			}
			rec := &jwtRecorder{inner: leg.inner}
			ts.httpClient.Transport = rec

			_, err = ts.Token(ctx)
			text := testutil.ErrText(t, err)

			jwts := rec.seen()
			if len(jwts) == 0 {
				t.Fatal("no request reached the transport, nothing was tested")
			}
			for _, jwt := range jwts {
				testutil.AssertNoCanaryLeak(t, jwt, text, logs.String())
			}
		})
	}
}
