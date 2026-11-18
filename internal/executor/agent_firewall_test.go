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
	return fmt.Sprintf(`case "$*" in *"/dev/tcp/127.0.0.1/%d"*) exit 1 ;; esac`, port)
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
case "$*" in *"/dev/tcp/127.0.0.1/%d"*) exit 1 ;; *"/dev/tcp/127.0.0.1/%d"*) ;; *"/dev/tcp/"*) sleep 1 ;; esac`, gw, md)
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
	for _, ip := range []string{"127.0.0.2", firstLocalIPv4()} {
		if ip == "" {
			continue
		}
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
	prev := runAsLocalIPv4
	runAsLocalIPv4 = func() string { return ip }
	t.Cleanup(func() { runAsLocalIPv4 = prev })
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked=%v", blocked), func(t *testing.T) {
			port := freePortOn(t, "127.0.0.1", ip)
			extra := rejectPort(port)
			if blocked {
				extra += fmt.Sprintf("\n"+`case "$*" in *"/dev/tcp/%s/%d"*) exit 1 ;; esac`, ip, port)
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

// GH-6477: a gateway host that is a specific IP is probed itself, not the
// first local address of the host.
// mutation pin: probe the first local IPv4 address for a gateway host
// 10.0.0.5 -> this test fails.
func TestRunAsGatewayProbeHostsSpecificIP(t *testing.T) {
	prev := runAsLocalIPv4
	runAsLocalIPv4 = func() string { return "192.0.2.7" }
	t.Cleanup(func() { runAsLocalIPv4 = prev })
	for host, want := range map[string]string{
		"10.0.0.5":      "127.0.0.1 10.0.0.5",
		"0.0.0.0":       "127.0.0.1 192.0.2.7",
		"":              "127.0.0.1 192.0.2.7",
		"::":            "127.0.0.1 192.0.2.7",
		"pilot.example": "127.0.0.1 192.0.2.7",
		"127.0.0.1":     "127.0.0.1",
		"localhost":     "127.0.0.1",
	} {
		if got := strings.Join(runAsGatewayProbeHosts(host), " "); got != want {
			t.Errorf("runAsGatewayProbeHosts(%q) = %q, want %q", host, got, want)
		}
	}
}

// GH-6477: a link-local address is not the local address of the probe.
// mutation pin: take the first non-loopback address, link-local included
// -> this test fails.
func TestFirstLocalIPv4SkipsLinkLocal(t *testing.T) {
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
		{[]string{"127.0.0.1/8", "169.254.10.20/16", "10.0.0.5/24"}, "10.0.0.5"},
		{[]string{"::1/128", "fe80::1/64", "169.254.10.20/16", "192.168.1.4/24"}, "192.168.1.4"},
		{[]string{"127.0.0.1/8", "169.254.10.20/16"}, ""},
	} {
		var addrs []net.Addr
		for _, a := range tc.addrs {
			addrs = append(addrs, ipNet(a))
		}
		if got := firstLocalIPv4Of(addrs); got != tc.want {
			t.Errorf("firstLocalIPv4Of(%q) = %q, want %q", tc.addrs, got, tc.want)
		}
	}
}

// With no non-loopback local address that part is skipped with one Warn
// line; the loopback part still runs.
func TestRunAsFirewallNonLoopbackGatewayHostNoAddressWarns(t *testing.T) {
	prev := runAsLocalIPv4
	runAsLocalIPv4 = func() string { return "" }
	t.Cleanup(func() { runAsLocalIPv4 = prev })
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
