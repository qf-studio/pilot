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
	// audioPath should be a path to a WAV file (16kHz, mono)
	Transcribe(ctx context.Context, audioPath string) (*Result, error)

	// Name returns the name of the transcriber
	Name() string

	// Available checks if the transcriber is available (dependencies installed)
	Available() bool
}

// Config holds transcription configuration
type Config struct {
	Backend       string `yaml:"backend"`        // "sensevoice", "whisper-api", "auto"
	OpenAIAPIKey  string `yaml:"openai_api_key"` // For Whisper API fallback
	SenseVoiceBin string `yaml:"sensevoice_bin"` // Path to SenseVoice Python script
	FFmpegPath    string `yaml:"ffmpeg_path"`    // Path to ffmpeg binary
}

// DefaultConfig returns default transcription configuration
func DefaultConfig() *Config {
	return &Config{
		Backend:       "auto",
		FFmpegPath:    "ffmpeg",
		SenseVoiceBin: "", // Will use bundled script
	}
}

// Service manages transcription backends
type Service struct {
	config     *Config
	primary    Transcriber
	fallback   Transcriber
	ffmpegPath string
}

// NewService creates a new transcription service
func NewService(config *Config) (*Service, error) {
	if config == nil {
		config = DefaultConfig()
	}

	s := &Service{
		config:     config,
		ffmpegPath: config.FFmpegPath,
	}

	if s.ffmpegPath == "" {
		s.ffmpegPath = "ffmpeg"
	}

	// Initialize backends based on config
	switch config.Backend {
	case "sensevoice":
		s.primary = NewSenseVoice(config.SenseVoiceBin)
		if config.OpenAIAPIKey != "" {
			s.fallback = NewWhisperAPI(config.OpenAIAPIKey)
		}
	case "whisper-api":
		if config.OpenAIAPIKey == "" {
			return nil, fmt.Errorf("OpenAI API key required for whisper-api backend")
		}
		s.primary = NewWhisperAPI(config.OpenAIAPIKey)
	case "auto":
		// Try SenseVoice first, fall back to Whisper API
		sv := NewSenseVoice(config.SenseVoiceBin)
		if sv.Available() {
			s.primary = sv
		}
		if config.OpenAIAPIKey != "" {
			wa := NewWhisperAPI(config.OpenAIAPIKey)
			if s.primary == nil {
				s.primary = wa
			} else {
				s.fallback = wa
			}
		}
	default:
		return nil, fmt.Errorf("unknown transcription backend: %s", config.Backend)
	}

	if s.primary == nil {
		return nil, fmt.Errorf("no transcription backend available")
	}

	return s, nil
}

// Transcribe transcribes audio from the given path
// The path can be any format supported by ffmpeg; it will be converted to WAV
func (s *Service) Transcribe(ctx context.Context, audioPath string) (*Result, error) {
	// Convert audio to WAV format for transcription
	wavPath, err := s.convertToWav(ctx, audioPath)
	if err != nil {
		return nil, fmt.Errorf("failed to convert audio: %w", err)
	}

	// Try primary transcriber
	result, err := s.primary.Transcribe(ctx, wavPath)
	if err != nil {
		// Try fallback if available
		if s.fallback != nil {
			result, err = s.fallback.Transcribe(ctx, wavPath)
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
