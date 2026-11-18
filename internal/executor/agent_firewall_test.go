package executor

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sort"
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

// rejectAddr is mock sudo code that fails the connection of the probe of
// port on ip: the probe runs and prints the marker with status 1, as bash
// does for a refused connection.
func rejectAddr(ip string, port int) string {
	return fmt.Sprintf(`case "$*" in *"/dev/tcp/%s/%d;"*) echo %s1; exit 0 ;; esac`, ip, port, runAsProbeMarker)
}

// gatewayProbesOn returns, sorted, the addresses on which a probe of the
// sudo log connected to port.
func gatewayProbesOn(t *testing.T, sudoLog string, port int) []string {
	t.Helper()
	var out []string
	for _, c := range sudoCalls(t, sudoLog) {
		_, rest, ok := strings.Cut(c, "/dev/tcp/")
		if !ok {
			continue
		}
		addr, _, _ := strings.Cut(rest, ";")
		if i := strings.LastIndex(addr, "/"); i > 0 && addr[i+1:] == fmt.Sprint(port) {
			out = append(out, addr[:i])
		}
	}
	sort.Strings(out)
	return out
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
	runAsAgentDial = func(s *runAsSettings, addr string, timeout time.Duration) agentDialResult {
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
		return agentDialAs(s, addr, timeout)
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
	runAsAgentDial = func(*runAsSettings, string, time.Duration) agentDialResult { ran = true; return agentDialFailed }
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

// GH-6477: the tests shorten runAsDialTimeout; the control connection keeps
// its own limit, so a slow mock sudo under -race does not read as a probe
// tool that does not work.
// mutation pin: bound the control connection by runAsDialTimeout -> this
// test fails.
func TestRunAsFirewallControlDialKeepsItsLimit(t *testing.T) {
	prev := runAsDialTimeout
	runAsDialTimeout = 200 * time.Millisecond
	t.Cleanup(func() { runAsDialTimeout = prev })
	gw, md := loopbackPort(t, false), loopbackPort(t, false)
	// Every connection but the gateway and metadata ones (the control one)
	// answers after more than runAsDialTimeout.
	slowControl := fmt.Sprintf(`case "$1" in -[0-9]*) kill -s KILL -- "$1"; exit $? ;; esac
case "$*" in *"/dev/tcp/127.0.0.1/%d;"*) echo %s1; exit 0 ;; *"/dev/tcp/127.0.0.1/%d;"*) ;; *"/dev/tcp/"*) sleep 1 ;; esac`, gw, runAsProbeMarker, md)
	log, sudoLog, err := runFirewallPreflight(t, slowControl, gw, md)
	if err != nil {
		t.Fatalf("preflight error %v, want a pass:\n%s", err, log.String())
	}
	if n := firewallProbes(t, sudoLog); n != 3 {
		t.Errorf("want the control, metadata and gateway connections, got %q", sudoCalls(t, sudoLog))
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

// secondProbeAddr returns a local IPv4 address other than 127.0.0.1 that the
// test can listen on: 127.0.0.2 (all of 127.0.0.0/8 is local on Linux), else
// the first local IPv4 address of the host.
func secondProbeAddr(t *testing.T) string {
	t.Helper()
	for _, ip := range append([]string{"127.0.0.2"}, localIPv4s()...) {
		if l, err := net.Listen("tcp", net.JoinHostPort(ip, "0")); err == nil {
			_ = l.Close()
			return ip
		}
	}
	t.Fatal("no local IPv4 address other than 127.0.0.1 to listen on")
	return ""
}

// freePortOn returns a port that is free on every host of hosts.
func freePortOn(t *testing.T, hosts ...string) int {
	t.Helper()
	for try := 0; try < 20; try++ {
		first, err := net.Listen("tcp", net.JoinHostPort(hosts[0], "0"))
		if err != nil {
			t.Fatal(err)
		}
		port := first.Addr().(*net.TCPAddr).Port
		ls, free := []net.Listener{first}, true
		for _, h := range hosts[1:] {
			l, err := net.Listen("tcp", net.JoinHostPort(h, fmt.Sprint(port)))
			if err != nil {
				free = false
				break
			}
			ls = append(ls, l)
		}
		for _, l := range ls {
			_ = l.Close()
		}
		if free {
			return port
		}
	}
	t.Fatalf("no port free on all of %q", hosts)
	return 0
}

// GH-6455: with a gateway host that is not a loopback address the gateway
// port on a non-loopback local address must be closed too. GH-6477: the
// local address is stubbed, so the test runs on a runner with no
// non-loopback IPv4 address too, and the port is free on both addresses.
// mutation pin: probe the gateway port on 127.0.0.1 only for a gateway host
// 0.0.0.0 -> this test fails (on every runner, it no longer skips).
func TestRunAsFirewallNonLoopbackGatewayHost(t *testing.T) {
	ip := secondProbeAddr(t)
	stubLocalIPv4s(t, ip)
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked=%v", blocked), func(t *testing.T) {
			port := freePortOn(t, "127.0.0.1", ip)
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
			if n := firewallProbes(t, sudoLog); n != 4 {
				t.Errorf("want the control, metadata and two gateway connections, got %q", sudoCalls(t, sudoLog))
			}
		})
	}
}

// stubLocalIPv4s replaces the local IPv4 addresses of the host.
func stubLocalIPv4s(t *testing.T, ips ...string) {
	t.Helper()
	prev := runAsLocalIPv4s
	runAsLocalIPv4s = func() []string { return ips }
	t.Cleanup(func() { runAsLocalIPv4s = prev })
}

// GH-6480: a literal IPv4 host is probed on that address alone, the second
// local address and 127.0.0.2 too; a wildcard, an empty host or a host name
// on 127.0.0.1 and every local address.
// mutation pin: return only the first non-loopback address for each host ->
// this test fails.
func TestGatewayProbeAddrs(t *testing.T) {
	stubLocalIPv4s(t, "10.0.0.5", "10.0.0.6")
	for host, want := range map[string]string{
		"10.0.0.6":      "10.0.0.6",
		"10.0.0.5":      "10.0.0.5",
		"127.0.0.2":     "127.0.0.2",
		"127.0.0.1":     "127.0.0.1",
		"192.0.2.9":     "192.0.2.9",
		"0.0.0.0":       "127.0.0.1 10.0.0.5 10.0.0.6",
		"":              "127.0.0.1 10.0.0.5 10.0.0.6",
		"::":            "127.0.0.1 10.0.0.5 10.0.0.6",
		"pilot.example": "127.0.0.1 10.0.0.5 10.0.0.6",
		"localhost":     "127.0.0.1 10.0.0.5 10.0.0.6",
		"::1":           "127.0.0.1",
		"2001:db8::1":   "127.0.0.1",
	} {
		if got := strings.Join(gatewayProbeAddrs(host), " "); got != want {
			t.Errorf("gatewayProbeAddrs(%q) = %q, want %q", host, got, want)
		}
	}
}

// GH-6477: a link-local address is not a local address of the probe.
// mutation pin: take every non-loopback address, link-local included ->
// this test fails.
func TestRunAsFirewallLocalIPv4sOfSkipsLinkLocal(t *testing.T) {
	ipNet := func(cidr string) net.Addr {
		ip, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		return n
	}
	for _, tc := range []struct {
		addrs []string
		want  string
	}{
		{[]string{"127.0.0.1/8", "169.254.10.20/16", "10.0.0.5/24", "10.0.1.7/24"}, "10.0.0.5 10.0.1.7"},
		{[]string{"::1/128", "fe80::1/64", "169.254.10.20/16", "192.168.1.4/24", "2001:db8::1/64"}, "192.168.1.4"},
		{[]string{"127.0.0.1/8", "169.254.10.20/16"}, ""},
	} {
		var addrs []net.Addr
		for _, a := range tc.addrs {
			addrs = append(addrs, ipNet(a))
		}
		if got := strings.Join(localIPv4sOf(addrs), " "); got != tc.want {
			t.Errorf("localIPv4sOf(%q) = %q, want %q", tc.addrs, got, tc.want)
		}
	}
}

// With no non-loopback local address that part is skipped with one Warn
// line; the loopback part still runs.
func TestRunAsFirewallNonLoopbackGatewayHostNoAddressWarns(t *testing.T) {
	stubLocalIPv4s(t)
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

func TestLoopbackGatewayHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1": true, "127.0.0.2": true, "localhost": true, "::1": true,
		"0.0.0.0": false, "": false, "10.0.0.5": false, "pilot.example": false,
	} {
		if got := loopbackGatewayHost(host); got != want {
			t.Errorf("loopbackGatewayHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// notRunProbes are mock sudo ends of a probe that never reports a connection
// try: the probe is killed, bash is missing (127), sudo fails for the agent
// user before bash runs.
var notRunProbes = map[string]string{
	"killed":    `kill -s KILL $$`,
	"exit127":   `echo "bash: command not found" >&2; exit 127`,
	"sudo_fail": `echo "sudo: unable to fork: Resource temporarily unavailable" >&2; exit 1`,
}

// onAddr is mock sudo code that runs body for the probe of port on
// 127.0.0.1.
func onAddr(port int, body string) string {
	return fmt.Sprintf(`case "$*" in *"/dev/tcp/127.0.0.1/%d;"*) %s ;; esac`, port, body)
}

// GH-6480: a probe of the gateway port that did not run proves no reject
// rule and is a refusal; the control connection still connects.
// mutation pin: count a probe with no marker as a failed connection -> this
// test fails.
func TestRunAsFirewallGatewayProbeNotRunRefuses(t *testing.T) {
	for name, body := range notRunProbes {
		t.Run(name, func(t *testing.T) {
			port := loopbackPort(t, false)
			log, sudoLog, err := runFirewallPreflight(t, onAddr(port, body), port, 0)
			requireFirewallRefusal(t, log, err, fmt.Sprintf("the probe of the gateway port %d on 127.0.0.1 did not run", port))
			if n := firewallProbes(t, sudoLog); n != 3 {
				t.Errorf("want the control, metadata and gateway connections, got %q", sudoCalls(t, sudoLog))
			}
		})
	}
}

// GH-6480: the same for the metadata address.
func TestRunAsFirewallMetadataProbeNotRunRefuses(t *testing.T) {
	for name, body := range notRunProbes {
		t.Run(name, func(t *testing.T) {
			gw, md := loopbackPort(t, false), loopbackPort(t, false)
			log, _, err := runFirewallPreflight(t, rejectPort(gw)+"\n"+onAddr(md, body), gw, md)
			requireFirewallRefusal(t, log, err, fmt.Sprintf("the probe of the metadata address 127.0.0.1:%d did not run", md))
		})
	}
}

// GH-6480: the control connection reads the marker too: a probe that exits
// 0 without it does not prove a connection.
func TestRunAsFirewallControlWithoutMarkerRefuses(t *testing.T) {
	gw, md := loopbackPort(t, false), loopbackPort(t, false)
	extra := rejectPort(gw) + "\n" + rejectPort(md) + "\n" + `case "$*" in *"/dev/tcp/"*) exit 0 ;; esac`
	log, _, err := runFirewallPreflight(t, extra, gw, md)
	requireFirewallRefusal(t, log, err, "the probe tool (bash /dev/tcp) does not work")
}

func TestRunAsFirewallProbeMarkerResult(t *testing.T) {
	for out, want := range map[string]agentDialResult{
		runAsProbeMarker + "0\n":            agentDialConnected,
		runAsProbeMarker + "1\n":            agentDialFailed,
		"motd\n" + runAsProbeMarker + "1\n": agentDialFailed,
		"":                                  agentDialNotRun,
		runAsProbeMarker + "\n":             agentDialNotRun,
		runAsProbeMarker + "x\n":            agentDialNotRun,
		runAsProbeMarker + "0\n" + runAsProbeMarker + "1\n": agentDialNotRun,
	} {
		if got := probeMarkerResult(out); got != want {
			t.Errorf("probeMarkerResult(%q) = %d, want %d", out, got, want)
		}
	}
}

// loopbackAliases skips the test unless the daemon can listen on every
// address of ips (all of 127.0.0.0/8 is local on Linux).
func loopbackAliases(t *testing.T, ips ...string) {
	t.Helper()
	for _, ip := range ips {
		l, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
		if err != nil {
			t.Skipf("cannot listen on %s: %v", ip, err)
		}
		_ = l.Close()
	}
}

// GH-6480: a gateway host that is the second local address is probed on
// that address, not on 127.0.0.1 or the first local address.
func TestRunAsFirewallGatewayHostSecondLocalAddress(t *testing.T) {
	loopbackAliases(t, "127.0.0.2", "127.0.0.3")
	stubLocalIPv4s(t, "127.0.0.2", "127.0.0.3")
	port := freePortOn(t, "127.0.0.1", "127.0.0.2", "127.0.0.3")
	log, sudoLog, err := runFirewallPreflightHost(t, rejectAddr("127.0.0.3", port), port, "127.0.0.3", 0)
	if err != nil || strings.Contains(log.String(), "level=WARN") {
		t.Fatalf("preflight error %v, want a pass without Warn line:\n%s", err, log.String())
	}
	if got := strings.Join(gatewayProbesOn(t, sudoLog, port), " "); got != "127.0.0.3" {
		t.Errorf("gateway port %d probed on %q, want 127.0.0.3 only", port, got)
	}
}

// GH-6480: a gateway host 127.0.0.2 is probed on 127.0.0.2; a probe there
// that connects is a refusal.
func TestRunAsFirewallGatewayHostOtherLoopback(t *testing.T) {
	loopbackAliases(t, "127.0.0.2")
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked=%v", blocked), func(t *testing.T) {
			port := freePortOn(t, "127.0.0.1", "127.0.0.2")
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
			if got := strings.Join(gatewayProbesOn(t, sudoLog, port), " "); got != "127.0.0.2" {
				t.Errorf("gateway port %d probed on %q, want 127.0.0.2 only", port, got)
			}
		})
	}
}

// GH-6480: a wildcard gateway host is probed on 127.0.0.1 and on each local
// address, the last one included.
func TestRunAsFirewallWildcardGatewayHostEveryAddress(t *testing.T) {
	loopbackAliases(t, "127.0.0.2", "127.0.0.3")
	stubLocalIPv4s(t, "127.0.0.2", "127.0.0.3")
	for _, lastBlocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("last_blocked=%v", lastBlocked), func(t *testing.T) {
			port := freePortOn(t, "127.0.0.1", "127.0.0.2", "127.0.0.3")
			extra := rejectPort(port) + "\n" + rejectAddr("127.0.0.2", port)
			if lastBlocked {
				extra += "\n" + rejectAddr("127.0.0.3", port)
			}
			log, sudoLog, err := runFirewallPreflightHost(t, extra, port, "0.0.0.0", 0)
			if !lastBlocked {
				requireFirewallRefusal(t, log, err, fmt.Sprintf("reached the gateway port %d on 127.0.0.3", port))
				return
			}
			if err != nil {
				t.Fatalf("preflight error %v, want a pass:\n%s", err, log.String())
			}
			if got := strings.Join(gatewayProbesOn(t, sudoLog, port), " "); got != "127.0.0.1 127.0.0.2 127.0.0.3" {
				t.Errorf("gateway port %d probed on %q, want 127.0.0.1, 127.0.0.2 and 127.0.0.3", port, got)
			}
		})
	}
}

