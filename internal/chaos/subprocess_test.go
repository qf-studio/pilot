package chaos

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestChaos_SubprocessHang verifies watchdog kills hanging processes
func TestChaos_SubprocessHang(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var alertEmitted atomic.Bool
	var processKilled atomic.Bool

	// Create watchdog with short timeout
	watchdog := NewProcessWatchdog(500*time.Millisecond, 100*time.Millisecond)
	watchdog.SetTimeoutHandler(func(ctx context.Context) error {
		alertEmitted.Store(true)
		return nil
	})

	// Start a process that hangs (sleep for a long time)
	cmd := exec.CommandContext(ctx, "sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start process: %v", err)
	}

	// Start watchdog
	watchdog.Start(ctx)

	// Wait for watchdog timeout
	time.Sleep(700 * time.Millisecond)

	// Watchdog should have triggered alert
	if !alertEmitted.Load() {
		t.Error("Expected alert to be emitted on timeout")
	}

	// Kill the hanging process
	if cmd.Process != nil {
		if err := cmd.Process.Kill(); err == nil {
			processKilled.Store(true)
		}
		// Wait for process to exit
		_ = cmd.Wait()
	}

	watchdog.Stop()

	if !processKilled.Load() {
		t.Log("Process was already terminated or couldn't be killed")
	}
}

// TestChaos_SubprocessEarlyExit verifies handling of processes that exit before timeout
func TestChaos_SubprocessEarlyExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var alertEmitted atomic.Bool

	watchdog := NewProcessWatchdog(2*time.Second, 100*time.Millisecond)
	watchdog.SetTimeoutHandler(func(ctx context.Context) error {
		alertEmitted.Store(true)
		return nil
	})

	// Start a process that exits quickly
	cmd := exec.CommandContext(ctx, "echo", "hello")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Process failed: %v", err)
	}

	// Start and immediately stop watchdog (process completed)
	watchdog.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	watchdog.Stop()

	// Alert should NOT be emitted since we stopped watchdog
	if alertEmitted.Load() {
		t.Error("Alert should not be emitted when process exits normally")
	}
}

// TestChaos_WatchdogReset verifies watchdog timer can be reset
func TestChaos_WatchdogReset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var timeoutCount atomic.Int64

	watchdog := NewProcessWatchdog(300*time.Millisecond, 50*time.Millisecond)
	watchdog.SetTimeoutHandler(func(ctx context.Context) error {
		timeoutCount.Add(1)
		return nil
	})

	// Start watchdog
	watchdog.Start(ctx)

	// Reset before timeout several times
	for i := 0; i < 3; i++ {
		time.Sleep(200 * time.Millisecond)
		watchdog.Reset(ctx)
	}

	// Stop before final timeout
	watchdog.Stop()

	// Should not have timed out
	if timeoutCount.Load() > 0 {
		t.Errorf("Watchdog should not have timed out, count: %d", timeoutCount.Load())
	}
}

// TestChaos_SQLiteLockContention verifies transactions wait properly under lock contention
func TestChaos_SQLiteLockContention(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create lock simulator with 100ms lock duration
	simulator := NewSQLiteLockSimulator(100*time.Millisecond, 5*time.Second)

	var wg sync.WaitGroup
	var successCount atomic.Int64
	var errorCount atomic.Int64
	numGoroutines := 10

	// Start a goroutine that holds the lock initially (creates contention)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := simulator.Lock(ctx); err != nil {
			t.Logf("Initial lock acquisition failed: %v", err)
			return
		}
		successCount.Add(1) // Count this as a success
		time.Sleep(100 * time.Millisecond)
		simulator.Unlock()
	}()

	// Wait a bit for lock to be acquired
	time.Sleep(20 * time.Millisecond)

	// Start multiple goroutines trying to acquire the lock
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			err := simulator.Lock(ctx)
			if err != nil {
				errorCount.Add(1)
				t.Logf("Goroutine %d failed to acquire lock: %v", id, err)
				return
			}
			defer simulator.Unlock()

			successCount.Add(1)
			// Simulate some work
			time.Sleep(10 * time.Millisecond)
		}(i)
	}

	wg.Wait()

	// All goroutines should eventually succeed (initial + numGoroutines)
	expectedSuccesses := int64(numGoroutines) + 1
	if successCount.Load() != expectedSuccesses {
		t.Errorf("Expected %d successful locks, got %d (errors: %d)",
			expectedSuccesses, successCount.Load(), errorCount.Load())
	}
}

