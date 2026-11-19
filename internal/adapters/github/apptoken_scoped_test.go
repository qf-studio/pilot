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
		_ = json.NewEncoder(w).Encode(scopedAnswer("qf-studio/pilot"))
	}))
}

// scopedAnswer is a well-formed answer of GitHub to a scoped mint for the
// repository fullName ("OWNER/NAME").
func scopedAnswer(fullName string) mintResponse {
	_, name, _ := strings.Cut(fullName, "/")
	return mintResponse{
		Token:               "test-scoped-read-token",
		ExpiresAt:           time.Now().Add(time.Hour).Truncate(time.Second),
		Permissions:         ReadOnlyPermissions(),
		RepositorySelection: "selected",
		Repositories:        []mintRepository{{Name: name, FullName: fullName}},
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

	token, expires, err := ts.ScopedToken(context.Background(), "qf-studio", "pilot", ReadOnlyPermissions())
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
	read := ReadOnlyPermissions()
	cases := map[string]struct {
		owner, repo string
		perms       map[string]string
	}{
		"contents write":           {"qf-studio", "pilot", writePerms},
		"admin":                    {"qf-studio", "pilot", map[string]string{"administration": "admin"}},
		"key outside the four":     {"qf-studio", "pilot", map[string]string{"administration": "read"}},
		"extra key next to four":   {"qf-studio", "pilot", extraKey},
		"empty level":              {"qf-studio", "pilot", map[string]string{"issues": ""}},
		"no permissions":           {"qf-studio", "pilot", map[string]string{}},
		"nil permissions":          {"qf-studio", "pilot", nil},
		"empty repo":               {"qf-studio", "", read},
		"owner and name":           {"qf-studio", "qf-studio/pilot", read},
		"owner only":               {"qf-studio", "qf-studio/", read},
		"leading slash":            {"qf-studio", "/pilot", read},
		"nested path":              {"qf-studio", "a/b/c", read},
		"space":                    {"qf-studio", " pilot", read},
		"query character":          {"qf-studio", "pilot?x=1", read},
		"non-ascii letter":         {"qf-studio", "pilоt", read},
		"backslash":                {"qf-studio", `qf-studio\pilot`, read},
		"name dot":                 {"qf-studio", ".", read},
		"name dot dot":             {"qf-studio", "..", read},
		"name leading dash":        {"qf-studio", "-x", read},
		"name leading dot":         {"qf-studio", ".x", read},
		"name of 101 characters":   {"qf-studio", strings.Repeat("a", 101), read},
		"empty owner":              {"", "pilot", read},
		"owner dot":                {".", "pilot", read},
		"owner dot dot":            {"..", "pilot", read},
		"owner leading dash":       {"-x", "pilot", read},
		"owner of 101 characters":  {strings.Repeat("a", 101), "pilot", read},
		"owner non-ascii letter":   {"qf-stüdio", "pilot", read},
		"owner with slash":         {"qf/studio", "pilot", read},
		"owner with a space":       {"qf studio", "pilot", read},
		"owner with a NUL":         {"qf\x00studio", "pilot", read},
		"name with a newline":      {"qf-studio", "pilot\n", read},
		"name of 300 characters":   {"qf-studio", strings.Repeat("a", 300), read},
		"owner and name both dots": {".", "..", read},
	}
	for name, tc := range cases {
		token, _, err := ts.ScopedToken(context.Background(), tc.owner, tc.repo, tc.perms)
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

	token, _, err := ts.ScopedToken(context.Background(), "qf-studio", "pilot", ReadOnlyPermissions())
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
	if _, _, err := ts.ScopedToken(context.Background(), "qf-studio", "pilot", ReadOnlyPermissions()); err != nil {
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
		a := scopedAnswer("qf-studio/pilot")
		edit(&a)
		return writeAnswer(http.StatusCreated, a)
	}

	var redirectTargetHits int64
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&redirectTargetHits, 1)
		writeAnswer(http.StatusCreated, scopedAnswer("qf-studio/pilot"))(w, r)
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
		"two repositories": answer(func(a *mintResponse) {
			a.Repositories = append(a.Repositories, mintRepository{Name: "other", FullName: "qf-studio/other"})
		}),
		"no repositories": answer(func(a *mintResponse) { a.Repositories = nil }),
		"other repository name": answer(func(a *mintResponse) {
			a.Repositories = []mintRepository{{Name: "other", FullName: "qf-studio/other"}}
		}),
		"no full_name":           answer(func(a *mintResponse) { a.Repositories[0].FullName = "" }),
		"expiry in the past":     answer(func(a *mintResponse) { a.ExpiresAt = time.Now().Add(-time.Minute) }),
		"zero expiry":            answer(func(a *mintResponse) { a.ExpiresAt = time.Time{} }),
		"expiry of two hours":    answer(func(a *mintResponse) { a.ExpiresAt = time.Now().Add(2 * time.Hour) }),
		"token of spaces":        answer(func(a *mintResponse) { a.Token = "   " }),
		"token with a newline":   answer(func(a *mintResponse) { a.Token = "test-scoped-read-token\n" }),
		"token with a space":     answer(func(a *mintResponse) { a.Token = "test-scoped read-token" }),
		"token with a NUL":       answer(func(a *mintResponse) { a.Token = "test-scoped-read-token\x00" }),
		"token of 10 characters": answer(func(a *mintResponse) { a.Token = "test-token" }),
		"token of 256 characters": answer(func(a *mintResponse) {
			a.Token = "test-scoped-read-token" + strings.Repeat("x", 256-len("test-scoped-read-token"))
		}),
		"token with a non-ascii letter": answer(func(a *mintResponse) { a.Token = "test-scoped-read-tökén" }),
		"extra permission key at read":  answer(func(a *mintResponse) { a.Permissions["administration"] = "read" }),
		"empty permissions object": func(w http.ResponseWriter, r *http.Request) {
			// omitempty drops an empty map, so the object is written by hand.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"test-scoped-read-token","expires_at":"` + time.Now().Add(time.Hour).Format(time.RFC3339) +
				`","permissions":{},"repository_selection":"selected","repositories":[{"name":"pilot","full_name":"qf-studio/pilot"}]}`))
		},
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
			token, expires, err := ts.ScopedToken(context.Background(), "qf-studio", "pilot", ReadOnlyPermissions())
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
		_ = json.NewEncoder(w).Encode(scopedAnswer("QF-Studio/Pilot"))
	}))
	defer server.Close()
	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}
	if _, _, err := ts.ScopedToken(context.Background(), "qf-studio", "pilot", ReadOnlyPermissions()); err != nil {
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
	_, _, err = ts.ScopedToken(context.Background(), "qf-studio", "pilot", ReadOnlyPermissions())
	if err == nil {
		t.Fatal("ScopedToken() = nil error, want the HTTP error")
	}
	if !strings.Contains(err.Error(), strings.Repeat("x", mintErrorMessageMax)) || strings.Contains(err.Error(), strings.Repeat("x", mintErrorMessageMax+1)) {
		t.Errorf("error has %d bytes, want GitHub's message cut to %d characters", len(err.Error()), mintErrorMessageMax)
	}
}

