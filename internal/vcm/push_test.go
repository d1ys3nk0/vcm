package vcm

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func remoteURL(t *testing.T, path string) string {
	t.Helper()
	return mustGit(t, path, "remote", "get-url", "origin")
}

func remoteBranches(t *testing.T, remote string) string {
	t.Helper()
	return mustGit(t, remote, "for-each-ref", "--format=%(refname)=%(objectname)", "refs/heads")
}

func advanceRemote(t *testing.T, remote, filename string) {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "clone")
	mustGit(t, filepath.Dir(clone), "clone", remote, clone)
	mustGit(t, clone, "config", "user.name", "VCM Test")
	mustGit(t, clone, "config", "user.email", "vcm@example.test")
	commitFile(t, clone, filename, "remote\n")
	mustGit(t, clone, "push", "origin", "main")
}

func TestPushPublishesDependenciesBeforeRoot(t *testing.T) {
	e := fixture(t, 2)
	e.Config.Children[0].DependsOn = []string{"repo1"}
	commitFile(t, filepath.Join(e.Root, "repo0"), "local.txt", "repo0\n")
	commitFile(t, filepath.Join(e.Root, "repo1"), "local.txt", "repo1\n")
	commitFile(t, e.Root, "local.txt", "root\n")

	var output bytes.Buffer
	e.Out = &output
	if err := e.Push(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(e.Root, "repo0"), filepath.Join(e.Root, "repo1"), e.Root} {
		remote := remoteURL(t, path)
		if got, want := mustGit(t, remote, "rev-parse", "refs/heads/main"), mustGit(t, path, "rev-parse", "refs/heads/main"); got != want {
			t.Fatalf("remote %s = %s, want %s", remote, got, want)
		}
		if got, want := mustGit(t, path, "rev-parse", "refs/remotes/origin/main"), mustGit(t, path, "rev-parse", "refs/heads/main"); got != want {
			t.Fatalf("cached origin/main for %s = %s, want %s", path, got, want)
		}
	}
	report, err := e.Tree(e.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range report.Repositories {
		if repository.SyncState != "current" || repository.Ahead != 0 || repository.Behind != 0 {
			t.Fatalf("repository %s status after push: %+v", repository.Name, repository)
		}
	}
	log := output.String()
	repo1 := strings.Index(log, "[push/repo1 @ "+filepath.Join(e.Root, "repo1")+"] pushed trunk main")
	repo0 := strings.Index(log, "[push/repo0 @ "+filepath.Join(e.Root, "repo0")+"] pushed trunk main")
	root := strings.Index(log, "[push/root @ "+e.Root+"] pushed trunk main")
	if repo1 < 0 || repo0 < repo1 || root < repo0 {
		t.Fatalf("push order does not follow dependencies with root last:\n%s", log)
	}
}

func TestPushPreflightFailuresLeaveEveryRemoteUnchanged(t *testing.T) {
	tests := map[string]func(*testing.T, *Engine){
		"path is not checkout root": func(t *testing.T, e *Engine) {
			path := filepath.Join(e.Root, "repo1")
			if err := os.Rename(path, path+"-moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"dirty checkout": func(t *testing.T, e *Engine) {
			put(t, filepath.Join(e.Root, "repo1", "dirty.txt"), "dirty\n")
		},
		"unfinished operation": func(t *testing.T, e *Engine) {
			path := filepath.Join(e.Root, "repo1")
			mergeHead := mustGit(t, path, "rev-parse", "--git-path", "MERGE_HEAD")
			if !filepath.IsAbs(mergeHead) {
				mergeHead = filepath.Join(path, mergeHead)
			}
			put(t, mergeHead, mustGit(t, path, "rev-parse", "HEAD")+"\n")
		},
		"wrong branch": func(t *testing.T, e *Engine) {
			mustGit(t, filepath.Join(e.Root, "repo1"), "checkout", "-b", "topic")
		},
		"missing origin": func(t *testing.T, e *Engine) {
			mustGit(t, filepath.Join(e.Root, "repo1"), "remote", "remove", "origin")
		},
		"mismatched origin": func(t *testing.T, e *Engine) {
			mustGit(t, filepath.Join(e.Root, "repo1"), "remote", "set-url", "origin", filepath.Join(t.TempDir(), "wrong.git"))
		},
		"missing remote trunk": func(t *testing.T, e *Engine) {
			remote := remoteURL(t, filepath.Join(e.Root, "repo1"))
			mustGit(t, remote, "update-ref", "-d", "refs/heads/main")
		},
		"behind remote trunk": func(t *testing.T, e *Engine) {
			advanceRemote(t, remoteURL(t, filepath.Join(e.Root, "repo1")), "remote.txt")
		},
		"divergent remote trunk": func(t *testing.T, e *Engine) {
			commitFile(t, filepath.Join(e.Root, "repo1"), "local-divergence.txt", "local\n")
			advanceRemote(t, remoteURL(t, filepath.Join(e.Root, "repo1")), "remote-divergence.txt")
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			e := fixture(t, 2)
			commitFile(t, filepath.Join(e.Root, "repo0"), "candidate.txt", "child\n")
			commitFile(t, e.Root, "candidate.txt", "root\n")
			setup(t, e)
			remotes := []string{e.Config.Children[0].URL, e.Config.Children[1].URL, remoteURL(t, e.Root)}
			before := map[string]string{}
			for _, remote := range remotes {
				before[remote] = remoteBranches(t, remote)
			}
			if err := e.Push(); err == nil {
				t.Fatal("invalid workspace passed push preflight")
			}
			for _, remote := range remotes {
				if after := remoteBranches(t, remote); after != before[remote] {
					t.Fatalf("remote %s changed after preflight failure: %q -> %q", remote, before[remote], after)
				}
			}
		})
	}
}

func TestPushIsIdempotentAndRetryableAfterPartialPublication(t *testing.T) {
	e := fixture(t, 1)
	child := filepath.Join(e.Root, "repo0")
	childRemote := remoteURL(t, child)
	rootRemote := remoteURL(t, e.Root)
	rootBefore := mustGit(t, rootRemote, "rev-parse", "refs/heads/main")
	commitFile(t, child, "candidate.txt", "child\n")
	commitFile(t, e.Root, "candidate.txt", "root\n")

	hook := filepath.Join(rootRemote, "hooks", "pre-receive")
	put(t, hook, "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.Push(); err == nil {
		t.Fatal("root remote rejection did not fail push")
	}
	if got, want := mustGit(t, childRemote, "rev-parse", "refs/heads/main"), mustGit(t, child, "rev-parse", "HEAD"); got != want {
		t.Fatalf("child was not published before root failure: got %s, want %s", got, want)
	}
	if got := mustGit(t, rootRemote, "rev-parse", "refs/heads/main"); got != rootBefore {
		t.Fatalf("rejected root unexpectedly changed: got %s, want %s", got, rootBefore)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := e.Push(); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := e.Push(); err != nil {
		t.Fatalf("already-current retry: %v", err)
	}
	if got, want := mustGit(t, rootRemote, "rev-parse", "refs/heads/main"), mustGit(t, e.Root, "rev-parse", "HEAD"); got != want {
		t.Fatalf("root retry did not publish: got %s, want %s", got, want)
	}
}

func TestPushRejectsLaterLocalDrift(t *testing.T) {
	e := fixture(t, 1)
	rootBefore := mustGit(t, e.Root, "rev-parse", "HEAD")
	commitFile(t, filepath.Join(e.Root, "repo0"), "publish", "publish")
	hook := filepath.Join(e.Root, "repo0", ".git", "hooks", "pre-push")
	put(t, hook, "#!/bin/sh\ngit -C '"+e.Root+"' -c core.hooksPath=/dev/null commit --allow-empty -m 'feat: concurrent' >/dev/null\n")
	if err := os.Chmod(hook, 0755); err != nil {
		t.Fatal(err)
	}
	err := e.Push()
	if err == nil || !strings.Contains(err.Error(), "changed after publication preflight") {
		t.Fatalf("expected drift error: %v", err)
	}
	if got := mustGit(t, remoteURL(t, e.Root), "rev-parse", "main"); got != rootBefore {
		t.Fatal("unvalidated root revision published")
	}
}
