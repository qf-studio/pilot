package forgejo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// Fixtures in testdata/ follow the documented Gitea-compatible v1 shapes of
// Forgejo 11.0.x (swagger: Issue, Repository, PullRequest).

const (
	testToken = "test-forgejo-token"
	testRepo  = "fixturebot/pilot-fixture"
	rp        = "/api/v1/repos/fixturebot/pilot-fixture"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, testToken, testRepo)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

func TestNewClientValidation(t *testing.T) {
	cases := []struct {
		name, base, token, repo string
		ok                      bool
	}{
		{"https", "https://forge.example.com", testToken, testRepo, true},
		{"https subpath", "https://forge.example.com/git/", testToken, testRepo, true},
		{"loopback v4", "http://127.0.0.1:3000", testToken, testRepo, true},
		{"loopback v6", "http://[::1]:3000", testToken, testRepo, true},
		{"http remote", "http://forge.example.com", testToken, testRepo, false},
		{"http localhost name", "http://localhost:3000", testToken, testRepo, true},
		{"http lookalike localhost", "http://localhost.example.com", testToken, testRepo, false},
		{"http private ip", "http://10.0.0.5", testToken, testRepo, false},
		{"userinfo", "https://u:p@forge.example.com", testToken, testRepo, false},
		{"query", "https://forge.example.com?x=1", testToken, testRepo, false},
		{"fragment", "https://forge.example.com#x", testToken, testRepo, false},
		{"ftp", "ftp://forge.example.com", testToken, testRepo, false},
		{"no host", "https://", testToken, testRepo, false},
		{"empty token", "https://forge.example.com", "", testRepo, false},
		{"token whitespace", "https://forge.example.com", "a b", testRepo, false},
		{"repo no slash", "https://forge.example.com", testToken, "pilot", false},
		{"repo traversal", "https://forge.example.com", testToken, "../x", false},
		{"repo three parts", "https://forge.example.com", testToken, "a/b/c", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClient(tc.base, tc.token, tc.repo)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
	c, err := NewClient("https://forge.example.com/git/api/v1/", testToken, testRepo)
	if err != nil || c.api.String() != "https://forge.example.com/git/api/v1" {
		t.Fatalf("api = %v err=%v", c.api.String(), err)
	}
}

func TestEndpoints(t *testing.T) {
	var gotAuth, gotBody, gotMethod, gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotMethod, gotPath = r.Header.Get("Authorization"), r.Method, r.URL.Path
		b := new(strings.Builder)
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		b.Write(buf[:n])
		gotBody = b.String()
		switch {
		case r.URL.Path == rp+"/issues/3" && r.Method == "GET":
			writeJSON(w, 200, fixture(t, "issue.json"))
		case r.URL.Path == rp && r.Method == "GET":
			writeJSON(w, 200, fixture(t, "repo.json"))
		case r.URL.Path == rp+"/issues/3/comments" && r.Method == "POST":
			writeJSON(w, 201, `{"id":1,"body":"hi"}`)
		default:
			writeJSON(w, 404, `{"message":"secret-body-detail"}`)
		}
	})
	ctx := context.Background()

	is, err := c.GetIssue(ctx, 3)
	if err != nil || is.Number != 3 || is.Title != "Fix the thing" || is.State != "open" || is.Body != "Please fix." ||
		len(is.Labels) != 1 || is.Labels[0].Name != "pilot" || is.IsPullRequest() {
		t.Fatalf("issue = %+v err=%v", is, err)
	}
	if gotAuth != "token "+testToken {
		t.Fatalf("auth = %q", gotAuth)
	}
	if br, err := c.DefaultBranch(ctx); err != nil || br != "main" {
		t.Fatalf("branch = %q err=%v", br, err)
	}
	if err := c.AddComment(ctx, 3, "hello"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != rp+"/issues/3/comments" || gotBody != `{"body":"hello"}` {
		t.Fatalf("comment req: %s %s %s", gotMethod, gotPath, gotBody)
	}

	// 404 and no raw body leakage.
	_, err = c.GetIssue(ctx, 99)
	if !errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), "secret-body-detail") {
		t.Fatalf("404 err = %v", err)
	}
	if err := c.AddComment(ctx, 0, "x"); err == nil {
		t.Fatal("want error for number 0")
	}
}

func TestStatusErrors(t *testing.T) {
	for _, code := range []int{401, 403, 404, 500} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, code, `{"message":"body mentions `+testToken+`"}`)
		})
		for name, err := range map[string]error{
			"issue":  func() error { _, e := c.GetIssue(context.Background(), 1); return e }(),
			"branch": func() error { _, e := c.DefaultBranch(context.Background()); return e }(),
		} {
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != code || strings.Contains(err.Error(), testToken) {
				t.Fatalf("%s %d: err = %v", name, code, err)
			}
		}
	}
}

func TestTransportErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c, err := NewClient(url, testToken, testRepo)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.GetIssue(context.Background(), 1)
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err = %v", err)
	}
	if got := c.scrub(errors.New("x" + testToken + "y")).Error(); got != "x[redacted]y" {
		t.Fatalf("scrub = %q", got)
	}
}

func TestResponseBodyBounded(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"default_branch":"`))
		chunk := []byte(strings.Repeat("a", 1<<20))
		for i := 0; i < 9; i++ {
			_, _ = w.Write(chunk)
		}
		_, _ = w.Write([]byte(`"}`))
	})
	if _, err := c.DefaultBranch(context.Background()); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientCrossOriginRedirect(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		writeJSON(w, 200, fixture(t, "repo.json"))
	}))
	defer other.Close()
	for _, code := range []int{301, 302, 307, 308} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/steal", code)
		})
		if _, err := c.DefaultBranch(context.Background()); err == nil {
			t.Fatalf("%d: redirect followed", code)
		}
		if err := c.AddComment(context.Background(), 1, "x"); err == nil {
			t.Fatalf("%d: POST redirect followed", code)
		}
	}
	if leaked.Load() {
		t.Fatal("Authorization forwarded cross-origin")
	}
}

func issuesPage(from, n int, pr bool) string {
	var items []string
	for i := from; i < from+n; i++ {
		extra := ""
		if pr && i == from {
			extra = `,"pull_request":{"merged":false,"html_url":"x"}`
		}
		items = append(items, fmt.Sprintf(`{"number":%d,"title":"t%d","state":"open","labels":[{"name":"pilot"}]%s}`, i, i, extra))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func TestClientListIssuesPagination(t *testing.T) {
	var queries []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		q := r.URL.Query()
		if r.URL.Path != rp+"/issues" || q.Get("state") != "open" || q.Get("type") != "issues" || q.Get("labels") != "pilot" || q.Get("limit") != "50" {
			writeJSON(w, 400, `{}`)
			return
		}
		switch q.Get("page") {
		case "1":
			writeJSON(w, 200, issuesPage(1, 50, true)) // first one is PR-backed
		case "2":
			writeJSON(w, 200, issuesPage(51, 50, false))
		case "3":
			writeJSON(w, 200, issuesPage(101, 7, false))
		default:
			writeJSON(w, 200, `[]`)
		}
	})
	var seen []int
	err := c.ListOpenIssues(context.Background(), "pilot", func(is Issue) error { seen = append(seen, is.Number); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 49+50+7 || seen[0] != 2 || seen[len(seen)-1] != 107 {
		t.Fatalf("seen %d: first=%d last=%d", len(seen), seen[0], seen[len(seen)-1])
	}
	if len(queries) != 4 { // third page is short; the walk ends on the empty page 4
		t.Fatalf("queries = %v", queries)
	}
}

func TestClientListIssuesLinkHeaderEndsWalk(t *testing.T) {
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Link", `<http://x/?page=1>; rel="prev"`)
		writeJSON(w, 200, issuesPage(1, 3, false))
	})
	n := 0
	if err := c.ListOpenIssues(context.Background(), "pilot", func(Issue) error { n++; return nil }); err != nil || n != 3 || calls != 1 {
		t.Fatalf("n=%d calls=%d err=%v", n, calls, err)
	}
}

func TestClientListIssuesPageCap(t *testing.T) {
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Link", `<http://x/?page=2>; rel="next"`)
		writeJSON(w, 200, issuesPage(1, 1, false)) // never empty, always a next page
	})
	err := c.ListOpenIssues(context.Background(), "pilot", func(Issue) error { return nil })
	if !errors.Is(err, ErrPageCap) || calls != c.pageCap {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestClientListIssuesErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			writeJSON(w, 500, `{}`)
			return
		}
		writeJSON(w, 200, issuesPage(1, 2, false))
	})
	err := c.ListOpenIssues(context.Background(), "pilot", func(Issue) error { return nil })
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 500 {
		t.Fatalf("page 2 failure must surface: %v", err)
	}
	stop := errors.New("stop")
	if err := c.ListOpenIssues(context.Background(), "pilot", func(Issue) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("visit error: %v", err)
	}
	for _, label := range []string{"", "a,b"} {
		if err := c.ListOpenIssues(context.Background(), label, func(Issue) error { return nil }); err == nil {
			t.Fatalf("label %q accepted", label)
		}
	}
}

func pullJSON(state string, merged bool, headRepo, headRef, baseRepo, baseRef, url string) string {
	hr := "null"
	if headRepo != "" {
		hr = fmt.Sprintf(`{"full_name":%q}`, headRepo)
	}
	return fmt.Sprintf(`{"html_url":%q,"state":%q,"merged":%t,"head":{"ref":%q,"repo":%s},"base":{"ref":%q,"repo":{"full_name":%q}}}`,
		url, state, merged, headRef, hr, baseRef, baseRepo)
}

