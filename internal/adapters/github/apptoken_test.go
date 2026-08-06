package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testAppPrivateKeyPath generates a throwaway RSA key, PEM-encodes it (PKCS1,
// matching GitHub's App private-key download format), writes it to a temp
// file, and returns the path. A real PEM is needed to exercise the actual
// RSA parsing/signing code path — there's no meaningful "fake constant" for
// a key, unlike a bearer token string.
func testAppPrivateKeyPath(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test RSA key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	path := filepath.Join(t.TempDir(), "test-github-app.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing test private key: %v", err)
	}
	return path
}

func testAppConfig(t *testing.T) *AppConfig {
	return &AppConfig{
		AppID:          123456,
		InstallationID: 78901234,
		PrivateKeyPath: testAppPrivateKeyPath(t),
	}
}

func TestNewTokenSource_MissingKeyFile(t *testing.T) {
	cfg := &AppConfig{AppID: 1, InstallationID: 2, PrivateKeyPath: "/nonexistent/path/key.pem"}
	_, err := NewTokenSource(cfg)
	if err == nil {
		t.Fatal("NewTokenSource() = nil error, want error naming the unreadable path")
	}
	if !strings.Contains(err.Error(), cfg.PrivateKeyPath) {
		t.Errorf("error %q does not name the private key path %q", err.Error(), cfg.PrivateKeyPath)
	}
}

func TestNewTokenSource_InvalidPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-key.pem")
	if err := os.WriteFile(path, []byte("this is not a PEM file"), 0o600); err != nil {
		t.Fatalf("writing bogus key file: %v", err)
	}
	cfg := &AppConfig{AppID: 1, InstallationID: 2, PrivateKeyPath: path}
	_, err := NewTokenSource(cfg)
	if err == nil {
		t.Fatal("NewTokenSource() = nil error, want error for invalid PEM")
	}
}

func TestNewTokenSource_InvalidAppConfig(t *testing.T) {
	cfg := &AppConfig{} // missing everything
	_, err := NewTokenSource(cfg)
	if err == nil {
		t.Fatal("NewTokenSource() = nil error, want validation error")
	}
	if !strings.Contains(err.Error(), "app_id") {
		t.Errorf("error %q does not name app_id as the first missing field", err.Error())
	}
}

// fakeTokenServer stands in for GitHub's
// POST /app/installations/{id}/access_tokens endpoint.
func fakeTokenServer(t *testing.T, expiresIn time.Duration, mintCount *int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("request missing Bearer auth header, got %q", auth)
		}
		jwt := strings.TrimPrefix(auth, "Bearer ")
		parts := strings.Split(jwt, ".")
		if len(parts) != 3 {
			t.Errorf("app JWT does not look like a JWT (want 3 dot-separated segments): %q", jwt)
		}

		atomic.AddInt64(mintCount, 1)
		resp := mintResponse{
			// Deliberately not a realistic-looking secret pattern —
			// see internal/testutil/tokens.go conventions.
			Token:     fmt.Sprintf("test-installation-token-%d", atomic.LoadInt64(mintCount)),
			ExpiresAt: time.Now().Add(expiresIn),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestTokenSource_Token_MintsAndCaches(t *testing.T) {
	var mintCount int64
	server := fakeTokenServer(t, time.Hour, &mintCount)
	defer server.Close()

	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}

	ctx := context.Background()
	tok1, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if tok1 == "" {
		t.Fatal("Token() returned empty token")
	}

	tok2, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("second Token() error = %v", err)
	}
	if tok2 != tok1 {
		t.Errorf("second Token() = %q, want cached %q", tok2, tok1)
	}
	if got := atomic.LoadInt64(&mintCount); got != 1 {
		t.Errorf("mint endpoint hit %d times, want 1 (second call should use the cache)", got)
	}
}

func TestTokenSource_Token_RefreshesNearExpiry(t *testing.T) {
	var mintCount int64
	// Expires in under the 5-minute refresh margin, so every Token() call
	// should re-mint rather than serve a soon-to-expire cached token.
	server := fakeTokenServer(t, 2*time.Minute, &mintCount)
	defer server.Close()

	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}

	ctx := context.Background()
	if _, err := ts.Token(ctx); err != nil {
		t.Fatalf("first Token() error = %v", err)
	}
	if _, err := ts.Token(ctx); err != nil {
		t.Fatalf("second Token() error = %v", err)
	}
	if got := atomic.LoadInt64(&mintCount); got != 2 {
		t.Errorf("mint endpoint hit %d times, want 2 (both calls within the refresh margin should re-mint)", got)
	}
}

func TestTokenSource_Token_MintFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer server.Close()

	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}

	_, err = ts.Token(context.Background())
	if err == nil {
		t.Fatal("Token() = nil error, want error from a 401 mint response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q does not surface the mint HTTP status", err.Error())
	}
}

