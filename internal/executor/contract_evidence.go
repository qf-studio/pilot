package executor

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Contract Evidence gate (TASK-460 doc-vs-wire leg, GH-5009/GH-5012).
//
// A prompt-only fix for the doc-vs-wire failure class (4th TASK-460
// incident: ui PR#113 trusted a consumer-side types.ts docblock over the
// producer server handler) was rejected as cosmetic — self-review output is
// only ever advisory-grepped, never a hard gate. This file implements the
// genuinely enforced, machine-validated replacement: when a project
// declares wire-contract dependencies and a diff touches a configured
// contract file, the executor must cite real producer source for the
// fields it asserts semantics about, and the daemon independently fetches
// and verifies those citations before allowing task success.
//
// This file is intentionally self-contained within internal/executor:
//   - No internal/config import (would cycle back into internal/executor
//     via internal/comms). ContractDependency below is an executor-local
//     mirror of config.ContractDependency (internal/config/contract_dependencies.go,
//     GH-5010); cmd/pilot bridges the two (GH-5013, out of this subtask's
//     scope fence).
//   - No internal/adapters/github import (same cycle risk). Producer content
//     is fetched via the injected ContractContentFetcher interface, the same
//     pattern already used for SubIssueLinker/PRCreator (runner.go) — a
//     *github.Client satisfies it via GetFileContent (GH-5011, separate
//     subtask; may be nil until that lands, in which case verification
//     hard-fails with ContractRejectionFetchError rather than passing open).

// ContractDependency is an executor-local mirror of config.ContractDependency
// (internal/config/contract_dependencies.go). Duplicated rather than
// imported to avoid an internal/executor -> internal/config import cycle;
// cmd/pilot's ContractDependencyLookup implementation is responsible for
// translating config.ContractDependency values into this shape (GH-5013).
type ContractDependency struct {
	// Owner is the GitHub org/user that owns the producer repo.
	Owner string
	// Repo is the producer repo name.
	Repo string
	// ContractFiles is the glob allowlist of paths (in the project under
	// execution) that count as touching this contract — e.g. generated
	// types, OpenAPI specs. Diff files are matched against these globs to
	// decide whether the gate applies at all (recall-oriented: growing this
	// list is how new incidents get covered, per the check-mocks.sh model).
	ContractFiles []string
	// Ref is the git ref (branch, tag, or SHA) to fetch ContractFiles from
	// in the producer repo. Empty lets the fetcher fall back to the
	// producer repo's default branch.
	Ref string
}

// ContractDependencyLookup returns the contract dependencies configured for
// the project at projectPath, or nil/empty when none are configured — in
// which case the gate is a complete no-op (zero new GitHub API calls, per
// GH-5009's first acceptance criterion). Set via
// Runner.SetContractDependencyLookup; cmd/pilot wires the real
// config-backed implementation (GH-5013).
type ContractDependencyLookup func(projectPath string) []ContractDependency

// ContractEvidenceSchema is the --json-schema passed to getContractEvidence,
// mirroring PostExecutionSummarySchema's structured-output style
// (structured_output.go): evidence is elicited via a schema-constrained
// call, never parsed out of freeform self-review text.
const ContractEvidenceSchema = `{"type":"object","properties":{"evidence":{"type":"array","items":{"type":"object","properties":{"field":{"type":"string"},"producer_repo":{"type":"string"},"producer_file":{"type":"string"},"producer_line":{"type":"integer"},"producing_expr":{"type":"string"}},"required":["field","producer_repo","producer_file","producer_line","producing_expr"]}}},"required":["evidence"]}`

// ContractEvidence is one field-level citation of the producer source that
// defines a wire-contract field's semantics (GH-5009 Requirement 2).
type ContractEvidence struct {
	// Field is the wire field name being cited (matched against tokens
	// detectTouchedContractFields extracted from the diff).
	Field string `json:"field"`
	// ProducerRepo identifies the cited repo as "owner/repo"; must match a
	// configured ContractDependency's Owner+"/"+Repo.
	ProducerRepo string `json:"producer_repo"`
	// ProducerFile is the path within ProducerRepo the field's semantics
	// are cited from.
	ProducerFile string `json:"producer_file"`
	// ProducerLine is the 1-indexed line in ProducerFile the citation
	// points at; verification inspects a small window around it.
	ProducerLine int `json:"producer_line"`
	// ProducingExpr is the snippet of code the executor claims establishes
	// the field's semantics (e.g. "inst.ConfigGeneration"), checked
	// (whitespace-normalized) against the fetched producer window.
	ProducingExpr string `json:"producing_expr"`
}

// contractEvidenceResponse is the --json-schema structured_output payload
// shape returned by getContractEvidence.
type contractEvidenceResponse struct {
	Evidence []ContractEvidence `json:"evidence"`
}

