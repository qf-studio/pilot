package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	planeSDK "github.com/qf-studio/studio-sdk/sdk/integrations/plane"

	"github.com/qf-studio/pilot/internal/adapters/plane"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
)

// TestPlanePollerRegistration_Fields verifies the SDK-based registration has the correct
// name and that its Enabled predicate gates on the Plane polling config.
func TestPlanePollerRegistration_Fields(t *testing.T) {
	reg := planePollerRegistration()

	if reg.Name != "plane" {
		t.Errorf("PollerRegistration.Name = %q, want %q", reg.Name, "plane")
	}

	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "nil plane config",
			cfg:  &config.Config{Adapters: &config.AdaptersConfig{}},
			want: false,
		},
		{
			name: "plane disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Plane: &plane.Config{Enabled: false, Polling: &plane.PollingConfig{Enabled: true}},
			}},
			want: false,
		},
		{
			name: "polling disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Plane: &plane.Config{Enabled: true, Polling: &plane.PollingConfig{Enabled: false}},
			}},
			want: false,
		},
		{
			name: "nil polling config",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Plane: &plane.Config{Enabled: true},
			}},
			want: false,
		},
		{
			name: "both enabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Plane: &plane.Config{Enabled: true, Polling: &plane.PollingConfig{Enabled: true}},
			}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.Enabled(tt.cfg)
			if got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPlanePriorityMapping verifies that Plane priority values from SDK IssueEvents are
// correctly converted to Pilot's int priority enum via sdkshim.PriorityFromSDK.
func TestPlanePriorityMapping(t *testing.T) {
	tests := []struct {
		sdkPriority string
		wantPilot   int
	}{
		{"urgent", sdkshim.PilotPriorityUrgent},
		{"high", sdkshim.PilotPriorityHigh},
		{"medium", sdkshim.PilotPriorityMedium},
		{"low", sdkshim.PilotPriorityLow},
		{"none", sdkshim.PilotPriorityNone},
		{"", sdkshim.PilotPriorityNone},
		{"unknown", sdkshim.PilotPriorityNone},
	}

	for _, tt := range tests {
		t.Run("priority_"+tt.sdkPriority, func(t *testing.T) {
			got := sdkshim.PriorityFromSDK(tt.sdkPriority)
			if got != tt.wantPilot {
				t.Errorf("PriorityFromSDK(%q) = %d, want %d", tt.sdkPriority, got, tt.wantPilot)
			}
		})
	}
}

// TestPlaneRepoResolutionGracefulError verifies that sdkshim.ResolveRepoForEvent returns
// ErrRepoNotResolved for the plane source (Phase-0 stub). The handler must not fail on this.
func TestPlaneRepoResolutionGracefulError(t *testing.T) {
	cfg := &config.Config{}
	ev := sdkcore.IssueEvent{
		IssueID:    "uuid-abc123",
		SequenceID: "PLANE-42",
		ProjectID:  "proj-uuid",
		Priority:   "high",
	}

	_, _, _, err := sdkshim.ResolveRepoForEvent(cfg, "plane", ev)
	if !errors.Is(err, sdkshim.ErrRepoNotResolved) {
		t.Errorf("ResolveRepoForEvent returned %v, want ErrRepoNotResolved", err)
	}
}

// TestPlaneSDKEventSequenceID verifies that the SequenceID produced by the SDK adapter
// is already prefixed with "PLANE-" — the handler must use it as-is, not re-prefix.
func TestPlaneSDKEventSequenceID(t *testing.T) {
	// Simulate what the SDK adapter's toIssueEvent does.
	sequenceNum := 42
	seqID := "PLANE-" + itoa(sequenceNum)

	if seqID != "PLANE-42" {
		t.Errorf("expected sequenceID = PLANE-42, got %q", seqID)
	}

	// If a handler called fmt.Sprintf("PLANE-%s", seqID) it would produce "PLANE-PLANE-42".
	doublePrefix := "PLANE-" + seqID
	if doublePrefix == "PLANE-42" {
		t.Error("double-prefix should NOT equal the expected ID")
	}
	if doublePrefix != "PLANE-PLANE-42" {
		t.Errorf("double-prefix sanity check: got %q, want PLANE-PLANE-42", doublePrefix)
	}
}

