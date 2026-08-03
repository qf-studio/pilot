//go:build !linux

package executor

// probeProcessLiveness is a no-op on non-Linux platforms: it always reports
// zero descendants and zero CPU ticks with a nil error. GH-4668's liveness
// grace depends on /proc, which only exists on Linux — the platform the box
// runs on (t3.xlarge, shared with the daemon) and where the incident this
// fixes actually happened. Reporting "no activity" rather than an error
// means non-Linux dev environments (e.g. darwin) get exactly today's
// kill-on-silence heartbeat behavior: no grace, no spurious probe-error
// warnings, identical externally observable behavior to the pre-GH-4668
// code path.
func probeProcessLiveness(_ int) (processLivenessSnapshot, error) {
	return processLivenessSnapshot{}, nil
}
