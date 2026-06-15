package transcription

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rewriteTransport redirects every request to the test server, so we can
// exercise the Whisper HTTP/parse paths without hitting api.openai.com.
type rewriteTransport struct{ base *url.URL }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = rt.base.Scheme
	req.URL.Host = rt.base.Host
	return http.DefaultTransport.RoundTrip(req)
}

// writeDummyAudio creates a small file to stand in for an audio upload.
func writeDummyAudio(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clip.ogg")
	// 32000 bytes => ~1.0s under the size-based duration estimate.
	if err := os.WriteFile(path, make([]byte, 32000), 0o600); err != nil {
		t.Fatalf("write dummy audio: %v", err)
	}
	return path
}

func newTestWhisper(t *testing.T, handler http.HandlerFunc) *WhisperAPI {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	w := NewWhisperAPI("test-openai-key")
	w.httpClient = &http.Client{Transport: rewriteTransport{base: base}}
	return w
}

func TestWhisperAPI_NameAndAvailable(t *testing.T) {
	if got := NewWhisperAPI("test-openai-key").Name(); got != "whisper-api" {
		t.Errorf("Name() = %q, want whisper-api", got)
	}
	tests := []struct {
		name   string
		apiKey string
		want   bool
	}{
		{"with key", "test-openai-key", true},
		{"empty key", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewWhisperAPI(tt.apiKey).Available(); got != tt.want {
				t.Errorf("Available() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWhisperAPI_Transcribe_PreflightErrors(t *testing.T) {
	tests := []struct {
		name      string
		apiKey    string
		audioPath string
		wantErr   string
	}{
		{"no api key", "", writeDummyAudio(t), "not available"},
		{"missing file", "test-openai-key", filepath.Join(t.TempDir(), "nope.ogg"), "failed to open audio file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewWhisperAPI(tt.apiKey)
			_, err := w.Transcribe(context.Background(), tt.audioPath)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestWhisperAPI_Transcribe_Success(t *testing.T) {
	audio := writeDummyAudio(t)
	w := newTestWhisper(t, func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte(`{"text":"hello world","language":"en","duration":3.5}`))
	})

	res, err := w.Transcribe(context.Background(), audio)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Text != "hello world" || res.Language != "en" || res.Duration != 3.5 {
		t.Errorf("unexpected result: %+v", res)
	}
	if res.Confidence != 0.95 {
		t.Errorf("Confidence = %v, want 0.95", res.Confidence)
	}
}

func TestWhisperAPI_Transcribe_EmptyTranscriptEstimatesDuration(t *testing.T) {
	audio := writeDummyAudio(t) // 32000 bytes => ~1.0s estimate
	w := newTestWhisper(t, func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		// Empty text, duration omitted (0) -> falls back to size-based estimate.
		_, _ = rw.Write([]byte(`{"text":"","language":""}`))
	})

	res, err := w.Transcribe(context.Background(), audio)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Text != "" {
		t.Errorf("Text = %q, want empty", res.Text)
	}
	if res.Duration <= 0.9 || res.Duration >= 1.1 {
		t.Errorf("estimated Duration = %v, want ~1.0", res.Duration)
	}
}

func TestWhisperAPI_Transcribe_APIAndParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"http 401", http.StatusUnauthorized, `{"error":"bad key"}`, "whisper API error (status 401)"},
		{"http 500", http.StatusInternalServerError, `boom`, "whisper API error (status 500)"},
		{"malformed json", http.StatusOK, `{not json`, "failed to parse Whisper API response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			audio := writeDummyAudio(t)
			w := newTestWhisper(t, func(rw http.ResponseWriter, _ *http.Request) {
				rw.WriteHeader(tt.status)
				_, _ = rw.Write([]byte(tt.body))
			})
			_, err := w.Transcribe(context.Background(), audio)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}
