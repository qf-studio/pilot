package executor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// httpBackedContractContentFetcher is a ContractContentFetcher that fetches
// producer content over a real HTTP round trip, replicating the wire
// protocol *github.Client.GetFileContent (internal/adapters/github/contents.go,
// GH-5011/PR#5015) speaks against the GitHub Contents API: GET
// /repos/{owner}/{repo}/contents/{path}?ref={ref}, decoding a JSON
// {"content": <base64>, "encoding": "base64"} response.
//
// This package cannot import internal/adapters/github directly to use the
// real *github.Client here — adapters/github -> internal/comms ->
// internal/executor would be an import cycle (documented at the top of
// contract_evidence.go, same constraint SubIssueLinker/PRCreator work
// around via injected interfaces). GH-5022's cmd/pilot-level test
// (TestNewGitHubContractContentFetcher_ConstructsRealClientType in
// cmd/pilot/adapters_test.go) is the constructor-level half of this gap: it
// asserts the production wiring injects the literal *github.Client type and
// that type correctly parses this exact wire shape via a fake HTTP
// transport. This test is the other half: proving the full Contract
// Evidence gate (Runner.Execute end to end, via the same
// contractEvidenceFetchFn harness the other tests in this file use) passes
// when its ContractContentFetcher is backed by a genuine HTTP round trip
// (rather than an in-memory map like fakeContractContentFetcher) to a fake
// transport serving the producer file — the two tests together cover the
// seam a map-backed fake alone would not.
type httpBackedContractContentFetcher struct {
	baseURL string
	client  *http.Client
	calls   int
}

func (f *httpBackedContractContentFetcher) GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	f.calls++
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s?ref=%s", f.baseURL, owner, repo, path, ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d fetching %s/%s:%s", resp.StatusCode, owner, repo, path)
	}
	var body struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	decoded, err := base64.StdEncoding.DecodeString(body.Content)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// TestRunner_ContractEvidence_GitHubBackedFetcher_ValidCitationSucceeds is
// GH-5022's end-to-end activation test: a project with contract_dependencies
// configured, a diff that touches a contract file, and a valid citation
// together pass the gate when the injected ContractContentFetcher serves
// the producer file over a fake HTTP transport — the same shape
// TestRunner_ContractEvidence_ValidCitationsSucceed proves with a map-backed
// fake, here proven against a real HTTP round trip instead.
func TestRunner_ContractEvidence_GitHubBackedFetcher_ValidCitationSucceeds(t *testing.T) {
	producerContent := specVersionProducerContent()
	encoded := base64.StdEncoding.EncodeToString([]byte(producerContent))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/repos/qf-studio/pilot-console/contents/internal/instances/handlers.go"
		if r.URL.Path != wantPath {
			t.Errorf("unexpected request path: got %q, want %q", r.URL.Path, wantPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"content":  encoded,
			"encoding": "base64",
		})
	}))
	defer server.Close()

	backend := &contractFileBackend{
		path:    "src/lib/api/types.ts",
		content: "export interface Instance {\n  specVersion: number;\n}\n",
	}
	runner, task := newContractGateTestRunner(t, backend)

	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.ts"}}}
	runner.SetContractDependencyLookup(func(projectPath string) []ContractDependency { return deps })

	fetcher := &httpBackedContractContentFetcher{baseURL: server.URL, client: server.Client()}
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
		t.Fatalf("expected success for a valid citation served over a fake HTTP transport, got %+v", result)
	}
	if result.ContractEvidence == nil || !result.ContractEvidence.Passed {
		t.Errorf("expected a passed ContractEvidence outcome, got %+v", result.ContractEvidence)
	}
	if fetcher.calls == 0 {
		t.Errorf("expected the HTTP-backed fetcher to be called to verify the citation")
	}
}