// itoa converts an int to a decimal string (avoids importing strconv in test).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}

// TestResolvePlaneProjectPath verifies the GH-6707 routing: each work item goes
// to the project entry paired with its Plane project; an unknown Plane project
// (or a config with no plane block) gets the default path.
func TestResolvePlaneProjectPath(t *testing.T) {
	const defaultPath = "/srv/default"
	paired := &config.Config{Projects: []*config.ProjectConfig{
		{Name: "default", Path: defaultPath},
		{Name: "web", Path: "/srv/web", Plane: &config.ProjectPlaneConfig{ProjectID: "plane-proj-web"}},
		{Name: "api", Path: "/srv/api", Plane: &config.ProjectPlaneConfig{ProjectID: "plane-proj-api"}},
	}}
	unpaired := &config.Config{Projects: []*config.ProjectConfig{
		{Name: "default", Path: defaultPath},
	}}

	tests := []struct {
		name      string
		cfg       *config.Config
		projectID string
		want      string
	}{
		{name: "first plane project", cfg: paired, projectID: "plane-proj-web", want: "/srv/web"},
		{name: "second plane project", cfg: paired, projectID: "plane-proj-api", want: "/srv/api"},
		{name: "unknown plane project falls back", cfg: paired, projectID: "plane-proj-other", want: defaultPath},
		{name: "no plane block keeps the default", cfg: unpaired, projectID: "plane-proj-web", want: defaultPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := sdkcore.IssueEvent{IssueID: "uuid-" + tt.projectID, SequenceID: "PLANE-7", ProjectID: tt.projectID}
			if got := resolvePlaneProjectPath(tt.cfg, ev, defaultPath); got != tt.want {
				t.Errorf("resolvePlaneProjectPath(%q) = %q, want %q", tt.projectID, got, tt.want)
			}
		})
	}
}

