package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeContractContentFetcher is a map-backed ContractContentFetcher for
// tests, keyed "owner/repo/path". Set err to simulate a hard fetch failure
// regardless of the requested key.
type fakeContractContentFetcher struct {
	content map[string]string
	err     error
	calls   int
}

func (f *fakeContractContentFetcher) GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	key := owner + "/" + repo + "/" + path
	content, ok := f.content[key]
	if !ok {
		return "", errors.New("fake fetcher: not found: " + key)
	}
	return content, nil
}

func tsInterfaceDiff(path, addedLine string) string {
	return "diff --git a/" + path + " b/" + path + "\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/" + path + "\n" +
		"+++ b/" + path + "\n" +
		"@@ -1,3 +1,4 @@\n" +
		" interface Instance {\n" +
		"+" + addedLine + "\n" +
		"-  oldField: string;\n" +
		" }\n"
}

func goStructDiff(path, addedLine string) string {
	return "diff --git a/" + path + " b/" + path + "\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/" + path + "\n" +
		"+++ b/" + path + "\n" +
		"@@ -1,3 +1,4 @@\n" +
		" type DTO struct {\n" +
		"+" + addedLine + "\n" +
		" }\n"
}

func TestDetectTouchedContractFields(t *testing.T) {
	deps := []ContractDependency{
		{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.ts", "internal/instances/handlers.go"}},
	}

	t.Run("no dependencies configured is a no-op", func(t *testing.T) {
		required, fields := detectTouchedContractFields(tsInterfaceDiff("src/lib/api/types.ts", "  specVersion: number;"), nil)
		if required {
			t.Fatalf("expected required=false with no deps, got true (fields=%v)", fields)
		}
		if len(fields) != 0 {
			t.Fatalf("expected no fields, got %v", fields)
		}
	})

	t.Run("empty diff is a no-op", func(t *testing.T) {
		required, fields := detectTouchedContractFields("", deps)
		if required || len(fields) != 0 {
			t.Fatalf("expected no-op for empty diff, got required=%v fields=%v", required, fields)
		}
	})

	t.Run("diff touching an unrelated file does not trigger the gate", func(t *testing.T) {
		diff := tsInterfaceDiff("src/lib/unrelated.py", "  specVersion: number;")
		required, fields := detectTouchedContractFields(diff, deps)
		if required {
			t.Fatalf("expected required=false for non-matching file, got true (fields=%v)", fields)
		}
	})

	t.Run("TS interface property on an added line is detected", func(t *testing.T) {
		diff := tsInterfaceDiff("src/lib/api/types.ts", "  specVersion: number;")
		required, fields := detectTouchedContractFields(diff, deps)
		if !required {
			t.Fatalf("expected required=true")
		}
		if len(fields) != 1 || fields[0] != "specVersion" {
			t.Fatalf("expected [specVersion], got %v", fields)
		}
	})

	t.Run("Go json tag on an added line is detected", func(t *testing.T) {
		diff := goStructDiff("internal/instances/handlers.go", "\tConfigGeneration int `json:\"specVersion\"`")
		required, fields := detectTouchedContractFields(diff, deps)
		if !required {
			t.Fatalf("expected required=true")
		}
		if len(fields) != 1 || fields[0] != "specVersion" {
			t.Fatalf("expected [specVersion], got %v", fields)
		}
	})

	t.Run("removed lines are not scanned", func(t *testing.T) {
		// tsInterfaceDiff includes a "-  oldField: string;" removed line;
		// it must never surface in the extracted field set.
		diff := tsInterfaceDiff("src/lib/api/types.ts", "  specVersion: number;")
		_, fields := detectTouchedContractFields(diff, deps)
		for _, f := range fields {
			if f == "oldField" {
				t.Fatalf("removed-line field leaked into result: %v", fields)
			}
		}
	})

	t.Run("duplicate field mentions are deduplicated", func(t *testing.T) {
		diff := "diff --git a/src/lib/api/types.ts b/src/lib/api/types.ts\n" +
			"--- a/src/lib/api/types.ts\n" +
			"+++ b/src/lib/api/types.ts\n" +
			"@@ -1,2 +1,3 @@\n" +
			"+  specVersion: number;\n" +
			"+  specVersion: number; // duplicate mention\n"
		_, fields := detectTouchedContractFields(diff, deps)
		count := 0
		for _, f := range fields {
			if f == "specVersion" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("expected specVersion exactly once, got %d occurrences in %v", count, fields)
		}
	})
}

func TestVerifyContractEvidence_NoRequiredFields(t *testing.T) {
	outcome := verifyContractEvidence(context.Background(), nil, nil, nil, nil)
	if !outcome.Passed || outcome.Required {
		t.Fatalf("expected trivial pass with Required=false, got %+v", outcome)
	}
}

func TestVerifyContractEvidence_ZeroEvidenceIsMissing(t *testing.T) {
	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.ts"}}}
	outcome := verifyContractEvidence(context.Background(), &fakeContractContentFetcher{}, deps, []string{"specVersion"}, nil)

	if outcome.Passed {
		t.Fatalf("expected failure for zero evidence, got %+v", outcome)
	}
	if len(outcome.Rejections) != 1 || outcome.Rejections[0].Reason != ContractRejectionMissing {
		t.Fatalf("expected a single missing rejection, got %+v", outcome.Rejections)
	}
}

func TestVerifyContractEvidence_FieldNotInDiffRejected(t *testing.T) {
	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.ts"}}}
	fetcher := &fakeContractContentFetcher{content: map[string]string{
		"qf-studio/pilot-console/internal/instances/handlers.go": "package instances\nfunc h() {\n\t// unrelated\n}\n",
	}}

	evidence := []ContractEvidence{{
		Field:         "unrelatedField", // real-but-irrelevant: not among requiredFields
		ProducerRepo:  "qf-studio/pilot-console",
		ProducerFile:  "internal/instances/handlers.go",
		ProducerLine:  2,
		ProducingExpr: "func h()",
	}}

	outcome := verifyContractEvidence(context.Background(), fetcher, deps, []string{"specVersion"}, evidence)

	if outcome.Passed {
		t.Fatalf("expected failure, got %+v", outcome)
	}
	var gotFieldNotInDiff, gotMissing bool
	for _, rej := range outcome.Rejections {
		if rej.Field == "unrelatedField" && rej.Reason == ContractRejectionFieldNotInDiff {
			gotFieldNotInDiff = true
		}
		if rej.Field == "specVersion" && rej.Reason == ContractRejectionMissing {
			gotMissing = true
		}
	}
	if !gotFieldNotInDiff {
		t.Errorf("expected a field_not_in_diff rejection for unrelatedField, got %+v", outcome.Rejections)
	}
	if !gotMissing {
		t.Errorf("expected specVersion to still be reported missing (irrelevant citation doesn't satisfy it), got %+v", outcome.Rejections)
	}
	// The fetcher must never be called for a citation rejected at rule (a) —
	// field-membership is checked before any network access.
	if fetcher.calls != 0 {
		t.Errorf("expected 0 fetch calls for a field-not-in-diff citation, got %d", fetcher.calls)
	}
}

func TestVerifyContractEvidence_UnconfiguredRepoRejected(t *testing.T) {
	// Mirrors the real ui PR#113 incident shape: the citation points at the
	// consumer's own repo (a same-repo docblock) instead of the configured
	// producer — qf-studio/pilot-console-ui was never declared as a
	// contract dependency, so this is rejected mechanically, without ever
	// needing to judge the cited text's semantic correctness.
	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.ts"}}}
	evidence := []ContractEvidence{{
		Field:         "specVersion",
		ProducerRepo:  "qf-studio/pilot-console-ui", // wrong: consumer repo, not the producer
		ProducerFile:  "src/lib/api/types.ts",
		ProducerLine:  12,
		ProducingExpr: "specVersion: number; // the APPLIED generation",
	}}

	outcome := verifyContractEvidence(context.Background(), &fakeContractContentFetcher{}, deps, []string{"specVersion"}, evidence)

	if outcome.Passed {
		t.Fatalf("expected failure for unconfigured producer repo, got %+v", outcome)
	}
	if len(outcome.Rejections) != 1 || outcome.Rejections[0].Reason != ContractRejectionUnconfiguredRepo {
		t.Fatalf("expected a single unconfigured_repo rejection, got %+v", outcome.Rejections)
	}
}

func TestVerifyContractEvidence_WrongLineRejected(t *testing.T) {
	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"internal/instances/handlers.go"}}}
	producerContent := strings.Join([]string{
		"package instances",                // 1
		"",                                 // 2
		"func Handler() {",                 // 3
		"\t// nothing relevant here",       // 4
		"\tlog.Info(\"handling request\")", // 5
		"}",                                // 6
		"",                                 // 7
		"type InstanceDTO struct {",        // 8
		"\tConfigGeneration int `json:\"specVersion\"`", // 9 - the real producer line
		"}", // 10
	}, "\n")
	fetcher := &fakeContractContentFetcher{content: map[string]string{
		"qf-studio/pilot-console/internal/instances/handlers.go": producerContent,
	}}

	evidence := []ContractEvidence{{
		Field:         "specVersion",
		ProducerRepo:  "qf-studio/pilot-console",
		ProducerFile:  "internal/instances/handlers.go",
		ProducerLine:  3, // wrong line: +/-3 window (lines 1-6) never reaches line 9
		ProducingExpr: "ConfigGeneration int",
	}}

	outcome := verifyContractEvidence(context.Background(), fetcher, deps, []string{"specVersion"}, evidence)

	if outcome.Passed {
		t.Fatalf("expected failure for wrong producer line, got %+v", outcome)
	}
	if len(outcome.Rejections) != 1 || outcome.Rejections[0].Reason != ContractRejectionProducerMismatch {
		t.Fatalf("expected a single producer_mismatch rejection, got %+v", outcome.Rejections)
	}
}

