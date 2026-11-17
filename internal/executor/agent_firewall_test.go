package executor

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
)

// GH-6431 (TASK-651): the firewall probe of the preflight. The connections
// run for real through mockSudoAgent (as the test user); extra shell code in
// the mock stands for the firewall rules of the agent user.

// freeLoopbackPort returns a port on 127.0.0.1 that nothing listens on.
func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// openLoopbackListener returns the port of a listener on 127.0.0.1 that stays
// open for the test.
func openLoopbackListener(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

// blockConnect is mock sudo code that fails a probe connection to port on
// 127.0.0.1, as the reject rule of the firewall does.
func blockConnect(port int) string {
	return fmt.Sprintf(`case "$*" in *"/dev/tcp/127.0.0.1/%d"*) exit 1 ;; esac`, port)
}

// stubMetadataAddr points the metadata address of the probe at port on
// 127.0.0.1: the real one may answer on a cloud host.
func stubMetadataAddr(t *testing.T, port int) {
	t.Helper()
	ph, pp := runAsMetadataHost, runAsMetadataPort
	runAsMetadataHost, runAsMetadataPort = "127.0.0.1", port
	t.Cleanup(func() { runAsMetadataHost, runAsMetadataPort = ph, pp })
}

// firewallPreflight runs the preflight with a stubbed host, the real firewall
// check, the mock sudo with extra and gatewayPort in the config. The metadata
// address is a closed loopback port unless the test pointed it elsewhere
// first (metadataPort > 0).
func firewallPreflight(t *testing.T, extra string, gatewayPort, metadataPort int) (*bytes.Buffer, string, error) {
	t.Helper()
	setRunAsStateForTest(t, nil, "")
	logPath := mockSudoAgent(t, "", extra)
	stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
	runAsFirewallCheck = checkRunAsFirewall
	if metadataPort == 0 {
		metadataPort = freeLoopbackPort(t)
	}
	stubMetadataAddr(t, metadataPort)
	buf := captureLog(t)
	PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents",
		RunAsGatewayPort: gatewayPort}, "", true, 0)
	return buf, logPath, RunAsUserPreflightError()
}

func wantFirewallRefusal(t *testing.T, buf *bytes.Buffer, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("preflight error = %v, want one naming %q", err, want)
	}
	if n := strings.Count(buf.String(), "level=ERROR"); n != 1 || !strings.Contains(buf.String(), want) {
		t.Errorf("want one Error line naming %q, got:\n%s", want, buf.String())
	}
}

// mutation pin: checkRunAsFirewall drops the control connection, so a probe
// tool that does not work reads as a blocked metadata address and gateway
// port -> this test fails.
func TestRunAsFirewallBrokenProbeTool(t *testing.T) {
	buf, _, err := firewallPreflight(t, `[ "$1" = bash ] && exit 1`, freeLoopbackPort(t), 0)
	wantFirewallRefusal(t, buf, err, "the probe tool (bash /dev/tcp) does not work")
}

func TestRunAsFirewallGatewayPortReached(t *testing.T) {
	port := freeLoopbackPort(t)
	buf, _, err := firewallPreflight(t, "", port, 0)
	wantFirewallRefusal(t, buf, err, fmt.Sprintf("reached the gateway port %d", port))
}

func TestRunAsFirewallMetadataReached(t *testing.T) {
	gw := freeLoopbackPort(t)
	md := openLoopbackListener(t)
	buf, _, err := firewallPreflight(t, blockConnect(gw), gw, md)
	wantFirewallRefusal(t, buf, err, fmt.Sprintf("reached the metadata address 127.0.0.1 port %d", md))
}

func TestRunAsFirewallGatewayPortInUse(t *testing.T) {
	port := openLoopbackListener(t)
	buf, _, err := firewallPreflight(t, blockConnect(port), port, 0)
	wantFirewallRefusal(t, buf, err, fmt.Sprintf("cannot listen on the gateway port %d", port))
}

