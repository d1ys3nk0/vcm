package vcm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"go.yaml.in/yaml/v3"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type failAfterOneWrite struct{ writes int }

func (w *failAfterOneWrite) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, errors.New("test output failure")
	}
	return len(p), nil
}

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
func abbreviated(t *testing.T, path, revision string) string {
	t.Helper()
	return mustGit(t, path, "rev-parse", "--short=7", revision)
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

func TestSelectInfersChangeFromManagedWorktree(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("selection-context")
	if err != nil {
		t.Fatal(err)
	}
	rootNested := filepath.Join(m.Workspace, "nested")
	childNested := filepath.Join(m.Repositories[1].Path, "nested")
	for _, path := range []string{rootNested, childNested} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}

	for name, selection := range map[string]struct {
		selector string
		cwd      string
	}{
		"tag":              {selector: m.Tag, cwd: e.Root},
		"workspace path":   {selector: m.Workspace, cwd: e.Root},
		"root":             {cwd: m.Workspace},
		"root descendant":  {cwd: rootNested},
		"child":            {cwd: m.Repositories[1].Path},
		"child descendant": {cwd: childNested},
	} {
		t.Run(name, func(t *testing.T) {
			selected, err := e.Select(selection.selector, selection.cwd)
			if err != nil {
				t.Fatal(err)
			}
			if selected.Tag != m.Tag {
				t.Fatalf("selected %q, want %q", selected.Tag, m.Tag)
			}
		})
	}

	if _, err := e.Select("", e.Root); err == nil || err.Error() != "Change argument is required outside a managed Change worktree" {
		t.Fatalf("unexpected omitted-selection error: %v", err)
	}
	if _, err := e.Select("missing-change", e.Root); err == nil || !strings.Contains(err.Error(), `no managed Change matches "missing-change"`) {
		t.Fatalf("unexpected explicit-selection error: %v", err)
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
	if err = e.Merge(m, "feat: test change"); err != nil {
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
	if err = e.Merge(m, "feat: test change"); err != nil {
		t.Fatal(err)
	}
}

func TestMergeMessageContract(t *testing.T) {
	e := fixture(t, 2)
	m, err := e.Create("message-contract")
	if err != nil {
		t.Fatal(err)
	}
	wantMessages := map[string]string{}
	sourceRevisions := map[string]string{}
	for i := range m.Repositories {
		commitFile(t, m.Repositories[i].Path, "changed.txt", "changed\n")
		revision := mustGit(t, m.Repositories[i].Path, "rev-parse", "HEAD")
		sourceRevisions[m.Repositories[i].Repository.Name] = revision
		wantMessages[m.Repositories[i].Repository.Name] = "feat: shared subject\n\nCommits:\n- " + abbreviated(t, m.Repositories[i].Path, revision) + " feat: update changed.txt"
	}
	if err = e.Merge(m, "not conventional"); err == nil {
		t.Fatal("fresh merge accepted invalid message")
	}
	if err = ValidateMergeMessage("ops: custom conventional type"); err != nil {
		t.Fatal(err)
	}
	// Persist the subject at the start, then simulate an interrupted gate.
	allow := filepath.Join(filepath.Dir(e.Root), "allow-message-merge")
	e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "stop", Shell: `test -f "` + allow + `"`}}}
	saveContractConfig(t, e)
	if err = e.Refresh(m); err != nil {
		t.Fatal(err)
	}
	rootRefresh := mustGit(t, m.Workspace, "rev-parse", "HEAD")
	wantMessages["root"] = strings.Join([]string{
		"feat: shared subject",
		"",
		"Commits:",
		"- " + abbreviated(t, m.Workspace, rootRefresh) + " chore(vcm): refresh " + m.Tag,
		"- " + abbreviated(t, m.Workspace, sourceRevisions["root"]) + " feat: update changed.txt",
	}, "\n")
	if err = e.Merge(m, "feat: shared subject"); err == nil {
		t.Fatal("expected gate interruption")
	}
	if m.MergeMessage != "feat: shared subject" {
		t.Fatalf("message not persisted: %q", m.MergeMessage)
	}
	if err = e.Merge(m, "fix: replacement"); err == nil {
		t.Fatal("retry accepted replacement message")
	}
	put(t, allow, "allowed")
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Repositories {
		body := mustGit(t, r.Origin, "log", "-1", "--format=%B")
		if body != wantMessages[r.Repository.Name] {
			t.Fatalf("repository %s commit message:\n%q\nwant:\n%q", r.Repository.Name, body, wantMessages[r.Repository.Name])
		}
	}
}

