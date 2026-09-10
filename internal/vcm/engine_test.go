package vcm

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"go.yaml.in/yaml/v3"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mustGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	s, e := git(path, args...)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func put(t *testing.T, path, s string) {
	t.Helper()
	if e := os.WriteFile(path, []byte(s), 0644); e != nil {
		t.Fatal(e)
	}
}
func commitFile(t *testing.T, path, name, content string) {
	t.Helper()
	put(t, filepath.Join(path, name), content)
	mustGit(t, path, "add", "--", name)
	mustGit(t, path, "commit", "-m", "feat: update "+name)
}
func fixture(t *testing.T, n int) *Engine {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "VCM Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "vcm@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "VCM Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "vcm@example.test")
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	root := filepath.Join(dir, "workspace")
	mustGit(t, dir, "init", "--initial-branch=main", root)
	mustGit(t, root, "config", "user.name", "VCM Test")
	mustGit(t, root, "config", "user.email", "vcm@example.test")
	c := Config{Version: 1, Root: Root{Trunk: "main"}, Children: []Repository{}}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("repo%d", i)
		remote := filepath.Join(dir, name+".git")
		seed := filepath.Join(dir, "seed"+name)
		mustGit(t, dir, "init", "--bare", "--initial-branch=main", remote)
		mustGit(t, dir, "init", "--initial-branch=main", seed)
		commitFile(t, seed, "file.txt", "base\n")
		mustGit(t, seed, "remote", "add", "origin", remote)
		mustGit(t, seed, "push", "origin", "main")
		c.Children = append(c.Children, Repository{Name: name, Path: name, URL: remote, Trunk: "main"})
	}
	data := "version: 1\nroot:\n  trunk: main\nchildren:\n"
	ignores := ""
	for _, r := range c.Children {
		data += fmt.Sprintf("  - name: %s\n    path: %s\n    url: %s\n    trunk: main\n", r.Name, r.Path, r.URL)
		ignores += "/" + r.Path + "/\n"
	}
	if n == 0 {
		data = "version: 1\nroot:\n  trunk: main\nchildren: []\n"
	}
	put(t, filepath.Join(root, "vcm.yml"), data)
	put(t, filepath.Join(root, ".gitignore"), ignores)
	mustGit(t, root, "add", ".")
	mustGit(t, root, "commit", "-m", "feat: workspace")
	remote := filepath.Join(dir, "workspace.git")
	mustGit(t, dir, "init", "--bare", "--initial-branch=main", remote)
	mustGit(t, root, "remote", "add", "origin", remote)
	mustGit(t, root, "push", "origin", "main")
	e, err := Open(root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Mutate(e.Bootstrap); err != nil {
		t.Fatal(err)
	}
	return e
}
func TestBootstrapAndDiscovery(t *testing.T) {
	e := fixture(t, 1)
	if err := e.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	root, err := discover(filepath.Join(e.Root, "repo0"))
	if err != nil || root != e.Root {
		t.Fatalf("discovery: %s %v", root, err)
	}
	mustGit(t, filepath.Join(e.Root, "repo0"), "remote", "set-url", "origin", "wrong")
	if err = e.Bootstrap(); err == nil {
		t.Fatal("accepted wrong remote")
	}
}
func TestCreateMergeLifecycle(t *testing.T) {
	e := fixture(t, 2)
	m, err := e.Create("feature-1")
	if err != nil {
		t.Fatal(err)
	}
	wantWorkspace := filepath.Join(filepath.Dir(e.Root), filepath.Base(e.Root)+"."+m.Tag)
	if m.Workspace != wantWorkspace {
		t.Fatalf("workspace = %q, want %q", m.Workspace, wantWorkspace)
	}
	for i := 1; i < len(m.Repositories); i++ {
		commitFile(t, m.Repositories[i].Path, "feature.txt", "feature\n")
	}
	commitFile(t, m.Workspace, "product.txt", "new\n")
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	if m.State != "dropped" {
		t.Fatal(m.State)
	}
	for _, r := range m.Repositories {
		if _, err = os.Stat(r.Path); !os.IsNotExist(err) {
			t.Fatalf("worktree remains: %s", r.Path)
		}
		if r.Repository.Name != "root" {
			if _, err = os.Stat(filepath.Join(r.Origin, "feature.txt")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
}

func TestCreatePreflightsAllRepositoriesBeforeCreatingWorktrees(t *testing.T) {
	e := fixture(t, 2)
	dirty := filepath.Join(e.Root, "repo1")
	put(t, filepath.Join(dirty, "uncommitted.txt"), "preserve me\n")

	origins := []string{e.Root, filepath.Join(e.Root, "repo0"), dirty}
	worktreesBefore := make([]string, len(origins))
	for i, origin := range origins {
		worktreesBefore[i] = mustGit(t, origin, "worktree", "list", "--porcelain")
	}

	if _, err := e.Create("preflight"); err == nil || !strings.Contains(err.Error(), "dirty checkout "+dirty) {
		t.Fatalf("expected dirty repository rejection: %v", err)
	}
	for i, origin := range origins {
		if after := mustGit(t, origin, "worktree", "list", "--porcelain"); after != worktreesBefore[i] {
			t.Fatalf("repository %s gained a worktree before preflight completed", origin)
		}
	}
	all, err := e.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("create recorded state before preflight completed: %d manifests", len(all))
	}
}

func TestCreateSelectsExactRepositories(t *testing.T) {
	t.Run("only", func(t *testing.T) {
		e := fixture(t, 2)
		log := filepath.Join(filepath.Dir(e.Root), "selected-hooks")
		t.Setenv("VCM_TEST_SELECTED_HOOKS", log)
		for i := range e.Config.Children {
			e.Config.Children[i].Hooks = Hooks{HookCreateAfter: {{ID: "record", Shell: `printf '%s\n' "$VCM_REPOSITORY_NAME" >> "$VCM_TEST_SELECTED_HOOKS"`}}}
		}
		saveContractConfig(t, e)
		m, err := e.CreateSelected("partial", "repo1", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Repositories) != 2 || m.Repositories[0].Repository.Name != "root" || m.Repositories[1].Repository.Name != "repo1" {
			t.Fatalf("unexpected repository selection: %+v", m.Repositories)
		}
		if _, err := os.Lstat(filepath.Join(m.Workspace, "repo0")); !os.IsNotExist(err) {
			t.Fatal("unselected repository worktree was created")
		}
		data, err := os.ReadFile(log)
		if err != nil || string(data) != "repo1\n" {
			t.Fatalf("unexpected selected hook output %q: %v", data, err)
		}
		commitFile(t, m.Repositories[1].Path, "partial.txt", "selected\n")
		if err = e.Merge(m); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(filepath.Join(m.Repositories[1].Origin, "partial.txt")); err != nil {
			t.Fatal("selected repository was not merged:", err)
		}
		if _, err = os.Stat(filepath.Join(e.Root, "repo0", "partial.txt")); !os.IsNotExist(err) {
			t.Fatal("unselected repository was changed")
		}
	})
	t.Run("except all children", func(t *testing.T) {
		e := fixture(t, 2)
		m, err := e.CreateSelected("root-only", "", "repo0,repo1")
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Repositories) != 1 || m.Repositories[0].Repository.Name != "root" {
			t.Fatalf("root-only selection contains children: %+v", m.Repositories)
		}
	})
}

func TestCreateSelectionValidation(t *testing.T) {
	e := fixture(t, 2)
	e.Config.Children[0].DependsOn = []string{"repo1"}
	for name, selection := range map[string][2]string{
		"mutually exclusive": {"repo0", "repo1"},
		"blank":              {"repo0,,repo1", ""},
		"duplicate":          {"repo0,repo0", ""},
		"root":               {"root", ""},
		"unknown":            {"missing", ""},
		"dependency":         {"repo0", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := e.CreateSelected("invalid-"+strings.ReplaceAll(name, " ", "-"), selection[0], selection[1]); err == nil {
				t.Fatal("invalid selection accepted")
			}
		})
	}
}

