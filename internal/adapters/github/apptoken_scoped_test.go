package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// scopedTokenServer stands in for POST /app/installations/{id}/access_tokens
// and records the body of each request.
func scopedTokenServer(t *testing.T, calls *int64, bodies chan<- []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(calls, 1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		if bodies != nil {
			bodies <- body
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(scopedAnswer("pilot"))
	}))
}

// scopedAnswer is a well-formed answer of GitHub to a scoped mint for the
// repository name.
func scopedAnswer(name string) mintResponse {
	return mintResponse{
		Token:               "test-scoped-read-token",
		ExpiresAt:           time.Now().Add(time.Hour).Truncate(time.Second),
		Permissions:         ReadOnlyPermissions(),
		RepositorySelection: "selected",
		Repositories:        []mintRepository{{Name: name}},
	}
}

func TestMintScopedBodyHasReposAndReadPermissions(t *testing.T) {
	var calls int64
	bodies := make(chan []byte, 1)
	server := scopedTokenServer(t, &calls, bodies)
	defer server.Close()

	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}

	token, expires, err := ts.ScopedToken(context.Background(), "pilot", ReadOnlyPermissions())
	if err != nil {
		t.Fatalf("ScopedToken() error = %v", err)
	}
	if token != "test-scoped-read-token" {
		t.Errorf("ScopedToken() token = %q, want the minted token", token)
	}
	if time.Until(expires) < 50*time.Minute {
		t.Errorf("ScopedToken() expires = %v, want the expiry of the response (~1h)", expires)
	}

	var got struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	if err := json.Unmarshal(<-bodies, &got); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if !reflect.DeepEqual(got.Repositories, []string{"pilot"}) {
		t.Errorf("repositories = %v, want [pilot] (the name with no owner)", got.Repositories)
	}
	want := map[string]string{"contents": "read", "issues": "read", "pull_requests": "read", "metadata": "read"}
	if !reflect.DeepEqual(got.Permissions, want) {
		t.Errorf("permissions = %v, want %v", got.Permissions, want)
	}
}

func TestScopedToken_RefusesNonReadWithoutRequest(t *testing.T) {
	var calls int64
	server := scopedTokenServer(t, &calls, nil)
	defer server.Close()

	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}

	writePerms := ReadOnlyPermissions()
	writePerms["contents"] = "write"
	extraKey := ReadOnlyPermissions()
	extraKey["administration"] = "read"
	cases := map[string]struct {
		repo  string
		perms map[string]string
	}{
		"contents write":         {"pilot", writePerms},
		"admin":                  {"pilot", map[string]string{"administration": "admin"}},
		"key outside the four":   {"pilot", map[string]string{"administration": "read"}},
		"extra key next to four": {"pilot", extraKey},
		"empty level":            {"pilot", map[string]string{"issues": ""}},
		"no permissions":         {"pilot", map[string]string{}},
		"nil permissions":        {"pilot", nil},
		"empty repo":             {"", ReadOnlyPermissions()},
		"owner and name":         {"qf-studio/pilot", ReadOnlyPermissions()},
		"owner only":             {"qf-studio/", ReadOnlyPermissions()},
		"leading slash":          {"/pilot", ReadOnlyPermissions()},
		"nested path":            {"a/b/c", ReadOnlyPermissions()},
		"space":                  {" pilot", ReadOnlyPermissions()},
		"query character":        {"pilot?x=1", ReadOnlyPermissions()},
		"non-ascii letter":       {"pilоt", ReadOnlyPermissions()},
		"backslash":              {`qf-studio\pilot`, ReadOnlyPermissions()},
	}
	for name, tc := range cases {
		token, _, err := ts.ScopedToken(context.Background(), tc.repo, tc.perms)
		if err == nil {
			t.Errorf("%s: ScopedToken() = nil error, want a refusal", name)
		}
		if token != "" {
			t.Errorf("%s: ScopedToken() token = %q, want empty", name, token)
		}
	}
	if n := atomic.LoadInt64(&calls); n != 0 {
		t.Errorf("token endpoint got %d requests, want 0 for refused input", n)
	}
}

