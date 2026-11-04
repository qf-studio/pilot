package executor

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// Sources of the billing mode in force (GH-6052). config is reported when no
// override is in force; fallback is reserved for the usage-limit fallback.
const (
	BillingModeSourceCLI      = "cli"
	BillingModeSourceGateway  = "gateway"
	BillingModeSourceFallback = "fallback"
	BillingModeSourceConfig   = "config"
)

// billingOverrideSetByExpiry is the set_by of the alert raised when an
// override reaches its until.
const billingOverrideSetByExpiry = "expiry"

// ErrBillingOverrideInvalid marks a SetBillingOverride request that can never
// succeed as written (unknown mode, until in the past, api_key without a key
// source in the config). Callers map it to a client error.
var ErrBillingOverrideInvalid = errors.New("invalid billing mode override")

// BillingOverrideStatus is the billing mode in force and why.
type BillingOverrideStatus struct {
	// Mode is the mode in force: the override's, else the config's
	// ("unset" when the config leaves billing.mode out).
	Mode string
	// Source is cli, gateway, fallback, or config when no override is in force.
	Source string
	// Until is when the override expires; zero when it has no expiry or no
	// override is in force.
	Until time.Time
	// ConfigMode is what the config alone decides.
	ConfigMode string
}

// billingOverrideState is the runtime override in force.
type billingOverrideState struct {
	mode   BillingMode
	source string
	setBy  string
	until  time.Time
}

// billingOverrideSourceOf is the source reported for a set_by: the part before
// the first colon, so "gateway:alice" is the gateway.
func billingOverrideSourceOf(setBy string) string {
	for i := 0; i < len(setBy); i++ {
		if setBy[i] == ':' {
			return setBy[:i]
		}
	}
	return setBy
}

// billingOverrideSource is the resolver's override source.
func (d *Dispatcher) billingOverrideSource() (BillingMode, string, time.Time, bool) {
	d.billingOverrideMu.Lock()
	defer d.billingOverrideMu.Unlock()
	st := d.billingOverrideState
	if st == nil {
		return "", "", time.Time{}, false
	}
	return st.mode, st.source, st.until, true
}

// BillingOverride reports the billing mode in force, its source and expiry.
func (d *Dispatcher) BillingOverride() BillingOverrideStatus {
	configMode := d.billingConfig().EffectiveMode()
	mode, source, until, ok := d.billingOverrideSource()
	if !ok {
		return BillingOverrideStatus{Mode: string(configMode), Source: BillingModeSourceConfig, ConfigMode: string(configMode)}
	}
	return BillingOverrideStatus{Mode: string(mode), Source: source, Until: until, ConfigMode: string(configMode)}
}

// SetBillingObserver registers fn to be told the billing mode in force after
// every switch: SetBillingOverride, ClearBillingOverride that cleared one, and
// an expiry. It is not called for the boot load; the caller reads
// BillingOverride() for the initial state. fn runs outside the override lock.
func (d *Dispatcher) SetBillingObserver(fn func(BillingOverrideStatus)) {
	d.billingOverrideMu.Lock()
	d.billingObserver = fn
	d.billingOverrideMu.Unlock()
}

func (d *Dispatcher) notifyBillingObserver() {
	d.billingOverrideMu.Lock()
	fn := d.billingObserver
	d.billingOverrideMu.Unlock()
	if fn != nil {
		fn(d.BillingOverride())
	}
}

