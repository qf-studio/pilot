package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// closingServer accepts the connection and drops it, so the client sees a
// transport error whose url.Error text carries the request URL.
func closingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("hijacking unsupported")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_TransportErrorDoesNotLeakToken(t *testing.T) {
	// Assembled at runtime: a literal would trip the secret-pattern scan.
	token := "123456789:AA" + strings.Repeat("a", 33)
	srv := closingServer(t)
	client := NewClientWithBaseURL(token, srv.URL)
	ctx := context.Background()

	calls := map[string]func() error{
		"CheckSingleton": func() error { return client.CheckSingleton(ctx) },
		"GetMe":          func() error { _, err := client.GetMe(ctx); return err },
		"GetUpdates":     func() error { _, err := client.GetUpdates(ctx, 0, 0); return err },
		"Verify":         func() error { return client.Verify(ctx) },
		"SendMessage": func() error {
			_, err := client.SendMessage(ctx, "1", "hi", "", 0)
			return err
		},
		"GetFile": func() error { _, err := client.GetFile(ctx, "file-id"); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("expected a transport error")
			}
			if strings.Contains(err.Error(), token) {
				t.Errorf("error holds the token: %v", err)
			}
			if !strings.Contains(err.Error(), "<redacted>") {
				t.Errorf("error lacks <redacted>: %v", err)
			}
		})
	}
}

func TestClient_DownloadFileTransportErrorDoesNotLeakToken(t *testing.T) {
	token := "123456789:AA" + strings.Repeat("a", 33)
	client := NewClient(token)
	// Route the hard-coded file URL to a server that drops the connection.
	srv := closingServer(t)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient.Transport = rewriteTransport{host: target.Host}

	_, err = client.DownloadFile(context.Background(), "photos/file_1.jpg")
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error holds the token: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Errorf("error lacks <redacted>: %v", err)
	}
}

// rewriteTransport sends every request to host over plain HTTP while keeping
// the original URL on the request, so the resulting url.Error shows it.
type rewriteTransport struct{ host string }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = rt.host
	return http.DefaultTransport.RoundTrip(clone)
}

func TestClient_RedactErrorLeavesOtherErrorsAndEmptyToken(t *testing.T) {
	plain := errors.New("boom")
	if got := NewClient("tok").redactError(plain); got != plain {
		t.Errorf("non-url error changed: %v", got)
	}
	ue := &url.Error{Op: "Get", URL: "https://example.com/a", Err: plain}
	_ = NewClient("").redactError(ue)
	if ue.URL != "https://example.com/a" {
		t.Errorf("empty token rewrote URL: %s", ue.URL)
	}
}
