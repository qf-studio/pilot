package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	linearSDK "github.com/qf-studio/studio-sdk/sdk/integrations/linear"

	"github.com/qf-studio/pilot/internal/adapters/linear"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
)

const (
	testLinearIssueUUID    = "11111111-1111-4111-8111-111111111111"
	testLinearIssueIdent   = "QF-295"
	testLinearStartedFirst = "aaaaaaaa-0000-4000-8000-000000000001"
	testLinearStartedLater = "aaaaaaaa-0000-4000-8000-000000000002"
	testLinearCompletedID  = "cccccccc-0000-4000-8000-000000000001"
	testLinearInReviewID   = "dddddddd-0000-4000-8000-000000000001"
	testLinearBacklogID    = "bbbbbbbb-0000-4000-8000-000000000001" // the fake issue's state before the start leg
)

// linearStateFake is a minimal Linear GraphQL server covering every query and
// mutation the state mover issues. It records them so tests can assert the
// exact GraphQL shape (GH-5701).
type linearStateFake struct {
	server *httptest.Server

	mu          sync.Mutex
	ops         []string            // operation markers in request order
	updates     []map[string]string // issueUpdate variables: id, stateId
	comments    []string
	getIssueIDs []string
	failUpdate  bool
	// startedNodes overrides the started-type nodes (empty = two default states).
	startedNodes string
}

func newLinearStateFake() *linearStateFake {
	f := &linearStateFake{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		f.mu.Lock()
		defer f.mu.Unlock()

		str := func(k string) string { s, _ := req.Variables[k].(string); return s }
		switch {
		case strings.Contains(req.Query, "issueUpdate"):
			f.ops = append(f.ops, "issueUpdate")
			if f.failUpdate {
				_, _ = w.Write([]byte(`{"errors":[{"message":"injected update failure"}]}`))
				return
			}
			f.updates = append(f.updates, map[string]string{"id": str("id"), "stateId": str("stateId")})
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true}}}`))
		case strings.Contains(req.Query, "commentCreate"):
			f.ops = append(f.ops, "commentCreate")
			f.comments = append(f.comments, str("body"))
			_, _ = w.Write([]byte(`{"data":{"commentCreate":{"success":true}}}`))
		case strings.Contains(req.Query, "query GetIssue"):
			f.ops = append(f.ops, "getIssue")
			f.getIssueIDs = append(f.getIssueIDs, str("id"))
			id := str("id")
			if id != testLinearIssueUUID && id != testLinearIssueIdent {
				_, _ = w.Write([]byte(`{"errors":[{"message":"Entity not found: Issue"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"issue":{"id":"` + testLinearIssueUUID + `","identifier":"` + testLinearIssueIdent +
				`","title":"t","team":{"id":"team-1","name":"QF","key":"QF"},"state":{"id":"` + testLinearBacklogID + `","name":"Backlog","type":"backlog"},"labels":{"nodes":[]}}}}`))
		case strings.Contains(req.Query, "GetTeamDoneState"):
			f.ops = append(f.ops, "doneState")
			_, _ = w.Write([]byte(`{"data":{"workflowStates":{"nodes":[{"id":"` + testLinearCompletedID + `","name":"Done","type":"completed"}]}}}`))
		case strings.Contains(req.Query, "PilotTeamWorkflowStates"):
			if _, typed := req.Variables["type"]; typed {
				f.ops = append(f.ops, "startedStates")
				nodes := f.startedNodes
				if nodes == "" {
					// Deliberately out of position order: the lowest position wins.
					nodes = `{"id":"` + testLinearStartedLater + `","name":"In Review","type":"started","position":3},` +
						`{"id":"` + testLinearStartedFirst + `","name":"In Progress","type":"started","position":2}`
				}
				_, _ = w.Write([]byte(`{"data":{"workflowStates":{"nodes":[` + nodes + `]}}}`))
				return
			}
			f.ops = append(f.ops, "listStates")
			_, _ = w.Write([]byte(`{"data":{"workflowStates":{"nodes":[` +
				`{"id":"` + testLinearStartedFirst + `","name":"In Progress","type":"started","position":2},` +
				`{"id":"` + testLinearInReviewID + `","name":"In Review","type":"started","position":3}]}}}`))
		case strings.Contains(req.Query, "SyncResolveWorkflowStates"):
			f.ops = append(f.ops, "resolveName")
			_, _ = w.Write([]byte(`{"data":{"workflowStates":{"nodes":[` +
				`{"id":"` + testLinearInReviewID + `","name":"In Review"},` +
				`{"id":"` + testLinearStartedFirst + `","name":"In Progress"}]}}}`))
		default:
			f.ops = append(f.ops, "unknown")
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	}))
	return f
}

