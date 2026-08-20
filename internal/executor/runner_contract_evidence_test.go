package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/webhooks"
)

// contractFileBackend simulates a Claude Code invocation that writes and
// commits a single file — used to put a diff-touching-a-contract-file shape
// in front of the Contract Evidence gate without shelling out to a real
// backend.
type contractFileBackend struct {
	path    string
	content string
}

func (b *contractFileBackend) Name() string      { return "contract-file-backend" }
func (b *contractFileBackend) IsAvailable() bool { return true }

func (b *contractFileBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	full := filepath.Join(opts.ProjectPath, b.path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(full, []byte(b.content), 0o644); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "touch contract file"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = opts.ProjectPath
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git %v: %v (%s)", args, err, out)
		}
	}
	return &BackendResult{Success: true, Output: "done"}, nil
}

// newContractGateTestRunner sets up a Runner against a real local+origin
// repo pair (so GetDiffAgainstOrigin behaves exactly as in production),
// wired to skip preflight/recording noise and with CreatePR=false so
// execution never reaches the real `gh pr create` path — the Contract
// Evidence gate is spliced before that branch, so CreatePR=false isolates
// it as the only extra logic under test.
func newContractGateTestRunner(t *testing.T, backend Backend) (*Runner, *Task) {
	t.Helper()
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	t.Cleanup(func() {
		_ = os.RemoveAll(localRepo)
		_ = os.RemoveAll(remoteRepo)
	})

	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: false}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)

	task := &Task{
		ID:          "GH-5012-contract-gate",
		Title:       "contract evidence gate test",
		Description: "synthetic task exercising the contract evidence gate",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-5012-contract-gate",
		CreatePR:    false,
	}

	return runner, task
}

func contractGateExecCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func specVersionProducerContent() string {
	return strings.Join([]string{
		"package instances",         // 1
		"",                          // 2
		"type InstanceDTO struct {", // 3
		"\tConfigGeneration int `json:\"specVersion\"`", // 4
		"}", // 5
	}, "\n")
}

func TestRunner_ContractEvidence_NoLookupConfigured_NoOp(t *testing.T) {
	backend := &contractFileBackend{
		path:    "src/lib/api/types.ts",
		content: "export interface Instance {\n  specVersion: number;\n}\n",
	}
	runner, task := newContractGateTestRunner(t, backend)

	fetcher := &fakeContractContentFetcher{content: map[string]string{
		"qf-studio/pilot-console/internal/instances/handlers.go": specVersionProducerContent(),
	}}
	runner.SetContractContentFetcher(fetcher)
	// Deliberately never call SetContractDependencyLookup: the gate must be
	// a complete no-op (r.contractDependencyLookup == nil), matching
	// existing behavior for every project that hasn't opted in.

	result, err := runner.Execute(contractGateExecCtx(t), task)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected success with no lookup configured, got %+v", result)
	}
	if fetcher.calls != 0 {
		t.Errorf("expected 0 fetch calls with no lookup configured, got %d", fetcher.calls)
	}
	if result.ContractEvidence != nil {
		t.Errorf("expected nil ContractEvidence outcome when the gate never ran, got %+v", result.ContractEvidence)
	}
}

func TestRunner_ContractEvidence_NoDependenciesConfigured_NoOp(t *testing.T) {
	backend := &contractFileBackend{
		path:    "src/lib/api/types.ts",
		content: "export interface Instance {\n  specVersion: number;\n}\n",
	}
	runner, task := newContractGateTestRunner(t, backend)

	fetcher := &fakeContractContentFetcher{content: map[string]string{
		"qf-studio/pilot-console/internal/instances/handlers.go": specVersionProducerContent(),
	}}
	runner.SetContractContentFetcher(fetcher)
	runner.SetContractDependencyLookup(func(projectPath string) []ContractDependency {
		return nil // configured, but this project declares no dependencies
	})

	result, err := runner.Execute(contractGateExecCtx(t), task)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected success with zero configured dependencies, got %+v", result)
	}
	if fetcher.calls != 0 {
		t.Errorf("expected 0 fetch calls with zero configured dependencies, got %d", fetcher.calls)
	}
}

// webhookRecorder is a minimal httptest-backed webhooks.Manager harness:
// asserting a real Dispatch() round-trip (not an internal mock) proves the
// failure path actually calls dispatchWebhook with the documented event
// type, end to end.
type webhookRecorder struct {
	mu      sync.Mutex
	events  []string
	server  *httptest.Server
	manager *webhooks.Manager
}

func newWebhookRecorder() *webhookRecorder {
	wr := &webhookRecorder{}
	wr.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wr.mu.Lock()
		wr.events = append(wr.events, r.Header.Get("X-Pilot-Event"))
		wr.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	wr.manager = webhooks.NewManager(&webhooks.Config{
		Enabled: true,
		Endpoints: []*webhooks.EndpointConfig{
			{ID: "test", Name: "test", URL: wr.server.URL, Enabled: true},
		},
	}, nil)
	return wr
}

func (wr *webhookRecorder) close() { wr.server.Close() }

