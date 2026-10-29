package quality

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Environment variables exported to every gate command (GH-5926). The
// Makefile's test-changed target reads them to scope `go test`.
const (
	// EnvChangedPackages is the space-separated import paths affected by the
	// task's diff; empty when the scope is full or docs-only.
	EnvChangedPackages = "PILOT_CHANGED_PACKAGES"
	// EnvChangedFull is "1" when the diff cannot be scoped and the whole suite
	// must run, "0" otherwise.
	EnvChangedFull = "PILOT_CHANGED_FULL"
	// EnvChangedReason says why the scope is what it is. "docs-only" is the one
	// value test-changed treats specially: an empty package list then means
	// there is nothing to test rather than a blind spot.
	EnvChangedReason = "PILOT_CHANGED_REASON"
)

// ScopeReasonDocsOnly is the reason for a diff that touches nothing but docs
// and tooling metadata: the test gate passes without running.
const ScopeReasonDocsOnly = "docs-only"

// scopeFullThresholdPercent: an affected set covering more than this share of
// the module's packages runs ./... instead of a long package list.
const scopeFullThresholdPercent = 70

// fullSuitePaths are repo-root entries whose change can alter any package's
// behaviour (dependencies, build, lint config, CI, tooling). Matched as the
// path itself or a directory prefix.
var fullSuitePaths = []string{"go.mod", "go.sum", "go.work", "go.work.sum", "Makefile", "scripts", ".github", "configs", ".golangci.yml", ".golangci.yaml"}

// ignoredPaths are directories whose content never reaches a Go test:
// Navigator docs, the docs site, and per-session Claude Code settings (the
// executor rewrites .claude/settings.json in every task worktree to install
// its hooks, which must not force the full suite on every task).
var ignoredPaths = []string{".agent", "docs", ".claude"}

// relPathLiteralRE finds "../dir/..." string literals and filepath.Join("..", "dir", ...)
// argument runs in test source.
var (
	relPathLiteralRE = regexp.MustCompile("[\"`](\\.\\./[^\"`\\s]*)[\"`]")
	relJoinLiteralRE = regexp.MustCompile(`"\.\."\s*,\s*((?:"[^"]*"\s*,?\s*)+)`)
	quotedPartRE     = regexp.MustCompile(`"([^"]*)"`)
)

type goListPackage struct {
	importPath  string
	dir         string // slash-separated, relative to the module root; "." for the root
	imports     []string
	testImports []string // TestImports + XTestImports
	testFiles   []string // TestGoFiles + XTestGoFiles, relative to dir
}

