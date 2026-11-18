package executor

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
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
// firewall check, mock sudo with extra and gatewayPort on 127.0.0.1 in the
// config. The metadata address is the loopback port metadataPort, a closed
// one when 0. The log has the Debug lines.
func runFirewallPreflight(t *testing.T, extra string, gatewayPort, metadataPort int) (log *bytes.Buffer, sudoLog string, err error) {
	t.Helper()
	return runFirewallPreflightHost(t, extra, gatewayPort, "127.0.0.1", metadataPort)
}

// firewallConfig is the config of runFirewallPreflightHost.
func firewallConfig(gatewayPort int, gatewayHost string) *ClaudeCodeConfig {
	return &ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents",
		RunAsGatewayPort: gatewayPort, RunAsGatewayHost: gatewayHost}
}

// runFirewallPreflightHost is runFirewallPreflight with gatewayHost as the
// gateway host of the config.
func runFirewallPreflightHost(t *testing.T, extra string, gatewayPort int, gatewayHost string, metadataPort int) (log *bytes.Buffer, sudoLog string, err error) {
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
	log = &bytes.Buffer{}
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prevLog) })
	PreflightRunAsUser(firewallConfig(gatewayPort, gatewayHost), "", true, 0)
	return log, sudoLog, RunAsUserPreflightError()
}

// firewallProbes counts the connections of the probe that ran through mock
// sudo.
func firewallProbes(t *testing.T, sudoLog string) int {
	t.Helper()
	var n int
	for _, c := range sudoCalls(t, sudoLog) {
		if strings.Contains(c, "/dev/tcp/") {
			n++
		}
	}
	return n
}

// rejectPort is mock sudo code that fails the probe of port on 127.0.0.1.
func rejectPort(port int) string {
	return rejectAddr("127.0.0.1", port)
}

// rejectAddr is mock sudo code that fails the probe of port on host as a
// reject rule does: the probe runs and prints its marker with status 1.
func rejectAddr(host string, port int) string {
	return fmt.Sprintf(`case "$*" in *"/dev/tcp/%s/%d;"*) echo %s1; exit 0 ;; esac`, host, port, runAsProbeMarker)
}

// probedOn reports whether a probe of port on host ran through mock sudo.
func probedOn(t *testing.T, sudoLog, host string, port int) bool {
	t.Helper()
	for _, c := range sudoCalls(t, sudoLog) {
		if strings.Contains(c, fmt.Sprintf("/dev/tcp/%s/%d;", host, port)) {
			return true
		}
	}
	return false
}

// fakeInterfaceAddrs makes the interface list of the host the given CIDRs.
func fakeInterfaceAddrs(t *testing.T, cidrs ...string) {
	t.Helper()
	var addrs []net.Addr
	for _, c := range cidrs {
		ip, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		addrs = append(addrs, n)
	}
	prev := runAsInterfaceAddrs
	runAsInterfaceAddrs = func() ([]net.Addr, error) { return addrs, nil }
	t.Cleanup(func() { runAsInterfaceAddrs = prev })
}

// realLocalIPv4 returns a non-loopback IPv4 address of the host that the
// test can listen on, "" when there is none.
func realLocalIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			l, err := net.Listen("tcp", net.JoinHostPort(n.IP.String(), "0"))
			if err != nil {
				continue
			}
			_ = l.Close()
			return n.IP.String()
		}
	}
	return ""
}

