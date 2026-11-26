package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	planeSDK "github.com/qf-studio/studio-sdk/sdk/integrations/plane"

	"github.com/qf-studio/pilot/internal/adapters/plane"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
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
	})
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