func TestFindPullRequest(t *testing.T) {
	const br, base = "pilot/GH-3", "main"
	other := pullJSON("open", false, testRepo, "other", testRepo, base, "https://f/other")
	cases := []struct {
		name    string
		pulls   []string
		want    PullRequest
		wantErr error
	}{
		{"none", []string{other}, PullRequest{}, nil},
		{"open", []string{other, pullJSON("open", false, testRepo, br, testRepo, base, "https://f/open")}, PullRequest{URL: "https://f/open", Open: true}, nil},
		{"merged", []string{pullJSON("closed", true, testRepo, br, testRepo, base, "https://f/m")}, PullRequest{URL: "https://f/m", Merged: true}, nil},
		{"closed unmerged", []string{pullJSON("closed", false, testRepo, br, testRepo, base, "https://f/c")}, PullRequest{URL: "https://f/c"}, nil},
		{"wrong head repo", []string{pullJSON("open", false, "evil/fork", br, testRepo, base, "https://f/x")}, PullRequest{}, nil},
		{"deleted head repo", []string{pullJSON("open", false, "", br, testRepo, base, "https://f/x")}, PullRequest{}, nil},
		{"wrong base repo", []string{pullJSON("open", false, testRepo, br, "evil/fork", base, "https://f/x")}, PullRequest{}, nil},
		{"wrong base ref", []string{pullJSON("open", false, testRepo, br, testRepo, "dev", "https://f/x")}, PullRequest{}, nil},
		{"case differs", []string{pullJSON("open", false, "FixtureBot/pilot-fixture", br, testRepo, base, "https://f/x")}, PullRequest{}, nil},
		{"open beats closed", []string{
			pullJSON("closed", false, testRepo, br, testRepo, base, "https://f/c"),
			pullJSON("open", false, testRepo, br, testRepo, base, "https://f/o")}, PullRequest{URL: "https://f/o", Open: true}, nil},
		{"two open ambiguous", []string{
			pullJSON("open", false, testRepo, br, testRepo, base, "https://f/1"),
			pullJSON("open", false, testRepo, br, testRepo, base, "https://f/2")}, PullRequest{}, ErrAmbiguousPullRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != rp+"/pulls" || r.URL.Query().Get("state") != "all" {
					writeJSON(w, 400, `{}`)
					return
				}
				if r.URL.Query().Get("page") == "1" {
					writeJSON(w, 200, "["+strings.Join(tc.pulls, ",")+"]")
					return
				}
				writeJSON(w, 200, `[]`)
			})
			got, err := c.FindPullRequest(context.Background(), br, base)
			if !errors.Is(err, tc.wantErr) || got != tc.want {
				t.Fatalf("got %+v err=%v", got, err)
			}
		})
	}
}

func TestFindPullRequestPaginatesAndErrors(t *testing.T) {
	const br, base = "pilot/GH-3", "main"
	fill := func(n int) string {
		var s []string
		for i := 0; i < n; i++ {
			s = append(s, pullJSON("open", false, testRepo, fmt.Sprintf("b%d", i), testRepo, base, "https://f/x"))
		}
		return "[" + strings.Join(s, ",") + "]"
	}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1":
			writeJSON(w, 200, fill(50))
		case "2":
			writeJSON(w, 200, "["+pullJSON("closed", true, testRepo, br, testRepo, base, "https://f/deep")+"]")
		default:
			writeJSON(w, 200, `[]`)
		}
	})
	got, err := c.FindPullRequest(context.Background(), br, base)
	if err != nil || got.URL != "https://f/deep" || !got.Merged {
		t.Fatalf("got %+v err=%v", got, err)
	}

	bad := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 500, `{}`) })
	if got, err := bad.FindPullRequest(context.Background(), br, base); err == nil || got.URL != "" {
		t.Fatalf("API error must be an error, not absence: %+v %v", got, err)
	}
	for _, in := range [][2]string{{"", base}, {br, ""}, {"owner:" + br, base}} {
		if _, err := bad.FindPullRequest(context.Background(), in[0], in[1]); err == nil {
			t.Fatalf("accepted %v", in)
		}
	}
}

type prServer struct {
	pulls []string // list result
	post  func(w http.ResponseWriter)
	posts int
	lists int
	body  string
}

func (p *prServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == rp+"/pulls":
			p.lists++
			if r.URL.Query().Get("page") == "1" {
				writeJSON(w, 200, "["+strings.Join(p.pulls, ",")+"]")
				return
			}
			writeJSON(w, 200, `[]`)
		case r.Method == "POST" && r.URL.Path == rp+"/pulls":
			p.posts++
			var m map[string]string
			_ = json.NewDecoder(r.Body).Decode(&m)
			p.body = fmt.Sprint(m["head"], "|", m["base"], "|", m["title"], "|", m["body"])
			p.post(w)
		default:
			writeJSON(w, 404, `{}`)
		}
	}
}

