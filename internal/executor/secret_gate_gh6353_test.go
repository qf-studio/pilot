package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// GH-6353: three more ways through the repository the session can write to run
// a program of the session inside a daemon git command or to publish bytes the
// gate did not read: a hooks directory the session can write, the co-author
// strip without the gate's git settings, and a NUL byte in a commit message.
// Each test pushes through the real push method on real temporary repositories.

const claudeCoAuthorTrailer = "Co-Authored-By: Claude <noreply@anthropic.com>"

// gateSite is one executor push site: prepare builds the commits it publishes,
// push runs it.
type gateSite struct {
	prepare func(f *secretGateFixture)
	push    func(f *secretGateFixture) error
}

var gateBranchSites = map[string]gateSite{
	"Push": {
		prepare: func(f *secretGateFixture) { f.commit("notes.md", "clean\n", "docs: notes") },
		push:    func(f *secretGateFixture) error { return f.git.Push(context.Background(), "pilot/GH-9", "GH-9") },
	},
	"PushForceWithLease": {
		prepare: func(f *secretGateFixture) { f.commit("notes.md", "clean\n", "docs: notes") },
		push: func(f *secretGateFixture) error {
			return f.git.PushForceWithLease(context.Background(), "pilot/GH-9", "", "GH-9")
		},
	},
}

// gateSites is gateBranchSites plus PushToMain.
func gateSites() map[string]gateSite {
	sites := map[string]gateSite{
		"PushToMain": {
			prepare: func(f *secretGateFixture) {
				f.run("checkout", "main")
				f.commit("main.go", "package main\n", "feat: main")
			},
			push: func(f *secretGateFixture) error { return f.git.PushToMain(context.Background(), "GH-9") },
		},
	}
	for name, s := range gateBranchSites {
		sites[name] = s
	}
	return sites
}

// recordGitArgv puts a git in front of PATH that records its arguments and runs
// the real git. Each git process the daemon starts is recorded; git's own child
// processes resolve git in its exec path and are not. stop restores PATH and
// returns the recorded argument lists.
func recordGitArgv(t *testing.T) (stop func() [][]string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logFile := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\037' \"$a\"; done >> '" + logFile + "'\n" +
		"printf '\\036' >> '" + logFile + "'\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	return func() [][]string {
		t.Helper()
		if err := os.Setenv("PATH", oldPath); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatalf("no git command was recorded: %v", err)
		}
		var argvs [][]string
		for _, rec := range strings.Split(string(data), "\x1e") {
			if rec == "" {
				continue
			}
			argvs = append(argvs, strings.Split(strings.TrimSuffix(rec, "\x1f"), "\x1f"))
		}
		return argvs
	}
}

// gitSubcommand is the first argument of argv after its -c pairs.
func gitSubcommand(argv []string) string {
	for i := 0; i < len(argv); i++ {
		if argv[i] == "-c" {
			i++
			continue
		}
		return argv[i]
	}
	return ""
}

// hasConfig reports whether argv passes -c value.
func hasConfig(argv []string, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-c" && argv[i+1] == value {
			return true
		}
	}
	return false
}

// branchMessages is the messages of the commits the remote branch holds above main.
func (f *secretGateFixture) branchMessages() string {
	f.t.Helper()
	return mustGit(f.t, f.remote, "log", "--format=%B", "main..refs/heads/pilot/GH-9")
}