func TestRunAsFirewallGatewayPortBlockedPasses(t *testing.T) {
	port := freeLoopbackPort(t)
	buf, logPath, err := firewallPreflight(t, blockConnect(port), port, 0)
	if err != nil || strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("preflight failed: %v\n%s", err, buf.String())
	}
	var probes []string
	for _, c := range sudoCalls(t, logPath) {
		if strings.Contains(c, "-- bash -c : </dev/tcp/") {
			probes = append(probes, c)
		}
	}
	if len(probes) != 3 || !strings.Contains(probes[2], fmt.Sprintf("/dev/tcp/127.0.0.1/%d", port)) {
		t.Errorf("want the control, metadata and gateway connections as the agent user, got %q", probes)
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 0 {
		t.Errorf("want no Warn line with a gateway port, got:\n%s", buf.String())
	}
	// The listener of the probe is closed again.
	l, lerr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if lerr != nil {
		t.Fatalf("gateway port %d still held after the probe: %v", port, lerr)
	}
	_ = l.Close()
}

func TestRunAsFirewallGatewayPortZeroWarns(t *testing.T) {
	buf, logPath, err := firewallPreflight(t, "", 0, 0)
	if err != nil {
		t.Fatalf("preflight failed: %v\n%s", err, buf.String())
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 || !strings.Contains(buf.String(), "gateway port is not probed") {
		t.Errorf("want one Warn line for the skipped gateway port, got:\n%s", buf.String())
	}
	var probes int
	for _, c := range sudoCalls(t, logPath) {
		if strings.Contains(c, "/dev/tcp/") {
			probes++
		}
	}
	if probes != 2 {
		t.Errorf("want the control and metadata connections only, got %d: %q", probes, sudoCalls(t, logPath))
	}
}

// TestRunAsFirewallRunsLast: the firewall probe runs after the workspace and
// data directory checks, and a failure of one of them runs no probe.
func TestRunAsFirewallRunsLast(t *testing.T) {
	for _, dataFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("data dir fails %v", dataFails), func(t *testing.T) {
			setRunAsStateForTest(t, nil, "")
			_ = mockSudo(t, 0)
			stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
			var order []string
			runAsWorkspaceCheck = func(*runAsSettings) string { order = append(order, "workspace"); return "" }
			runAsDataDirCheck = func(*runAsSettings) string {
				order = append(order, "data")
				if dataFails {
					return "data dir stub"
				}
				return ""
			}
			var gotPort int
			runAsFirewallCheck = func(s *runAsSettings) string {
				order = append(order, "firewall")
				gotPort = s.gatewayPort
				return "firewall: stub"
			}
			PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents",
				RunAsGatewayPort: 9091}, "", true, 0)
			err := RunAsUserPreflightError()
			if dataFails {
				if strings.Join(order, ",") != "workspace,data" {
					t.Errorf("checks ran as %v, want no firewall probe after a failed data directory", order)
				}
				return
			}
			if strings.Join(order, ",") != "workspace,data,firewall" || gotPort != 9091 {
				t.Errorf("checks ran as %v with gateway port %d, want the firewall last with port 9091", order, gotPort)
			}
			if err == nil || !strings.Contains(err.Error(), "firewall: stub") {
				t.Errorf("preflight error = %v, want the refusal of the firewall probe", err)
			}
		})
	}
}

func TestRunAsFirewallNotRunWithOptionEmpty(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	logPath := mockSudoAgent(t, "", "")
	prev := runAsFirewallCheck
	called := false
	runAsFirewallCheck = func(*runAsSettings) string { called = true; return "unexpected" }
	t.Cleanup(func() { runAsFirewallCheck = prev })
	PreflightRunAsUser(&ClaudeCodeConfig{RunAsGatewayPort: 9091}, "", true, 0)
	if called {
		t.Error("the firewall probe ran with run_as_user empty")
	}
	if err := RunAsUserPreflightError(); err != nil {
		t.Errorf("preflight with the option empty: %v", err)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Errorf("sudo ran with the option empty: %q", sudoCalls(t, logPath))
	}
}
