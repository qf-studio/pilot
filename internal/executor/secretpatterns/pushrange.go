package secretpatterns

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
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
	var candidates []string
	if b := strings.TrimPrefix(strings.TrimSpace(defaultBranch), "origin/"); b != "" {
		candidates = append(candidates, "origin/"+b)
	}
	if out, err := gitOutput(ctx, dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if ref := strings.TrimSpace(string(out)); ref != "" {
			candidates = append(candidates, ref)
		}
	}
	candidates = append(candidates, "origin/main", "origin/master")
	for _, ref := range candidates {
		if out, err := gitOutput(ctx, dir, "merge-base", head, ref); err == nil {
			if sha := strings.TrimSpace(string(out)); sha != "" {
				return sha
			}
		}
	}
	return ""
}

// CheckPush is CheckPushRange for the commits a push of head would publish,
// measured from PushBase. When no base resolves (a default branch with another
// name and no origin/HEAD, a shallow clone) it scans only the commits that are
// on no remote ref, and refuses with a *PushScanError when it cannot list them.
//
// A cancelled or expired ctx is returned as the context error, never as a
// refusal: git failed because the caller gave up, not because the push cannot be
// cleared.
func CheckPush(ctx context.Context, dir, head string, opts PushOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("secret gate: %w", err)
	}
	if base := PushBase(ctx, dir, head, opts.DefaultBranch); base != "" {
		return checkRange(ctx, dir, base, []string{base + ".." + head}, head, opts)
	}
	// PushBase swallows git errors, so a context that ended during it shows up here.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("secret gate: %w", err)
	}
	out, err := gitOutput(ctx, dir, "rev-list", "--reverse", head, "--not", "--remotes")
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return fmt.Errorf("secret gate: %w", cerr)
		}
		return &PushScanError{Reason: "the push base could not be computed: no merge base with the default branch and the commits not on a remote could not be listed"}
	}
	oldest, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if oldest == "" {
		return nil // every commit is already on a remote ref: nothing to publish
	}
	diffBase, logArgs := emptyTree, []string{head, "--not", "--remotes"}
	if parent, perr := gitOutput(ctx, dir, "rev-parse", "--verify", "--quiet", oldest+"^"); perr == nil {
		diffBase = strings.TrimSpace(string(parent))
	} else if isShallowBoundary(ctx, dir, oldest) {
		// Decision: the oldest unpushed commit is the shallow boundary of a clone
		// whose remote branch moved on, so it came from the remote and its parent
		// is only missing because of the depth. Measuring from the empty tree (or
		// scanning it as a root commit) would show every file it holds as added
		// and raise a false alert on old published content. Diff against the
		// boundary commit itself and leave it out of the per-commit scan.
		diffBase = oldest
		logArgs = append(logArgs, oldest)
	}
	return checkRange(ctx, dir, diffBase, logArgs, head, opts)
}

// isShallowBoundary reports whether sha is a shallow boundary of the repository
// at dir: a commit whose parents were cut off by a shallow fetch, listed in
// git's shallow file. A genuine root commit is not.
func isShallowBoundary(ctx context.Context, dir, sha string) bool {
	if out, err := gitOutput(ctx, dir, "rev-parse", "--is-shallow-repository"); err != nil || strings.TrimSpace(string(out)) != "true" {
		return false
	}
	out, err := gitOutput(ctx, dir, "rev-parse", "--path-format=absolute", "--git-path", "shallow")
	if err != nil {
		return false
	}
	data, err := os.ReadFile(strings.TrimSpace(string(out)))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == sha {
			return true
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
// selected by logArgs (git log revision arguments).
//
// Decision: besides the net diff and the messages, every commit is scanned on
// its own. A token committed and removed again by a later commit is gone from
// the net diff but still reaches the remote, which is exactly how the
// 2026-04-07 token was published.
func checkRange(ctx context.Context, dir, diffBase string, logArgs []string, head string, opts PushOptions) error {
	diff, err := gitOutputMax(ctx, dir, int64(netDiffScanFactor*maxCommitScanBytes), "diff", "--no-color", "--text", diffBase+".."+head)
	if errors.Is(err, errScanTooLarge) {
		return &PushScanError{Reason: fmt.Sprintf("the diff of the push is larger than %d bytes, too large to scan", netDiffScanFactor*maxCommitScanBytes)}
	}
	if err != nil {
		return fmt.Errorf("secret gate: git diff: %w", err)
	}
	msgs, err := gitOutput(ctx, dir, append([]string{"log", "--format=%B"}, logArgs...)...)
	if err != nil {
		return fmt.Errorf("secret gate: git log: %w", err)
	}
	netFile, netKind, netHit := scanPatch(diff, opts)
	msgKind, msgHit := firstHit(msgs, false)

	// Per-commit pass: oldest first, so the error names the first offender.
	if err := scanCommits(ctx, dir, logArgs, opts); err != nil {
		return err
	}

	// Hits the per-commit pass cannot see (a merge's conflict resolution).
	if netHit || msgHit {
		sha, _ := gitOutput(ctx, dir, "rev-parse", "--short=12", head)
		e := &PushRangeError{Commit: strings.TrimSpace(string(sha)), File: netFile, Kind: netKind}
		if !netHit {
			e.File, e.Kind = CommitMessageFile, msgKind
		}
		return e
	}
	return nil
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

	args := append([]string{"-c", "core.quotepath=false", "log", "-p", "--text", "--reverse", "--no-color", format}, logArgs...)
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
		case bytes.HasPrefix(line, []byte("diff --git ")):
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

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
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