// writeHooks writes an executable script per hook name into dir that creates marker.
func writeHooks(t *testing.T, dir, marker string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range names {
		if err := os.WriteFile(filepath.Join(dir, h), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// requireMarker fails the test when marker is missing: the fixture's program
// would not have run anyway, so a missing marker after the push proves nothing.
func requireMarker(t *testing.T, marker, what string) {
	t.Helper()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fixture: %s does not run in this repository at all", what)
	}
}

// mutation pin: create a real directory for the hooks path again -> this test fails
func TestSecretGate_HooksPathIsTheConstantAndNoDirectoryIsCreated(t *testing.T) {
	for name, site := range gateSites() {
		t.Run(name, func(t *testing.T) {
			f := newSecretGateFixture(t)
			site.prepare(f)
			// The strip rewrites, so its commands are recorded too.
			f.commit("more.md", "clean\n", "docs: more\n\n"+claudeCoAuthorTrailer)
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)

			stop := recordGitArgv(t)
			err := site.push(f)
			argvs := stop()
			if err != nil {
				t.Fatalf("clean push: %v", err)
			}

			if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "pilot-empty-hooks*")); len(left) > 0 {
				t.Errorf("a hooks directory exists after the push: %v", left)
			}
			if _, err := os.Stat(secretpatterns.NoHooksPath); err == nil {
				t.Errorf("the hooks path %s exists", secretpatterns.NoHooksPath)
			}
			want := "core.hooksPath=" + secretpatterns.NoHooksPath
			pushes := 0
			for _, argv := range argvs {
				// The default-branch lookup of PushToMain (GetDefaultBranch) is a daemon
				// command outside the gate and the push (TASK-651).
				if len(argv) > 0 && (argv[0] == "symbolic-ref" || argv[0] == "show-ref") {
					continue
				}
				if !hasConfig(argv, want) {
					t.Errorf("git %q lacks -c %s", argv, want)
				}
				for _, a := range argv {
					if a == "push" {
						pushes++
						break
					}
				}
			}
			if pushes != 1 || len(argvs) < 5 {
				t.Errorf("recorded %d git commands with %d pushes: the recorder missed the push", len(argvs), pushes)
			}
		})
	}
}

// mutation pin: drop hooks off from pushArgs or from GitScanConfig -> this test fails
func TestSecretGate_RepositoryAndConfiguredHooksDoNotRunInPush(t *testing.T) {
	for _, where := range []string{"hooks directory", "core.hooksPath"} {
		for name, site := range gateSites() {
			t.Run(where+"/"+name, func(t *testing.T) {
				marker := filepath.Join(t.TempDir(), "hook-ran")
				f := newSecretGateFixture(t)
				site.prepare(f)
				hooksDir := filepath.Join(f.gitDir(), "hooks")
				if where == "core.hooksPath" {
					hooksDir = t.TempDir()
					f.run("config", "core.hooksPath", hooksDir)
				}
				writeHooks(t, hooksDir, marker, "pre-push", "reference-transaction")

				if err := site.push(f); err != nil {
					t.Fatalf("clean push: %v", err)
				}
				assertNoMarker(t, marker, "a pre-push or reference-transaction hook")

				f.run("update-ref", "refs/pilot-sanity", "HEAD")
				requireMarker(t, marker, "the reference-transaction hook")
			})
		}
	}
}

// signedTrailerCommit writes a commit on top of HEAD with an OpenPGP gpgsig
// header and the co-author trailer, and moves pilot/GH-9 to it. The tree is
// HEAD's, so the index stays valid for the amend.
func (f *secretGateFixture) signedTrailerCommit() string {
	f.t.Helper()
	tree := f.run("rev-parse", "HEAD^{tree}")
	parent := f.run("rev-parse", "HEAD")
	sig := "-----BEGIN PGP SIGNATURE-----\n\niQEzBAABCAAdFiEE\n-----END PGP SIGNATURE-----"
	obj := "tree " + tree + "\nparent " + parent + "\n" +
		"author t <t@example.com> 1700000000 +0000\ncommitter t <t@example.com> 1700000000 +0000\n" +
		"gpgsig " + strings.ReplaceAll(sig, "\n", "\n ") + "\n\ndocs: signed\n\n" + claudeCoAuthorTrailer + "\n"
	sha := f.hashObject("commit", obj)
	f.run("update-ref", "refs/heads/pilot/GH-9", sha)
	return sha
}