// ContractContentFetcher fetches file content from a producer repo at a
// given ref so the gate can independently verify a citation instead of
// trusting it. *github.Client satisfies this via GetFileContent (GH-5011);
// injected via interface (rather than imported) to avoid an
// internal/executor -> internal/adapters/github cycle, the same pattern as
// SubIssueLinker and PRCreator.
type ContractContentFetcher interface {
	GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error)
}

// ContractRejectionReason classifies why a contract-evidence citation (or
// the absence of one for a required field) failed the gate.
type ContractRejectionReason string

const (
	// ContractRejectionMissing means a field the diff touched has zero
	// citations at all (Requirement 5d).
	ContractRejectionMissing ContractRejectionReason = "missing"
	// ContractRejectionFieldNotInDiff means the cited Field does not
	// appear among the fields detectTouchedContractFields extracted from
	// the diff's added lines within a contract_files-matching hunk — a
	// real-but-irrelevant citation (Requirement 5a).
	ContractRejectionFieldNotInDiff ContractRejectionReason = "field_not_in_diff"
	// ContractRejectionUnconfiguredRepo means ProducerRepo does not match
	// any configured ContractDependency (Requirement 5b).
	ContractRejectionUnconfiguredRepo ContractRejectionReason = "unconfigured_repo"
	// ContractRejectionProducerMismatch means the fetched producer window
	// around ProducerLine did not contain both the field name and the
	// cited ProducingExpr (Requirement 5c) — this is the rule that catches
	// the ui PR#113 shape (citation of a consumer-side comment, not the
	// producer).
	ContractRejectionProducerMismatch ContractRejectionReason = "producer_mismatch"
	// ContractRejectionFetchError means fetching the cited producer
	// content failed (network/API error, or no fetcher configured at
	// all). Fetch errors are hard failures, never silent passes
	// (Requirement 5, closing sentence).
	ContractRejectionFetchError ContractRejectionReason = "fetch_error"
)

// ContractFieldRejection records one field-level (or citation-level)
// failure contributing to a failed ContractEvidenceOutcome.
type ContractFieldRejection struct {
	Field  string                  `json:"field"`
	Reason ContractRejectionReason `json:"reason"`
	Detail string                  `json:"detail"`
}

// ContractEvidenceOutcome is the result of verifyContractEvidence: whether
// the gate applied at all (Required), whether it passed, and — on
// failure — the specific rejections that explain why.
type ContractEvidenceOutcome struct {
	// Required is true when the diff touched at least one file matching a
	// configured ContractDependency's ContractFiles glob. When false the
	// gate is a no-op and Passed is trivially true.
	Required bool `json:"required"`
	// Passed is true when every required field has at least one verified
	// citation and no citation was rejected.
	Passed bool `json:"passed"`
	// Fields lists the contract fields detected as touched by the diff
	// (the set requiring citations).
	Fields []string `json:"fields"`
	// Verified lists the subset of Fields that had at least one citation
	// that passed all verification rules.
	Verified []string `json:"verified,omitempty"`
	// Rejections lists every failed citation or missing-field reason.
	// Empty when Passed is true.
	Rejections []ContractFieldRejection `json:"rejections,omitempty"`
}

// Summary renders a human-readable description of a failed outcome,
// suitable for ExecutionResult.Error. Returns "" when the outcome is nil or
// passed (nothing to report).
func (o *ContractEvidenceOutcome) Summary() string {
	if o == nil || o.Passed {
		return ""
	}
	parts := make([]string, 0, len(o.Rejections))
	for _, rej := range o.Rejections {
		parts = append(parts, fmt.Sprintf("%s (%s): %s", rej.Field, rej.Reason, rej.Detail))
	}
	return fmt.Sprintf(
		"contract evidence gate failed: %d field citation(s) rejected: %s",
		len(o.Rejections), strings.Join(parts, "; "),
	)
}

// goJSONTagFieldRegex extracts the field name from a Go struct tag like
// `json:"specVersion,omitempty"` on an added diff line.
var goJSONTagFieldRegex = regexp.MustCompile(`json:"([a-zA-Z0-9_]+)`)

// tsInterfaceFieldRegex extracts the property name from a TypeScript
// interface/type member line like `specVersion?: number;` on an added diff
// line. Anchored to line-start (after diff "+" stripping) with optional
// leading whitespace, matching the conventional one-property-per-line
// interface body style.
var tsInterfaceFieldRegex = regexp.MustCompile(`^\s*([a-zA-Z_$][a-zA-Z0-9_$]*)\??\s*:\s*\S`)

