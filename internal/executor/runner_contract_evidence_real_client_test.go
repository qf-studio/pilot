package executor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// httpContentsAPIFetcher is a ContractContentFetcher that fetches producer
// content over real HTTP from a fake transport, mirroring the exact wire
// format of GitHub's Contents API (GET /repos/{owner}/{repo}/contents/{path}
// -> base64-wrapped JSON body) that *github.Client.GetFileContent (PR#5015,
// internal/adapters/github/contents.go) implements in production.
//
// This is a local reimplementation rather than a direct use of
// *github.Client because internal/executor cannot import
// internal/adapters/github: that package imports internal/comms, which
// imports internal/executor, so the import would cycle (documented at the
// top of contract_evidence.go) — the exact reason ContractContentFetcher is
// an interface at all. cmd/pilot/contract_content_fetcher_test.go (GH-5022)
// covers the other half of the end-to-end story: a constructor-level
// assertion that newProjectContractContentFetcher(cfg) — the function wired
// at all 5 runner-construction sites — returns the real *github.Client type,
// plus GetFileContent against the same kind of fake HTTP transport used
// here. Together the two tests cover "real type is wired in production" and
// "the gate genuinely passes end-to-end when that wire format is served",
// without either test needing to cross the package boundary that would
// cycle.
type httpContentsAPIFetcher struct {
	baseURL string
	client  *http.Client
}

func (f *httpContentsAPIFetcher) GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s", f.baseURL, owner, repo, path)
	if ref != "" {
		url += "?ref=" + ref
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("contents API returned status %d: %s", resp.StatusCode, string(body))
	}

	var payload struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	decoded, err := base64.StdEncoding.DecodeString(payload.Content)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// TestRunner_ContractEvidence_FakeHTTPTransportEndToEnd is GH-5022's
// end-to-end activation check: a project with contract_dependencies
// configured, a diff that touches an allow-listed contract file, and a
// citation that a fetcher must independently verify by making a real HTTP
// round trip against a fake transport (httptest.Server) serving the
// producer file — proving the gate passes on the full Runner.Execute()
// path when the fetcher genuinely has to fetch over HTTP, not just an
// in-memory map lookup (as fakeContractContentFetcher, used by every other
// test in this package, does).
func TestRunner_ContractEvidence_FakeHTTPTransportEndToEnd(t *testing.T) {
	producerContent := specVersionProducerContent()
	encoded := base64.StdEncoding.EncodeToString([]byte(producerContent))

	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		wantPath := "/repos/qf-studio/pilot-console/contents/internal/instances/handlers.go"
		if r.URL.Path != wantPath {
			t.Errorf("unexpected request path: %s, want %s", r.URL.Path, wantPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"content":  encoded,
			"encoding": "base64",
		})
	}))
	defer server.Close()

	fetcher := &httpContentsAPIFetcher{baseURL: server.URL, client: server.Client()}

	backend := &contractFileBackend{
		path:    "src/lib/api/types.ts",
		content: "export interface Instance {\n  specVersion: number;\n}\n",
	}
	runner, task := newContractGateTestRunner(t, backend)

	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.ts"}}}
	runner.SetContractDependencyLookup(func(projectPath string) []ContractDependency { return deps })
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
		t.Fatalf("expected success for a valid citation verified over a real HTTP round trip, got %+v", result)
	}
	if result.ContractEvidence == nil || !result.ContractEvidence.Passed {
		t.Errorf("expected a passed ContractEvidence outcome, got %+v", result.ContractEvidence)
	}
	if requests == 0 {
		t.Error("expected the gate to make at least one HTTP request against the fake transport")
	}
}
