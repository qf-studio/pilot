package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

const (
	// installationTokenRefreshMargin is how long before the actual token
	// expiry TokenSource proactively mints a replacement, so Token() never
	// hands out a token within this window of expiring (GH-4743 acceptance:
	// "refresh proactively before the ~1h expiry").
	installationTokenRefreshMargin = 5 * time.Minute

	// appJWTLifetime is how long the App-level JWT used to request an
	// installation token is valid for. GitHub caps this at 10 minutes;
	// stay comfortably under that.
	appJWTLifetime = 8 * time.Minute

	// appJWTClockSkew backdates the JWT's iat by this much, per GitHub's
	// documented guidance, to tolerate clock drift between this host and
	// GitHub's servers.
	appJWTClockSkew = 60 * time.Second

	// mintRetryBackoff is how long TokenSource waits after a failed mint
	// attempt before allowing another network call. Callers arriving within
	// this window of a failure are answered immediately from the cached
	// error instead of each dialing GitHub and paying the mint HTTP timeout
	// (GH-4754 acceptance #3: mint-outage serialization collapsed
	// process-wide GitHub throughput to ~4 req/min when every caller queued
	// behind TokenSource.mu and each paid up to the 15s mint timeout).
	mintRetryBackoff = 10 * time.Second

	// mintFailureLogInterval throttles mint-failure log lines so a sustained
	// outage produces one line per interval instead of one per caller
	// (GH-4754 acceptance #3: "ERROR logs rate-limited").
	mintFailureLogInterval = 60 * time.Second
)

// TokenSource mints, caches, and proactively refreshes a GitHub App
// installation access token (GH-4743). It is safe for concurrent use — all
// callers share one cached token and a mint in flight is not duplicated.
type TokenSource struct {
	appID          int64
	installationID int64
	privateKey     *rsa.PrivateKey
	httpClient     *http.Client
	baseURL        string // overridable for tests

	mu        sync.Mutex
	token     string
	expiresAt time.Time

	// mint-outage coordination (GH-4754): inFlight lets concurrent callers
	// share a single in-progress mint HTTP call instead of each dialing
	// GitHub; lastMintErr/lastMintAt implement the post-failure backoff so a
	// caller arriving shortly after a failure is answered from the cached
	// error rather than paying the mint timeout again; lastLogAt throttles
	// the mint-failure log line to at most one per mintFailureLogInterval.
	inFlight    *mintCall
	lastMintErr error
	lastMintAt  time.Time
	lastLogAt   time.Time
}

// mintCall represents one in-flight (or just-completed) mint HTTP call
// shared by every TokenSource.Token caller that arrives while it is running.
type mintCall struct {
	done      chan struct{}
	token     string
	expiresAt time.Time
	err       error
}

// NewTokenSource builds a TokenSource from an AppConfig, reading and parsing
// the PEM private key at cfg.PrivateKeyPath. It returns an error naming
// exactly what failed (missing/unreadable file vs. unparseable key) since a
// bad key path is a deploy-time mistake that should be loud, not a silent
// auth failure discovered later on the first API call.
func NewTokenSource(cfg *AppConfig) (*TokenSource, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(cfg.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("reading github app private key %q: %w", cfg.PrivateKeyPath, err)
	}
	key, err := parseRSAPrivateKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing github app private key %q: %w", cfg.PrivateKeyPath, err)
	}
	return &TokenSource{
		appID:          cfg.AppID,
		installationID: cfg.InstallationID,
		privateKey:     key,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		baseURL:        githubAPIURL,
	}, nil
}

// NewTokenSourceWithBaseURL is NewTokenSource with an overridable API base
// URL, for pointing tests at a fake token-mint endpoint instead of
// api.github.com.
func NewTokenSourceWithBaseURL(cfg *AppConfig, baseURL string) (*TokenSource, error) {
	ts, err := NewTokenSource(cfg)
	if err != nil {
		return nil, err
	}
	ts.baseURL = baseURL
	return ts, nil
}

// parseRSAPrivateKey accepts both PKCS1 ("BEGIN RSA PRIVATE KEY") and PKCS8
// ("BEGIN PRIVATE KEY") PEM encodings — GitHub's App private-key download is
// PKCS1, but operators sometimes re-encode it, so support both rather than
// failing on a harmless format difference.
func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("not a PKCS1 or PKCS8 RSA private key: %w", err)
	}
	rsaKey, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not RSA (got %T)", keyAny)
	}
	return rsaKey, nil
}