// detectTouchedContractFields scans diff hunks restricted to files matching
// a configured ContractDependency's ContractFiles glob and extracts changed
// field tokens (Go json:"..." tags, TS interface property names) from
// added lines (GH-5009 Requirement 3).
//
// This is a recall-oriented heuristic, by design: false negatives here mean
// a real semantics-changing field silently skips the gate, so the
// extraction is intentionally permissive (any added line matching either
// pattern contributes a field, regardless of surrounding context). The
// verification step (verifyContractEvidence) is where precision is
// enforced — rejecting bad or irrelevant citations, not the detection step.
//
// required reports whether the diff touched at least one contract file at
// all (independent of whether any field tokens were extracted on added
// lines); fields is the deduplicated list of extracted field names, in
// first-seen order.
func detectTouchedContractFields(diff string, deps []ContractDependency) (required bool, fields []string) {
	if diff == "" || len(deps) == 0 {
		return false, nil
	}

	seen := make(map[string]bool)
	for _, section := range splitDiffByFile(diff) {
		if !matchesAnyContractGlob(section.path, deps) {
			continue
		}
		required = true

		for _, line := range strings.Split(section.body, "\n") {
			if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") {
				continue
			}
			added := strings.TrimPrefix(line, "+")

			for _, m := range goJSONTagFieldRegex.FindAllStringSubmatch(added, -1) {
				field := m[1]
				if !seen[field] {
					seen[field] = true
					fields = append(fields, field)
				}
			}

			if m := tsInterfaceFieldRegex.FindStringSubmatch(added); m != nil {
				field := m[1]
				if !seen[field] {
					seen[field] = true
					fields = append(fields, field)
				}
			}
		}
	}

	return required, fields
}

// matchesAnyContractGlob reports whether path matches any ContractFiles
// glob across deps. Tries the full (repo-relative) path first, then falls
// back to matching the basename alone — this lets a bare pattern like
// "*.ts" match regardless of directory depth, since path/filepath.Match
// does not support "**" cross-directory wildcards.
func matchesAnyContractGlob(path string, deps []ContractDependency) bool {
	base := filepath.Base(path)
	for _, dep := range deps {
		for _, pattern := range dep.ContractFiles {
			if ok, _ := filepath.Match(pattern, path); ok {
				return true
			}
			if ok, _ := filepath.Match(pattern, base); ok {
				return true
			}
		}
	}
	return false
}

// shortCircuitEmptyContractFields decides, from detectTouchedContractFields'
// required/fields output, whether the getContractEvidence LLM/structured-
// output subprocess call is needed at all (GH-5021b).
//
// A contract file can be touched (required=true) by a hunk that adds no
// line matching either field-token regex — e.g. a comment-only or
// non-field change inside an allow-listed file. There is nothing to cite in
// that case, so calling out to getContractEvidence would spend a real LLM
// invocation on an empty field set for no reason. This short-circuits
// before that call: it builds the outcome directly via
// verifyContractEvidence's existing empty-fields behavior (nil evidence,
// empty requiredFields), which reports Required=false/Passed=true —
// deliberately reusing that path rather than hand-building an equivalent
// trivial outcome, so the two "nothing to verify" cases (no contract file
// touched at all vs. touched-but-fieldless) stay consistent.
//
// Returns a non-nil outcome (and needsLLM=false) when the caller should
// skip getContractEvidence and use the outcome as-is; returns a nil outcome
// with needsLLM=true when the caller must still call getContractEvidence
// and run its result through verifyContractEvidence itself.
func shortCircuitEmptyContractFields(
	ctx context.Context,
	fetcher ContractContentFetcher,
	deps []ContractDependency,
	required bool,
	fields []string,
) (outcome *ContractEvidenceOutcome, needsLLM bool) {
	if !required {
		return nil, false
	}
	if len(fields) == 0 {
		return verifyContractEvidence(ctx, fetcher, deps, fields, nil), false
	}
	return nil, true
}

