package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/redact"
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
		httpClient:     &http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirect},
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

// noRedirect stops the client at the first redirect answer. A redirect would
// carry the App JWT of the mint request to wherever the answer points; the
// redirect answer itself is then a non-201 status and an error.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

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
// The full mint reads only the token and its expiry; ScopedToken also reads
// what GitHub granted.
type mintResponse struct {
	Token               string            `json:"token"`
	ExpiresAt           time.Time         `json:"expires_at"`
	Permissions         map[string]string `json:"permissions,omitempty"`
	RepositorySelection string            `json:"repository_selection,omitempty"`
	Repositories        []mintRepository  `json:"repositories,omitempty"`
}

// mintRepository is one entry of the repositories list of a mint answer.
// FullName is "OWNER/NAME".
type mintRepository struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
}

// mint requests a fresh installation access token from GitHub. The request
// has no body, so the token carries every permission of the installation for
// every repository of it.
func (ts *TokenSource) mint(ctx context.Context) (string, time.Time, error) {
	parsed, err := ts.postAccessToken(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	return parsed.Token, parsed.ExpiresAt, nil
}

// mintStatusError is a non-201 answer of the access token endpoint. body is
// the response text with the App JWT already masked.
type mintStatusError struct {
	status int
	body   string
}

func (e *mintStatusError) Error() string {
	return fmt.Sprintf("minting installation token: status %d: %s", e.status, answerText(e.body))
}

// postAccessToken posts payload (nil for no body) to the access token
// endpoint of the installation and returns the answer of GitHub. The client
// follows no redirect.
func (ts *TokenSource) postAccessToken(ctx context.Context, payload []byte) (*mintResponse, error) {
	appJWT, err := ts.signAppJWT()
	if err != nil {
		return nil, fmt.Errorf("signing github app JWT: %w", err)
	}

	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", ts.baseURL, ts.installationID)
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("building installation token request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+appJWT)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := ts.httpClient.Do(req)
	if err != nil {
		// The App JWT is the credential of this request; a transport error
		// can carry the request it failed on.
		return nil, fmt.Errorf("minting installation token: %w", redact.Error(err, appJWT))
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading installation token response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		// GitHub's own error bodies are JSON messages ("Bad credentials"
		// etc.), but a proxy or an intercepting server can echo the request
		// back, so the JWT is masked rather than assumed absent.
		return nil, &mintStatusError{status: resp.StatusCode, body: redact.Text(string(respBody), appJWT)}
	}

	var parsed mintResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parsing installation token response: %w", err)
	}
	if parsed.Token == "" {
		return nil, fmt.Errorf("installation token response had no token field")
	}
	return &parsed, nil
}

// permissionRead is the one permission level ScopedToken mints.
const permissionRead = "read"

// ReadOnlyPermissions is the permission set of a read token for one
// repository: the agent of a run can read the code, the issues and the pull
// requests, and nothing else (TASK-651). Each call returns a new map.
func ReadOnlyPermissions() map[string]string {
	return map[string]string{
		"contents":      permissionRead,
		"issues":        permissionRead,
		"pull_requests": permissionRead,
		"metadata":      permissionRead,
	}
}

// scopedMintRequest is the body of POST /app/installations/{id}/access_tokens
// that narrows the token to the named repositories and permissions. See:
// https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app
type scopedMintRequest struct {
	Repositories []string          `json:"repositories"`
	Permissions  map[string]string `json:"permissions"`
}

// mintErrorMessageMax is the length, in characters, to which a text of an
// answer of GitHub is cut in an error value, so an answer with a long body
// cannot fill a caller's log.
const mintErrorMessageMax = 200

// Limits of a scoped mint. GitHub names an owner or a repository with at most
// 100 characters; an installation token lives one hour, so an expiry later
// than scopedTokenMaxLifetime is not an answer of GitHub. The token goes into
// a file and an environment value, so its length is bounded as well.
const (
	repoNameMaxLen         = 100
	scopedTokenMinLen      = 20
	scopedTokenMaxLen      = 255
	scopedTokenMaxLifetime = 90 * time.Minute
)

// ScopedToken mints an installation token that can only read the repository
// owner/name, with the permissions in perms. The owner and the name each hold
// 1 to repoNameMaxLen ASCII letters, digits, '.', '-' and '_', and start with
// neither '-' nor '.'. The request names the repository with no owner — the
// installation fixes the owner — so the answer must show that the owner of
// the installation is owner. Each key of perms must be a key of
// ReadOnlyPermissions, each value must be "read" and perms must not be empty
// — an empty set would let GitHub grant every permission of the
// installation. Any other input is an error without a request.
//
// The answer of GitHub is checked as well: it must select exactly the one
// repository asked for by its full name, grant only "read" on keys of perms,
// carry a token of printable ASCII and expire within scopedTokenMaxLifetime.
// Any other answer is an error and no token. The request follows no
// redirect.
//
// Decision: the scoped token bypasses the cache, the single-flight mint and
// the failure backoff of Token. Those exist for the one shared full token;
// a scoped token belongs to one caller and one repository, and storing it in
// ts.token would hand a read-only token to each caller of Token.
func (ts *TokenSource) ScopedToken(ctx context.Context, owner, name string, perms map[string]string) (string, time.Time, error) {
	if !validRepoName(owner) {
		return "", time.Time{}, fmt.Errorf("scoped installation token: owner %q is not a GitHub owner name", owner)
	}
	if !validRepoName(name) {
		return "", time.Time{}, fmt.Errorf("scoped installation token: repository %q is not a repository name with no owner", name)
	}
	if len(perms) == 0 {
		return "", time.Time{}, errors.New("scoped installation token: no permissions given")
	}
	allowed := ReadOnlyPermissions()
	for perm, level := range perms {
		if _, ok := allowed[perm]; !ok {
			return "", time.Time{}, fmt.Errorf("scoped installation token: permission %q refused, it is not a read-only permission", perm)
		}
		if level != permissionRead {
			return "", time.Time{}, fmt.Errorf("scoped installation token: permission %s=%q refused, only %q is allowed", perm, level, permissionRead)
		}
	}
	fullName := owner + "/" + name

	payload, err := json.Marshal(scopedMintRequest{Repositories: []string{name}, Permissions: perms})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("encoding scoped installation token request: %w", err)
	}

	parsed, err := ts.postAccessToken(ctx, payload)
	if err != nil {
		// The error text of the full mint keeps the response body; here only
		// the status and GitHub's message field are kept, so a body that
		// carries a token never reaches a caller's log.
		var statusErr *mintStatusError
		if errors.As(err, &statusErr) {
			return "", time.Time{}, fmt.Errorf("minting scoped installation token for %s: status %d%s", fullName, statusErr.status, mintErrorMessage(statusErr.body))
		}
		return "", time.Time{}, fmt.Errorf("minting scoped installation token for %s: %w", fullName, err)
	}
	if err := checkScopedAnswer(parsed, fullName, perms, time.Now()); err != nil {
		return "", time.Time{}, fmt.Errorf("minting scoped installation token for %s: %w", fullName, err)
	}
	return parsed.Token, parsed.ExpiresAt, nil
}

