package forgejo

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSecret = "test-webhook-secret"

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

type call struct {
	repo   string
	number int
}

type hook struct {
	h     http.Handler
	calls []call
	err   error
}

func newHook(secret string) *hook {
	k := &hook{}
	k.h = NewWebhookHandler(secret, func(_ context.Context, repo string, n int) error {
		k.calls = append(k.calls, call{repo, n})
		return k.err
	})
	return k
}

// req builds a signed Forgejo 11 delivery; mutate before sending to break it.
func req(t *testing.T, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/webhooks/forgejo", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forgejo-Event", "issues")
	r.Header.Set("X-Forgejo-Event-Type", "issues")
	r.Header.Set("X-Forgejo-Signature", sign(testSecret, body))
	return r
}

func (k *hook) do(r *http.Request) int {
	w := httptest.NewRecorder()
	k.h.ServeHTTP(w, r)
	return w.Code
}

func TestWebhookAcceptsRealDeliveries(t *testing.T) {
	cases := []struct{ name, fixture, eventType string }{
		{"opened", "webhook_opened.json", "issues"},
		{"label_updated", "webhook_label_updated.json", "issue_label"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fixture(t, tc.fixture))
			k := newHook(testSecret)
			r := req(t, body)
			r.Header.Set("X-Forgejo-Event-Type", tc.eventType)
			if code := k.do(r); code != 204 {
				t.Fatalf("code = %d", code)
			}
			if len(k.calls) != 1 || k.calls[0] != (call{"fixturebot/pilot-fixture", 3}) {
				t.Fatalf("calls = %+v", k.calls)
			}
			// Event-Type is optional: the action alone decides.
			k2 := newHook(testSecret)
			r = req(t, body)
			r.Header.Del("X-Forgejo-Event-Type")
			if code := k2.do(r); code != 204 || len(k2.calls) != 1 {
				t.Fatalf("no event-type: code=%d calls=%d", code, len(k2.calls))
			}
		})
	}
}

func TestWebhookGiteaFallbackHeaders(t *testing.T) {
	body := []byte(fixture(t, "webhook_opened.json"))
	k := newHook(testSecret)
	r := req(t, body)
	r.Header.Del("X-Forgejo-Signature")
	r.Header.Del("X-Forgejo-Event")
	r.Header.Set("X-Gitea-Signature", sign(testSecret, body))
	r.Header.Set("X-Gitea-Event", "issues")
	if code := k.do(r); code != 204 || len(k.calls) != 1 {
		t.Fatalf("code=%d calls=%d", code, len(k.calls))
	}
	// A present-but-wrong primary does not fall back to a valid secondary.
	k = newHook(testSecret)
	r = req(t, body)
	r.Header.Set("X-Forgejo-Signature", strings.Repeat("0", 64))
	r.Header.Set("X-Gitea-Signature", sign(testSecret, body))
	if code := k.do(r); code != 401 || len(k.calls) != 0 {
		t.Fatalf("code=%d calls=%d", code, len(k.calls))
	}
}

