package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/gateway"
)

// billingTestGateway is an httptest gateway that records the billing-mode
// requests it gets and answers with a canned status.
type billingTestGateway struct {
	srv *httptest.Server

	mu     sync.Mutex
	posts  []billingModeRequestBody
	auth   []string
	status string // /api/v1/status body
	reply  billingModeBody
	fail   string // when set, POST answers 400 with this message
}

func newBillingTestGateway(t *testing.T) *billingTestGateway {
	t.Helper()
	g := &billingTestGateway{reply: billingModeBody{Mode: "subscription", Source: "config", ConfigMode: "subscription"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/billing-mode", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.auth = append(g.auth, r.Header.Get("Authorization"))
		if r.Method == http.MethodPost {
			var req billingModeRequestBody
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			g.posts = append(g.posts, req)
			if g.fail != "" {
				http.Error(w, g.fail, http.StatusBadRequest)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(g.reply)
	})
	mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		_, _ = w.Write([]byte(g.status))
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

// useGatewayConfig points cfgFile at a config whose gateway is addr ("host:port")
// and returns the config path.
func useGatewayConfig(t *testing.T, addr string, auth bool) string {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	cfg := config.DefaultConfig()
	cfg.Gateway.Host = host
	cfg.Gateway.Port = port
	if auth {
		cfg.Auth.Type = "api-token"
		cfg.Auth.Token = "test-gateway-token"
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(cfg, path); err != nil {
		t.Fatalf("save config: %v", err)
	}
	old := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = old })
	return path
}

func runBillingModeCmd(t *testing.T, args ...string) (stdout string, exit int) {
	t.Helper()
	var out bytes.Buffer
	cmd := newBillingModeCmd()
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		return out.String() + err.Error(), exitCodeOf(err)
	}
	return out.String(), 0
}

func TestBillingCmd_StatusAndSet(t *testing.T) {
	t.Run("status reads the gateway", func(t *testing.T) {
		g := newBillingTestGateway(t)
		g.reply = billingModeBody{Mode: "api_key", Source: "cli", Until: "2026-11-05T09:00:00Z", ConfigMode: "subscription"}
		useGatewayConfig(t, g.srv.Listener.Addr().String(), false)

		out, exit := runBillingModeCmd(t, "status")
		if exit != 0 {
			t.Fatalf("exit = %d, output %q", exit, out)
		}
		for _, want := range []string{"api_key", "cli", "2026-11-05T09:00:00Z", "subscription"} {
			if !strings.Contains(out, want) {
				t.Errorf("status output lacks %q:\n%s", want, out)
			}
		}
		if len(g.posts) != 0 {
			t.Errorf("status posted %d requests, want none", len(g.posts))
		}
	})

	t.Run("set posts to the gateway and leaves config.yaml alone", func(t *testing.T) {
		g := newBillingTestGateway(t)
		g.reply = billingModeBody{Mode: "api_key", Source: "cli", Until: "2026-11-05T09:00:00Z", ConfigMode: "subscription"}
		path := useGatewayConfig(t, g.srv.Listener.Addr().String(), true)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		out, exit := runBillingModeCmd(t, "set", "api_key", "--until", "2026-11-05T09:00:00Z")
		if exit != 0 {
			t.Fatalf("exit = %d, output %q", exit, out)
		}
		if len(g.posts) != 1 {
			t.Fatalf("gateway got %d POSTs, want 1", len(g.posts))
		}
		got := g.posts[0]
		if got.Mode != "api_key" || got.Until != "2026-11-05T09:00:00Z" || got.SetBy != "cli" {
			t.Errorf("POST body = %+v, want mode api_key, until 2026-11-05T09:00:00Z, set_by cli", got)
		}
		if g.auth[0] != "Bearer test-gateway-token" {
			t.Errorf("Authorization = %q, want the configured bearer token", g.auth[0])
		}
		if !strings.Contains(out, "api_key") || !strings.Contains(out, "cli") {
			t.Errorf("set did not print the resulting status:\n%s", out)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("config.yaml changed; set must only post to the gateway")
		}
	})

	t.Run("a duration becomes an RFC 3339 until", func(t *testing.T) {
		g := newBillingTestGateway(t)
		useGatewayConfig(t, g.srv.Listener.Addr().String(), false)

		start := time.Now()
		if out, exit := runBillingModeCmd(t, "set", "subscription", "--until", "2h"); exit != 0 {
			t.Fatalf("exit = %d, output %q", exit, out)
		}
		if len(g.posts) != 1 {
			t.Fatalf("gateway got %d POSTs, want 1", len(g.posts))
		}
		until, err := time.Parse(time.RFC3339, g.posts[0].Until)
		if err != nil {
			t.Fatalf("until %q is not RFC 3339: %v", g.posts[0].Until, err)
		}
		if d := until.Sub(start); d < 2*time.Hour-time.Minute || d > 2*time.Hour+time.Minute {
			t.Errorf("until is %s from now, want about 2h", d)
		}
	})

	t.Run("set config posts mode config with no until", func(t *testing.T) {
		g := newBillingTestGateway(t)
		useGatewayConfig(t, g.srv.Listener.Addr().String(), false)

		if out, exit := runBillingModeCmd(t, "set", "config"); exit != 0 {
			t.Fatalf("exit = %d, output %q", exit, out)
		}
		if len(g.posts) != 1 {
			t.Fatalf("gateway got %d POSTs, want 1", len(g.posts))
		}
		if got := g.posts[0]; got.Mode != "config" || got.Until != "" {
			t.Errorf("POST body = %+v, want mode config and no until", got)
		}
	})

	t.Run("a 400 exits 1 with the gateway message", func(t *testing.T) {
		g := newBillingTestGateway(t)
		g.fail = `invalid mode "bogus": accepted values are subscription, api_key, config`
		useGatewayConfig(t, g.srv.Listener.Addr().String(), false)

		out, exit := runBillingModeCmd(t, "set", "bogus")
		if exit != 1 {
			t.Errorf("exit = %d, want 1", exit)
		}
		if !strings.Contains(out, g.fail) {
			t.Errorf("output lacks the gateway message %q:\n%s", g.fail, out)
		}
	})

	t.Run("an unreachable daemon exits 2", func(t *testing.T) {
		g := newBillingTestGateway(t)
		addr := g.srv.Listener.Addr().String()
		g.srv.Close()
		useGatewayConfig(t, addr, false)

		for _, args := range [][]string{{"status"}, {"set", "api_key"}} {
			out, exit := runBillingModeCmd(t, args...)
			if exit != 2 {
				t.Errorf("%v: exit = %d, want 2", args, exit)
			}
			if want := "daemon not reachable at http://" + addr; !strings.Contains(out, want) {
				t.Errorf("%v: output %q lacks %q", args, out, want)
			}
		}
	})

	t.Run("a bad --until never reaches the gateway", func(t *testing.T) {
		g := newBillingTestGateway(t)
		useGatewayConfig(t, g.srv.Listener.Addr().String(), false)

		if _, exit := runBillingModeCmd(t, "set", "api_key", "--until", "tomorrow"); exit != 1 {
			t.Errorf("exit = %d, want 1", exit)
		}
		if len(g.posts) != 0 {
			t.Errorf("gateway got %d POSTs for an unparseable --until", len(g.posts))
		}
	})
}

func TestStatusCmd_BillingLine(t *testing.T) {
	statusOutput := func(t *testing.T, body string) string {
		t.Helper()
		g := newBillingTestGateway(t)
		g.status = body
		useGatewayConfig(t, g.srv.Listener.Addr().String(), false)
		return captureStdout(func() {
			cmd := newStatusCmd()
			cmd.SetArgs(nil)
			if err := cmd.Execute(); err != nil {
				t.Errorf("pilot status: %v", err)
			}
		})
	}

	t.Run("override with an expiry", func(t *testing.T) {
		out := statusOutput(t, `{"billing_mode":"api_key","billing_mode_source":"cli","billing_mode_until":"2026-11-05T09:00:00Z","billing_config_mode":"subscription"}`)
		want := "billing: api_key (cli), back to subscription at 2026-11-05T09:00:00Z"
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	})

	t.Run("config source without an expiry", func(t *testing.T) {
		out := statusOutput(t, `{"billing_mode":"subscription","billing_mode_source":"config","billing_mode_until":"","billing_config_mode":"subscription"}`)
		if !strings.Contains(out, "billing: subscription (config)\n") {
			t.Errorf("status output lacks the plain billing line:\n%s", out)
		}
		if strings.Contains(out, "back to") {
			t.Errorf("a config source has no expiry to report:\n%s", out)
		}
	})

	t.Run("older daemon without billing_mode prints no billing line", func(t *testing.T) {
		out := statusOutput(t, `{"status":"ok","admission_paused":false}`)
		if strings.Contains(out, "billing") {
			t.Errorf("status prints a billing line for a body without billing_mode:\n%s", out)
		}
		if !strings.Contains(out, "Pilot Status") {
			t.Errorf("status output is missing entirely:\n%s", out)
		}
	})
}

// recordingBillingSource is a gateway.BillingOverrideSource that reports the
// setBy it was given as the Source, as the dispatcher does.
type recordingBillingSource struct {
	mu     sync.Mutex
	status executor.BillingOverrideStatus
	setBys []string
}

func (r *recordingBillingSource) BillingOverride() executor.BillingOverrideStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *recordingBillingSource) SetBillingOverride(mode string, until time.Time, setBy string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setBys = append(r.setBys, setBy)
	r.status = executor.BillingOverrideStatus{Mode: mode, Source: setBy, Until: until, ConfigMode: "subscription"}
	return nil
}

