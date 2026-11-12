package secretpatterns

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
)

// ErrPushRefused is matched (errors.Is) by every *PushRangeError. A retry loop
// must treat it as final: the same commits hold the same secret on every
// attempt.
var ErrPushRefused = errors.New("push refused")

// PushRangeError is the CheckPushRange verdict for a range that holds a secret.
// It names the commit and the file, never the value.
type PushRangeError struct {
	Commit string // short SHA of the commit that adds the secret
	File   string // path of the file, or CommitMessageFile
	Kind   string // Match.Kind of the hit: KindConfiguredValue or a pattern prefix
}

// CommitMessageFile is PushRangeError.File when the secret is in a commit message.
const CommitMessageFile = "the commit message"

func (e *PushRangeError) Error() string {
	return fmt.Sprintf("push refused: commit %s adds a secret value in %s", e.Commit, e.File)
}

// Is makes errors.Is(err, ErrPushRefused) true for a *PushRangeError.
func (e *PushRangeError) Is(target error) bool { return target == ErrPushRefused }

// PushScanError is the verdict for a push the gate cannot clear for a reason
// other than a secret: the push base could not be computed, or a commit's diff
// is too large to scan. It is a refusal (errors.Is ErrPushRefused) but not a
// secret alert: nothing was found, so nothing was leaked.
type PushScanError struct {
	Reason string
}

func (e *PushScanError) Error() string { return "push refused: " + e.Reason }

// Is makes errors.Is(err, ErrPushRefused) true for a *PushScanError.
func (e *PushScanError) Is(target error) bool { return target == ErrPushRefused }

// DefaultMaxCommitScanBytes caps the message and diff output the gate reads
// for one commit. A commit above it is refused rather than scanned in part.
const DefaultMaxCommitScanBytes = 20 << 20

// maxCommitScanBytes is the per-commit cap in force; a var so a test can lower it.
var maxCommitScanBytes = DefaultMaxCommitScanBytes

// netDiffScanFactor sizes the cap of the net diff of the whole range, which
// spans many commits, against the per-commit cap.
const netDiffScanFactor = 5

// PushOptions are the per-project settings of the gate.
type PushOptions struct {
	// DefaultBranch is the project's configured default branch; PushBase tries
	// origin/<DefaultBranch> first. Empty means detect it.
	DefaultBranch string
	// AllowPaths are globs of files that show secret shapes on purpose, on top
	// of the built-in list. A registered value is still refused in them.
	AllowPaths []string
}

// patternAllowlist lists the files that show secret shapes on purpose. It
// mirrors ALLOWLIST in scripts/check-secret-patterns.sh, so a Pilot PR that
// edits them is not refused for its own fixtures. It never excuses a
// registered value.
var patternAllowlist = map[string]bool{
	"CLAUDE.md":                                            true,
	"CONTRIBUTING.md":                                      true,
	"internal/testutil/tokens.go":                          true,
	"scripts/check-secret-patterns.sh":                     true,
	"internal/executor/acceptance_evidence_run_test.go":    true,
	".agent/tasks/archive/TASK-41-test-secret-patterns.md": true,
	"internal/executor/secretpatterns/patterns.txt":        true,
	"internal/executor/secretpatterns/patterns_test.go":    true,
}

// shapesAllowed reports whether file may show secret shapes: it is on the
// built-in list or matches one of the project's allow_paths globs.
func (o PushOptions) shapesAllowed(file string) bool {
	if patternAllowlist[file] {
		return true
	}
	for _, g := range o.AllowPaths {
		if MatchPathGlob(g, file) {
			return true
		}
	}
	return false
}

