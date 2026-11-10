// Package leakscan is the runtime net under secret redaction (GH-6250).
//
// Redaction masks known values and shapes at every write. A writer that
// bypasses it — a new sink, a direct os.Stderr write, a secret registered
// after startup — would leave a credential on disk unnoticed (a token sat in
// the daemon log for 30 days). The scanner periodically reads the bytes
// appended to the daemon log and to recordings, matches them against the
// canonical secret patterns and the registered secret values, and raises a
// security_event alert with the location and a masked sample.
//
// It searches the raw bytes on disk and never depends on a writer having
// redacted. The full value never leaves this package: not in the alert, the
// log or a metric.
package leakscan

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

const (
	// DefaultInterval is the scan period when Config.Interval is unset.
	DefaultInterval = 5 * time.Minute

	// chunkSize is how many bytes are read and matched at a time.
	chunkSize = 1 << 20
	// overlap is the tail of a chunk kept in front of the next one so a
	// secret that straddles a chunk boundary is still matched. It exceeds the
	// longest pattern match and any realistic configured secret.
	overlap = 4096
	// maxPartialLine is how long an unterminated log tail may grow before it
	// is scanned anyway. Normally the tail is held back until its newline
	// arrives so a line being written is not split across two ticks.
	maxPartialLine = 1 << 20
	// tickOverlap is how much of the previous read is kept and scanned again
	// together with the next one, so a value split across two reads (a
	// recording appended mid-token, a log line over maxPartialLine) is found.
	tickOverlap = 512
	// activeGrace is how long a recording directory is still visited after
	// its last change. An append to an existing file does not touch the
	// directory's mtime, so a directory with recent activity stays on the
	// list even when its own mtime is old.
	activeGrace = time.Hour
)

// maxPerTick bounds the bytes scanned per file per tick, keeping a huge
// backlog from stalling the loop; the remainder is scanned next tick. A var so
// a test can use a small cap.
var maxPerTick int64 = 64 << 20

// mtimeSlack widens the "modified since the last tick" test. Filesystem
// timestamps are coarser than the wall clock and can trail it, so a file
// written just before a tick could otherwise fall behind the baseline and be
// skipped forever. Re-visiting is cheap: the per-file offset makes an
// unchanged file a no-op.
const mtimeSlack = 2 * time.Second

// recordingFiles are the recording artifacts that hold agent output.
var recordingFiles = map[string]bool{"stream.jsonl": true, "summary.md": true}

// EventSink receives the security events. *alerts.Engine implements it.
type EventSink interface {
	ProcessEvent(alerts.Event)
}

// Config configures a LeakScanner.
type Config struct {
	// LogPath is the daemon log file. Empty skips log scanning.
	LogPath string
	// RecordingsPath is the recordings directory. Empty skips recordings.
	RecordingsPath string
	// Interval between scans; zero means DefaultInterval.
	Interval time.Duration
	// Sink receives the alerts. Nil still counts hits and logs them.
	Sink EventSink
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

// LeakScanner scans for secrets that reached disk unredacted.
type LeakScanner struct {
	cfg Config
	log *slog.Logger

	// logOffset is the next unread byte of the daemon log. In memory only: a
	// restart begins at the file end, so old content is not re-alerted.
	logOffset int64
	// logIno identifies the file logOffset belongs to, so a replaced file
	// (rotation) is noticed even when the new one is already longer.
	logIno   uint64
	logIDSet bool
	// logTail is the last tickOverlap bytes the log scan read, scanned again
	// in front of the next read.
	logTail []byte
	// lastTick is when recordings were last scanned; only directories and new
	// files modified after it (less mtimeSlack) are visited.
	lastTick time.Time
	// recOffsets is the scanned size per recording file, so an append-only
	// stream.jsonl is scanned incrementally and an old hit is not re-alerted
	// on every tick. A file with an entry is finished on later ticks whatever
	// its mtime says.
	recOffsets map[string]int64
	// recTails is the overlap per recording file, like logTail.
	recTails map[string][]byte
	// activeDirs is when a recording directory last had a file change or
	// leave bytes unscanned.
	activeDirs map[string]time.Time
	// walkAll makes the first scan visit every directory: a recording that is
	// still being appended to when the daemon starts has an old directory
	// mtime and no recorded activity yet.
	walkAll bool
	// warnedRoot keeps the missing-recordings-root warning to one line.
	warnedRoot bool
}

// NewLeakScanner builds a scanner. The log offset starts at the current end of
// the log file and the recordings baseline at now: only content that arrives
// after startup is scanned.
func NewLeakScanner(cfg Config) *LeakScanner {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &LeakScanner{
		cfg:        cfg,
		log:        log.With(slog.String("component", "leakscan")),
		lastTick:   time.Now(),
		recOffsets: make(map[string]int64),
		recTails:   make(map[string][]byte),
		activeDirs: make(map[string]time.Time),
		walkAll:    true,
	}
	if cfg.LogPath != "" {
		if st, err := os.Stat(cfg.LogPath); err == nil {
			s.logOffset = st.Size()
			s.logIno, s.logIDSet = fileID(st)
		}
	}
	return s
}

// Run scans every Interval until ctx is done.
func (s *LeakScanner) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.scanOnce(ctx)
		}
	}
}

