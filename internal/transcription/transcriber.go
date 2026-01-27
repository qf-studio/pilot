package transcription

import (
	"context"
	"fmt"
)

// Result represents the result of a transcription
type Result struct {
	Text       string  // Transcribed text
	Language   string  // Detected language (ISO 639-1 code)
	Confidence float64 // Confidence score (0-1)
	Duration   float64 // Audio duration in seconds
}

// Transcriber is the interface for speech-to-text services
type Transcriber interface {
	// Transcribe converts audio to text
	// audioPath can be any format supported by Whisper API (ogg, mp3, wav, etc.)
	Transcribe(ctx context.Context, audioPath string) (*Result, error)

	// Name returns the name of the transcriber
	Name() string

	// Available checks if the transcriber is available (dependencies installed)
	Available() bool
}

// Config holds transcription configuration
type Config struct {
	Backend      string `yaml:"backend"`        // "whisper-api" or "auto"
	OpenAIAPIKey string `yaml:"openai_api_key"` // OpenAI API key for Whisper
}

// DefaultConfig returns default transcription configuration
func DefaultConfig() *Config {
	return &Config{
		Backend: "auto",
	}
}

// Service manages transcription backends
type Service struct {
	config   *Config
	primary  Transcriber
	fallback Transcriber
}

// NewService creates a new transcription service
func NewService(config *Config) (*Service, error) {
	if config == nil {
		config = DefaultConfig()
	}

	s := &Service{
		config: config,
	}

	// Initialize Whisper API backend
	if config.OpenAIAPIKey == "" {
		return nil, fmt.Errorf("OpenAI API key required for transcription (set openai_api_key in config)")
	}
	s.primary = NewWhisperAPI(config.OpenAIAPIKey)

	if s.primary == nil {
		return nil, fmt.Errorf("no transcription backend available")
	}

	return s, nil
}

// Transcribe transcribes audio from the given path
// Whisper API supports: flac, mp3, mp4, mpeg, mpga, m4a, ogg, wav, webm
func (s *Service) Transcribe(ctx context.Context, audioPath string) (*Result, error) {
	// Try primary transcriber (sends file directly to Whisper API)
	result, err := s.primary.Transcribe(ctx, audioPath)
	if err != nil {
		// Try fallback if available
		if s.fallback != nil {
			result, err = s.fallback.Transcribe(ctx, audioPath)
			if err != nil {
				return nil, fmt.Errorf("transcription failed (primary and fallback): %w", err)
			}
			return result, nil
		}
		return nil, fmt.Errorf("transcription failed: %w", err)
	}

	return result, nil
}

// Available returns true if at least one transcriber is available
func (s *Service) Available() bool {
	if s.primary != nil && s.primary.Available() {
		return true
	}
	if s.fallback != nil && s.fallback.Available() {
		return true
	}
	return false
}

// BackendName returns the name of the active backend
func (s *Service) BackendName() string {
	if s.primary != nil {
		return s.primary.Name()
	}
	return "none"
}