func TestInterruptedCreateRequiresMatchingSelection(t *testing.T) {
	e := fixture(t, 2)
	e.Config.Children[0].Hooks = Hooks{HookCreateAfter: {{ID: "blocked", Shell: "false"}}}
	saveContractConfig(t, e)
	m, err := e.CreateSelected("resume-selection", "repo0", "")
	if err == nil || m == nil || m.State != "creating" {
		t.Fatalf("expected interrupted creation: %v", err)
	}
	if _, err = e.CreateSelected("resume-selection", "repo1", ""); err == nil || !strings.Contains(err.Error(), "selection does not match") {
		t.Fatalf("mismatched selection accepted: %v", err)
	}
}

func TestCreateBeforeFailureLeavesNoWorktreeAndResumesRecordedSynchronization(t *testing.T) {
	e := fixture(t, 1)
	allow := filepath.Join(filepath.Dir(e.Root), "allow-create-before")
	e.Config.Root.Hooks = Hooks{HookCreateBefore: {{ID: "gate", Shell: `test -f "` + allow + `"`}}}
	saveContractConfig(t, e)
	m, err := e.Create("before-worktree")
	if err == nil || m == nil || m.State != "creating" {
		t.Fatalf("expected resumable create-before failure: %v", err)
	}
	bases := []string{m.Repositories[0].Base, m.Repositories[1].Base}
	for _, repository := range m.Repositories {
		if repository.Owned {
			t.Fatalf("repository %s recorded ownership before worktree creation", repository.Repository.Name)
		}
		if _, statErr := os.Stat(repository.Path); !os.IsNotExist(statErr) {
			t.Fatalf("worktree exists after create-before failure: %s", repository.Path)
		}
	}
	put(t, allow, "allowed")
	m, err = e.Create("before-worktree")
	if err != nil {
		t.Fatal(err)
	}
	for i, repository := range m.Repositories {
		if repository.Base != bases[i] {
			t.Fatalf("repository %s repeated completed synchronization", repository.Repository.Name)
		}
	}
}

