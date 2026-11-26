package forgejo

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
)

// maxWebhookBody bounds the accepted delivery size.
const maxWebhookBody = 2 << 20

type webhookHandler struct {
	secret  []byte
	onIssue func(ctx context.Context, repo string, number int) error
}

// NewWebhookHandler returns an http.Handler for signed Forgejo issue
// deliveries. onIssue receives only repository.full_name and issue.number and
// must return nil for ignored, duplicate or persisted work; a non-nil error
// answers 503. An empty secret rejects every request (no unsigned mode).
func NewWebhookHandler(secret string, onIssue func(ctx context.Context, repo string, number int) error) http.Handler {
	return &webhookHandler{secret: []byte(secret), onIssue: onIssue}
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(h.secret) == 0 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.ContentLength > maxWebhookBody {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	raw, err := readBounded(w, r)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "bad request", http.StatusBadRequest)
		}
		return
	}
	if !h.validSignature(r, raw) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		http.Error(w, "unsupported content type", http.StatusBadRequest)
		return
	}
	if eventName(r) == "" {
		http.Error(w, "missing event header", http.StatusBadRequest)
		return
	}
	if !relevantEvent(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var p struct {
		Action string `json:"action"`
		Issue  *struct {
			Number      int             `json:"number"`
			PullRequest json.RawMessage `json:"pull_request"`
		} `json:"issue"`
		PullRequest json.RawMessage `json:"pull_request"`
		Repository  *struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if p.Action != "opened" && p.Action != "label_updated" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if present(p.PullRequest) || (p.Issue != nil && present(p.Issue.PullRequest)) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if p.Issue == nil || p.Issue.Number <= 0 || p.Repository == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, _, err := splitRepo(p.Repository.FullName); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if h.onIssue == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := h.onIssue(r.Context(), p.Repository.FullName, p.Issue.Number); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func readBounded(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(http.MaxBytesReader(w, r.Body, maxWebhookBody)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// validSignature checks the hex HMAC-SHA256 of the exact raw bytes. The
// X-Forgejo-Signature header is primary; X-Gitea-Signature is used only when
// the primary is absent.
func (h *webhookHandler) validSignature(r *http.Request, raw []byte) bool {
	sig := strings.TrimSpace(r.Header.Get("X-Forgejo-Signature"))
	if sig == "" {
		sig = strings.TrimSpace(r.Header.Get("X-Gitea-Signature"))
	}
	got, err := hex.DecodeString(sig)
	if err != nil || len(got) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, h.secret)
	mac.Write(raw)
	return hmac.Equal(got, mac.Sum(nil))
}

// relevantEvent reports whether the headers describe an issues event. Forgejo
// 11 sends X-Forgejo-Event: issues for both opened and label_updated, with
// X-Forgejo-Event-Type issues or issue_label; Gitea-style headers are a
// fallback. The Event-Type header is optional, but when present it must be one
// of the two issue types. The payload action decides the rest.
func relevantEvent(r *http.Request) bool {
	ev := eventName(r)
	if ev != "issues" && ev != "issue_label" {
		return false
	}
	et := r.Header.Get("X-Forgejo-Event-Type")
	if et == "" {
		et = r.Header.Get("X-Gitea-Event-Type")
	}
	return et == "" || et == "issues" || et == "issue_label"
}

func eventName(r *http.Request) string {
	if ev := r.Header.Get("X-Forgejo-Event"); ev != "" {
		return ev
	}
	return r.Header.Get("X-Gitea-Event")
}

func present(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	return len(s) > 0 && !bytes.Equal(s, []byte("null"))
}
