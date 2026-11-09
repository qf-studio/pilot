// Package redact masks configured credential values in adapter error text.
//
// Adapter clients send their credential in a request header. A transport
// error, a response body that echoes the request, or a header dump can still
// carry it into an error that callers log or alert on. Clients pass the
// credential values they hold through Text or Error before returning.
package redact

import "strings"

// Mask replaces a credential value in redacted text.
const Mask = "<redacted>"

// Text returns s with every non-empty secret replaced by Mask.
func Text(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, Mask)
		}
	}
	return s
}

// redactedError reports a masked message and keeps the original error
// reachable through errors.Is and errors.As.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// Error returns err with every secret masked in its text. A nil err, or one
// whose text holds none of the secrets, is returned unchanged.
func Error(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	masked := Text(msg, secrets...)
	if masked == msg {
		return err
	}
	return &redactedError{msg: masked, err: err}
}