func (r *recordingBillingSource) ClearBillingOverride(setBy string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setBys = append(r.setBys, setBy)
	r.status = executor.BillingOverrideStatus{Mode: "subscription", Source: "config", ConfigMode: "subscription"}
	return nil
}

// TestBillingCmd_SetReportsCLISource runs pilot billing-mode set against the
// real gateway handler: the source cli on the printed status must come from the
// set_by the command posts, not from a canned reply.
func TestBillingCmd_SetReportsCLISource(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	src := &recordingBillingSource{status: executor.BillingOverrideStatus{Mode: "subscription", Source: "config", ConfigMode: "subscription"}}
	server := gateway.NewServerWithAuth(&gateway.Config{Host: "127.0.0.1", Port: port},
		&gateway.AuthConfig{Type: gateway.AuthTypeAPIToken, Token: "test-gateway-token"})
	server.SetBillingOverrideSource(src)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = server.Start(ctx) }()
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	useGatewayConfig(t, addr, true)

	out, exit := runBillingModeCmd(t, "set", "api_key")
	if exit != 0 {
		t.Fatalf("exit = %d, output %q", exit, out)
	}
	if !strings.Contains(out, "api_key") || !strings.Contains(out, "cli") || strings.Contains(out, "gateway") {
		t.Errorf("set did not report source cli:\n%s", out)
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if len(src.setBys) != 1 || src.setBys[0] != "cli" {
		t.Errorf("source got setBy %v, want [cli]", src.setBys)
	}
}

