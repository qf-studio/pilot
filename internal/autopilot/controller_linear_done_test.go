package autopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

type fakeLinearDoneCall struct {
	Identifier string
	PRURL      string
}

// fakeLinearDoneNotifier is a test double for LinearDoneNotifier.
type fakeLinearDoneNotifier struct {
	calls []fakeLinearDoneCall
	err   error
}

func (f *fakeLinearDoneNotifier) NotifyMerged(_ context.Context, identifier, prURL string) error {
	f.calls = append(f.calls, fakeLinearDoneCall{Identifier: identifier, PRURL: prURL})
	return f.err
}

func TestLinearIssueIdentifierFromBranch(t *testing.T) {
	tests := []struct{ branch, want string }{
		{"pilot/LIN-QF-295", "QF-295"},
		{"LIN-QF-295", "QF-295"},
		{"pilot/GH-10", ""},
		{"pilot/JIRA-KAN-6", ""},
		{"feature/LIN-QF-1", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := linearIssueIdentifierFromBranch(tt.branch); got != tt.want {
			t.Errorf("linearIssueIdentifierFromBranch(%q) = %q, want %q", tt.branch, got, tt.want)
		}
	}
}

// TestNotifyLinearDone_HandleMerging covers GH-5701's autopilot merge path: a
// pilot/LIN-* PR merge fires the notifier exactly once with the identifier
// and PR URL; a non-LIN branch never does; a notifier error is WARN-only and
// the merge still completes.
func TestNotifyLinearDone_HandleMerging(t *testing.T) {
	tests := []struct {
		name          string
		branchName    string
		notifierErr   error
		wantCalls     []fakeLinearDoneCall
		wantWarnInLog string
	}{
		{
			name:       "LIN branch fires once",
			branchName: "pilot/LIN-QF-295",
			wantCalls:  []fakeLinearDoneCall{{Identifier: "QF-295", PRURL: "https://github.com/owner/repo/pull/201"}},
		},
		{
			name:       "non-LIN branch does not fire",
			branchName: "pilot/GH-30",
		},
		{
			name:          "notifier error is WARN-only",
			branchName:    "pilot/LIN-QF-296",
			notifierErr:   context.DeadlineExceeded,
			wantWarnInLog: "failed to move Linear issue to done",
			wantCalls:     []fakeLinearDoneCall{{Identifier: "QF-296", PRURL: "https://github.com/owner/repo/pull/201"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo/pulls/201/merge" && r.Method == http.MethodPost {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("{}"))
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvDev
			cfg.AutoMerge = true

			var logBuf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuf, nil))

			c := NewController(cfg, ghClient, nil, "owner", "repo", WithLogger(logger))
			notifier := &fakeLinearDoneNotifier{err: tt.notifierErr}
			c.SetLinearDoneNotifier(notifier)

			c.mu.Lock()
			c.activePRs[201] = &PRState{
				PRNumber:     201,
				PRURL:        "https://github.com/owner/repo/pull/201",
				BranchName:   tt.branchName,
				HeadSHA:      "sha201",
				Stage:        StageMerging,
				TargetBranch: "main",
				CreatedAt:    time.Now(),
			}
			c.mu.Unlock()

			if err := c.ProcessPR(context.Background(), 201, nil); err != nil {
				t.Fatalf("ProcessPR returned error: %v", err)
			}
			pr, _ := c.GetPRState(201)
			if pr.Stage != StageMerged {
				t.Errorf("Stage = %s, want %s — merge must succeed even if the Linear leg fails", pr.Stage, StageMerged)
			}
			if len(notifier.calls) != len(tt.wantCalls) {
				t.Fatalf("notifier calls = %+v, want %+v", notifier.calls, tt.wantCalls)
			}
			for i, want := range tt.wantCalls {
				if notifier.calls[i] != want {
					t.Errorf("call[%d] = %+v, want %+v", i, notifier.calls[i], want)
				}
			}
			if tt.wantWarnInLog != "" && !strings.Contains(logBuf.String(), tt.wantWarnInLog) {
				t.Errorf("log missing %q; got: %s", tt.wantWarnInLog, logBuf.String())
			}
		})
	}
}

// TestNotifyLinearDone_ExternalMerge covers the checkExternalMergeOrClose
// merged branch: a human-merged pilot/LIN-* PR fires the leg once.
func TestNotifyLinearDone_ExternalMerge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls/42" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number: 42, State: "closed", Merged: true,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	notifier := &fakeLinearDoneNotifier{}
	c.SetLinearDoneNotifier(notifier)

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 0, "abc123", "pilot/LIN-QF-295", "")
	c.processAllPRs(context.Background())

	want := fakeLinearDoneCall{Identifier: "QF-295", PRURL: "https://github.com/owner/repo/pull/42"}
	if len(notifier.calls) != 1 || notifier.calls[0] != want {
		t.Fatalf("notifier calls = %+v, want [%+v]", notifier.calls, want)
	}
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR should be removed after external merge detection")
	}
}

// TestNotifyLinearDone_OncePerPR: a second merge tick (re-entry) must not call
// the notifier again, and the flag survives a store round-trip.
func TestNotifyLinearDone_OncePerPR(t *testing.T) {
	store := newTestStateStore(t)
	c := NewController(DefaultConfig(), nil, nil, "owner", "repo")
	c.SetStateStore(store)
	notifier := &fakeLinearDoneNotifier{}
	c.SetLinearDoneNotifier(notifier)

	prState := &PRState{
		PRNumber:   42,
		PRURL:      "https://github.com/owner/repo/pull/42",
		BranchName: "pilot/LIN-QF-295",
		Stage:      StageWaitingCI,
		CreatedAt:  time.Now().Truncate(time.Second),
	}

	c.notifyLinearDone(context.Background(), prState)
	if len(notifier.calls) != 1 || !prState.LinearDoneNotified {
		t.Fatalf("after first call: calls=%d notified=%v", len(notifier.calls), prState.LinearDoneNotified)
	}

	loaded, err := store.GetPRState("owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if loaded == nil || !loaded.LinearDoneNotified {
		t.Fatalf("LinearDoneNotified did not persist: %+v", loaded)
	}
	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil {
		t.Fatalf("LoadAllPRStates: %v", err)
	}
	if len(all) != 1 || !all[0].LinearDoneNotified {
		t.Fatalf("LoadAllPRStates did not preserve LinearDoneNotified: %+v", all)
	}

	c.notifyLinearDone(context.Background(), loaded)
	c.notifyLinearDone(context.Background(), prState)
	if len(notifier.calls) != 1 {
		t.Errorf("notifier calls = %d after re-entry, want 1", len(notifier.calls))
	}
}

// TestNotifyLinearDone_NilNotifier: an unwired controller is a no-op and does
// not set the attempt flag.
func TestNotifyLinearDone_NilNotifier(t *testing.T) {
	c := NewController(DefaultConfig(), nil, nil, "owner", "repo")
	prState := &PRState{PRNumber: 1, BranchName: "pilot/LIN-QF-1"}
	c.notifyLinearDone(context.Background(), prState)
	if prState.LinearDoneNotified {
		t.Error("LinearDoneNotified set without a notifier")
	}
}