func TestMergeDefaultMessageAcrossChangedRepositories(t *testing.T) {
	e := fixture(t, 2)
	m, err := e.Create("default-message")
	if err != nil {
		t.Fatal(err)
	}
	wantMessages := map[string]string{}
	for i := range m.Repositories {
		commitFile(t, m.Repositories[i].Path, "default.txt", "changed\n")
		revision := mustGit(t, m.Repositories[i].Path, "rev-parse", "HEAD")
		wantMessages[m.Repositories[i].Repository.Name] = "feat: default-message\n\nCommits:\n- " + abbreviated(t, m.Repositories[i].Path, revision) + " feat: update default.txt"
	}
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	if m.MergeMessage != "feat: default-message" {
		t.Fatalf("default message not persisted: %q", m.MergeMessage)
	}
	for _, repository := range m.Repositories {
		body := mustGit(t, repository.Origin, "log", "-1", "--format=%B")
		if body != wantMessages[repository.Repository.Name] {
			t.Fatalf("repository %s commit message:\n%q\nwant:\n%q", repository.Repository.Name, body, wantMessages[repository.Repository.Name])
		}
	}
}

func TestMergeCommitHistoryPerRepository(t *testing.T) {
	e := fixture(t, 2)
	e.Config.Children[0].Hooks = Hooks{HookMergeBefore: {{ID: "history", Shell: `printf 'hook\n' > hook.txt; git add hook.txt; git commit -m 'chore: merge hook' -m 'hook body must be omitted'`}}}
	saveContractConfig(t, e)
	m, err := e.Create("commit-history")
	if err != nil {
		t.Fatal(err)
	}

	root := &m.Repositories[0]
	put(t, filepath.Join(root.Path, "root.txt"), "root\n")
	mustGit(t, root.Path, "add", "root.txt")
	command := exec.Command("git", "-C", root.Path, "commit", "-m", "feat: root source", "-m", "private root body")
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Hidden Author", "GIT_AUTHOR_EMAIL=hidden@example.test", "GIT_AUTHOR_DATE=2001-02-03T04:05:06Z")
	if output, commandErr := command.CombinedOutput(); commandErr != nil {
		t.Fatalf("custom root commit: %s: %v", output, commandErr)
	}
	rootSource := mustGit(t, root.Path, "rev-parse", "HEAD")

	changed := &m.Repositories[1]
	put(t, filepath.Join(changed.Path, "source.txt"), "source\n")
	mustGit(t, changed.Path, "add", "source.txt")
	mustGit(t, changed.Path, "commit", "-m", "feat: child source", "-m", "child body must be omitted")
	childSource := mustGit(t, changed.Path, "rev-parse", "HEAD")
	commitFile(t, changed.Origin, "trunk.txt", "trunk\n")
	trunkCommit := mustGit(t, changed.Origin, "rev-parse", "HEAD")
	unchangedBefore := mustGit(t, m.Repositories[2].Origin, "rev-parse", "HEAD")

	if err = e.Refresh(m); err != nil {
		t.Fatal(err)
	}
	refreshCommit := mustGit(t, changed.Path, "rev-parse", "HEAD")
	if err = e.Merge(m, "feat: repository histories"); err != nil {
		t.Fatal(err)
	}
	hookCommit := changed.Source

	wantRoot := "feat: repository histories\n\nCommits:\n- " + abbreviated(t, root.Origin, rootSource) + " feat: root source"
	wantChild := strings.Join([]string{
		"feat: repository histories",
		"",
		"Commits:",
		"- " + abbreviated(t, changed.Origin, hookCommit) + " chore: merge hook",
		"- " + abbreviated(t, changed.Origin, refreshCommit) + " chore(vcm): refresh " + m.Tag,
		"- " + abbreviated(t, changed.Origin, childSource) + " feat: child source",
	}, "\n")
	if got := mustGit(t, root.Origin, "log", "-1", "--format=%B"); got != wantRoot {
		t.Fatalf("root commit message:\n%q\nwant:\n%q", got, wantRoot)
	}
	if got := mustGit(t, changed.Origin, "log", "-1", "--format=%B"); got != wantChild {
		t.Fatalf("child commit message:\n%q\nwant:\n%q", got, wantChild)
	}
	if got := mustGit(t, m.Repositories[2].Origin, "rev-parse", "HEAD"); got != unchangedBefore {
		t.Fatalf("unchanged repository gained commit %s, want %s", got, unchangedBefore)
	}
	for _, omitted := range []string{"private root body", "child body must be omitted", "hook body must be omitted", "Hidden Author", "hidden@example.test", trunkCommit, "feat: update trunk.txt"} {
		if strings.Contains(wantRoot, omitted) || strings.Contains(wantChild, omitted) {
			t.Fatalf("commit metadata or target history leaked into squash message: %q", omitted)
		}
	}
}