// slowPort is mock sudo code that holds the probe of port on 127.0.0.1 for
// longer than the time limit. The kill of the cancel reaches the mock as
// `-PGID` (the mock drops everything up to the first "--") and is run here.
func slowPort(port int) string {
	return fmt.Sprintf(`case "$1" in -[0-9]*) kill -s KILL -- "$1"; exit $? ;; esac
case "$*" in *"/dev/tcp/127.0.0.1/%d"*) sleep 10; exit 1 ;; esac`, port)
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
	runAsAgentDial = func(s *runAsSettings, addr string) agentDialResult {
		mu.Lock()
		dialed = append(dialed, addr)
		mu.Unlock()
		if addr == gwAddr {
			if l, err := net.Listen("tcp", gwAddr); err == nil {
				_ = l.Close()
				t.Errorf("no listener of the daemon on the gateway port %d during the probe", port)
			}
			return agentDialFailed
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
		if strings.Contains(c, "-u pilot-agent") && strings.Contains(c, "-- bash -c : 2>/dev/null </dev/tcp/127.0.0.1/") {
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

// GH-6455: every entry point but the daemon start passes no gateway port:
// the gateway part is skipped with one Debug line, no Warn.
func TestRunAsFirewallGatewayPortZeroDebug(t *testing.T) {
	log, sudoLog, err := runFirewallPreflight(t, "", 0, 0)
	if err != nil {
		t.Fatalf("preflight error %v\n%s", err, log.String())
	}
	var lines []string
	for _, l := range strings.Split(log.String(), "\n") {
		if strings.Contains(l, "the gateway port is not probed") {
			lines = append(lines, l)
		}
	}
	if strings.Contains(log.String(), "level=WARN") || len(lines) != 1 || !strings.Contains(lines[0], "level=DEBUG") {
		t.Errorf("want one Debug line and no Warn line for the gateway port, got:\n%s", log.String())
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
	runAsAgentDial = func(*runAsSettings, string) agentDialResult { ran = true; return agentDialFailed }
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

// GH-6455: a reject rule of the host script answers with a TCP reset, so a
// gateway connection with no answer by the time limit proves no rule.
// mutation pin: count a timeout at the gateway port as blocked -> the
// preflight passes and this test fails.
func TestRunAsFirewallGatewayTimeoutRefuses(t *testing.T) {
	prev := runAsDialTimeout
	runAsDialTimeout = 300 * time.Millisecond
	t.Cleanup(func() { runAsDialTimeout = prev })
	port := loopbackPort(t, false)
	slow := slowPort(port)
	start := time.Now()
	log, _, err := runFirewallPreflight(t, slow, port, 0)
	requireFirewallRefusal(t, log, err, fmt.Sprintf("no answer from the gateway port %d on 127.0.0.1", port))
	if d := time.Since(start); d > 8*time.Second {
		t.Errorf("preflight took %s: the probe did not stop at the time limit", d)
	}
}

// For the metadata address a timeout stays a block.
func TestRunAsFirewallMetadataTimeoutPasses(t *testing.T) {
	prev := runAsDialTimeout
	runAsDialTimeout = 300 * time.Millisecond
	t.Cleanup(func() { runAsDialTimeout = prev })
	gw, md := loopbackPort(t, false), loopbackPort(t, true)
	log, _, err := runFirewallPreflight(t, rejectPort(gw)+"\n"+slowPort(md), gw, md)
	if err != nil {
		t.Fatalf("preflight error %v, want a pass:\n%s", err, log.String())
	}
}

// GH-6455: a failed result is not kept (pilot start --replace: the port was
// held by the old daemon); a passed one is.
func TestRunAsFirewallFailedResultIsNotKept(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := held.Addr().(*net.TCPAddr).Port
	log, sudoLog, err := runFirewallPreflight(t, rejectPort(port), port, 0)
	requireFirewallRefusal(t, log, err, fmt.Sprintf("cannot listen on the gateway port %d", port))
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	before := firewallProbes(t, sudoLog)

	PreflightRunAsUser(firewallConfig(port, "127.0.0.1"), "", true, 0)
	if err := RunAsUserPreflightError(); err != nil {
		t.Fatalf("second preflight with the same settings: %v, want the probes run again and a pass", err)
	}
	afterPass := firewallProbes(t, sudoLog)
	if afterPass <= before {
		t.Fatalf("the probes did not run again after a failed result (%d connections before, %d after)", before, afterPass)
	}

	PreflightRunAsUser(firewallConfig(port, "127.0.0.1"), "", true, 0)
	if err := RunAsUserPreflightError(); err != nil || firewallProbes(t, sudoLog) != afterPass {
		t.Errorf("third preflight: error %v, %d connections (want %d): a passed result is kept", err, firewallProbes(t, sudoLog), afterPass)
	}
}

// GH-6455, GH-6480: a wildcard gateway host is probed on 127.0.0.1 and each
// non-loopback IPv4 address of the interface list; IPv6 addresses stay out.
func TestRunAsFirewallNonLoopbackGatewayHost(t *testing.T) {
	ip := realLocalIPv4(t)
	if ip == "" {
		t.Skip("the host has no non-loopback IPv4 address")
	}
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked=%v", blocked), func(t *testing.T) {
			fakeInterfaceAddrs(t, "127.0.0.1/8", ip+"/24", "::1/128", "fe80::1/64")
			port := loopbackPort(t, false)
			extra := rejectPort(port)
			if blocked {
				extra += "\n" + rejectAddr(ip, port)
			}
			log, sudoLog, err := runFirewallPreflightHost(t, extra, port, "0.0.0.0", 0)
			if !blocked {
				requireFirewallRefusal(t, log, err, fmt.Sprintf("reached the gateway port %d on %s", port, ip))
				return
			}
			if err != nil || strings.Contains(log.String(), "level=WARN") {
				t.Fatalf("preflight error %v, want a pass without Warn line:\n%s", err, log.String())
			}
			if n := firewallProbes(t, sudoLog); n != 4 || !probedOn(t, sudoLog, "127.0.0.1", port) || !probedOn(t, sudoLog, ip, port) {
				t.Errorf("want the control, metadata and two gateway connections, got %q", sudoCalls(t, sudoLog))
			}
		})
	}
}

// With no non-loopback local address that part is skipped with one Warn
// line; the loopback part still runs.
func TestRunAsFirewallNonLoopbackGatewayHostNoAddressWarns(t *testing.T) {
	fakeInterfaceAddrs(t, "127.0.0.1/8", "::1/128")
	port := loopbackPort(t, false)
	log, sudoLog, err := runFirewallPreflightHost(t, rejectPort(port), port, "0.0.0.0", 0)
	if err != nil {
		t.Fatalf("preflight error %v\n%s", err, log.String())
	}
	if n := strings.Count(log.String(), "level=WARN"); n != 1 || !strings.Contains(log.String(), "no non-loopback IPv4 address") {
		t.Errorf("want one Warn line for the missing address, got:\n%s", log.String())
	}
	if n := firewallProbes(t, sudoLog); n != 3 {
		t.Errorf("want the control, metadata and loopback gateway connections, got %q", sudoCalls(t, sudoLog))
	}
}

// GH-6480: a probe that never tried to connect is not a blocked connection.
// mutation pin: count a probe with no marker as a failed connection -> the
// killed probe on the gateway connection passes and this test fails.
func TestRunAsFirewallProbeNotRunRefuses(t *testing.T) {
	modes := map[string]string{
		"killed":    `exec bash -c 'kill -s KILL $$'`,
		"exit127":   `exec bash -c 'exit 127'`,
		"sudo_exit": `echo "sudo: unable to open a PAM session" >&2; exit 1`,
	}
	for name, code := range modes {
		t.Run(name+"/gateway", func(t *testing.T) {
			port := loopbackPort(t, false)
			extra := fmt.Sprintf(`case "$*" in *"/dev/tcp/127.0.0.1/%d;"*) %s ;; esac`, port, code)
			log, _, err := runFirewallPreflight(t, extra, port, 0)
			requireFirewallRefusal(t, log, err, fmt.Sprintf("the probe did not run for the gateway port %d on 127.0.0.1", port))
		})
		t.Run(name+"/metadata", func(t *testing.T) {
			gw, md := loopbackPort(t, false), loopbackPort(t, false)
			extra := rejectPort(gw) + "\n" + fmt.Sprintf(`case "$*" in *"/dev/tcp/127.0.0.1/%d;"*) %s ;; esac`, md, code)
			log, _, err := runFirewallPreflight(t, extra, gw, md)
			requireFirewallRefusal(t, log, err, fmt.Sprintf("the probe did not run for the metadata address 127.0.0.1:%d", md))
		})
	}
}

// GH-6480: a gateway host that is the second local address is probed on
// that address, not on 127.0.0.1 or the first one.
// mutation pin: return only the first non-loopback address for each host ->
// this test fails.
func TestRunAsFirewallGatewayHostSecondLocalAddress(t *testing.T) {
	ip := realLocalIPv4(t)
	if ip == "" {
		t.Skip("the host has no non-loopback IPv4 address")
	}
	fakeInterfaceAddrs(t, "127.0.0.1/8", "192.0.2.10/24", ip+"/24")
	port := loopbackPort(t, false)
	log, sudoLog, err := runFirewallPreflightHost(t, rejectAddr(ip, port), port, ip, 0)
	if err != nil {
		t.Fatalf("preflight error %v, want a pass:\n%s", err, log.String())
	}
	if !probedOn(t, sudoLog, ip, port) || probedOn(t, sudoLog, "127.0.0.1", port) || firewallProbes(t, sudoLog) != 3 {
		t.Errorf("want the gateway port probed on %s only, got %q", ip, sudoCalls(t, sudoLog))
	}
}

// GH-6480: a gateway host of 127.0.0.2 is probed on 127.0.0.2.
func TestRunAsFirewallGatewayHostOtherLoopback(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked=%v", blocked), func(t *testing.T) {
			port := loopbackPort(t, false)
			if l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.2", strconv.Itoa(port))); err != nil {
				t.Skipf("cannot listen on 127.0.0.2: %v", err)
			} else {
				_ = l.Close()
			}
			extra := rejectPort(port)
			if blocked {
				extra = rejectAddr("127.0.0.2", port)
			}
			log, sudoLog, err := runFirewallPreflightHost(t, extra, port, "127.0.0.2", 0)
			if !blocked {
				requireFirewallRefusal(t, log, err, fmt.Sprintf("reached the gateway port %d on 127.0.0.2", port))
				return
			}
			if err != nil {
				t.Fatalf("preflight error %v, want a pass:\n%s", err, log.String())
			}
			if !probedOn(t, sudoLog, "127.0.0.2", port) || probedOn(t, sudoLog, "127.0.0.1", port) {
				t.Errorf("want the gateway port probed on 127.0.0.2 only, got %q", sudoCalls(t, sudoLog))
			}
		})
	}
}