// MatchPathGlob matches a slash-separated path against a glob. "*" and "?"
// stay inside one path segment, "**" matches any number of segments, and a
// pattern without a slash matches the file name at any depth.
func MatchPathGlob(pattern, file string) bool {
	pattern = strings.TrimPrefix(strings.TrimSpace(pattern), "./")
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		return matchSegments([]string{pattern}, []string{path.Base(file)})
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 0 && pat[0] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 0 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if matchSegments(pat, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// emptyTree is git's well-known empty tree, the diff base for a range with no
// base commit.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// PushBase returns the commit a push of head is measured from: the merge base
// of head and the remote default branch. defaultBranch is the project's
// configured default branch and is tried first (origin/<defaultBranch>), then
// origin/HEAD, origin/main and origin/master. It returns "" when none resolves;
// CheckPush then scans only the commits that are on no remote ref.
func PushBase(ctx context.Context, dir, head, defaultBranch string) string {
	base, _ := pushBase(ctx, dir, head, defaultBranch)
	return base
}

// baseCandidates lists the remote refs a push base is looked for on, most
// specific first. The error is the context error, when the context ended.
func baseCandidates(ctx context.Context, dir, defaultBranch string) ([]string, error) {
	var candidates []string
	if b := strings.TrimPrefix(strings.TrimSpace(defaultBranch), "origin/"); b != "" {
		candidates = append(candidates, "origin/"+b)
	}
	out, err := gitOutput(ctx, dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if cerr := ctxError(ctx); cerr != nil {
		return nil, cerr
	}
	if err == nil {
		if ref := strings.TrimSpace(string(out)); ref != "" {
			candidates = append(candidates, ref)
		}
	}
	return append(candidates, "origin/main", "origin/master"), nil
}

// pushBase is PushBase that also returns the context error. git failures stay
// swallowed (a candidate that does not resolve is the normal case), so the
// context is checked after every call: a context that ended must not look like
// "no base" and turn into a scan refusal (GH-6318).
func pushBase(ctx context.Context, dir, head, defaultBranch string) (string, error) {
	candidates, err := baseCandidates(ctx, dir, defaultBranch)
	if err != nil {
		return "", err
	}
	for _, ref := range candidates {
		out, err := gitOutput(ctx, dir, "merge-base", head, ref)
		if cerr := ctxError(ctx); cerr != nil {
			return "", cerr
		}
		if err == nil {
			if sha := strings.TrimSpace(string(out)); sha != "" {
				return sha, nil
			}
		}
	}
	return "", nil
}

// ctxError is the context error of ctx as the gate reports it, or nil.
func ctxError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("secret gate: %w", err)
	}
	return nil
}

// CheckPush is CheckPushRange for the commits a push of head would publish,
// measured from PushBase. When no base resolves (a default branch with another
// name and no origin/HEAD, a shallow clone) it scans only the commits that are
// on no remote ref, and refuses with a *PushScanError when it cannot list them.
//
// A cancelled or expired ctx is returned as the context error, never as a
// refusal: git failed because the caller gave up, not because the push cannot be
// cleared (GH-6284). The context is checked after every git call of the no-base
// path, so no further git call starts once it has ended (GH-6318).
func CheckPush(ctx context.Context, dir, head string, opts PushOptions) error {
	if err := ctxError(ctx); err != nil {
		return err
	}
	base, err := pushBase(ctx, dir, head, opts.DefaultBranch)
	if err != nil {
		return err
	}
	if base != "" {
		return checkRange(ctx, dir, base, []string{base + ".." + head}, head, opts)
	}
	out, err := gitOutput(ctx, dir, "rev-list", "--reverse", head, "--not", "--remotes")
	if cerr := ctxError(ctx); cerr != nil {
		return cerr
	}
	if err != nil {
		return &PushScanError{Reason: "the push base could not be computed: no merge base with the default branch and the commits not on a remote could not be listed"}
	}
	oldest, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if oldest == "" {
		return nil // every commit is already on a remote ref: nothing to publish
	}
	diffBase, logArgs := emptyTree, []string{head, "--not", "--remotes"}
	parent, perr := gitOutput(ctx, dir, "rev-parse", "--verify", "--quiet", oldest+"^")
	if cerr := ctxError(ctx); cerr != nil {
		return cerr
	}
	if perr == nil {
		diffBase = strings.TrimSpace(string(parent))
	} else {
		// Decision: the oldest unpushed commit has no parent. It is either a root
		// commit, which is scanned from the empty tree like any other new content,
		// or the shallow boundary of a clone whose remote branch moved on: its parent
		// is only missing because of the depth, and measuring from the empty tree
		// would show every file it holds as added and raise a false alert on old
		// published content. A boundary counts only when the shallow file lists it
		// AND a remote-tracking ref has held it, so a line appended to the shallow
		// file does not exempt a commit (GH-6284, GH-6318).
		boundary, tips, berr := shallowBoundary(ctx, dir, oldest, opts.DefaultBranch)
		if berr != nil {
			return berr
		}
		if boundary {
			// Decision: a depth above 1 leaves more than the boundary commit in the
			// window. Every commit a remote ref has held (the tips of the default
			// branch's remote-tracking reflog and what they reach) was published, so
			// all of them are excluded, not only the boundary. After --not, so a "^"
			// would not flip it back.
			logArgs = append(logArgs, tips...)
			logArgs = append(logArgs, oldest)
			first, ferr := gitOutput(ctx, dir, append([]string{"rev-list", "--reverse"}, logArgs...)...)
			if cerr := ctxError(ctx); cerr != nil {
				return cerr
			}
			if ferr != nil {
				return &PushScanError{Reason: "the push base could not be computed: the commits not on a remote could not be listed"}
			}
			firstNew, _, _ := strings.Cut(strings.TrimSpace(string(first)), "\n")
			if firstNew == "" {
				return nil // everything the window holds was published
			}
			diffBase = oldest
			np, nerr := gitOutput(ctx, dir, "rev-parse", "--verify", "--quiet", firstNew+"^")
			if cerr := ctxError(ctx); cerr != nil {
				return cerr
			}
			if nerr == nil {
				diffBase = strings.TrimSpace(string(np))
			}
		}
	}
	// Decision: a merge in the unpushed range may bring in a branch that is
	// already on a remote, and the net diff from the oldest unpushed commit's
	// parent would then hold that branch's old changes and refuse them as new.
	// With a merge in the range the net diff is skipped; the per-commit pass
	// reads every merge as a combined diff (--cc), which shows only what the
	// merge itself introduced.
	merges, merr := gitOutput(ctx, dir, append([]string{"rev-list", "--merges", "-n", "1"}, logArgs...)...)
	if cerr := ctxError(ctx); cerr != nil {
		return cerr
	}
	if merr != nil {
		return &PushScanError{Reason: "the push base could not be computed: the merge commits not on a remote could not be listed"}
	}
	if strings.TrimSpace(string(merges)) != "" {
		diffBase = ""
	}
	return checkRange(ctx, dir, diffBase, logArgs, head, opts)
}

// maxReflogTips caps the remote-tracking reflog entries read per ref.
const maxReflogTips = 64

// shallowBoundary reports whether sha is a shallow boundary that came from the
// remote: git's shallow file lists it and a remote-tracking ref has held it. tips
// are the commits those refs have held, for the caller to exclude.
//
// The remote-tracking refs are the default branch candidates and origin/HEAD; a
// fetch moves them forward, so their reflogs still name the tip the clone was
// cut at. Without a reflog (a host that does not keep one) a boundary is not
// recognised and the commit is scanned in full: a false refusal at worst, never
// a skipped commit.
func shallowBoundary(ctx context.Context, dir, sha, defaultBranch string) (ok bool, tips []string, err error) {
	listed, err := inShallowFile(ctx, dir, sha)
	if err != nil || !listed {
		return false, nil, err
	}
	refs, err := baseCandidates(ctx, dir, defaultBranch)
	if err != nil {
		return false, nil, err
	}
	// A clone writes the reflog of origin/HEAD only, so the tip it was cut at is
	// found there once a fetch has moved origin/<branch> on.
	refs = append(refs, "origin/HEAD")
	seen := map[string]bool{}
	var held []string
	for _, ref := range refs {
		out, rerr := gitOutput(ctx, dir, "reflog", "show", "--format=%H", "-n", strconv.Itoa(maxReflogTips), "refs/remotes/"+ref)
		if cerr := ctxError(ctx); cerr != nil {
			return false, nil, cerr
		}
		if rerr != nil {
			continue
		}
		for _, h := range strings.Fields(string(out)) {
			if !seen[h] {
				seen[h] = true
				held = append(held, h)
			}
		}
	}
	if len(held) == 0 {
		return false, nil, nil
	}
	// Keep the entries that still resolve (a pruned object would fail the next
	// rev-list), then ask whether any of them reaches sha: "sha --not tips" is
	// empty exactly when it does.
	out, lerr := gitOutput(ctx, dir, append([]string{"rev-list", "--no-walk=unsorted", "--ignore-missing"}, held...)...)
	if cerr := ctxError(ctx); cerr != nil {
		return false, nil, cerr
	}
	if lerr != nil {
		return false, nil, nil
	}
	tips = strings.Fields(string(out))
	if len(tips) == 0 {
		return false, nil, nil
	}
	left, rerr := gitOutput(ctx, dir, append([]string{"rev-list", "-n", "1", sha, "--not"}, tips...)...)
	if cerr := ctxError(ctx); cerr != nil {
		return false, nil, cerr
	}
	if rerr != nil || strings.TrimSpace(string(left)) != "" {
		return false, nil, nil // no remote-tracking ref has held sha
	}
	return true, tips, nil
}

// minAbsolutePathGit is the first git release with rev-parse --path-format.
const minAbsolutePathGit = "2.31"

var warnOldGitOnce sync.Once

// gitVersionOutput is `git --version`; a var so a test can stand in an old git.
var gitVersionOutput = func(ctx context.Context, dir string) (string, error) {
	out, err := gitOutput(ctx, dir, "--version")
	return string(out), err
}

// warnOldGit logs once per process when the host's git predates --path-format
// (2.31). The shallow-boundary check needs it to find the shallow file and is
// silently off without it, so a depth-limited clone is refused for its old
// published content instead of recognised.
func warnOldGit(ctx context.Context, dir string) {
	warnOldGitOnce.Do(func() {
		out, err := gitVersionOutput(ctx, dir)
		if err != nil {
			return
		}
		if v, ok := parseGitVersion(out); ok && versionLess(v, minAbsolutePathGit) {
			slog.Warn("git is older than 2.31: the push secret gate cannot recognise the boundary of a shallow clone and scans its published history as new",
				slog.String("git_version", v), slog.String("minimum", minAbsolutePathGit))
		}
	})
}

// parseGitVersion extracts "2.39.5" from "git version 2.39.5 (Apple Git-154)".
func parseGitVersion(out string) (string, bool) {
	f := strings.Fields(out)
	if len(f) < 3 || f[0] != "git" || f[1] != "version" {
		return "", false
	}
	return f[2], true
}

// versionLess reports whether the dotted version v is below min, comparing the
// numeric parts; a non-numeric part ends the comparison as "not less".
func versionLess(v, min string) bool {
	vp, mp := strings.Split(v, "."), strings.Split(min, ".")
	for i := range mp {
		if i >= len(vp) {
			return true
		}
		a, aerr := strconv.Atoi(vp[i])
		b, _ := strconv.Atoi(mp[i])
		if aerr != nil {
			return false
		}
		if a != b {
			return a < b
		}
	}
	return false
}

// inShallowFile reports whether sha is listed in git's shallow file at dir: a
// commit whose parents were cut off by a shallow fetch. A genuine root commit is
// not. Anyone with access to the repository can write the file, so a hit is only
// a lead (see shallowBoundary).
func inShallowFile(ctx context.Context, dir, sha string) (bool, error) {
	out, err := gitOutput(ctx, dir, "rev-parse", "--is-shallow-repository")
	if cerr := ctxError(ctx); cerr != nil {
		return false, cerr
	}
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return false, nil
	}
	out, err = gitOutput(ctx, dir, "rev-parse", "--path-format=absolute", "--git-path", "shallow")
	if cerr := ctxError(ctx); cerr != nil {
		return false, cerr
	}
	if err != nil {
		warnOldGit(ctx, dir)
		return false, ctxError(ctx)
	}
	data, err := os.ReadFile(strings.TrimSpace(string(out)))
	if err != nil {
		return false, nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == sha {
			return true, nil
		}
	}
	return false, nil
}