// hit is one folded finding: all matches of one kind in one file this tick.
type hit struct {
	kind   string
	offset int64 // absolute offset of the first match
	count  int
	sample string // masked
}

// ScanOnce runs one scan of the log and the recordings and sends the alerts.
func (s *LeakScanner) ScanOnce() {
	s.scanOnce(context.Background())
}

func (s *LeakScanner) scanOnce(ctx context.Context) {
	tickStart := time.Now()
	s.scanLog()
	s.scanRecordings(ctx, tickStart)
	s.lastTick = tickStart
}

func (s *LeakScanner) scanLog() {
	path := s.cfg.LogPath
	if path == "" {
		return
	}
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	ino, idSet := fileID(st)
	replaced := idSet && s.logIDSet && ino != s.logIno
	if st.Size() < s.logOffset || replaced {
		// Truncated or rotated. internal/logging renames the live file to a
		// backup: the bytes written to it after the last tick are only there.
		s.scanRotatedBackup(path)
		s.logOffset = 0
		s.logTail = nil
	}
	s.logIno, s.logIDSet = ino, idSet
	if st.Size() == s.logOffset {
		return
	}
	res, err := scanFile(path, s.logOffset, true, s.logTail)
	if err != nil {
		s.log.Warn("leak scan: read log failed", slog.String("file", path), slog.Any("error", err))
		return
	}
	s.logOffset, s.logTail = res.next, res.tail
	s.report(path, res.hits)
}

// scanRotatedBackup scans the rest of the file the log was before it was
// rotated away: from the stored offset to its end.
func (s *LeakScanner) scanRotatedBackup(path string) {
	if s.logOffset == 0 {
		return
	}
	backup := s.findRotatedBackup(path)
	if backup == "" {
		return
	}
	res, err := scanFile(backup, s.logOffset, false, s.logTail)
	if err != nil {
		s.log.Warn("leak scan: read rotated log failed", slog.String("file", backup), slog.Any("error", err))
		return
	}
	s.report(backup, res.hits)
}

// findRotatedBackup returns the backup the live log was renamed to: the one
// that still carries the inode the offset belongs to, else the newest backup
// modified since the last tick. "" when there is none.
func (s *LeakScanner) findRotatedBackup(path string) string {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(base, ext) + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var (
		newest     string
		newestTime time.Time
	)
	for _, e := range entries {
		name := e.Name()
		if name == base || e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ext) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() < s.logOffset {
			continue
		}
		full := filepath.Join(dir, name)
		if ino, ok := fileID(info); ok && s.logIDSet && ino == s.logIno {
			return full
		}
		if info.ModTime().After(s.lastTick.Add(-mtimeSlack)) && (newest == "" || info.ModTime().After(newestTime)) {
			newest, newestTime = full, info.ModTime()
		}
	}
	return newest
}

