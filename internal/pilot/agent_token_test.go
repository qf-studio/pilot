package pilot

import "testing"

// TestAgentTokenRepoLookupReachesOrchestrator (TASK-651): the repository
// lookup of the read token that cmd/pilot hands the pilot instance
// (SetAgentTokenRepoLookup) is the one the runner of its orchestrator asks,
// so a run_as_user run of the gateway gets the token of its project.
func TestAgentTokenRepoLookupReachesOrchestrator(t *testing.T) {
	p, err := New(newTestPilotConfig(t, "test-github-token"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = p.Stop() }()

	if p.orchestrator.AgentTokenRepoLookup() != nil {
		t.Fatal("the orchestrator has a repository lookup before SetAgentTokenRepoLookup")
	}
	var asked []string
	p.SetAgentTokenRepoLookup(func(path string) (string, bool) {
		asked = append(asked, path)
		return "OWNER-A/NAME", true
	})
	lookup := p.orchestrator.AgentTokenRepoLookup()
	if lookup == nil {
		t.Fatal("the runner of the orchestrator has no repository lookup")
	}
	if repo, entry := lookup("/srv/a"); repo != "OWNER-A/NAME" || !entry {
		t.Errorf("runner lookup(/srv/a) = %q, %v, want OWNER-A/NAME, true", repo, entry)
	}
	if len(asked) != 1 || asked[0] != "/srv/a" {
		t.Errorf("the lookup of the pilot instance was asked for %q, want once for /srv/a", asked)
	}
}