func (f *linearStateFake) client() *linearSDK.Client {
	return linearSDK.NewClientWithBaseURL(testutil.FakeLinearAPIKey, f.server.URL)
}

func (f *linearStateFake) mover(cfg linear.Transitions) *linearStateMover {
	c := f.client()
	return newLinearStateMover(c, linearSDK.NewSyncAdapter(c), cfg)
}

func (f *linearStateFake) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, o := range f.ops {
		if o == op {
			n++
		}
	}
	return n
}

func (f *linearStateFake) lastUpdate(t *testing.T) map[string]string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.updates) == 0 {
		t.Fatal("no issueUpdate recorded")
	}
	return f.updates[len(f.updates)-1]
}

func TestLinearStateMover_DefaultStarted(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	m := f.mover(linear.Transitions{})

	if err := m.Move(context.Background(), testLinearIssueUUID, "LIN-QF-295", legStart); err != nil {
		t.Fatalf("Move(start): %v", err)
	}
	u := f.lastUpdate(t)
	if u["id"] != testLinearIssueUUID || u["stateId"] != testLinearStartedFirst {
		t.Errorf("issueUpdate = %+v, want issue %s → lowest-position started state %s", u, testLinearIssueUUID, testLinearStartedFirst)
	}

	// Resolver cache: the second dispatch for the same team does not re-list states.
	if err := m.Move(context.Background(), testLinearIssueUUID, "LIN-QF-295", legStart); err != nil {
		t.Fatalf("second Move(start): %v", err)
	}
	if n := f.count("startedStates"); n != 1 {
		t.Errorf("started-state queries = %d, want 1 (cached per team)", n)
	}
}

func TestLinearStateMover_DefaultCompleted(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	m := f.mover(linear.Transitions{})

	if err := m.Move(context.Background(), testLinearIssueUUID, "QF-295", legMerge); err != nil {
		t.Fatalf("Move(merge): %v", err)
	}
	if u := f.lastUpdate(t); u["stateId"] != testLinearCompletedID {
		t.Errorf("issueUpdate stateId = %s, want completed state %s", u["stateId"], testLinearCompletedID)
	}
}

func TestLinearStateMover_ConfiguredUUIDPassesThrough(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	m := f.mover(linear.Transitions{InProgress: testLinearInReviewID})

	if err := m.Move(context.Background(), testLinearIssueUUID, "LIN-QF-295", legStart); err != nil {
		t.Fatalf("Move(start): %v", err)
	}
	if u := f.lastUpdate(t); u["stateId"] != testLinearInReviewID {
		t.Errorf("stateId = %s, want the configured UUID %s", u["stateId"], testLinearInReviewID)
	}
	for _, op := range []string{"resolveName", "startedStates"} {
		if n := f.count(op); n != 0 {
			t.Errorf("%s calls = %d, want 0 for a UUID target", op, n)
		}
	}
	// The start leg reads the issue once to record its prior state (GH-5703).
	if n := f.count("getIssue"); n != 1 {
		t.Errorf("getIssue calls = %d, want 1 (prior-state read)", n)
	}
}

func TestLinearStateMover_ConfiguredNameResolves(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	m := f.mover(linear.Transitions{InReview: "In Review"})

	if err := m.Move(context.Background(), testLinearIssueUUID, "LIN-QF-295", legPR); err != nil {
		t.Fatalf("Move(pr): %v", err)
	}
	if u := f.lastUpdate(t); u["id"] != testLinearIssueUUID || u["stateId"] != testLinearInReviewID {
		t.Errorf("issueUpdate = %+v, want name 'In Review' resolved to %s", u, testLinearInReviewID)
	}
}