// mutation pin: build one strip command with exec.CommandContext directly -> this test fails
func TestSecretGate_CoAuthorStripRunsNoSignatureProgram(t *testing.T) {
	for name, site := range gateBranchSites {
		for _, path := range []string{"amend", "rewrite"} {
			t.Run(name+"/"+path, func(t *testing.T) {
				marker := filepath.Join(t.TempDir(), "gpg-ran")
				f := newSecretGateFixture(t)
				if path == "rewrite" {
					f.commit("a.md", "a\n", "docs: a\n\n"+claudeCoAuthorTrailer)
				}
				signed := f.signedTrailerCommit()
				f.run("config", "log.showSignature", "true")
				f.run("config", "gpg.program", writeMarkerScript(t, marker))
				// The amend and commit-tree sign when commit.gpgSign is true.
				f.run("config", "commit.gpgSign", "true")

				if err := site.push(f); err != nil {
					t.Fatalf("push: %v", err)
				}
				assertNoMarker(t, marker, "gpg.program")
				if msgs := f.branchMessages(); strings.Contains(msgs, "anthropic.com") || !strings.Contains(msgs, "docs: signed") {
					t.Errorf("the strip did not run; the remote holds:\n%s", msgs)
				}

				f.run("log", "-1", "--format=%s", signed)
				requireMarker(t, marker, "gpg.program")
			})
		}
	}
}

// mutation pin: build one strip command with exec.CommandContext directly -> this test fails
func TestSecretGate_CoAuthorStripRunsNoHook(t *testing.T) {
	for name, site := range gateBranchSites {
		for _, path := range []string{"amend", "rewrite"} {
			t.Run(name+"/"+path, func(t *testing.T) {
				marker := filepath.Join(t.TempDir(), "hook-ran")
				f := newSecretGateFixture(t)
				if path == "rewrite" {
					f.commit("a.md", "a\n", "docs: a\n\n"+claudeCoAuthorTrailer)
				}
				f.commit("b.md", "b\n", "docs: b\n\n"+claudeCoAuthorTrailer)
				writeHooks(t, filepath.Join(f.gitDir(), "hooks"), marker,
					"post-commit", "prepare-commit-msg", "post-rewrite", "reference-transaction")
				f.run("config", "core.fsmonitor", writeMarkerScript(t, marker))

				stop := recordGitArgv(t)
				err := site.push(f)
				argvs := stop()
				if err != nil {
					t.Fatalf("push: %v", err)
				}
				assertNoMarker(t, marker, "a commit hook or the fsmonitor program")
				// commit-tree runs no hook and does not sign on its own, so its
				// settings are checked on the command line: every write of the strip
				// is built by gitHardened.
				writes := map[string]int{}
				for _, argv := range argvs {
					sub := gitSubcommand(argv)
					if sub != "commit" && sub != "commit-tree" && sub != "update-ref" {
						continue
					}
					writes[sub]++
					for _, c := range []string{"core.hooksPath=" + secretpatterns.NoHooksPath, "commit.gpgSign=false", "core.fsmonitor=false", "gpg.program=false"} {
						if !hasConfig(argv, c) {
							t.Errorf("strip command git %q lacks -c %s", argv, c)
						}
					}
				}
				if path == "amend" && writes["commit"] != 1 || path == "rewrite" && (writes["commit-tree"] != 2 || writes["update-ref"] != 1) {
					t.Errorf("recorded strip writes %v, want the %s path", writes, path)
				}
				msgs := f.branchMessages()
				if strings.Contains(msgs, "anthropic.com") || !strings.Contains(msgs, "docs: b") {
					t.Errorf("the strip did not run; the remote holds:\n%s", msgs)
				}

				f.run("update-ref", "refs/pilot-sanity", "HEAD")
				requireMarker(t, marker, "the reference-transaction hook")
			})
		}
	}
}