// CheckPushRange refuses the commits in base..head when their diff or messages
// hold a registered secret value or a known secret shape. base "" means every
// commit reachable from head. The returned *PushRangeError names the commit and
// the file, never the value. Only added lines count: a secret already in base
// is not this push's doing.
//
// A failure to run git is returned as a plain error (fail closed): the caller
// must not push on it.
func CheckPushRange(ctx context.Context, dir, base, head string) error {
	if base == "" {
		return checkRange(ctx, dir, emptyTree, []string{head}, head, PushOptions{})
	}
	return checkRange(ctx, dir, base, []string{base + ".." + head}, head, PushOptions{})
}

// checkRange scans the net diff diffBase..head, the messages and every commit
// selected by logArgs (git log revision arguments). diffBase "" skips the net
// diff.
//
// Decision: besides the net diff and the messages, every commit is scanned on
// its own. A token committed and removed again by a later commit is gone from
// the net diff but still reaches the remote, which is exactly how the
// 2026-04-07 token was published.
func checkRange(ctx context.Context, dir, diffBase string, logArgs []string, head string, opts PushOptions) error {
	var diff []byte
	if diffBase != "" {
		var err error
		diff, err = gitOutputMax(ctx, dir, int64(netDiffScanFactor*maxCommitScanBytes), "diff", "--no-color", "--text", diffBase+".."+head)
		if cerr := ctxError(ctx); cerr != nil {
			return cerr
		}
		if errors.Is(err, errScanTooLarge) {
			return &PushScanError{Reason: fmt.Sprintf("the diff of the push is larger than %d bytes, too large to scan", netDiffScanFactor*maxCommitScanBytes)}
		}
		if err != nil {
			return fmt.Errorf("secret gate: git diff: %w", err)
		}
	}
	msgs, err := gitOutput(ctx, dir, append([]string{"log", "--format=%B"}, logArgs...)...)
	if cerr := ctxError(ctx); cerr != nil {
		return cerr
	}
	if err != nil {
		return fmt.Errorf("secret gate: git log: %w", err)
	}
	netFile, netKind, netHit := scanPatch(diff, opts)
	msgKind, msgHit := firstHit(msgs, false)

	// Per-commit pass: oldest first, so the error names the first offender.
	if err := scanCommits(ctx, dir, logArgs, opts); err != nil {
		// A hit stays a hit; any other failure of a context that ended is the context's.
		if cerr := ctxError(ctx); cerr != nil && !errors.Is(err, ErrPushRefused) {
			return cerr
		}
		return err
	}

	// Hits the per-commit pass cannot see (a merge's conflict resolution).
	if netHit || msgHit {
		commit := head
		if ctx.Err() == nil {
			if sha, serr := gitOutput(ctx, dir, "rev-parse", "--short=12", head); serr == nil {
				commit = strings.TrimSpace(string(sha))
			}
		}
		e := &PushRangeError{Commit: commit, File: netFile, Kind: netKind}
		if !netHit {
			e.File, e.Kind = CommitMessageFile, msgKind
		}
		return e
	}
	return ctxError(ctx)
}