func TestLinearStateMover_UnconfiguredOptionalLegsSkip(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	m := f.mover(linear.Transitions{})

	for _, leg := range []linearLeg{legPR, legFailed} {
		if err := m.Move(context.Background(), testLinearIssueUUID, "LIN-QF-295", leg); !errors.Is(err, errNoTransition) {
			t.Errorf("Move(%s) err = %v, want errNoTransition", leg, err)
		}
	}
	if n := len(f.ops); n != 0 {
		t.Errorf("requests = %v, want none for unconfigured optional legs", f.ops)
	}
}

func TestLinearStateMover_FailureReturnsErrorAndWarns(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	f.failUpdate = true
	m := f.mover(linear.Transitions{})
	var buf bytes.Buffer
	m.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	err := m.Move(context.Background(), testLinearIssueUUID, "LIN-QF-295", legStart)
	if err == nil {
		t.Fatal("expected an error when the update fails")
	}
	out := buf.String()
	if !strings.Contains(out, `msg="Linear state transition"`) || !strings.Contains(out, "leg=start") ||
		!strings.Contains(out, "issue=LIN-QF-295") || !strings.Contains(out, "to=type:started") {
		t.Errorf("INFO transition line missing or malformed: %s", out)
	}
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "Linear state transition failed") {
		t.Errorf("WARN failure line missing: %s", out)
	}
}

func TestLinearStateMover_NoStartedStateIsError(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	f.startedNodes = " "
	m := f.mover(linear.Transitions{})

	if err := m.Move(context.Background(), testLinearIssueUUID, "LIN-QF-295", legStart); err == nil {
		t.Fatal("expected an error when the team has no started-type state")
	}
	if n := len(f.updates); n != 0 {
		t.Errorf("issueUpdate calls = %d, want 0", n)
	}
}

// TestLinearStateMover_MoveIdentifierResolvesViaGetIssue pins the GraphQL
// shape the merge leg depends on: the branch-derived identifier ("QF-295") is
// sent as the id variable of issue(id:), and the transition then targets the
// UUID the API returns — never the identifier.
func TestLinearStateMover_MoveIdentifierResolvesViaGetIssue(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	m := f.mover(linear.Transitions{})

	if err := m.MoveIdentifier(context.Background(), testLinearIssueIdent, legMerge); err != nil {
		t.Fatalf("MoveIdentifier: %v", err)
	}
	f.mu.Lock()
	gotIDs := append([]string(nil), f.getIssueIDs...)
	f.mu.Unlock()
	if len(gotIDs) == 0 || gotIDs[0] != testLinearIssueIdent {
		t.Errorf("GetIssue ids = %v, want first to be the identifier %q", gotIDs, testLinearIssueIdent)
	}
	if u := f.lastUpdate(t); u["id"] != testLinearIssueUUID || u["stateId"] != testLinearCompletedID {
		t.Errorf("issueUpdate = %+v, want issue %s → %s", u, testLinearIssueUUID, testLinearCompletedID)
	}

	if err := m.MoveIdentifier(context.Background(), "ZZ-1", legMerge); err == nil {
		t.Error("expected an error for an unknown identifier")
	}
}

func TestPreflightLinearTransitions(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()

	run := func(tr linear.Transitions) string {
		var buf bytes.Buffer
		preflightLinearTransitions(context.Background(), slog.New(slog.NewTextHandler(&buf, nil)), f.client(), "QF", tr)
		return buf.String()
	}

	if out := run(linear.Transitions{InProgress: "In Progress", InReview: testLinearInReviewID}); out != "" {
		t.Errorf("resolvable targets must be silent, got: %s", out)
	}
	out := run(linear.Transitions{Done: "Shipped"})
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "transition=done") || !strings.Contains(out, "Shipped") {
		t.Errorf("unresolvable name must WARN, got: %q", out)
	}
	before := f.count("listStates")
	if out := run(linear.Transitions{}); out != "" || f.count("listStates") != before {
		t.Errorf("no configured targets must make no request and log nothing (out=%q)", out)
	}
}