// A replace ref of the tree object alone changes no id the strip reads (%T
// prints the id in the commit), so the session replaces the head commit with
// one that names another tree: that is the tree a strip honouring replace refs
// would read and publish.
//
// mutation pin: strip reads run without GIT_NO_REPLACE_OBJECTS -> this test fails
func TestSecretGate_CoAuthorStripIgnoresReplaceRefs(t *testing.T) {
	f := newSecretGateFixture(t)
	f.commit("a.md", "a\n", "docs: a\n\n"+claudeCoAuthorTrailer)
	fakeTree := f.run("rev-parse", "HEAD^{tree}")
	f.commit("b.md", "real\n", "docs: b\n\n"+claudeCoAuthorTrailer)
	head, realTree, parent := f.run("rev-parse", "HEAD"), f.run("rev-parse", "HEAD^{tree}"), f.run("rev-parse", "HEAD^")
	fake := f.hashObject("commit", "tree "+fakeTree+"\nparent "+parent+"\n"+
		"author t <t@example.com> 1700000000 +0000\ncommitter t <t@example.com> 1700000000 +0000\n\n"+
		"docs: b\n\n"+claudeCoAuthorTrailer+"\n")
	f.run("replace", head, fake)
	if got := f.run("log", "-1", "--format=%T", head); got != fakeTree {
		t.Fatalf("fixture: the replace ref is not honoured (%s)", got)
	}

	if err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9"); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := strings.TrimSpace(mustGit(t, f.remote, "rev-parse", "refs/heads/pilot/GH-9^{tree}")); got != realTree {
		t.Errorf("the remote head has the tree %s, want the real tree %s", got, realTree)
	}
	if msgs := f.branchMessages(); strings.Contains(msgs, "anthropic.com") {
		t.Errorf("the strip did not run; the remote holds:\n%s", msgs)
	}
}

// mutation pin: remove checkCommitNUL -> this test fails
func TestSecretGate_CommitWithNULByteIsRefused(t *testing.T) {
	for name, rest := range map[string]string{"secret after the NUL": secretGateValue, "no secret": "more text"} {
		t.Run(name, func(t *testing.T) {
			f := newSecretGateFixture(t)
			secretpatterns.RegisterSecretValues(secretGateValue)
			tree := f.run("rev-parse", "HEAD^{tree}")
			parent := f.run("rev-parse", "HEAD")
			obj := "tree " + tree + "\nparent " + parent + "\n" +
				"author t <t@example.com> 1700000000 +0000\ncommitter t <t@example.com> 1700000000 +0000\n" +
				"\nclean\x00" + rest + "\n"
			file := filepath.Join(t.TempDir(), "obj")
			if err := os.WriteFile(file, []byte(obj), 0o644); err != nil {
				t.Fatal(err)
			}
			// --literally: git's own object check refuses a NUL in a commit body.
			sha := f.run("hash-object", "-t", "commit", "-w", "--literally", file)
			f.run("update-ref", "refs/heads/pilot/GH-9", sha)
			if msg := f.run("log", "-1", "--format=%B", sha); msg != "clean" {
				t.Fatalf("fixture: git prints the message as %q, want it cut at the NUL", msg)
			}
			before := f.remoteState()

			err := f.pushRefusedNothingPublished(before)
			if want := "commit " + sha[:12] + " holds a NUL byte; the commit is not pushed"; !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q does not hold %q", err, want)
			}
			if strings.Contains(err.Error(), secretGateValue) {
				t.Errorf("error holds the value: %v", err)
			}
		})
	}
}

// The autopilot push sites are covered by TestConflictPush_CleanPushStillPasses
// and TestConflictPush_HooksDoNotRunInPush.
//
// mutation pin: a NUL check that refuses every commit, or a strip command that fails -> this test fails
func TestSecretGate_CleanAndUtf8PushesPassAndStripStillWorks(t *testing.T) {
	const subject = "docs: Grüße, naïve ✓ 日本"
	for name, site := range gateSites() {
		t.Run(name, func(t *testing.T) {
			f := newSecretGateFixture(t)
			secretpatterns.RegisterSecretValues(secretGateValue)
			site.prepare(f)
			f.commit("utf8.md", "Grüße\n", subject+"\n\n"+claudeCoAuthorTrailer)

			if err := site.push(f); err != nil {
				t.Fatalf("push: %v", err)
			}
			if name == "PushToMain" {
				if local, remote := f.run("rev-parse", "main"), strings.TrimSpace(mustGit(t, f.remote, "rev-parse", "main")); local != remote {
					t.Errorf("remote main = %s, want %s", remote, local)
				}
				return
			}
			msgs := f.branchMessages()
			if !strings.Contains(msgs, subject) {
				t.Errorf("the remote lacks the UTF-8 message:\n%s", msgs)
			}
			if strings.Contains(msgs, "anthropic.com") {
				t.Errorf("the strip did not remove the trailer:\n%s", msgs)
			}
		})
	}
}
