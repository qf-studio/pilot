package linear

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTransitions_YAMLParsesTopLevelAndPerWorkspace(t *testing.T) {
	const doc = `
enabled: true
api_key: legacy-key
team_id: LEG
transitions:
  in_progress: Doing
  done: Shipped
workspaces:
  - name: a
    api_key: key-a
    team_id: A
    transitions:
      done: Released
      failed: Blocked
  - name: b
    api_key: key-b
    team_id: B
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Transitions.InProgress != "Doing" || cfg.Transitions.Done != "Shipped" {
		t.Fatalf("top-level transitions = %+v", cfg.Transitions)
	}

	wss := cfg.GetWorkspaces()
	if len(wss) != 2 {
		t.Fatalf("workspaces = %d, want 2", len(wss))
	}
	// a: own done/failed override, in_progress inherited from the top level.
	want := Transitions{InProgress: "Doing", Done: "Released", Failed: "Blocked"}
	if wss[0].Transitions != want {
		t.Errorf("ws a transitions = %+v, want %+v", wss[0].Transitions, want)
	}
	// b: everything inherited.
	want = Transitions{InProgress: "Doing", Done: "Shipped"}
	if wss[1].Transitions != want {
		t.Errorf("ws b transitions = %+v, want %+v", wss[1].Transitions, want)
	}
	// The source config is not mutated.
	if cfg.Workspaces[1].Transitions != (Transitions{}) {
		t.Errorf("GetWorkspaces mutated config: %+v", cfg.Workspaces[1].Transitions)
	}
}

func TestTransitions_LegacyWorkspaceCarriesTopLevel(t *testing.T) {
	cfg := &Config{APIKey: "k", TeamID: "T", Transitions: Transitions{InReview: "In Review", Failed: "Canceled"}}
	wss := cfg.GetWorkspaces()
	if len(wss) != 1 {
		t.Fatalf("workspaces = %d, want 1", len(wss))
	}
	if got := wss[0].Transitions; got != cfg.Transitions {
		t.Errorf("legacy workspace transitions = %+v, want %+v", got, cfg.Transitions)
	}
}

func TestTransitions_NoTopLevelKeepsWorkspacePointers(t *testing.T) {
	ws := &WorkspaceConfig{Name: "x", Transitions: Transitions{Done: "Done"}}
	cfg := &Config{Workspaces: []*WorkspaceConfig{ws}}
	if got := cfg.GetWorkspaces(); got[0] != ws {
		t.Error("expected the original workspace pointer when no top-level transitions are set")
	}
}

func TestTransitions_Override(t *testing.T) {
	base := Transitions{InProgress: "A", Done: "B"}
	got := base.Override(Transitions{Done: "C", Failed: "D"})
	want := Transitions{InProgress: "A", Done: "C", Failed: "D"}
	if got != want {
		t.Errorf("Override = %+v, want %+v", got, want)
	}
}
