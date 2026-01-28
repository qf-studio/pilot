package alerts

import (
	"github.com/alekspetrov/pilot/internal/executor"
)

// EngineAdapter wraps an Engine to implement executor.AlertEventProcessor.
// This allows the alerts.Engine to receive events from the executor without
// creating an import cycle.
type EngineAdapter struct {
	engine *Engine
}

// NewEngineAdapter creates an adapter that wraps the alerts engine.
func NewEngineAdapter(engine *Engine) *EngineAdapter {
	return &EngineAdapter{engine: engine}
}

// ProcessEvent converts an executor.AlertEvent to alerts.Event and processes it.
func (a *EngineAdapter) ProcessEvent(event executor.AlertEvent) {
	// Convert executor event type to alerts event type
	alertEvent := Event{
		Type:      EventType(event.Type),
		TaskID:    event.TaskID,
		TaskTitle: event.TaskTitle,
		Project:   event.Project,
		Phase:     event.Phase,
		Progress:  event.Progress,
		Error:     event.Error,
		Metadata:  event.Metadata,
		Timestamp: event.Timestamp,
	}
	a.engine.ProcessEvent(alertEvent)
}