// ChangedGoPackages returns the import paths of the Go packages whose tests a
// diff can affect, a full flag, and a reason.
//
// The diff is the union of the commits since the merge base with baseBranch,
// uncommitted changes against HEAD, and untracked files. A package is affected
// when it changed, when its production-import closure contains a changed
// package, or when one hop of its test imports (and the closure of those) does.
// Test imports are never followed transitively: a package's tests importing
// another package says nothing about who imports that package's tests.
//
// full is true — and pkgs nil — whenever the diff cannot be scoped with
// confidence: build/CI/config changes, files outside any Go package, any git or
// go list failure, no merge base, an empty baseBranch, or an affected set above
// 70 percent of the module. A diff of only docs yields no packages, full false
// and reason "docs-only".
func ChangedGoPackages(ctx context.Context, dir, baseBranch string) (pkgs []string, full bool, reason string) {
	if strings.TrimSpace(baseBranch) == "" {
		return nil, true, "empty base branch"
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return nil, true, "not a Go module root"
	}

	files, err := changedFiles(ctx, dir, baseBranch)
	if err != nil {
		return nil, true, err.Error()
	}
	if len(files) == 0 {
		return nil, true, "no changed files found"
	}

	var scoped []string
	for _, f := range files {
		if underAny(f, ignoredPaths) {
			continue
		}
		if underAny(f, fullSuitePaths) || isModuleFile(f) {
			return nil, true, "build or config change: " + f
		}
		scoped = append(scoped, f)
	}
	if len(scoped) == 0 {
		return nil, false, ScopeReasonDocsOnly
	}

	graph, err := listPackages(ctx, dir)
	if err != nil {
		return nil, true, err.Error()
	}

	byDir := make(map[string]*goListPackage, len(graph))
	byPath := make(map[string]*goListPackage, len(graph))
	for _, p := range graph {
		byDir[p.dir] = p
		byPath[p.importPath] = p
	}

	changed := make(map[string]bool)
	for _, f := range scoped {
		p := owningPackage(f, byDir)
		if p == nil {
			return nil, true, "change outside any Go package: " + f
		}
		changed[p.importPath] = true
	}

	// Test-side edges: imports, plus relative-path reads of a sibling directory.
	testEdges := make(map[string][]string, len(graph))
	relEdges := make(map[string][]string, len(graph))
	for _, p := range graph {
		for _, imp := range p.testImports {
			if byPath[imp] != nil {
				testEdges[p.importPath] = append(testEdges[p.importPath], imp)
			}
		}
		relEdges[p.importPath] = relativeReads(dir, p, byDir)
	}

	closures := make(map[string]map[string]bool, len(graph))
	var closure func(ip string) map[string]bool
	closure = func(ip string) map[string]bool {
		if c, ok := closures[ip]; ok {
			return c
		}
		c := map[string]bool{ip: true}
		closures[ip] = c // set before recursing; import graphs are acyclic
		for _, imp := range byPath[ip].imports {
			if byPath[imp] == nil {
				continue
			}
			for k := range closure(imp) {
				c[k] = true
			}
		}
		return c
	}
	hits := func(set map[string]bool) bool {
		for ip := range set {
			if changed[ip] {
				return true
			}
		}
		return false
	}

	var affected []string
	for _, p := range graph {
		ok := hits(closure(p.importPath))
		for _, q := range testEdges[p.importPath] {
			if ok {
				break
			}
			ok = hits(closure(q))
		}
		for _, q := range relEdges[p.importPath] {
			if ok {
				break
			}
			ok = changed[q]
		}
		if ok {
			affected = append(affected, p.importPath)
		}
	}
	sort.Strings(affected)

	if len(affected)*100 > scopeFullThresholdPercent*len(graph) {
		return nil, true, fmt.Sprintf("affected %d of %d packages (over %d%%)", len(affected), len(graph), scopeFullThresholdPercent)
	}
	return affected, false, fmt.Sprintf("affected %d of %d packages", len(affected), len(graph))
}

// underAny reports whether p equals one of roots or lies beneath it.
func underAny(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// isModuleFile matches go.mod / go.sum at any depth (nested modules).
func isModuleFile(p string) bool {
	switch path.Base(p) {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return true
	}
	return false
}

// owningPackage maps a changed file to the package it belongs to: the package
// in its directory, or — for testdata — the package above the testdata dir.
func owningPackage(file string, byDir map[string]*goListPackage) *goListPackage {
	d := path.Dir(file)
	if p, ok := byDir[d]; ok {
		return p
	}
	parts := strings.Split(d, "/")
	for i, part := range parts {
		if part != "testdata" {
			continue
		}
		parent := "."
		if i > 0 {
			parent = strings.Join(parts[:i], "/")
		}
		return byDir[parent]
	}
	return nil
}

// relativeReads returns the import paths of sibling packages whose directory a
// test file of p names through a "../dir" literal.
func relativeReads(root string, p *goListPackage, byDir map[string]*goListPackage) []string {
	var out []string
	seen := map[string]bool{}
	for _, tf := range p.testFiles {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p.dir), filepath.FromSlash(tf)))
		if err != nil {
			continue
		}
		var lits []string
		for _, m := range relPathLiteralRE.FindAllSubmatch(src, -1) {
			lits = append(lits, string(m[1]))
		}
		for _, m := range relJoinLiteralRE.FindAllSubmatch(src, -1) {
			parts := []string{".."}
			for _, q := range quotedPartRE.FindAllSubmatch(m[1], -1) {
				parts = append(parts, string(q[1]))
			}
			lits = append(lits, strings.Join(parts, "/"))
		}
		for _, lit := range lits {
			if target := packageForRelative(p.dir, lit, byDir); target != nil && target.importPath != p.importPath && !seen[target.importPath] {
				seen[target.importPath] = true
				out = append(out, target.importPath)
			}
		}
	}
	return out
}