func (wr *webhookRecorder) received(eventType string) bool {
	wr.mu.Lock()
	defer wr.mu.Unlock()
	for _, e := range wr.events {
		if e == eventType {
			return true
		}
	}
	return false
}

func TestRunner_ContractEvidence_ZeroEvidenceFails(t *testing.T) {
	backend := &contractFileBackend{
		path:    "src/lib/api/types.ts",
		content: "export interface Instance {\n  specVersion: number;\n}\n",
	}
	runner, task := newContractGateTestRunner(t, backend)

	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.ts"}}}
	runner.SetContractDependencyLookup(func(projectPath string) []ContractDependency { return deps })
	runner.contractEvidenceFetchFn = func(ctx context.Context, dir string, fields []string) ([]ContractEvidence, error) {
		return nil, nil // zero evidence entries
	}

	alerts := &fakeAlertProcessor{}
	runner.SetAlertProcessor(alerts)
	wr := newWebhookRecorder()
	defer wr.close()
	runner.SetWebhookManager(wr.manager)

	result, err := runner.Execute(contractGateExecCtx(t), task)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected failure for zero evidence, got %+v", result)
	}
	if result.Error == "" {
		t.Errorf("expected non-empty result.Error")
	}
	if result.ContractEvidence == nil || result.ContractEvidence.Passed {
		t.Errorf("expected a failed ContractEvidence outcome, got %+v", result.ContractEvidence)
	}

	var sawTaskFailed bool
	for _, e := range alerts.events {
		if e.Type == AlertEventTypeTaskFailed {
			sawTaskFailed = true
		}
	}
	if !sawTaskFailed {
		t.Errorf("expected an AlertEventTypeTaskFailed event, got %+v", alerts.events)
	}
	if !wr.received(string(webhooks.EventTaskFailed)) {
		t.Errorf("expected a dispatched %s webhook, got events %v", webhooks.EventTaskFailed, wr.events)
	}
}

func TestRunner_ContractEvidence_ZeroEvidenceFails_RecordsFailedFinish(t *testing.T) {
	backend := &contractFileBackend{
		path:    "src/lib/api/types.ts",
		content: "export interface Instance {\n  specVersion: number;\n}\n",
	}
	runner, task := newContractGateTestRunner(t, backend)

	recordingsDir := t.TempDir()
	runner.SetRecordingsPath(recordingsDir)
	runner.SetRecordingEnabled(true)

	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.ts"}}}
	runner.SetContractDependencyLookup(func(projectPath string) []ContractDependency { return deps })
	runner.contractEvidenceFetchFn = func(ctx context.Context, dir string, fields []string) ([]ContractEvidence, error) {
		return nil, nil
	}

	result, err := runner.Execute(contractGateExecCtx(t), task)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected failure, got %+v", result)
	}

	entries, err := os.ReadDir(recordingsDir)
	if err != nil {
		t.Fatalf("ReadDir(recordingsDir): %v", err)
	}
	var foundFailedStatus bool
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(recordingsDir, e.Name(), "metadata.json"))
		if readErr != nil {
			continue
		}
		var meta struct {
			Status string `json:"status"`
		}
		if jsonErr := json.Unmarshal(data, &meta); jsonErr != nil {
			t.Fatalf("unmarshal metadata.json for %s: %v", e.Name(), jsonErr)
		}
		if meta.Status == "failed" {
			foundFailedStatus = true
		}
	}
	if !foundFailedStatus {
		t.Errorf("expected a recording with status=failed under %s, entries=%v", recordingsDir, entries)
	}
}

func TestRunner_ContractEvidence_IrrelevantCitationFails(t *testing.T) {
	backend := &contractFileBackend{
		path:    "src/lib/api/types.ts",
		content: "export interface Instance {\n  specVersion: number;\n}\n",
	}
	runner, task := newContractGateTestRunner(t, backend)

	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.ts"}}}
	runner.SetContractDependencyLookup(func(projectPath string) []ContractDependency { return deps })
	fetcher := &fakeContractContentFetcher{content: map[string]string{
		"qf-studio/pilot-console/internal/instances/handlers.go": specVersionProducerContent(),
	}}
	runner.SetContractContentFetcher(fetcher)
	runner.contractEvidenceFetchFn = func(ctx context.Context, dir string, fields []string) ([]ContractEvidence, error) {
		return []ContractEvidence{{
			Field:         "totallyUnrelatedField", // real citation, but for a field the diff never touched
			ProducerRepo:  "qf-studio/pilot-console",
			ProducerFile:  "internal/instances/handlers.go",
			ProducerLine:  4,
			ProducingExpr: "ConfigGeneration int",
		}}, nil
	}

	result, err := runner.Execute(contractGateExecCtx(t), task)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected failure for an irrelevant citation, got %+v", result)
	}
}

