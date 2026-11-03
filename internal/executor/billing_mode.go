package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// BillingMode says how a model call is paid for (GH-6016): out of the Claude
// subscription the CLI is logged in to, or with an Anthropic API key. The
// claude CLI picks its credential from its own environment, so the mode is
// "which credential variables the subprocess env carries".
type BillingMode string

const (
	// BillingModeSubscription runs model subprocesses with no API key, so the
	// CLI uses its OAuth login. The default.
	BillingModeSubscription BillingMode = "subscription"
	// BillingModeAPIKey injects a resolved Anthropic API key into every model
	// subprocess of one execution.
	BillingModeAPIKey BillingMode = "api_key"
)

// billingKeyEnv is the variable the claude CLI reads an API key from.
const billingKeyEnv = "ANTHROPIC_API_KEY"

// billingKeyUnavailablePrefix starts the error of an execution failed closed
// because the api_key mode key did not resolve.
const billingKeyUnavailablePrefix = "billing_key_unavailable"

// DefaultBillingKeyCommandTimeout bounds one api_key_command run.
const DefaultBillingKeyCommandTimeout = 10 * time.Second

// BillingKeyRetryInterval is how often the dispatcher re-runs the resolver
// while admission is paused for AdmissionPauseOwnerBillingKey.
const BillingKeyRetryInterval = 5 * time.Minute

// BillingConfig is executor.claude_code.billing (GH-6016).
type BillingConfig struct {
	// Mode is subscription (default) or api_key.
	Mode BillingMode `yaml:"mode,omitempty"`
	// APIKeyCommand is run through the shell at each admission in api_key
	// mode; its trimmed stdout is the key. Timeout 10s.
	APIKeyCommand string `yaml:"api_key_command,omitempty"`
	// APIKeyEnv names a daemon environment variable holding the key. Exactly
	// one of APIKeyCommand and APIKeyEnv is set in api_key mode.
	APIKeyEnv string `yaml:"api_key_env,omitempty"`
}

// EffectiveMode returns the configured mode, subscription when unset.
func (c *BillingConfig) EffectiveMode() BillingMode {
	if c == nil || c.Mode == "" {
		return BillingModeSubscription
	}
	return c.Mode
}

// BillingKeyError is why an api_key mode key could not be resolved. Its text
// starts with billing_key_unavailable and never carries the key value.
type BillingKeyError struct {
	Reason string
	// ExitCode is the api_key_command exit code; -1 when it did not run to an
	// exit (timeout, start failure) or no command is configured.
	ExitCode int
}

func (e *BillingKeyError) Error() string {
	return fmt.Sprintf("%s: %s (exit code %d)", billingKeyUnavailablePrefix, e.Reason, e.ExitCode)
}

// IsBillingKeyUnavailable reports whether err is a fail-closed api_key
// resolution error.
func IsBillingKeyUnavailable(err error) bool {
	var ke *BillingKeyError
	return errors.As(err, &ke)
}

// BillingCredential is the result of one resolution: the mode in force and the
// env entries to add to every model subprocess of one execution.
type BillingCredential struct {
	mu   sync.RWMutex
	mode BillingMode
	key  []byte
}

// Mode returns the mode in force. Safe on a nil receiver (subscription).
func (c *BillingCredential) Mode() BillingMode {
	if c == nil {
		return BillingModeSubscription
	}
	return c.mode
}

// MemoryMode returns Mode in the vocabulary of the billing_mode columns.
func (c *BillingCredential) MemoryMode() string {
	if c == nil {
		return memory.BillingModeUnknown
	}
	return memory.NormalizeBillingMode(string(c.mode))
}

// Env returns the entries to append to a model subprocess env: none in
// subscription mode or after Zero, ANTHROPIC_API_KEY=<key> in api_key mode.
func (c *BillingCredential) Env() []string {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.key) == 0 {
		return nil
	}
	return []string{billingKeyEnv + "=" + string(c.key)}
}

// Zero wipes the key. Called when the execution finishes; later spawns on the
// same context carry no key. Safe on a nil receiver.
func (c *BillingCredential) Zero() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.key {
		c.key[i] = 0
	}
	c.key = nil
}

type billingCredentialKey struct{}

// WithBillingCredential returns ctx carrying cred for modelSubprocessEnvFor.
func WithBillingCredential(ctx context.Context, cred *BillingCredential) context.Context {
	return context.WithValue(ctx, billingCredentialKey{}, cred)
}

// BillingCredentialFromContext returns the credential on ctx, or nil when none
// was resolved for it.
func BillingCredentialFromContext(ctx context.Context) *BillingCredential {
	if ctx == nil {
		return nil
	}
	cred, _ := ctx.Value(billingCredentialKey{}).(*BillingCredential)
	return cred
}