// resolveRecordingsRoot resolves symlinks in the recordings root: WalkDir does
// not follow a symlink as the root, so a symlinked directory would otherwise
// be walked as a single entry and never scanned. "" when the root is missing.
func (s *LeakScanner) resolveRecordingsRoot() string {
	root := s.cfg.RecordingsPath
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		if !s.warnedRoot {
			s.warnedRoot = true
			s.log.Warn("leak scan: recordings directory not readable, recordings are not scanned",
				slog.String("path", root), slog.Any("error", err))
		}
		return ""
	}
	s.warnedRoot = false
	return resolved
}

func (s *LeakScanner) scanRecordings(ctx context.Context, tickStart time.Time) {
	if s.cfg.RecordingsPath == "" {
		return
	}
	root := s.resolveRecordingsRoot()
	if root == "" {
		return
	}
	since := s.lastTick.Add(-mtimeSlack)
	seenFiles := make(map[string]bool)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			// A new file bumps the directory's mtime; an append to an old
			// file does not, so a recently active directory stays listed.
			info, ierr := d.Info()
			if ierr != nil {
				return fs.SkipDir
			}
			if !s.walkAll && !info.ModTime().After(since) && tickStart.Sub(s.activeDirs[path]) > activeGrace {
				return fs.SkipDir
			}
			return nil
		}
		if !recordingFiles[d.Name()] {
			return nil
		}
		seenFiles[path] = true
		info, err := d.Info()
		if err != nil {
			return nil
		}
		dir := filepath.Dir(path)
		off, known := s.recOffsets[path]
		if info.Size() < off {
			off = 0
			delete(s.recTails, path)
		}
		// A file seen before is finished whatever its mtime says: a recording
		// larger than maxPerTick takes several ticks. A new file must have
		// been modified since the last tick, which skips old content that
		// predates startup.
		if !known && !info.ModTime().After(since) {
			return nil
		}
		if info.ModTime().After(since) {
			s.activeDirs[dir] = tickStart
		}
		if info.Size() == off {
			return nil
		}
		s.activeDirs[dir] = tickStart
		res, err := scanFile(path, off, false, s.recTails[path])
		if err != nil {
			s.log.Warn("leak scan: read recording failed", slog.String("file", path), slog.Any("error", err))
			return nil
		}
		s.recOffsets[path], s.recTails[path] = res.next, res.tail
		s.report(path, res.hits)
		return nil
	})
	if ctx.Err() != nil {
		return
	}
	s.walkAll = false
	// Forget files that are gone and files in directories past their grace
	// (those were skipped above, so they are not in seenFiles).
	for p := range s.recOffsets {
		if seenFiles[p] {
			continue
		}
		delete(s.recOffsets, p)
		delete(s.recTails, p)
	}
	for dir, at := range s.activeDirs {
		if tickStart.Sub(at) > activeGrace {
			delete(s.activeDirs, dir)
		}
	}
}

// report counts the hits and sends one security event per kind.
func (s *LeakScanner) report(file string, hits []hit) {
	for _, h := range hits {
		addHits(h.kind, int64(h.count))
		s.log.Warn("leak scan: secret found",
			slog.String("file", file),
			slog.Int64("offset", h.offset),
			slog.String("kind", h.kind),
			slog.Int("count", h.count),
			slog.String("sample", h.sample))
		if s.cfg.Sink == nil {
			continue
		}
		s.cfg.Sink.ProcessEvent(alerts.Event{
			Type:   alerts.EventTypeSecurityEvent,
			Source: alerts.SecurityEventSourceLeakScan,
			Error:  "possible secret in " + file,
			Metadata: map[string]string{
				"file":   file,
				"offset": strconv.FormatInt(h.offset, 10),
				"kind":   h.kind,
				"sample": h.sample,
				"count":  strconv.Itoa(h.count),
			},
			Timestamp: time.Now(),
		})
	}
}

// maskSample keeps the first 4 characters of a secret and drops the rest.
func maskSample(b []byte) string {
	n := 0
	for i := range string(b) {
		if n == 4 {
			return string(b[:i]) + "…"
		}
		n++
	}
	return string(b) + "…"
}

