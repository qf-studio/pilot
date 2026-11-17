package executor

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
)

// GH-6431 (TASK-651): the firewall probe of the preflight. The connections
// run for real through mockSudoAgent (as the test user, who has no firewall);
// a reject rule is a line of the mock or a replaced runAsAgentDial.

// loopbackPort returns a port on 127.0.0.1: a free one (open false) or the
// port of a listener that stays open for the test (open true).
func loopbackPort(t *testing.T, open bool) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if open {
		t.Cleanup(func() { _ = l.Close() })
	} else if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// runFirewallPreflight runs the preflight on a stubbed host with the real
// firewall check, mock sudo with extra and gatewayPort in the config. The
// metadata address is the loopback port metadataPort, a closed one when 0.
func runFirewallPreflight(t *testing.T, extra string, gatewayPort, metadataPort int) (log *bytes.Buffer, sudoLog string, err error) {
	t.Helper()
	setRunAsStateForTest(t, nil, "")
	sudoLog = mockSudoAgent(t, "", extra)
	stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
	runAsFirewallCheck = checkRunAsFirewall
	if metadataPort == 0 {
		metadataPort = loopbackPort(t, false)
	}
	prev := runAsMetadataAddr
	runAsMetadataAddr = fmt.Sprintf("127.0.0.1:%d", metadataPort)
	t.Cleanup(func() { runAsMetadataAddr = prev })
	log = captureLog(t)
	PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents",
		RunAsGatewayPort: gatewayPort}, "", true, 0)
	return log, sudoLog, RunAsUserPreflightError()
}

// rejectPort is mock sudo code that fails the probe of port on 127.0.0.1.
func rejectPort(port int) string {
	return fmt.Sprintf(`case "$*" in *"/dev/tcp/127.0.0.1/%d"*) exit 1 ;; esac`, port)
}

func requireFirewallRefusal(t *testing.T, log *bytes.Buffer, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("preflight error = %v, want a refusal naming %q", err, want)
	}
	if n := strings.Count(log.String(), "level=ERROR"); n != 1 || !strings.Contains(log.String(), want) {
		t.Errorf("want one Error line naming %q, got:\n%s", want, log.String())
	}
}

// mutation pin: without the control connection a probe tool that does not
// work reads as a blocked metadata address and gateway port, the preflight
// passes and this test fails.
func TestRunAsFirewallBrokenProbeToolRefuses(t *testing.T) {
	log, _, err := runFirewallPreflight(t, `[ "$1" = bash ] && exit 1`, loopbackPort(t, false), 0)
	requireFirewallRefusal(t, log, err, "the probe tool (bash /dev/tcp) does not work")
}

func TestRunAsFirewallGatewayPortReachedRefuses(t *testing.T) {
	port := loopbackPort(t, false)
	log, _, err := runFirewallPreflight(t, "", port, 0)
	requireFirewallRefusal(t, log, err, fmt.Sprintf("reached the gateway port %d", port))
}

func TestRunAsFirewallMetadataReachedRefuses(t *testing.T) {
	gw, md := loopbackPort(t, false), loopbackPort(t, true)
	log, _, err := runFirewallPreflight(t, rejectPort(gw), gw, md)
	requireFirewallRefusal(t, log, err, fmt.Sprintf("reached the metadata address 127.0.0.1:%d", md))
}

func TestRunAsFirewallGatewayPortTakenRefuses(t *testing.T) {
	port := loopbackPort(t, true)
	log, _, err := runFirewallPreflight(t, rejectPort(port), port, 0)
	requireFirewallRefusal(t, log, err, fmt.Sprintf("cannot listen on the gateway port %d", port))
}