func TestCreatePR(t *testing.T) {
	const br, base = "pilot/GH-3", "main"
	open := pullJSON("open", false, testRepo, br, testRepo, base, "https://f/open")
	merged := pullJSON("closed", true, testRepo, br, testRepo, base, "https://f/merged")
	closed := pullJSON("closed", false, testRepo, br, testRepo, base, "https://f/closed")
	created := func(w http.ResponseWriter) { writeJSON(w, 201, fixture(t, "pull.json")) }

	t.Run("creates when none", func(t *testing.T) {
		ps := &prServer{post: created}
		c := newTestClient(t, ps.handler(t))
		url, err := c.CreatePR(context.Background(), br, base, "T", "B")
		if err != nil || url != "http://127.0.0.1:31957/fixturebot/pilot-fixture/pulls/7" || ps.posts != 1 || ps.body != "pilot/GH-3|main|T|B" {
			t.Fatalf("url=%q err=%v posts=%d body=%q", url, err, ps.posts, ps.body)
		}
	})
	for name, tc := range map[string]struct{ pull, url string }{"existing open": {open, "https://f/open"}, "existing merged": {merged, "https://f/merged"}} {
		t.Run(name, func(t *testing.T) {
			ps := &prServer{pulls: []string{tc.pull}, post: created}
			c := newTestClient(t, ps.handler(t))
			url, err := c.CreatePR(context.Background(), br, base, "T", "B")
			if err != nil || url != tc.url || ps.posts != 0 {
				t.Fatalf("url=%q err=%v posts=%d", url, err, ps.posts)
			}
		})
	}
	t.Run("closed unmerged is not recreated", func(t *testing.T) {
		ps := &prServer{pulls: []string{closed}, post: created}
		c := newTestClient(t, ps.handler(t))
		if _, err := c.CreatePR(context.Background(), br, base, "T", "B"); !errors.Is(err, ErrPullRequestClosed) || ps.posts != 0 {
			t.Fatalf("err=%v posts=%d", err, ps.posts)
		}
	})
	t.Run("lookup error never posts", func(t *testing.T) {
		posts := 0
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				posts++
			}
			writeJSON(w, 500, `{}`)
		})
		if _, err := c.CreatePR(context.Background(), br, base, "T", "B"); err == nil || posts != 0 {
			t.Fatalf("err=%v posts=%d", err, posts)
		}
	})
	t.Run("conflict recovers", func(t *testing.T) {
		ps := &prServer{post: func(w http.ResponseWriter) { writeJSON(w, 409, `{"message":"exists"}`) }}
		var gets int
		inner := ps.handler(t)
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				gets++
				if gets > 1 && r.URL.Query().Get("page") == "1" { // appears after the first lookup
					ps.pulls = []string{open}
				}
			}
			inner(w, r)
		})
		url, err := c.CreatePR(context.Background(), br, base, "T", "B")
		if err != nil || url != "https://f/open" || ps.posts != 1 {
			t.Fatalf("url=%q err=%v posts=%d", url, err, ps.posts)
		}
	})
	t.Run("conflict with lookup error does not recover", func(t *testing.T) {
		var gets int
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				writeJSON(w, 409, `{}`)
				return
			}
			gets++
			if gets == 1 {
				writeJSON(w, 200, `[]`)
				return
			}
			writeJSON(w, 500, `{}`)
		})
		_, err := c.CreatePR(context.Background(), br, base, "T", "B")
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != 500 {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("conflict without a match returns the conflict", func(t *testing.T) {
		ps := &prServer{post: func(w http.ResponseWriter) { writeJSON(w, 409, `{}`) }}
		c := newTestClient(t, ps.handler(t))
		_, err := c.CreatePR(context.Background(), br, base, "T", "B")
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != 409 {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("other statuses and empty url", func(t *testing.T) {
		for _, code := range []int{401, 404, 422, 500} {
			ps := &prServer{post: func(w http.ResponseWriter) { writeJSON(w, code, `{"message":"x"}`) }}
			c := newTestClient(t, ps.handler(t))
			if _, err := c.CreatePR(context.Background(), br, base, "T", "B"); err == nil {
				t.Fatalf("%d: want error", code)
			}
		}
		ps := &prServer{post: func(w http.ResponseWriter) { writeJSON(w, 201, `{}`) }}
		c := newTestClient(t, ps.handler(t))
		if _, err := c.CreatePR(context.Background(), br, base, "T", "B"); err == nil {
			t.Fatal("empty html_url accepted")
		}
	})
}

func TestContextCancel(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.DefaultBranch(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
