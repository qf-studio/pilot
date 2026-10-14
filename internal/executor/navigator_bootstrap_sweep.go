package executor

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// strayBaseline remembers what the project clone looked like before the
// bootstrap session: the dirty paths outside the Navigator directory and
// their contents, so the sweep never discards the developer's own
// uncommitted work.
type strayBaseline struct {
	dir    string
	prefix string
	dirty  map[string]strayFile
}

type strayFile struct {
	exists bool
	mode   os.FileMode
	data   []byte
}

// snapshotStrayBaseline records the dirty state before the session. It returns
// nil when projectPath is not inside a git work tree (nothing to sweep).
func snapshotStrayBaseline(ctx context.Context, projectPath string) *strayBaseline {
	prefixOut, err := sweepGitOutput(ctx, projectPath, "rev-parse", "--show-prefix")
	if err != nil {
		return nil
	}
	b := &strayBaseline{dir: projectPath, prefix: strings.TrimSpace(string(prefixOut)), dirty: map[string]strayFile{}}
	entries, err := gitDirtyEntries(ctx, projectPath, b.prefix)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		b.dirty[e.path] = readStrayFile(projectPath, e.path)
	}
	return b
}

func readStrayFile(dir, rel string) strayFile {
	full := filepath.Join(dir, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return strayFile{}
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return strayFile{}
	}
	return strayFile{exists: true, mode: info.Mode().Perm(), data: data}
}

// sweepStrayWrites reverts or deletes every change the session left outside
// the Navigator directory and returns the project-relative paths it found.
//
// Decision: the PreToolUse guard is the primary boundary but only covers the
// file-writing tools; a Bash command (e.g. `git checkout`, `sed -i`) can still
// touch tracked files. The sweep is the backstop that makes the post-session
// tree match the pre-session tree outside .agent/.
func (b *strayBaseline) sweepStrayWrites(ctx context.Context, log *slog.Logger) []string {
	if b == nil {
		return nil
	}
	entries, err := gitDirtyEntries(ctx, b.dir, b.prefix)
	if err != nil {
		log.Warn("Navigator bootstrap: could not list stray writes", slog.Any("error", err))
		return nil
	}
	var stray []string
	for _, e := range entries {
		before, wasDirty := b.dirty[e.path]
		if wasDirty && sameStrayFile(before, readStrayFile(b.dir, e.path)) {
			continue
		}
		stray = append(stray, e.path)
		if wasDirty {
			b.restoreBaseline(e.path, before, log)
		} else {
			b.revertToHead(ctx, e, log)
		}
	}
	// A pre-dirty file the session reverted back to HEAD no longer shows in
	// status; restore it from the snapshot too.
	listed := map[string]struct{}{}
	for _, e := range entries {
		listed[e.path] = struct{}{}
	}
	for p, before := range b.dirty {
		if _, ok := listed[p]; ok {
			continue
		}
		stray = append(stray, p)
		b.restoreBaseline(p, before, log)
	}
	sort.Strings(stray)
	return stray
}

func sameStrayFile(a, b strayFile) bool {
	return a.exists == b.exists && bytes.Equal(a.data, b.data)
}

// restoreBaseline puts a file that was already dirty before the session back
// to its pre-session content.
func (b *strayBaseline) restoreBaseline(rel string, before strayFile, log *slog.Logger) {
	full := filepath.Join(b.dir, filepath.FromSlash(rel))
	log.Warn("Navigator bootstrap: restoring stray write outside the Navigator directory", slog.String("path", rel))
	if !before.exists {
		_ = os.Remove(full)
		return
	}
	if err := os.WriteFile(full, before.data, before.mode); err != nil {
		log.Warn("Navigator bootstrap: restore failed", slog.String("path", rel), slog.Any("error", err))
	}
}

// revertToHead undoes a change to a path that was clean before the session.
func (b *strayBaseline) revertToHead(ctx context.Context, e dirtyEntry, log *slog.Logger) {
	log.Warn("Navigator bootstrap: reverting stray write outside the Navigator directory",
		slog.String("path", e.path), slog.String("status", e.status))
	full := filepath.Join(b.dir, filepath.FromSlash(e.path))
	spec := b.prefix + e.path
	switch {
	case e.status == "??":
		if err := os.Remove(full); err != nil {
			log.Warn("Navigator bootstrap: delete failed", slog.String("path", e.path), slog.Any("error", err))
			return
		}
		removeEmptyParents(b.dir, filepath.Dir(full))
	case sweepGitRun(ctx, b.dir, "cat-file", "-e", "HEAD:"+spec) == nil:
		if err := sweepGitRun(ctx, b.dir, "checkout", "HEAD", "--", e.path); err != nil {
			log.Warn("Navigator bootstrap: revert failed", slog.String("path", e.path), slog.Any("error", err))
		}
	default:
		// Added to the index, absent from HEAD.
		if err := sweepGitRun(ctx, b.dir, "rm", "-f", "-q", "--", e.path); err != nil {
			log.Warn("Navigator bootstrap: revert failed", slog.String("path", e.path), slog.Any("error", err))
			return
		}
		removeEmptyParents(b.dir, filepath.Dir(full))
	}
}

// removeEmptyParents removes now-empty directories from dir up to, not
// including, root.
func removeEmptyParents(root, dir string) {
	for dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

type dirtyEntry struct {
	status string // two-char porcelain XY
	path   string // slash path relative to the project dir
}

// gitDirtyEntries lists changed paths below dir that lie outside the
// Navigator directory. Renames are reported as delete + add so each path is
// handled on its own.
func gitDirtyEntries(ctx context.Context, dir, prefix string) ([]dirtyEntry, error) {
	out, err := sweepGitOutput(ctx, dir, "status", "--porcelain", "-z", "-uall", "--no-renames", "--", ".")
	if err != nil {
		return nil, err
	}
	var entries []dirtyEntry
	for _, rec := range strings.Split(string(out), "\x00") {
		if len(rec) < 4 {
			continue
		}
		p := strings.TrimPrefix(rec[3:], prefix)
		if p == navigatorDirName || strings.HasPrefix(p, navigatorDirName+"/") {
			continue
		}
		entries = append(entries, dirtyEntry{status: rec[:2], path: p})
	}
	return entries, nil
}

func sweepGitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

func sweepGitRun(ctx context.Context, dir string, args ...string) error {
	_, err := sweepGitOutput(ctx, dir, args...)
	return err
}