// GH-6480: a passed result is kept for the same gateway only: another
// gateway port or another gateway host runs the probes again.
// mutation pin: drop the gateway port or the gateway host from the compare
// of the kept result -> this test fails.
func TestRunAsFirewallPassedResultKeptForSameGatewayOnly(t *testing.T) {
	loopbackAliases(t, "127.0.0.2")
	a := freePortOn(t, "127.0.0.1", "127.0.0.2")
	b := freePortOn(t, "127.0.0.1", "127.0.0.2")
	for b == a {
		b = freePortOn(t, "127.0.0.1", "127.0.0.2")
	}
	extra := rejectPort(a) + "\n" + rejectPort(b) + "\n" + rejectAddr("127.0.0.2", b)
	log, sudoLog, err := runFirewallPreflight(t, extra, a, 0)
	if err != nil {
		t.Fatalf("first preflight: %v\n%s", err, log.String())
	}
	for _, step := range []struct {
		name string
		cfg  *ClaudeCodeConfig
	}{
		{"another gateway port", firewallConfig(b, "127.0.0.1")},
		{"another gateway host", firewallConfig(b, "127.0.0.2")},
	} {
		before := firewallProbes(t, sudoLog)
		PreflightRunAsUser(step.cfg, "", true, 0)
		if err := RunAsUserPreflightError(); err != nil {
			t.Fatalf("%s: preflight error %v", step.name, err)
		}
		if after := firewallProbes(t, sudoLog); after <= before {
			t.Errorf("%s: the probes did not run again after a passed result (%d connections before, %d after)", step.name, before, after)
		}
	}
}

