package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	requestTimeout   = 30 * time.Second
	maxResponseBytes = 8 << 20 // per response
	pageLimit        = 50      // Forgejo's default MAX_RESPONSE_ITEMS
	defaultPageCap   = 200
)

var (
	// ErrNotFound matches an API error with HTTP status 404.
	ErrNotFound = errors.New("forgejo: not found")
	// ErrAmbiguousPullRequest means several open PRs matched one head/base pair.
	ErrAmbiguousPullRequest = errors.New("forgejo: ambiguous pull request match")
	// ErrPullRequestClosed means the only matching PR was closed without merging.
	ErrPullRequestClosed = errors.New("forgejo: matching pull request was closed unmerged")
	// ErrPageCap means a listing exceeded the hard page cap and was not truncated silently.
	ErrPageCap = errors.New("forgejo: pagination page cap exceeded")
)

// APIError is a non-success HTTP status from the API. It carries no response body.
type APIError struct {
	Op     string
	Status int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("forgejo: %s: unexpected status %d", e.Op, e.Status)
}

// Is lets errors.Is(err, ErrNotFound) match a 404.
func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

// Retryable reports whether the failure is plausibly transient (429 or 5xx).
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// Label is an issue label.
type Label struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// IssuePullRequest is the pull_request marker Forgejo attaches to PR-backed issues.
type IssuePullRequest struct {
	Merged  bool   `json:"merged"`
	HTMLURL string `json:"html_url"`
}

// Issue is the subset of a Forgejo issue the adapter needs.
type Issue struct {
	Number      int               `json:"number"`
	Title       string            `json:"title"`
	Body        string            `json:"body"`
	State       string            `json:"state"`
	Labels      []Label           `json:"labels"`
	PullRequest *IssuePullRequest `json:"pull_request,omitempty"`
}

// IsPullRequest reports whether the issue is backed by a pull request.
func (i Issue) IsPullRequest() bool { return i.PullRequest != nil }

// HasLabel reports whether the issue carries the named label (case-insensitive).
func (i Issue) HasLabel(name string) bool {
	for _, l := range i.Labels {
		if strings.EqualFold(l.Name, name) {
			return true
		}
	}
	return false
}

// PullRequest describes an exactly matched PR. A zero value means no match.
type PullRequest struct {
	Number int
	URL    string
	// Open is true for an open PR; Merged for a merged one. Both false on a
	// match means closed without merging.
	Open   bool
	Merged bool
}

// Client talks to one repository on one Forgejo instance.
type Client struct {
	api     url.URL // base ending in /api/v1
	token   string
	repo    string // exact "owner/name"
	owner   string
	name    string
	http    *http.Client
	pageCap int
}

// NewClient validates its inputs and returns a client for repo ("owner/name").
func NewClient(baseURL, token, repo string) (*Client, error) {
	u, err := parseBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if token == "" || strings.ContainsFunc(token, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return nil, errors.New("forgejo: token is empty or contains whitespace/control characters")
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = requestTimeout
	return &Client{
		api: *u, token: token, repo: repo, owner: owner, name: name, pageCap: defaultPageCap,
		http: &http.Client{
			Timeout:   requestTimeout,
			Transport: tr,
			// Never follow redirects: the credential must not leave the configured origin.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("forgejo: invalid base URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("forgejo: base URL must not contain userinfo, query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return nil, errors.New("forgejo: http base URL is only allowed for localhost, 127.0.0.1 or ::1")
		}
	default:
		return nil, errors.New("forgejo: base URL must use https")
	}
	p := strings.TrimRight(u.Path, "/")
	p = strings.TrimSuffix(p, "/api/v1")
	return &url.URL{Scheme: u.Scheme, Host: u.Host, Path: p + "/api/v1"}, nil
}

// isLoopbackHost mirrors the config policy: localhost, 127.0.0.1 or ::1 only.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && (addr == netip.AddrFrom4([4]byte{127, 0, 0, 1}) || addr == netip.IPv6Loopback())
}

