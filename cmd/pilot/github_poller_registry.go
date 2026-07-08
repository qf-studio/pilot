package main

import "sync"

// githubProcessedMarker is the subset of GitHub poller behavior the cross-poller
// skip / clear loops depend on (GH-3240 sub-issue skip, GH-3271 done re-mark,
// GH-2589/GH-2402 stale-label recovery). Both the in-tree *github.Poller and the
// SDK *githubSDK.Poller satisfy it, so a single registry can route marking to
// whichever poller owns a given repo.
type githubProcessedMarker interface {
	MarkProcessed(number int)
	ClearProcessed(number int)
}

// githubPollerRegistry maps owner/repo → the poller(s) responsible for that repo.
//
// It closes two defects at once (GH-4110):
//
//   - The SDK poller handle never left githubPollerRegistration()'s CreateAndStart
//     closure, so the main.go skip / clear loops — which ranged only the in-tree
//     ghPollers slice — could not reach it. Since 2026-07-06 the pilot repo polls
//     via the SDK poller, so its epic-created sub-issues were never skip-marked and
//     got duplicate-dispatched (confirmed live on epic GH-3927's children).
//   - Marking ranged every in-tree poller regardless of repo, so a sub-issue number
//     in one repo could suppress an unrelated same-numbered issue in another repo.
//
// Registration happens during startup: in-tree pollers add themselves inline, the
// SDK poller adds itself from its synchronous CreateAndStart. The skip / clear
// callbacks capture the registry by reference and fire later at runtime, by which
// point every poller is registered. Designed for N per-repo instances — the M7
// 4d.2 fan-out will register multiple SDK pollers here.
//
// All methods are safe on a nil receiver so callers need not guard.
type githubPollerRegistry struct {
	mu     sync.Mutex
	byRepo map[string][]githubProcessedMarker
}

func newGithubPollerRegistry() *githubPollerRegistry {
	return &githubPollerRegistry{byRepo: make(map[string][]githubProcessedMarker)}
}

// add registers a poller as an owner of repo (owner/repo form). No-op when the
// registry, marker, or repo is empty.
func (r *githubPollerRegistry) add(repo string, m githubProcessedMarker) {
	if r == nil || m == nil || repo == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byRepo[repo] = append(r.byRepo[repo], m)
}

// markProcessed marks issue number n processed in every poller registered for
// repo. When repo is empty (source repo unknown) it falls back to marking all
// pollers: under-marking would re-introduce the duplicate-dispatch bug this
// registry exists to fix, which is the worse failure than the cross-repo
// over-mark the fallback risks.
func (r *githubPollerRegistry) markProcessed(repo string, n int) {
	if r == nil {
		return
	}
	if repo == "" {
		r.markProcessedAll(n)
		return
	}
	for _, m := range r.snapshot(repo) {
		m.MarkProcessed(n)
	}
}

// markProcessedAll marks n processed in every registered poller across all repos.
func (r *githubPollerRegistry) markProcessedAll(n int) {
	if r == nil {
		return
	}
	for _, m := range r.snapshotAll() {
		m.MarkProcessed(n)
	}
}

// clearProcessed clears issue number n in every poller registered for repo
// (stale-label recovery). Cleaners are always constructed for a concrete repo,
// so repo is non-empty in practice.
func (r *githubPollerRegistry) clearProcessed(repo string, n int) {
	if r == nil {
		return
	}
	for _, m := range r.snapshot(repo) {
		m.ClearProcessed(n)
	}
}

// snapshot copies the markers for one repo under lock so callouts happen without
// holding the mutex.
func (r *githubPollerRegistry) snapshot(repo string) []githubProcessedMarker {
	r.mu.Lock()
	defer r.mu.Unlock()
	src := r.byRepo[repo]
	out := make([]githubProcessedMarker, len(src))
	copy(out, src)
	return out
}

// snapshotAll copies every registered marker under lock.
func (r *githubPollerRegistry) snapshotAll() []githubProcessedMarker {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []githubProcessedMarker
	for _, ms := range r.byRepo {
		out = append(out, ms...)
	}
	return out
}
