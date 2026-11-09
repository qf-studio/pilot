package replay

import (
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
	if !strings.Contains(string(data), "<redacted>") {
		t.Errorf("stream.jsonl lacks <redacted>:\n%s", data)
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