func TestMergeForceIgnoresOnlyCleanHookCommandFailures(t *testing.T) {
	e := fixture(t, 1)
	log := filepath.Join(filepath.Dir(e.Root), "merge-force-events")
	t.Setenv("VCM_TEST_EVENTS", log)
	e.Config.Root.Hooks = Hooks{HookMergeBefore: {
		{ID: "commit-then-fail", Shell: `printf 'forced output\n' > forced.txt; git add forced.txt; git commit -m 'chore: forced hook output'; printf 'failed\n' >> "$VCM_TEST_EVENTS"; false`},
		{ID: "continue", Shell: `printf 'continued\n' >> "$VCM_TEST_EVENTS"`},
	}}
	saveContractConfig(t, e)
	m, err := e.Create("force-clean-failure")
	if err != nil {
		t.Fatal(err)
	}
	e.MergeForce = true
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "failed\ncontinued\n" {
		t.Fatalf("later hook did not run: %q %v", data, err)
	}
	failed := m.Hooks["root/merge-before/commit-then-fail"]
	if failed.Status != "failed" || failed.Error == "" {
		t.Fatalf("ignored failure not persisted: %+v", failed)
	}
	if _, err = os.Stat(filepath.Join(e.Root, "forced.txt")); err != nil {
		t.Fatal("committed hook output was not integrated:", err)
	}
	if len(m.Backups) != 0 {
		t.Fatalf("merge force created destructive cleanup backups: %v", m.Backups)
	}
}

func TestMergeRemovesIgnoredContentWithoutRecoveryBackups(t *testing.T) {
	e := fixture(t, 1)
	commitFile(t, e.Root, ".gitignore", "/repo0/\n/generated/\n")
	mustGit(t, e.Root, "push", "origin", "main")
	child := filepath.Join(e.Root, "repo0")
	commitFile(t, child, ".gitignore", "generated/\n")
	mustGit(t, child, "push", "origin", "main")
	m, err := e.Create("ignored-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range m.Repositories {
		ignored := filepath.Join(repository.Path, "generated", "cache", "artifact")
		if err = os.MkdirAll(filepath.Dir(ignored), 0755); err != nil {
			t.Fatal(err)
		}
		put(t, ignored, "disposable\n")
	}
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	if len(m.Backups) != 0 {
		t.Fatalf("merge cleanup created recovery backups: %v", m.Backups)
	}
	for _, repository := range m.Repositories {
		if _, err = os.Lstat(repository.Path); !os.IsNotExist(err) {
			t.Fatalf("worktree with ignored content remains: %s", repository.Path)
		}
		if _, err = git(repository.Origin, "show-ref", "--verify", "refs/heads/"+m.Tag); err == nil {
			t.Fatalf("Change branch remains for %s", repository.Repository.Name)
		}
	}
}

func TestMergeFinalizingRetryDeletesIgnoredContentAfterSafetyRepair(t *testing.T) {
	e := fixture(t, 0)
	commitFile(t, e.Root, ".gitignore", "generated/\nforeign/\n")
	mustGit(t, e.Root, "push", "origin", "main")
	m, err := e.Create("ignored-cleanup-retry")
	if err != nil {
		t.Fatal(err)
	}
	ignored := filepath.Join(m.Workspace, "generated", "cache")
	if err = os.MkdirAll(filepath.Dir(ignored), 0755); err != nil {
		t.Fatal(err)
	}
	put(t, ignored, "disposable\n")
	foreign := filepath.Join(m.Workspace, "foreign")
	mustGit(t, m.Workspace, "init", foreign)
	put(t, filepath.Join(foreign, "valuable"), "preserve\n")
	if err = e.Merge(m); err == nil || !strings.Contains(err.Error(), "unrelated nested Git repository") {
		t.Fatalf("foreign nested repository did not block cleanup: %v", err)
	}
	if m.State != "merge-finalizing" {
		t.Fatalf("merge did not checkpoint integration before cleanup: %s", m.State)
	}
	if _, err = os.Stat(ignored); err != nil {
		t.Fatal("ignored content was removed before cleanup safety passed:", err)
	}
	if err = os.RemoveAll(foreign); err != nil {
		t.Fatal(err)
	}
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(m.Workspace); !os.IsNotExist(err) {
		t.Fatal("plain merge retry did not remove retained worktree")
	}
	if len(m.Backups) != 0 {
		t.Fatalf("merge retry created recovery backups: %v", m.Backups)
	}
}

func TestMergeForceKeepsDirtyHookFailureFatal(t *testing.T) {
	e := fixture(t, 0)
	e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "dirty", Shell: `printf 'dirty\n' > dirty.txt; false`}}}
	saveContractConfig(t, e)
	m, err := e.Create("force-dirty-failure")
	if err != nil {
		t.Fatal(err)
	}
	e.MergeForce = true
	if err = e.Merge(m); err == nil || !strings.Contains(err.Error(), "dirty checkout") {
		t.Fatalf("force ignored dirty hook failure: %v", err)
	}
	if _, err = os.Stat(m.Workspace); err != nil {
		t.Fatal("dirty workspace was removed:", err)
	}
}

