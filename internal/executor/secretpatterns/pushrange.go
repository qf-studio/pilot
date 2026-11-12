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
	"path/filepath"
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
	return fmt.Sprintf("push refused: commit %s adds a secret value in %s", e.Commit, Redact(e.File))
}

// newPushRangeError builds the verdict with the file path masked: a path can
// hold the secret itself, and the error, the log line and the alert print it.
func newPushRangeError(commit, file, kind string) *PushRangeError {
	return &PushRangeError{Commit: commit, File: Redact(file), Kind: kind}
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

// QualifyBranchRef returns the full ref name of a branch under refs/heads. A
// short name is resolved by git through $GIT_DIR/<name> before refs/heads/<name>,
// so a file the session writes in the git dir can stand in for the branch while
// git push sends refs/heads/<name> (GH-6334). HEAD and a name that already
// starts with "refs/" are returned as they are.
func QualifyBranchRef(head string) string {
	if head == "" || head == "HEAD" || strings.HasPrefix(head, "refs/") {
		return head
	}
	return "refs/heads/" + head
}

// pushBase is PushBase that also returns the context error. git failures stay
// swallowed (a candidate that does not resolve is the normal case), so the
// context is checked after every call: a context that ended must not look like
// "no base" and turn into a scan refusal (GH-6318).
func pushBase(ctx context.Context, dir, head, defaultBranch string) (string, error) {
	var candidates []string
	if b := strings.TrimPrefix(strings.TrimSpace(defaultBranch), "origin/"); b != "" {
		candidates = append(candidates, "origin/"+b)
	}
	out, err := gitOutput(ctx, dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if cerr := ctxError(ctx); cerr != nil {
		return "", cerr
	}
	if err == nil {
		if ref := strings.TrimSpace(string(out)); ref != "" {
			candidates = append(candidates, ref)
		}
	}
	candidates = append(candidates, "origin/main", "origin/master")
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
	head = QualifyBranchRef(head)
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
		boundary, berr := isShallowBoundary(ctx, dir, oldest)
		if berr != nil {
			return berr
		}
		if boundary {
			// Decision: the oldest unpushed commit is the shallow boundary of a clone
			// whose remote branch moved on, so it came from the remote and its parent
			// is only missing because of the depth. Measuring from the empty tree (or
			// scanning it as a root commit) would show every file it holds as added
			// and raise a false alert on old published content. Diff against the
			// boundary commit itself and leave it out of the per-commit scan (GH-6284).
			diffBase = oldest
			logArgs = append(logArgs, oldest) // after --not: excluded (a "^" would flip it back)
		}
	}
	// Decision: a merge in the unpushed range may bring in a branch that is
	// already on a remote, and the net diff from the oldest unpushed commit's
	// parent would then hold that branch's old changes and refuse them as new.
	// With a merge in the range the net diff is skipped; the per-commit pass
	// reads every merge as a combined diff (--cc), which shows only what the
	// merge itself introduced.
	merges, merr := gitOutput(ctx, dir, "rev-list", "--merges", "-n", "1", head, "--not", "--remotes")
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

// isShallowBoundary reports whether sha is a shallow boundary of the repository
// at dir: a commit whose parents were cut off by a shallow fetch, listed in
// git's shallow file. A genuine root commit is not. The error is the context
// error, when the context ended during a git call.
func isShallowBoundary(ctx context.Context, dir, sha string) (bool, error) {
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
	// Decision: a git before 2.31 does not fail on --path-format. rev-parse prints
	// an option it does not know back as output and exits 0, so the answer is
	// told apart by its shape: a path that is not absolute means the option was
	// not understood and the boundary check is off on this host.
	shallowPath := strings.TrimSpace(string(out))
	if err != nil || !filepath.IsAbs(shallowPath) {
		warnOldGit(ctx, dir)
		return false, ctxError(ctx)
	}
	data, err := os.ReadFile(shallowPath)
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

// minAbsolutePathGit is the first git release with rev-parse --path-format.
const minAbsolutePathGit = "2.31"

var (
	warnOldGitMu   sync.Mutex
	warnOldGitDone bool
)

// gitVersionOutput is `git --version`; a var so a test can stand in an old git.
var gitVersionOutput = func(ctx context.Context, dir string) (string, error) {
	out, err := gitOutput(ctx, dir, "--version")
	return string(out), err
}

// warnOldGit logs once per process when the host's git predates --path-format
// (2.31). The shallow-boundary check needs it to find the shallow file and is
// silently off without it, so a depth-limited clone is refused for its old
// published content instead of recognised. The once-guard is spent only by a
// version that was read: a failed call or an ended context leaves it for the
// next push.
func warnOldGit(ctx context.Context, dir string) {
	warnOldGitMu.Lock()
	defer warnOldGitMu.Unlock()
	if warnOldGitDone {
		return
	}
	out, err := gitVersionOutput(ctx, dir)
	if err != nil || ctx.Err() != nil {
		return
	}
	warnOldGitDone = true
	if v, ok := parseGitVersion(out); ok && versionLess(v, minAbsolutePathGit) {
		slog.Warn("git is older than 2.31: the push secret gate cannot recognise the boundary of a shallow clone and scans its published history as new",
			slog.String("git_version", v), slog.String("minimum", minAbsolutePathGit))
	}
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
		diff, err = gitOutputMax(ctx, dir, int64(netDiffScanFactor*maxCommitScanBytes), "diff", "--no-color", "--text", "--no-textconv", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/", diffBase+".."+head)
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
	msgs, err := gitOutput(ctx, dir, append([]string{"log", "--no-textconv", "--no-ext-diff", "--format=%B"}, logArgs...)...)
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
		// The hit stands even when the context has ended; the commit is then named
		// by the ref, since no further git call may start.
		commit := head
		if ctx.Err() == nil {
			if sha, serr := gitOutput(ctx, dir, "rev-parse", "--short=12", head); serr == nil {
				commit = strings.TrimSpace(string(sha))
			}
		}
		e := newPushRangeError(commit, netFile, netKind)
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

	args := append(gitScanConfig(), "log", "-p", "--cc", "--text", "--no-textconv", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/", "--reverse", "--no-color", format)
	args = append(args, logArgs...)
	defer gitCallDone(args)
	cmd := gitCommand(ctx, dir, args...)
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
			return newPushRangeError(shortSHA(sha), CommitMessageFile, kind)
		}
		if len(patch) > maxCommitScanBytes {
			return &PushScanError{Reason: fmt.Sprintf("commit %s has more than %d bytes of diff, too large to scan", shortSHA(sha), maxCommitScanBytes)}
		}
		if file, kind, hit := scanPatch(patch, opts); hit {
			return newPushRangeError(shortSHA(sha), file, kind)
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
// with a hit. The path of each file the diff adds, changes or renames is scanned
// first, with the same patterns and values as its content (GH-6334). File
// headers (diff --git … +++ b/<path>) are read only in the header block, before
// the first hunk: an added line that starts with "++ " shows up in a hunk as
// "+++ …" and is scanned, not taken for a file name.
func scanPatch(patch []byte, opts PushOptions) (file, kind string, hit bool) {
	var (
		cur      string
		inHeader bool
		added    bytes.Buffer
	)
	flush := func() bool {
		defer added.Reset()
		skipShapes := opts.shapesAllowed(cur)
		var k string
		ok := false
		if cur != "" && cur != "/dev/null" { // a deleted file has no path to publish
			k, ok = firstHit([]byte(cur), skipShapes)
		}
		if !ok && added.Len() > 0 {
			k, ok = firstHit(added.Bytes(), skipShapes)
		}
		if ok {
			file, kind = cur, k
		}
		return ok
	}
	for _, line := range bytes.Split(patch, []byte{'\n'}) {
		switch {
		case bytes.HasPrefix(line, []byte("diff --git ")):
			if flush() {
				return file, kind, true
			}
			// The new path is the last " b/" part; "+++" or "rename to" below
			// replace it when the diff has them (a path may hold " b/" itself).
			rest := string(line[len("diff --git "):])
			cur, inHeader = rest, true
			if i := strings.LastIndex(rest, " b/"); i >= 0 {
				cur = rest[i+len(" b/"):]
			}
		case bytes.HasPrefix(line, []byte("diff --cc ")):
			if flush() {
				return file, kind, true
			}
			cur, inHeader = string(line[len("diff --cc "):]), true
		case inHeader && bytes.HasPrefix(line, []byte("@@")):
			inHeader = false
		case inHeader:
			switch {
			case bytes.HasPrefix(line, []byte("+++ ")):
				cur = strings.TrimSuffix(strings.TrimPrefix(string(line[4:]), "b/"), "\t")
			case bytes.HasPrefix(line, []byte("rename to ")):
				cur = string(line[len("rename to "):])
			case bytes.HasPrefix(line, []byte("copy to ")):
				cur = string(line[len("copy to "):])
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

// gitCommand is the one way the gate starts git. Decision: it runs with
// GIT_NO_REPLACE_OBJECTS=1. A replace ref or a graft made by the session maps a
// commit to another one for every reading command, so the scan would see the
// replacement while git push sends the real objects (GH-6318).
func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_NO_REPLACE_OBJECTS=1")
	return cmd
}

// gitScanConfig is the -c arguments of every git call of the gate. Decision:
// the repository is writable by the session, so the gate states what it needs
// instead of reading the repository's config: log.showRoot=true, or a root
// commit shows no patch when the config says false (GH-6334).
func gitScanConfig() []string {
	return []string{"-c", "core.quotepath=false", "-c", "log.showRoot=true"}
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	defer gitCallDone(args)
	cmd := gitCommand(ctx, dir, append(gitScanConfig(), args...)...)
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
	cmd := gitCommand(ctx, dir, append(gitScanConfig(), args...)...)
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
