package transcription

import (
	"context"
	"errors"
	"testing"
)

// fakeTranscriber is a controllable Transcriber stub for Service tests.
type fakeTranscriber struct {
	name      string
	available bool
	result    *Result
	err       error
}

func (f *fakeTranscriber) Transcribe(_ context.Context, _ string) (*Result, error) {
	return f.result, f.err
}
func (f *fakeTranscriber) Name() string    { return f.name }
func (f *fakeTranscriber) Available() bool { return f.available }

func TestDefaultConfig(t *testing.T) {
	if got := DefaultConfig().Backend; got != "auto" {
		t.Errorf("DefaultConfig().Backend = %q, want auto", got)
	}
}

func TestNewService(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{"nil config (no key)", nil, true},
		{"empty key", &Config{}, true},
		{"with key", &Config{OpenAIAPIKey: "test-openai-key"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, err := NewService(tt.config)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if svc.BackendName() != "whisper-api" {
				t.Errorf("BackendName() = %q, want whisper-api", svc.BackendName())
			}
			if !svc.Available() {
				t.Error("Available() = false, want true")
			}
		})
	}
}

func TestService_Transcribe_PrimarySuccess(t *testing.T) {
	want := &Result{Text: "ok"}
	svc := &Service{primary: &fakeTranscriber{name: "p", available: true, result: want}}
	got, err := svc.Transcribe(context.Background(), "x.ogg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("result = %+v, want %+v", got, want)
	}
}

func TestService_Transcribe_PrimaryFailsNoFallback(t *testing.T) {
	svc := &Service{primary: &fakeTranscriber{name: "p", err: errors.New("boom")}}
	_, err := svc.Transcribe(context.Background(), "x.ogg")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestService_Transcribe_FallbackUsedOnPrimaryFailure(t *testing.T) {
	want := &Result{Text: "fallback"}
	svc := &Service{
		primary:  &fakeTranscriber{name: "p", err: errors.New("boom")},
		fallback: &fakeTranscriber{name: "f", available: true, result: want},
	}
	got, err := svc.Transcribe(context.Background(), "x.ogg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("result = %+v, want fallback result", got)
	}
}

func TestService_Transcribe_BothFail(t *testing.T) {
	svc := &Service{
		primary:  &fakeTranscriber{name: "p", err: errors.New("primary boom")},
		fallback: &fakeTranscriber{name: "f", err: errors.New("fallback boom")},
	}
	_, err := svc.Transcribe(context.Background(), "x.ogg")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestService_BackendName_NoPrimary(t *testing.T) {
	svc := &Service{}
	if got := svc.BackendName(); got != "none" {
		t.Errorf("BackendName() = %q, want none", got)
	}
}