// TestStatusCmd_BillingLineAuthed: /api/v1/status is protected, so pilot status
// must send the configured bearer token to print the billing line.
func TestStatusCmd_BillingLineAuthed(t *testing.T) {
	const body = `{"billing_mode":"api_key","billing_mode_source":"cli","billing_mode_until":"","billing_config_mode":"subscription"}`
	var (
		mu   sync.Mutex
		auth []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer test-gateway-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	runStatus := func(t *testing.T) string {
		t.Helper()
		return captureStdout(func() {
			cmd := newStatusCmd()
			cmd.SetArgs(nil)
			if err := cmd.Execute(); err != nil {
				t.Errorf("pilot status: %v", err)
			}
		})
	}

	t.Run("token configured prints the billing line", func(t *testing.T) {
		useGatewayConfig(t, srv.Listener.Addr().String(), true)
		out := runStatus(t)
		if !strings.Contains(out, "billing: api_key (cli)") {
			t.Errorf("status output lacks the billing line:\n%s", out)
		}
		mu.Lock()
		defer mu.Unlock()
		if got := auth[len(auth)-1]; got != "Bearer test-gateway-token" {
			t.Errorf("Authorization = %q, want the configured bearer token", got)
		}
	})

	t.Run("no token configured prints no billing line", func(t *testing.T) {
		useGatewayConfig(t, srv.Listener.Addr().String(), false)
		if out := runStatus(t); strings.Contains(out, "billing") {
			t.Errorf("status printed a billing line the gateway refused:\n%s", out)
		}
	})

	t.Run("daemon down prints no billing line", func(t *testing.T) {
		down := httptest.NewServer(http.NotFoundHandler())
		addr := down.Listener.Addr().String()
		down.Close()
		useGatewayConfig(t, addr, true)
		out := runStatus(t)
		if strings.Contains(out, "billing") {
			t.Errorf("status printed a billing line with no daemon:\n%s", out)
		}
		if !strings.Contains(out, "Pilot Status") {
			t.Errorf("status output is missing entirely:\n%s", out)
		}
	})
}
