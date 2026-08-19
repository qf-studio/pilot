package ghguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testIdentity() Identity {
	return Identity{
		TaskIssue:  "4671",
		TaskRepo:   "qf-studio/pilot",
		TaskBranch: "pilot/GH-4671",
		RealGh:     "/usr/bin/gh",
	}
}

func TestClassify(t *testing.T) {
	id := testIdentity()

	tests := []struct {
		name        string
		args        []string
		wantVerdict Verdict
	}{
		// --- unconditional reads ---
		{"issue view own", []string{"issue", "view", "4671"}, VerdictAllow},
		{"issue view other issue same repo", []string{"issue", "view", "1"}, VerdictAllow},
		{"issue list", []string{"issue", "list"}, VerdictAllow},
		{"issue list with flags", []string{"issue", "list", "--state", "open", "--label", "pilot"}, VerdictAllow},
		{"pr view", []string{"pr", "view", "42"}, VerdictAllow},
		{"pr list", []string{"pr", "list"}, VerdictAllow},
		{"pr checks", []string{"pr", "checks", "42"}, VerdictAllow},
		{"pr diff", []string{"pr", "diff", "42"}, VerdictAllow},
		{"run view", []string{"run", "view", "12345"}, VerdictAllow},
		{"run list", []string{"run", "list"}, VerdictAllow},
		{"auth status", []string{"auth", "status"}, VerdictAllow},
		{"issue view with matching -R short", []string{"issue", "view", "1", "-R", "qf-studio/pilot"}, VerdictAllow},
		{"issue view with matching --repo long", []string{"issue", "view", "1", "--repo", "qf-studio/pilot"}, VerdictAllow},
		{"issue view with matching --repo=value", []string{"issue", "view", "1", "--repo=qf-studio/pilot"}, VerdictAllow},

		// --- gh api reads ---
		{"api GET implicit", []string{"api", "repos/qf-studio/pilot/issues/1"}, VerdictAllow},
		{"api GET explicit -X", []string{"api", "repos/qf-studio/pilot/issues/1", "-X", "GET"}, VerdictAllow},
		{"api GET explicit --method lowercase", []string{"api", "repos/qf-studio/pilot/issues/1", "--method", "get"}, VerdictAllow},

		// --- own-artifact allows ---
		{"pr create no head (current branch)", []string{"pr", "create", "--title", "t", "--body", "b"}, VerdictAllow},
		{"pr create head matches task branch", []string{"pr", "create", "--head", "pilot/GH-4671", "--title", "t"}, VerdictAllow},
		{"issue comment own issue", []string{"issue", "comment", "4671", "--body", "hi"}, VerdictAllow},
		{"issue comment own issue via URL", []string{"issue", "comment", "https://github.com/qf-studio/pilot/issues/4671", "--body", "hi"}, VerdictAllow},
		{"pr comment no target (current branch)", []string{"pr", "comment", "--body", "hi"}, VerdictAllow},
		{"pr comment target matches task branch", []string{"pr", "comment", "pilot/GH-4671", "--body", "hi"}, VerdictAllow},

		// --- hard denies: issue lifecycle mutation ---
		{"issue close", []string{"issue", "close", "4671"}, VerdictDeny},
		{"issue reopen", []string{"issue", "reopen", "1"}, VerdictDeny},
		{"issue edit", []string{"issue", "edit", "4671", "--title", "x"}, VerdictDeny},
		{"issue lock", []string{"issue", "lock", "4671"}, VerdictDeny},
		{"issue transfer", []string{"issue", "transfer", "4671", "other/repo"}, VerdictDeny},
		{"issue delete", []string{"issue", "delete", "4671"}, VerdictDeny},

		// --- hard denies: label mutation ---
		{"issue edit add-label", []string{"issue", "edit", "4671", "--add-label", "pilot-superseded"}, VerdictDeny},
		{"issue edit remove-label", []string{"issue", "edit", "4671", "--remove-label", "bug"}, VerdictDeny},
		{"pr edit add-label", []string{"pr", "edit", "42", "--add-label", "x"}, VerdictDeny},

		// --- hard denies: cross-issue/PR number on own-artifact commands ---
		{"issue comment on sibling issue", []string{"issue", "comment", "1", "--body", "hi"}, VerdictDeny},
		{"pr create head mismatched branch", []string{"pr", "create", "--head", "some-other-branch", "--title", "t"}, VerdictDeny},
		{"pr comment on numeric target", []string{"pr", "comment", "99", "--body", "hi"}, VerdictDeny},
		{"pr comment on URL target", []string{"pr", "comment", "https://github.com/qf-studio/pilot/pull/99", "--body", "hi"}, VerdictDeny},

		// --- hard denies: -R/--repo mismatch (even on read commands) ---
		{"issue view wrong repo short flag", []string{"issue", "view", "1", "-R", "qf-studio/upstream"}, VerdictDeny},
		{"issue view wrong repo long flag", []string{"issue", "view", "1", "--repo", "qf-studio/upstream"}, VerdictDeny},
		{"issue view wrong repo equals form", []string{"issue", "view", "1", "--repo=qf-studio/upstream"}, VerdictDeny},
		{"pr list wrong repo", []string{"pr", "list", "-R", "someone/else"}, VerdictDeny},

		// --- hard denies: gh api non-GET / implicit mutation ---
		{"api POST explicit -X", []string{"api", "repos/o/r/issues/1/labels", "-X", "POST", "-f", "labels[]=x"}, VerdictDeny},
		{"api PATCH via --method", []string{"api", "repos/o/r/issues/1", "--method", "PATCH"}, VerdictDeny},
		{"api DELETE", []string{"api", "repos/o/r/issues/1", "-X", "DELETE"}, VerdictDeny},
		{"api with -f data flag even without -X", []string{"api", "repos/o/r/issues/1", "-f", "state=closed"}, VerdictDeny},
		{"api with -F data flag", []string{"api", "graphql", "-F", "query=x"}, VerdictDeny},
		{"api with --input flag", []string{"api", "repos/o/r/issues", "--input", "payload.json"}, VerdictDeny},

		// --- hard denies: whole command families ---
		{"release create", []string{"release", "create", "v1.0.0"}, VerdictDeny},
		{"release delete", []string{"release", "delete", "v1.0.0"}, VerdictDeny},
		{"repo delete", []string{"repo", "delete", "qf-studio/pilot"}, VerdictDeny},
		{"repo edit", []string{"repo", "edit", "--description", "x"}, VerdictDeny},
		{"secret set", []string{"secret", "set", "FOO"}, VerdictDeny},
		{"variable set", []string{"variable", "set", "FOO"}, VerdictDeny},
		{"workflow run", []string{"workflow", "run", "deploy.yml"}, VerdictDeny},
		{"workflow disable", []string{"workflow", "disable", "deploy.yml"}, VerdictDeny},

		// --- default deny for anything unlisted ---
		{"gist create", []string{"gist", "create", "file.txt"}, VerdictDeny},
		{"empty argv", []string{}, VerdictDeny},
		{"issue comment missing target", []string{"issue", "comment", "--body", "hi"}, VerdictDeny},

		// --- parser parity (pflag shapes) ---
		// GH-4963: gh uses pflag, which accepts attached shorthand
		// (-XPOST == -X POST), `=` form, boolean bundling, and
		// last-occurrence-wins for scalars. Rows below are drawn from the
		// issue's confirmed parity gaps (G1-G11), each verified live
		// against the installed `gh` binary (via --hostname pointed at a
		// non-routable host + --verbose, so no request ever reaches
		// GitHub) rather than taken on faith from the issue text.
		{"api attached -XPOST", []string{"api", "repos/o/r/issues", "-XPOST"}, VerdictDeny},       // G1
		{"api attached -XDELETE", []string{"api", "repos/o/r/issues/1", "-XDELETE"}, VerdictDeny}, // G2
		{"api attached -XGET is a read", []string{"api", "repos/o/r/issues/1", "-XGET"}, VerdictAllow},
		{"api eq short -X=POST", []string{"api", "x", "-X=POST"}, VerdictDeny},                           // regression
		{"api long eq --method=PATCH", []string{"api", "x", "--method=PATCH"}, VerdictDeny},              // regression
		{"api attached raw-field", []string{"api", "repos/o/r/issues/1", "-fstate=closed"}, VerdictDeny}, // G3
		{"api attached typed field", []string{"api", "graphql", "-Fquery=mutation"}, VerdictDeny},        // G4
		{"api eq short field", []string{"api", "graphql", "-F=query=x"}, VerdictDeny},

		// -p/--preview take a VALUE (opt into API previews) — they are not
		// a paginate shorthand; --paginate itself has no short form. This
		// was checked live: `gh api ... -pXDELETE` sends a plain GET with
		// a garbage preview header, never a DELETE. The bundling/attached-
		// value/doesn't-swallow-the-next-flag mechanics the issue's G5-G7
		// rows were probing are exercised below with -i/--include instead,
		// gh's actual boolean short flag on `api`.
		{"api bundled bool then value (-i include, -X method)", []string{"api", "repos/o/r/x", "-iX", "POST"}, VerdictDeny},
		{"api bundled attached bool+value", []string{"api", "repos/o/r/x", "-iXPOST"}, VerdictDeny},
		{"api -i is boolean, does not swallow -X", []string{"api", "repos/o/r/x", "-i", "-X", "POST"}, VerdictDeny},
		{"api paginate GET stays a read", []string{"api", "repos/o/r/x", "--paginate"}, VerdictAllow},
		{"api include bool GET stays a read", []string{"api", "repos/o/r/x", "-i"}, VerdictAllow},
		{"api attached preview value stays a read", []string{"api", "repos/o/r/x", "-pXDELETE"}, VerdictAllow}, // -p swallows "XDELETE" as its own value, not -X/DELETE
		{"api benign value flags", []string{"api", "x", "--cache", "3600s", "-H", "Accept: a", "-q", ".items", "-t", "{{.}}"}, VerdictAllow},
		{"api attached header stays a read", []string{"api", "x", "-HAccept: a"}, VerdictAllow},

		// repeated flags — last occurrence wins
		{"api repeated -X last POST attached", []string{"api", "x", "-X", "GET", "-XPOST"}, VerdictDeny}, // G8
		{"api repeated -X last GET", []string{"api", "x", "-X", "POST", "-X", "GET"}, VerdictAllow},      // regression

		// attached -R
		{"issue view attached -R matching", []string{"issue", "view", "1", "-Rqf-studio/pilot"}, VerdictAllow},
		{"issue view attached -R wrong repo", []string{"issue", "view", "1", "-Rqf-studio/upstream"}, VerdictDeny},                  // G9
		{"issue comment attached -R cross-repo", []string{"issue", "comment", "4671", "-Rother/repo", "--body", "hi"}, VerdictDeny}, // G10

		// pr create -f is --fill (boolean), not a value flag
		{"pr create fill does not swallow --head", []string{"pr", "create", "-f", "--head", "other-branch"}, VerdictDeny}, // G11
		{"pr create fill with own head", []string{"pr", "create", "-f", "--head", "pilot/GH-4671"}, VerdictAllow},
		{"pr create fill with own head via -H", []string{"pr", "create", "-f", "-H", "pilot/GH-4671"}, VerdictAllow},

		// -- terminates flag parsing
		{"pr create -- then --head is positional not a flag", []string{"pr", "create", "--head", "pilot/GH-4671", "--", "--head"}, VerdictAllow},

		// fail-closed on unverifiable api/own-artifact flags
		{"api unknown flag fails closed", []string{"api", "x", "--not-a-real-flag", "v"}, VerdictDeny},
		{"api dangling -X at end of argv", []string{"api", "x", "-X"}, VerdictDeny},
		{"pr create unknown flag fails closed", []string{"pr", "create", "--not-a-real-flag", "v"}, VerdictDeny},
		{"issue comment unknown flag fails closed", []string{"issue", "comment", "4671", "--not-a-real-flag", "v"}, VerdictDeny},
		// kindRead commands stay lenient on unknown flags — never denied for
		// this reason alone (repo mismatch / hard-deny checks still apply).
		{"issue view unknown flag stays lenient", []string{"issue", "view", "1", "--not-a-real-flag", "v"}, VerdictAllow},

		// --- explicit-GET query fields (#4905) ---
		// All three verified live: gh sends a plain GET with the -f value
		// appended to the query string, including with a lowercase
		// "--method get" (gh's method compare is case-insensitive).
		{"api search -f with explicit -X GET", []string{"api", "search/issues", "-X", "GET", "-f", "q=x in:body"}, VerdictAllow},
		{"api search -f with --method get", []string{"api", "search/issues", "--method", "get", "-f", "q=x"}, VerdictAllow},
		{"api search -f with attached -XGET", []string{"api", "search/issues", "-XGET", "-f", "q=x"}, VerdictAllow},
		{"api field without method still denies", []string{"api", "repos/o/r/issues/1", "-f", "state=closed"}, VerdictDeny}, // regression
		{"api field with -X GET then -X POST", []string{"api", "x", "-X", "GET", "-f", "a=b", "-X", "POST"}, VerdictDeny},

		// --input is a body ALWAYS — immune to the relaxation. Verified
		// live: `-X GET --input file.json` still ships the file as the
		// request body.
		{"api --input with explicit GET", []string{"api", "x", "-X", "GET", "--input", "p.json"}, VerdictDeny},
		{"api --input=file", []string{"api", "x", "--input=p.json"}, VerdictDeny}, // regression
		{"api --input from stdin", []string{"api", "x", "--input", "-"}, VerdictDeny},

		// --- api graphql with data-carrying flags denies regardless of
		// method (GH-4986/D2): the explicit-GET relaxation above is
		// REST-shaped and never applies to graphql, since gh always POSTs
		// the query — the query text, not -X, decides mutate vs. read.
		{"api graphql with -f query denies (no explicit method)", []string{"api", "graphql", "-f", "query=mutation { x }"}, VerdictDeny},
		{"api graphql with -f query denies even with explicit -X GET", []string{"api", "graphql", "-f", "query=mutation { x }", "-X", "GET"}, VerdictDeny},
		{"api graphql with --field denies even with --method get", []string{"api", "graphql", "--field", "query=x", "--method", "get"}, VerdictDeny},
		{"api graphql with --input denies regardless of method", []string{"api", "graphql", "--input", "p.json", "-X", "GET"}, VerdictDeny},
		{"api graphql bare is a read", []string{"api", "graphql"}, VerdictAllow},
		{"api graphql with only benign flags is a read", []string{"api", "graphql", "--jq", ".data"}, VerdictAllow},

		// --- --hostname is the argv twin of GH_HOST (GH-4986): same
		// fail-closed non-github.com deny, case-insensitive github.com no-op.
		{"api --hostname non-github.com denies", []string{"api", "--hostname", "ghe.example.com", "user"}, VerdictDeny},
		{"api --hostname github.com is a no-op", []string{"api", "--hostname", "github.com", "user"}, VerdictAllow},
		{"api --hostname case-insensitive github.com is a no-op", []string{"api", "--hostname", "GitHub.Com", "user"}, VerdictAllow},
		{"api --hostname non-github.com denies even a would-be-GET", []string{"api", "--hostname", "ghe.example.com", "user", "-X", "GET"}, VerdictDeny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(id, tt.args, EnvOverride{})
			if got.Verdict != tt.wantVerdict {
				t.Fatalf("Classify(%v) = %s (reason: %q), want %s", tt.args, got.Verdict, got.Reason, tt.wantVerdict)
			}
			if got.Verdict == VerdictDeny {
				if got.Reason == "" {
					t.Errorf("deny decision missing Reason")
				}
				if got.Allowed == "" {
					t.Errorf("deny decision missing Allowed hint")
				}
			}
		})
	}
}

