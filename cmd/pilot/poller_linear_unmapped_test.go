package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	linearSDK "github.com/qf-studio/studio-sdk/sdk/integrations/linear"

	"github.com/qf-studio/pilot/internal/config"
)

// fakeLinearServer is a minimal Linear GraphQL endpoint: enough of the label,
// issue-list and comment queries for the real studio-sdk poller to run against.
type fakeLinearServer struct {
	mu          sync.Mutex
	labelled    bool     // when true the listed issue carries repo:api
	comments    []string // bodies posted via commentCreate
	listCalls   int
	commentRead int
}

func (f *fakeLinearServer) setLabelled(v bool) {
	f.mu.Lock()
	f.labelled = v
	f.mu.Unlock()
}

func (f *fakeLinearServer) commentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.comments)
}

func (f *fakeLinearServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	reply := func(data string) {
		_, _ = w.Write([]byte(`{"data":` + data + `}`))
	}
	issueJSON := func(labels []string) string {
		nodes := make([]string, 0, len(labels))
		for _, l := range labels {
			nodes = append(nodes, `{"id":"lbl-`+l+`","name":"`+l+`"}`)
		}
		return `{"id":"issue-uuid-1","identifier":"APP-1","title":"t","description":"d","priority":0,` +
			`"state":{"id":"s","name":"Todo","type":"unstarted"},"labels":{"nodes":[` + strings.Join(nodes, ",") + `]},` +
			`"team":{"id":"team-uuid","name":"App","key":"APP"},"url":"u",` +
			`"createdAt":"2026-10-01T00:00:00Z","updatedAt":"2026-10-01T00:00:00Z"}`
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.Contains(req.Query, "query GetLabel("):
		name, _ := req.Variables["name"].(string)
		reply(`{"issueLabels":{"nodes":[{"id":"lbl-` + name + `","name":"` + name + `"}]}}`)
	case strings.Contains(req.Query, "query ListIssues"):
		if req.Variables["label"] != "pilot" {
			reply(`{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}`)
			return
		}
		f.listCalls++
		labels := []string{"pilot"}
		if f.labelled {
			labels = append(labels, "repo:api")
		}
		reply(`{"issues":{"nodes":[` + issueJSON(labels) + `],"pageInfo":{"hasNextPage":false,"endCursor":""}}}`)
	case strings.Contains(req.Query, "query GetIssue"):
		reply(`{"issue":` + issueJSON([]string{"pilot"}) + `}`)
	case strings.Contains(req.Query, "query IssueComments"):
		f.commentRead++
		nodes := make([]string, 0, len(f.comments))
		for _, c := range f.comments {
			b, _ := json.Marshal(c)
			nodes = append(nodes, `{"body":`+string(b)+`}`)
		}
		reply(`{"issue":{"comments":{"nodes":[` + strings.Join(nodes, ",") + `],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}`)
	case strings.Contains(req.Query, "mutation CreateComment"):
		body, _ := req.Variables["body"].(string)
		f.comments = append(f.comments, body)
		reply(`{"commentCreate":{"success":true}}`)
	default: // AddLabel / RemoveLabel
		reply(`{"ok":{"success":true}}`)
	}
}

// memProcessedStore is an in-memory linear ProcessedStore that records the
// persisted state, so the test can assert a skipped issue is not left marked.
type memProcessedStore struct {
	mu  sync.Mutex
	ids map[string]time.Time
}

func (s *memProcessedStore) Mark(_, _, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = time.Now()
	return nil
}

func (s *memProcessedStore) Unmark(_, _, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
	return nil
}

func (s *memProcessedStore) IsProcessed(_, _, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.ids[id]
	return ok, nil
}

func (s *memProcessedStore) Load(_, _ string) (map[string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]time.Time, len(s.ids))
	for k, v := range s.ids {
		out[k] = v
	}
	return out, nil
}

