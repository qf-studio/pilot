package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RuntimeOverrideBillingMode is the runtime_overrides key of the billing mode
// override (GH-6052). Values: subscription or api_key.
const RuntimeOverrideBillingMode = "billing.mode"

// GetRuntimeOverride returns the override stored under key. until is the zero
// time when the override has no expiry; found is false when no row exists
// (the config decides).
func (s *Store) GetRuntimeOverride(key string) (value string, until time.Time, setBy string, setAt time.Time, found bool, err error) {
	var (
		untilN sql.NullTime
		setAtN sql.NullTime
	)
	row := s.db.QueryRow(`SELECT value, until, set_by, set_at FROM runtime_overrides WHERE key = ?`, key)
	if scanErr := row.Scan(&value, &untilN, &setBy, &setAtN); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return "", time.Time{}, "", time.Time{}, false, nil
		}
		return "", time.Time{}, "", time.Time{}, false, fmt.Errorf("GetRuntimeOverride: scan: %w", scanErr)
	}
	if untilN.Valid {
		until = untilN.Time.UTC()
	}
	if setAtN.Valid {
		setAt = setAtN.Time.UTC()
	}
	return value, until, setBy, setAt, true, nil
}

// SetRuntimeOverride stores value under key, replacing any previous row. A zero
// until stores NULL (no expiry). set_at is now.
func (s *Store) SetRuntimeOverride(key, value string, until time.Time, setBy string) error {
	var untilArg any
	if !until.IsZero() {
		untilArg = until.UTC()
	}
	return s.withRetry("SetRuntimeOverride", func() error {
		_, err := s.db.Exec(`
			INSERT INTO runtime_overrides (key, value, until, set_by, set_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				value = excluded.value,
				until = excluded.until,
				set_by = excluded.set_by,
				set_at = excluded.set_at
		`, key, value, untilArg, setBy, time.Now().UTC())
		return err
	})
}

// DeleteRuntimeOverride removes the override under key. A missing row is not an
// error.
func (s *Store) DeleteRuntimeOverride(key string) error {
	return s.withRetry("DeleteRuntimeOverride", func() error {
		_, err := s.db.Exec(`DELETE FROM runtime_overrides WHERE key = ?`, key)
		return err
	})
}