// A reject rule for the gateway port as a replaced dial function; the control
// and metadata connections still run as the agent user through mock sudo.
func TestRunAsFirewallGatewayPortBlockedPasses(t *testing.T) {
	port := loopbackPort(t, false)
	gwAddr := fmt.Sprintf("127.0.0.1:%d", port)
	var mu sync.Mutex
	var dialed []string
	prev := runAsAgentDial
	runAsAgentDial = func(s *runAsSettings, addr string) bool {
		mu.Lock()
		dialed = append(dialed, addr)
		mu.Unlock()
		if addr == gwAddr {
			if l, err := net.Listen("tcp", gwAddr); err == nil {
				_ = l.Close()
				t.Errorf("no listener of the daemon on the gateway port %d during the probe", port)
			}
			return false
		}
		return agentDialAs(s, addr)
	}
	t.Cleanup(func() { runAsAgentDial = prev })

	log, sudoLog, err := runFirewallPreflight(t, "", port, 0)
	if err != nil || strings.Contains(log.String(), "level=ERROR") || strings.Contains(log.String(), "level=WARN") {
		t.Fatalf("preflight error %v, want a pass without Error or Warn line:\n%s", err, log.String())
	}
	sort.Strings(dialed)
	if len(dialed) != 3 || !strings.Contains(strings.Join(dialed, " "), gwAddr) {
		t.Errorf("dialed %q, want the control, metadata and gateway connections", dialed)
	}
	var probes int
	for _, c := range sudoCalls(t, sudoLog) {
		if strings.Contains(c, "-u pilot-agent") && strings.Contains(c, "-- bash -c : </dev/tcp/127.0.0.1/") {
			probes++
		}
	}
	if probes != 2 {
		t.Errorf("want the control and metadata connections as the agent user through sudo, got %q", sudoCalls(t, sudoLog))
	}
	l, lerr := net.Listen("tcp", gwAddr)
	if lerr != nil {
		t.Fatalf("gateway port %d still held after the probe: %v", port, lerr)
	}
	_ = l.Close()
}

func TestRunAsFirewallGatewayPortZeroWarns(t *testing.T) {
	log, sudoLog, err := runFirewallPreflight(t, "", 0, 0)
	if err != nil {
		t.Fatalf("preflight error %v\n%s", err, log.String())
	}
	if n := strings.Count(log.String(), "level=WARN"); n != 1 || !strings.Contains(log.String(), "the gateway port is not probed") {
		t.Errorf("want one Warn line for the gateway port, got:\n%s", log.String())
	}
	var probes int
	for _, c := range sudoCalls(t, sudoLog) {
		if strings.Contains(c, "/dev/tcp/") {
			probes++
		}
	}
	if probes != 2 {
		t.Errorf("want the control and metadata connections only, got %q", sudoCalls(t, sudoLog))
	}
}

// The probe is the last check of the preflight, gets the gateway port of the
// config, and does not run after a failed check.
func TestRunAsFirewallIsTheLastCheck(t *testing.T) {
	for _, dataFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("data_dir_fails=%v", dataFails), func(t *testing.T) {
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
			port := 0
			runAsFirewallCheck = func(s *runAsSettings) string {
				order = append(order, "firewall")
				port = s.gatewayPort
				return "firewall: stub"
			}
			PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents",
				RunAsGatewayPort: 9091}, "", true, 0)
			err := RunAsUserPreflightError()
			want := "workspace,data,firewall"
			if dataFails {
				want = "workspace,data"
			}
			if got := strings.Join(order, ","); got != want {
				t.Fatalf("checks ran as %s, want %s", got, want)
			}
			if !dataFails && (port != 9091 || err == nil || !strings.Contains(err.Error(), "firewall: stub")) {
				t.Errorf("gateway port %d, error %v: want port 9091 and the refusal of the probe", port, err)
			}
		})
	}
}

func TestRunAsFirewallNotRunWithOptionEmpty(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	sudoLog := mockSudoAgent(t, "", "")
	prevCheck, prevDial := runAsFirewallCheck, runAsAgentDial
	ran := false
	runAsFirewallCheck = func(*runAsSettings) string { ran = true; return "unexpected" }
	runAsAgentDial = func(*runAsSettings, string) bool { ran = true; return false }
	t.Cleanup(func() { runAsFirewallCheck, runAsAgentDial = prevCheck, prevDial })
	PreflightRunAsUser(&ClaudeCodeConfig{RunAsGatewayPort: 9091}, "", true, 0)
	if ran {
		t.Error("the firewall probe ran with run_as_user empty")
	}
	if err := RunAsUserPreflightError(); err != nil {
		t.Errorf("preflight with the option empty: %v", err)
	}
	if _, err := os.Stat(sudoLog); err == nil {
		t.Errorf("sudo ran with the option empty: %q", sudoCalls(t, sudoLog))
	}
}
