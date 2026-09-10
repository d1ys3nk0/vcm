package vcm

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func issueExists(report AuditReport, repository, kind, target string) bool {
	for _, issue := range report.Issues {
		if issue.Repository == repository && issue.Kind == kind && (issue.Path == target || issue.Branch == target) {
			return true
		}
	}
	return false
}

func configureChildren(t *testing.T, e *Engine, names ...string) {
	t.Helper()
	byName := make(map[string]Repository, len(e.Config.Children))
	for _, repository := range e.Config.Children {
		byName[repository.Name] = repository
	}
	children := make([]Repository, 0, len(names))
	for _, name := range names {
		repository, ok := byName[name]
		if !ok {
			t.Fatalf("unknown fixture repository %q", name)
		}
		children = append(children, repository)
	}
	e.Config.Children = children
	e.store.config = e.Config
	data, err := yaml.Marshal(e.Config)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, e.Root, "vcm.yml", string(data))
}

func auditRepositoryNames(report AuditReport) []string {
	names := make([]string, 0, len(report.Repositories))
	for _, repository := range report.Repositories {
		names = append(names, repository.Name)
	}
	return names
}

func assertPruneOrder(t *testing.T, actions []PruneAction, repositories []string) {
	t.Helper()
	phases := []string{PruneReset, PruneRemoveWorktree, PruneDeleteBranch}
	lastPhase := -1
	for _, action := range actions {
		phase := -1
		for i, candidate := range phases {
			if action.Action == candidate {
				phase = i
				break
			}
		}
		if phase < lastPhase {
			t.Fatalf("actions out of phase order: %+v", actions)
		}
		lastPhase = phase
	}
	for _, phase := range phases {
		got := []string{}
		last := ""
		lastTarget := ""
		for _, action := range actions {
			if action.Action != phase {
				continue
			}
			if action.Repository != last {
				got = append(got, action.Repository)
				last = action.Repository
				lastTarget = ""
			}
			if action.Target < lastTarget {
				t.Fatalf("%s targets for %s are not sorted: %+v", phase, action.Repository, actions)
			}
			lastTarget = action.Target
		}
		if !reflect.DeepEqual(got, repositories) {
			t.Fatalf("%s repository order = %v, want %v; actions: %+v", phase, got, repositories, actions)
		}
	}
}

func TestCheckPreservesConfiguredRepositoryAndIssueOrder(t *testing.T) {
	e := fixture(t, 3)
	configureChildren(t, e, "repo2", "repo0", "repo1")
	want := []string{"root", "repo2", "repo0", "repo1"}

	report, err := e.Check()
	if err != nil {
		t.Fatal(err)
	}
	if !report.Clean || !reflect.DeepEqual(auditRepositoryNames(report), want) {
		t.Fatalf("clean audit repository order = %v, want %v; report: %+v", auditRepositoryNames(report), want, report)
	}

	paths := map[string]string{"root": e.Root}
	for _, repository := range e.Config.Children {
		paths[repository.Name] = filepath.Join(e.Root, repository.Path)
	}
	for _, name := range want {
		put(t, filepath.Join(paths[name], "dirty.txt"), "dirty\n")
		mustGit(t, paths[name], "branch", "z-stray")
		mustGit(t, paths[name], "branch", "a-stray")
	}
	report, err = e.Check()
	if err != nil {
		t.Fatal(err)
	}
	if report.Clean || !reflect.DeepEqual(auditRepositoryNames(report), want) {
		t.Fatalf("issue audit repository order = %v, want %v; report: %+v", auditRepositoryNames(report), want, report)
	}
	rank := map[string]int{"root": 0, "repo2": 1, "repo0": 2, "repo1": 3}
	lastRank := -1
	lastKey := ""
	for _, issue := range report.Issues {
		currentRank := rank[issue.Repository]
		key := issue.Kind + "\x00" + issue.Path + "\x00" + issue.Branch
		if currentRank < lastRank || currentRank == lastRank && key < lastKey {
			t.Fatalf("issues are not grouped and sorted in repository order: %+v", report.Issues)
		}
		if currentRank != lastRank {
			lastKey = ""
		}
		lastRank, lastKey = currentRank, key
	}
}

func TestCheckAppendsStaleRepositoriesByName(t *testing.T) {
	e := fixture(t, 4)
	if _, err := e.Create("stale-order"); err != nil {
		t.Fatal(err)
	}
	configureChildren(t, e, "repo3", "repo0")

	report, err := e.Check()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"root", "repo3", "repo0", "repo1", "repo2"}
	if report.Clean || !reflect.DeepEqual(auditRepositoryNames(report), want) {
		t.Fatalf("audit repository order = %v, want %v; report: %+v", auditRepositoryNames(report), want, report)
	}
	if len(report.Issues) < 2 || report.Issues[len(report.Issues)-2].Repository != "repo1" || report.Issues[len(report.Issues)-1].Repository != "repo2" {
		t.Fatalf("stale repository findings are not appended by name: %+v", report.Issues)
	}
}