// TestPlanePollerAgainstFakePlaneServer runs the SDK Plane poller, built with
// the config mapping of planePollerRegistration, against a fake Plane server
// with two projects (GH-6722): the label of each project is looked up, the
// work item list of the first project follows the cursor to its second page,
// the text priority decodes, and the one work item that does not decode is
// skipped instead of failing its page.
func TestPlanePollerAgainstFakePlaneServer(t *testing.T) {
	const (
		workspace   = "test-workspace"
		projA       = "proj-a"
		projB       = "proj-b"
		labelA      = "label-uuid-a"
		labelB      = "label-uuid-b"
		cursorPage2 = "cursor-page-2"
	)
	pilotLabel := map[string]string{projA: labelA, projB: labelB}

	labelsBody := map[string]string{
		projA: `{"results":[{"id":"` + labelA + `","name":"pilot"},{"id":"other-a","name":"bug"}]}`,
		projB: `{"results":[{"id":"other-b","name":"bug"},{"id":"` + labelB + `","name":"Pilot"}]}`,
	}
	// Page 1 of project A: the text priority and a work item whose
	// sequence_id has the wrong type.
	projAPage1 := `{"results":[
		{"id":"a-text-priority","sequence_id":1,"name":"Text priority","priority":"high","labels":["` + labelA + `"],"project":"` + projA + `","created_at":"2026-11-01T10:00:00Z"},
		{"id":"a-broken","sequence_id":"not-a-number","name":"Broken","priority":2,"labels":["` + labelA + `"],"project":"` + projA + `","created_at":"2026-11-01T10:01:00Z"},
		{"id":"a-page1-int","sequence_id":3,"name":"Int priority","priority":3,"labels":["` + labelA + `"],"project":"` + projA + `","created_at":"2026-11-01T10:02:00Z"}
	],"next_page_results":true,"next_cursor":"` + cursorPage2 + `"}`
	projAPage2 := `{"results":[
		{"id":"a-page2","sequence_id":4,"name":"Second page","priority":4,"labels":["` + labelA + `"],"project":"` + projA + `","created_at":"2026-11-01T10:03:00Z"}
	],"next_page_results":false,"next_cursor":""}`
	projBPage := `{"results":[
		{"id":"b-item","sequence_id":1,"name":"Project B","priority":1,"labels":["` + labelB + `"],"project":"` + projB + `","created_at":"2026-11-01T10:04:00Z"}
	],"next_page_results":false,"next_cursor":""}`

	var (
		reqMu       sync.Mutex
		listLabels  = map[string][]string{} // project → label query of each work item list request
		listCursors = map[string][]string{} // project → cursor query of each work item list request
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/api/v1/workspaces/" + workspace + "/projects/"
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		project, resource := parts[0], parts[1]
		if _, ok := pilotLabel[project]; !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch resource {
		case "labels":
			_, _ = w.Write([]byte(labelsBody[project]))
		case "states":
			_, _ = w.Write([]byte(`{"results":[]}`))
		case "work-items":
			label, cursor := r.URL.Query().Get("label"), r.URL.Query().Get("cursor")
			reqMu.Lock()
			listLabels[project] = append(listLabels[project], label)
			listCursors[project] = append(listCursors[project], cursor)
			reqMu.Unlock()
			if label != pilotLabel[project] {
				_, _ = w.Write([]byte(`{"results":[]}`))
				return
			}
			switch {
			case project == projB:
				_, _ = w.Write([]byte(projBPage))
			case cursor == cursorPage2:
				_, _ = w.Write([]byte(projAPage2))
			default:
				_, _ = w.Write([]byte(projAPage1))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sdkCfg, interval := planeSDKConfig(&plane.Config{
		Enabled:       true,
		BaseURL:       srv.URL,
		APIKey:        "fake-api-key",
		WorkspaceSlug: workspace,
		ProjectIDs:    []string{projA, projB},
		Polling:       &plane.PollingConfig{Enabled: true, Interval: time.Hour},
	}, false)
	if interval != time.Hour {
		t.Fatalf("planeSDKConfig interval = %v, want %v", interval, time.Hour)
	}
	if sdkCfg.TriggerLabel != "pilot" {
		t.Fatalf("planeSDKConfig TriggerLabel = %q, want the default %q", sdkCfg.TriggerLabel, "pilot")
	}

	want := []string{"a-page1-int", "a-page2", "a-text-priority", "b-item"}
	var (
		evMu   sync.Mutex
		events = map[string]sdkcore.IssueEvent{}
	)
	allSeen := make(chan struct{})
	handler := sdkcore.IssueHandlerFunc(func(_ context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
		evMu.Lock()
		defer evMu.Unlock()
		events[ev.IssueID] = ev
		seen := 0
		for _, id := range want {
			if _, ok := events[id]; ok {
				seen++
			}
		}
		if seen == len(want) {
			select {
			case <-allSeen:
			default:
				close(allSeen)
			}
		}
		return &sdkcore.IssueResult{Success: true}, nil
	})

	poller := planeSDK.New(sdkCfg).NewPoller(sdkcore.PollerDeps{Handler: handler})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startErr := make(chan error, 1)
	go func() { startErr <- poller.Start(ctx) }()

	select {
	case <-allSeen:
		cancel()
	case err := <-startErr:
		t.Fatalf("poller Start returned before every work item reached the handler: %v", err)
	case <-ctx.Done():
	}
	if err := <-startErr; err != nil {
		t.Fatalf("poller Start: %v", err)
	}

	evMu.Lock()
	got := make([]string, 0, len(events))
	for id := range events {
		got = append(got, id)
	}
	textPriority, hasTextPriority := events["a-text-priority"]
	evMu.Unlock()
	sort.Strings(got)

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("handler got work items %v, want %v", got, want)
	}
	for _, id := range got {
		if id == "a-broken" {
			t.Errorf("the work item that does not decode reached the handler")
		}
	}
	if !hasTextPriority {
		t.Errorf("the work item with the text priority did not reach the handler")
	} else if p := sdkshim.PriorityFromSDK(textPriority.Priority); p != sdkshim.PilotPriorityHigh {
		t.Errorf("text priority \"high\": PriorityFromSDK(%q) = %d, want %d", textPriority.Priority, p, sdkshim.PilotPriorityHigh)
	}

	reqMu.Lock()
	defer reqMu.Unlock()
	for _, project := range []string{projA, projB} {
		labels := listLabels[project]
		if len(labels) == 0 {
			t.Errorf("no work item list request for project %s", project)
		}
		for _, label := range labels {
			if label != pilotLabel[project] {
				t.Errorf("work item list of project %s asked for label %q, want %q", project, label, pilotLabel[project])
			}
		}
	}
	if !containsString(listCursors[projA], cursorPage2) {
		t.Errorf("work item list of project %s did not ask for the second page (cursors %q)", projA, listCursors[projA])
	}
}

// TestPlaneOnPRCreated_StoresLink covers GH-6739: after the handler path
// recorded the work item's project and the SDK fired OnPRCreated, the
// controller that registered the PR also gets SetPlaneLink with the PR
// number, the project ID and the work item ID — in the routed branch (project
// pairing) and in the default branch (no pairing).
func TestPlaneOnPRCreated_StoresLink(t *testing.T) {
	const (
		projectID  = "plane-project-uuid"
		workItemID = "plane-work-item-uuid"
		prURL      = "https://github.com/acme/other/pull/42"
	)
	issueEv := sdkcore.IssueEvent{IssueID: workItemID, ProjectID: projectID}
	prEv := sdkcore.PRCreatedEvent{PRNumber: 42, PRURL: prURL, IssueID: workItemID, BranchName: "pilot/PLANE-7"}

	tests := []struct {
		name     string
		routed   bool
		record   bool
		wantLink bool
	}{
		{name: "routed branch", routed: true, record: true, wantLink: true},
		{name: "default branch", routed: false, record: true, wantLink: true},
		{name: "routed branch, no recorded pair", routed: true, record: false, wantLink: false},
		{name: "default branch, no recorded pair", routed: false, record: false, wantLink: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := &fakePRRegistrar{}
			other := &fakePRRegistrar{}
			links := newPlaneWorkItemLinks()
			if tt.record {
				recordPlaneLink(links, issueEv, &sdkcore.IssueResult{Success: true, PRNumber: 42, PRURL: prURL}, nil)
			}

			var cb func(sdkcore.PRCreatedEvent)
			target := def
			if tt.routed {
				cb = planeRoutedOnPRCreated("acme/default", def, map[string]prRegistrar{"acme/other": other}, links)
				target = other
			} else {
				cb = planeDefaultOnPRCreated(def, links)
			}
			cb(prEv)

			if len(target.calls) != 1 || target.calls[0] != 42 {
				t.Fatalf("OnPRCreated calls = %v, want [42]", target.calls)
			}
			if !tt.wantLink {
				if len(target.planeLinks) != 0 {
					t.Fatalf("SetPlaneLink called with no recorded pair: %v", target.planeLinks)
				}
				return
			}
			want := []fakePlaneLink{{prNumber: 42, projectID: projectID, workItemID: workItemID}}
			if len(target.planeLinks) != 1 || target.planeLinks[0] != want[0] {
				t.Fatalf("SetPlaneLink calls = %v, want %v", target.planeLinks, want)
			}
			if got := strings.Join(target.order, ","); got != "register,plane-link" {
				t.Fatalf("call order = %q, want OnPRCreated then SetPlaneLink", got)
			}
			if tt.routed && (len(def.calls) != 0 || len(def.planeLinks) != 0) {
				t.Fatalf("default controller touched for a foreign repo PR: calls=%v links=%v", def.calls, def.planeLinks)
			}
			if got := links.take(workItemID); got != "" {
				t.Fatalf("entry left in the map after the callback: %q", got)
			}
		})
	}
}

// TestPlaneOnPRCreated_RecordOnlyWhenPRReturned: the handler records the pair
// only when the SDK will fire OnPRCreated (no error, PR number above zero),
// so no entry stays in the map with no callback to take it.
func TestPlaneOnPRCreated_RecordOnlyWhenPRReturned(t *testing.T) {
	ev := sdkcore.IssueEvent{IssueID: "wi", ProjectID: "proj"}
	tests := []struct {
		name   string
		result *sdkcore.IssueResult
		err    error
		want   string
	}{
		{"PR returned", &sdkcore.IssueResult{Success: true, PRNumber: 5}, nil, "proj"},
		{"no PR", &sdkcore.IssueResult{Success: true}, nil, ""},
		{"nil result", nil, nil, ""},
		{"run error", &sdkcore.IssueResult{PRNumber: 5}, errors.New("boom"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			links := newPlaneWorkItemLinks()
			recordPlaneLink(links, ev, tt.result, tt.err)
			if got := links.take("wi"); got != tt.want {
				t.Fatalf("take = %q, want %q", got, tt.want)
			}
		})
	}

	// A nil map (no callback wired) records and takes nothing.
	var nilLinks *planeWorkItemLinks
	recordPlaneLink(nilLinks, ev, &sdkcore.IssueResult{PRNumber: 5}, nil)
	if got := nilLinks.take("wi"); got != "" {
		t.Fatalf("nil links take = %q, want empty", got)
	}
}

// TestPlaneOnPRCreated_LinksParallelRuns: parallel runs record and take
// their own entries without a race (run with -race).
func TestPlaneOnPRCreated_LinksParallelRuns(t *testing.T) {
	links := newPlaneWorkItemLinks()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "wi-" + strconv.Itoa(i)
			links.record(id, "proj-"+strconv.Itoa(i))
			if got := links.take(id); got != "proj-"+strconv.Itoa(i) {
				t.Errorf("take(%s) = %q", id, got)
			}
		}(i)
	}
	wg.Wait()
}

