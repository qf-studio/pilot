package transcription

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// getPythonPath returns the path to Python, preferring ~/.pilot/venv if it exists
func getPythonPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		venvPython := filepath.Join(home, ".pilot", "venv", "bin", "python3")
		if _, err := os.Stat(venvPython); err == nil {
			return venvPython
		}
	}
	return "python3"
}

// SenseVoice implements transcription using FunASR SenseVoice model
type SenseVoice struct {
	scriptPath string
	available  *bool // Cached availability check
}

// NewSenseVoice creates a new SenseVoice transcriber
func NewSenseVoice(scriptPath string) *SenseVoice {
	return &SenseVoice{
		scriptPath: scriptPath,
	}
}

// Name returns the transcriber name
func (s *SenseVoice) Name() string {
	return "sensevoice"
}

// Available checks if SenseVoice is available
func (s *SenseVoice) Available() bool {
	if s.available != nil {
		return *s.available
	}

	// Check if Python and funasr are available
	cmd := exec.Command(getPythonPath(), "-c", "import funasr; print('ok')")
	output, err := cmd.CombinedOutput()
	available := err == nil && strings.TrimSpace(string(output)) == "ok"
	s.available = &available
	return available
}

// Transcribe transcribes audio using SenseVoice
func (s *SenseVoice) Transcribe(ctx context.Context, audioPath string) (*Result, error) {
	if !s.Available() {
		return nil, fmt.Errorf("SenseVoice not available (funasr not installed)")
	}

	// Get script path - use bundled script or custom path
	script := s.scriptPath
	if script == "" {
		// Look for bundled script in expected locations
		candidates := []string{
			filepath.Join(filepath.Dir(os.Args[0]), "scripts", "sensevoice_transcribe.py"),
			filepath.Join(filepath.Dir(os.Args[0]), "..", "scripts", "sensevoice_transcribe.py"),
			"scripts/sensevoice_transcribe.py",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				script = c
				break
			}
		}
	}

	// If no bundled script, use inline Python
	if script == "" {
		return s.transcribeInline(ctx, audioPath)
	}

	// Run the script
	cmd := exec.CommandContext(ctx, getPythonPath(), script, audioPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("SenseVoice failed: %w\nOutput: %s", err, string(output))
	}

	// Parse JSON output
	var result struct {
		Text       string  `json:"text"`
		Language   string  `json:"language"`
		Confidence float64 `json:"confidence"`
		Duration   float64 `json:"duration"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse SenseVoice output: %w\nOutput: %s", err, string(output))
	}

	return &Result{
		Text:       result.Text,
		Language:   result.Language,
		Confidence: result.Confidence,
		Duration:   result.Duration,
	}, nil
}

// transcribeInline runs SenseVoice using inline Python code
func (s *SenseVoice) transcribeInline(ctx context.Context, audioPath string) (*Result, error) {
	pythonCode := fmt.Sprintf(`
import json
import sys

try:
    from funasr import AutoModel
    import torchaudio

    # Load audio to get duration
    waveform, sample_rate = torchaudio.load(%q)
    duration = waveform.shape[1] / sample_rate

    # Load model (cached after first load)
    model = AutoModel(model="iic/SenseVoiceSmall", trust_remote_code=True)

    # Transcribe
    result = model.generate(input=%q)

    if result and len(result) > 0:
        text = result[0].get("text", "")
        # SenseVoice returns language tags like <|en|> at start
        language = "unknown"
        if text.startswith("<|") and "|>" in text:
            lang_end = text.index("|>") + 2
            lang_tag = text[2:lang_end-2]
            language = lang_tag
            text = text[lang_end:].strip()

        output = {
            "text": text,
            "language": language,
            "confidence": 0.9,  # SenseVoice doesn't provide confidence
            "duration": float(duration)
        }
        print(json.dumps(output))
    else:
        print(json.dumps({"error": "No transcription result"}))
        sys.exit(1)
except Exception as e:
    print(json.dumps({"error": str(e)}))
    sys.exit(1)
`, audioPath, audioPath)

	cmd := exec.CommandContext(ctx, getPythonPath(), "-c", pythonCode)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("SenseVoice inline failed: %w\nOutput: %s", err, string(output))
	}

	// Parse JSON output
	var result struct {
		Text       string  `json:"text"`
		Language   string  `json:"language"`
		Confidence float64 `json:"confidence"`
		Duration   float64 `json:"duration"`
		Error      string  `json:"error,omitempty"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse SenseVoice output: %w\nOutput: %s", err, string(output))
	}

	if result.Error != "" {
		return nil, fmt.Errorf("SenseVoice error: %s", result.Error)
	}

	return &Result{
		Text:       result.Text,
		Language:   result.Language,
		Confidence: result.Confidence,
		Duration:   result.Duration,
	}, nil
}
