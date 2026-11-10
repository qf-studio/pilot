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
	// maxPerTick bounds the bytes scanned per file per tick, keeping a huge
	// backlog from stalling the loop; the remainder is scanned next tick.
	maxPerTick = 64 << 20
	// maxPartialLine is how long an unterminated log tail may grow before it
	// is scanned anyway. Normally the tail is held back until its newline
	// arrives so a line being written is not split across two ticks.
	maxPartialLine = 1 << 20
)

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
	// lastTick is when recordings were last scanned; only files modified
	// after it (less mtimeSlack) are visited.
	lastTick time.Time
	// recOffsets is the scanned size per recording file, so an append-only
	// stream.jsonl is scanned incrementally and an old hit is not re-alerted
	// on every tick.
	recOffsets map[string]int64
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
	}
	if cfg.LogPath != "" {
		if st, err := os.Stat(cfg.LogPath); err == nil {
			s.logOffset = st.Size()
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
			s.ScanOnce()
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
	tickStart := time.Now()
	s.scanLog()
	s.scanRecordings()
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
	if st.Size() < s.logOffset {
		// Truncated or rotated: start over from the top of the new file.
		s.logOffset = 0
	}
	if st.Size() == s.logOffset {
		return
	}
	hits, next, err := scanFile(path, s.logOffset, true)
	if err != nil {
		s.log.Warn("leak scan: read log failed", slog.String("file", path), slog.Any("error", err))
		return
	}
	s.logOffset = next
	s.report(path, hits)
}

func (s *LeakScanner) scanRecordings() {
	root := s.cfg.RecordingsPath
	if root == "" {
		return
	}
	seen := make(map[string]bool)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !recordingFiles[d.Name()] {
			return nil
		}
		seen[path] = true
		info, err := d.Info()
		if err != nil {
			return nil
		}
		off := s.recOffsets[path]
		if info.Size() < off {
			off = 0
		}
		if !info.ModTime().After(s.lastTick.Add(-mtimeSlack)) || info.Size() == off {
			return nil
		}
		hits, next, err := scanFile(path, off, false)
		if err != nil {
			s.log.Warn("leak scan: read recording failed", slog.String("file", path), slog.Any("error", err))
			return nil
		}
		s.recOffsets[path] = next
		s.report(path, hits)
		return nil
	})
	for p := range s.recOffsets {
		if !seen[p] {
			delete(s.recOffsets, p)
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

// scanFile scans path from offset and returns the folded hits (sorted by
// offset) and the offset to resume from. With holdPartial an unterminated last
// line is left for the next scan.
func scanFile(path string, offset int64, holdPartial bool) ([]hit, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	end := st.Size()
	if end-offset > maxPerTick {
		end = offset + maxPerTick
	}
	if end <= offset {
		return nil, offset, nil
	}
	if holdPartial {
		end = holdBackPartialLine(f, offset, end)
	}
	if end <= offset {
		return nil, offset, nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}

	var (
		folded = make(map[string]*hit)
		carry  []byte // last bytes of the previous chunk
		base   = offset
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
			for _, m := range secretpatterns.FindMatches(window) {
				if m.End <= len(carry) {
					continue // wholly inside the overlap: seen last chunk
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
			return nil, base, rerr
		}
	}

	next := pos
	out := make([]hit, 0, len(folded))
	for _, h := range folded {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].offset < out[j].offset })
	return out, next, nil
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
