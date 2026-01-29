package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewMergeWaiter(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)

	t.Run("with default config", func(t *testing.T) {
		waiter := NewMergeWaiter(client, "owner", "repo", nil)

		if waiter == nil {
			t.Fatal("NewMergeWaiter returned nil")
		}
		if waiter.config.PollInterval != 30*time.Second {
			t.Errorf("default PollInterval = %v, want 30s", waiter.config.PollInterval)
		}
		if waiter.config.Timeout != 1*time.Hour {
			t.Errorf("default Timeout = %v, want 1h", waiter.config.Timeout)
		}
	})

	t.Run("with custom config", func(t *testing.T) {
		cfg := &MergeWaiterConfig{
			PollInterval: 10 * time.Second,
			Timeout:      30 * time.Minute,
		}
		waiter := NewMergeWaiter(client, "owner", "repo", cfg)

		if waiter.config.PollInterval != 10*time.Second {
			t.Errorf("PollInterval = %v, want 10s", waiter.config.PollInterval)
		}
		if waiter.config.Timeout != 30*time.Minute {
			t.Errorf("Timeout = %v, want 30m", waiter.config.Timeout)
		}
	})
}

func TestMergeWaiter_WaitForMerge_AlreadyMerged(t *testing.T) {
	pr := &PullRequest{
		Number:  123,
		State:   "closed",
		Merged:  true,
		HTMLURL: "https://github.com/owner/repo/pull/123",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	waiter := NewMergeWaiter(client, "owner", "repo", &MergeWaiterConfig{
		PollInterval: 10 * time.Millisecond,
		Timeout:      1 * time.Second,
	})

	result, err := waiter.WaitForMerge(context.Background(), 123)

	if err != nil {
		t.Fatalf("WaitForMerge() error = %v", err)
	}
	if !result.Merged {
		t.Error("result.Merged should be true")
	}
	if result.Closed {
		t.Error("result.Closed should be false for merged PRs")
	}
	if result.PRNumber != 123 {
		t.Errorf("result.PRNumber = %d, want 123", result.PRNumber)
	}
}

func TestMergeWaiter_WaitForMerge_ClosedWithoutMerge(t *testing.T) {
	pr := &PullRequest{
		Number:  123,
		State:   "closed",
		Merged:  false,
		HTMLURL: "https://github.com/owner/repo/pull/123",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	waiter := NewMergeWaiter(client, "owner", "repo", &MergeWaiterConfig{
		PollInterval: 10 * time.Millisecond,
		Timeout:      1 * time.Second,
	})

	result, err := waiter.WaitForMerge(context.Background(), 123)

	if err != nil {
		t.Fatalf("WaitForMerge() error = %v", err)
	}
	if result.Merged {
		t.Error("result.Merged should be false")
	}
	if !result.Closed {
		t.Error("result.Closed should be true")
	}
}

func TestMergeWaiter_WaitForMerge_Conflicting(t *testing.T) {
	mergeable := false
	pr := &PullRequest{
		Number:    123,
		State:     "open",
		Merged:    false,
		Mergeable: &mergeable,
		HTMLURL:   "https://github.com/owner/repo/pull/123",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	waiter := NewMergeWaiter(client, "owner", "repo", &MergeWaiterConfig{
		PollInterval: 10 * time.Millisecond,
		Timeout:      1 * time.Second,
	})

	result, err := waiter.WaitForMerge(context.Background(), 123)

	if err != nil {
		t.Fatalf("WaitForMerge() error = %v", err)
	}
	if !result.Conflicting {
		t.Error("result.Conflicting should be true")
	}
}

func TestMergeWaiter_WaitForMerge_Timeout(t *testing.T) {
	// PR stays open and mergeable
	mergeable := true
	pr := &PullRequest{
		Number:    123,
		State:     "open",
		Merged:    false,
		Mergeable: &mergeable,
		HTMLURL:   "https://github.com/owner/repo/pull/123",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	waiter := NewMergeWaiter(client, "owner", "repo", &MergeWaiterConfig{
		PollInterval: 10 * time.Millisecond,
		Timeout:      50 * time.Millisecond,
	})

	result, err := waiter.WaitForMerge(context.Background(), 123)

	if err != ErrMergeTimeout {
		t.Errorf("WaitForMerge() error = %v, want ErrMergeTimeout", err)
	}
	if result == nil {
		t.Fatal("result should not be nil on timeout")
	}
	if !result.TimedOut {
		t.Error("result.TimedOut should be true")
	}
}

func TestMergeWaiter_WaitForMerge_ContextCancelled(t *testing.T) {
	// PR stays open - will never merge in this test
	mergeable := true
	pr := &PullRequest{
		Number:    123,
		State:     "open",
		Merged:    false,
		Mergeable: &mergeable,
		HTMLURL:   "https://github.com/owner/repo/pull/123",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	waiter := NewMergeWaiter(client, "owner", "repo", &MergeWaiterConfig{
		PollInterval: 50 * time.Millisecond,
		Timeout:      10 * time.Second, // Long timeout
	})

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := waiter.WaitForMerge(ctx, 123)

	if err != context.Canceled {
		t.Errorf("WaitForMerge() error = %v, want context.Canceled", err)
	}
}

func TestMergeWaiter_WaitForMerge_EventuallyMerges(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)

		pr := &PullRequest{
			Number:  123,
			HTMLURL: "https://github.com/owner/repo/pull/123",
		}

		// First 2 calls: PR is open
		// 3rd call: PR is merged
		if count < 3 {
			mergeable := true
			pr.State = "open"
			pr.Merged = false
			pr.Mergeable = &mergeable
		} else {
			pr.State = "closed"
			pr.Merged = true
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	waiter := NewMergeWaiter(client, "owner", "repo", &MergeWaiterConfig{
		PollInterval: 10 * time.Millisecond,
		Timeout:      1 * time.Second,
	})

	result, err := waiter.WaitForMerge(context.Background(), 123)

	if err != nil {
		t.Fatalf("WaitForMerge() error = %v", err)
	}
	if !result.Merged {
		t.Error("result.Merged should be true")
	}
	if atomic.LoadInt32(&callCount) < 3 {
		t.Errorf("expected at least 3 API calls, got %d", callCount)
	}
}

func TestMergeWaiter_WaitWithCallback(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)

		pr := &PullRequest{
			Number:  123,
			HTMLURL: "https://github.com/owner/repo/pull/123",
		}

		if count < 2 {
			mergeable := true
			pr.State = "open"
			pr.Merged = false
			pr.Mergeable = &mergeable
		} else {
			pr.State = "closed"
			pr.Merged = true
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	waiter := NewMergeWaiter(client, "owner", "repo", &MergeWaiterConfig{
		PollInterval: 10 * time.Millisecond,
		Timeout:      1 * time.Second,
	})

	callbackCount := 0
	result, err := waiter.WaitWithCallback(context.Background(), 123, func(r *MergeWaitResult) {
		callbackCount++
	})

	if err != nil {
		t.Fatalf("WaitWithCallback() error = %v", err)
	}
	if !result.Merged {
		t.Error("result.Merged should be true")
	}
	if callbackCount < 2 {
		t.Errorf("callback called %d times, want at least 2", callbackCount)
	}
}

func TestExtractPRNumber(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    int
		wantErr bool
	}{
		{
			name:    "standard PR URL",
			url:     "https://github.com/owner/repo/pull/123",
			want:    123,
			wantErr: false,
		},
		{
			name:    "PR URL with trailing slash",
			url:     "https://github.com/owner/repo/pull/456/",
			want:    456,
			wantErr: false,
		},
		{
			name:    "PR URL with files path",
			url:     "https://github.com/owner/repo/pull/789/files",
			want:    789,
			wantErr: false,
		},
		{
			name:    "pulls endpoint",
			url:     "https://api.github.com/repos/owner/repo/pulls/42",
			want:    42,
			wantErr: false,
		},
		{
			name:    "empty URL",
			url:     "",
			want:    0,
			wantErr: true,
		},
		{
			name:    "non-PR URL",
			url:     "https://github.com/owner/repo/issues/123",
			want:    0,
			wantErr: true,
		},
		{
			name:    "invalid number",
			url:     "https://github.com/owner/repo/pull/abc",
			want:    0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractPRNumber(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractPRNumber() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ExtractPRNumber() = %v, want %v", got, tt.want)
			}
		})
	}
}