func TestWebhookSignatureOverRawBytes(t *testing.T) {
	raw := []byte(fixture(t, "webhook_opened.json"))
	// Same JSON, different bytes: the signature over the original must not verify.
	altered := bytes.ReplaceAll(raw, []byte(`"opened"`), []byte(`"opened" `))
	remarshaled := bytes.ReplaceAll(raw, []byte(","), []byte(", "))

	signedAltered := req(t, altered)
	signedAltered.Header.Set("X-Forgejo-Signature", sign(testSecret, raw))
	signedRemarsh := req(t, remarshaled)
	signedRemarsh.Header.Set("X-Forgejo-Signature", sign(testSecret, bytes.TrimSpace(raw)))

	good := sign(testSecret, raw)
	cases := map[string]func(*http.Request){
		"missing":      func(r *http.Request) { r.Header.Del("X-Forgejo-Signature") },
		"empty":        func(r *http.Request) { r.Header.Set("X-Forgejo-Signature", "") },
		"truncated":    func(r *http.Request) { r.Header.Set("X-Forgejo-Signature", good[:62]) },
		"non-hex":      func(r *http.Request) { r.Header.Set("X-Forgejo-Signature", strings.Repeat("z", 64)) },
		"wrong secret": func(r *http.Request) { r.Header.Set("X-Forgejo-Signature", sign("other", raw)) },
		"prefixed":     func(r *http.Request) { r.Header.Set("X-Forgejo-Signature", "sha256="+good) },
		"uppercase ok": nil, // hex is case-insensitive; must still be accepted
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			k := newHook(testSecret)
			r := req(t, raw)
			want := 401
			if mut == nil {
				r.Header.Set("X-Forgejo-Signature", strings.ToUpper(good))
				want = 204
			} else {
				mut(r)
			}
			if code := k.do(r); code != want {
				t.Fatalf("code = %d, want %d", code, want)
			}
			if want == 401 && len(k.calls) != 0 {
				t.Fatal("callback ran on rejected request")
			}
		})
	}
	for name, r := range map[string]*http.Request{"body altered after signing": signedAltered, "re-marshaled body": signedRemarsh} {
		k := newHook(testSecret)
		if code := k.do(r); code != 401 || len(k.calls) != 0 {
			t.Fatalf("%s: code=%d calls=%d", name, code, len(k.calls))
		}
	}
}

func TestWebhookEmptySecretRejectsEverything(t *testing.T) {
	body := []byte(fixture(t, "webhook_opened.json"))
	k := newHook("")
	r := req(t, body)
	r.Header.Set("X-Forgejo-Signature", sign("", body))
	if code := k.do(r); code != 401 || len(k.calls) != 0 {
		t.Fatalf("signed with empty secret: code=%d", code)
	}
	r = req(t, body)
	r.Header.Del("X-Forgejo-Signature")
	if code := k.do(r); code != 401 {
		t.Fatalf("unsigned: code=%d", code)
	}
}

func TestWebhookMethod(t *testing.T) {
	k := newHook(testSecret)
	for _, m := range []string{"GET", "PUT", "DELETE", "HEAD"} {
		r := httptest.NewRequest(m, "/webhooks/forgejo", nil)
		w := httptest.NewRecorder()
		k.h.ServeHTTP(w, r)
		if w.Code != 405 || w.Header().Get("Allow") != "POST" {
			t.Fatalf("%s: code=%d", m, w.Code)
		}
	}
}

func TestWebhookOversize(t *testing.T) {
	big := bytes.Repeat([]byte("a"), maxWebhookBody+1)
	k := newHook(testSecret)
	r := req(t, big)
	if code := k.do(r); code != 413 || len(k.calls) != 0 {
		t.Fatalf("declared length: code=%d", code)
	}
	r = req(t, big)
	r.ContentLength = -1 // chunked: only the reader limit can catch it
	if code := k.do(r); code != 413 {
		t.Fatalf("undeclared length: code=%d", code)
	}
	// Exactly at the limit is read (and then fails on content, not size).
	r = req(t, bytes.Repeat([]byte("a"), maxWebhookBody))
	if code := k.do(r); code != 400 {
		t.Fatalf("at limit: code=%d", code)
	}
}