func validName(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func splitRepo(repo string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || !validName(owner) || !validName(name) {
		return "", "", errors.New("forgejo: repo must be in owner/name form")
	}
	return owner, name, nil
}

func (c *Client) repoPath(suffix string) string {
	return "/repos/" + c.owner + "/" + c.name + suffix
}

// do performs one request and returns the body and headers of an expected status.
func (c *Client) do(ctx context.Context, op, method, path string, query url.Values, in any, ok ...int) ([]byte, http.Header, error) {
	u := c.api
	u.Path += path
	u.RawQuery = query.Encode()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, nil, fmt.Errorf("forgejo: %s: encode request: %w", op, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, nil, fmt.Errorf("forgejo: %s: build request: %w", op, c.scrub(err))
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "pilot-forgejo-adapter")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, nil, fmt.Errorf("forgejo: %s: request failed: %w", op, c.scrub(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("forgejo: %s: read response: %w", op, c.scrub(err))
	}
	if len(data) > maxResponseBytes {
		return nil, nil, fmt.Errorf("forgejo: %s: response too large (limit %d bytes)", op, maxResponseBytes)
	}
	for _, s := range ok {
		if resp.StatusCode == s {
			return data, resp.Header, nil
		}
	}
	return nil, nil, &APIError{Op: op, Status: resp.StatusCode}
}

// scrub removes the token from an error message, keeping the error unwrappable
// when no token is present.
func (c *Client) scrub(err error) error {
	if strings.Contains(err.Error(), c.token) {
		return errors.New(strings.ReplaceAll(err.Error(), c.token, "[redacted]"))
	}
	return err
}

func decode(op string, data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("forgejo: %s: decode response: malformed JSON", op)
	}
	return nil
}

// paged walks a list endpoint page by page, calling visit for every item. It
// stops on an empty page or when a Link header carries no rel="next"; exceeding the page cap is an error.
func paged[T any](ctx context.Context, c *Client, op, path string, q url.Values, visit func(T) error) error {
	for page := 1; page <= c.pageCap; page++ {
		query := url.Values{}
		for k, v := range q {
			query[k] = v
		}
		query.Set("page", strconv.Itoa(page))
		query.Set("limit", strconv.Itoa(pageLimit))
		data, hdr, err := c.do(ctx, op, http.MethodGet, path, query, nil, http.StatusOK)
		if err != nil {
			return err
		}
		var items []T
		if err := decode(op, data, &items); err != nil {
			return err
		}
		for _, it := range items {
			if err := visit(it); err != nil {
				return err
			}
		}
		if len(items) == 0 {
			return nil
		}
		// A Link header without rel="next" is the server saying this was the
		// last page. Without a Link header, keep going until an empty page:
		// the server may clamp the page size below the requested limit, so a
		// short page is not proof of the end.
		if link := hdr.Get("Link"); link != "" && !strings.Contains(link, `rel="next"`) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s (cap %d pages)", ErrPageCap, op, c.pageCap)
}

// GetIssue fetches one issue (or PR-backed issue; see Issue.IsPullRequest).
func (c *Client) GetIssue(ctx context.Context, number int) (Issue, error) {
	const op = "get issue"
	if number <= 0 {
		return Issue{}, errors.New("forgejo: invalid issue number")
	}
	data, _, err := c.do(ctx, op, http.MethodGet, c.repoPath("/issues/"+strconv.Itoa(number)), nil, nil, http.StatusOK)
	if err != nil {
		return Issue{}, err
	}
	var is Issue
	if err := decode(op, data, &is); err != nil {
		return Issue{}, err
	}
	if is.Number != number {
		return Issue{}, fmt.Errorf("forgejo: %s: response is for a different issue", op)
	}
	return is, nil
}

// DefaultBranch returns the repository's default branch.
func (c *Client) DefaultBranch(ctx context.Context) (string, error) {
	const op = "get repository"
	data, _, err := c.do(ctx, op, http.MethodGet, c.repoPath(""), nil, nil, http.StatusOK)
	if err != nil {
		return "", err
	}
	var r struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := decode(op, data, &r); err != nil {
		return "", err
	}
	if r.DefaultBranch == "" {
		return "", fmt.Errorf("forgejo: %s: no default branch", op)
	}
	return r.DefaultBranch, nil
}

// ListOpenIssues calls visit for every open, non-PR issue carrying label,
// across all pages. A visit error stops the walk and is returned.
func (c *Client) ListOpenIssues(ctx context.Context, label string, visit func(Issue) error) error {
	if label == "" {
		return errors.New("forgejo: label is required")
	}
	if visit == nil {
		return errors.New("forgejo: visit callback is required")
	}
	q := url.Values{"state": {"open"}, "type": {"issues"}, "labels": {label}}
	seen := map[int]bool{}
	return paged(ctx, c, "list issues", c.repoPath("/issues"), q, func(is Issue) error {
		if is.IsPullRequest() || is.State == "closed" || seen[is.Number] {
			return nil
		}
		seen[is.Number] = true
		return visit(is)
	})
}

type prRef struct {
	Ref  string `json:"ref"`
	Repo *struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
}

func (r prRef) matches(repo, ref string) bool {
	return r.Repo != nil && r.Repo.FullName == repo && r.Ref == ref
}

type apiPull struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	Head    prRef  `json:"head"`
	Base    prRef  `json:"base"`
}

// FindPullRequest looks through all PRs (any state) for one whose head is
// branch head of this repository and whose base is branch base of this
// repository. No match returns the zero PullRequest and a nil error. A match
// prefers open over merged over closed-unmerged; several open matches are an
// error, never a silent pick.
func (c *Client) FindPullRequest(ctx context.Context, head, base string) (PullRequest, error) {
	if head == "" || base == "" {
		return PullRequest{}, errors.New("forgejo: head and base branches are required")
	}
	var open []PullRequest
	var merged, closed *PullRequest
	err := paged(ctx, c, "list pull requests", c.repoPath("/pulls"), url.Values{"state": {"all"}}, func(p apiPull) error {
		if !p.Head.matches(c.repo, head) || !p.Base.matches(c.repo, base) {
			return nil
		}
		pr := PullRequest{Number: p.Number, URL: p.HTMLURL, Merged: p.Merged, Open: p.State == "open" && !p.Merged}
		switch {
		case pr.Open:
			open = append(open, pr)
		case pr.Merged:
			if merged == nil {
				merged = &pr
			}
		default:
			if closed == nil || pr.Number > closed.Number {
				closed = &pr
			}
		}
		return nil
	})
	if err != nil {
		return PullRequest{}, err
	}
	switch {
	case len(open) > 1:
		return PullRequest{}, ErrAmbiguousPullRequest
	case len(open) == 1:
		return open[0], nil
	case merged != nil:
		return *merged, nil
	case closed != nil:
		return *closed, nil
	}
	return PullRequest{}, nil
}

// CreatePR opens a PR from source into target and returns its URL. An existing
// open or merged PR for the exact pair is returned instead of creating a new
// one; a lookup failure is an error, never treated as absence. A conflict
// answer from the server is recovered by looking the PR up again. A PR closed
// without merging is not recreated: ErrPullRequestClosed is returned.
func (c *Client) CreatePR(ctx context.Context, source, target, title, body string) (string, error) {
	const op = "create pull request"
	if source == "" || target == "" || source == target {
		return "", errors.New("forgejo: source and target must be distinct non-empty branches")
	}
	if title == "" {
		return "", errors.New("forgejo: title is required")
	}
	existing, err := c.FindPullRequest(ctx, source, target)
	if err != nil {
		return "", err
	}
	if u, done, err := existingResult(existing); done {
		return u, err
	}
	payload := map[string]string{"title": title, "body": body, "head": source, "base": target}
	data, _, err := c.do(ctx, op, http.MethodPost, c.repoPath("/pulls"), nil, payload, http.StatusCreated)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusConflict {
			again, lerr := c.FindPullRequest(ctx, source, target)
			if lerr != nil {
				return "", lerr
			}
			if u, done, rerr := existingResult(again); done {
				return u, rerr
			}
		}
		return "", err
	}
	var pr apiPull
	if err := decode(op, data, &pr); err != nil {
		return "", err
	}
	if pr.HTMLURL == "" {
		return "", fmt.Errorf("forgejo: %s: response has no URL", op)
	}
	return pr.HTMLURL, nil
}

// existingResult maps a lookup result to a CreatePR outcome; done is false when
// there was no match.
func existingResult(pr PullRequest) (prURL string, done bool, err error) {
	switch {
	case pr.URL == "":
		return "", false, nil
	case pr.Open || pr.Merged:
		return pr.URL, true, nil
	default:
		return "", true, fmt.Errorf("%w: %s", ErrPullRequestClosed, pr.URL)
	}
}

// AddComment posts a comment on an issue.
func (c *Client) AddComment(ctx context.Context, number int, body string) error {
	if number <= 0 {
		return errors.New("forgejo: invalid issue number")
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("forgejo: comment body is required")
	}
	_, _, err := c.do(ctx, "add comment", http.MethodPost, c.repoPath("/issues/"+strconv.Itoa(number)+"/comments"),
		nil, map[string]string{"body": body}, http.StatusCreated)
	return err
}
