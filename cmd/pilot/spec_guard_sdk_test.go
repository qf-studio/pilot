package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/ghissue"
	"github.com/qf-studio/pilot/internal/testutil"
)

type specGuardFake struct {
	server             *httptest.Server
	mu                 sync.Mutex
	existing           []string // comment bodies returned by ListIssueComments
	labelsAdded        []string
	commentsAdded      []string
	freshIssueBody     string // JSON body returned for the single-issue GET (GH-4634 pre-escalation re-fetch)
	freshIssueStatus   int    // HTTP status for the single-issue GET; 0 means default 200
	listCommentsStatus int    // HTTP status for ListIssueComments; 0 means default 200
}

func newSpecGuardFake(existingComments ...string) *specGuardFake {
	f := &specGuardFake{existing: existingComments}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/comments"):
			f.mu.Lock()
			status := f.listCommentsStatus
			f.mu.Unlock()
			if status != 0 && status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			type c struct {
				Body string `json:"body"`
			}
			out := make([]c, len(f.existing))
			for i, b := range f.existing {
				out[i] = c{Body: b}
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.labelsAdded = append(f.labelsAdded, body.Labels...)
			f.mu.Unlock()
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			var body struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.commentsAdded = append(f.commentsAdded, body.Body)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"id":1}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/") && !strings.HasSuffix(r.URL.Path, "/comments"):
			// GH-4634: single-issue GET, used for the pre-escalation
			// re-fetch (and for parentResolver lookups, unused here).
			f.mu.Lock()
			status, body := f.freshIssueStatus, f.freshIssueBody
			f.mu.Unlock()
			if status != 0 && status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			if body == "" {
				body = "{}"
			}
			_, _ = w.Write([]byte(body))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	return f
}

// setFreshIssue configures the mocked pre-escalation single-issue GET
// (client.GetIssue, GH-4634) to return the given issue.
func (f *specGuardFake) setFreshIssue(t *testing.T, issue githubSDK.Issue) {
	t.Helper()
	b, err := json.Marshal(issue)
	if err != nil {
		t.Fatalf("marshal fresh issue: %v", err)
	}
	f.mu.Lock()
	f.freshIssueBody = string(b)
	f.mu.Unlock()
}

func TestApplySpecGuardSDK_FirstStrike(t *testing.T) {
	f := newSpecGuardFake()
	defer f.server.Close()

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL)
	issue := &githubSDK.Issue{Number: 7, Title: "thin", Body: "too thin"}

	skipped := applySpecGuardSDK(context.Background(), client, "o", "r", issue, []string{"body too short"})
	if !skipped {
		t.Fatal("guard must skip dispatch on first strike")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.labelsAdded) != 1 || f.labelsAdded[0] != githubSDK.LabelSpecIncomplete {
		t.Errorf("labels added = %v, want [%s]", f.labelsAdded, githubSDK.LabelSpecIncomplete)
	}
	wantFingerprint := specBodyFingerprint(issue.Body)
	if len(f.commentsAdded) != 1 || !strings.Contains(f.commentsAdded[0], ghissue.BuildSpecCommentMarker(wantFingerprint)) {
		t.Errorf("first-strike comment missing fingerprinted marker; comments = %v", f.commentsAdded)
	}
}

// TestApplySpecGuardSDK_ListCommentsErrorDefersDispatch covers GH-5178: if
// ListIssueComments itself errors, the guard cannot tell whether this issue
// has ever been struck before, so it must defer dispatch to the next poll
// tick (return true) rather than silently letting a possibly-thin issue
// through (which the old `return false` did). No strike is recorded either:
// zero labels added, zero comments posted.
func TestApplySpecGuardSDK_ListCommentsErrorDefersDispatch(t *testing.T) {
	f := newSpecGuardFake()
	defer f.server.Close()
	f.listCommentsStatus = http.StatusInternalServerError

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL)
	issue := &githubSDK.Issue{Number: 7, Title: "thin", Body: "too thin"}

	skipped := applySpecGuardSDK(context.Background(), client, "o", "r", issue, []string{"body too short"})
	if !skipped {
		t.Fatal("guard must defer dispatch (return true) when ListIssueComments errors")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.labelsAdded) != 0 {
		t.Errorf("labels added = %v, want none (list-comments error must not record a strike)", f.labelsAdded)
	}
	if len(f.commentsAdded) != 0 {
		t.Errorf("comments added = %v, want none (list-comments error must not record a strike)", f.commentsAdded)
	}
}

