package main

import (
	"context"
	"log/slog"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/leakscan"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/replay"
)

// daemonLogPath returns the file the daemon log lands in, or "" when it goes
// to the terminal only (nothing on disk to scan). In dashboard mode logs go to
// logging.dashboard_log (default ~/.pilot/logs/daemon.log); otherwise only a
// file path in logging.output is a file.
func daemonLogPath(cfg *config.Config, dashboardMode bool) string {
	var lc *logging.Config
	if cfg != nil {
		lc = cfg.Logging
	}
	if dashboardMode {
		if lc == nil || lc.DashboardLog == "" {
			return logging.DefaultDaemonLogPath()
		}
		if lc.DashboardLog == "off" {
			return ""
		}
		return lc.DashboardLog
	}
	if lc == nil {
		return ""
	}
	switch lc.Output {
	case "", "stdout", "stderr":
		return ""
	}
	return lc.Output
}

// warnLogNotScanned logs the one line that says why daemonLogPath came back
// empty and the daemon log is left out of the scan.
func warnLogNotScanned(log *slog.Logger, dashboardMode bool) {
	reason := "logging.output is the terminal (stdout or stderr), so the daemon log is not written to disk; set it to a file path to scan it"
	if dashboardMode {
		reason = "logging.dashboard_log is off, so the daemon log is not written to disk"
	}
	log.Warn("leak scan: daemon log is not scanned, only recordings are", slog.String("reason", reason))
}

// startLeakScan runs the runtime leak scanner in the background (GH-6250). It
// is the net under value redaction: it reads the raw log and recordings on
// disk, so it still catches a writer that bypassed redaction. A nil alerts
// engine still counts hits and logs them (masked).
func startLeakScan(ctx context.Context, cfg *config.Config, dashboardMode bool, alertsEngine *alerts.Engine) {
	if !cfg.LeakScanEnabled() {
		return
	}
	lcfg := leakscan.Config{
		LogPath:        daemonLogPath(cfg, dashboardMode),
		RecordingsPath: replay.DefaultRecordingsPath(),
		Interval:       cfg.LeakScanInterval(),
		Logger:         logging.WithComponent("leakscan"),
	}
	if alertsEngine != nil {
		lcfg.Sink = alertsEngine // keep a nil engine out of the interface
	}
	if lcfg.LogPath == "" {
		warnLogNotScanned(logging.WithComponent("start"), dashboardMode)
	}
	scanner := leakscan.NewLeakScanner(lcfg)
	logging.SafeGo("leakscan", func() { scanner.Run(ctx) })
	logging.WithComponent("start").Info("leak scan started",
		slog.Duration("interval", lcfg.Interval),
		slog.Bool("log", lcfg.LogPath != ""))
}
