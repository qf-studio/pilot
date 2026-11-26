package pilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/plane"
	"github.com/qf-studio/pilot/internal/testutil"
)

// GH-6705: the webhook resolver is built from the SDK Plane client.
func TestPlaneLabelResolver(t *testing.T) {
	if planeLabelResolver(nil) != nil {
		t.Error("nil config: want no resolver")
	}
	if planeLabelResolver(&plane.Config{BaseURL: "http://unused"}) != nil {
		t.Error("no API key: want no resolver")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/ws/projects/proj-1/labels/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"bug-uuid","name":"bug"},{"id":"pilot-uuid","name":"Pilot"}]}`))
	}))
	defer srv.Close()

	resolve := planeLabelResolver(&plane.Config{BaseURL: srv.URL, APIKey: testutil.FakePlaneAPIKey, WorkspaceSlug: "ws"})
	if resolve == nil {
		t.Fatal("with API key: want a resolver")
	}
	id, err := resolve(context.Background(), "proj-1", "pilot")
	if err != nil || id != "pilot-uuid" {
		t.Errorf("resolve(pilot) = %q, %v; want pilot-uuid", id, err)
	}
	id, err = resolve(context.Background(), "proj-1", "missing")
	if err != nil || id != "" {
		t.Errorf("resolve(missing) = %q, %v; want empty", id, err)
	}
	if _, err := resolve(context.Background(), "proj-2", "pilot"); err == nil {
		t.Error("resolve on a failing project: want an error")
	}
}
