package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/dashboard"
	"github.com/qf-studio/pilot/internal/executor"
)

// Exit codes of `pilot billing-mode` beyond the default 1 (gateway answered
// with an error).
const exitDaemonUnreachable = 2

// billingModeClientTimeout bounds one call to the gateway.
const billingModeClientTimeout = 5 * time.Second

// cliExitError is an error that carries the process exit code main must use.
type cliExitError struct {
	code int
	msg  string
}

func (e *cliExitError) Error() string { return e.msg }

// exitCodeOf maps a command error to the process exit code: the error's own
// code for a cliExitError, else 1.
func exitCodeOf(err error) int {
	var ce *cliExitError
	if errors.As(err, &ce) {
		return ce.code
	}
	return 1
}

// gatewayBaseURL is the base URL of the daemon's gateway, as pilot status
// prints it.
func gatewayBaseURL(cfg *config.Config) string {
	return fmt.Sprintf("http://%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
}

// billingModeBody is the body of GET and a successful POST /api/v1/billing-mode.
type billingModeBody struct {
	Mode       string `json:"mode"`
	Source     string `json:"source"`
	Until      string `json:"until"`
	ConfigMode string `json:"config_mode"`
}

// billingModeRequestBody is the POST body.
type billingModeRequestBody struct {
	Mode  string `json:"mode"`
	Until string `json:"until,omitempty"`
	SetBy string `json:"set_by,omitempty"`
}

func newBillingModeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "billing-mode",
		Short: "Show or switch the billing mode of the running daemon",
		Long: `Show or switch the billing mode of the running daemon through its gateway.

Unlike "pilot config", this does not edit config.yaml and needs no restart: the
override applies to every task admitted from now on and survives a daemon
restart until its expiry.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(newBillingModeStatusCmd(), newBillingModeSetCmd())
	return cmd
}

func newBillingModeStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show the billing mode in force, its source and expiry",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			body, err := callBillingMode(cfg, http.MethodGet, nil)
			if err != nil {
				return err
			}
			printBillingMode(cmd.OutOrStdout(), body)
			return nil
		},
	}
}

func newBillingModeSetCmd() *cobra.Command {
	var until string

	cmd := &cobra.Command{
		Use:   "set <subscription|api_key|config>",
		Short: "Switch the billing mode of the running daemon",
		Long: `Switch the billing mode of the running daemon.

subscription and api_key override the config's mode; config drops the override
so the config decides again. --until takes an RFC 3339 time or a duration from
now, after which the override reverts to the config's mode.

Examples:
  pilot billing-mode set api_key --until 2h
  pilot billing-mode set subscription --until 2026-11-05T09:00:00Z
  pilot billing-mode set config`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			untilRFC3339, err := parseBillingUntil(until, time.Now())
			if err != nil {
				return err
			}
			body, err := callBillingMode(cfg, http.MethodPost, &billingModeRequestBody{
				Mode:  args[0],
				Until: untilRFC3339,
				SetBy: executor.BillingModeSourceCLI,
			})
			if err != nil {
				return err
			}
			printBillingMode(cmd.OutOrStdout(), body)
			return nil
		},
	}
	cmd.Flags().StringVar(&until, "until", "", "expiry: an RFC 3339 time or a duration from now (e.g. 2h)")
	return cmd
}

// parseBillingUntil turns an --until value into an RFC 3339 time: "" stays
// empty (no expiry), a duration is added to now.
func parseBillingUntil(s string, now time.Time) (string, error) {
	if s == "" {
		return "", nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return "", fmt.Errorf("invalid --until %q: a duration must be positive", s)
		}
		return now.Add(d).UTC().Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("invalid --until %q: want an RFC 3339 time or a duration such as 2h", s)
}

// callBillingMode calls /api/v1/billing-mode on the daemon's gateway. A gateway
// that does not answer is exit 2; one that answers with an error is exit 1
// carrying the gateway's message.
func callBillingMode(cfg *config.Config, method string, req *billingModeRequestBody) (*billingModeBody, error) {
	baseURL := gatewayBaseURL(cfg)

	var payload io.Reader
	if req != nil {
		data, err := json.Marshal(req)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		payload = bytes.NewReader(data)
	}
	httpReq, err := http.NewRequest(method, baseURL+"/api/v1/billing-mode", payload)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if req != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if auth := cfg.GatewayAuthConfig(); auth != nil {
		httpReq.Header.Set("Authorization", "Bearer "+auth.Token)
	}

	client := &http.Client{Timeout: billingModeClientTimeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, &cliExitError{code: exitDaemonUnreachable, msg: fmt.Sprintf("daemon not reachable at %s", baseURL)}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		text := strings.TrimSpace(string(msg))
		if text == "" {
			text = resp.Status
		}
		return nil, &cliExitError{code: 1, msg: text}
	}
	var body billingModeBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode gateway response: %w", err)
	}
	return &body, nil
}

func printBillingMode(w io.Writer, b *billingModeBody) {
	until := b.Until
	if until == "" {
		until = "none"
	}
	_, _ = fmt.Fprintf(w, "mode:        %s\n", b.Mode)
	_, _ = fmt.Fprintf(w, "source:      %s\n", b.Source)
	_, _ = fmt.Fprintf(w, "until:       %s\n", until)
	_, _ = fmt.Fprintf(w, "config mode: %s\n", b.ConfigMode)
}

// billingStatusLine renders the pilot status billing line, or "" when the
// status body carries no billing_mode (an older daemon).
func billingStatusLine(st admissionStatusBody) string {
	if st.BillingMode == "" {
		return ""
	}
	line := fmt.Sprintf("billing: %s (%s)", st.BillingMode, st.BillingModeSource)
	if st.BillingModeUntil != "" {
		line += fmt.Sprintf(", back to %s at %s", st.BillingConfigMode, st.BillingModeUntil)
	}
	return line
}

// wireBillingChip feeds the TUI header chip: the mode in force once at boot and
// again after every switch.
func wireBillingChip(d *executor.Dispatcher, program *tea.Program) {
	if d == nil || program == nil {
		return
	}
	d.SetBillingObserver(func(st executor.BillingOverrideStatus) {
		program.Send(billingModeMsg(st))
	})
	// Send blocks until the program runs, so the boot message goes out of band.
	go func() {
		program.Send(billingModeMsg(d.BillingOverride()))
	}()
}

func billingModeMsg(st executor.BillingOverrideStatus) dashboard.BillingModeMsg {
	return dashboard.BillingModeMsg{Mode: st.Mode, Source: st.Source, Until: st.Until}
}