// TestApplySpecGuardSDK_SecondStrikeEscalates covers GH-4635 (c): a genuine
// repeat strike (fingerprint match, and the pre-escalation fresh re-read at
// GH-4634 still fails validation) must escalate to pilot-blocked AND post an
// escalation comment carrying the current failure reasons — GH-4624/D2 found
// this test previously asserted zero comments as correct, test-locking a
// silent block that hid the #4498 loop.
func TestApplySpecGuardSDK_SecondStrikeEscalates(t *testing.T) {
	issue := &githubSDK.Issue{Number: 7, Title: "still thin", Body: "still too thin"}
	fingerprint := specBodyFingerprint(issue.Body)
	f := newSpecGuardFake("earlier strike\n" + ghissue.BuildSpecCommentMarker(fingerprint) + "\ndetails")
	defer f.server.Close()
	// The GH-4634 pre-escalation re-fetch still returns a too-thin body, so
	// the strike proceeds to escalation.
	f.setFreshIssue(t, githubSDK.Issue{Number: 7, Body: "still too thin"})

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL)

	skipped := applySpecGuardSDK(context.Background(), client, "o", "r", issue, []string{"body too short"})
	if !skipped {
		t.Fatal("guard must skip dispatch on second strike")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.labelsAdded) != 1 || f.labelsAdded[0] != githubSDK.LabelBlocked {
		t.Errorf("labels added = %v, want [%s]", f.labelsAdded, githubSDK.LabelBlocked)
	}
	if len(f.commentsAdded) != 1 {
		t.Fatalf("second strike must post an escalation comment; got %d", len(f.commentsAdded))
	}
	escalation := f.commentsAdded[0]
	if fp, ok := ghissue.FindSpecCommentMarkerFingerprint(escalation); !ok || fp == "" {
		t.Errorf("escalation comment missing fingerprinted marker; comment = %q", escalation)
	}
	if !strings.Contains(escalation, "body too short") {
		t.Errorf("escalation comment missing current failure reasons; comment = %q", escalation)
	}
}

// TestApplySpecGuardSDK_SecondStrikeMarkerButFreshBodyValid_NoStrike covers
// GH-4635 (a) / GH-4634 (d): the exact #4498 scenario. A prior marker on the
// issue matches the in-memory (stale) body's fingerprint, so the guard would
// naively treat this as a second strike — but the pre-escalation fresh
// single-issue GET shows the body was fixed in the meantime and now passes
// ValidateSpec. The strike must be aborted entirely: no labels, no comments.
func TestApplySpecGuardSDK_SecondStrikeMarkerButFreshBodyValid_NoStrike(t *testing.T) {
	issue := &githubSDK.Issue{Number: 7, Title: "was thin", Body: "still too thin"}
	fingerprint := specBodyFingerprint(issue.Body)
	f := newSpecGuardFake("earlier strike\n" + ghissue.BuildSpecCommentMarker(fingerprint) + "\ndetails")
	defer f.server.Close()

	goodBody := "## Context\n\nThis body was edited after the first strike and is now long enough, with a structural section header, so it passes spec validation on the fresh re-read.\n\n## Acceptance\n\n- [ ] done"
	f.setFreshIssue(t, githubSDK.Issue{Number: 7, Body: goodBody})

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL)

	skipped := applySpecGuardSDK(context.Background(), client, "o", "r", issue, []string{"body too short"})
	if !skipped {
		t.Fatal("guard must still skip dispatch this round")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.labelsAdded) != 0 {
		t.Errorf("labels added = %v, want none (strike must be aborted on a now-valid fresh body)", f.labelsAdded)
	}
	if len(f.commentsAdded) != 0 {
		t.Errorf("comments added = %v, want none (strike must be aborted on a now-valid fresh body)", f.commentsAdded)
	}
}

