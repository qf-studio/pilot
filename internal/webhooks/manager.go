package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Manager handles webhook delivery to configured endpoints.
type Manager struct {
	config     *Config
	httpClient *http.Client
	logger     *slog.Logger
	mu         sync.RWMutex

	// Metrics
	deliveries     int64
	failures       int64
	retries        int64
	lastDeliveryAt time.Time
}

// DeliveryResult represents the result of a webhook delivery attempt.
type DeliveryResult struct {
	EndpointID string
	Success    bool
	StatusCode int
	Attempts   int
	Error      error
	Duration   time.Duration
}

// NewManager creates a new webhook manager with the given configuration.
func NewManager(config *Config, logger *slog.Logger) *Manager {
	if config == nil {
		config = DefaultConfig()
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Manager{
		config:     config,
		httpClient: &http.Client{},
		logger:     logger.With("component", "webhooks"),
	}
}

// Dispatch sends an event to all subscribed endpoints.
// It returns the results for each endpoint delivery.
func (m *Manager) Dispatch(ctx context.Context, event *Event) []DeliveryResult {
	if !m.config.Enabled {
		return nil
	}

	m.mu.RLock()
	endpoints := m.config.Endpoints
	m.mu.RUnlock()

	var results []DeliveryResult
	var wg sync.WaitGroup

	for _, endpoint := range endpoints {
		if !endpoint.Enabled || !endpoint.SubscribesTo(event.Type) {
			continue
		}

		wg.Add(1)
		go func(ep *EndpointConfig) {
			defer wg.Done()
			result := m.deliver(ctx, ep, event)
			m.mu.Lock()
			results = append(results, result)
			m.mu.Unlock()
		}(endpoint)
	}

	wg.Wait()
	return results
}

// deliver sends an event to a single endpoint with retry logic.
func (m *Manager) deliver(ctx context.Context, endpoint *EndpointConfig, event *Event) DeliveryResult {
	startTime := time.Now()
	retryConfig := endpoint.GetRetry(m.config.Defaults)
	timeout := endpoint.GetTimeout(m.config.Defaults)

	result := DeliveryResult{
		EndpointID: endpoint.ID,
	}

	// Marshal event to JSON
	payload, err := json.Marshal(event)
	if err != nil {
		result.Error = fmt.Errorf("failed to marshal event: %w", err)
		result.Duration = time.Since(startTime)
		return result
	}

	// Calculate signature
	signature := m.sign(payload, endpoint.Secret)

	// Retry loop
	delay := retryConfig.InitialDelay
	for attempt := 1; attempt <= retryConfig.MaxAttempts; attempt++ {
		result.Attempts = attempt

		// Create request
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint.URL, bytes.NewReader(payload))
		if err != nil {
			cancel()
			result.Error = fmt.Errorf("failed to create request: %w", err)
			continue
		}

		// Set headers
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Pilot-Event", string(event.Type))
		req.Header.Set("X-Pilot-Signature", signature)
		req.Header.Set("X-Pilot-Delivery", event.ID)
		req.Header.Set("X-Pilot-Timestamp", event.Timestamp.Format(time.RFC3339))
		req.Header.Set("User-Agent", "Pilot-Webhooks/1.0")

		// Add custom headers
		for k, v := range endpoint.Headers {
			req.Header.Set(k, v)
		}

		// Execute request
		resp, err := m.httpClient.Do(req)
		cancel()

		if err != nil {
			result.Error = err
			m.logger.Warn("webhook delivery failed",
				"endpoint", endpoint.Name,
				"attempt", attempt,
				"error", err,
			)
		} else {
			// Read and discard body
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			result.StatusCode = resp.StatusCode

			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				result.Success = true
				result.Duration = time.Since(startTime)
				m.recordSuccess()
				m.logger.Debug("webhook delivered",
					"endpoint", endpoint.Name,
					"event", event.Type,
					"status", resp.StatusCode,
				)
				return result
			}

			result.Error = fmt.Errorf("unexpected status code: %d", resp.StatusCode)
			m.logger.Warn("webhook delivery failed",
				"endpoint", endpoint.Name,
				"attempt", attempt,
				"status", resp.StatusCode,
			)
		}

		// Don't retry on last attempt
		if attempt >= retryConfig.MaxAttempts {
			break
		}

		// Wait before retry with exponential backoff
		m.recordRetry()
		select {
		case <-ctx.Done():
			result.Error = ctx.Err()
			result.Duration = time.Since(startTime)
			return result
		case <-time.After(delay):
		}

		// Calculate next delay with exponential backoff
		delay = time.Duration(float64(delay) * retryConfig.Multiplier)
		if delay > retryConfig.MaxDelay {
			delay = retryConfig.MaxDelay
		}
	}

	result.Duration = time.Since(startTime)
	m.recordFailure()
	m.logger.Error("webhook delivery exhausted retries",
		"endpoint", endpoint.Name,
		"event", event.Type,
		"attempts", result.Attempts,
		"error", result.Error,
	)

	return result
}

// sign generates an HMAC-SHA256 signature for the payload.
func (m *Manager) sign(payload []byte, secret string) string {
	if secret == "" {
		return ""
	}
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	return "sha256=" + hex.EncodeToString(h.Sum(nil))
}

// VerifySignature verifies an HMAC-SHA256 signature against a payload.
// This is useful for handlers that receive webhooks.
func VerifySignature(payload []byte, signature, secret string) bool {
	if secret == "" || signature == "" {
		return false
	}
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	expected := "sha256=" + hex.EncodeToString(h.Sum(nil))
	return hmac.Equal([]byte(signature), []byte(expected))
}

// AddEndpoint adds a new endpoint to the manager.
func (m *Manager) AddEndpoint(endpoint *EndpointConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Generate ID if not provided
	if endpoint.ID == "" {
		endpoint.ID = "ep_" + randomString(8)
	}

	m.config.Endpoints = append(m.config.Endpoints, endpoint)
}

// RemoveEndpoint removes an endpoint by ID.
func (m *Manager) RemoveEndpoint(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, ep := range m.config.Endpoints {
		if ep.ID == id {
			m.config.Endpoints = append(m.config.Endpoints[:i], m.config.Endpoints[i+1:]...)
			return true
		}
	}
	return false
}

// GetEndpoint returns an endpoint by ID.
func (m *Manager) GetEndpoint(id string) *EndpointConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, ep := range m.config.Endpoints {
		if ep.ID == id {
			return ep
		}
	}
	return nil
}

// ListEndpoints returns all configured endpoints.
func (m *Manager) ListEndpoints() []*EndpointConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*EndpointConfig, len(m.config.Endpoints))
	copy(result, m.config.Endpoints)
	return result
}

// Stats returns current webhook delivery statistics.
func (m *Manager) Stats() (deliveries, failures, retries int64, lastDelivery time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.deliveries, m.failures, m.retries, m.lastDeliveryAt
}

func (m *Manager) recordSuccess() {
	m.mu.Lock()
	m.deliveries++
	m.lastDeliveryAt = time.Now()
	m.mu.Unlock()
}

func (m *Manager) recordFailure() {
	m.mu.Lock()
	m.failures++
	m.mu.Unlock()
}

func (m *Manager) recordRetry() {
	m.mu.Lock()
	m.retries++
	m.mu.Unlock()
}

// UpdateConfig updates the webhook configuration.
func (m *Manager) UpdateConfig(config *Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = config
}

// IsEnabled returns whether webhooks are enabled.
func (m *Manager) IsEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.Enabled
}
