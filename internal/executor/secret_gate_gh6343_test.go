package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

// GH-6343: five more ways through the repository the session can write to
// publish a secret or to run a program of the session inside a daemon git
// command (a non-UTF-8 commit message, a pre-push hook, submodule recursion,
// an annotated tag for the autopilot sites, a signature program). Each test
// pushes through the real push method on real temporary repositories.

// writeMarkerScript writes an executable shell script that creates marker, and
// returns its path.
func writeMarkerScript(t *testing.T, marker string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func assertNoMarker(t *testing.T, marker, what string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("%s ran: git executed a program the session chose", what)
	}
}

// commitWithConfig stages file and commits with extra git -c arguments.
func (f *secretGateFixture) commitWithConfig(file, content, msg string, config ...string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.local, file), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.run("add", file)
	var args []string
	for _, c := range config {
		args = append(args, "-c", c)
	}
	f.run(append(args, "commit", "-m", msg)...)
}

// mutation pin: remove the encoding check -> this test fails
func TestSecretGate_CommitInIBM037EncodingIsRefused(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.commitWithConfig("notes.md", "clean\n", "docs: "+secretGateValue, "i18n.commitEncoding=IBM037")
	if enc := f.run("log", "-1", "--format=%e"); enc != "IBM037" {
		t.Fatalf("fixture commit has the encoding %q, want IBM037", enc)
	}
	before := f.remoteState()

	err := f.pushRefusedNothingPublished(before)
	if !strings.Contains(err.Error(), "has the encoding IBM037; only UTF-8 commit messages are pushed") {
		t.Errorf("the refusal is not the encoding refusal: %v", err)
	}
	if strings.Contains(err.Error(), secretGateValue) {
		t.Errorf("error holds the value: %v", err)
	}
}

// mutation pin: remove the encoding check -> this test fails
func TestSecretGate_MislabelledEncodingHidesSecretAndIsRefused(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	// The message bytes are UTF-8 but the header says IBM037: git re-encodes them
	// as if they were EBCDIC, so a reader that decodes sees noise while the remote
	// receives the secret as written.
	tree := f.run("rev-parse", "HEAD^{tree}")
	parent := f.run("rev-parse", "HEAD")
	obj := "tree " + tree + "\nparent " + parent + "\n" +
		"author t <t@example.com> 1700000000 +0000\ncommitter t <t@example.com> 1700000000 +0000\n" +
		"encoding IBM037\n\ntoken = " + secretGateValue + "\n"
	sha := f.hashObject("commit", obj)
	f.run("update-ref", "refs/heads/pilot/GH-9", sha)
	before := f.remoteState()

	err := f.pushRefusedNothingPublished(before)
	if !strings.Contains(err.Error(), "has the encoding IBM037") {
		t.Errorf("the refusal is not the encoding refusal: %v", err)
	}
}

// hashObject writes an object of the given type from text and returns its id.
func (f *secretGateFixture) hashObject(kind, text string) string {
	f.t.Helper()
	tmp := filepath.Join(f.t.TempDir(), "obj")
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return f.run("hash-object", "-t", kind, "-w", tmp)
}

func TestSecretGate_Utf8EncodingNamesAreNotRefused(t *testing.T) {
	for _, name := range []string{"UTF-8", "utf-8", "UTF8", "utf8"} {
		t.Run(name, func(t *testing.T) {
			f := newSecretGateFixture(t)
			tree := f.run("rev-parse", "HEAD^{tree}")
			parent := f.run("rev-parse", "HEAD")
			obj := "tree " + tree + "\nparent " + parent + "\n" +
				"author t <t@example.com> 1700000000 +0000\ncommitter t <t@example.com> 1700000000 +0000\n" +
				"encoding " + name + "\n\nclean\n"
			f.run("update-ref", "refs/heads/pilot/GH-9", f.hashObject("commit", obj))

			if err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9"); err != nil {
				t.Fatalf("push of a %s commit: %v", name, err)
			}
		})
	}
}

// mutation pin: remove the i18n.logOutputEncoding value -> this test fails
func TestSecretGate_LogOutputEncodingConfigDoesNotHideSecret(t *testing.T) {
	f := newSecretGateFixture(t)
	secretpatterns.RegisterSecretValues(secretGateValue)
	f.run("config", "i18n.logOutputEncoding", "UTF-16")
	f.commit("notes.md", "clean\n", "docs: "+secretGateValue)
	before := f.remoteState()

	f.assertPushRefusedNothingPublished(before)
}