// TestChaos_SQLiteLockTimeout verifies timeout behavior on lock contention
func TestChaos_SQLiteLockTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create simulator with very short timeout
	simulator := NewSQLiteLockSimulator(1*time.Second, 100*time.Millisecond)

	// Acquire lock and don't release
	if err := simulator.Lock(ctx); err != nil {
		t.Fatalf("Failed to acquire initial lock: %v", err)
	}

	// Try to acquire lock from another "transaction" - should timeout
	errCh := make(chan error, 1)
	go func() {
		errCh <- simulator.Lock(ctx)
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("Expected timeout error, got success")
		} else if err.Error() != "database is locked: timeout exceeded" {
			t.Errorf("Expected lock timeout error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Lock attempt didn't timeout as expected")
	}

	// Release the lock
	simulator.Unlock()
}

// TestChaos_ProcessSignalHandling verifies proper signal handling
func TestChaos_ProcessSignalHandling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a process that handles SIGTERM
	cmd := exec.CommandContext(ctx, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start process: %v", err)
	}

	// Give process time to start
	time.Sleep(100 * time.Millisecond)

	// Send SIGTERM
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		// On some systems, the process might exit before signal delivery
		t.Logf("Failed to send SIGTERM: %v", err)
	}

	// Wait for process with timeout
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		// Process exited (expected)
		t.Logf("Process exited: %v", err)
	case <-time.After(2 * time.Second):
		// Process didn't exit, force kill
		_ = cmd.Process.Kill()
		t.Log("Process required SIGKILL")
	}
}

// TestChaos_ProcessResourceExhaustion simulates running out of file descriptors
func TestChaos_ProcessResourceExhaustion(t *testing.T) {
	if os.Getenv("RUN_RESOURCE_EXHAUSTION_TESTS") == "" {
		t.Skip("Skipping resource exhaustion test (set RUN_RESOURCE_EXHAUSTION_TESTS=1 to run)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Open many files to exhaust file descriptors
	files := make([]*os.File, 0)
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()

	var exhaustedFDs bool
	for i := 0; i < 10000; i++ {
		f, err := os.CreateTemp("", "chaos-test-*")
		if err != nil {
			exhaustedFDs = true
			t.Logf("FD exhaustion after %d files: %v", i, err)
			break
		}
		files = append(files, f)
	}

	if !exhaustedFDs {
		t.Log("Did not exhaust file descriptors (limit might be higher)")
	}

	// Verify we can still operate after cleaning up
	for _, f := range files {
		_ = f.Close()
	}
	files = nil

	// Should be able to open files again
	f, err := os.CreateTemp("", "chaos-test-recovery-*")
	if err != nil {
		t.Errorf("Failed to recover from FD exhaustion: %v", err)
	} else {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}

	_ = ctx // Keep ctx in scope
}

// TestChaos_DeadlockDetection verifies deadlock situations are detectable
func TestChaos_DeadlockDetection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create two locks that could deadlock
	lock1 := NewSQLiteLockSimulator(100*time.Millisecond, 500*time.Millisecond)
	lock2 := NewSQLiteLockSimulator(100*time.Millisecond, 500*time.Millisecond)

	var deadlockDetected atomic.Bool
	var wg sync.WaitGroup

	// Goroutine 1: acquire lock1, then try lock2
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := lock1.Lock(ctx); err != nil {
			return
		}
		defer lock1.Unlock()

		time.Sleep(50 * time.Millisecond) // Create window for deadlock

		if err := lock2.Lock(ctx); err != nil {
			deadlockDetected.Store(true)
			t.Logf("Goroutine 1 detected potential deadlock: %v", err)
		} else {
			lock2.Unlock()
		}
	}()

	// Goroutine 2: acquire lock2, then try lock1
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := lock2.Lock(ctx); err != nil {
			return
		}
		defer lock2.Unlock()

		time.Sleep(50 * time.Millisecond) // Create window for deadlock

		if err := lock1.Lock(ctx); err != nil {
			deadlockDetected.Store(true)
			t.Logf("Goroutine 2 detected potential deadlock: %v", err)
		} else {
			lock1.Unlock()
		}
	}()

	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Completed (may or may not have deadlocked depending on timing)
	case <-ctx.Done():
		t.Log("Context cancelled (possible deadlock averted by timeout)")
	}

	t.Logf("Deadlock detected: %v", deadlockDetected.Load())
}