func TestMergeForceKeepsRunnerAndRevisionFailuresFatal(t *testing.T) {
	t.Run("configuration", func(t *testing.T) {
		e := fixture(t, 0)
		e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "retry", Shell: "false"}}}
		saveContractConfig(t, e)
		m, err := e.Create("force-config-failure")
		if err != nil {
			t.Fatal(err)
		}
		if err = e.Merge(m); err == nil {
			t.Fatal("expected initial hook failure")
		}
		put(t, filepath.Join(e.Root, "vcm.yml"), "not: [valid")
		e.MergeForce = true
		if err = e.Merge(m); err == nil || !strings.Contains(err.Error(), "read current hook configuration") {
			t.Fatalf("force ignored hook configuration failure: %v", err)
		}
	})
	t.Run("runner start", func(t *testing.T) {
		e := fixture(t, 0)
		e.Config.Runners.Shell = filepath.Join(t.TempDir(), "missing-shell")
		e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "missing-runner", Shell: "false"}}}
		saveContractConfig(t, e)
		m, err := e.Create("force-runner-failure")
		if err != nil {
			t.Fatal(err)
		}
		e.MergeForce = true
		if err = e.Merge(m); err == nil || !strings.Contains(err.Error(), "missing-shell") {
			t.Fatalf("force ignored runner start failure: %v", err)
		}
	})
	t.Run("hook output", func(t *testing.T) {
		e := fixture(t, 0)
		e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "output", Shell: "printf 'output\\n'; false"}}}
		saveContractConfig(t, e)
		m, err := e.Create("force-output-failure")
		if err != nil {
			t.Fatal(err)
		}
		e.Out = &failAfterOneWrite{}
		e.MergeForce = true
		if err = e.Merge(m); err == nil || !strings.Contains(err.Error(), "output") {
			t.Fatalf("force ignored hook output failure: %v", err)
		}
	})
	t.Run("target drift", func(t *testing.T) {
		e := fixture(t, 0)
		m, err := e.Create("force-target-drift")
		if err != nil {
			t.Fatal(err)
		}
		commitFile(t, e.Root, "drift.txt", "drift\n")
		e.MergeForce = true
		want := fmt.Sprintf("repository root: target advanced from recorded base; run vcm refresh %s", m.Tag)
		if err = e.Merge(m); err == nil || err.Error() != want {
			t.Fatalf("force ignored target drift: %v", err)
		}
	})
}

func TestMergeSkipHooksIsSelectiveAndDoesNotPersistStatus(t *testing.T) {
	e := fixture(t, 0)
	log := filepath.Join(filepath.Dir(e.Root), "skip-events")
	t.Setenv("VCM_TEST_EVENTS", log)
	e.Config.Root.Hooks = Hooks{
		HookMergeBefore: {{ID: "before", Shell: `printf 'before\n' >> "$VCM_TEST_EVENTS"`}},
		HookMergeAfter:  {{ID: "after", Shell: `printf 'after\n' >> "$VCM_TEST_EVENTS"`}},
	}
	saveContractConfig(t, e)
	m, err := e.Create("selective-skip")
	if err != nil {
		t.Fatal(err)
	}
	e.SkipMergeHooks = map[string]bool{HookMergeBefore: true}
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "after\n" {
		t.Fatalf("selective skip output: %q %v", data, err)
	}
	if _, exists := m.Hooks["root/merge-before/before"]; exists {
		t.Fatal("skipped hook outcome was persisted")
	}
}