// TestClassify_EnvOverride is GH-4968/D5: gh itself honors GH_REPO/GH_HOST
// from the environment, not just -R/--repo on argv, so Classify must too —
// otherwise `GH_REPO=other/repo gh issue comment N --body x` sails through
// the repo-scoping check (which used to look at argv only) into a
// cross-repo mutation, the exact class GH-4649 exists to stop. Kept as its
// own table (rather than folded into TestClassify's) since every row here
// exercises the env parameter, not just args.
func TestClassify_EnvOverride(t *testing.T) {
	id := testIdentity()

	tests := []struct {
		name        string
		args        []string
		env         EnvOverride
		wantVerdict Verdict
	}{
		{"GH_REPO cross-repo mutation denied like argv -R would be",
			[]string{"issue", "comment", "1", "--body", "x"}, EnvOverride{Repo: "other/repo"}, VerdictDeny},
		{"GH_REPO matching own repo allows a read",
			[]string{"issue", "view", "1"}, EnvOverride{Repo: "qf-studio/pilot"}, VerdictAllow},
		{"GH_REPO matching own repo allows own-artifact comment",
			[]string{"issue", "comment", "4671", "--body", "x"}, EnvOverride{Repo: "qf-studio/pilot"}, VerdictAllow},
		{"argv -R wins over conflicting GH_REPO (own repo via argv, foreign via env)",
			[]string{"issue", "view", "1", "-R", "qf-studio/pilot"}, EnvOverride{Repo: "other/repo"}, VerdictAllow},
		{"argv -R wins over conflicting GH_REPO (foreign via argv, own via env) — still denied",
			[]string{"issue", "view", "1", "-R", "other/repo"}, EnvOverride{Repo: "qf-studio/pilot"}, VerdictDeny},
		{"GH_HOST non-github.com denies an otherwise-allowed read",
			[]string{"api", "user"}, EnvOverride{Host: "ghe.example.com"}, VerdictDeny},
		{"GH_HOST=github.com is a no-op",
			[]string{"issue", "view", "1"}, EnvOverride{Host: "github.com"}, VerdictAllow},
		{"GH_HOST case-insensitive match is a no-op",
			[]string{"issue", "view", "1"}, EnvOverride{Host: "GitHub.com"}, VerdictAllow},

		// --hostname + GH_HOST both set: denies regardless (host reason),
		// with argv --hostname taking precedence over GH_HOST — mirroring
		// -R/--repo's precedence over GH_REPO (GH-4986).
		{"--hostname and GH_HOST both non-github.com denies",
			[]string{"api", "--hostname", "ghe.example.com", "user"}, EnvOverride{Host: "other.example.com"}, VerdictDeny},
		{"argv --hostname=github.com wins over conflicting GH_HOST",
			[]string{"api", "--hostname", "github.com", "user"}, EnvOverride{Host: "ghe.example.com"}, VerdictAllow},
		{"argv --hostname=ghe wins over conflicting GH_HOST=github.com — still denied",
			[]string{"api", "--hostname", "ghe.example.com", "user"}, EnvOverride{Host: "github.com"}, VerdictDeny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(id, tt.args, tt.env)
			if got.Verdict != tt.wantVerdict {
				t.Fatalf("Classify(%v, env=%+v) = %s (reason: %q), want %s", tt.args, tt.env, got.Verdict, got.Reason, tt.wantVerdict)
			}
			if got.Verdict == VerdictDeny {
				if got.Reason == "" {
					t.Errorf("deny decision missing Reason")
				}
				if got.Allowed == "" {
					t.Errorf("deny decision missing Allowed hint")
				}
			}
		})
	}
}

