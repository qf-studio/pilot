package redact

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestText(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		secrets []string
		want    string
	}{
		{"single", "token=abc!", []string{"abc"}, "token=<redacted>!"},
		{"repeated", "abc abc", []string{"abc"}, "<redacted> <redacted>"},
		{"several secrets", "a b", []string{"a", "b"}, "<redacted> <redacted>"},
		{"empty secret ignored", "abc", []string{""}, "abc"},
		{"no secrets", "abc", nil, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Text(tt.in, tt.secrets...); got != tt.want {
				t.Errorf("Text() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestError(t *testing.T) {
	if Error(nil, "x") != nil {
		t.Error("nil error must stay nil")
	}

	clean := errors.New("boom")
	if got := Error(clean, "secret"); got != clean {
		t.Errorf("error without secret was replaced: %v", got)
	}

	base := fmt.Errorf("call: %w", context.Canceled)
	leaky := fmt.Errorf("Get https://h/secret: %w", base)
	got := Error(leaky, "secret")
	if got.Error() != "Get https://h/<redacted>: call: context canceled" {
		t.Errorf("message = %q", got.Error())
	}
	if !errors.Is(got, context.Canceled) {
		t.Error("masked error lost its chain")
	}
}