// fakeBackendErr is a classified backend error (executor.BackendError).
type fakeBackendErr struct{ typ, msg, stderr string }

func (e *fakeBackendErr) Error() string        { return e.typ + ": " + e.msg + " (stderr: " + e.stderr + ")" }
func (e *fakeBackendErr) ErrorType() string    { return e.typ }
func (e *fakeBackendErr) ErrorMessage() string { return e.msg }
func (e *fakeBackendErr) ErrorStderr() string  { return e.stderr }

func TestLinearFailureReason(t *testing.T) {
	if got := linearFailureReason(nil); got != "unknown error" {
		t.Errorf("nil error → %q", got)
	}
	long := errors.New(strings.Repeat("x", 500))
	got := linearFailureReason(long)
	if r := []rune(got); len(r) != linearFailureReasonMax+1 {
		t.Errorf("truncated length = %d runes, want %d", len(r), linearFailureReasonMax+1)
	}
	if got := linearFailureReason(errors.New("  boom \n")); got != "error: boom" {
		t.Errorf("trim → %q", got)
	}
}

// TestLinearFailureReason_NoLeak pins GH-5703: the public failure comment
// carries a class and the first line only — never absolute paths, worktree
// locations, stderr or the rest of the wrapped chain.
func TestLinearFailureReason_NoLeak(t *testing.T) {
	const wt = "/tmp/pilot-worktree-GH-5703-1792064788020163877"
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "wrapped chain with worktree path keeps first line, drops path",
			err:  fmt.Errorf("execution failed: git push in %s: exit 1\nremote: secret detail\n  at /var/lib/pilot/x.go:12", wt),
			want: "error: execution failed: git push in <path> exit 1",
		},
		{
			name: "path in quotes and after colon",
			err:  errors.New(`open "` + wt + `/a.go": no such file; cwd:/home/ec2-user/repo`),
			want: "error: open \"<path>\": no such file; cwd:<path>",
		},
		{
			name: "classified backend error uses its type and message, not stderr",
			err:  fmt.Errorf("wrapped: %w", &fakeBackendErr{typ: "rate_limit", msg: "429 from " + wt, stderr: "trace at /tmp/secret/stderr.log"}),
			want: "rate_limit: 429 from <path>",
		},
		{
			name: "non-path slashes survive",
			err:  errors.New("tests failed and/or lint failed (ratio 3/4)"),
			want: "error: tests failed and/or lint failed (ratio 3/4)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := linearFailureReason(tt.err)
			if got != tt.want {
				t.Errorf("reason = %q\nwant     %q", got, tt.want)
			}
			for _, bad := range []string{"/tmp/", "/var/", "/home/", "pilot-worktree", "secret", "stderr"} {
				if strings.Contains(got, bad) {
					t.Errorf("reason %q leaks %q", got, bad)
				}
			}
		})
	}
}

// --- merge-leg workspace routing -------------------------------------------

type fakeIdentMover struct {
	err   error
	calls []string
}

func (f *fakeIdentMover) MoveIdentifier(_ context.Context, identifier string, leg linearLeg) error {
	f.calls = append(f.calls, identifier+":"+string(leg))
	return f.err
}

func TestLinearDoneNotifier_RoutesByTeamKey(t *testing.T) {
	a, b := &fakeIdentMover{}, &fakeIdentMover{}
	n := newLinearDoneNotifier([]linearDoneWorkspace{{teamRef: "AA", mover: a}, {teamRef: "qf", mover: b}})

	if err := n.NotifyMerged(context.Background(), "QF-295", "https://example.test/pr/1"); err != nil {
		t.Fatalf("NotifyMerged: %v", err)
	}
	if len(a.calls) != 0 || len(b.calls) != 1 || b.calls[0] != "QF-295:merge" {
		t.Errorf("a=%v b=%v, want only b called with QF-295:merge", a.calls, b.calls)
	}
}

