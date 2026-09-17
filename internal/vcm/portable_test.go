package vcm

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishAndSnapshotRoundTrip(t *testing.T) {
	e := fixture(t, 1)
	localRoot := mustGit(t, e.Root, "remote", "get-url", "origin")
	localChild := e.Config.Children[0].URL
	mustGit(t, e.Root, "remote", "set-url", "--push", "origin", localRoot)
	mustGit(t, e.Root, "remote", "set-url", "origin", "https://example.test/root.git")
	child := filepath.Join(e.Root, e.Config.Children[0].Path)
	mustGit(t, child, "remote", "set-url", "--push", "origin", localChild)
	mustGit(t, child, "remote", "set-url", "origin", "https://example.test/child.git")
	e.Config.Children[0].URL = "https://example.test/child.git"
	saveContractConfig(t, e)
	e, err := Open(e.Root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.Create("portable")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, m.Repositories[1].Path, "feature.txt", "feature\n")
	if err = e.Publish(m); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "other")
	mustGit(t, e.Root, "clone", localRoot, root)
	mustGit(t, root, "remote", "set-url", "origin", "https://example.test/root.git")
	mustGit(t, root, "clone", localChild, filepath.Join(root, e.Config.Children[0].Path))
	mustGit(t, filepath.Join(root, e.Config.Children[0].Path), "remote", "set-url", "origin", "https://example.test/child.git")
	// Populate objects from local test transports without changing trusted repository identities.
	for i, remote := range []string{localRoot, localChild} {
		origin := root
		if i > 0 {
			origin = filepath.Join(root, e.Config.Children[0].Path)
		}
		mustGit(t, origin, "fetch", remote, "refs/heads/"+m.Tag)
	}
	other, err := Open(root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.Export(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range s.Repositories {
		if r.PublicationRef != "refs/heads/"+m.Tag {
			t.Fatal("missing publication ref")
		}
	}
	restored, err := other.Restore(s, "restored", false)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Tag == m.Tag {
		t.Fatal("identity reused")
	}
	for i, r := range restored.Repositories {
		if got := mustGit(t, r.Path, "rev-parse", "HEAD"); got != s.Repositories[i].Source {
			t.Fatal("source mismatch")
		}
		if r.Base != s.Repositories[i].Base {
			t.Fatal("base mismatch")
		}
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), m.Workspace) {
		t.Fatal("local workspace leaked")
	}
}
func TestExportRejectsDirtyAndSnapshotMismatch(t *testing.T) {
	e := fixture(t, 0)
	mustGit(t, e.Root, "remote", "set-url", "origin", "https://example.test/root.git")
	m, err := e.Create("portable")
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(m.Workspace, "new.txt"), "work")
	if _, err = e.Export(m); err == nil {
		t.Fatal("dirty export accepted")
	}
	if err = os.Remove(filepath.Join(m.Workspace, "new.txt")); err != nil {
		t.Fatal(err)
	}
	s, err := e.Export(m)
	if err != nil {
		t.Fatal(err)
	}
	s.Repositories[0].URL = "https://untrusted.invalid/repo"
	if _, err = e.Restore(s, "unsafe", true); err == nil {
		t.Fatal("untrusted URL accepted")
	}
}
func TestPublishPreflightsAllAndDryRunDoesNotContactRemote(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("portable")
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(m.Workspace, "new.txt"), "dirty")
	if err = e.Publish(m); err == nil {
		t.Fatal("dirty publication accepted")
	}
	if got := mustGit(t, e.Root, "ls-remote", m.Repositories[1].Repository.URL, "refs/heads/"+m.Tag); got != "" {
		t.Fatal("child published before complete preflight")
	}
	mustGit(t, m.Repositories[1].Path, "remote", "set-url", "--push", "origin", "/missing/remote")
	p := e.PublishPlan(m)
	if len(p.Unverified) == 0 {
		t.Fatal("remote verification not described")
	}
}
func TestPortableURLRemovesCredentials(t *testing.T) {
	got := portableURL("https://user:secret@example.test/repo?token=secret#secret")
	if got != "https://example.test/repo" {
		t.Fatal(got)
	}
}

func TestRestoreResumesJournalWithoutHooks(t *testing.T) {
	e := fixture(t, 0)
	e.Config.Root.Hooks = Hooks{HookCreateBefore: {{ID: "must-not-run", Shell: "exit 99"}}, HookCreateAfter: {{ID: "must-not-run", Shell: "exit 99"}}}
	saveContractConfig(t, e)
	mustGit(t, e.Root, "remote", "set-url", "origin", "https://example.test/root.git")
	sha := mustGit(t, e.Root, "rev-parse", "HEAD")
	s := &Snapshot{Version: 1, Tag: newTag("original"), Repositories: []SnapshotRepository{{Name: "root", Path: ".", URL: "https://example.test/root.git", Trunk: "main", Base: sha, Source: sha}}}
	m, err := e.snapshotManifest(s, "resumed")
	if err != nil {
		t.Fatal(err)
	}
	m.Repositories[0].Intent = "create"
	if err = e.store.save(m); err != nil {
		t.Fatal(err)
	}
	mustGit(t, e.Root, "worktree", "add", "-b", m.Tag, m.Workspace, sha)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hooks := mustGit(t, e.Root, "rev-parse", "--git-path", "hooks")
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(e.Root, hooks)
	}
	hook := filepath.Join(hooks, "post-checkout")
	put(t, hook, "#!/bin/sh\ntouch '"+marker+"'\n")
	if err = os.Chmod(hook, 0755); err != nil {
		t.Fatal(err)
	}
	p := e.RestorePlan(s, "resumed", false)
	if p.Tag != m.Tag || len(p.Blockers) > 0 {
		t.Fatalf("resume preview: %+v", p)
	}
	restored, err := e.Restore(s, "resumed", false)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Tag != m.Tag || restored.State != "ready" {
		t.Fatal("journal not resumed")
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("hook executed")
	}
	p = e.RestorePlan(s, "resumed", true)
	if len(p.Blockers) == 0 {
		t.Fatal("existing ready Change not blocked")
	}
	_, err = e.Restore(s, "fresh", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("hook executed during fresh restore")
	}
}
func TestRestorePreviewRejectsNonCommitWithFetch(t *testing.T) {
	e := fixture(t, 0)
	mustGit(t, e.Root, "remote", "set-url", "origin", "https://example.test/root.git")
	sha := mustGit(t, e.Root, "rev-parse", "HEAD^{tree}")
	s := &Snapshot{Version: 1, Tag: newTag("original"), Repositories: []SnapshotRepository{{Name: "root", Path: ".", URL: "https://example.test/root.git", Trunk: "main", Base: sha, Source: sha}}}
	p := e.RestorePlan(s, "invalid", true)
	if len(p.Blockers) == 0 {
		t.Fatal("non-commit treated as remotely verifiable")
	}
}