// SetBillingOverride makes mode (subscription or api_key) the billing mode of
// every execution admitted from now on, until the zero or future until, and
// persists it so it survives a restart. A running execution keeps the
// credential it froze at admission. A later call replaces the override and its
// expiry timer.
func (d *Dispatcher) SetBillingOverride(mode string, until time.Time, setBy string) error {
	m := BillingMode(mode)
	if m != BillingModeSubscription && m != BillingModeAPIKey {
		return fmt.Errorf("%w: mode %q, accepted values are subscription, api_key", ErrBillingOverrideInvalid, mode)
	}
	clock := d.pauseClockOrReal()
	now := clock.Now()
	if !until.IsZero() {
		until = until.UTC()
		if !until.After(now) {
			return fmt.Errorf("%w: until %s is not in the future", ErrBillingOverrideInvalid, until.Format(time.RFC3339))
		}
	}
	if cfg := d.billingConfig(); m == BillingModeAPIKey && (cfg == nil || (cfg.APIKeyCommand == "" && cfg.APIKeyEnv == "")) {
		return fmt.Errorf("%w: api_key needs api_key_command or api_key_env in executor.claude_code.billing", ErrBillingOverrideInvalid)
	}
	if setBy == "" {
		setBy = "unknown"
	}
	source := billingOverrideSourceOf(setBy)

	d.billingOverrideMu.Lock()
	if d.store != nil {
		if err := d.store.SetRuntimeOverride(memory.RuntimeOverrideBillingMode, mode, until, setBy); err != nil {
			d.billingOverrideMu.Unlock()
			return fmt.Errorf("persist billing mode override: %w", err)
		}
	}
	from := d.effectiveBillingModeLocked()
	d.billingOverrideState = &billingOverrideState{mode: m, source: source, setBy: setBy, until: until}
	d.armBillingOverrideTimerLocked(clock, now, until)
	setProcessBillingPinned(true)
	SetProcessBillingMode(m)
	d.billingOverrideMu.Unlock()

	d.logBillingModeChange(from, m, until, source, setBy)
	d.emitBillingModeChanged(from, m, until, source, setBy)
	if m != BillingModeAPIKey {
		d.releaseBillingKeyHold()
	}
	d.notifyBillingObserver()
	return nil
}

// ClearBillingOverride removes the override so the config decides again. With
// no override in force it is a no-op.
func (d *Dispatcher) ClearBillingOverride(setBy string) error {
	if setBy == "" {
		setBy = "unknown"
	}
	d.billingOverrideMu.Lock()
	if d.billingOverrideState == nil {
		d.billingOverrideMu.Unlock()
		return nil
	}
	if d.store != nil {
		if err := d.store.DeleteRuntimeOverride(memory.RuntimeOverrideBillingMode); err != nil {
			d.billingOverrideMu.Unlock()
			return fmt.Errorf("delete billing mode override: %w", err)
		}
	}
	from, to := d.revertBillingOverrideLocked()
	d.billingOverrideMu.Unlock()

	d.finishBillingOverrideRevert(from, to, setBy)
	return nil
}

// expireBillingOverride is the expiry timer callback; a stale generation (the
// override was replaced or cleared since) is ignored.
func (d *Dispatcher) expireBillingOverride(gen uint64) {
	d.billingOverrideMu.Lock()
	if gen != d.billingOverrideGen || d.billingOverrideState == nil {
		d.billingOverrideMu.Unlock()
		return
	}
	if d.store != nil {
		// A row that outlives its override is dropped by the boot load.
		if err := d.store.DeleteRuntimeOverride(memory.RuntimeOverrideBillingMode); err != nil {
			d.log.Warn("could not delete the expired billing mode override", slog.Any("error", err))
		}
	}
	from, to := d.revertBillingOverrideLocked()
	d.billingOverrideMu.Unlock()

	d.finishBillingOverrideRevert(from, to, billingOverrideSetByExpiry)
}

// loadBillingOverride restores the persisted override at boot: dropped when
// its until is past, otherwise applied with the remaining timer armed.
func (d *Dispatcher) loadBillingOverride() {
	if d.store == nil {
		return
	}
	value, until, setBy, _, found, err := d.store.GetRuntimeOverride(memory.RuntimeOverrideBillingMode)
	if err != nil {
		d.log.Warn("could not read the billing mode override, the config decides", slog.Any("error", err))
		return
	}
	if !found {
		return
	}
	m := BillingMode(value)
	clock := d.pauseClockOrReal()
	now := clock.Now()
	valid := m == BillingModeSubscription || m == BillingModeAPIKey
	if !valid || (!until.IsZero() && !until.After(now)) {
		d.log.Info("dropping the stored billing mode override",
			slog.String("mode", value), slog.Time("until", until), slog.Bool("valid_mode", valid))
		if delErr := d.store.DeleteRuntimeOverride(memory.RuntimeOverrideBillingMode); delErr != nil {
			d.log.Warn("could not delete the stale billing mode override", slog.Any("error", delErr))
		}
		return
	}
	source := billingOverrideSourceOf(setBy)

	d.billingOverrideMu.Lock()
	from := d.effectiveBillingModeLocked()
	d.billingOverrideState = &billingOverrideState{mode: m, source: source, setBy: setBy, until: until}
	d.armBillingOverrideTimerLocked(clock, now, until)
	setProcessBillingPinned(true)
	SetProcessBillingMode(m)
	d.billingOverrideMu.Unlock()

	d.logBillingModeChange(from, m, until, source, setBy)
	if m != BillingModeAPIKey {
		d.releaseBillingKeyHold()
	}
}