func TestRemovedSelectedRepositoryBlocksMutationButStatusReportsIt(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("missing-config")
	if err != nil {
		t.Fatal(err)
	}
	e.Config.Children = []Repository{}
	e.store.config = e.Config
	m, err = e.store.load(m.Tag)
	if err != nil {
		t.Fatal(err)
	}
	report := e.Status(m)
	if len(report.Repositories) != 2 || !strings.Contains(report.Repositories[1].Error, "absent from current configuration") {
		t.Fatalf("status did not report missing selected repository: %+v", report.Repositories)
	}
	if err = e.Merge(m); err == nil || !strings.Contains(err.Error(), "restore its vcm.yml entry") {
		t.Fatalf("merge did not block missing selected repository: %v", err)
	}
	e.Force = true
	if err = e.Drop(m); err == nil || !strings.Contains(err.Error(), "restore its vcm.yml entry") {
		t.Fatalf("drop did not block missing selected repository: %v", err)
	}
}

func TestRepositoryAddedDuringActiveChangeIsSkippedByMergeAndDrop(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("added-later")
	if err != nil {
		t.Fatal(err)
	}
	added := Repository{Name: "added", Path: "added", URL: filepath.Join(filepath.Dir(e.Root), "not-needed.git"), Trunk: "main"}
	e.Config.Children = append(e.Config.Children, added)
	saveContractConfig(t, e)
	e.store.config = e.Config
	m, err = e.store.load(m.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Repositories) != 2 {
		t.Fatalf("newly configured repository entered active Change: %+v", m.Repositories)
	}
	commitFile(t, m.Repositories[1].Path, "selected.txt", "merged\n")
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	if m.State != "dropped" {
		t.Fatalf("merge/drop did not complete: %s", m.State)
	}
	if _, err = os.Lstat(filepath.Join(m.Workspace, added.Path)); !os.IsNotExist(err) {
		t.Fatal("newly configured repository gained a Change worktree")
	}
	if _, err = os.Stat(filepath.Join(m.Repositories[1].Origin, "selected.txt")); err != nil {
		t.Fatal("recorded repository was not merged:", err)
	}
}

func TestConflictPreflightDoesNotPartiallyMerge(t *testing.T) {
	e := fixture(t, 2)
	m, err := e.Create("conflict")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, m.Repositories[1].Path, "feature.txt", "new\n")
	r := m.Repositories[2]
	commitFile(t, r.Path, "file.txt", "source\n")
	commitFile(t, r.Origin, "file.txt", "target\n")
	before := mustGit(t, m.Repositories[1].Origin, "rev-parse", "HEAD")
	if err = e.Merge(m); err == nil {
		t.Fatal("expected conflict")
	}
	if after := mustGit(t, m.Repositories[1].Origin, "rev-parse", "HEAD"); before != after {
		t.Fatal("first repository merged before global conflict check")
	}
}
func TestPostMergeFailureResumesWithoutDuplicate(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("retry")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, m.Repositories[1].Path, "feature.txt", "new\n")
	sentinel := filepath.Join(filepath.Dir(e.Root), "allow")
	e.Config.Root.Hooks = Hooks{HookMergeAfter: {{ID: "gate", Shell: "test -f " + sentinel}}}
	saveContractConfig(t, e)
	if err = e.Merge(m); err == nil {
		t.Fatal("expected hook failure")
	}
	target := mustGit(t, m.Repositories[1].Origin, "rev-parse", "HEAD")
	if !m.Repositories[1].Merged {
		t.Fatal("downstream not checkpointed")
	}
	if m.State != "merge-finalizing" {
		t.Fatalf("post-cleanup failure state: %s", m.State)
	}
	for _, repository := range m.Repositories {
		if _, statErr := os.Stat(repository.Path); !os.IsNotExist(statErr) {
			t.Fatalf("worktree remains after cleanup: %s", repository.Path)
		}
	}
	put(t, sentinel, "yes")
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	if target != mustGit(t, m.Repositories[1].Origin, "rev-parse", "HEAD") {
		t.Fatal("duplicate merge")
	}
}