// scanCommits scans every selected commit on its own, streaming git's output so
// a huge commit is cut off at maxCommitScanBytes instead of read whole.
//
// Decision: records are separated by a random marker drawn per call, not by a
// control byte. With --text a binary file's own bytes reach the stream, and a
// file that carries the separator (or a forged marker) must not be able to split
// a record and move its content out of the added lines.
func scanCommits(ctx context.Context, dir string, logArgs []string, opts PushOptions) error {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Errorf("secret gate: marker: %w", err)
	}
	mark := "PILOTGATE" + hex.EncodeToString(raw[:])
	start, msgMark, patchMark := []byte(mark+"S\n"), []byte("\n"+mark+"M\n"), []byte("\n"+mark+"P\n")
	format := "--format=" + mark + "S%n%H%n" + mark + "M%n%B%n" + mark + "P"

	args := append([]string{"-c", "core.quotepath=false", "log", "-p", "--cc", "--text", "--reverse", "--no-color", format}, logArgs...)
	defer gitCallDone(args)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, perr := cmd.StdoutPipe()
	if perr != nil {
		return fmt.Errorf("secret gate: git log -p: %w", perr)
	}
	if serr := cmd.Start(); serr != nil {
		return fmt.Errorf("secret gate: git log -p: %w", serr)
	}
	done := false
	defer func() {
		if !done {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	var (
		pending []byte
		chunk   = make([]byte, 64<<10)
		eof     bool
	)
	for !eof {
		// Fill until pending holds one full record (or the output ends).
		searchFrom := 0
		var rec []byte
		for {
			if i := bytes.Index(pending[searchFrom:], start); i >= 0 {
				i += searchFrom
				rec = append(rec, pending[:i]...)
				pending = append(pending[:0], pending[i+len(start):]...)
				break
			}
			if len(pending) > maxCommitScanBytes+len(start) {
				return &PushScanError{Reason: fmt.Sprintf("a commit has more than %d bytes of diff, too large to scan", maxCommitScanBytes)}
			}
			searchFrom = max(0, len(pending)-len(start)+1)
			n, rerr := stdout.Read(chunk)
			pending = append(pending, chunk[:n]...)
			if rerr == io.EOF {
				eof = true
				rec = append(rec, pending...)
				pending = nil
				break
			}
			if rerr != nil {
				return fmt.Errorf("secret gate: git log -p: %w", rerr)
			}
		}
		if len(rec) == 0 {
			continue
		}
		// rec: <sha> \n mark M \n <message> \n mark P \n <patch>   (the leading
		// "S\n" was consumed with the separator).
		shaB, rest, ok := bytes.Cut(rec, msgMark)
		if !ok {
			continue
		}
		msg, patch, _ := bytes.Cut(rest, patchMark)
		sha := bytes.TrimSpace(shaB)
		if kind, hit := firstHit(msg, false); hit {
			return &PushRangeError{Commit: shortSHA(sha), File: CommitMessageFile, Kind: kind}
		}
		if len(patch) > maxCommitScanBytes {
			return &PushScanError{Reason: fmt.Sprintf("commit %s has more than %d bytes of diff, too large to scan", shortSHA(sha), maxCommitScanBytes)}
		}
		if file, kind, hit := scanPatch(patch, opts); hit {
			return &PushRangeError{Commit: shortSHA(sha), File: file, Kind: kind}
		}
	}
	done = true
	if werr := cmd.Wait(); werr != nil {
		return fmt.Errorf("secret gate: git log -p: %w: %s", werr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func shortSHA(sha []byte) string {
	s := strings.TrimSpace(string(sha))
	if len(s) > 12 {
		s = s[:12]
	}
	return s
}

// firstHit reports the Kind of the first secret in text. skipPatterns ignores
// shape matches, so only a registered value counts.
func firstHit(text []byte, skipPatterns bool) (string, bool) {
	for _, m := range FindMatches(text) {
		if skipPatterns && m.Kind != KindConfiguredValue {
			continue
		}
		return m.Kind, true
	}
	return "", false
}

// scanPatch scans the added lines of a unified diff, one file at a time (so a
// value spanning several added lines still matches), and returns the first file
// with a hit. File headers (diff --git … +++ b/<path>) are read only in the
// header block, before the first hunk: an added line that starts with "++ "
// shows up in a hunk as "+++ …" and is scanned, not taken for a file name.
func scanPatch(patch []byte, opts PushOptions) (file, kind string, hit bool) {
	var (
		cur      string
		inHeader bool
		added    bytes.Buffer
	)
	flush := func() bool {
		if added.Len() == 0 {
			return false
		}
		k, ok := firstHit(added.Bytes(), opts.shapesAllowed(cur))
		added.Reset()
		if ok {
			file, kind = cur, k
		}
		return ok
	}
	for _, line := range bytes.Split(patch, []byte{'\n'}) {
		switch {
		case bytes.HasPrefix(line, []byte("diff --git ")), bytes.HasPrefix(line, []byte("diff --cc ")):
			if flush() {
				return file, kind, true
			}
			cur, inHeader = "", true
		case inHeader && bytes.HasPrefix(line, []byte("@@")):
			inHeader = false
		case inHeader:
			if bytes.HasPrefix(line, []byte("+++ ")) {
				cur = strings.TrimSuffix(strings.TrimPrefix(string(line[4:]), "b/"), "\t")
			}
		case len(line) > 0 && line[0] == '+':
			added.Write(line[1:])
			added.WriteByte('\n')
		}
	}
	if flush() {
		return file, kind, true
	}
	return "", "", false
}

// gitCallDone is called after every git call of the gate with its arguments. A
// test seam: it lets a test end the context between two calls.
var gitCallDone = func(args []string) {}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	defer gitCallDone(args)
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotepath=false"}, args...)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// errScanTooLarge is returned by gitOutputMax when git prints more than max bytes.
var errScanTooLarge = errors.New("git output too large to scan")

// gitOutputMax is gitOutput that stops reading, and kills git, past max bytes.
func gitOutputMax(ctx context.Context, dir string, max int64, args ...string) ([]byte, error) {
	defer gitCallDone(args)
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotepath=false"}, args...)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out, rerr := io.ReadAll(io.LimitReader(stdout, max+1))
	if rerr == nil && int64(len(out)) > max {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errScanTooLarge
	}
	if werr := cmd.Wait(); werr != nil {
		return nil, fmt.Errorf("%w: %s", werr, strings.TrimSpace(stderr.String()))
	}
	if rerr != nil {
		return nil, rerr
	}
	return out, nil
}
