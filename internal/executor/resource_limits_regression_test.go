package executor

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
)

// TestNodeOptionsEnv is a table-driven test for the cooperative V8 heap
// bound injected via NODE_OPTIONS. GH-4401.
func TestNodeOptionsEnv(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		cfg      *SubprocessLimitsConfig
		want     string
	}{
		{name: "nil config", existing: "", cfg: nil, want: ""},
		{name: "disabled", existing: "", cfg: &SubprocessLimitsConfig{Enabled: false, MaxRSSMB: 4096}, want: ""},
		{name: "enabled zero cap", existing: "", cfg: &SubprocessLimitsConfig{Enabled: true, MaxRSSMB: 0}, want: ""},
		{name: "enabled negative cap", existing: "", cfg: &SubprocessLimitsConfig{Enabled: true, MaxRSSMB: -5}, want: ""},
		{
			name:     "enabled, no existing NODE_OPTIONS",
			existing: "",
			cfg:      &SubprocessLimitsConfig{Enabled: true, MaxRSSMB: 4096},
			want:     "--max-old-space-size=4096",
		},
		{
			name:     "enabled, preserves existing NODE_OPTIONS",
			existing: "--experimental-fetch",
			cfg:      &SubprocessLimitsConfig{Enabled: true, MaxRSSMB: 512},
			want:     "--experimental-fetch --max-old-space-size=512",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nodeOptionsEnv(tt.existing, tt.cfg)
			if got != tt.want {
				t.Errorf("nodeOptionsEnv(%q, %+v) = %q, want %q", tt.existing, tt.cfg, got, tt.want)
			}
		})
	}
}

// TestApplyResourceLimits_NodeFetchSucceeds is the GH-4401 regression test:
// it spawns a real Node child, applies the full subprocess_limits cap
// pipeline (cgroup v2 leaf on Linux + NODE_OPTIONS heap bound everywhere —
// exactly as backend_claudecode.go wires it), and asserts an HTTPS fetch
// from inside that child succeeds.
//
// This is the test that would have caught GH-4401 before the S6-lite
// cutover: with the old RLIMIT_AS implementation, every case in this table
// would have failed with a ~25ms "fetch failed" despite a successful TLS
// handshake, because RLIMIT_AS caps virtual address space and Node/V8
// reserves far more VA than it ever touches (pointer cage, code ranges,
// io_uring/undici buffers). The cgroup v2 memory.max + NODE_OPTIONS
// replacement caps actual RSS / heap size instead, which does not interact
// with mmap-based virtual memory reservations at all.
func TestApplyResourceLimits_NodeFetchSucceeds(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found on PATH, skipping Node subprocess regression test")
	}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	tests := []struct {
		name string
		cfg  *SubprocessLimitsConfig
	}{
		{name: "cap disabled", cfg: &SubprocessLimitsConfig{Enabled: false, MaxRSSMB: 4096}},
		{name: "cap enabled, default 4096 MiB", cfg: &SubprocessLimitsConfig{Enabled: true, MaxRSSMB: 4096}},
		{name: "cap enabled, tight 512 MiB", cfg: &SubprocessLimitsConfig{Enabled: true, MaxRSSMB: 512}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := fmt.Sprintf(
				`fetch(%q).then(r => { if (!r.ok) { console.error('bad status', r.status); process.exit(1); } process.exit(0); }).catch(e => { console.error('fetch error:', String(e)); process.exit(1); });`,
				srv.URL,
			)
			cmd := exec.Command(nodePath, "-e", script)

			env := append(os.Environ(),
				"NODE_TLS_REJECT_UNAUTHORIZED=0", // httptest.NewTLSServer uses a self-signed cert
			)
			if nodeOpts := nodeOptionsEnv(os.Getenv("NODE_OPTIONS"), tt.cfg); nodeOpts != "" {
				env = append(env, "NODE_OPTIONS="+nodeOpts)
			}
			cmd.Env = env

			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output

			if err := cmd.Start(); err != nil {
				t.Fatalf("failed to start node: %v", err)
			}

			cleanup := applyResourceLimits(cmd.Process.Pid, tt.cfg)

			runErr := cmd.Wait()
			cleanup()

			if runErr != nil {
				t.Fatalf("node fetch failed: %v (output: %s)", runErr, output.String())
			}
		})
	}
}
