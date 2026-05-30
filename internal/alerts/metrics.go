package alerts

import "sync"

// alertFiredKey identifies a fired-alert counter by rule name and severity.
type alertFiredKey struct {
	Rule     string
	Severity string
}

// alertDeliveryKey identifies a delivery attempt by channel name, type, and result.
type alertDeliveryKey struct {
	Channel string
	Type    string
	Result  string
}

// AlertMetrics collects counters for the alerting subsystem.
// All methods are goroutine-safe.
type AlertMetrics struct {
	mu            sync.RWMutex
	firedTotal    map[alertFiredKey]int64
	deliveryTotal map[alertDeliveryKey]int64
	droppedTotal  int64
}

// NewAlertMetrics creates an empty AlertMetrics instance.
func NewAlertMetrics() *AlertMetrics {
	return &AlertMetrics{
		firedTotal:    make(map[alertFiredKey]int64),
		deliveryTotal: make(map[alertDeliveryKey]int64),
	}
}

// RecordFired increments the fired counter for a rule+severity pair.
func (m *AlertMetrics) RecordFired(rule, severity string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.firedTotal[alertFiredKey{Rule: rule, Severity: severity}]++
}

// RecordDelivery increments the delivery counter for a channel+type+result triple.
// result is "success" or "failure".
func (m *AlertMetrics) RecordDelivery(channel, channelType, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deliveryTotal[alertDeliveryKey{Channel: channel, Type: channelType, Result: result}]++
}

// RecordDropped increments the dropped-event counter.
func (m *AlertMetrics) RecordDropped() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.droppedTotal++
}

// Snapshot returns a point-in-time copy of all counters.
func (m *AlertMetrics) Snapshot() AlertMetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	fired := make(map[alertFiredKey]int64, len(m.firedTotal))
	for k, v := range m.firedTotal {
		fired[k] = v
	}
	delivery := make(map[alertDeliveryKey]int64, len(m.deliveryTotal))
	for k, v := range m.deliveryTotal {
		delivery[k] = v
	}
	return AlertMetricsSnapshot{
		FiredTotal:    fired,
		DeliveryTotal: delivery,
		DroppedTotal:  m.droppedTotal,
	}
}

// AlertMetricsSnapshot is a read-only copy of alert metrics at a point in time.
// QueueDepth is populated by Engine.AlertSnapshot, not by Snapshot() itself.
type AlertMetricsSnapshot struct {
	FiredTotal    map[alertFiredKey]int64
	DeliveryTotal map[alertDeliveryKey]int64
	DroppedTotal  int64
	QueueDepth    int
}