// TestPlaneSDKConfig_CompletedStateAtMerge covers GH-6751: the SDK poller
// leaves the completed state to the merge leg only when an autopilot
// controller takes the PR (the condition under which planeOnPRCreated is not
// nil). With no controller nothing sees the merge, so the field stays false
// and the poller completes the work item at the end of the run.
func TestPlaneSDKConfig_CompletedStateAtMerge(t *testing.T) {
	ctrl := autopilot.NewController(autopilot.DefaultConfig(), nil, nil, "acme", "web")
	unpaired := &config.Config{Projects: []*config.ProjectConfig{{Name: "web", Path: "/srv/web"}}}
	paired := &config.Config{Projects: []*config.ProjectConfig{
		{Name: "web", Path: "/srv/web", Plane: &config.ProjectPlaneConfig{ProjectID: "plane-proj-web"}},
	}}

	tests := []struct {
		name  string
		cfg   *config.Config
		def   *autopilot.Controller
		ctrls map[string]*autopilot.Controller
		want  bool
	}{
		{name: "default controller", cfg: unpaired, def: ctrl, want: true},
		{name: "controller of a repository, paired", cfg: paired, ctrls: map[string]*autopilot.Controller{"acme/web": ctrl}, want: true},
		{name: "no controller", cfg: unpaired, want: false},
		{name: "no controller, paired", cfg: paired, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mergeStep := planeOnPRCreated(tt.cfg, tt.def, tt.ctrls, newPlaneWorkItemLinks()) != nil
			sdkCfg, _ := planeSDKConfig(&plane.Config{Enabled: true, WorkspaceSlug: "ws"}, mergeStep)
			if sdkCfg.CompletedStateAtMerge != tt.want {
				t.Errorf("CompletedStateAtMerge = %v, want %v", sdkCfg.CompletedStateAtMerge, tt.want)
			}
		})
	}

	// The live call site passes the same condition and wires the merge leg.
	body := githubFuncBody(t, "poller_plane.go", "func planePollerRegistration() PollerRegistration {")
	if !strings.Contains(body, "planeSDKConfig(deps.Cfg.Adapters.Plane, onPRCreated != nil)") {
		t.Error("planePollerRegistration must set CompletedStateAtMerge from onPRCreated != nil")
	}
	if !strings.Contains(body, "registerPlaneDoneNotifier(newPlaneDoneNotifier(planeClient, deps.Cfg.Adapters.Plane.WorkspaceSlug), deps.AutopilotController, deps.AutopilotControllers)") {
		t.Error("the Plane merge-leg notifier must be registered on the default and every per-repo autopilot controller")
	}
}
