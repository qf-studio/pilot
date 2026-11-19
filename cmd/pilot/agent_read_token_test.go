package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
)

// TestAgentReadTokenProvider_NilWithoutApp: with no GitHub App configured
// there is no provider, even when the daemon holds a config token and a
// GITHUB_TOKEN — the token of the daemon is never handed to the agent.
func TestAgentReadTokenProvider_NilWithoutApp(t *testing.T) {
	resetGitHubTokenTestState(t)
	if err := os.Setenv("GITHUB_TOKEN", "test-github-token"); err != nil {
		t.Fatalf("setenv: %v", err)
	}

	for name, cfg := range map[string]*config.Config{
		"nil config":   nil,
		"no adapters":  {},
		"no github":    {Adapters: &config.AdaptersConfig{}},
		"token no app": {Adapters: &config.AdaptersConfig{GitHub: &github.Config{Enabled: true, Token: "config-token"}}},
	} {
		if agentReadTokenProvider(cfg) != nil {
			t.Errorf("%s: agentReadTokenProvider() != nil, want nil with no GitHub App", name)
		}
	}
}

// TestAgentReadTokenProvider_AppMintFailureNoFallback: a failed App mint
// gives an error, not the config token that resolveGitHubToken falls back to.
func TestAgentReadTokenProvider_AppMintFailureNoFallback(t *testing.T) {
	resetGitHubTokenTestState(t)

	cfg := &config.Config{Adapters: &config.AdaptersConfig{
		GitHub: &github.Config{
			Enabled: true,
			Token:   "config-token",
			Repo:    "owner/repo",
			App: &github.AppConfig{
				AppID:          123456,
				InstallationID: 78901234,
				PrivateKeyPath: "/nonexistent/github-app.pem",
			},
		},
	}}

	provider := agentReadTokenProvider(cfg)
	if provider == nil {
		t.Fatal("agentReadTokenProvider() = nil, want a provider with a GitHub App configured")
	}
	token, _, err := provider(context.Background(), "owner/repo")
	if err == nil {
		t.Fatal("provider() = nil error, want the mint error of the unreadable key")
	}
	if token != "" {
		t.Errorf("provider() token = %q, want empty (no fallback to the daemon token)", token)
	}
}

// testAgentAppConfig writes an RSA key made in the test and returns an App
// config that points at it.
func testAgentAppConfig(t *testing.T) *github.AppConfig {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test RSA key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "test-github-app.pem")
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing test private key: %v", err)
	}
	return &github.AppConfig{AppID: 123456, InstallationID: 78901234, PrivateKeyPath: path}
}