func TestMergeSkipHooksAfterAndBothExecuteExpectedPhases(t *testing.T) {
	for _, test := range []struct {
		name       string
		skipped    map[string]bool
		wantEvents string
		wantRun    []string
		wantSkip   []string
	}{
		{name: "after only", skipped: map[string]bool{HookMergeAfter: true}, wantEvents: "before\n", wantRun: []string{HookMergeBefore}, wantSkip: []string{HookMergeAfter}},
		{name: "both", skipped: map[string]bool{HookMergeBefore: true, HookMergeAfter: true}, wantSkip: []string{HookMergeBefore, HookMergeAfter}},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := fixture(t, 0)
			var output bytes.Buffer
			e.Out = &output
			events := filepath.Join(filepath.Dir(e.Root), "skip-execution-events")
			t.Setenv("VCM_TEST_EVENTS", events)
			e.Config.Root.Hooks = Hooks{
				HookMergeBefore: {{ID: "before", Shell: `printf 'before\n' >> "$VCM_TEST_EVENTS"`}},
				HookMergeAfter:  {{ID: "after", Shell: `printf 'after\n' >> "$VCM_TEST_EVENTS"`}},
			}
			saveContractConfig(t, e)
			m, err := e.Create("skip-" + strings.ReplaceAll(test.name, " ", "-"))
			if err != nil {
				t.Fatal(err)
			}
			output.Reset()
			e.SkipMergeHooks = test.skipped
			if err = e.Merge(m); err != nil {
				t.Fatal(err)
			}
			data, readErr := os.ReadFile(events)
			if test.wantEvents == "" && os.IsNotExist(readErr) {
				data, readErr = nil, nil
			}
			if readErr != nil || string(data) != test.wantEvents {
				t.Fatalf("hook events = %q, want %q: %v", data, test.wantEvents, readErr)
			}
			for _, phase := range test.wantRun {
				id := strings.TrimPrefix(phase, "merge-")
				if outcome := m.Hooks["root/"+phase+"/"+id]; outcome.Status != "complete" {
					t.Fatalf("executed hook %s outcome: %+v", phase, outcome)
				}
			}
			for _, phase := range test.wantSkip {
				id := strings.TrimPrefix(phase, "merge-")
				if _, exists := m.Hooks["root/"+phase+"/"+id]; exists {
					t.Fatalf("skipped hook %s persisted an outcome", phase)
				}
				if !strings.Contains(output.String(), "/"+phase+"/"+id+" @") || !strings.Contains(output.String(), "skipped by --skip-hooks") {
					t.Fatalf("skipped hook %s was not logged:\n%s", phase, output.String())
				}
			}
		})
	}
}

func TestMergeFailedHookRetriesUnlessPhaseSkipped(t *testing.T) {
	e := fixture(t, 0)
	count := filepath.Join(filepath.Dir(e.Root), "retry-count")
	allow := filepath.Join(filepath.Dir(e.Root), "retry-allow")
	t.Setenv("VCM_TEST_COUNT", count)
	t.Setenv("VCM_TEST_ALLOW", allow)
	e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "retry", Shell: `printf 'run\n' >> "$VCM_TEST_COUNT"; test -f "$VCM_TEST_ALLOW"`}}}
	saveContractConfig(t, e)
	m, err := e.Create("failed-retry")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Merge(m); err == nil {
		t.Fatal("expected initial hook failure")
	}
	put(t, allow, "yes")
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(count)
	if err != nil || string(data) != "run\nrun\n" {
		t.Fatalf("failed hook was not retried: %q %v", data, err)
	}
}

func TestMergeSkipGitHooksAppliesOnlyInsideLifecycleHook(t *testing.T) {
	for _, suppress := range []bool{false, true} {
		t.Run(fmt.Sprintf("suppress-%t", suppress), func(t *testing.T) {
			if suppress {
				t.Setenv("GIT_CONFIG_COUNT", "1")
				t.Setenv("GIT_CONFIG_KEY_0", "user.name")
				t.Setenv("GIT_CONFIG_VALUE_0", "Inherited Hook User")
			}
			e := fixture(t, 0)
			hooksDir := filepath.Join(filepath.Dir(e.Root), "reject-hooks")
			if err := os.MkdirAll(hooksDir, 0755); err != nil {
				t.Fatal(err)
			}
			preCommit := filepath.Join(hooksDir, "pre-commit")
			put(t, preCommit, "#!/bin/sh\nexit 1\n")
			if err := os.Chmod(preCommit, 0755); err != nil {
				t.Fatal(err)
			}
			hook := `printf 'hook commit\n' > hook-commit.txt; git add hook-commit.txt; git commit -m 'chore: hook commit'`
			if suppress {
				hook = `test "$(git config --get user.name)" = "Inherited Hook User"; ` + hook
			}
			e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "commit", Shell: hook}}}
			saveContractConfig(t, e)
			mustGit(t, e.Root, "config", "core.hooksPath", hooksDir)
			m, err := e.Create("git-hook-policy")
			if err != nil {
				t.Fatal(err)
			}
			e.SkipGitHooks = suppress
			err = e.Merge(m)
			if !suppress {
				if err == nil || !strings.Contains(err.Error(), "dirty checkout") {
					t.Fatalf("configured Git hook did not block lifecycle commit: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(filepath.Join(e.Root, "hook-commit.txt")); err != nil {
				t.Fatal("suppressed Git hook commit was not integrated:", err)
			}
		})
	}
}

func TestMergeSkipGitHooksRejectsMalformedInheritedConfig(t *testing.T) {
	_, err := suppressGitHooks([]string{"PATH=/bin", "GIT_CONFIG_COUNT=invalid"})
	if err == nil || !strings.Contains(err.Error(), `invalid GIT_CONFIG_COUNT "invalid"`) {
		t.Fatalf("malformed inherited Git configuration was not rejected: %v", err)
	}
}

