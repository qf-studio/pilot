package executor

// MetricsRecorder accepts per-execution telemetry from the runner.
// The interface is satisfied by *autopilot.Metrics without importing the autopilot package.
type MetricsRecorder interface {
	RecordTokens(model, direction string, n int64)
	RecordCost(model string, costUSD float64)
	RecordExecution(model, result string)
}
