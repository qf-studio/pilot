package executor

import (
	"strings"
	"sync"
	"testing"
)

func TestBoundedBuffer_UnderCap(t *testing.T) {
	b := newBoundedBuffer(100)
	b.WriteLine("hello")
	b.WriteLine("world")

	got := b.String()
	want := "hello\nworld\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if b.Dropped() != 0 {
		t.Fatalf("expected 0 dropped bytes, got %d", b.Dropped())
	}
}

func TestBoundedBuffer_TailTruncation(t *testing.T) {
	b := newBoundedBuffer(10)
	b.WriteString("0123456789") // exactly at cap
	b.WriteString("ABCDE")      // forces head-drop of 5

	got := b.String()
	// Truncation marker should be prepended; trailing content is last 10 bytes.
	if !strings.HasPrefix(got, "[stderr truncated") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	if !strings.HasSuffix(got, "56789ABCDE") {
		t.Fatalf("expected tail 56789ABCDE, got %q", got)
	}
	if b.Dropped() != 5 {
		t.Fatalf("expected 5 dropped bytes, got %d", b.Dropped())
	}
}

func TestBoundedBuffer_SingleWriteLargerThanCap(t *testing.T) {
	b := newBoundedBuffer(5)
	b.WriteString("abcdefghij") // 10 bytes into 5-byte cap

	got := b.String()
	if !strings.HasSuffix(got, "fghij") {
		t.Fatalf("expected tail fghij, got %q", got)
	}
	if b.Len() != 5 {
		t.Fatalf("expected len 5, got %d", b.Len())
	}
	if b.Dropped() != 5 {
		t.Fatalf("expected 5 dropped bytes, got %d", b.Dropped())
	}
}

func TestBoundedBuffer_UnboundedWhenCapZero(t *testing.T) {
	b := newBoundedBuffer(0)
	for i := 0; i < 1000; i++ {
		b.WriteString("x")
	}
	if b.Len() != 1000 {
		t.Fatalf("expected 1000 bytes, got %d", b.Len())
	}
	if b.Dropped() != 0 {
		t.Fatalf("expected 0 dropped, got %d", b.Dropped())
	}
}

func TestBoundedBuffer_ConcurrentWrites(t *testing.T) {
	b := newBoundedBuffer(10000)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b.WriteLine("line")
			}
		}()
	}
	wg.Wait()

	// Total writes: 50 * 100 * 5 = 25000 bytes; cap is 10000.
	if b.Len() > 10000 {
		t.Fatalf("buffer exceeded cap: %d", b.Len())
	}
	// Should have dropped at least 15000 bytes.
	if b.Dropped() < 15000 {
		t.Fatalf("expected at least 15000 dropped, got %d", b.Dropped())
	}
}