func TestRestoreFetchesMissingObjectsFromTrustedOrigin(t *testing.T) {
	e := fixture(t, 0)
	local := mustGit(t, e.Root, "remote", "get-url", "origin")
	second := filepath.Join(t.TempDir(), "second")
	mustGit(t, e.Root, "clone", local, second)
	portable := "ssh://example.test/workspace.git"
	mustGit(t, e.Root, "remote", "set-url", "--push", "origin", local)
	mustGit(t, e.Root, "remote", "set-url", "origin", portable)
	m, err := e.Create("portable")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, m.Workspace, "feature.txt", "feature")
	if err = e.Publish(m); err != nil {
		t.Fatal(err)
	}
	s, err := e.Export(m)
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, second, "remote", "set-url", "origin", portable)
	other, err := Open(second, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	p := other.RestorePlan(s, "copy", true)
	if len(p.Blockers) > 0 || len(p.Unverified) == 0 {
		t.Fatalf("missing remote object preview: %+v", p)
	}
	if _, err = other.Restore(s, "copy", false); err == nil {
		t.Fatal("missing object accepted")
	}
	script := filepath.Join(t.TempDir(), "ssh")
	put(t, script, "#!/bin/sh\nexec git-upload-pack \"$VCM_TEST_REMOTE\"\n")
	if err = os.Chmod(script, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", script)
	t.Setenv("GIT_SSH_VARIANT", "ssh")
	t.Setenv("VCM_TEST_REMOTE", local)
	restored, err := other.Restore(s, "copy", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, restored.Workspace, "rev-parse", "HEAD"); got != s.Repositories[0].Source {
		t.Fatal("wrong restored source")
	}
}
func TestPublishReportsPartialFailureAndRetry(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("partial")
	if err != nil {
		t.Fatal(err)
	}
	remote := m.Repositories[0].Repository.URL
	hook := filepath.Join(remote, "hooks", "pre-receive")
	put(t, hook, "#!/bin/sh\nexit 1\n")
	if err = os.Chmod(hook, 0755); err != nil {
		t.Fatal(err)
	}
	err = e.Publish(m)
	pe, ok := err.(*PublicationError)
	if !ok || pe.Blocked != "root" || len(pe.Completed) != 1 || pe.Completed[0] != "repo0" {
		t.Fatalf("wrong partial error: %#v", err)
	}
	if err = os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err = e.Publish(m); err != nil {
		t.Fatal(err)
	}
}
func TestPublishRejectsDriftBeforeNextRepository(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("drift")
	if err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(m.Repositories[1].Origin, ".git", "hooks", "pre-push")
	put(t, hook, "#!/bin/sh\nunset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE\ngit -C \"$VCM_TEST_ROOT\" -c core.hooksPath=/dev/null commit --allow-empty -m drift >/dev/null\n")
	if err = os.Chmod(hook, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VCM_TEST_ROOT", m.Workspace)
	err = e.Publish(m)
	pe, ok := err.(*PublicationError)
	if !ok || pe.Blocked != "root" || len(pe.Completed) != 1 {
		t.Fatalf("wrong drift error: %#v", err)
	}
	if got := mustGit(t, e.Root, "ls-remote", m.Repositories[0].Repository.URL, "refs/heads/"+m.Tag); got != "" {
		t.Fatal("drifted root published")
	}
}

func TestPublishRejectsDivergentRemoteBeforePublishing(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("divergent")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Publish(m); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "remote-work")
	mustGit(t, e.Root, "clone", "--branch", m.Tag, m.Repositories[1].Repository.URL, clone)
	commitFile(t, clone, "remote.txt", "remote work")
	mustGit(t, clone, "push", "origin", m.Tag)
	commitFile(t, m.Repositories[1].Path, "local.txt", "local work")
	commitFile(t, m.Workspace, "local.txt", "root work")
	rootBefore := mustGit(t, e.Root, "ls-remote", m.Repositories[0].Repository.URL, "refs/heads/"+m.Tag)
	err = e.Publish(m)
	pe, ok := err.(*PublicationError)
	if !ok || pe.Blocked != "repo0" || len(pe.Completed) != 0 {
		t.Fatalf("wrong divergence error: %#v", err)
	}
	if got := mustGit(t, e.Root, "ls-remote", m.Repositories[0].Repository.URL, "refs/heads/"+m.Tag); got != rootBefore {
		t.Fatal("root published before preflight completed")
	}
}
