package alerts

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Channel is the interface for alert delivery channels
type Channel interface {
	// Name returns the channel name
	Name() string

	// Type returns the channel type (slack, telegram, etc.)
	Type() string

	// Send sends an alert through this channel
	Send(ctx context.Context, alert *Alert) error
}

// Dispatcher routes alerts to configured channels
type Dispatcher struct {
	channels map[string]Channel
	config   *AlertConfig
	logger   *slog.Logger
	mu       sync.RWMutex
}

// DispatcherOption configures the Dispatcher
type DispatcherOption func(*Dispatcher)

// WithDispatcherLogger sets the logger for the dispatcher
func WithDispatcherLogger(logger *slog.Logger) DispatcherOption {
	return func(d *Dispatcher) {
		d.logger = logger
	}
}

// NewDispatcher creates a new alert dispatcher
func NewDispatcher(config *AlertConfig, opts ...DispatcherOption) *Dispatcher {
	d := &Dispatcher{
		channels: make(map[string]Channel),
		config:   config,
		logger:   slog.Default(),
	}

	for _, opt := range opts {
		opt(d)
	}

	return d
}

// RegisterChannel registers a channel for alert delivery
func (d *Dispatcher) RegisterChannel(channel Channel) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.channels[channel.Name()] = channel
	d.logger.Info("registered alert channel",
		"name", channel.Name(),
		"type", channel.Type(),
	)
}

// UnregisterChannel removes a channel
func (d *Dispatcher) UnregisterChannel(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.channels, name)
}

// GetChannel returns a channel by name
func (d *Dispatcher) GetChannel(name string) (Channel, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	ch, ok := d.channels[name]
	return ch, ok
}

// ListChannels returns all registered channel names
func (d *Dispatcher) ListChannels() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	names := make([]string, 0, len(d.channels))
	for name := range d.channels {
		names = append(names, name)
	}
	return names
}

// Dispatch sends an alert to specified channels
func (d *Dispatcher) Dispatch(ctx context.Context, alert *Alert, channelNames []string) []DeliveryResult {
	results := make([]DeliveryResult, 0, len(channelNames))

	// Use WaitGroup for parallel delivery
	var wg sync.WaitGroup
	resultCh := make(chan DeliveryResult, len(channelNames))

	d.mu.RLock()
	for _, name := range channelNames {
		channel, ok := d.channels[name]
		if !ok {
			results = append(results, DeliveryResult{
				ChannelName: name,
				Success:     false,
				Error:       ErrChannelNotFound,
				SentAt:      time.Now(),
			})
			continue
		}

		wg.Add(1)
		go func(ch Channel, chName string) {
			defer wg.Done()
			result := d.sendToChannel(ctx, ch, alert)
			result.ChannelName = chName
			resultCh <- result
		}(channel, name)
	}
	d.mu.RUnlock()

	// Wait for all deliveries to complete
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect results
	for result := range resultCh {
		results = append(results, result)
	}

	return results
}

// DispatchAll sends an alert to all enabled channels
func (d *Dispatcher) DispatchAll(ctx context.Context, alert *Alert) []DeliveryResult {
	d.mu.RLock()
	channelNames := make([]string, 0, len(d.channels))
	for name := range d.channels {
		// Check if channel is enabled in config
		for _, cfg := range d.config.Channels {
			if cfg.Name == name && cfg.Enabled {
				// Check severity filter
				if d.severityMatches(cfg.Severities, alert.Severity) {
					channelNames = append(channelNames, name)
				}
				break
			}
		}
	}
	d.mu.RUnlock()

	return d.Dispatch(ctx, alert, channelNames)
}

func (d *Dispatcher) severityMatches(severities []Severity, alertSeverity Severity) bool {
	if len(severities) == 0 {
		return true // No filter means accept all
	}
	for _, s := range severities {
		if s == alertSeverity {
			return true
		}
	}
	return false
}

func (d *Dispatcher) sendToChannel(ctx context.Context, ch Channel, alert *Alert) DeliveryResult {
	result := DeliveryResult{
		ChannelName: ch.Name(),
		SentAt:      time.Now(),
	}

	// Add timeout for delivery
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	err := ch.Send(ctx, alert)
	if err != nil {
		result.Success = false
		result.Error = err
		d.logger.Error("failed to send alert",
			"channel", ch.Name(),
			"channel_type", ch.Type(),
			"alert_id", alert.ID,
			"error", err,
		)
	} else {
		result.Success = true
		d.logger.Debug("alert sent successfully",
			"channel", ch.Name(),
			"alert_id", alert.ID,
		)
	}

	return result
}

// ErrChannelNotFound is returned when a channel is not registered
var ErrChannelNotFound = &ChannelError{Message: "channel not found"}

// ChannelError represents a channel-related error
type ChannelError struct {
	Message string
}

func (e *ChannelError) Error() string {
	return e.Message
}