func TestScopedToken_HTTPErrorHasNoTokenText(t *testing.T) {
	var gotJWT atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		gotJWT.Store(jwt)
		// A proxy that echoes the credential and carries a token field.
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"There is at least one repository that does not exist (` + jwt + `)","token":"test-leaked-token-text"}`))
	}))
	defer server.Close()

	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}

	token, _, err := ts.ScopedToken(context.Background(), "pilot", ReadOnlyPermissions())
	if err == nil {
		t.Fatal("ScopedToken() = nil error, want the HTTP error of the endpoint")
	}
	if token != "" {
		t.Errorf("ScopedToken() token = %q, want empty", token)
	}
	msg := err.Error()
	if !strings.Contains(msg, "422") {
		t.Errorf("error %q does not name the HTTP status", msg)
	}
	if strings.Contains(msg, "test-leaked-token-text") {
		t.Errorf("error %q carries the token text of the response", msg)
	}
	if jwt, _ := gotJWT.Load().(string); jwt == "" || strings.Contains(msg, jwt) {
		t.Errorf("error %q carries the App JWT (or no JWT was sent)", msg)
	}
}

func TestScopedToken_DoesNotTouchFullTokenCache(t *testing.T) {
	var fullMints int64
	var scopedCalls int64
	full := fakeTokenServer(t, time.Hour, &fullMints)
	defer full.Close()
	scoped := scopedTokenServer(t, &scopedCalls, nil)
	defer scoped.Close()

	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), full.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}
	fullToken, err := ts.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	ts.mu.Lock()
	cachedExpiry := ts.expiresAt
	ts.mu.Unlock()

	ts.baseURL = scoped.URL
	if _, _, err := ts.ScopedToken(context.Background(), "pilot", ReadOnlyPermissions()); err != nil {
		t.Fatalf("ScopedToken() error = %v", err)
	}
	ts.baseURL = full.URL

	ts.mu.Lock()
	gotToken, gotExpiry := ts.token, ts.expiresAt
	ts.mu.Unlock()
	if gotToken != fullToken || !gotExpiry.Equal(cachedExpiry) {
		t.Errorf("full token cache = %q (exp %v) after a scoped call, want %q (exp %v)", gotToken, gotExpiry, fullToken, cachedExpiry)
	}
	again, err := ts.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() after scoped call error = %v", err)
	}
	if again != fullToken {
		t.Errorf("Token() after a scoped call = %q, want the cached full token %q", again, fullToken)
	}
	if n := atomic.LoadInt64(&fullMints); n != 1 {
		t.Errorf("full mints = %d, want 1 (the scoped call must not re-mint or clear the full token)", n)
	}
}

// TestScopedToken_RefusesBadAnswer: an answer of GitHub that is wider or
// weaker than the read token for one repository is an error and no token.
func TestScopedToken_RefusesBadAnswer(t *testing.T) {
	writeAnswer := func(status int, v any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	answer := func(edit func(*mintResponse)) http.HandlerFunc {
		a := scopedAnswer("pilot")
		edit(&a)
		return writeAnswer(http.StatusCreated, a)
	}

	var redirectTargetHits int64
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&redirectTargetHits, 1)
		writeAnswer(http.StatusCreated, scopedAnswer("pilot"))(w, r)
	}))
	defer redirectTarget.Close()

	cases := map[string]http.HandlerFunc{
		"contents write": answer(func(a *mintResponse) { a.Permissions["contents"] = "write" }),
		"extra admin permission": answer(func(a *mintResponse) {
			a.Permissions["administration"] = "admin"
		}),
		"no permissions":           answer(func(a *mintResponse) { a.Permissions = nil }),
		"repository_selection all": answer(func(a *mintResponse) { a.RepositorySelection = "all" }),
		"no repository_selection":  answer(func(a *mintResponse) { a.RepositorySelection = "" }),
		"two repositories":         answer(func(a *mintResponse) { a.Repositories = append(a.Repositories, mintRepository{Name: "other"}) }),
		"no repositories":          answer(func(a *mintResponse) { a.Repositories = nil }),
		"other repository name":    answer(func(a *mintResponse) { a.Repositories = []mintRepository{{Name: "other"}} }),
		"expiry in the past":       answer(func(a *mintResponse) { a.ExpiresAt = time.Now().Add(-time.Minute) }),
		"zero expiry":              answer(func(a *mintResponse) { a.ExpiresAt = time.Time{} }),
		"token of spaces":          answer(func(a *mintResponse) { a.Token = "   " }),
		"redirect": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectTarget.URL+r.URL.Path, http.StatusTemporaryRedirect)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
			if err != nil {
				t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
			}
			token, expires, err := ts.ScopedToken(context.Background(), "pilot", ReadOnlyPermissions())
			if err == nil {
				t.Fatal("ScopedToken() = nil error, want a refusal of the answer")
			}
			if token != "" || !expires.IsZero() {
				t.Errorf("ScopedToken() = (%q, %v), want no token and a zero expiry", token, expires)
			}
			if strings.Contains(err.Error(), "test-scoped-read-token") {
				t.Errorf("error %q carries the token", err)
			}
		})
	}
	if n := atomic.LoadInt64(&redirectTargetHits); n != 0 {
		t.Errorf("redirect target got %d requests, want 0 (no redirect is followed)", n)
	}
}

// TestScopedToken_AnswerNameCaseIgnored: GitHub answers with the canonical
// case of the name; a request in another case is still the same repository.
func TestScopedToken_AnswerNameCaseIgnored(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(scopedAnswer("Pilot"))
	}))
	defer server.Close()
	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}
	if _, _, err := ts.ScopedToken(context.Background(), "pilot", ReadOnlyPermissions()); err != nil {
		t.Errorf("ScopedToken() error = %v, want the token for the same name in another case", err)
	}
}

// TestScopedToken_ErrorMessageCut: GitHub's error message is cut to
// mintErrorMessageMax characters in the error.
func TestScopedToken_ErrorMessageCut(t *testing.T) {
	long := strings.Repeat("x", 5000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": long})
	}))
	defer server.Close()
	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}
	_, _, err = ts.ScopedToken(context.Background(), "pilot", ReadOnlyPermissions())
	if err == nil {
		t.Fatal("ScopedToken() = nil error, want the HTTP error")
	}
	if !strings.Contains(err.Error(), strings.Repeat("x", mintErrorMessageMax)) || strings.Contains(err.Error(), strings.Repeat("x", mintErrorMessageMax+1)) {
		t.Errorf("error has %d bytes, want GitHub's message cut to %d characters", len(err.Error()), mintErrorMessageMax)
	}
}
