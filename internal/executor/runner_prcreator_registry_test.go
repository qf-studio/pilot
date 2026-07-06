package executor

import (
	"context"
	"testing"
)

// TestRegisterPRCreator_KeyedLookup covers the M7 4d.4 registry: startup-time
// per-repo registration, exact-key lookup, nil on miss.
func TestRegisterPRCreator_KeyedLookup(t *testing.T) {
	r := &Runner{}
	if got := r.prCreatorFor("github:o/r"); got != nil {
		t.Fatal("empty registry must return nil")
	}

	fake := prCreatorFunc(func() string { return "https://x/pull/1" })
	r.RegisterPRCreator("github:o/r", fake)

	if got := r.prCreatorFor("github:o/r"); got == nil {
		t.Fatal("registered creator not found")
	}
	if got := r.prCreatorFor("github:other/repo"); got != nil {
		t.Fatal("lookup must be exact-key — other repos get the gh-CLI fallback")
	}
	if got := r.prCreatorFor("gitlab:o/r"); got != nil {
		t.Fatal("adapter prefix must namespace the key")
	}
}

// prCreatorFunc is a minimal PRCreator stub.
type prCreatorFunc func() string

func (f prCreatorFunc) CreatePR(_ context.Context, _, _, _, _ string) (string, error) {
	return f(), nil
}
