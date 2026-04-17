package executor

import "sync"

// boundedBuffer is a thread-safe byte buffer with a hard size cap.
// When writes would exceed the cap, the oldest bytes are discarded so
// the most recent output (which error classifiers actually inspect) is
// retained. GH-2332: prevents runaway stderr accumulation in long
// Claude Code sessions from OOM-killing Pilot itself.
type boundedBuffer struct {
	mu       sync.Mutex
	data     []byte
	cap      int
	dropped  int64 // total bytes dropped due to truncation
	overflow bool  // true once any truncation has occurred
}

// newBoundedBuffer creates a buffer capped at maxBytes total length.
// maxBytes <= 0 disables capping (unbounded).
func newBoundedBuffer(maxBytes int) *boundedBuffer {
	return &boundedBuffer{cap: maxBytes}
}

// WriteLine appends line + "\n" to the buffer, tail-truncating if needed.
func (b *boundedBuffer) WriteLine(line string) {
	b.write([]byte(line))
	b.write([]byte{'\n'})
}

// WriteString appends s to the buffer, tail-truncating if needed.
func (b *boundedBuffer) WriteString(s string) {
	b.write([]byte(s))
}

func (b *boundedBuffer) write(p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.cap <= 0 {
		b.data = append(b.data, p...)
		return
	}

	// Incoming payload is larger than the entire cap: keep only its tail.
	if len(p) >= b.cap {
		dropTotal := int64(len(b.data)) + int64(len(p)-b.cap)
		b.data = append(b.data[:0], p[len(p)-b.cap:]...)
		b.dropped += dropTotal
		b.overflow = true
		return
	}

	// Normal append: drop from head as needed to make room for p.
	if need := len(b.data) + len(p) - b.cap; need > 0 {
		b.data = b.data[need:]
		b.dropped += int64(need)
		b.overflow = true
	}
	b.data = append(b.data, p...)
}

// String returns the current buffered content. If any truncation occurred,
// a single-line prefix marker is prepended so downstream loggers know the
// buffer was clipped.
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.overflow {
		return string(b.data)
	}
	// Minimal marker — classifyClaudeCodeError does substring matches on
	// the tail (rate_limit, api_error, etc.), so a prefix is safe.
	return "[stderr truncated; older output dropped]\n" + string(b.data)
}

// Len returns the current buffered byte count (post-truncation).
func (b *boundedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.data)
}

// Dropped returns the total bytes discarded due to truncation.
func (b *boundedBuffer) Dropped() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}
