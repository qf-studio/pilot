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
	"net/http"
	"os"
	"sync"
	"time"
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
func (ts *TokenSource) Token(ctx context.Context) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.token != "" && time.Until(ts.expiresAt) > installationTokenRefreshMargin {
		return ts.token, nil
	}

	token, expiresAt, err := ts.mint(ctx)
	if err != nil {
		return "", err
	}
	ts.token = token
	ts.expiresAt = expiresAt
	return token, nil
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