func TestPrunePreservesConfiguredRepositoryOrderWithinPhases(t *testing.T) {
	e := fixture(t, 3)
	configureChildren(t, e, "repo2", "repo0", "repo1")
	want := []string{"root", "repo2", "repo0", "repo1"}
	paths := map[string]string{"root": e.Root}
	for _, repository := range e.Config.Children {
		paths[repository.Name] = filepath.Join(e.Root, repository.Path)
	}
	for _, name := range want {
		origin := paths[name]
		put(t, filepath.Join(origin, "dirty.txt"), "dirty\n")
		mustGit(t, origin, "branch", "z-stray")
		worktree := filepath.Join(filepath.Dir(e.Root), name+"-unexpected-worktree")
		mustGit(t, origin, "worktree", "add", "-b", "a-worktree", worktree, "main")
	}

	preview, err := e.PruneDryRun()
	if err != nil {
		t.Fatal(err)
	}
	assertPruneOrder(t, preview.Actions, want)

	var report PruneReport
	err = e.Mutate(func() error {
		var pruneErr error
		report, pruneErr = e.Prune(func(PruneAction) bool { return true })
		return pruneErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete {
		t.Fatalf("prune incomplete: %+v", report)
	}
	assertPruneOrder(t, report.Actions, want)
}

func TestCheckAuditsCleanWorkspaceAndManagedChanges(t *testing.T) {
	e := fixture(t, 2)
	report, err := e.Check()
	if err != nil {
		t.Fatal(err)
	}
	if !report.Clean || len(report.Repositories) != 3 {
		t.Fatalf("unexpected initial audit: %+v", report)
	}
	m, err := e.Create("audit-clean")
	if err != nil {
		t.Fatal(err)
	}
	report, err = e.Check()
	if err != nil {
		t.Fatal(err)
	}
	if !report.Clean {
		t.Fatalf("managed Change was not clean: %+v", report.Issues)
	}
	for _, repository := range m.Repositories {
		if issueExists(report, repository.Repository.Name, IssueUnexpectedBranch, m.Tag) {
			t.Fatalf("managed branch was unexpected in %s", repository.Repository.Name)
		}
	}
}

func TestCheckAndPruneUnexpectedResourcesInSafeOrder(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("prune-all")
	if err != nil {
		t.Fatal(err)
	}
	managed := m.Repositories[1].Path
	put(t, filepath.Join(managed, "discard.txt"), "discard\n")
	common := mustGit(t, managed, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(common) {
		common = filepath.Join(managed, common)
	}
	put(t, filepath.Join(common, "info", "exclude"), "ignored.txt\n")
	put(t, filepath.Join(managed, "ignored.txt"), "keep\n")

	origin := filepath.Join(e.Root, "repo0")
	mustGit(t, origin, "branch", "stray")
	unexpectedPath := filepath.Join(filepath.Dir(e.Root), "unexpected-worktree")
	mustGit(t, origin, "worktree", "add", "-b", "unexpected", unexpectedPath, "main")
	put(t, filepath.Join(unexpectedPath, "dirty.txt"), "discard with worktree\n")

	check, err := e.Check()
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct{ kind, target string }{
		{IssueDirtyCheckout, managed},
		{IssueUnexpectedBranch, "stray"},
		{IssueUnexpectedBranch, "unexpected"},
		{IssueUnexpectedWorktree, unexpectedPath},
	} {
		if !issueExists(check, "repo0", expected.kind, expected.target) {
			t.Errorf("missing %s finding for %s: %+v", expected.kind, expected.target, check.Issues)
		}
	}

	var report PruneReport
	err = e.Mutate(func() error {
		var pruneErr error
		report, pruneErr = e.Prune(func(PruneAction) bool { return true })
		return pruneErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete || len(report.RemainingIssues) != 0 {
		t.Fatalf("prune incomplete: %+v", report)
	}
	lastPhase := -1
	phases := map[string]int{PruneReset: 0, PruneRemoveWorktree: 1, PruneDeleteBranch: 2}
	for _, action := range report.Actions {
		if action.Status != PruneCompleted {
			t.Fatalf("action did not complete: %+v", action)
		}
		if phases[action.Action] < lastPhase {
			t.Fatalf("actions out of order: %+v", report.Actions)
		}
		lastPhase = phases[action.Action]
	}
	if _, err := os.Stat(filepath.Join(managed, "discard.txt")); !os.IsNotExist(err) {
		t.Fatal("ordinary untracked file survived reset")
	}
	if data, err := os.ReadFile(filepath.Join(managed, "ignored.txt")); err != nil || string(data) != "keep\n" {
		t.Fatalf("ignored file was not preserved: %q %v", data, err)
	}
	if _, err := os.Stat(unexpectedPath); !os.IsNotExist(err) {
		t.Fatal("unexpected worktree remains")
	}
	branches := mustGit(t, origin, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if strings.Contains(branches, "stray") || strings.Contains(branches, "unexpected") {
		t.Fatalf("unexpected branches remain: %s", branches)
	}
}

func TestPruneDeclinesItemsAndDryRunDoesNotMutate(t *testing.T) {
	e := fixture(t, 0)
	mustGit(t, e.Root, "branch", "keep-for-now")
	preview, err := e.PruneDryRun()
	if err != nil {
		t.Fatal(err)
	}
	if preview.Complete || len(preview.Actions) != 1 || preview.Actions[0].Action != PruneDeleteBranch {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if mustGit(t, e.Root, "show-ref", "--verify", "refs/heads/keep-for-now") == "" {
		t.Fatal("dry run removed branch")
	}
	var report PruneReport
	err = e.Mutate(func() error {
		var pruneErr error
		report, pruneErr = e.Prune(func(PruneAction) bool { return false })
		return pruneErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete || len(report.Actions) != 1 || report.Actions[0].Status != PruneDeclined || len(report.RemainingIssues) == 0 {
		t.Fatalf("declined prune result: %+v", report)
	}
}

func TestCheckProtectsManagedBranchAtMismatchedPath(t *testing.T) {
	e := fixture(t, 0)
	m, err := e.Create("moved")
	if err != nil {
		t.Fatal(err)
	}
	moved := m.Workspace + "-elsewhere"
	mustGit(t, e.Root, "worktree", "move", m.Workspace, moved)
	put(t, filepath.Join(moved, "untracked.txt"), "preserve\n")
	report, err := e.Check()
	if err != nil {
		t.Fatal(err)
	}
	if !issueExists(report, "root", IssueMissingWorktree, m.Workspace) || !issueExists(report, "root", IssueOwnershipMismatch, moved) {
		t.Fatalf("moved managed worktree was not protected: %+v", report.Issues)
	}
	if !issueExists(report, "root", IssueDirtyCheckout, moved) {
		t.Fatalf("protected moved worktree was not audited for cleanliness: %+v", report.Issues)
	}
	preview, err := e.PruneDryRun()
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range preview.Actions {
		if action.Target == moved || action.Target == m.Tag {
			t.Fatalf("protected live-state resource became prune candidate: %+v", action)
		}
	}
}

func TestCheckRejectsAliasedManagedPathWithoutPruneCandidate(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("aliased")
	if err != nil {
		t.Fatal(err)
	}
	managed := m.Repositories[1]
	realPath := managed.Path + "-real"
	mustGit(t, managed.Origin, "worktree", "move", managed.Path, realPath)
	if err := os.Symlink(realPath, managed.Path); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(realPath, "untracked.txt"), "preserve\n")

	report, err := e.Check()
	if err != nil {
		t.Fatal(err)
	}
	if report.Clean || !issueExists(report, "repo0", IssueOwnershipMismatch, managed.Path) {
		t.Fatalf("aliased managed path was accepted: %+v", report.Issues)
	}
	if !issueExists(report, "repo0", IssueDirtyCheckout, realPath) {
		t.Fatalf("aliased worktree was not cleanliness-audited: %+v", report.Issues)
	}
	preview, err := e.PruneDryRun()
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range preview.Actions {
		if action.Repository == "repo0" && (action.Target == managed.Path || action.Target == realPath || action.Target == m.Tag) {
			t.Fatalf("aliased managed resource became prune candidate: %+v", action)
		}
	}
}

func TestPruneRemovesLockedAndStaleUnexpectedWorktrees(t *testing.T) {
	e := fixture(t, 0)
	parent := filepath.Dir(e.Root)
	locked := filepath.Join(parent, "locked-worktree")
	stale := filepath.Join(parent, "stale-worktree")
	detached := filepath.Join(parent, "detached-worktree")
	mustGit(t, e.Root, "worktree", "add", "-b", "locked-branch", locked, "main")
	mustGit(t, e.Root, "worktree", "lock", "--reason", "test", locked)
	mustGit(t, e.Root, "worktree", "add", "-b", "stale-branch", stale, "main")
	mustGit(t, e.Root, "worktree", "add", "--detach", detached, "HEAD")
	if err := os.RemoveAll(stale); err != nil {
		t.Fatal(err)
	}

	var report PruneReport
	err := e.Mutate(func() error {
		var pruneErr error
		report, pruneErr = e.Prune(func(PruneAction) bool { return true })
		return pruneErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete {
		t.Fatalf("locked or stale worktree was not pruned: %+v", report)
	}
	if _, err := os.Stat(detached); !os.IsNotExist(err) {
		t.Fatal("detached unexpected worktree remains")
	}
}

func TestInvalidStateBlocksEveryPruneAction(t *testing.T) {
	e := fixture(t, 0)
	m, err := e.Create("hidden-authorization")
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, e.Root, "branch", "otherwise-unexpected")
	unexpectedPath := filepath.Join(filepath.Dir(e.Root), "otherwise-unexpected-worktree")
	mustGit(t, e.Root, "worktree", "add", "-b", "otherwise-unexpected-worktree", unexpectedPath, "main")
	put(t, filepath.Join(e.store.dir, m.Tag+".json"), "{\n")

	preview, err := e.PruneDryRun()
	if err != nil {
		t.Fatal(err)
	}
	if preview.Complete || len(preview.Actions) != 0 || !issueExists(AuditReport{Issues: preview.RemainingIssues}, "root", IssueInspectionError, filepath.Join(e.store.dir, m.Tag+".json")) {
		t.Fatalf("invalid state did not block dry-run candidates: %+v", preview)
	}
	confirmations := 0
	var report PruneReport
	err = e.Mutate(func() error {
		var pruneErr error
		report, pruneErr = e.Prune(func(PruneAction) bool {
			confirmations++
			return true
		})
		return pruneErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete || confirmations != 0 || len(report.Actions) != 0 {
		t.Fatalf("invalid state allowed destructive actions: confirmations=%d report=%+v", confirmations, report)
	}
	for _, path := range []string{m.Workspace, unexpectedPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("worktree %s was removed: %v", path, err)
		}
	}
	for _, branch := range []string{m.Tag, "otherwise-unexpected", "otherwise-unexpected-worktree"} {
		if mustGit(t, e.Root, "show-ref", "--verify", "refs/heads/"+branch) == "" {
			t.Fatalf("branch %s was removed", branch)
		}
	}
}

func TestPruneAbortsUnfinishedMergeBeforeReset(t *testing.T) {
	e := fixture(t, 0)
	commitFile(t, e.Root, "conflict.txt", "base\n")
	mustGit(t, e.Root, "checkout", "-b", "conflict-source")
	commitFile(t, e.Root, "conflict.txt", "source\n")
	mustGit(t, e.Root, "checkout", "main")
	commitFile(t, e.Root, "conflict.txt", "main\n")
	if _, err := git(e.Root, "merge", "conflict-source"); err == nil {
		t.Fatal("expected merge conflict")
	}
	if report, err := e.Check(); err != nil || !issueExists(report, "root", IssueDirtyCheckout, e.Root) {
		t.Fatalf("unfinished merge was not reported: %+v %v", report, err)
	}
	var report PruneReport
	err := e.Mutate(func() error {
		var pruneErr error
		report, pruneErr = e.Prune(func(PruneAction) bool { return true })
		return pruneErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete {
		t.Fatalf("unfinished merge was not cleaned: %+v", report)
	}
}

func TestPruneBranchDeletionUsesAuditedObjectIDAndContinues(t *testing.T) {
	e := fixture(t, 0)
	mustGit(t, e.Root, "branch", "changes-during-prune")
	mustGit(t, e.Root, "branch", "delete-me")
	changed := false
	var report PruneReport
	err := e.Mutate(func() error {
		var pruneErr error
		report, pruneErr = e.Prune(func(action PruneAction) bool {
			if action.Action == PruneDeleteBranch && action.Target == "changes-during-prune" {
				commit := mustGit(t, e.Root, "commit-tree", "HEAD^{tree}", "-m", "concurrent branch update")
				mustGit(t, e.Root, "update-ref", "refs/heads/changes-during-prune", commit)
				changed = true
			}
			return true
		})
		return pruneErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || report.Complete {
		t.Fatalf("compare-and-delete guard was not exercised: %+v", report)
	}
	statuses := map[string]string{}
	for _, action := range report.Actions {
		statuses[action.Target] = action.Status
	}
	if statuses["changes-during-prune"] != PruneFailed || statuses["delete-me"] != PruneCompleted {
		t.Fatalf("branch actions did not fail independently: %+v", report.Actions)
	}
}
