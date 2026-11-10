package replay

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

func TestRecorder_RedactsRegisteredSecretValue(t *testing.T) {
	const value = "zq-opaque-config-value-0042"
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	secretpatterns.RegisterSecretValues(value)

	tests := []struct {
		name string
		file string
	}{
		{"stream", "stream.jsonl"},
		{"summary", "summary.md"},
		{"metadata", "metadata.json"},
	}

	dir := t.TempDir()
	rec, err := NewRecorder("TASK-1", "/proj", dir)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	// An agent Read of config.yaml, as in the 2026-08-03 incident.
	if err := rec.RecordEvent(`{"type":"user","tool_use_result":{"content":"api_key: ` + value + `"}}`); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	rec.SetBranch("branch-" + value)
	if err := rec.Finish("completed"); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, rec.id, tt.file))
			if err != nil {
				t.Fatalf("read %s: %v", tt.file, err)
			}
			if strings.Contains(string(data), value) {
				t.Errorf("%s holds the registered value:\n%s", tt.file, data)
			}
		})
	}
	data, _ := os.ReadFile(filepath.Join(dir, rec.id, "stream.jsonl"))
	// json.Marshal writes the marker as \u003credacted\u003e.
	if !strings.Contains(string(data), `\u003credacted\u003e`) {
		t.Errorf("stream.jsonl lacks the redaction marker:\n%s", data)
	}
}

func TestRecorder_PrivateModes(t *testing.T) {
	dir := t.TempDir()
	rec, err := NewRecorder("TASK-2", "/proj", dir)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if err := rec.RecordEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/p/a.go"}}]}}`); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if err := rec.Finish("completed"); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	base := filepath.Join(dir, rec.id)
	tests := []struct {
		path string
		want os.FileMode
	}{
		{base, 0700},
		{filepath.Join(base, "diffs"), 0700},
		{filepath.Join(base, "stream.jsonl"), 0600},
		{filepath.Join(base, "summary.md"), 0600},
		{filepath.Join(base, "metadata.json"), 0600},
		{filepath.Join(base, "diffs", "changes.json"), 0600},
	}
	for _, tt := range tests {
		t.Run(filepath.Base(tt.path), func(t *testing.T) {
			info, err := os.Stat(tt.path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if got := info.Mode().Perm(); got != tt.want {
				t.Errorf("mode = %o, want %o", got, tt.want)
			}
		})
	}
}

// A value with a quote, a backslash or a newline is JSON-escaped inside the
// agent's stream line, and escaped again when the event wraps that line in
// StreamEvent.Raw (GH-6266).
func TestRecorder_RedactsEscapedValueInDoubleEncodedRaw(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"double quote", `pa"ss-opaque-value-0042`},
		{"backslash", `pa\ss-opaque-value-0042`},
		{"newline", "pass-opaque\nvalue-0042"},
		{"all three", "pa\"ss\\word\nopaque-0042"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secretpatterns.ResetSecretValuesForTest()
			t.Cleanup(secretpatterns.ResetSecretValuesForTest)
			secretpatterns.RegisterSecretValues(tt.value)

			dir := t.TempDir()
			rec, err := NewRecorder("TASK-3", "/proj", dir)
			if err != nil {
				t.Fatalf("NewRecorder: %v", err)
			}
			enc, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			line := `{"type":"user","tool_use_result":{"content":` + string(enc) + `}}`
			if err := rec.RecordEvent(line); err != nil {
				t.Fatalf("RecordEvent: %v", err)
			}
			if err := rec.Finish("completed"); err != nil {
				t.Fatalf("Finish: %v", err)
			}

			f, err := os.Open(filepath.Join(dir, rec.id, "stream.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			sc := bufio.NewScanner(f)
			if !sc.Scan() {
				t.Fatal("empty stream file")
			}
			var ev struct {
				Raw string `json:"raw"`
			}
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				t.Fatalf("decode event: %v", err)
			}
			if strings.Contains(ev.Raw, tt.value) || strings.Contains(ev.Raw, string(enc[1:len(enc)-1])) {
				t.Errorf("decoded raw holds the value: %s", ev.Raw)
			}
			if !strings.Contains(ev.Raw, "<redacted>") {
				t.Errorf("decoded raw lacks <redacted>: %s", ev.Raw)
			}
			var inner map[string]any
			if err := json.Unmarshal([]byte(ev.Raw), &inner); err != nil {
				t.Errorf("redacted raw is no longer valid JSON: %v\n%s", err, ev.Raw)
			}
		})
	}
}

func TestRecorder_RedactsDiffsFile(t *testing.T) {
	const value = "zq-opaque-config-value-0042"
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	secretpatterns.RegisterSecretValues(value)

	dir := t.TempDir()
	rec, err := NewRecorder("TASK-4", "/proj", dir)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	rec.trackFileChange(&ParsedEvent{FilePath: "/proj/" + value + ".txt", FileOperation: "modify"})
	if err := rec.Finish("completed"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, rec.id, "diffs", "changes.json"))
	if err != nil {
		t.Fatalf("read changes.json: %v", err)
	}
	if strings.Contains(string(data), value) || !strings.Contains(string(data), "<redacted>") {
		t.Errorf("changes.json = %s", data)
	}
}

func TestRecorder_TightensExistingDirModes(t *testing.T) {
	base := filepath.Join(t.TempDir(), "recordings")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	rec, err := NewRecorder("TASK-5", "/proj", base)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if err := rec.Finish("completed"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{base, filepath.Join(base, rec.id)} {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("%s mode = %o, want 700", d, got)
		}
	}
}
