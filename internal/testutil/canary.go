package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// CanaryToken is a credential value that must never appear in an error, a log
// line or an alert. Build a client with it as the credential, drive the
// client through a failure, and call AssertNoCanaryLeak on everything the
// failure produced.
const CanaryToken = "pilot-canary-7f3e1c9a-do-not-log"

// AssertNoCanaryLeak fails the test for every text that holds canary, naming
// the index of the text in texts.
func AssertNoCanaryLeak(t testing.TB, canary string, texts ...string) {
	t.Helper()
	for i, text := range texts {
		if strings.Contains(text, canary) {
			t.Errorf("text %d holds the canary %q: %s", i, canary, text)
		}
	}
}

// dumpRequest renders the request line, every header and, when it has one,
// the body. It is the worst case a failing component can put into an error.
func dumpRequest(req *http.Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", req.Method, req.URL.String())
	names := make([]string, 0, len(req.Header))
	for name := range req.Header {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "%s: %s\n", name, strings.Join(req.Header[name], ", "))
	}
	return b.String()
}

type failingTransport struct{}

func (failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
	return nil, fmt.Errorf("transport failed for request:\n%s", dumpRequest(req))
}

// FailingTransport returns a RoundTripper that always fails with an error
// whose text holds the request URL and all request headers. A real transport
// never prints headers; this is the worst case, so a client that passes with
// it cannot leak its credential through a transport error.
func FailingTransport() http.RoundTripper {
	return failingTransport{}
}

// EchoOption customizes EchoServer.
type EchoOption func(*echoConfig)

type echoConfig struct {
	jsonFields []string
}

// EchoJSONFields makes EchoServer answer with a JSON object instead of plain
// text. Each named field holds the echo (request line, headers and body) as a
// string. Slack and Telegram parse a JSON body and put its "error" or
// "description" string into the error they return, so a plain-text echo never
// reaches that code path; a JSON echo does.
func EchoJSONFields(fields ...string) EchoOption {
	return func(c *echoConfig) { c.jsonFields = fields }
}

// EchoServer starts an httptest server that answers every request with status
// and a plain-text body holding the request line, all request headers and the
// request body. With EchoJSONFields the body is a JSON object that carries the
// same text in the named string fields. It is closed when the test ends.
func EchoServer(t testing.TB, status int, opts ...EchoOption) *httptest.Server {
	t.Helper()
	var cfg echoConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		echo := fmt.Sprintf("%s\n%s", dumpRequest(r), body)
		if len(cfg.jsonFields) > 0 {
			obj := make(map[string]string, len(cfg.jsonFields))
			for _, f := range cfg.jsonFields {
				obj[f] = echo
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(obj)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, echo)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ErrText returns err's text and fails the test when err is nil: a canary
// test whose call unexpectedly succeeded proves nothing.
func ErrText(t testing.TB, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected the call to fail")
	}
	return err.Error()
}

// CaptureSlog routes the default slog logger into the returned buffer at debug
// level until the test ends. Tests that use it must not run in parallel.
func CaptureSlog(t testing.TB) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}
