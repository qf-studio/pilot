package transcription

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const whisperAPIURL = "https://api.openai.com/v1/audio/transcriptions"

// WhisperAPI implements transcription using OpenAI's Whisper API
type WhisperAPI struct {
	apiKey     string
	httpClient *http.Client
}

// NewWhisperAPI creates a new Whisper API transcriber
func NewWhisperAPI(apiKey string) *WhisperAPI {
	return &WhisperAPI{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Name returns the transcriber name
func (w *WhisperAPI) Name() string {
	return "whisper-api"
}

// Available checks if Whisper API is available
func (w *WhisperAPI) Available() bool {
	return w.apiKey != ""
}

// Transcribe transcribes audio using Whisper API
func (w *WhisperAPI) Transcribe(ctx context.Context, audioPath string) (*Result, error) {
	if !w.Available() {
		return nil, fmt.Errorf("whisper API not available (no API key)")
	}

	// Open the audio file
	file, err := os.Open(audioPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open audio file: %w", err)
	}
	defer func() { _ = file.Close() }()

	// Get file info for duration estimation
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat audio file: %w", err)
	}

	// Create multipart form
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	// Add the file
	part, err := writer.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return nil, fmt.Errorf("failed to create form file: %w", err)
	}

	if _, err := io.Copy(part, file); err != nil {
		return nil, fmt.Errorf("failed to copy file to form: %w", err)
	}

	// Add model parameter
	if err := writer.WriteField("model", "whisper-1"); err != nil {
		return nil, fmt.Errorf("failed to write model field: %w", err)
	}

	// Add response format for verbose JSON (includes language)
	if err := writer.WriteField("response_format", "verbose_json"); err != nil {
		return nil, fmt.Errorf("failed to write response_format field: %w", err)
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", whisperAPIURL, &requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+w.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send request
	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whisper API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	// Parse verbose JSON response
	var apiResp struct {
		Text     string  `json:"text"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
	}

	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse Whisper API response: %w", err)
	}

	// Estimate duration if not provided (16kHz mono WAV: ~32 bytes per sample)
	duration := apiResp.Duration
	if duration == 0 {
		// Rough estimate: 16kHz, 16-bit mono = 32000 bytes/second
		duration = float64(fileInfo.Size()) / 32000.0
	}

	return &Result{
		Text:       apiResp.Text,
		Language:   apiResp.Language,
		Confidence: 0.95, // Whisper doesn't provide confidence, but is generally reliable
		Duration:   duration,
	}, nil
}