// TestAgentReadTokenProviderNeverReturnsTheFullToken: the provider returns
// the scoped read token of the configured repository — never the full token
// of the source, also not after a failed scoped call — and refuses each
// repository that is not in the GitHub config without a request.
func TestAgentReadTokenProviderNeverReturnsTheFullToken(t *testing.T) {
	resetGitHubTokenTestState(t)

	const fullToken = "test-full-installation-token"
	const scopedToken = "test-scoped-read-token"
	type scopedBody struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	var (
		mu         sync.Mutex
		scopedReqs []scopedBody
		fullMints  int64
		failScoped atomic.Bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if len(raw) == 0 {
			// The full mint: no body, every repository of the installation.
			atomic.AddInt64(&fullMints, 1)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":                fullToken,
				"expires_at":           time.Now().Add(time.Hour),
				"repository_selection": "all",
				"permissions":          map[string]string{"contents": "write", "pull_requests": "write"},
			})
			return
		}
		var body scopedBody
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("scoped request body is not JSON: %v", err)
		}
		mu.Lock()
		scopedReqs = append(scopedReqs, body)
		mu.Unlock()
		if failScoped.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"server error"}`))
			return
		}
		// The installation of this server holds qf-studio/pilot and
		// acme/widget.
		owners := map[string]string{"pilot": "qf-studio", "widget": "acme"}
		repos := make([]map[string]string, 0, len(body.Repositories))
		for _, name := range body.Repositories {
			repos = append(repos, map[string]string{"name": name, "full_name": owners[name] + "/" + name})
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":                scopedToken,
			"expires_at":           time.Now().Add(time.Hour),
			"repository_selection": "selected",
			"repositories":         repos,
			"permissions":          body.Permissions,
		})
	}))
	defer server.Close()

	appCfg := testAgentAppConfig(t)
	source, err := github.NewTokenSourceWithBaseURL(appCfg, server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}
	// Put the source where the provider reads it.
	ghAppTokenCache.once.Do(func() { ghAppTokenCache.source = source })

	ctx := context.Background()
	full, err := source.Token(ctx)
	if err != nil || full != fullToken {
		t.Fatalf("source.Token() = (%q, %v), want the full token first", full, err)
	}

	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{GitHub: &github.Config{
			Enabled: true,
			Token:   "config-token",
			Repo:    "qf-studio/pilot",
			App:     appCfg,
		}},
		Projects: []*config.ProjectConfig{
			{Name: "widget", GitHub: &config.ProjectGitHubConfig{Owner: "acme", Repo: "widget"}},
		},
	}
	provider := agentReadTokenProvider(cfg)
	if provider == nil {
		t.Fatal("agentReadTokenProvider() = nil, want a provider with a GitHub App configured")
	}
	scopedCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(scopedReqs)
	}
	wantPerms := map[string]string{"contents": "read", "issues": "read", "pull_requests": "read", "metadata": "read"}

	for _, tc := range []struct{ repo, name string }{
		{"qf-studio/pilot", "pilot"},
		{"QF-Studio/Pilot", "pilot"}, // the name of the config entry, not of the argument
		{"acme/widget", "widget"},
	} {
		before := scopedCount()
		token, expires, err := provider(ctx, tc.repo)
		if err != nil {
			t.Fatalf("provider(%q) error = %v", tc.repo, err)
		}
		if token == full {
			t.Fatalf("provider(%q) returned the full token of the source", tc.repo)
		}
		if token != scopedToken {
			t.Errorf("provider(%q) token = %q, want the scoped token", tc.repo, token)
		}
		if time.Until(expires) < 50*time.Minute {
			t.Errorf("provider(%q) expires = %v, want ~1h", tc.repo, expires)
		}
		if got := scopedCount(); got != before+1 {
			t.Fatalf("provider(%q) made %d scoped requests, want 1", tc.repo, got-before)
		}
		mu.Lock()
		body := scopedReqs[len(scopedReqs)-1]
		mu.Unlock()
		if !reflect.DeepEqual(body.Repositories, []string{tc.name}) {
			t.Errorf("provider(%q) posted repositories %v, want [%s]", tc.repo, body.Repositories, tc.name)
		}
		if !reflect.DeepEqual(body.Permissions, wantPerms) {
			t.Errorf("provider(%q) posted permissions %v, want %v", tc.repo, body.Permissions, wantPerms)
		}
	}

	// A failed scoped call gives an error, not the full token.
	failScoped.Store(true)
	before := scopedCount()
	token, _, err := provider(ctx, "qf-studio/pilot")
	if err == nil {
		t.Error("provider() after a 500 = nil error, want the error of the scoped call")
	}
	if token != "" {
		t.Errorf("provider() after a 500 token = %q, want empty", token)
	}
	if got := scopedCount(); got != before+1 {
		t.Errorf("provider() after a 500 made %d scoped requests, want 1", got-before)
	}
	failScoped.Store(false)

	// A repository outside the config gives an error and no request.
	for _, repo := range []string{
		"qf-studio/other", // not in the config
		"evil/pilot",      // another owner, a configured repository name
		"evil/widget",
		"pilot",                 // no owner
		"",                      // empty
		"/pilot",                // empty owner
		"qf-studio/",            // empty name
		"qf-studio/pilot/extra", // nested path
		" qf-studio/pilot",      // space
	} {
		before := scopedCount()
		token, _, err := provider(ctx, repo)
		if err == nil {
			t.Errorf("provider(%q) = nil error, want a refusal", repo)
		}
		if token != "" {
			t.Errorf("provider(%q) token = %q, want empty", repo, token)
		}
		if got := scopedCount(); got != before {
			t.Errorf("provider(%q) made %d scoped requests, want 0", repo, got-before)
		}
	}
	if n := atomic.LoadInt64(&fullMints); n != 1 {
		t.Errorf("full mints = %d, want 1 (only the one made by the test)", n)
	}
}

// TestScopedTokenChecksTheOwnerOfTheAnswer: the request to GitHub names the
// repository with no owner, so only the full_name of the answer shows that
// the token is for the repository of the owner asked for.
func TestScopedTokenChecksTheOwnerOfTheAnswer(t *testing.T) {
	resetGitHubTokenTestState(t)

	// answerFor is the full_name the server puts in its answer.
	var answerFor atomic.Value
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		fullName, _ := answerFor.Load().(string)
		_, name, _ := strings.Cut(fullName, "/")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":                "test-scoped-read-token",
			"expires_at":           time.Now().Add(time.Hour),
			"repository_selection": "selected",
			"repositories":         []map[string]string{{"name": name, "full_name": fullName}},
			"permissions":          github.ReadOnlyPermissions(),
		})
	}))
	defer server.Close()

	appCfg := testAgentAppConfig(t)
	source, err := github.NewTokenSourceWithBaseURL(appCfg, server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}
	ctx := context.Background()
	read := github.ReadOnlyPermissions()

	// The right name, the full_name of another owner.
	answerFor.Store("owner-a/name")
	token, expires, err := source.ScopedToken(ctx, "owner-b", "name", read)
	if err == nil {
		t.Error("ScopedToken(owner-b, name) with an answer for owner-a/name = nil error, want a refusal")
	}
	if token != "" || !expires.IsZero() {
		t.Errorf("ScopedToken(owner-b, name) = (%q, %v), want no token", token, expires)
	}

	// The right full_name in another letter case.
	answerFor.Store("Owner-B/Name")
	if token, _, err := source.ScopedToken(ctx, "owner-b", "name", read); err != nil || token != "test-scoped-read-token" {
		t.Errorf("ScopedToken(owner-b, name) with an answer for Owner-B/Name = (%q, %v), want the token", token, err)
	}

	// The provider for the config entry OWNER-B/NAME with an answer for
	// OWNER-A/NAME.
	ghAppTokenCache.once.Do(func() { ghAppTokenCache.source = source })
	cfg := &config.Config{Adapters: &config.AdaptersConfig{GitHub: &github.Config{
		Enabled: true,
		Repo:    "OWNER-B/NAME",
		App:     appCfg,
	}}}
	provider := agentReadTokenProvider(cfg)
	if provider == nil {
		t.Fatal("agentReadTokenProvider() = nil, want a provider with a GitHub App configured")
	}
	answerFor.Store("OWNER-A/NAME")
	before := atomic.LoadInt64(&requests)
	token, _, err = provider(ctx, "OWNER-B/NAME")
	if err == nil {
		t.Error("provider(OWNER-B/NAME) with an answer for OWNER-A/NAME = nil error, want a refusal")
	}
	if token != "" {
		t.Errorf("provider(OWNER-B/NAME) token = %q, want empty", token)
	}
	if n := atomic.LoadInt64(&requests) - before; n != 1 {
		t.Errorf("provider(OWNER-B/NAME) made %d requests, want 1 (the refusal is of the answer)", n)
	}
}