// TestApplySpecGuardSDK_SecondStrikeSkipLabelOnFreshRead_NoStrike covers
// GH-4635 (d): the other half of the pre-escalation abort condition — a
// fresh re-read that opts out via pilot-skip-spec-check (SkipReason set,
// Valid stays false) must also abort the strike, not just an outright-valid
// body.
func TestApplySpecGuardSDK_SecondStrikeSkipLabelOnFreshRead_NoStrike(t *testing.T) {
	issue := &githubSDK.Issue{Number: 7, Title: "still thin", Body: "still too thin"}
	fingerprint := specBodyFingerprint(issue.Body)
	f := newSpecGuardFake("earlier strike\n" + ghissue.BuildSpecCommentMarker(fingerprint) + "\ndetails")
	defer f.server.Close()
	f.setFreshIssue(t, githubSDK.Issue{
		Number: 7,
		Body:   "still too thin",
		Labels: []githubSDK.Label{{Name: githubSDK.LabelSkipSpecCheck}},
	})

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL)

	skipped := applySpecGuardSDK(context.Background(), client, "o", "r", issue, []string{"body too short"})
	if !skipped {
		t.Fatal("guard must still skip dispatch this round")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.labelsAdded) != 0 {
		t.Errorf("labels added = %v, want none (strike must be aborted when the fresh read opts out)", f.labelsAdded)
	}
	if len(f.commentsAdded) != 0 {
		t.Errorf("comments added = %v, want none (strike must be aborted when the fresh read opts out)", f.commentsAdded)
	}
}

// TestApplySpecGuardSDK_SecondStrikeAbortsOnFreshFetchError covers the fetch
// failure edge of the GH-4634 pre-escalation re-read: if the fresh
// single-issue GET errors, the guard must not escalate on stale data — it
// aborts the strike rather than trusting the earlier in-memory snapshot.
func TestApplySpecGuardSDK_SecondStrikeAbortsOnFreshFetchError(t *testing.T) {
	issue := &githubSDK.Issue{Number: 7, Title: "still thin", Body: "still too thin"}
	fingerprint := specBodyFingerprint(issue.Body)
	f := newSpecGuardFake("earlier strike\n" + ghissue.BuildSpecCommentMarker(fingerprint) + "\ndetails")
	defer f.server.Close()
	f.freshIssueStatus = http.StatusInternalServerError

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL)

	skipped := applySpecGuardSDK(context.Background(), client, "o", "r", issue, []string{"body too short"})
	if !skipped {
		t.Fatal("guard must still skip dispatch this round")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.labelsAdded) != 0 {
		t.Errorf("labels added = %v, want none (strike must be aborted on fetch error)", f.labelsAdded)
	}
	if len(f.commentsAdded) != 0 {
		t.Errorf("comments added = %v, want none (strike must be aborted on fetch error)", f.commentsAdded)
	}
}

// TestApplySpecGuardSDK_BodyChangedResetsToFirstStrike covers GH-4632: if the
// issue body was edited since the last strike, the fingerprint in the marker
// comment no longer matches. That must be treated as a fresh first strike
// (fresh comment with the new reasons), not an escalation to pilot-blocked.
// This also satisfies GH-4635 (b): fingerprint mismatch -> first-strike
// comment, and the assertion below (labelsAdded == [LabelSpecIncomplete])
// already implies no pilot-blocked label was added.
func TestApplySpecGuardSDK_BodyChangedResetsToFirstStrike(t *testing.T) {
	staleFingerprint := specBodyFingerprint("the old, since-edited body")
	f := newSpecGuardFake("earlier strike\n" + ghissue.BuildSpecCommentMarker(staleFingerprint) + "\ndetails")
	defer f.server.Close()

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL)
	issue := &githubSDK.Issue{Number: 7, Title: "edited", Body: "the new body, still too thin though"}

	skipped := applySpecGuardSDK(context.Background(), client, "o", "r", issue, []string{"body too short"})
	if !skipped {
		t.Fatal("guard must skip dispatch on a body-changed reset")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.labelsAdded) != 1 || f.labelsAdded[0] != githubSDK.LabelSpecIncomplete {
		t.Errorf("labels added = %v, want [%s] (must not escalate to blocked)", f.labelsAdded, githubSDK.LabelSpecIncomplete)
	}
	wantFingerprint := specBodyFingerprint(issue.Body)
	if len(f.commentsAdded) != 1 || !strings.Contains(f.commentsAdded[0], ghissue.BuildSpecCommentMarker(wantFingerprint)) {
		t.Errorf("expected a fresh first-strike comment with the new body's fingerprint; comments = %v", f.commentsAdded)
	}
}