// TestLinearDoneNotifier_MatchedWorkspaceErrorWins pins GH-5703: when the team
// key matches a workspace and its move fails, that error is returned and the
// other workspaces are never called (their "not found" must not mask it).
func TestLinearDoneNotifier_MatchedWorkspaceErrorWins(t *testing.T) {
	wantErr := errors.New("done state not configured for team QF")
	matched := &fakeIdentMover{err: wantErr}
	other := &fakeIdentMover{err: errors.New("Entity not found: Issue")}
	n := newLinearDoneNotifier([]linearDoneWorkspace{{teamRef: "AA", mover: other}, {teamRef: "qf", mover: matched}})

	err := n.NotifyMerged(context.Background(), "QF-295", "")
	if !errors.Is(err, wantErr) {
		t.Fatalf("NotifyMerged error = %v, want the matched workspace's error %v", err, wantErr)
	}
	if len(matched.calls) != 1 || len(other.calls) != 0 {
		t.Errorf("matched=%v other=%v, want matched called once and other never", matched.calls, other.calls)
	}
}

func TestLinearDoneNotifier_FallsBackAcrossWorkspaces(t *testing.T) {
	// team_id is a UUID, so the key cannot match: try each workspace until one can read the issue.
	a := &fakeIdentMover{err: errors.New("not found")}
	b := &fakeIdentMover{}
	n := newLinearDoneNotifier([]linearDoneWorkspace{{teamRef: "uuid-a", mover: a}, {teamRef: "uuid-b", mover: b}})

	if err := n.NotifyMerged(context.Background(), "QF-295", ""); err != nil {
		t.Fatalf("NotifyMerged: %v", err)
	}
	if len(a.calls) != 1 || len(b.calls) != 1 {
		t.Errorf("a=%v b=%v, want both tried", a.calls, b.calls)
	}

	allFail := newLinearDoneNotifier([]linearDoneWorkspace{{teamRef: "x", mover: a}})
	if err := allFail.NotifyMerged(context.Background(), "QF-295", ""); err == nil {
		t.Error("expected an error when every workspace fails")
	}
	if err := newLinearDoneNotifier(nil).NotifyMerged(context.Background(), "QF-295", ""); err == nil {
		t.Error("expected an error with no workspaces")
	}
}

// --- handler legs ----------------------------------------------------------

type legRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *legRecorder) add(e string) { r.mu.Lock(); r.events = append(r.events, e); r.mu.Unlock() }

type recMover struct {
	rec *legRecorder
	err error
}

func (m recMover) Move(_ context.Context, issueUUID, _ string, leg linearLeg) error {
	m.rec.add("move:" + string(leg) + ":" + issueUUID)
	return m.err
}

func (m recMover) MoveNoPR(_ context.Context, issueUUID, _ string) error {
	m.rec.add("move:nopr:" + issueUUID)
	return m.err
}

type recNotifier struct {
	rec *legRecorder
	err error
}

func (n recNotifier) NotifyTaskCompleted(_ context.Context, issueID, prURL, _ string) error {
	n.rec.add("completed:" + issueID + ":" + prURL)
	return n.err
}

func (n recNotifier) NotifyTaskEndedWithoutPR(_ context.Context, issueID string) error {
	n.rec.add("nopr:" + issueID)
	return n.err
}

func (n recNotifier) NotifyTaskFailed(_ context.Context, issueID, reason string) error {
	n.rec.add("failed:" + issueID + ":" + reason)
	return n.err
}