// TestClassify_EnvBypassJournalDistinguishable verifies the GH-4968
// requirement that a denial driven by GH_REPO/GH_HOST is distinguishable
// from an argv -R/--repo denial: the Decision carries the env-derived
// value (which cmd/pilot/ghguard.go copies into the journal entry) and the
// Reason text itself names the env var rather than "-R/--repo".
func TestClassify_EnvBypassJournalDistinguishable(t *testing.T) {
	id := testIdentity()

	envDeny := Classify(id, []string{"issue", "comment", "1", "--body", "x"}, EnvOverride{Repo: "other/repo"})
	if envDeny.Verdict != VerdictDeny {
		t.Fatalf("expected deny, got %s", envDeny.Verdict)
	}
	if envDeny.EnvRepo != "other/repo" {
		t.Errorf("expected Decision.EnvRepo = %q, got %q", "other/repo", envDeny.EnvRepo)
	}
	if !strings.Contains(envDeny.Reason, "GH_REPO") {
		t.Errorf("expected env-derived deny reason to mention GH_REPO, got %q", envDeny.Reason)
	}

	argvDeny := Classify(id, []string{"issue", "comment", "1", "--body", "x", "-R", "other/repo"}, EnvOverride{})
	if argvDeny.Verdict != VerdictDeny {
		t.Fatalf("expected deny, got %s", argvDeny.Verdict)
	}
	if argvDeny.EnvRepo != "" {
		t.Errorf("expected Decision.EnvRepo empty for an argv-driven denial, got %q", argvDeny.EnvRepo)
	}
	if strings.Contains(argvDeny.Reason, "GH_REPO") {
		t.Errorf("argv-driven deny reason should not mention GH_REPO, got %q", argvDeny.Reason)
	}

	hostDeny := Classify(id, []string{"api", "user"}, EnvOverride{Host: "ghe.example.com"})
	if hostDeny.Verdict != VerdictDeny {
		t.Fatalf("expected deny, got %s", hostDeny.Verdict)
	}
	if hostDeny.EnvHost != "ghe.example.com" {
		t.Errorf("expected Decision.EnvHost = %q, got %q", "ghe.example.com", hostDeny.EnvHost)
	}
	if !strings.Contains(hostDeny.Reason, "GH_HOST") {
		t.Errorf("expected GH_HOST-driven deny reason to mention GH_HOST, got %q", hostDeny.Reason)
	}

	// --hostname (argv) is the GH-4986 twin: same EnvHost field (shared
	// journal schema), but the Reason names --hostname instead of GH_HOST so
	// the two are still distinguishable in the audit trail.
	hostnameDeny := Classify(id, []string{"api", "--hostname", "ghe.example.com", "user"}, EnvOverride{})
	if hostnameDeny.Verdict != VerdictDeny {
		t.Fatalf("expected deny, got %s", hostnameDeny.Verdict)
	}
	if hostnameDeny.EnvHost != "ghe.example.com" {
		t.Errorf("expected Decision.EnvHost = %q, got %q", "ghe.example.com", hostnameDeny.EnvHost)
	}
	if !strings.Contains(hostnameDeny.Reason, "--hostname") {
		t.Errorf("expected --hostname-driven deny reason to mention --hostname, got %q", hostnameDeny.Reason)
	}
	if strings.Contains(hostnameDeny.Reason, "GH_HOST") {
		t.Errorf("--hostname-driven deny reason should not mention GH_HOST, got %q", hostnameDeny.Reason)
	}
}