// TestApplySpecGuardSDK_LegacyMarkerTreatedAsFirstStrike covers GH-4632:
// markers posted before fingerprints existed carry no sha256 and must be
// treated as stale, i.e. equivalent to a first strike rather than a
// confirmed repeat.
func TestApplySpecGuardSDK_LegacyMarkerTreatedAsFirstStrike(t *testing.T) {
	f := newSpecGuardFake("earlier strike\n" + ghissue.SpecCommentMarker + "\ndetails")
	defer f.server.Close()

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, f.server.URL)
	issue := &githubSDK.Issue{Number: 7, Title: "still thin", Body: "still too thin"}

	skipped := applySpecGuardSDK(context.Background(), client, "o", "r", issue, []string{"body too short"})
	if !skipped {
		t.Fatal("guard must skip dispatch on a legacy-marker first strike")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.labelsAdded) != 1 || f.labelsAdded[0] != githubSDK.LabelSpecIncomplete {
		t.Errorf("labels added = %v, want [%s] (legacy marker must not escalate)", f.labelsAdded, githubSDK.LabelSpecIncomplete)
	}
	if len(f.commentsAdded) != 1 {
		t.Errorf("expected a fresh first-strike comment; got %d", len(f.commentsAdded))
	}
}

// TestGithubHandlerSDK_SpecGuardWired is a source-level guard: the SDK handler
// must run ghissue.ValidateSpec before dispatch (M7 4d.3 — the canary-caught
// spec-gate class must not regress when the SDK poll path goes live).
func TestGithubHandlerSDK_SpecGuardWired(t *testing.T) {
	content, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatalf("read handlers.go: %v", err)
	}
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")
	_ = content
	if !strings.Contains(body, "ghissue.ValidateSpec") {
		t.Error("handleGithubIssueEventSDK must run ghissue.ValidateSpec before dispatch")
	}
	if !strings.Contains(body, "applySpecGuardSDK") {
		t.Error("handleGithubIssueEventSDK must apply the spec guard on validation failure")
	}
}

// TestGithubHandlerSDK_SpecBodyThreadedFromFreshGET is a source-level
// regression guard for GH-4635 (e) / GH-4631: specIssue.Body must come from
// the fresh single-issue GET (realIssue.Body) rather than the poll-tick
// list-snapshot (ev.Body) when the fetch succeeds — GH-4624/D3 identified
// this as the root cause of the #4498 block/unblock loop (the spec guard
// judged a stale pre-edit body). ev.Body is still the correct fallback when
// the fetch itself fails.
func TestGithubHandlerSDK_SpecBodyThreadedFromFreshGET(t *testing.T) {
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")

	if !strings.Contains(body, "specBody := ev.Body") {
		t.Error("handleGithubIssueEventSDK must default specBody to ev.Body as the fetch-failure fallback")
	}
	if !strings.Contains(body, "specBody = realIssue.Body") {
		t.Error("handleGithubIssueEventSDK must overwrite specBody with realIssue.Body when the fresh GET succeeds")
	}
	if !strings.Contains(strings.Join(strings.Fields(body), " "), "Body: specBody") {
		t.Error("handleGithubIssueEventSDK must build specIssue with Body: specBody, not Body: ev.Body")
	}
	if strings.Contains(strings.Join(strings.Fields(body), " "), "Body: ev.Body") {
		t.Error("handleGithubIssueEventSDK must not construct specIssue directly from the stale ev.Body")
	}
}