func TestRunLinearStateLegs(t *testing.T) {
	ev := sdkcore.IssueEvent{IssueID: "uuid-1", SequenceID: "LIN-QF-295"}
	boom := errors.New("executor exploded")

	tests := []struct {
		name       string
		res        *sdkcore.IssueResult
		runErr     error
		moverErr   error
		notifyErr  error
		noAdmit    bool // run returns without the dispatch having been admitted
		wantEvents []string
	}{
		{
			name:       "success with MR URL: start, run, completion comment, pr leg",
			res:        &sdkcore.IssueResult{Success: true, PRURL: "https://gitlab.example.test/g/p/-/merge_requests/12"},
			wantEvents: []string{"move:start:uuid-1", "run", "completed:uuid-1:https://gitlab.example.test/g/p/-/merge_requests/12", "move:pr:uuid-1"},
		},
		{
			name:       "failure: failure comment and failed leg",
			res:        &sdkcore.IssueResult{Success: false, Error: boom},
			wantEvents: []string{"move:start:uuid-1", "run", "failed:uuid-1:error: executor exploded", "move:failed:uuid-1"},
		},
		{
			name:       "skipped result posts and moves nothing after the start leg",
			res:        &sdkcore.IssueResult{Skipped: true, SkipReason: "needs_human"},
			wantEvents: []string{"move:start:uuid-1", "run"},
		},
		{
			name:       "success without PR URL: terminal comment and no-PR move (GH-5703)",
			res:        &sdkcore.IssueResult{Success: true},
			wantEvents: []string{"move:start:uuid-1", "run", "nopr:uuid-1", "move:nopr:uuid-1"},
		},
		{
			name:       "gated decline (success + ErrDispatchGated) never moves or comments (GH-5703)",
			res:        &sdkcore.IssueResult{Success: true, Error: executor.ErrDispatchGated},
			noAdmit:    true,
			wantEvents: []string{"run"},
		},
		{
			name:       "nil result: nothing after run",
			res:        nil,
			wantEvents: []string{"move:start:uuid-1", "run"},
		},
		{
			name:       "mover and notifier failures never change the result",
			res:        &sdkcore.IssueResult{Success: true, PRURL: "https://github.com/o/r/pull/1"},
			moverErr:   errors.New("linear down"),
			notifyErr:  errors.New("linear down"),
			wantEvents: []string{"move:start:uuid-1", "run", "completed:uuid-1:https://github.com/o/r/pull/1", "move:pr:uuid-1"},
		},
		{
			name:       "handler error is passed through untouched",
			res:        nil,
			runErr:     boom,
			wantEvents: []string{"move:start:uuid-1", "run"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &legRecorder{}
			got, err := runLinearStateLegs(context.Background(), ev,
				recNotifier{rec: rec, err: tt.notifyErr}, recMover{rec: rec, err: tt.moverErr},
				func(admitted func()) (*sdkcore.IssueResult, error) {
					if !tt.noAdmit {
						admitted()
						admitted() // idempotent: only the first call moves
					}
					rec.add("run")
					return tt.res, tt.runErr
				})
			if got != tt.res || err != tt.runErr {
				t.Errorf("result/error altered: got (%v, %v), want (%v, %v)", got, err, tt.res, tt.runErr)
			}
			if strings.Join(rec.events, "|") != strings.Join(tt.wantEvents, "|") {
				t.Errorf("events = %v\nwant     %v", rec.events, tt.wantEvents)
			}
		})
	}
}