// TestClassify_IncompleteIdentity verifies that when TaskRepo/TaskBranch/
// TaskIssue are empty (identity incomplete), repo-mismatch checks are
// skipped (unenforceable) but own-artifact checks still deny rather than
// guess, and hard denies are unaffected.
func TestClassify_IncompleteIdentity(t *testing.T) {
	id := Identity{} // fully empty

	// Reads still work — the allowlist doesn't require identity.
	if got := Classify(id, []string{"issue", "view", "1"}, EnvOverride{}); got.Verdict != VerdictAllow {
		t.Errorf("read with empty identity: got %s, want allow", got.Verdict)
	}

	// Own-artifact still denies — can't confirm ownership without identity.
	if got := Classify(id, []string{"issue", "comment", "1", "--body", "hi"}, EnvOverride{}); got.Verdict != VerdictDeny {
		t.Errorf("issue comment with empty identity: got %s, want deny", got.Verdict)
	}
	if got := Classify(id, []string{"pr", "create", "--head", "some-branch"}, EnvOverride{}); got.Verdict != VerdictDeny {
		t.Errorf("pr create --head with empty identity: got %s, want deny", got.Verdict)
	}

	// pr create with no --head at all still allows (defaults to current branch).
	if got := Classify(id, []string{"pr", "create", "--title", "t"}, EnvOverride{}); got.Verdict != VerdictAllow {
		t.Errorf("pr create no --head with empty identity: got %s, want allow", got.Verdict)
	}
}

