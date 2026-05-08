package executor

// MetricsRecorder collects per-execution token, cost, and result metrics.
// The interface lives in the executor package to avoid an import cycle with autopilot.
type MetricsRecorder interface {
	// RecordTokens records input/output/cache token counts for one execution.
	RecordTokens(model string, tokensIn, tokensOut, cacheCreation, cacheRead int64)

	// RecordCost records the estimated USD cost for one execution.
	RecordCost(model string, costUSD float64)

	// RecordExecution records the outcome of one execution.
	// result is one of "success", "failed", or "timed_out".
	RecordExecution(model string, result string)
}