// Token returns a currently-valid installation access token, minting (or
// refreshing) one if the cached token is empty or within
// installationTokenRefreshMargin of expiring. Never logs the token or the
// signing JWT — callers must do the same (GH-4743: zero token material in
// logs/argv).
//
// GH-4754: a mint failure while the cached token is still within the
// refresh margin (but not yet actually expired) serves the still-valid
// cached token with a WARN log instead of erroring — an error here used to
// make resolveGitHubToken silently fail over to a different credential
// identity (config PAT/env/gh-CLI) on every request during a transient mint
// blip, flapping between rate pools and actor identities. A mint failure
// with nothing valid cached (empty, or actually past expiresAt) still
// returns the error.
func (ts *TokenSource) Token(ctx context.Context) (string, error) {
	ts.mu.Lock()
	if ts.token != "" && time.Until(ts.expiresAt) > installationTokenRefreshMargin {
		tok := ts.token
		ts.mu.Unlock()
		return tok, nil
	}
	stillValid := ts.token != "" && time.Until(ts.expiresAt) > 0
	cachedToken := ts.token
	cachedExpiresAt := ts.expiresAt
	ts.mu.Unlock()

	token, expiresAt, err := ts.mintOnce(ctx)
	if err == nil {
		ts.mu.Lock()
		ts.token = token
		ts.expiresAt = expiresAt
		ts.mu.Unlock()
		return token, nil
	}

	if ts.shouldLog() {
		log := logging.WithComponent("github-auth").With(
			slog.Int64("app_id", ts.appID),
			slog.Int64("installation_id", ts.installationID),
			slog.Any("error", err),
		)
		if stillValid {
			log.Warn("github app installation token refresh failed within the refresh margin — serving cached token",
				slog.Time("cached_expires_at", cachedExpiresAt))
		} else {
			log.Error("github app installation token mint failed")
		}
	}

	if stillValid {
		return cachedToken, nil
	}
	return "", err
}

// shouldLog reports whether a mint-failure log line should be emitted now,
// throttling to at most one per mintFailureLogInterval so a sustained outage
// (or many concurrent callers each hitting a fresh Token() failure) doesn't
// flood the log with one line per caller (GH-4754 acceptance #3).
func (ts *TokenSource) shouldLog() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if time.Since(ts.lastLogAt) < mintFailureLogInterval {
		return false
	}
	ts.lastLogAt = time.Now()
	return true
}

// Invalidate discards the cached token so the next Token() call re-mints
// unconditionally, regardless of the cached expiry. Callers use this when
// GitHub answers a request with 401 even though TokenSource still considers
// the token valid — evidence GitHub revoked it early (App private-key
// rotation, installation suspend/reinstall). Without this, Token() would
// keep serving the revoked token verbatim until its cached expiresAt, up to
// ~55 minutes of hard 401s (GH-4754 acceptance #1).
func (ts *TokenSource) Invalidate() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.token = ""
	ts.expiresAt = time.Time{}
}

// mintOnce performs a single-flight mint: concurrent callers arriving while
// a mint HTTP call is already in progress share its result instead of each
// dialing GitHub, and a caller arriving within mintRetryBackoff of the last
// completed *failure* is answered immediately from that cached error
// without making a network call at all. Together these keep a mint outage
// from serializing every GitHub request behind repeated 15s mint timeouts
// (GH-4754 acceptance #3).
func (ts *TokenSource) mintOnce(ctx context.Context) (string, time.Time, error) {
	ts.mu.Lock()
	if ts.inFlight != nil {
		call := ts.inFlight
		ts.mu.Unlock()
		select {
		case <-call.done:
			return call.token, call.expiresAt, call.err
		case <-ctx.Done():
			return "", time.Time{}, ctx.Err()
		}
	}
	if ts.lastMintErr != nil && time.Since(ts.lastMintAt) < mintRetryBackoff {
		err := ts.lastMintErr
		ts.mu.Unlock()
		return "", time.Time{}, err
	}
	call := &mintCall{done: make(chan struct{})}
	ts.inFlight = call
	ts.mu.Unlock()

	token, expiresAt, err := ts.mint(ctx)

	ts.mu.Lock()
	ts.inFlight = nil
	ts.lastMintAt = time.Now()
	ts.lastMintErr = err
	ts.mu.Unlock()

	call.token, call.expiresAt, call.err = token, expiresAt, err
	close(call.done)

	return token, expiresAt, err
}

// mintResponse is the body of POST /app/installations/{id}/access_tokens.
type mintResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// mint requests a fresh installation access token from GitHub.
func (ts *TokenSource) mint(ctx context.Context) (string, time.Time, error) {
	appJWT, err := ts.signAppJWT()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("signing github app JWT: %w", err)
	}

	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", ts.baseURL, ts.installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("building installation token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+appJWT)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := ts.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("minting installation token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("reading installation token response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		// GitHub's error bodies are JSON messages ("Bad credentials" etc.)
		// and never echo the request's JWT/token back — safe to include.
		return "", time.Time{}, fmt.Errorf("minting installation token: status %d: %s", resp.StatusCode, string(body))
	}

	var parsed mintResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", time.Time{}, fmt.Errorf("parsing installation token response: %w", err)
	}
	if parsed.Token == "" {
		return "", time.Time{}, fmt.Errorf("installation token response had no token field")
	}
	return parsed.Token, parsed.ExpiresAt, nil
}

// signAppJWT builds and RS256-signs the App-level JWT GitHub requires to
// mint installation tokens. See:
// https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app
func (ts *TokenSource) signAppJWT() (string, error) {
	now := time.Now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]int64{
		"iat": now.Add(-appJWTClockSkew).Unix(),
		"exp": now.Add(appJWTLifetime).Unix(),
		"iss": ts.appID,
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)

	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, ts.privateKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("signing JWT: %w", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
