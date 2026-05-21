package executor

import (
	"context"
	"time"
)

// RSSSample holds peak and final RSS readings from a subprocess.
type RSSSample struct {
	PeakMB  int
	FinalMB int
}

// StartRSSSampler polls the subprocess RSS every interval until ctx is cancelled,
// then returns the peak and final readings.  Implementations are split by OS:
// rss_sampler_linux.go reads /proc/<pid>/status; rss_sampler_darwin.go uses
// `ps -o rss=`; rss_sampler_other.go is a no-op that always returns zero.
func StartRSSSampler(ctx context.Context, pid int, interval time.Duration) <-chan RSSSample {
	ch := make(chan RSSSample, 1)
	go func() {
		var peak int
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				final := readRSSMB(pid)
				if final > peak {
					peak = final
				}
				ch <- RSSSample{PeakMB: peak, FinalMB: final}
				return
			case <-ticker.C:
				if rss := readRSSMB(pid); rss > peak {
					peak = rss
				}
			}
		}
	}()
	return ch
}