// TestLinearHandlerRunsStateLegs drives the legs through the real SDK
// client and a fake GraphQL server: the start move must hit the API before any
// dispatch, the completion comment must carry the MR URL, and the issue UUID
// (ev.IssueID) — not the identifier — must be what issueUpdate receives.
func TestLinearHandlerRunsStateLegs(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	c := f.client()
	mover := newLinearStateMover(c, linearSDK.NewSyncAdapter(c), linear.Transitions{InReview: "In Review"})
	notifier := newLinearLegNotifier(c)
	ev := sdkcore.IssueEvent{IssueID: testLinearIssueUUID, SequenceID: "LIN-QF-295"}
	const mr = "https://gitlab.example.test/g/p/-/merge_requests/12"

	_, err := runLinearStateLegs(context.Background(), ev, notifier, mover, func(admitted func()) (*sdkcore.IssueResult, error) {
		admitted()
		if f.count("issueUpdate") != 1 {
			t.Error("start leg must have moved the issue before dispatch")
		}
		return &sdkcore.IssueResult{Success: true, PRURL: mr}, nil
	})
	if err != nil {
		t.Fatalf("runLinearStateLegs: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.updates) != 2 {
		t.Fatalf("issueUpdate calls = %d, want 2 (start, pr)", len(f.updates))
	}
	if f.updates[0]["stateId"] != testLinearStartedFirst || f.updates[1]["stateId"] != testLinearInReviewID {
		t.Errorf("states = %s, %s; want %s, %s", f.updates[0]["stateId"], f.updates[1]["stateId"], testLinearStartedFirst, testLinearInReviewID)
	}
	for _, u := range f.updates {
		if u["id"] != testLinearIssueUUID {
			t.Errorf("issueUpdate id = %s, want the issue UUID", u["id"])
		}
	}
	if len(f.comments) != 1 || !strings.Contains(f.comments[0], mr) {
		t.Errorf("comments = %v, want one completion comment carrying %s", f.comments, mr)
	}
}

// --- GH-5703: gated declines, success without a PR ------------------------

// TestLinearGatedDeclineMakesNoStateMutation drives the real handler chain
// (handleLinearIssueWithResult → handleIssueGeneric with a real dispatcher)
// against the fake Linear API. The task already has terminal completion, so
// the admission gate declines with ErrDispatchGated; the issue — a Done issue
// in a repick storm — must see zero issueUpdate mutations and no comment.
func TestLinearGatedDeclineMakesNoStateMutation(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	c := f.client()
	mover := newLinearStateMover(c, linearSDK.NewSyncAdapter(c), linear.Transitions{})
	notifier := newLinearLegNotifier(c)

	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("dispatcher.Start: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	const taskID = "LIN-QF-5703"
	const projectPath = "/tmp/pilot-gh-5703-gated-does-not-exist"
	t.Cleanup(func() { repickBackoff.recordSuccess(repickBackoffKey(projectPath, taskID)) })

	// A terminal, done execution: HasTerminalCompletion declines the re-pick.
	seed := &executor.Task{ID: taskID, ProjectPath: projectPath}
	execID, err := executor.NewExecutionLifecycle(store).Begin(seed, executor.ExecStatusRunning, 0)
	if err != nil {
		t.Fatalf("seed Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "no_op"); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	cfg := &config.Config{Adapters: &config.AdaptersConfig{Linear: &linear.Config{}}}
	ev := sdkcore.IssueEvent{IssueID: testLinearIssueUUID, SequenceID: taskID, Title: "gated"}

	var res *sdkcore.IssueResult
	for i := 0; i < 3; i++ { // a repick storm: every tick is declined
		res, err = runLinearStateLegs(context.Background(), ev, notifier, mover, func(admitted func()) (*sdkcore.IssueResult, error) {
			return handleLinearIssueWithResult(context.Background(), cfg, ev, projectPath, dispatcher, executor.NewRunner(), executor.NewMonitor(), nil, nil, nil, admitted)
		})
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if res == nil || !errors.Is(res.Error, executor.ErrDispatchGated) {
		t.Fatalf("result = %+v, want a gated decline (ErrDispatchGated)", res)
	}
	if n := f.count("issueUpdate"); n != 0 {
		t.Errorf("issueUpdate mutations on a gated decline = %d, want 0", n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.comments) != 0 {
		t.Errorf("comments on a gated decline = %v, want none", f.comments)
	}
}

// TestLinearSuccessWithoutPRRestoresPriorState: Success=true, PRURL="" (no
// changes needed, issue closed mid-run, operator cancel) posts one terminal
// comment and moves the issue back to the state recorded by the start leg.
func TestLinearSuccessWithoutPRRestoresPriorState(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	c := f.client()
	mover := newLinearStateMover(c, linearSDK.NewSyncAdapter(c), linear.Transitions{})
	notifier := newLinearLegNotifier(c)
	ev := sdkcore.IssueEvent{IssueID: testLinearIssueUUID, SequenceID: "LIN-QF-295"}

	res, err := runLinearStateLegs(context.Background(), ev, notifier, mover, func(admitted func()) (*sdkcore.IssueResult, error) {
		admitted()
		return &sdkcore.IssueResult{Success: true}, nil
	})
	if err != nil || res == nil || !res.Success {
		t.Fatalf("result altered: %+v, %v", res, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.updates) != 2 {
		t.Fatalf("issueUpdate calls = %d (%v), want 2 (start, restore)", len(f.updates), f.updates)
	}
	if f.updates[0]["stateId"] != testLinearStartedFirst {
		t.Errorf("start moved to %s, want %s", f.updates[0]["stateId"], testLinearStartedFirst)
	}
	if f.updates[1]["id"] != testLinearIssueUUID || f.updates[1]["stateId"] != testLinearBacklogID {
		t.Errorf("restore = %+v, want issue %s → prior state %s", f.updates[1], testLinearIssueUUID, testLinearBacklogID)
	}
	if len(f.comments) != 1 || !strings.Contains(f.comments[0], "without opening a pull request") {
		t.Errorf("comments = %v, want exactly one terminal no-PR comment", f.comments)
	}
}

// With a failed state configured it wins over the recorded prior state.
func TestLinearSuccessWithoutPRPrefersConfiguredFailedState(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	c := f.client()
	mover := newLinearStateMover(c, linearSDK.NewSyncAdapter(c), linear.Transitions{Failed: testLinearInReviewID})
	notifier := newLinearLegNotifier(c)
	ev := sdkcore.IssueEvent{IssueID: testLinearIssueUUID, SequenceID: "LIN-QF-295"}

	_, err := runLinearStateLegs(context.Background(), ev, notifier, mover, func(admitted func()) (*sdkcore.IssueResult, error) {
		admitted()
		return &sdkcore.IssueResult{Success: true}, nil
	})
	if err != nil {
		t.Fatalf("runLinearStateLegs: %v", err)
	}
	if u := f.lastUpdate(t); u["stateId"] != testLinearInReviewID {
		t.Errorf("last move = %+v, want the configured failed state %s", u, testLinearInReviewID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.updates) != 2 || len(f.comments) != 1 {
		t.Errorf("updates=%d comments=%d, want 2 and 1", len(f.updates), len(f.comments))
	}
}

// Without a recorded prior state (the start leg never ran) there is nowhere to
// move the issue: comment only.
func TestLinearStateMover_MoveNoPRWithoutPriorIsNoTransition(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	m := f.mover(linear.Transitions{})

	if err := m.MoveNoPR(context.Background(), testLinearIssueUUID, "LIN-QF-295"); !errors.Is(err, errNoTransition) {
		t.Errorf("MoveNoPR without prior = %v, want errNoTransition", err)
	}
	if n := f.count("issueUpdate"); n != 0 {
		t.Errorf("issueUpdate calls = %d, want 0", n)
	}
}

// The PR leg ends the bookkeeping: a later no-PR close-out cannot resurrect a
// stale prior state.
func TestLinearStateMover_PriorStateForgottenAfterLaterLeg(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	m := f.mover(linear.Transitions{InReview: "In Review"})

	ctx := context.Background()
	if err := m.Move(ctx, testLinearIssueUUID, "LIN-QF-295", legStart); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := m.Move(ctx, testLinearIssueUUID, "LIN-QF-295", legPR); err != nil {
		t.Fatalf("pr: %v", err)
	}
	if err := m.MoveNoPR(ctx, testLinearIssueUUID, "LIN-QF-295"); !errors.Is(err, errNoTransition) {
		t.Errorf("MoveNoPR after pr leg = %v, want errNoTransition", err)
	}
}

// The merge leg reads the issue once (GH-5703 note: no repeated GetIssue).
func TestLinearStateMover_MoveIdentifierFetchesIssueOnce(t *testing.T) {
	f := newLinearStateFake()
	defer f.server.Close()
	m := f.mover(linear.Transitions{})

	if err := m.MoveIdentifier(context.Background(), testLinearIssueIdent, legMerge); err != nil {
		t.Fatalf("MoveIdentifier: %v", err)
	}
	if n := f.count("getIssue"); n != 1 {
		t.Errorf("getIssue calls = %d, want 1", n)
	}
}