func TestExtractIssueOrPRNumber(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"42", "42", true},
		{"0", "0", true},
		{"https://github.com/qf-studio/pilot/issues/4671", "4671", true},
		{"https://github.com/qf-studio/pilot/pull/99", "99", true},
		{"https://github.com/qf-studio/pilot/pull/99/", "99", true},
		{"https://github.com/qf-studio/pilot/pull/99?tab=files", "99", true},
		{"some-branch-name", "", false},
		{"", "", false},
		{"pilot/GH-4671", "", false},
	}
	for _, tt := range tests {
		got, ok := extractIssueOrPRNumber(tt.in)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("extractIssueOrPRNumber(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestParseArgs_FlagAliases(t *testing.T) {
	p := parseArgs([]string{"issue", "view", "1", "-R", "o/r"})
	if !p.repoGiven || p.repo != "o/r" {
		t.Errorf("-R short flag not parsed: %+v", p)
	}

	p = parseArgs([]string{"issue", "view", "1", "--repo", "o/r"})
	if !p.repoGiven || p.repo != "o/r" {
		t.Errorf("--repo long flag not parsed: %+v", p)
	}

	p = parseArgs([]string{"api", "x", "--method", "POST"})
	if p.method != "POST" {
		t.Errorf("--method not parsed: %+v", p)
	}

	p = parseArgs([]string{"api", "x", "-X", "DELETE"})
	if p.method != "DELETE" {
		t.Errorf("-X short flag not parsed: %+v", p)
	}
}

// TestParseArgs_PflagShapes asserts the derived parsedArgs fields
// (method/hasFieldFlag/hasInputFlag/repo/head) directly for the pflag
// shapes GH-4963 closes parity gaps on — attached shorthand, `=` forms,
// boolean bundling, and last-occurrence-wins — independent of the
// resulting Classify verdict (covered separately in TestClassify).
func TestParseArgs_PflagShapes(t *testing.T) {
	p := parseArgs([]string{"api", "x", "-XPOST"})
	if p.method != "POST" {
		t.Errorf("attached -XPOST: method = %q, want POST: %+v", p.method, p)
	}

	p = parseArgs([]string{"api", "x", "-X=POST"})
	if p.method != "POST" {
		t.Errorf("-X=POST: method = %q, want POST: %+v", p.method, p)
	}

	p = parseArgs([]string{"api", "x", "-fstate=closed"})
	if !p.hasFieldFlag {
		t.Errorf("attached -fstate=closed: hasFieldFlag = false, want true: %+v", p)
	}

	p = parseArgs([]string{"api", "x", "--input=payload.json"})
	if !p.hasInputFlag || p.hasFieldFlag {
		t.Errorf("--input=payload.json: hasInputFlag=%v hasFieldFlag=%v, want true/false: %+v", p.hasInputFlag, p.hasFieldFlag, p)
	}

	// Last occurrence wins for the method scalar.
	p = parseArgs([]string{"api", "x", "-X", "GET", "-XPOST"})
	if p.method != "POST" {
		t.Errorf("repeated -X: method = %q, want POST (last wins): %+v", p.method, p)
	}

	// Boolean bundling: -i (include) doesn't swallow -X's value.
	p = parseArgs([]string{"api", "x", "-iX", "POST"})
	if p.method != "POST" {
		t.Errorf("bundled -iX POST: method = %q, want POST: %+v", p.method, p)
	}

	// Attached -R on a command outside `api`.
	p = parseArgs([]string{"issue", "view", "1", "-Rqf-studio/pilot"})
	if !p.repoGiven || p.repo != "qf-studio/pilot" {
		t.Errorf("attached -R: repoGiven=%v repo=%q: %+v", p.repoGiven, p.repo, p)
	}

	// pr create: -f is boolean --fill, does not consume --head's value.
	p = parseArgs([]string{"pr", "create", "-f", "--head", "other-branch"})
	if !p.headGiven || p.head != "other-branch" {
		t.Errorf("pr create -f --head: headGiven=%v head=%q, want true/other-branch: %+v", p.headGiven, p.head, p)
	}

	// -- terminates flag parsing: a literal "--head" after -- is positional.
	p = parseArgs([]string{"pr", "create", "--head", "keep-me", "--", "--head"})
	if p.head != "keep-me" || len(p.positional) != 1 || p.positional[0] != "--head" {
		t.Errorf("-- termination: head=%q positional=%v, want keep-me/[--head]: %+v", p.head, p.positional, p)
	}

	// Unknown flag on a strict command sets parseIssue; on a lenient
	// (kindRead) command it does not.
	p = parseArgs([]string{"api", "x", "--not-a-real-flag", "v"})
	if p.parseIssue == "" {
		t.Errorf("api unknown flag: parseIssue empty, want non-empty: %+v", p)
	}
	p = parseArgs([]string{"issue", "view", "1", "--not-a-real-flag", "v"})
	if p.parseIssue != "" {
		t.Errorf("issue view unknown flag: parseIssue = %q, want empty (lenient): %+v", p.parseIssue, p)
	}

	// --hostname (GH-4986): recognized on api, sets host/hostGiven.
	p = parseArgs([]string{"api", "--hostname", "ghe.example.com", "user"})
	if !p.hostGiven || p.host != "ghe.example.com" {
		t.Errorf("--hostname not parsed: hostGiven=%v host=%q, want true/ghe.example.com: %+v", p.hostGiven, p.host, p)
	}

	// api graphql: the endpoint lands as the first positional, not the sub
	// (api has no subcommand in this grammar).
	p = parseArgs([]string{"api", "graphql", "-f", "query=x"})
	if len(p.positional) == 0 || p.positional[0] != "graphql" || !p.hasFieldFlag {
		t.Errorf("api graphql: positional=%v hasFieldFlag=%v, want [graphql]/true: %+v", p.positional, p.hasFieldFlag, p)
	}
}

func TestJournal_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")

	// Missing file reads as empty, not an error.
	entries, err := ReadJournal(path)
	if err != nil {
		t.Fatalf("ReadJournal on missing file: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries, got %d", len(entries))
	}

	e1 := JournalEntry{Verdict: VerdictDeny, Reason: "test deny 1", Args: []string{"issue", "close", "1"}, TaskIssue: "4671", TaskRepo: "qf-studio/pilot"}
	e2 := JournalEntry{Verdict: VerdictDeny, Reason: "test deny 2", Args: []string{"release", "create", "v1"}, TaskIssue: "4671", TaskRepo: "qf-studio/pilot"}

	if err := AppendJournal(path, e1); err != nil {
		t.Fatalf("AppendJournal e1: %v", err)
	}
	if err := AppendJournal(path, e2); err != nil {
		t.Fatalf("AppendJournal e2: %v", err)
	}

	entries, err = ReadJournal(path)
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Reason != e1.Reason || entries[1].Reason != e2.Reason {
		t.Errorf("journal entries out of order or corrupted: %+v", entries)
	}
}

func TestResolveFallbackGh_ExcludesShimDir(t *testing.T) {
	dir := t.TempDir()
	shimDir := filepath.Join(dir, "shim")
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// A "gh" in the shim dir (simulating the shim itself sitting on PATH).
	writeExecutable(t, filepath.Join(shimDir, "gh"), "#!/bin/sh\necho shim\n")
	// The "real" gh elsewhere on PATH.
	writeExecutable(t, filepath.Join(realDir, "gh"), "#!/bin/sh\necho real\n")

	oldPath := os.Getenv("PATH")
	defer func() { _ = os.Setenv("PATH", oldPath) }()
	_ = os.Setenv("PATH", shimDir+string(os.PathListSeparator)+realDir)

	got, err := ResolveFallbackGh(shimDir)
	if err != nil {
		t.Fatalf("ResolveFallbackGh: %v", err)
	}
	if got != filepath.Join(realDir, "gh") {
		t.Errorf("ResolveFallbackGh = %q, want %q (should skip shim dir)", got, filepath.Join(realDir, "gh"))
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
