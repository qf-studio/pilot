package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
)

type fakePRRegistrar struct {
	calls []int
	urls  []string
}

func (f *fakePRRegistrar) OnPRCreated(prNumber int, prURL string, _ int, _ string, _ string, _ string) {
	f.calls = append(f.calls, prNumber)
	f.urls = append(f.urls, prURL)
}

func TestResolvePRController(t *testing.T) {
	def := &fakePRRegistrar{}
	other := &fakePRRegistrar{}
	ctrls := map[string]prRegistrar{"acme/default": def, "acme/other": other}

	tests := []struct {
		name       string
		url        string
		wantCtrl   prRegistrar
		wantNumber int
		wantLog    string
	}{
		{"default repo URL", "https://github.com/acme/default/pull/12", def, 12, ""},
		{"other registered repo", "https://github.com/acme/other/pull/34", other, 34, ""},
		{"case-insensitive repo", "https://github.com/Acme/Other/pull/5", other, 5, ""},
		{"unregistered repo", "https://github.com/acme/unknown/pull/7", nil, 0, "no autopilot controller for repo acme/unknown"},
		{"GitLab MR URL", "https://gitlab.com/acme/other/-/merge_requests/9", nil, 0, "not a GitHub PR URL"},
		{"malformed URL", "not a url", nil, 0, "not a GitHub PR URL"},
		{"empty URL", "", nil, 0, "not a GitHub PR URL"},
		{"issue URL not PR", "https://github.com/acme/other/issues/3", nil, 0, "not a GitHub PR URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			got, n := resolvePRController(log, "LIN-1", tt.url, "acme/default", def, ctrls)
			if got != tt.wantCtrl || n != tt.wantNumber {
				t.Fatalf("got (%v, %d), want (%v, %d)", got, n, tt.wantCtrl, tt.wantNumber)
			}
			out := buf.String()
			if tt.wantLog == "" {
				if out != "" {
					t.Fatalf("unexpected log: %s", out)
				}
				return
			}
			for _, want := range []string{"level=INFO", "task_id=LIN-1", "pr_url=", tt.wantLog} {
				if !strings.Contains(out, want) {
					t.Errorf("log %q missing %q", out, want)
				}
			}
		})
	}
}

// The default controller is used only when the URL's repo equals the default
// repo, even when it is absent from the per-repo map (gateway mode).
func TestResolvePRController_DefaultOnlyForDefaultRepo(t *testing.T) {
	def := &fakePRRegistrar{}
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	if got, _ := resolvePRController(log, "x", "https://github.com/acme/default/pull/1", "acme/default", def, nil); got != def {
		t.Error("default repo URL should resolve to the default controller")
	}
	if got, _ := resolvePRController(log, "x", "https://github.com/acme/other/pull/1", "acme/default", def, nil); got != nil {
		t.Error("non-default repo must not fall back to the default controller")
	}
	if got, _ := resolvePRController(log, "x", "https://github.com/acme/default/pull/1", "", def, nil); got != nil {
		t.Error("unknown default repo must not resolve the default controller")
	}
}

func TestLinearOnPRCreated_NeverRegistersForeignRepoWithDefault(t *testing.T) {
	def := &fakePRRegistrar{}
	other := &fakePRRegistrar{}
	cb := linearOnPRCreated("acme/default", def, map[string]prRegistrar{"acme/other": other})

	cb(sdkcore.PRCreatedEvent{PRNumber: 42, PRURL: "https://github.com/acme/other/pull/42", IssueID: "i1"})
	cb(sdkcore.PRCreatedEvent{PRNumber: 43, PRURL: "https://github.com/acme/unregistered/pull/43", IssueID: "i2"})
	cb(sdkcore.PRCreatedEvent{PRNumber: 44, PRURL: "https://gitlab.com/acme/x/-/merge_requests/44", IssueID: "i3"})

	if len(def.calls) != 0 {
		t.Fatalf("default controller received foreign PRs: %v", def.calls)
	}
	if len(other.calls) != 1 || other.calls[0] != 42 {
		t.Fatalf("other controller calls = %v, want [42]", other.calls)
	}

	cb(sdkcore.PRCreatedEvent{PRNumber: 7, PRURL: "https://github.com/acme/default/pull/7", IssueID: "i4"})
	if len(def.calls) != 1 || def.calls[0] != 7 {
		t.Fatalf("default controller calls = %v, want [7]", def.calls)
	}
}