// mutation pin: remove hooks off from pushArgs -> this test fails
func TestSecretGate_PrePushHookDoesNotRunInPush(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "hook-ran")
	for name, push := range map[string]func(f *secretGateFixture) error{
		"Push": func(f *secretGateFixture) error {
			return f.git.Push(context.Background(), "pilot/GH-9", "GH-9")
		},
		"PushForceWithLease": func(f *secretGateFixture) error {
			return f.git.PushForceWithLease(context.Background(), "pilot/GH-9", "", "GH-9")
		},
		"PushToMain": func(f *secretGateFixture) error {
			f.run("checkout", "main")
			f.commit("main.go", "package main\n", "feat: main")
			return f.git.PushToMain(context.Background(), "GH-9")
		},
	} {
		t.Run(name, func(t *testing.T) {
			_ = os.Remove(marker)
			f := newSecretGateFixture(t)
			f.commit("notes.md", "clean\n", "docs: notes")
			hook := filepath.Join(f.gitDir(), "hooks", "pre-push")
			if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}

			if err := push(f); err != nil {
				t.Fatalf("clean push: %v", err)
			}
			assertNoMarker(t, marker, "the pre-push hook")
		})
	}
}

// mutation pin: remove the hooks off value from gitScanConfig -> this test fails
func TestSecretGate_GateCommandsDoNotRunHooks(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "hook-ran")
	f := newSecretGateFixture(t)
	f.commit("notes.md", "clean\n", "docs: notes")
	hooksDir := filepath.Join(f.gitDir(), "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ntouch " + marker + "\n"
	for _, h := range []string{"pre-push", "reference-transaction", "post-checkout", "post-commit"} {
		if err := os.WriteFile(filepath.Join(hooksDir, h), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckSecretGate(context.Background(), f.local, "pilot/GH-9", "GH-9"); err != nil {
		t.Fatalf("gate on clean commits: %v", err)
	}
	assertNoMarker(t, marker, "a hook")
	if _, err := os.Stat(secretpatterns.NoHooksPath); err == nil {
		t.Errorf("the hooks path %s exists", secretpatterns.NoHooksPath)
	}
}

// mutation pin: remove --no-recurse-submodules and the push.recurseSubmodules value -> this test fails
func TestSecretGate_SubmoduleBranchIsNotPushedOnDemand(t *testing.T) {
	// A submodule push runs git in a submodule with the file transport restricted.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")

	f := newSecretGateFixture(t)
	root := t.TempDir()
	subRemote := filepath.Join(root, "sub-remote.git")
	subSeed := filepath.Join(root, "sub-seed")
	mustGit(t, root, "init", "--bare", "-b", "main", subRemote)
	mustGit(t, root, "init", "-b", "main", subSeed)
	mustGit(t, subSeed, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-m", "sub init")
	mustGit(t, subSeed, "push", subRemote, "main")

	f.run("submodule", "add", subRemote, "sub")
	f.run("commit", "-m", "chore: add submodule")
	f.run("push", "origin", "pilot/GH-9")
	f.run("config", "push.recurseSubmodules", "on-demand")

	sub := filepath.Join(f.local, "sub")
	mustGit(t, sub, "checkout", "-b", "pilot/GH-9")
	mustGit(t, sub, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-m", "sub change")
	f.run("add", "sub")
	f.run("commit", "-m", "chore: bump submodule")
	subBefore := mustGit(t, subRemote, "for-each-ref") + mustGit(t, subRemote, "rev-list", "--all", "--count")

	if err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9"); err != nil {
		t.Fatalf("push: %v", err)
	}
	if after := mustGit(t, subRemote, "for-each-ref") + mustGit(t, subRemote, "rev-list", "--all", "--count"); after != subBefore {
		t.Errorf("the push changed the remote of the submodule:\nbefore:\n%s\nafter:\n%s", subBefore, after)
	}
}

// signedLookingCommit writes a commit on top of HEAD with a gpgsig header that
// holds sig (continuation lines indented by one space) and moves pilot/GH-9 to it.
func (f *secretGateFixture) signedLookingCommit(sig string) {
	f.t.Helper()
	tree := f.run("rev-parse", "HEAD^{tree}")
	parent := f.run("rev-parse", "HEAD")
	obj := "tree " + tree + "\nparent " + parent + "\n" +
		"author t <t@example.com> 1700000000 +0000\ncommitter t <t@example.com> 1700000000 +0000\n" +
		"gpgsig " + strings.ReplaceAll(strings.TrimSuffix(sig, "\n"), "\n", "\n ") + "\n\nclean\n"
	f.run("update-ref", "refs/heads/pilot/GH-9", f.hashObject("commit", obj))
}

// mutation pin: remove --no-show-signature and the log.showSignature value -> this test fails
func TestSecretGate_SignatureProgramDoesNotRunWhenShowSignatureIsTrue(t *testing.T) {
	f := newSecretGateFixture(t)
	gpgMarker := filepath.Join(t.TempDir(), "gpg-ran")
	sshMarker := filepath.Join(t.TempDir(), "ssh-keygen-ran")
	f.run("config", "log.showSignature", "true")
	f.run("config", "gpg.program", writeMarkerScript(t, gpgMarker))
	// A signature of the ssh format is verified by gpg.ssh.program, which
	// gpg.program does not replace.
	f.run("config", "gpg.ssh.program", writeMarkerScript(t, sshMarker))
	f.signedLookingCommit("-----BEGIN SSH SIGNATURE-----\nU1NIU0lHAAAAAQ==\n-----END SSH SIGNATURE-----\n")
	if out := f.run("log", "-1", "--format=%s"); out == "" {
		t.Fatal("fixture commit is unreadable")
	}
	_ = os.Remove(gpgMarker)
	_ = os.Remove(sshMarker)

	if err := CheckSecretGate(context.Background(), f.local, "pilot/GH-9", "GH-9"); err != nil {
		t.Fatalf("gate on a clean signed-looking commit: %v", err)
	}
	assertNoMarker(t, gpgMarker, "gpg.program")
	assertNoMarker(t, sshMarker, "gpg.ssh.program")
}

// mutation pin: remove the first-push upstream (-u) -> this test fails
func TestSecretGate_CleanPushStillPassesAndSetsUpstream(t *testing.T) {
	t.Run("Push", func(t *testing.T) {
		f := newSecretGateFixture(t)
		f.commit("notes.md", "clean\n", "docs: notes")
		if err := f.git.Push(context.Background(), "pilot/GH-9", "GH-9"); err != nil {
			t.Fatalf("clean push: %v", err)
		}
		f.assertUpstreamAndRemote()
	})
	t.Run("PushForceWithLease", func(t *testing.T) {
		f := newSecretGateFixture(t)
		f.commit("notes.md", "clean\n", "docs: notes")
		if err := f.git.PushForceWithLease(context.Background(), "pilot/GH-9", "", "GH-9"); err != nil {
			t.Fatalf("clean push with lease: %v", err)
		}
		f.assertUpstreamAndRemote()
	})
	t.Run("PushToMain", func(t *testing.T) {
		f := newSecretGateFixture(t)
		f.run("checkout", "main")
		f.commit("main.go", "package main\n", "feat: main")
		if err := f.git.PushToMain(context.Background(), "GH-9"); err != nil {
			t.Fatalf("clean push to main: %v", err)
		}
		if local, remote := f.run("rev-parse", "main"), strings.TrimSpace(mustGit(t, f.remote, "rev-parse", "main")); local != remote {
			t.Errorf("remote main = %s, want %s", remote, local)
		}
	})
}

func (f *secretGateFixture) assertUpstreamAndRemote() {
	f.t.Helper()
	if up := f.run("rev-parse", "--abbrev-ref", "pilot/GH-9@{upstream}"); up != "origin/pilot/GH-9" {
		f.t.Errorf("upstream = %q, want origin/pilot/GH-9", up)
	}
	local, remote := f.run("rev-parse", "pilot/GH-9"), strings.TrimSpace(mustGit(f.t, f.remote, "rev-parse", "refs/heads/pilot/GH-9"))
	if local != remote {
		f.t.Errorf("remote branch = %s, want %s", remote, local)
	}
}