func TestMergeForcePreservesOwnershipConflictAndIncompleteLifecycleSafety(t *testing.T) {
	t.Run("ownership", func(t *testing.T) {
		e := fixture(t, 0)
		m, err := e.Create("force-ownership")
		if err != nil {
			t.Fatal(err)
		}
		ownedPath := m.Repositories[0].Path
		m.Repositories[0].Path = filepath.Join(filepath.Dir(ownedPath), "wrong-workspace")
		e.MergeForce = true
		if err = e.Merge(m); err == nil || !strings.Contains(err.Error(), "ownership paths mismatch") {
			t.Fatalf("force ignored ownership failure: %v", err)
		}
		if _, err = os.Stat(ownedPath); err != nil {
			t.Fatal("owned workspace was removed:", err)
		}
	})
	t.Run("conflict", func(t *testing.T) {
		e := fixture(t, 0)
		commonBase := mustGit(t, e.Root, "rev-parse", "HEAD")
		commitFile(t, e.Root, "conflict.txt", "target\n")
		mustGit(t, e.Root, "push", "origin", "main")
		m, err := e.Create("force-conflict")
		if err != nil {
			t.Fatal(err)
		}
		mustGit(t, m.Workspace, "reset", "--hard", commonBase)
		commitFile(t, m.Workspace, "conflict.txt", "source\n")
		e.MergeForce = true
		if err = e.Merge(m); err == nil || !strings.Contains(err.Error(), "conflict after merge gate") {
			t.Fatalf("force ignored merge conflict: %v", err)
		}
		if _, err = os.Stat(m.Workspace); err != nil {
			t.Fatal("conflicting workspace was removed:", err)
		}
	})
	for _, state := range []string{"creating", "refreshing", "dropping"} {
		t.Run(state, func(t *testing.T) {
			e := fixture(t, 0)
			m, err := e.Create("force-incomplete-" + state)
			if err != nil {
				t.Fatal(err)
			}
			m.State = state
			e.MergeForce = true
			if err = e.Merge(m); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.TrimSuffix(state, "ing")) {
				t.Fatalf("force accepted incomplete %s lifecycle: %v", state, err)
			}
			if _, err = os.Stat(m.Workspace); err != nil {
				t.Fatal("incomplete workspace was removed:", err)
			}
		})
	}
}

func TestRefreshAdvancedAndSelectedRepositories(t *testing.T) {
	e := fixture(t, 2)
	m, err := e.CreateSelected("refresh-selected", "repo0", "")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, m.Repositories[1].Path, "feature.txt", "feature\n")
	commitFile(t, m.Repositories[1].Origin, "trunk.txt", "trunk\n")
	want := fmt.Sprintf("repository repo0: target advanced from recorded base; run vcm refresh %s", m.Tag)
	if err = e.Merge(m, "feat: selected refresh"); err == nil || err.Error() != want {
		t.Fatalf("merge did not block advanced target: %v", err)
	}
	if err = e.Refresh(m); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"feature.txt", "trunk.txt"} {
		if _, err = os.Stat(filepath.Join(m.Repositories[1].Path, file)); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.Repositories) != 2 {
		t.Fatal("refresh changed partial selection")
	}
	base := m.Repositories[1].Base
	if err = e.Refresh(m); err != nil {
		t.Fatal(err)
	}
	if m.Repositories[1].Base != base {
		t.Fatal("unchanged refresh advanced base")
	}
}

func TestRefreshConflictRepairAndTargetDrift(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("refresh-conflict")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, m.Repositories[1].Path, "conflict.txt", "source\n")
	commitFile(t, m.Repositories[1].Origin, "conflict.txt", "target\n")
	if err = e.Refresh(m); err == nil || !strings.Contains(err.Error(), "conflict preserved") {
		t.Fatalf("refresh conflict not preserved: %v", err)
	}
	recordedTarget := m.Repositories[1].TargetBefore
	commitFile(t, m.Repositories[1].Origin, "drift.txt", "drift\n")
	advancedTarget := mustGit(t, m.Repositories[1].Origin, "rev-parse", "HEAD")
	// Resolve and commit the preserved merge without moving the canonical trunk
	// back to the recorded refresh target.
	put(t, filepath.Join(m.Repositories[1].Path, "conflict.txt"), "resolved\n")
	mustGit(t, m.Repositories[1].Path, "add", "conflict.txt")
	mustGit(t, m.Repositories[1].Path, "commit", "--no-edit")
	unrelated := filepath.Join(m.Repositories[1].Path, "unrelated.txt")
	put(t, unrelated, "unrelated\n")
	if err = e.Refresh(m); err == nil || !strings.Contains(err.Error(), "completed refresh recovery is not clean") {
		t.Fatalf("refresh accepted unrelated dirty content: %v", err)
	}
	if m.Repositories[1].Intent != "refresh" {
		t.Fatalf("dirty recovery finalized its checkpoint: %+v", m.Repositories[1])
	}
	if err = os.Remove(unrelated); err != nil {
		t.Fatal(err)
	}
	report := e.Status(m)
	if got := report.Repositories[1]; got.RecordedTarget != recordedTarget || got.Target != advancedTarget || got.RecoveryState != refreshRecoveryCommittedResolution {
		t.Fatalf("status did not describe refresh recovery: %+v", got)
	}
	if err = e.Refresh(m); err != nil {
		t.Fatal(err)
	}
	if m.State != "ready" || m.Repositories[1].Intent != "" {
		t.Fatalf("refresh did not recover: %+v", m.Repositories[1])
	}
	changeHead := mustGit(t, m.Repositories[1].Path, "rev-parse", "HEAD")
	if !ancestor(m.Repositories[1].Path, recordedTarget, changeHead) || !ancestor(m.Repositories[1].Path, advancedTarget, changeHead) {
		t.Fatalf("reconciled refresh %s does not contain recorded target %s and advanced target %s", changeHead, recordedTarget, advancedTarget)
	}
	if _, err = os.Stat(filepath.Join(m.Repositories[1].Path, "drift.txt")); err != nil {
		t.Fatalf("reconciled refresh omitted canonical advance: %v", err)
	}
}