// GH-6480: a passed result is kept only for the same gateway port and host.
func TestRunAsFirewallPassedResultRerunsForOtherGateway(t *testing.T) {
	portA, portB := loopbackPort(t, false), loopbackPort(t, false)
	extra := rejectPort(portA) + "\n" + rejectPort(portB) + "\n" + rejectAddr("127.0.0.2", portB)
	log, sudoLog, err := runFirewallPreflight(t, extra, portA, 0)
	if err != nil {
		t.Fatalf("first preflight: %v\n%s", err, log.String())
	}
	n := firewallProbes(t, sudoLog)
	PreflightRunAsUser(firewallConfig(portB, "127.0.0.1"), "", true, 0)
	if err := RunAsUserPreflightError(); err != nil || firewallProbes(t, sudoLog) <= n || !probedOn(t, sudoLog, "127.0.0.1", portB) {
		t.Fatalf("another gateway port: error %v, connections %q: want the probes run again", err, sudoCalls(t, sudoLog))
	}
	n = firewallProbes(t, sudoLog)
	PreflightRunAsUser(firewallConfig(portB, "127.0.0.2"), "", true, 0)
	if err := RunAsUserPreflightError(); err != nil || firewallProbes(t, sudoLog) <= n || !probedOn(t, sudoLog, "127.0.0.2", portB) {
		t.Fatalf("another gateway host: error %v, connections %q: want the probes run again", err, sudoCalls(t, sudoLog))
	}
}

