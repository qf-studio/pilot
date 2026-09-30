package typesafe

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const fakeKey = "fake-typesafe-key-for-tests"

const okBody = `{"model":"jev-latest","answers":{"kind_1":{"type":"choice","choice":"other","probabilities":{"other":0.9,"mutation":0.1},"confidence":0.85}},"usage":{"input_tokens":10,"output_tokens":2}}`

func newTestClient(srv *httptest.Server, timeout string) *Client {
	return NewClient(Config{Endpoint: srv.URL, Model: "jev-test", Timeout: timeout}, fakeKey, nil)
}

func oneQuestion() map[string]Question {
	return map[string]Question{"kind_1": ChoiceQuestion("classify", map[string]any{"mutation": "m", "other": nil})}
}

func TestClient_Ask_RequestShape(t *testing.T) {
	var gotReq *http.Request
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okBody))
	}))
	defer srv.Close()

	qs := map[string]Question{
		"kind_1": ChoiceQuestion("a", map[string]any{"x": nil}),
		"kind_2": ChoiceQuestion("b", map[string]any{"y": nil}),
	}
	state := map[string]any{"items": map[string]string{"1": "hello"}}
	ans, err := newTestClient(srv, "2s").Ask(context.Background(), state, qs)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if gotReq.Method != http.MethodPost {
		t.Errorf("method = %s", gotReq.Method)
	}
	if got := gotReq.Header.Get("Authorization"); got != "Bearer "+fakeKey {
		t.Errorf("Authorization = %q", got)
	}
	if got := gotReq.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := gotReq.Header.Get("User-Agent"); got != "pilot-typesafe/1" {
		t.Errorf("User-Agent = %q", got)
	}
	if gotBody["model"] != "jev-test" {
		t.Errorf("model = %v", gotBody["model"])
	}
	gotQs, _ := gotBody["questions"].(map[string]any)
	if len(gotQs) != 2 {
		t.Errorf("questions = %v", gotBody["questions"])
	}
	if q, _ := gotQs["kind_1"].(map[string]any); q["type"] != "choice" || q["instructions"] != "a" {
		t.Errorf("kind_1 = %v", gotQs["kind_1"])
	}
	if st, _ := gotBody["state"].(map[string]any); st["items"] == nil {
		t.Errorf("state = %v", gotBody["state"])
	}
	if a := ans.Answers["kind_1"]; a.Choice != "other" || a.Confidence != 0.85 {
		t.Errorf("answer = %+v", a)
	}
	if ans.Usage.InputTokens != 10 {
		t.Errorf("usage = %+v", ans.Usage)
	}
}

func TestClient_Ask_RedactedStateReachesServer(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(okBody))
	}))
	defer srv.Close()

	item := capItem(redactSecrets("run with token=abc123secret and check"), 600)
	_, err := newTestClient(srv, "2s").Ask(context.Background(),
		map[string]any{"items": map[string]string{"1": item}}, oneQuestion())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "abc123secret") {
		t.Fatalf("secret reached the server: %s", raw)
	}
	if !strings.Contains(string(raw), "[redacted]") {
		t.Fatalf("expected [redacted] in body: %s", raw)
	}
}

func TestClient_Ask_Timeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	_, err := newTestClient(srv, "100ms").Ask(context.Background(), nil, oneQuestion())
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error should wrap DeadlineExceeded: %v", err)
	}
	if strings.Contains(err.Error(), fakeKey) {
		t.Errorf("error leaks key: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("timeout not honoured: %v", time.Since(start))
	}
}

func TestClient_Ask_CallerContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okBody))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newTestClient(srv, "2s").Ask(ctx, nil, oneQuestion()); err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestClient_Ask_HTTPErrors(t *testing.T) {
	for _, status := range []int{401, 422, 429, 529} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				// A misbehaving server echoes the credential back.
				_, _ = w.Write([]byte(`{"error":"bad key ` + r.Header.Get("Authorization") + ` and ` + fakeKey + `"}`))
			}))
			defer srv.Close()

			_, err := newTestClient(srv, "2s").Ask(context.Background(), nil, oneQuestion())
			if err == nil {
				t.Fatal("expected error")
			}
			msg := err.Error()
			if !strings.Contains(msg, strconv.Itoa(status)) {
				t.Errorf("error lacks status %d: %s", status, msg)
			}
			if strings.Contains(msg, fakeKey) {
				t.Errorf("error leaks key: %s", msg)
			}
		})
	}
}

func TestClient_Ask_ErrorBodyCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(strings.Repeat("z", 5000)))
	}))
	defer srv.Close()
	_, err := newTestClient(srv, "2s").Ask(context.Background(), nil, oneQuestion())
	if err == nil {
		t.Fatal("expected error")
	}
	if len(err.Error()) > 400 {
		t.Errorf("error body excerpt not capped: %d bytes", len(err.Error()))
	}
}

func TestClient_Ask_MalformedBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"not json", `not json at all`},
		{"truncated", `{"answers":{"kind_1":`},
		{"wrong shape", `{"answers":"oops"}`},
		{"no answers", `{"model":"jev-latest"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			if _, err := newTestClient(srv, "2s").Ask(context.Background(), nil, oneQuestion()); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestNewClient_NilLoggerAndDefaults(t *testing.T) {
	c := NewClient(Config{}, fakeKey, nil)
	if c.endpoint != DefaultEndpoint || c.model != DefaultModel || c.timeout != DefaultTimeout || c.log == nil {
		t.Errorf("client = %+v", c)
	}
}