// packageForRelative resolves lit against fromDir and returns the deepest
// package directory it points into. The module root only matches when lit
// names it exactly, so "../../README.md" does not tie everything to the root.
func packageForRelative(fromDir, lit string, byDir map[string]*goListPackage) *goListPackage {
	resolved := path.Clean(path.Join(fromDir, lit))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return nil
	}
	if resolved == "." {
		return byDir["."]
	}
	for d := resolved; d != "." && d != "/"; d = path.Dir(d) {
		if p, ok := byDir[d]; ok {
			return p
		}
	}
	return nil
}

// changedFiles returns the slash-separated, repo-relative paths in the diff
// against the merge base, the working tree and the untracked set.
func changedFiles(ctx context.Context, dir, baseBranch string) ([]string, error) {
	top, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("git rev-parse: %w", err)
	}
	topDir := strings.TrimSpace(string(top))
	if !samePath(topDir, dir) {
		return nil, fmt.Errorf("%s is not the repository root", dir)
	}

	mergeBase := ""
	for _, ref := range []string{"origin/" + baseBranch, baseBranch} {
		out, err := gitOutput(ctx, dir, "merge-base", ref, "HEAD")
		if err == nil {
			mergeBase = strings.TrimSpace(string(out))
			break
		}
	}
	if mergeBase == "" {
		return nil, fmt.Errorf("no merge base with origin/%s or %s", baseBranch, baseBranch)
	}

	seen := map[string]bool{}
	var files []string
	for _, args := range [][]string{
		{"diff", "--name-only", "--no-renames", "-z", mergeBase + "...HEAD"},
		{"diff", "--name-only", "--no-renames", "-z", "HEAD"},
		{"ls-files", "-o", "--exclude-standard", "-z"},
	} {
		out, err := gitOutput(ctx, dir, args...)
		if err != nil {
			return nil, fmt.Errorf("git %s: %w", args[0], err)
		}
		for _, f := range bytes.Split(out, []byte{0}) {
			s := strings.TrimSpace(string(f))
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			files = append(files, path.Clean(filepath.ToSlash(s)))
		}
	}
	sort.Strings(files)
	return files, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// listPackages reads the module's package graph with go list.
func listPackages(ctx context.Context, dir string) ([]*goListPackage, error) {
	const format = `{{.ImportPath}}{{"\t"}}{{.Dir}}{{"\t"}}{{join .Imports " "}}{{"\t"}}{{join .TestImports " "}} {{join .XTestImports " "}}{{"\t"}}{{join .TestGoFiles " "}} {{join .XTestGoFiles " "}}`
	cmd := exec.CommandContext(ctx, "go", "list", "-f", format, "./...")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		root = dir
	}

	var pkgs []*goListPackage
	// Decision: never trim the whole output or individual lines. The last
	// package's line can end in empty columns (no test imports, no test
	// files), which trimming would strip, leaving fewer than five columns.
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 5 {
			return nil, fmt.Errorf("go list: unexpected line %q", line)
		}
		pdir, err := filepath.EvalSymlinks(cols[1])
		if err != nil {
			pdir = cols[1]
		}
		rel, err := filepath.Rel(root, pdir)
		if err != nil || strings.HasPrefix(filepath.ToSlash(rel), "..") {
			return nil, fmt.Errorf("go list: package %s outside %s", cols[0], dir)
		}
		pkgs = append(pkgs, &goListPackage{
			importPath:  cols[0],
			dir:         filepath.ToSlash(rel),
			imports:     strings.Fields(cols[2]),
			testImports: strings.Fields(cols[3]),
			testFiles:   strings.Fields(cols[4]),
		})
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("go list: no packages")
	}
	return pkgs, nil
}