func TestVerifyContractEvidence_FetchErrorIsHardFailure(t *testing.T) {
	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"*.go"}}}
	evidence := []ContractEvidence{{
		Field:         "specVersion",
		ProducerRepo:  "qf-studio/pilot-console",
		ProducerFile:  "internal/instances/handlers.go",
		ProducerLine:  9,
		ProducingExpr: "ConfigGeneration",
	}}

	t.Run("fetcher returns an error", func(t *testing.T) {
		fetcher := &fakeContractContentFetcher{err: errors.New("GitHub API unavailable")}
		outcome := verifyContractEvidence(context.Background(), fetcher, deps, []string{"specVersion"}, evidence)
		if outcome.Passed {
			t.Fatalf("expected failure on fetch error, got %+v", outcome)
		}
		if len(outcome.Rejections) != 1 || outcome.Rejections[0].Reason != ContractRejectionFetchError {
			t.Fatalf("expected a single fetch_error rejection, got %+v", outcome.Rejections)
		}
	})

	t.Run("no fetcher configured at all", func(t *testing.T) {
		outcome := verifyContractEvidence(context.Background(), nil, deps, []string{"specVersion"}, evidence)
		if outcome.Passed {
			t.Fatalf("expected failure with nil fetcher, got %+v", outcome)
		}
		if len(outcome.Rejections) != 1 || outcome.Rejections[0].Reason != ContractRejectionFetchError {
			t.Fatalf("expected a single fetch_error rejection, got %+v", outcome.Rejections)
		}
	})
}