// TestTokenSource_Invalidate_ForcesRemint is the GH-4754 acceptance #1
// regression test: a caller that learns (via a 401 on an otherwise-still-
// cached token) that GitHub revoked the token early must be able to force
// TokenSource to re-mint on the very next Token() call, even though the
// cached token's expiresAt says it's still fine.
func TestTokenSource_Invalidate_ForcesRemint(t *testing.T) {
	var mintCount int64
	server := fakeTokenServer(t, time.Hour, &mintCount)
	defer server.Close()

	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}

	ctx := context.Background()
	tok1, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("first Token() error = %v", err)
	}

	ts.Invalidate()

	tok2, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("Token() after Invalidate() error = %v", err)
	}
	if tok2 == tok1 {
		t.Errorf("Token() after Invalidate() = %q, same as pre-invalidate token %q — want a fresh mint", tok2, tok1)
	}
	if got := atomic.LoadInt64(&mintCount); got != 2 {
		t.Errorf("mint endpoint hit %d times, want 2 (Invalidate() must force a re-mint despite the cached token's expiresAt being an hour out)", got)
	}
}

// TestTokenSource_Token_ServesStaleWithinMarginOnMintFailure is the GH-4754
// acceptance #2 regression test: a proactive refresh attempted inside the
// refresh margin that fails must not error (which used to make
// resolveGitHubToken silently fail over to a different credential identity
// on every request) — it must keep serving the still-valid cached token.
func TestTokenSource_Token_ServesStaleWithinMarginOnMintFailure(t *testing.T) {
	var mintCount int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&mintCount, 1)
		if n == 1 {
			// First mint succeeds but with an expiry inside the refresh
			// margin, so the very next Token() call attempts a proactive
			// refresh rather than serving straight from cache.
			resp := mintResponse{
				Token:     "test-installation-token-1",
				ExpiresAt: time.Now().Add(2 * time.Minute),
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		// Every subsequent mint (the proactive refresh) fails.
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"mint temporarily unavailable"}`))
	}))
	defer server.Close()

	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}

	ctx := context.Background()
	tok1, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("first Token() error = %v", err)
	}

	tok2, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("Token() error = %v, want nil — a refresh-margin mint failure must serve the still-valid cached token instead of erroring", err)
	}
	if tok2 != tok1 {
		t.Errorf("Token() = %q, want the still-valid cached token %q served despite the refresh failure", tok2, tok1)
	}
}

// TestTokenSource_Token_MintOutageBackoff is the GH-4754 acceptance #3
// regression test: once a mint attempt has failed, a caller arriving shortly
// after must be answered from the cached failure instead of dialing GitHub
// (and paying the mint HTTP timeout) again — otherwise a sustained outage
// serializes every caller behind repeated full-timeout network calls.
func TestTokenSource_Token_MintOutageBackoff(t *testing.T) {
	var mintCount int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&mintCount, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"server error"}`))
	}))
	defer server.Close()

	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}

	ctx := context.Background()
	if _, err := ts.Token(ctx); err == nil {
		t.Fatal("first Token() = nil error, want error from mint failure")
	}
	if _, err := ts.Token(ctx); err == nil {
		t.Fatal("second Token() = nil error, want the cached failure surfaced again")
	}

	if got := atomic.LoadInt64(&mintCount); got != 1 {
		t.Errorf("mint endpoint hit %d times for 2 calls inside the backoff window, want 1 (second call must be answered from the cached failure, not dial the network again)", got)
	}
}

// TestTokenSource_Token_SingleFlightMint is the GH-4754 acceptance #3
// regression test for the concurrent-caller half of mint-outage
// serialization: N callers arriving while a mint is already in flight must
// share that one in-flight attempt (and its result) rather than each
// dialing GitHub independently.
func TestTokenSource_Token_SingleFlightMint(t *testing.T) {
	var mintCount int64
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&mintCount, 1)
		<-release // hold the response so every concurrent caller has time to queue up behind the single in-flight mint
		resp := mintResponse{
			Token:     "test-installation-token-shared",
			ExpiresAt: time.Now().Add(time.Hour),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ts, err := NewTokenSourceWithBaseURL(testAppConfig(t), server.URL)
	if err != nil {
		t.Fatalf("NewTokenSourceWithBaseURL() error = %v", err)
	}

	const n = 5
	var wg sync.WaitGroup
	tokens := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tokens[i], errs[i] = ts.Token(context.Background())
		}(i)
	}

	// Give every goroutine a chance to reach mintOnce and queue behind the
	// single in-flight call before letting the mint response through.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Token() [%d] error = %v", i, err)
		}
		if tokens[i] != tokens[0] {
			t.Errorf("Token() [%d] = %q, want shared result %q across all concurrent callers", i, tokens[i], tokens[0])
		}
	}
	if got := atomic.LoadInt64(&mintCount); got != 1 {
		t.Errorf("mint endpoint hit %d times for %d concurrent callers, want 1 (single-flight)", got, n)
	}
}

func TestSignAppJWT(t *testing.T) {
	ts, err := NewTokenSource(testAppConfig(t))
	if err != nil {
		t.Fatalf("NewTokenSource() error = %v", err)
	}

	jwt, err := ts.signAppJWT()
	if err != nil {
		t.Fatalf("signAppJWT() error = %v", err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("signAppJWT() = %q, want 3 dot-separated segments", jwt)
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decoding JWT claims segment: %v", err)
	}
	var claims map[string]int64
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatalf("unmarshaling JWT claims: %v", err)
	}
	if claims["iss"] != ts.appID {
		t.Errorf("JWT iss = %d, want %d", claims["iss"], ts.appID)
	}
	if claims["exp"] <= claims["iat"] {
		t.Errorf("JWT exp (%d) not after iat (%d)", claims["exp"], claims["iat"])
	}
}
