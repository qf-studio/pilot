package main

import (
	"sort"
	"sync"
	"testing"
)

// fakeMarker records MarkProcessed / ClearProcessed calls for assertions.
type fakeMarker struct {
	mu      sync.Mutex
	marked  []int
	cleared []int
}

func (f *fakeMarker) MarkProcessed(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, n)
}

func (f *fakeMarker) ClearProcessed(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, n)
}

func (f *fakeMarker) snapshotMarked() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]int(nil), f.marked...)
	sort.Ints(out)
	return out
}

func (f *fakeMarker) snapshotCleared() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]int(nil), f.cleared...)
	sort.Ints(out)
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// GH-4110 core guarantee: marking is scoped to the target repo so a sub-issue
// number in one repo cannot suppress a same-numbered issue in another.
func TestGithubPollerRegistry_MarkProcessedScopedByRepo(t *testing.T) {
	reg := newGithubPollerRegistry()
	a := &fakeMarker{}
	b := &fakeMarker{}
	reg.add("owner/a", a)
	reg.add("owner/b", b)

	reg.markProcessed("owner/a", 42)

	if got := a.snapshotMarked(); !equalInts(got, []int{42}) {
		t.Errorf("repo a marked = %v, want [42]", got)
	}
	if got := b.snapshotMarked(); len(got) != 0 {
		t.Errorf("repo b marked = %v, want [] (cross-repo leak)", got)
	}
}

// N pollers for the same repo (M7 4d.2 fan-out shape) all get marked.
func TestGithubPollerRegistry_MultiplePollersPerRepo(t *testing.T) {
	reg := newGithubPollerRegistry()
	p1 := &fakeMarker{}
	p2 := &fakeMarker{}
	reg.add("owner/a", p1)
	reg.add("owner/a", p2)

	reg.markProcessed("owner/a", 7)

	for i, p := range []*fakeMarker{p1, p2} {
		if got := p.snapshotMarked(); !equalInts(got, []int{7}) {
			t.Errorf("poller %d marked = %v, want [7]", i, got)
		}
	}
}

// Empty repo (unknown source repo) falls back to marking every poller so the
// skip guarantee is never lost — under-marking is the worse failure.
func TestGithubPollerRegistry_EmptyRepoMarksAll(t *testing.T) {
	reg := newGithubPollerRegistry()
	a := &fakeMarker{}
	b := &fakeMarker{}
	reg.add("owner/a", a)
	reg.add("owner/b", b)

	reg.markProcessed("", 99)

	if got := a.snapshotMarked(); !equalInts(got, []int{99}) {
		t.Errorf("repo a marked = %v, want [99]", got)
	}
	if got := b.snapshotMarked(); !equalInts(got, []int{99}) {
		t.Errorf("repo b marked = %v, want [99]", got)
	}
}

// clearProcessed is likewise repo-scoped (stale-label recovery).
func TestGithubPollerRegistry_ClearProcessedScopedByRepo(t *testing.T) {
	reg := newGithubPollerRegistry()
	a := &fakeMarker{}
	b := &fakeMarker{}
	reg.add("owner/a", a)
	reg.add("owner/b", b)

	reg.clearProcessed("owner/b", 13)

	if got := a.snapshotCleared(); len(got) != 0 {
		t.Errorf("repo a cleared = %v, want []", got)
	}
	if got := b.snapshotCleared(); !equalInts(got, []int{13}) {
		t.Errorf("repo b cleared = %v, want [13]", got)
	}
}

// Marking/clearing a repo with no registered poller is a harmless no-op — this
// is what lets the stale-label cleaner wire callbacks unconditionally.
func TestGithubPollerRegistry_UnknownRepoNoop(t *testing.T) {
	reg := newGithubPollerRegistry()
	reg.add("owner/a", &fakeMarker{})
	// Must not panic.
	reg.markProcessed("owner/missing", 1)
	reg.clearProcessed("owner/missing", 1)
}

// All methods are nil-safe so callers (gateway mode, GitHub polling off) need
// not guard.
func TestGithubPollerRegistry_NilSafe(t *testing.T) {
	var reg *githubPollerRegistry
	reg.add("owner/a", &fakeMarker{})
	reg.markProcessed("owner/a", 1)
	reg.markProcessedAll(1)
	reg.clearProcessed("owner/a", 1)
}

// add ignores empty repo / nil marker rather than corrupting the map.
func TestGithubPollerRegistry_AddGuards(t *testing.T) {
	reg := newGithubPollerRegistry()
	reg.add("", &fakeMarker{})
	reg.add("owner/a", nil)
	// Nothing registered → marking is a no-op, no panic.
	reg.markProcessed("owner/a", 1)
	if len(reg.byRepo) != 0 {
		t.Errorf("registry byRepo = %v, want empty after guarded adds", reg.byRepo)
	}
}