// GH-6480: the daemon start with a gateway port of 0 or below is refused;
// without requireGatewayPort the gateway part is skipped (GatewayPortZeroDebug).
func TestRunAsFirewallDaemonStartGatewayPortZeroRefuses(t *testing.T) {
	for _, port := range []int{0, -1} {
		t.Run(strconv.Itoa(port), func(t *testing.T) {
			setRunAsStateForTest(t, nil, "")
			_ = mockSudoAgent(t, "", "")
			stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
			runAsFirewallCheck = checkRunAsFirewall
			log := captureLog(t)
			cfg := firewallConfig(port, "")
			cfg.RunAsRequireGatewayPort = true
			PreflightRunAsUser(cfg, "", true, 0)
			requireFirewallRefusal(t, log, RunAsUserPreflightError(), runAsGatewayPortCheck)
		})
	}
}

func TestGatewayProbeAddrs(t *testing.T) {
	fakeInterfaceAddrs(t, "127.0.0.1/8", "10.0.0.5/24", "::1/128", "10.0.1.6/24", "2001:db8::1/64", "10.0.0.5/24")
	all := []string{"127.0.0.1", "10.0.0.5", "10.0.1.6"}
	for host, want := range map[string][]string{
		"10.0.1.6":      {"10.0.1.6"},
		"10.0.0.5":      {"10.0.0.5"},
		"127.0.0.1":     {"127.0.0.1"},
		"127.0.0.2":     {"127.0.0.2"},
		"198.51.100.9":  {"198.51.100.9"},
		"":              all,
		"0.0.0.0":       all,
		"::":            all,
		"localhost":     all,
		"pilot.example": all,
		"2001:db8::1":   all,
	} {
		if got := gatewayProbeAddrs(host); !slices.Equal(got, want) {
			t.Errorf("gatewayProbeAddrs(%q) = %q, want %q", host, got, want)
		}
	}
}