func TestDropAfterFailureResumesAfterWorktreeRemoval(t *testing.T) {
	e := fixture(t, 1)
	allow := filepath.Join(filepath.Dir(e.Root), "allow-drop-after")
	log := filepath.Join(filepath.Dir(e.Root), "drop-after-log")
	e.Config.Children[0].Hooks = Hooks{HookDropAfter: {
		{ID: "record-once", Shell: `printf 'once\n' >> "` + log + `"`},
		{ID: "finalize", Shell: `test -f "` + allow + `"`},
	}}
	saveContractConfig(t, e)
	m, err := e.Create("drop-after-retry")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Drop(m); err == nil {
		t.Fatal("expected drop-after failure")
	}
	if !m.Repositories[1].Removed {
		t.Fatal("child removal was not checkpointed")
	}
	if _, err = os.Stat(m.Repositories[1].Path); !os.IsNotExist(err) {
		t.Fatal("child worktree remains after removal")
	}
	put(t, allow, "allowed")
	m, err = e.store.load(m.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Drop(m); err != nil {
		t.Fatal(err)
	}
	if m.State != "dropped" {
		t.Fatalf("drop retry state: %s", m.State)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "once\n" {
		t.Fatalf("completed drop-after hook repeated: %q %v", data, err)
	}
}

func TestInterruptedRemovalRunsAfterHookWithoutRepeatingRemoval(t *testing.T) {
	e := fixture(t, 1)
	log := filepath.Join(filepath.Dir(e.Root), "interrupted-removal-log")
	e.Config.Children[0].Hooks = Hooks{HookDropAfter: {{ID: "record", Shell: `printf 'after\n' >> "` + log + `"`}}}
	saveContractConfig(t, e)
	m, err := e.Create("interrupted-removal")
	if err != nil {
		t.Fatal(err)
	}
	r := &m.Repositories[1]
	r.Intent = "remove"
	m.State = "dropping"
	if err = e.store.save(m); err != nil {
		t.Fatal(err)
	}
	mustGit(t, r.Origin, "worktree", "remove", r.Path)
	if err = e.Drop(m); err != nil {
		t.Fatal(err)
	}
	if !r.Removed || r.Intent != "" {
		t.Fatalf("interrupted removal not checkpointed: %+v", *r)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "after\n" {
		t.Fatalf("drop-after outcome: %q %v", data, err)
	}
}

func TestMergeRejectsTargetDriftBeforeFurtherIntegration(t *testing.T) {
	e := fixture(t, 2)
	allow := filepath.Join(filepath.Dir(e.Root), "allow-second-merge")
	e.Config.Children[1].Hooks = Hooks{HookMergeBefore: {{ID: "gate", Shell: `test -f "` + allow + `"`}}}
	saveContractConfig(t, e)
	m, err := e.Create("target-drift")
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range m.Repositories[1:] {
		commitFile(t, repository.Path, "feature.txt", "reviewed\n")
	}
	if err = e.Merge(m); err == nil {
		t.Fatal("expected second repository gate failure")
	}
	if !m.Repositories[1].Merged {
		t.Fatal("first completed integration was not checkpointed")
	}
	firstTarget := m.Repositories[1].Target
	commitFile(t, e.Root, "unexpected.txt", "target drift\n")
	put(t, allow, "allowed")
	m, err = e.store.load(m.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Merge(m); err == nil || !strings.Contains(err.Error(), "target changed after merge gate") {
		t.Fatalf("target drift accepted: %v", err)
	}
	if got := mustGit(t, m.Repositories[1].Origin, "rev-parse", "HEAD"); got != firstTarget {
		t.Fatal("completed integration changed while rejecting target drift")
	}
	if m.Repositories[2].Merged {
		t.Fatal("pending repository integrated before target drift rejection")
	}
}
func TestHooksDirtyFailureAndEnvironment(t *testing.T) {
	e := fixture(t, 1)
	e.Config.Children[0].Hooks = Hooks{HookCreateAfter: {{ID: "inspect", Shell: `test "$VCM_REPOSITORY_NAME" = repo0; test "$PWD" = "$VCM_REPOSITORY_PATH"; test -d "$VCM_ROOT"; echo preserved > generated.txt`}}}
	configBytes, err := yaml.Marshal(e.Config)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, e.Root, "vcm.yml", string(configBytes))
	mustGit(t, e.Root, "push", "origin", "main")
	m, err := e.Create("hook")
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("expected dirty rejection: %v", err)
	}
	if _, err = os.Stat(filepath.Join(m.Repositories[1].Path, "generated.txt")); err != nil {
		t.Fatal(err)
	}
	mustGit(t, m.Repositories[1].Path, "add", "generated.txt")
	mustGit(t, m.Repositories[1].Path, "commit", "-m", "feat: generated")
	m, err = e.Create("hook")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != "ready" {
		t.Fatal(m.State)
	}
}

func TestTypedMultilineHooksUseConfiguredRunnersAndEnvironment(t *testing.T) {
	e := fixture(t, 1)
	runnerDir := t.TempDir()
	python, err := exec.LookPath("python")
	if err != nil {
		t.Fatal(err)
	}
	e.Config.Runners = Runners{Shell: filepath.Join(runnerDir, "custom-shell"), Python: filepath.Join(runnerDir, "custom-python")}
	if err := os.Symlink("/bin/bash", e.Config.Runners.Shell); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(python, e.Config.Runners.Python); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(filepath.Dir(e.Root), "hook-events")
	t.Setenv("VCM_TEST_EVENTS", log)
	e.Config.Children[0].Hooks = Hooks{HookCreateAfter: {
		{ID: "shell-environment", Shell: `test "$VCM_REPOSITORY_NAME" = repo0
test "$PWD" = "$VCM_REPOSITORY_PATH"
test "$VCM_ROOT" != "$VCM_ROOT_ORIGIN"
test "$VCM_HOOK_PHASE" = create-after
test "$VCM_HOOK_ID" = shell-environment
printf 'shell\n' >> "$VCM_TEST_EVENTS"
`},
		{ID: "python-environment", Python: `import os
from pathlib import Path

assert os.environ["VCM_REPOSITORY_NAME"] == "repo0"
assert Path.cwd() == Path(os.environ["VCM_REPOSITORY_PATH"])
assert os.environ["VCM_ROOT"] != os.environ["VCM_ROOT_ORIGIN"]
assert os.environ["VCM_HOOK_PHASE"] == "create-after"
assert os.environ["VCM_HOOK_ID"] == "python-environment"
with open(os.environ["VCM_TEST_EVENTS"], "a") as stream:
    stream.write("python\n")
`},
	}}
	saveContractConfig(t, e)
	if _, err := e.Create("typed-hooks"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "shell\npython\n" {
		t.Fatalf("hook output: %q", data)
	}
}
func TestDropForcePreservesData(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("drop")
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(m.Repositories[1].Path, "untracked.txt"), "save me")
	if err = e.Drop(m); err == nil {
		t.Fatal("dirty drop accepted")
	}
	e.Force = true
	if err = e.Drop(m); err != nil {
		t.Fatal(err)
	}
	if len(m.Backups) == 0 {
		t.Fatal("no backups")
	}
	found := false
	for _, path := range m.Backups {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Base(h.Name) == "untracked.txt" {
				b, err := io.ReadAll(tr)
				if err != nil {
					t.Fatal(err)
				}
				if string(b) == "save me" {
					found = true
				}
			}
		}
		gz.Close()
		f.Close()
	}
	if !found {
		t.Fatal("discarded filesystem bytes not present in recovery archive")
	}
	for _, p := range m.Backups {
		if st, err := os.Stat(p); err != nil || st.Size() == 0 {
			t.Fatalf("missing backup %s", p)
		}
	}
}
func TestMutationLock(t *testing.T) {
	e := fixture(t, 0)
	unlock, err := e.store.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err = e.Mutate(func() error { t.Fatal("mutated under lock"); return nil }); err == nil {
		t.Fatal("lock accepted")
	}
}
func TestSafeSyncAndForce(t *testing.T) {
	e := fixture(t, 1)
	path := filepath.Join(e.Root, "repo0")
	commitFile(t, path, "local.txt", "local\n")
	before := mustGit(t, path, "rev-parse", "HEAD")
	if err := e.Sync(); err == nil {
		t.Fatal("sync discarded local commits")
	}
	e.Force = true
	if err := e.Sync(); err != nil {
		t.Fatal(err)
	}
	refs := mustGit(t, path, "for-each-ref", "--format=%(objectname)", "refs/vcm/recovery")
	if !strings.Contains(refs, before) {
		t.Fatal("local history not preserved")
	}
}

func TestMergeRecoversPreparedIndex(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("interrupted")
	if err != nil {
		t.Fatal(err)
	}
	r := &m.Repositories[1]
	commitFile(t, r.Path, "feature.txt", "feature\n")
	r.Source = mustGit(t, r.Path, "rev-parse", "HEAD")
	r.TargetBefore = mustGit(t, r.Origin, "rev-parse", "HEAD")
	r.MergeTree = mustGit(t, r.Origin, "merge-tree", "--write-tree", r.TargetBefore, r.Source)
	r.MergeCommit = mustGit(t, r.Origin, "commit-tree", r.MergeTree, "-p", r.TargetBefore, "-m", "feat: interrupted merge")
	r.Intent = "merge"
	m.State = "merging"
	if err = e.store.save(m); err != nil {
		t.Fatal(err)
	}
	mustGit(t, r.Origin, "read-tree", "-u", "-m", r.TargetBefore, r.MergeCommit)
	m, err = e.store.load(m.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, r.Origin, "rev-parse", "HEAD"); got != r.MergeCommit {
		t.Fatal("did not reconcile exact intended commit")
	}
}
func TestMergeCompatibleAdvancedTrunkAndUnchanged(t *testing.T) {
	e := fixture(t, 2)
	m, err := e.Create("advanced")
	if err != nil {
		t.Fatal(err)
	}
	r := m.Repositories[1]
	commitFile(t, r.Path, "source.txt", "source\n")
	commitFile(t, r.Origin, "target.txt", "target\n")
	unchanged := mustGit(t, m.Repositories[2].Origin, "rev-parse", "HEAD")
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	if mustGit(t, m.Repositories[2].Origin, "rev-parse", "HEAD") != unchanged {
		t.Fatal("unchanged repo received commit")
	}
	for _, file := range []string{"source.txt", "target.txt"} {
		if _, err = os.Stat(filepath.Join(r.Origin, file)); err != nil {
			t.Fatal(err)
		}
	}
}
func TestDropPreservesUnrelatedBranch(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("ownership")
	if err != nil {
		t.Fatal(err)
	}
	r := m.Repositories[1]
	mustGit(t, r.Path, "checkout", "-b", "unrelated")
	e.Force = true
	if err = e.Drop(m); err == nil {
		t.Fatal("removed unrelated branch checkout")
	}
	if _, err = os.Stat(r.Path); err != nil {
		t.Fatal(err)
	}
}
func TestDryRunCreatesNoState(t *testing.T) {
	e := fixture(t, 0)
	if _, err := e.CreatePlan("bad_slug"); err == nil {
		t.Fatal("invalid dry-run slug accepted")
	}
	e.DryRun = true
	if err := e.Mutate(func() error { t.Fatal("dry run mutation executed"); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreatePlan("preview"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.store.dir); !os.IsNotExist(err) { // bootstrap creates only a lock, remove it before checking manifest absence.
		entries, err := os.ReadDir(e.store.dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Name() != "mutation.lock" {
				t.Fatalf("dry run created state %s", entry.Name())
			}
		}
	}
}
func TestDropRejectsUnrelatedNestedRepository(t *testing.T) {
	e := fixture(t, 0)
	m, err := e.Create("nested")
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(m.Workspace, "foreign")
	mustGit(t, m.Workspace, "init", "--initial-branch=main", nested)
	e.Force = true
	if err = e.Drop(m); err == nil {
		t.Fatal("deleted unrelated repository")
	}
	if _, err = os.Stat(filepath.Join(nested, ".git")); err != nil {
		t.Fatal(err)
	}
}