// holdBackPartialLine returns end moved back to just past the last newline in
// [offset, end), so a line still being written is scanned whole on a later
// tick instead of being split across two. When the unterminated tail is longer
// than maxPartialLine, or holds no newline at all, end is returned unchanged.
func holdBackPartialLine(f *os.File, offset, end int64) int64 {
	start := end - maxPartialLine
	if start < offset {
		start = offset
	}
	tail := make([]byte, end-start)
	if _, err := f.ReadAt(tail, start); err != nil && !errors.Is(err, io.EOF) {
		return end
	}
	if i := bytes.LastIndexByte(tail, '\n'); i >= 0 {
		return start + int64(i) + 1
	}
	return end
}

// scanResult is what one scanFile call found.
type scanResult struct {
	hits []hit  // folded, sorted by offset
	next int64  // offset to resume from
	tail []byte // last tickOverlap bytes read, for the next call's prefix
}

// scanFile scans path from offset. prefix is the tail of the previous read, the
// bytes just before offset; it is matched together with the new bytes so a
// value split across two reads is found, and a match wholly inside it (already
// reported) is not counted again. With holdPartial an unterminated last line
// is left for the next scan.
func scanFile(path string, offset int64, holdPartial bool, prefix []byte) (scanResult, error) {
	unchanged := scanResult{next: offset, tail: prefix}
	f, err := os.Open(path)
	if err != nil {
		return unchanged, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return unchanged, err
	}
	end := st.Size()
	if end-offset > maxPerTick {
		end = offset + maxPerTick
	}
	if end <= offset {
		return unchanged, nil
	}
	if holdPartial {
		end = holdBackPartialLine(f, offset, end)
	}
	if end <= offset {
		return unchanged, nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return unchanged, err
	}

	var (
		folded = make(map[string]*hit)
		carry  = append([]byte(nil), prefix...) // bytes just before pos
		pos    = offset
		buf    = make([]byte, chunkSize)
	)
	for pos < end {
		want := int64(len(buf))
		if end-pos < want {
			want = end - pos
		}
		n, rerr := io.ReadFull(f, buf[:want])
		if n > 0 {
			window := append(append(make([]byte, 0, len(carry)+n), carry...), buf[:n]...)
			windowStart := pos - int64(len(carry))
			// A variable-length match that ended inside the carry and now
			// runs on into the new bytes was reported once already.
			reported := make(map[matchKey]bool)
			if len(carry) > 0 {
				for _, m := range secretpatterns.FindMatches(carry) {
					reported[matchKey{m.Kind, m.Start}] = true
				}
			}
			for _, m := range secretpatterns.FindMatches(window) {
				if m.End <= len(carry) || reported[matchKey{m.Kind, m.Start}] {
					continue // seen in the previous chunk or read
				}
				h := folded[m.Kind]
				if h == nil {
					h = &hit{kind: m.Kind, offset: windowStart + int64(m.Start), sample: maskSample(window[m.Start:m.End])}
					folded[m.Kind] = h
				}
				h.count++
			}
			pos += int64(n)
			keep := overlap
			if keep > len(window) {
				keep = len(window)
			}
			carry = append(carry[:0], window[len(window)-keep:]...)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				break
			}
			return unchanged, rerr
		}
	}

	out := make([]hit, 0, len(folded))
	for _, h := range folded {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].offset < out[j].offset })
	tail := carry
	if len(tail) > tickOverlap {
		tail = tail[len(tail)-tickOverlap:]
	}
	return scanResult{hits: out, next: pos, tail: append([]byte(nil), tail...)}, nil
}

// matchKey identifies a match inside one window.
type matchKey struct {
	kind  string
	start int
}

var (
	hitCountsMu sync.Mutex
	hitCounts   = map[string]int64{}
)

func addHits(kind string, n int64) {
	hitCountsMu.Lock()
	hitCounts[kind] += n
	hitCountsMu.Unlock()
}

// Hits returns a snapshot of pilot_leak_scan_hits_total by kind.
func Hits() map[string]int64 {
	hitCountsMu.Lock()
	defer hitCountsMu.Unlock()
	out := make(map[string]int64, len(hitCounts))
	for k, v := range hitCounts {
		out[k] = v
	}
	return out
}