// validRepoName reports whether s is a GitHub owner or repository name: 1 to
// repoNameMaxLen ASCII letters, digits, '.', '-' and '_', not starting with
// '-' or '.' (so neither "." nor "..").
func validRepoName(s string) bool {
	if s == "" || len(s) > repoNameMaxLen || s[0] == '-' || s[0] == '.' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// validScopedToken reports whether token is scopedTokenMinLen to
// scopedTokenMaxLen printable ASCII characters with no space.
func validScopedToken(token string) bool {
	if len(token) < scopedTokenMinLen || len(token) > scopedTokenMaxLen {
		return false
	}
	for i := 0; i < len(token); i++ {
		if token[i] <= ' ' || token[i] > '~' {
			return false
		}
	}
	return true
}

// checkScopedAnswer refuses a mint answer that is wider or weaker than the
// read token for fullName ("OWNER/NAME") with perms that was asked for. The
// token is never part of the error text, and each other text of the answer
// passes through answerText.
func checkScopedAnswer(parsed *mintResponse, fullName string, perms map[string]string, now time.Time) error {
	if parsed.RepositorySelection != "selected" {
		return fmt.Errorf("answer has repository_selection %q, want \"selected\"", answerText(parsed.RepositorySelection))
	}
	if len(parsed.Repositories) != 1 || !strings.EqualFold(parsed.Repositories[0].FullName, fullName) {
		got := make([]string, 0, len(parsed.Repositories))
		for _, r := range parsed.Repositories {
			got = append(got, answerText(r.FullName))
		}
		return fmt.Errorf("answer has repositories %s, want exactly [%q]", answerText(fmt.Sprintf("%q", got)), fullName)
	}
	if len(parsed.Permissions) == 0 {
		return errors.New("answer names no permissions")
	}
	for perm, level := range parsed.Permissions {
		if _, asked := perms[perm]; !asked {
			return fmt.Errorf("answer grants %q, a permission that was not asked for", answerText(perm))
		}
		if level != permissionRead {
			return fmt.Errorf("answer grants %s=%q, want only %q", answerText(perm), answerText(level), permissionRead)
		}
	}
	if !validScopedToken(parsed.Token) {
		return fmt.Errorf("answer has a token that is not %d to %d printable ASCII characters with no space", scopedTokenMinLen, scopedTokenMaxLen)
	}
	if !parsed.ExpiresAt.After(now) {
		return fmt.Errorf("answer has expiry %v, want a time in the future", parsed.ExpiresAt)
	}
	if parsed.ExpiresAt.After(now.Add(scopedTokenMaxLifetime)) {
		return fmt.Errorf("answer has expiry %v, want at most %v from now", parsed.ExpiresAt, scopedTokenMaxLifetime)
	}
	return nil
}

// answerText makes a text of an answer of GitHub safe for an error value:
// cut to mintErrorMessageMax characters, with each character outside
// printable ASCII replaced by '?'.
func answerText(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == mintErrorMessageMax {
			break
		}
		if r < ' ' || r > '~' {
			r = '?'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// mintErrorMessage returns ": <message>" for a JSON error body of GitHub
// with a message field, passed through answerText, or "" for any other body.
func mintErrorMessage(body string) string {
	var parsed struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil || parsed.Message == "" {
		return ""
	}
	return ": " + answerText(parsed.Message)
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