func TestRefreshRejectsAdvancedTargetWhileConflictIsUnresolved(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("refresh-unresolved")
	if err != nil {
		t.Fatal(err)
	}
	r := &m.Repositories[1]
	commitFile(t, r.Path, "conflict.txt", "source\n")
	commitFile(t, r.Origin, "conflict.txt", "target\n")
	if err = e.Refresh(m); err == nil || !strings.Contains(err.Error(), "conflict preserved") {
		t.Fatalf("refresh conflict not preserved: %v", err)
	}
	commitFile(t, r.Origin, "advanced.txt", "advanced\n")
	if err = e.Refresh(m); err == nil || !strings.Contains(err.Error(), "recorded refresh is incomplete") || !strings.Contains(err.Error(), "dirty checkout") {
		t.Fatalf("unresolved refresh was not rejected with actionable diagnostics: %v", err)
	}
}

func TestRefreshRejectsRewrittenCanonicalTargetAfterCompletedResolution(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("refresh-rewritten")
	if err != nil {
		t.Fatal(err)
	}
	r := &m.Repositories[1]
	originalBase := r.Base
	commitFile(t, r.Path, "conflict.txt", "source\n")
	commitFile(t, r.Origin, "conflict.txt", "target\n")
	if err = e.Refresh(m); err == nil || !strings.Contains(err.Error(), "conflict preserved") {
		t.Fatalf("refresh conflict not preserved: %v", err)
	}
	recordedTarget := r.TargetBefore
	put(t, filepath.Join(r.Path, "conflict.txt"), "resolved\n")
	mustGit(t, r.Path, "add", "conflict.txt")
	mustGit(t, r.Path, "commit", "--no-edit")
	mustGit(t, r.Origin, "reset", "--hard", originalBase)
	commitFile(t, r.Origin, "rewritten.txt", "rewritten\n")
	rewrittenTarget := mustGit(t, r.Origin, "rev-parse", "HEAD")

	if err = e.Refresh(m); err == nil || !strings.Contains(err.Error(), "no longer contains recorded base") {
		t.Fatalf("rewritten canonical target was not rejected: %v", err)
	}
	if r.Base != recordedTarget || r.Intent != "" {
		t.Fatalf("completed checkpoint was not finalized before rewritten target rejection: %+v", r)
	}
	if ancestor(r.Origin, recordedTarget, rewrittenTarget) {
		t.Fatalf("test canonical target unexpectedly contains recorded target")
	}
}

