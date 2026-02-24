package telegram

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// Transport handles Telegram polling and delegates message processing to Handler.
type Transport struct {
	client    *Client  // Telegram bot API client
	handler   *Handler // Handler for business logic
	offset    int64    // Last processed update ID
	mu        sync.Mutex
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// NewTransport creates a new Telegram transport layer.
func NewTransport(client *Client, handler *Handler) *Transport {
	return &Transport{
		client:  client,
		handler: handler,
		stopCh:  make(chan struct{}),
	}
}

// StartPolling begins the long-polling loop in a goroutine.
func (t *Transport) StartPolling(ctx context.Context) {
	t.wg.Add(1)
	go t.pollLoop(ctx)

	t.wg.Add(1)
	go t.cleanupLoop(ctx)
}

// Stop gracefully stops the polling loop.
func (t *Transport) Stop() {
	close(t.stopCh)
	t.wg.Wait()
}

// pollLoop continuously fetches and processes updates.
func (t *Transport) pollLoop(ctx context.Context) {
	defer t.wg.Done()

	logging.WithComponent("telegram").Debug("Transport poll loop started")

	for {
		select {
		case <-ctx.Done():
			logging.WithComponent("telegram").Debug("Transport poll loop stopped")
			return
		case <-t.stopCh:
			logging.WithComponent("telegram").Debug("Transport poll loop stopped")
			return
		default:
			t.fetchAndProcess(ctx)
		}
	}
}

// cleanupLoop removes expired pending tasks periodically.
func (t *Transport) cleanupLoop(ctx context.Context) {
	defer t.wg.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.stopCh:
			return
		case <-ticker.C:
			if t.handler != nil {
				t.handler.cleanupExpiredTasks(ctx)
			}
		}
	}
}

// fetchAndProcess fetches updates from Telegram and processes them.
func (t *Transport) fetchAndProcess(ctx context.Context) {
	updates, err := t.client.GetUpdates(ctx, t.offset, 30)
	if err != nil {
		if ctx.Err() == nil {
			logging.WithComponent("telegram").Warn("Error fetching updates", slog.Any("error", err));
		}
		time.Sleep(time.Second)
		return
	}

	for _, update := range updates {
		t.processUpdate(ctx, update)

		// Update offset to acknowledge this update
		t.mu.Lock()
		if update.UpdateID >= t.offset {
			t.offset = update.UpdateID + 1
		}
		t.mu.Unlock()
	}
}

// processUpdate processes a Telegram update and dispatches to handler.
// This delegates to the existing handler methods for business logic.
func (t *Transport) processUpdate(ctx context.Context, update *Update) {
	if t.handler == nil {
		return
	}

	// Delegate to handler's existing processUpdate logic
	t.handler.processUpdate(ctx, update)
}

