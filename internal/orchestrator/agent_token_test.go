package orchestrator

import "testing"

// TestAgentTokenRepoLookupReachesRunner (TASK-651): the repository lookup of
// the read token that the orchestrator gets is the one its runner asks, so a
// run_as_user run of the pilot instance gets the token of its project.
func TestAgentTokenRepoLookupReachesRunner(t *testing.T) {
	orch, err := NewOrchestrator(&Config{MaxConcurrent: 1}, nil)
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}
	defer orch.Stop()

	var asked []string
	orch.SetAgentTokenRepoLookup(func(path string) (string, bool) {
		asked = append(asked, path)
		return "OWNER-A/NAME", true
	})
	lookup := orch.runner.AgentTokenRepoLookup()
	if lookup == nil {
		t.Fatal("the runner of the orchestrator has no repository lookup")
	}
	if repo, entry := lookup("/srv/a"); repo != "OWNER-A/NAME" || !entry {
		t.Errorf("runner lookup(/srv/a) = %q, %v, want OWNER-A/NAME, true", repo, entry)
	}
	if len(asked) != 1 || asked[0] != "/srv/a" {
		t.Errorf("the lookup of the orchestrator was asked for %q, want once for /srv/a", asked)
	}
}