// verifyContractEvidence enforces all four rejection rules from GH-5009
// Requirement 5:
//
//	(a) a citation whose Field doesn't appear among requiredFields (the
//	    diff-touched, contract_files-matched field set) is rejected as
//	    field_not_in_diff — blocks real-but-irrelevant citations;
//	(b) a citation whose ProducerRepo isn't a configured dependency is
//	    rejected as unconfigured_repo;
//	(c) a citation is verified by fetching the producer content at the
//	    cited file+ref and checking that the +/-3-line window around the
//	    cited line contains both the field name (or its json:"field" form)
//	    and a whitespace-normalized substring match of ProducingExpr;
//	    mismatches are rejected as producer_mismatch — this is the rule
//	    that catches the ui PR#113 shape (a citation of a same-repo
//	    docblock instead of the actual producer);
//	(d) any required field with zero citations at all is rejected as
//	    missing.
//
// Fetch errors (including a nil fetcher) are hard failures
// (fetch_error), never silent passes.
func verifyContractEvidence(
	ctx context.Context,
	fetcher ContractContentFetcher,
	deps []ContractDependency,
	requiredFields []string,
	evidence []ContractEvidence,
) *ContractEvidenceOutcome {
	outcome := &ContractEvidenceOutcome{
		Required: len(requiredFields) > 0,
		Fields:   requiredFields,
	}
	if !outcome.Required {
		outcome.Passed = true
		return outcome
	}

	touched := make(map[string]bool, len(requiredFields))
	for _, f := range requiredFields {
		touched[f] = true
	}

	depByRepo := make(map[string]ContractDependency, len(deps))
	for _, d := range deps {
		depByRepo[d.Owner+"/"+d.Repo] = d
	}

	verified := make(map[string]bool)
	cited := make(map[string]bool)

	for _, e := range evidence {
		cited[e.Field] = true

		if !touched[e.Field] {
			outcome.Rejections = append(outcome.Rejections, ContractFieldRejection{
				Field:  e.Field,
				Reason: ContractRejectionFieldNotInDiff,
				Detail: fmt.Sprintf("field %q not found among fields the diff touched in a contract_files-matching hunk", e.Field),
			})
			continue
		}

		dep, ok := depByRepo[e.ProducerRepo]
		if !ok {
			outcome.Rejections = append(outcome.Rejections, ContractFieldRejection{
				Field:  e.Field,
				Reason: ContractRejectionUnconfiguredRepo,
				Detail: fmt.Sprintf("producer repo %q is not a configured contract dependency", e.ProducerRepo),
			})
			continue
		}

		if fetcher == nil {
			outcome.Rejections = append(outcome.Rejections, ContractFieldRejection{
				Field:  e.Field,
				Reason: ContractRejectionFetchError,
				Detail: "no contract content fetcher is configured",
			})
			continue
		}

		content, err := fetcher.GetFileContent(ctx, dep.Owner, dep.Repo, e.ProducerFile, dep.Ref)
		if err != nil {
			outcome.Rejections = append(outcome.Rejections, ContractFieldRejection{
				Field:  e.Field,
				Reason: ContractRejectionFetchError,
				Detail: fmt.Sprintf("fetch %s/%s:%s@%s: %v", dep.Owner, dep.Repo, e.ProducerFile, dep.Ref, err),
			})
			continue
		}

		window := producerLineWindow(content, e.ProducerLine, 3)
		if !producerWindowContains(window, e.Field, e.ProducingExpr) {
			outcome.Rejections = append(outcome.Rejections, ContractFieldRejection{
				Field:  e.Field,
				Reason: ContractRejectionProducerMismatch,
				Detail: fmt.Sprintf("producer %s/%s:%s around line %d does not contain field %q and the cited expression", dep.Owner, dep.Repo, e.ProducerFile, e.ProducerLine, e.Field),
			})
			continue
		}

		verified[e.Field] = true
	}

	for _, f := range requiredFields {
		if verified[f] {
			outcome.Verified = append(outcome.Verified, f)
			continue
		}
		if !cited[f] {
			outcome.Rejections = append(outcome.Rejections, ContractFieldRejection{
				Field:  f,
				Reason: ContractRejectionMissing,
				Detail: fmt.Sprintf("no citation provided for required field %q", f),
			})
		}
		// Fields that were cited but failed (b)/(c) above already have a
		// specific rejection recorded from the evidence loop; no need to
		// add a second, less-specific "missing" rejection for them.
	}

	outcome.Passed = len(outcome.Rejections) == 0
	return outcome
}

// producerLineWindow returns the +/-radius lines of content around the
// 1-indexed line, clamped to content bounds. Returns "" for empty content.
func producerLineWindow(content string, line int, radius int) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	if line < 1 {
		line = 1
	}
	start := line - radius - 1 // 0-indexed, inclusive
	if start < 0 {
		start = 0
	}
	if start >= len(lines) {
		return ""
	}
	end := line + radius // 0-indexed, exclusive
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start:end], "\n")
}

// producerWindowContains reports whether window contains both the field
// name (as a bare token or in json:"field" form) and, when non-empty, a
// whitespace-normalized substring match of producingExpr.
func producerWindowContains(window, field, producingExpr string) bool {
	if window == "" || field == "" {
		return false
	}
	fieldMatch := strings.Contains(window, field) || strings.Contains(window, fmt.Sprintf(`json:"%s`, field))
	if !fieldMatch {
		return false
	}
	if strings.TrimSpace(producingExpr) == "" {
		return true
	}
	return strings.Contains(normalizeWhitespace(window), normalizeWhitespace(producingExpr))
}

// normalizeWhitespace collapses all runs of whitespace to single spaces and
// trims the result, so cited expressions match regardless of incidental
// formatting differences between the citation and the producer source.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