// effectiveBillingModeLocked is the mode in force; billingOverrideMu is held.
func (d *Dispatcher) effectiveBillingModeLocked() BillingMode {
	if st := d.billingOverrideState; st != nil {
		return st.mode
	}
	return d.billingConfig().EffectiveMode()
}

// armBillingOverrideTimerLocked replaces the expiry timer: it stops the pending
// one, bumps the generation so a callback already in flight is a no-op, and arms
// a new one when until is set. billingOverrideMu is held.
func (d *Dispatcher) armBillingOverrideTimerLocked(clock pauseClock, now, until time.Time) {
	if d.billingOverrideTimer != nil {
		d.billingOverrideTimer.Stop()
		d.billingOverrideTimer = nil
	}
	d.billingOverrideGen++
	if until.IsZero() {
		return
	}
	gen := d.billingOverrideGen
	d.billingOverrideTimer = clock.AfterFunc(until.Sub(now), func() { d.expireBillingOverride(gen) })
}

// revertBillingOverrideLocked drops the in-memory override and publishes the
// config's mode; it returns the modes before and after. billingOverrideMu is held
// and an override is in force.
func (d *Dispatcher) revertBillingOverrideLocked() (from, to BillingMode) {
	from = d.billingOverrideState.mode
	d.billingOverrideState = nil
	if d.billingOverrideTimer != nil {
		d.billingOverrideTimer.Stop()
		d.billingOverrideTimer = nil
	}
	d.billingOverrideGen++
	to = d.billingConfig().EffectiveMode()
	setProcessBillingPinned(false)
	SetProcessBillingMode(to)
	return from, to
}

func (d *Dispatcher) finishBillingOverrideRevert(from, to BillingMode, setBy string) {
	d.logBillingModeChange(from, to, time.Time{}, BillingModeSourceConfig, setBy)
	d.emitBillingModeChanged(from, to, time.Time{}, BillingModeSourceConfig, setBy)
	if to != BillingModeAPIKey {
		d.releaseBillingKeyHold()
	}
	d.notifyBillingObserver()
}

func (d *Dispatcher) logBillingModeChange(from, to BillingMode, until time.Time, source, setBy string) {
	attrs := []any{
		slog.String("from", string(from)),
		slog.String("to", string(to)),
		slog.String("source", source),
		slog.String("set_by", setBy),
	}
	if !until.IsZero() {
		attrs = append(attrs, slog.Time("until", until))
	}
	d.log.Info("billing mode override changed", attrs...)
}

func (d *Dispatcher) emitBillingModeChanged(from, to BillingMode, until time.Time, source, setBy string) {
	if d.runner == nil || d.runner.AlertProcessor() == nil {
		return
	}
	untilStr := ""
	if !until.IsZero() {
		untilStr = until.Format(time.RFC3339)
	}
	d.runner.AlertProcessor().ProcessEvent(AlertEvent{
		Type: AlertEventTypeBillingModeChanged,
		Metadata: map[string]string{
			"from":   string(from),
			"to":     string(to),
			"until":  untilStr,
			"source": source,
			"set_by": setBy,
		},
		Timestamp: d.pauseClockOrReal().Now(),
	})
}

// stopBillingOverrideTimer cancels the pending expiry on Stop and releases the
// process-wide pin; the stored row stays for the next boot.
func (d *Dispatcher) stopBillingOverrideTimer() {
	d.billingOverrideMu.Lock()
	defer d.billingOverrideMu.Unlock()
	if d.billingOverrideTimer != nil {
		d.billingOverrideTimer.Stop()
		d.billingOverrideTimer = nil
	}
	d.billingOverrideGen++
	if d.billingOverrideState != nil {
		d.billingOverrideState = nil
		setProcessBillingPinned(false)
		SetProcessBillingMode(d.billingConfig().EffectiveMode())
	}
}