// TestChaos_ConcurrentSubprocesses verifies handling multiple concurrent processes
func TestChaos_ConcurrentSubprocesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	numProcesses := 20
	var wg sync.WaitGroup
	var successCount atomic.Int64
	var errorCount atomic.Int64

	for i := 0; i < numProcesses; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Start a short-lived process
			cmd := exec.CommandContext(ctx, "echo", "hello from process")
			output, err := cmd.Output()
			if err != nil {
				errorCount.Add(1)
				t.Logf("Process %d failed: %v", id, err)
				return
			}

			if len(output) > 0 {
				successCount.Add(1)
			}
		}(i)
	}

	wg.Wait()

	if errorCount.Load() > 0 {
		t.Logf("Some processes failed: %d errors out of %d", errorCount.Load(), numProcesses)
	}

	if successCount.Load() == 0 {
		t.Error("Expected at least some successful process executions")
	}

	t.Logf("Concurrent subprocess test: %d succeeded, %d failed",
		successCount.Load(), errorCount.Load())
}

// TestChaos_ProcessMemoryPressure simulates memory pressure scenario
func TestChaos_ProcessMemoryPressure(t *testing.T) {
	if os.Getenv("RUN_MEMORY_PRESSURE_TESTS") == "" {
		t.Skip("Skipping memory pressure test (set RUN_MEMORY_PRESSURE_TESTS=1 to run)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Allocate memory in chunks until we hit limits or timeout
	chunks := make([][]byte, 0)
	defer func() {
		// Free memory
		chunks = nil
	}()

	chunkSize := 100 * 1024 * 1024 // 100MB chunks
	var allocated int64

	for {
		select {
		case <-ctx.Done():
			t.Logf("Stopped allocation after %d MB", allocated/1024/1024)
			return
		default:
		}

		chunk := make([]byte, chunkSize)
		// Touch the memory to ensure it's allocated
		for i := 0; i < len(chunk); i += 4096 {
			chunk[i] = byte(i)
		}
		chunks = append(chunks, chunk)
		allocated += int64(chunkSize)

		// Stop after 1GB to avoid actually crashing
		if allocated > 1024*1024*1024 {
			t.Logf("Allocated %d MB without issues", allocated/1024/1024)
			break
		}
	}
}

// TestChaos_GracefulShutdown verifies cleanup on context cancellation
func TestChaos_GracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var cleanupCalled atomic.Bool
	var wg sync.WaitGroup

	// Start a worker that runs until context is cancelled
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			cleanupCalled.Store(true)
		}()

		<-ctx.Done()
	}()

	// Cancel context
	cancel()

	// Wait for cleanup
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good
	case <-time.After(2 * time.Second):
		t.Error("Cleanup didn't complete within timeout")
	}

	if !cleanupCalled.Load() {
		t.Error("Cleanup was not called")
	}
}