func TestVerifyContractEvidence_ValidCitationsSucceed(t *testing.T) {
	deps := []ContractDependency{{Owner: "qf-studio", Repo: "pilot-console", ContractFiles: []string{"internal/instances/handlers.go"}}}
	producerContent := strings.Join([]string{
		"package instances",         // 1
		"",                          // 2
		"type InstanceDTO struct {", // 3
		"\tConfigGeneration int `json:\"specVersion\"`", // 4
		"}", // 5
	}, "\n")
	fetcher := &fakeContractContentFetcher{content: map[string]string{
		"qf-studio/pilot-console/internal/instances/handlers.go": producerContent,
	}}

	evidence := []ContractEvidence{{
		Field:         "specVersion",
		ProducerRepo:  "qf-studio/pilot-console",
		ProducerFile:  "internal/instances/handlers.go",
		ProducerLine:  4,
		ProducingExpr: "ConfigGeneration int",
	}}

	outcome := verifyContractEvidence(context.Background(), fetcher, deps, []string{"specVersion"}, evidence)

	if !outcome.Passed {
		t.Fatalf("expected success, got %+v", outcome)
	}
	if len(outcome.Rejections) != 0 {
		t.Fatalf("expected zero rejections, got %+v", outcome.Rejections)
	}
	if len(outcome.Verified) != 1 || outcome.Verified[0] != "specVersion" {
		t.Fatalf("expected specVersion verified, got %v", outcome.Verified)
	}
}

func TestNormalizeWhitespace(t *testing.T) {
	cases := map[string]string{
		"  a   b\tc\n": "a b c",
		"":             "",
		"single":       "single",
	}
	for in, want := range cases {
		if got := normalizeWhitespace(in); got != want {
			t.Errorf("normalizeWhitespace(%q) = %q, want %q", in, got, want)
		}
	}
}