// BillingResolver resolves the credential of one execution or chat turn.
type BillingResolver struct {
	cfg     *BillingConfig
	getenv  func(string) string
	timeout time.Duration
	log     *slog.Logger
}

// NewBillingResolver builds a resolver for cfg; nil cfg means subscription.
func NewBillingResolver(cfg *BillingConfig) *BillingResolver {
	return &BillingResolver{
		cfg:     cfg,
		getenv:  os.Getenv,
		timeout: DefaultBillingKeyCommandTimeout,
		log:     slog.Default(),
	}
}

// Mode returns the configured mode. Safe on a nil receiver.
func (r *BillingResolver) Mode() BillingMode {
	if r == nil {
		return BillingModeSubscription
	}
	return r.cfg.EffectiveMode()
}

// Resolve returns the mode in force and its env entries. In api_key mode any
// failure (command exit non-zero, timeout, empty output, env var unset) is a
// *BillingKeyError; it never falls back to the subscription.
func (r *BillingResolver) Resolve(ctx context.Context) (*BillingCredential, error) {
	if r.Mode() != BillingModeAPIKey {
		return &BillingCredential{mode: BillingModeSubscription}, nil
	}
	key, err := r.resolveKey(ctx)
	if err != nil {
		return nil, err
	}
	return &BillingCredential{mode: BillingModeAPIKey, key: key}, nil
}

func (r *BillingResolver) resolveKey(ctx context.Context) ([]byte, error) {
	if name := r.cfg.APIKeyEnv; name != "" && r.cfg.APIKeyCommand == "" {
		v := strings.TrimSpace(r.getenv(name))
		if v == "" {
			return nil, &BillingKeyError{Reason: "api_key_env variable is unset or empty", ExitCode: -1}
		}
		return []byte(v), nil
	}
	if r.cfg.APIKeyCommand == "" {
		return nil, &BillingKeyError{Reason: "api_key mode has neither api_key_command nor api_key_env", ExitCode: -1}
	}

	cctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "sh", "-c", r.cfg.APIKeyCommand)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A grandchild holding the pipes open must not outlive the timeout.
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	defer func() {
		for i := range stdout.Bytes() {
			stdout.Bytes()[i] = 0
		}
	}()

	// Decision: stderr is logged at warn, stdout never is — it is the key.
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		if len(msg) > 500 {
			msg = msg[:500] + "..."
		}
		r.log.Warn("billing api_key_command wrote to stderr", slog.String("stderr", msg))
	}

	if runErr != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return nil, &BillingKeyError{Reason: fmt.Sprintf("api_key_command timed out after %s", r.timeout), ExitCode: -1}
		}
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			return nil, &BillingKeyError{Reason: "api_key_command failed", ExitCode: ee.ExitCode()}
		}
		return nil, &BillingKeyError{Reason: "api_key_command could not run", ExitCode: -1}
	}
	key := bytes.TrimSpace(stdout.Bytes())
	if len(key) == 0 {
		return nil, &BillingKeyError{Reason: "api_key_command printed no key", ExitCode: 0}
	}
	return append([]byte(nil), key...), nil
}

// prepareBilling makes sure ctx carries the billing credential of this run
// (GH-6016). The dispatcher's worker puts one on ctx; a run that did not come
// through it (pilot task, direct callers) resolves here and, in api_key mode,
// fails closed rather than running on the subscription. The returned release
// wipes a credential this call resolved.
func (r *Runner) prepareBilling(ctx context.Context, task *Task) (context.Context, func(), error) {
	ctx, cred, release, err := r.billingContext(ctx)
	if err != nil {
		return ctx, release, err
	}
	if task.billingMode == "" {
		task.billingMode = cred.MemoryMode()
	}
	return ctx, release, nil
}

// billingContext returns ctx carrying a billing credential: the one already on
// ctx, or a freshly resolved one whose release wipes it. A backend other than
// the claude CLI has no billing mode: the credential is nil and the mode
// unknown.
func (r *Runner) billingContext(ctx context.Context) (context.Context, *BillingCredential, func(), error) {
	noop := func() {}
	if r.config != nil && r.config.Type != "" && r.config.Type != BackendTypeClaudeCode {
		return ctx, nil, noop, nil
	}
	if cred := BillingCredentialFromContext(ctx); cred != nil {
		return ctx, cred, noop, nil
	}
	var cfg *BillingConfig
	if r.config != nil && r.config.ClaudeCode != nil {
		cfg = r.config.ClaudeCode.Billing
	}
	cred, err := NewBillingResolver(cfg).Resolve(ctx)
	if err != nil {
		return ctx, nil, noop, err
	}
	return WithBillingCredential(ctx, cred), cred, cred.Zero, nil
}
