package vcm

import (
	"os"
	"path/filepath"
	"testing"
)

func treeRepository(t *testing.T, report TreeReport, name string) TreeRepository {
	t.Helper()
	for _, repository := range report.Repositories {
		if repository.Name == name {
			return repository
		}
	}
	t.Fatalf("tree report omitted repository %s", name)
	return TreeRepository{}
}

func TestTreeBaseCountsUniqueTrackedAndIndividualUntrackedFiles(t *testing.T) {
	e := fixture(t, 1)
	child := filepath.Join(e.Root, "repo0")
	put(t, filepath.Join(child, "file.txt"), "staged\n")
	mustGit(t, child, "add", "file.txt")
	put(t, filepath.Join(child, "file.txt"), "staged and unstaged\n")
	if err := os.MkdirAll(filepath.Join(child, "new"), 0755); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(child, "one.txt"), "one\n")
	put(t, filepath.Join(child, "new", "two.txt"), "two\n")
	put(t, filepath.Join(child, "ignored.txt"), "ignored\n")
	put(t, filepath.Join(child, ".git", "info", "exclude"), "ignored.txt\n")
	commitFile(t, e.Root, "root-local.txt", "local\n")

	report, err := e.Tree(e.Root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Context != "base" || report.Change != "" || len(report.Repositories) != 2 || report.Repositories[0].Name != "root" || report.Repositories[1].Name != "repo0" {
		t.Fatalf("unexpected base tree inventory: %+v", report)
	}
	root := treeRepository(t, report, "root")
	if root.SyncState != "ahead" || root.Ahead != 1 || !root.Cached || root.SyncTarget != "origin/main" {
		t.Fatalf("unexpected root synchronization state: %+v", root)
	}
	childState := treeRepository(t, report, "repo0")
	if childState.Clean || childState.TrackedChanges != 1 || childState.UntrackedFiles != 2 || childState.SyncState != "current" {
		t.Fatalf("unexpected child tree state: %+v", childState)
	}
}

func TestTreeReportsMissingCachedTargetWithoutFailure(t *testing.T) {
	e := fixture(t, 1)
	child := filepath.Join(e.Root, "repo0")
	mustGit(t, child, "update-ref", "-d", "refs/remotes/origin/main")
	report, err := e.Tree(e.Root)
	if err != nil {
		t.Fatal(err)
	}
	state := treeRepository(t, report, "repo0")
	if state.SyncState != "target missing" || state.Detail != "cached remote target is missing; run vcm fetch" {
		t.Fatalf("unexpected missing-target state: %+v", state)
	}
}

func TestTreeReportsCachedRemoteBehindState(t *testing.T) {
	e := fixture(t, 1)
	advancePullRemote(t, e.Config.Children[0].URL, "remote.txt")
	if err := e.Fetch(); err != nil {
		t.Fatal(err)
	}
	report, err := e.Tree(e.Root)
	if err != nil {
		t.Fatal(err)
	}
	state := treeRepository(t, report, "repo0")
	if state.SyncState != "behind" || state.Ahead != 0 || state.Behind != 1 {
		t.Fatalf("unexpected behind state: %+v", state)
	}
}

func TestTreeChangeUsesLocalTrunksAndKeepsPartialRepositoriesUnavailable(t *testing.T) {
	e := fixture(t, 1)
	manifest, err := e.CreateSelected("partial-tree", "", "repo0")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, manifest.Workspace, "change.txt", "change\n")
	commitFile(t, e.Root, "base.txt", "base\n")
	report, err := e.Tree(manifest.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if report.Context != "change" || report.Change != manifest.Tag || len(report.Repositories) != 2 {
		t.Fatalf("unexpected Change inventory: %+v", report)
	}
	root := treeRepository(t, report, "root")
	if root.SyncState != "diverged" || root.Ahead != 1 || root.Behind != 1 || root.Cached || root.SyncTarget != "main" {
		t.Fatalf("unexpected Change synchronization state: %+v", root)
	}
	child := treeRepository(t, report, "repo0")
	if child.Available || child.SyncState != "not selected" || child.Path != filepath.Join(manifest.Workspace, "repo0") {
		t.Fatalf("partial Change fell back to canonical checkout: %+v", child)
	}
}