func TestRefreshReconcilesAdvancedTargetAfterPreparedCheckpoint(t *testing.T) {
	for _, prepared := range []string{"merge-commit", "index"} {
		t.Run(prepared, func(t *testing.T) {
			e := fixture(t, 1)
			m, err := e.Create("refresh-" + prepared)
			if err != nil {
				t.Fatal(err)
			}
			r := &m.Repositories[1]
			commitFile(t, r.Path, "feature.txt", "feature\n")
			commitFile(t, r.Origin, "trunk.txt", "trunk\n")
			r.Source = mustGit(t, r.Path, "rev-parse", "HEAD")
			r.TargetBefore = mustGit(t, r.Origin, "rev-parse", "HEAD")
			r.MergeTree = strings.Split(mustGit(t, r.Path, "merge-tree", "--write-tree", r.Source, r.TargetBefore), "\n")[0]
			r.MergeCommit = mustGit(t, r.Path, "commit-tree", r.MergeTree, "-p", r.Source, "-p", r.TargetBefore, "-m", "chore(vcm): refresh test")
			recordedTarget := r.TargetBefore
			r.Intent, m.State = "refresh", "refreshing"
			if err = e.store.save(m); err != nil {
				t.Fatal(err)
			}
			switch prepared {
			case "merge-commit":
				mustGit(t, r.Path, "update-ref", "refs/heads/"+m.Tag, r.MergeCommit, r.Source)
				mustGit(t, r.Path, "reset", "--hard", r.MergeCommit)
			case "index":
				mustGit(t, r.Path, "read-tree", "-u", "-m", r.Source, r.MergeCommit)
			}
			commitFile(t, r.Origin, "newer.txt", "newer\n")
			advancedTarget := mustGit(t, r.Origin, "rev-parse", "HEAD")
			unrelated := filepath.Join(r.Path, "unrelated.txt")
			put(t, unrelated, "unrelated\n")
			dirtyError := "completed refresh recovery is not clean"
			if prepared == "index" {
				dirtyError = "refresh recovery contains unrelated untracked content"
			}
			if err = e.Refresh(m); err == nil || !strings.Contains(err.Error(), dirtyError) {
				t.Fatalf("%s recovery accepted unrelated untracked content: %v", prepared, err)
			}
			if r.Intent != "refresh" {
				t.Fatalf("dirty %s recovery finalized its checkpoint: %+v", prepared, r)
			}
			if err = os.Remove(unrelated); err != nil {
				t.Fatal(err)
			}
			expectedRecovery := refreshRecoveryRecordedMergeCommit
			if prepared == "index" {
				expectedRecovery = refreshRecoveryPreparedIndex
			}
			if got := e.Status(m).Repositories[1]; got.RecordedTarget != recordedTarget || got.Target != advancedTarget || got.RecoveryState != expectedRecovery {
				t.Fatalf("status did not describe %s recovery: %+v", prepared, got)
			}

			if err = e.Refresh(m); err != nil {
				t.Fatal(err)
			}
			changeHead := mustGit(t, r.Path, "rev-parse", "HEAD")
			if m.State != "ready" || r.Intent != "" || r.Base != advancedTarget {
				t.Fatalf("prepared refresh did not reconcile: state=%s repository=%+v", m.State, r)
			}
			if !ancestor(r.Path, recordedTarget, changeHead) || !ancestor(r.Path, advancedTarget, changeHead) {
				t.Fatalf("reconciled refresh %s does not contain recorded target %s and advanced target %s", changeHead, recordedTarget, advancedTarget)
			}
		})
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
		if err = e.Merge(m, "feat: test change"); err != nil {
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
	if err = e.Merge(m, "feat: test change"); err == nil || !strings.Contains(err.Error(), "restore its vcm.yml entry") {
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
	if err = e.Refresh(m); err != nil {
		t.Fatal(err)
	}
	if err = e.Merge(m, "feat: test change"); err != nil {
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
	if err = e.Merge(m, "feat: test change"); err == nil {
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
	if err = e.Refresh(m); err != nil {
		t.Fatal(err)
	}
	if err = e.Merge(m, "feat: test change"); err == nil {
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
	if err = e.Merge(m, "feat: test change"); err != nil {
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
	if err = e.Merge(m, "feat: test change"); err == nil {
		t.Fatal("expected second repository gate failure")
	}
	if m.Repositories[1].Merged {
		t.Fatal("late hook failure mutated an earlier trunk")
	}
	firstTarget := mustGit(t, m.Repositories[1].Origin, "rev-parse", "HEAD")
	commitFile(t, e.Root, "unexpected.txt", "target drift\n")
	put(t, allow, "allowed")
	m, err = e.store.load(m.Tag)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("repository root: target advanced from recorded base; run vcm refresh %s", m.Tag)
	if err = e.Merge(m, "feat: test change"); err == nil || err.Error() != want {
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
	m.MergeMessage = "feat: test change"
	wantMessage := "feat: test change\n\nCommits:\n- " + abbreviated(t, r.Origin, r.Source) + " feat: update feature.txt"
	r.MergeCommit = mustGit(t, r.Origin, "commit-tree", r.MergeTree, "-p", r.TargetBefore, "-m", wantMessage)
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
	if err = e.Merge(m, "feat: test change"); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, r.Origin, "rev-parse", "HEAD"); got != r.MergeCommit {
		t.Fatal("did not reconcile exact intended commit")
	}
	if got := mustGit(t, r.Origin, "log", "-1", "--format=%B"); got != wantMessage {
		t.Fatalf("prepared commit message changed on retry:\n%q\nwant:\n%q", got, wantMessage)
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
	if err = e.Refresh(m); err != nil {
		t.Fatal(err)
	}
	if err = e.Merge(m, "feat: test change"); err != nil {
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