// TestScopedToken_AsksForSubsetOfPermissions: an answer may grant fewer of
// the keys that were asked for, never one that was not asked for.
func TestScopedToken_AsksForSubsetOfPermissions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := scopedAnswer("qf-studio/pilot")
		a.Permissions = map[string]string{"contents": "read", "metadata": "read"}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(a)
	}))
	defer server.Close()
	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}
	ctx := context.Background()
	if _, _, err := ts.ScopedToken(ctx, "qf-studio", "pilot", ReadOnlyPermissions()); err != nil {
		t.Errorf("ScopedToken() error = %v, want the token for a subset of the asked keys", err)
	}
	if _, _, err := ts.ScopedToken(ctx, "qf-studio", "pilot", map[string]string{"contents": "read"}); err == nil {
		t.Error("ScopedToken() = nil error, want a refusal of metadata that was not asked for")
	}
}

// TestScopedToken_AnswerTextIsMadeSafe: a text of an answer of GitHub in an
// error value shows each character outside printable ASCII as '?'.
func TestScopedToken_AnswerTextIsMadeSafe(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"error message": {
			status: http.StatusUnprocessableEntity,
			body:   `{"message":"bad \u001b[31mred\u001b[0m\nnext"}`,
			want:   "bad ?[31mred?[0m?next",
		},
		"repository_selection": {
			status: http.StatusCreated,
			body:   `{"token":"test-scoped-read-token","repository_selection":"\u001b[2Jall"}`,
			want:   "?[2Jall",
		},
		"full_name": {
			status: http.StatusCreated,
			body:   `{"token":"test-scoped-read-token","repository_selection":"selected","repositories":[{"name":"pilot","full_name":"evil/\u001b]0;x\u0007pilot"}]}`,
			want:   "evil/?]0;x?pilot",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
			if err != nil {
				t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
			}
			_, _, err = ts.ScopedToken(context.Background(), "qf-studio", "pilot", ReadOnlyPermissions())
			if err == nil {
				t.Fatal("ScopedToken() = nil error, want an error")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error %q does not show the answer text as %q", msg, tc.want)
			}
			for _, r := range msg {
				if r < ' ' || r > '~' {
					t.Errorf("error %q holds the character %U outside printable ASCII", msg, r)
				}
			}
		})
	}
}

// TestFullMint_FollowsNoRedirect: the full mint stops at a redirect answer,
// so the App JWT of the request never reaches the host the answer names.
func TestFullMint_FollowsNoRedirect(t *testing.T) {
	var targetHits int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&targetHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(mintResponse{Token: "test-installation-token", ExpiresAt: time.Now().Add(time.Hour)})
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}
	token, err := ts.Token(context.Background())
	if err == nil {
		t.Fatal("Token() = nil error, want the redirect answer as an error")
	}
	if token != "" {
		t.Errorf("Token() = %q, want no token", token)
	}
	if n := atomic.LoadInt64(&targetHits); n != 0 {
		t.Errorf("redirect target got %d requests, want 0 (no redirect is followed)", n)
	}
}
