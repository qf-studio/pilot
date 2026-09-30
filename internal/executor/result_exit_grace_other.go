//go:build !linux

package executor

// survivingChildCmdlines is a no-op off Linux (no /proc); the GH-5530 WARN
// line is still emitted, just without the child command lines.
func survivingChildCmdlines(_ int) []string { return nil }