func TestWebhookInvalidContent(t *testing.T) {
	good := []byte(fixture(t, "webhook_opened.json"))
	signed := func(body string, mut func(*http.Request)) *http.Request {
		r := req(t, []byte(body))
		if mut != nil {
			mut(r)
		}
		return r
	}
	cases := map[string]*http.Request{
		"not json":          signed("not json", nil),
		"empty body":        signed("", nil),
		"form content type": signed(string(good), func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }),
		"no content type":   signed(string(good), func(r *http.Request) { r.Header.Del("Content-Type") }),
		"no event":          signed(string(good), func(r *http.Request) { r.Header.Del("X-Forgejo-Event") }),
		"no repository":     signed(`{"action":"opened","issue":{"number":3}}`, nil),
		"bad repo name":     signed(`{"action":"opened","issue":{"number":3},"repository":{"full_name":"../x"}}`, nil),
		"no issue":          signed(`{"action":"opened","repository":{"full_name":"a/b"}}`, nil),
		"zero number":       signed(`{"action":"opened","issue":{"number":0},"repository":{"full_name":"a/b"}}`, nil),
		"string number":     signed(`{"action":"opened","issue":{"number":"3"},"repository":{"full_name":"a/b"}}`, nil),
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			k := newHook(testSecret)
			if code := k.do(r); code != 400 || len(k.calls) != 0 {
				t.Fatalf("code=%d calls=%d", code, len(k.calls))
			}
		})
	}
}

func TestWebhookIgnored(t *testing.T) {
	good := string(fixture(t, "webhook_opened.json"))
	withAction := func(a string) string { return strings.Replace(good, `"opened"`, `"`+a+`"`, 1) }
	cases := map[string]func() *http.Request{
		"push event": func() *http.Request {
			r := req(t, []byte(`{"ref":"refs/heads/main"}`))
			r.Header.Set("X-Forgejo-Event", "push")
			return r
		},
		"pull_request event": func() *http.Request {
			r := req(t, []byte(good))
			r.Header.Set("X-Forgejo-Event", "pull_request")
			return r
		},
		"issue_comment event": func() *http.Request {
			r := req(t, []byte(good))
			r.Header.Set("X-Forgejo-Event", "issue_comment")
			return r
		},
		"unknown event type": func() *http.Request {
			r := req(t, []byte(good))
			r.Header.Set("X-Forgejo-Event-Type", "issue_assign")
			return r
		},
		"closed":        func() *http.Request { return req(t, []byte(withAction("closed"))) },
		"edited":        func() *http.Request { return req(t, []byte(withAction("edited"))) },
		"label_cleared": func() *http.Request { return req(t, []byte(withAction("label_cleared"))) },
		"reopened":      func() *http.Request { return req(t, []byte(withAction("reopened"))) },
		"PR-backed issue": func() *http.Request {
			return req(t, []byte(strings.Replace(good, `"pull_request":null`, `"pull_request":{"merged":false}`, 1)))
		},
		"PR payload": func() *http.Request {
			return req(t, []byte(strings.Replace(good, `"action":"opened"`, `"action":"opened","pull_request":{"number":3}`, 1)))
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			k := newHook(testSecret)
			if code := k.do(mk()); code != 204 || len(k.calls) != 0 {
				t.Fatalf("code=%d calls=%d", code, len(k.calls))
			}
		})
	}
	// Unrelated events are only acknowledged when signed.
	k := newHook(testSecret)
	r := req(t, []byte(`{}`))
	r.Header.Set("X-Forgejo-Event", "push")
	r.Header.Set("X-Forgejo-Signature", strings.Repeat("0", 64))
	if code := k.do(r); code != 401 {
		t.Fatalf("unsigned push: code=%d", code)
	}
}

func TestWebhookCallbackFailureIs503(t *testing.T) {
	body := []byte(fixture(t, "webhook_opened.json"))
	k := newHook(testSecret)
	k.err = errors.New("store down: secret-detail")
	w := httptest.NewRecorder()
	k.h.ServeHTTP(w, req(t, body))
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret-detail") {
		t.Fatalf("code=%d body=%q", w.Code, w.Body.String())
	}
	if len(k.calls) != 1 {
		t.Fatalf("calls=%d", len(k.calls))
	}

	nilCb := NewWebhookHandler(testSecret, nil)
	w = httptest.NewRecorder()
	nilCb.ServeHTTP(w, req(t, body))
	if w.Code != 503 {
		t.Fatalf("nil callback: code=%d", w.Code)
	}
}