// TestLinearNoProjectMappingSkip_IsRetriedAfterLabel runs the real studio-sdk
// Linear poller (against a fake Linear API) with the production handler. A
// no-match issue must yield exactly one Linear comment across three polls and
// must not stay persisted as processed; once a repo label is added the fourth
// poll dispatches it (GH-5576). If the handler stops clearing the processed
// mark, the poller never re-examines the issue and the dispatch never happens.
func TestLinearNoProjectMappingSkip_IsRetriedAfterLabel(t *testing.T) {
	fake := &fakeLinearServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	cfg := &config.Config{Projects: []*config.ProjectConfig{
		{Name: "api", Path: "/repos/api"},
		{Name: "client", Path: "/repos/client"},
	}}
	client := linearSDK.NewClientWithBaseURL("test-linear-key", srv.URL)
	store := &memProcessedStore{ids: map[string]time.Time{}}
	ws := &linearSDK.WorkspaceConfig{Name: "ws", APIKey: "test-linear-key", TeamID: "APP", TriggerLabel: "pilot"}

	var handlerCalls atomic.Int32
	dispatched := make(chan string, 4)
	commentsAtDispatch := make(chan int, 1)

	var poller *linearSDK.Poller
	handler := newLinearWorkspaceHandler(cfg, ws, client, newLinearUnmappedNotifier(client, cfg),
		func(id string) { poller.ClearProcessed(id) },
		func(_ context.Context, _ sdkcore.IssueEvent, path string) (*sdkcore.IssueResult, error) {
			commentsAtDispatch <- fake.commentCount()
			dispatched <- path
			return &sdkcore.IssueResult{Success: true}, nil
		})

	poller = linearSDK.NewPoller(client, ws, 30*time.Millisecond,
		linearSDK.WithProcessedStore(store),
		linearSDK.WithOnLinearIssue(func(ctx context.Context, issue *linearSDK.Issue) (*linearSDK.IssueResult, error) {
			labels := make([]string, 0, len(issue.Labels))
			for _, l := range issue.Labels {
				labels = append(labels, l.Name)
			}
			// After the third skipped poll the operator adds the repo label.
			if handlerCalls.Add(1) == 3 {
				defer fake.setLabelled(true)
			}
			res, err := handler(ctx, sdkcore.IssueEvent{
				IssueID:    issue.ID,
				SequenceID: "LIN-" + issue.Identifier,
				Labels:     labels,
				ProjectID:  issue.Team.ID,
			})
			if err != nil || res == nil {
				return nil, err
			}
			return &linearSDK.IssueResult{Success: res.Success}, nil
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = poller.Start(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	select {
	case path := <-dispatched:
		if path != "/repos/api" {
			t.Errorf("dispatched to %q, want /repos/api", path)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("issue never dispatched after the repo label was added (handler calls: %d); a skipped issue stayed marked processed", handlerCalls.Load())
	}

	if got := <-commentsAtDispatch; got != 1 {
		t.Errorf("comments before dispatch = %d, want exactly 1 across the skipped polls", got)
	}
	if n := handlerCalls.Load(); n < 4 {
		t.Errorf("handler calls = %d, want >= 4 (three skips, then the dispatch)", n)
	}

	fake.mu.Lock()
	body := ""
	if len(fake.comments) > 0 {
		body = fake.comments[0]
	}
	fake.mu.Unlock()
	for _, want := range []string{linearNoMappingMarker, "`repo:api`", "`repo:client`", "linear.project_id"} {
		if !strings.Contains(body, want) {
			t.Errorf("comment missing %q:\n%s", want, body)
		}
	}
}

// TestLinearNoProjectMappingSkip_NotPersisted pins that a skipped issue is not
// left in the processed store (the persisted mark is what makes a skip permanent
// across restarts).
func TestLinearNoProjectMappingSkip_NotPersisted(t *testing.T) {
	fake := &fakeLinearServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	cfg := &config.Config{Projects: []*config.ProjectConfig{{Name: "api", Path: "/repos/api"}}}
	client := linearSDK.NewClientWithBaseURL("test-linear-key", srv.URL)
	store := &memProcessedStore{ids: map[string]time.Time{}}
	ws := &linearSDK.WorkspaceConfig{Name: "ws", APIKey: "test-linear-key", TeamID: "APP", TriggerLabel: "pilot"}

	called := make(chan struct{}, 1)
	var poller *linearSDK.Poller
	handler := newLinearWorkspaceHandler(cfg, ws, client, newLinearUnmappedNotifier(client, cfg),
		func(id string) { poller.ClearProcessed(id) },
		func(context.Context, sdkcore.IssueEvent, string) (*sdkcore.IssueResult, error) {
			t.Error("dispatch called for an unmapped issue")
			return nil, nil
		})
	poller = linearSDK.NewPoller(client, ws, time.Hour,
		linearSDK.WithProcessedStore(store),
		linearSDK.WithOnLinearIssue(func(ctx context.Context, issue *linearSDK.Issue) (*linearSDK.IssueResult, error) {
			defer func() { called <- struct{}{} }()
			_, err := handler(ctx, sdkcore.IssueEvent{IssueID: issue.ID, SequenceID: "LIN-" + issue.Identifier, Labels: []string{"pilot"}})
			return nil, err
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = poller.Start(ctx)
	}()
	select {
	case <-called:
	case <-time.After(10 * time.Second):
		t.Fatal("handler never called")
	}
	cancel()
	<-done

	if ok, _ := store.IsProcessed("linear", "", "issue-uuid-1"); ok {
		t.Error("skipped issue is still persisted as processed")
	}
	if poller.IsProcessed("issue-uuid-1") {
		t.Error("skipped issue is still marked processed in memory")
	}
}

// TestLinearUnmappedNotifier covers the once-per-issue guard directly.
func TestLinearUnmappedNotifier(t *testing.T) {
	cfg := &config.Config{Projects: []*config.ProjectConfig{{Name: "api", Path: "/repos/api"}}}
	ev := sdkcore.IssueEvent{IssueID: "issue-uuid-1", SequenceID: "LIN-APP-1"}

	t.Run("existing marker comment prevents a new one after restart", func(t *testing.T) {
		fake := &fakeLinearServer{comments: []string{"human comment", "**x** (" + linearNoMappingMarker + ")"}}
		srv := httptest.NewServer(fake)
		defer srv.Close()
		n := newLinearUnmappedNotifier(linearSDK.NewClientWithBaseURL("test-linear-key", srv.URL), cfg)
		n.notify(context.Background(), ev)
		if got := fake.commentCount(); got != 2 {
			t.Errorf("comments = %d, want 2 (no new comment)", got)
		}
	})

	t.Run("repeat notify does not re-read or re-post", func(t *testing.T) {
		fake := &fakeLinearServer{}
		srv := httptest.NewServer(fake)
		defer srv.Close()
		n := newLinearUnmappedNotifier(linearSDK.NewClientWithBaseURL("test-linear-key", srv.URL), cfg)
		for i := 0; i < 3; i++ {
			n.notify(context.Background(), ev)
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.comments) != 1 || fake.commentRead != 1 {
			t.Errorf("comments = %d, reads = %d, want 1 and 1", len(fake.comments), fake.commentRead)
		}
	})

	t.Run("failed comment read does not post", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		n := newLinearUnmappedNotifier(linearSDK.NewClientWithBaseURL("test-linear-key", srv.URL), cfg)
		n.notify(context.Background(), ev) // must not panic or mark as commented
		n.mu.Lock()
		defer n.mu.Unlock()
		if len(n.commented) != 0 {
			t.Error("issue marked commented despite a failed read")
		}
	})
}

func TestLinearNoMappingComment_ListsConfiguredProjects(t *testing.T) {
	body := linearNoMappingComment(linearRoutingConfig())
	for _, want := range []string{linearNoMappingMarker, "`repo:linearinvoices-api`", "`repo:linearinvoices-client`", "`repo:other`", "linear.project_id"} {
		if !strings.Contains(body, want) {
			t.Errorf("comment missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(linearNoMappingComment(&config.Config{}), "no Pilot projects are configured") {
		t.Error("empty config should say no projects are configured")
	}
}