func TestRunner_ContractEvidence_WrongProducerLineFails(t *testing.T) {
	backend := &contractFileBackend{
		path:    "src/lib/api/types.ts",
		content: "export interface Instance {\n  specVersion: number;\n}\n",
	}
	runner, task := newContractGateTestRunner(t, backend)

	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.ts"}}}
	runner.SetContractDependencyLookup(func(projectPath string) []ContractDependency { return deps })
	fetcher := &fakeContractContentFetcher{content: map[string]string{
		"qf-studio/pilot-console/internal/instances/handlers.go": specVersionProducerContent(),
	}}
	runner.SetContractContentFetcher(fetcher)
	runner.contractEvidenceFetchFn = func(ctx context.Context, dir string, fields []string) ([]ContractEvidence, error) {
		return []ContractEvidence{{
			Field:         "specVersion",
			ProducerRepo:  "qf-studio/pilot-console",
			ProducerFile:  "internal/instances/handlers.go",
			ProducerLine:  100, // wrong: the real definition is at line 4; 100 is far outside any +/-3 window and out of file bounds
			ProducingExpr: "ConfigGeneration int",
		}}, nil
	}

	result, err := runner.Execute(contractGateExecCtx(t), task)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected failure for a wrong producer line, got %+v", result)
	}
}

func TestRunner_ContractEvidence_ValidCitationsSucceed(t *testing.T) {
	backend := &contractFileBackend{
		path:    "src/lib/api/types.ts",
		content: "export interface Instance {\n  specVersion: number;\n}\n",
	}
	runner, task := newContractGateTestRunner(t, backend)

	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.ts"}}}
	runner.SetContractDependencyLookup(func(projectPath string) []ContractDependency { return deps })
	fetcher := &fakeContractContentFetcher{content: map[string]string{
		"qf-studio/pilot-console/internal/instances/handlers.go": specVersionProducerContent(),
	}}
	runner.SetContractContentFetcher(fetcher)
	runner.contractEvidenceFetchFn = func(ctx context.Context, dir string, fields []string) ([]ContractEvidence, error) {
		return []ContractEvidence{{
			Field:         "specVersion",
			ProducerRepo:  "qf-studio/pilot-console",
			ProducerFile:  "internal/instances/handlers.go",
			ProducerLine:  4,
			ProducingExpr: "ConfigGeneration int",
		}}, nil
	}

	result, err := runner.Execute(contractGateExecCtx(t), task)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected success for valid, fetch-verified citations, got %+v", result)
	}
	if result.ContractEvidence == nil || !result.ContractEvidence.Passed {
		t.Errorf("expected a passed ContractEvidence outcome, got %+v", result.ContractEvidence)
	}
	if fetcher.calls == 0 {
		t.Errorf("expected the fetcher to be called to verify the citation")
	}
}

// TestRunner_ContractEvidence_UIPR113SyntheticFixture replicates the ui
// PR#113 (ui GH-112) incident shape at the runner level: the diff touches a
// configured contract file, but the cited "producer" is actually the
// consumer's own repo — a same-repo docblock, never the real producer
// (pilot-console/internal/instances/handlers.go). Since
// qf-studio/pilot-console-ui was never declared as a contract dependency,
// this is rejected mechanically (unconfigured_repo), the same way it would
// be caught in production.
func TestRunner_ContractEvidence_UIPR113SyntheticFixture(t *testing.T) {
	backend := &contractFileBackend{
		path: "src/lib/api/types.ts",
		content: "export interface Instance {\n" +
			"  // specVersion: the APPLIED config generation.\n" +
			"  specVersion: number;\n" +
			"}\n",
	}
	runner, task := newContractGateTestRunner(t, backend)

	// Only the real producer is a configured dependency.
	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.ts"}}}
	runner.SetContractDependencyLookup(func(projectPath string) []ContractDependency { return deps })
	fetcher := &fakeContractContentFetcher{content: map[string]string{
		"qf-studio/pilot-console/internal/instances/handlers.go": specVersionProducerContent(),
	}}
	runner.SetContractContentFetcher(fetcher)
	runner.contractEvidenceFetchFn = func(ctx context.Context, dir string, fields []string) ([]ContractEvidence, error) {
		return []ContractEvidence{{
			Field:         "specVersion",
			ProducerRepo:  "qf-studio/pilot-console-ui", // the executor's own (consumer) repo, not the producer
			ProducerFile:  "src/lib/api/types.ts",
			ProducerLine:  2,
			ProducingExpr: "specVersion: the APPLIED config generation",
		}}, nil
	}

	result, err := runner.Execute(contractGateExecCtx(t), task)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected failure for the ui PR#113 consumer-docblock citation shape, got %+v", result)
	}
	if result.ContractEvidence == nil {
		t.Fatalf("expected a ContractEvidence outcome")
	}
	var sawUnconfiguredRepo bool
	for _, rej := range result.ContractEvidence.Rejections {
		if rej.Reason == ContractRejectionUnconfiguredRepo {
			sawUnconfiguredRepo = true
		}
	}
	if !sawUnconfiguredRepo {
		t.Errorf("expected an unconfigured_repo rejection, got %+v", result.ContractEvidence.Rejections)
	}
	// The real producer's content must never even be consulted: rule (b)
	// rejects the unconfigured-repo citation before any fetch happens.
	if fetcher.calls != 0 {
		t.Errorf("expected 0 fetch calls when the citation's repo isn't configured, got %d", fetcher.calls)
	}
}