// GH-6480: at the daemon start (RunAsGatewayRequired) a gateway port of 0
// or below is a refusal, and no probe runs; with the option empty nothing
// happens.
func TestRunAsUserDaemonStartWithoutGatewayPortRefuses(t *testing.T) {
	for _, port := range []int{0, -1} {
		t.Run(fmt.Sprint(port), func(t *testing.T) {
			setRunAsStateForTest(t, nil, "")
			sudoLog := mockSudoAgent(t, "", "")
			stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
			runAsFirewallCheck = checkRunAsFirewall
			log := captureLog(t)
			cfg := firewallConfig(port, "")
			cfg.RunAsGatewayRequired = true
			PreflightRunAsUser(cfg, "", true, 0)
			requireFirewallRefusal(t, log, RunAsUserPreflightError(), runAsGatewayPortCheck)
			if n := firewallProbes(t, sudoLog); n != 0 {
				t.Errorf("want no probe, got %q", sudoCalls(t, sudoLog))
			}

			PreflightRunAsUser(&ClaudeCodeConfig{RunAsGatewayRequired: true, RunAsGatewayPort: port}, "", true, 0)
			if err := RunAsUserPreflightError(); err != nil {
				t.Errorf("option empty: preflight error %v, want none", err)
			}
		})
	}
}
