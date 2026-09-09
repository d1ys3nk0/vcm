package vcm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func saveContractConfig(t *testing.T, e *Engine) {
	t.Helper()
	data, err := yaml.Marshal(e.Config)
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, e.Root, "workspace.yml", string(data))
	mustGit(t, e.Root, "push", "origin", "main")
}

func TestRecoveryContractHookOrder(t *testing.T) {
	e := fixture(t, 2)
	log := filepath.Join(filepath.Dir(e.Root), "hook-events")
	t.Setenv("VCM_TEST_EVENTS", log)
	e.Config.Repositories[0].DependsOn = []string{e.Config.Repositories[1].Name}
	hook := func(phase string) Hook {
		return Hook{ID: phase, Command: `test "$PWD" = "$VCM_REPOSITORY_PATH"; test "$(git rev-parse --show-toplevel)" = "$VCM_REPOSITORY_PATH"; test "$(git rev-parse --abbrev-ref HEAD)" = "$VCM_CHANGE_TAG"; printf '%s/` + phase + `\n' "$VCM_REPOSITORY_NAME" >> "$VCM_TEST_EVENTS"`}
	}
	for i := range e.Config.Repositories {
		e.Config.Repositories[i].Hooks = Hooks{}
		for _, phase := range []string{"create", "merge", "drop"} {
			e.Config.Repositories[i].Hooks[phase] = []Hook{hook(phase)}
		}
	}
	e.Config.Hooks = Hooks{}
	for _, phase := range []string{"create", "pre-merge", "post-merge", "drop"} {
		e.Config.Hooks[phase] = []Hook{hook(phase)}
	}
	for _, phase := range []string{"create", "drop"} {
		e.Config.Hooks[phase][0].Command += `; test -e "$VCM_WORKSPACE/repo0/.git"; test -e "$VCM_WORKSPACE/repo1/.git"`
	}
	saveContractConfig(t, e)
	m, err := e.Create("hook-order")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Repositories {
		commitFile(t, r.Path, "feature.txt", "reviewed content\n")
	}
	if err := e.Merge(m); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "repo1/create\nrepo0/create\nworkspace/create\nworkspace/pre-merge\nrepo1/merge\nrepo0/merge\nworkspace/post-merge\nworkspace/drop\nrepo0/drop\nrepo1/drop\n"
	if string(data) != want {
		t.Fatalf("unexpected hook sequence:\n%s\nwant:\n%s", data, want)
	}
}

func TestRecoveryContractPendingSourceDrift(t *testing.T) {
	e := fixture(t, 2)
	allow := filepath.Join(filepath.Dir(e.Root), "allow-merge")
	t.Setenv("VCM_TEST_ALLOW", allow)
	e.Config.Hooks = Hooks{"pre-merge": {{ID: "reviewed", Command: "true"}}}
	e.Config.Repositories[0].Hooks = Hooks{"merge": {{ID: "blocked", Command: `test -f "$VCM_TEST_ALLOW"`}}}
	saveContractConfig(t, e)
	m, err := e.Create("pending-source")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Repositories[1:] {
		commitFile(t, r.Path, "feature.txt", "reviewed\n")
	}
	before := mustGit(t, m.Repositories[1].Origin, "rev-parse", "HEAD")
	if err := e.Merge(m); err == nil {
		t.Fatal("expected merge hook failure")
	}
	changed := m.Repositories[2]
	commitFile(t, changed.Path, "later.txt", "not reviewed\n")
	put(t, allow, "allowed")
	m, err = e.store.load(m.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Merge(m); err == nil {
		t.Fatal("accepted source changed after completed review gate")
	}
	if got := mustGit(t, m.Repositories[1].Origin, "rev-parse", "HEAD"); got != before {
		t.Fatal("merged before rejecting pending source drift")
	}
	if _, err := os.Stat(changed.Path); err != nil {
		t.Fatal("lost unreviewed source workspace:", err)
	}
}

func TestRecoveryContractDropHookCommitsPreserved(t *testing.T) {
	for _, owner := range []string{"workspace", "downstream"} {
		t.Run(owner, func(t *testing.T) {
			e := fixture(t, 1)
			hooks := Hooks{"drop": {{ID: "retained-output", Command: `printf 'retain this output\n' > hook-output.txt; git add hook-output.txt; git commit -m 'chore: retain hook output'`}}}
			if owner == "workspace" {
				e.Config.Hooks = hooks
			} else {
				e.Config.Repositories[0].Hooks = hooks
			}
			saveContractConfig(t, e)
			m, err := e.Create("retain-hook")
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Drop(m); err == nil {
				t.Fatal("drop accepted new unmerged hook commit")
			}
			index := 0
			if owner == "downstream" {
				index = 1
			}
			if _, err := os.Stat(filepath.Join(m.Repositories[index].Path, "hook-output.txt")); err != nil {
				t.Fatal("removed committed hook output:", err)
			}
			for _, r := range m.Repositories {
				if _, err := os.Stat(r.Path); err != nil {
					t.Fatal("removed workspace resources before rejecting new hook commit:", err)
				}
			}
		})
	}
}

func TestRecoveryContractRootRefCompletedWithNestedCheckouts(t *testing.T) {
	e := fixture(t, 2)
	m, err := e.Create("root-ref-recovery")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Repositories {
		commitFile(t, r.Path, "feature.txt", "committed source\n")
	}
	m.State = "merging"
	for i := 1; i < len(m.Repositories); i++ {
		if err := e.mergeOne(m, &m.Repositories[i]); err != nil {
			t.Fatal(err)
		}
	}
	r := &m.Repositories[0]
	r.Source = mustGit(t, r.Path, "rev-parse", "HEAD")
	r.TargetBefore = mustGit(t, r.Origin, "rev-parse", "HEAD")
	r.MergeTree = strings.Split(mustGit(t, r.Origin, "merge-tree", "--write-tree", r.TargetBefore, r.Source), "\n")[0]
	r.MergeCommit = mustGit(t, r.Origin, "commit-tree", r.MergeTree, "-p", r.TargetBefore, "-m", "feat: merge reviewed root")
	r.Intent = "merge"
	if err := e.store.save(m); err != nil {
		t.Fatal(err)
	}
	mustGit(t, r.Origin, "read-tree", "-u", "-m", r.TargetBefore, r.MergeCommit)
	mustGit(t, r.Origin, "update-ref", "refs/heads/"+r.Repository.Trunk, r.MergeCommit, r.TargetBefore)
	expected := r.MergeCommit
	m, err = e.store.load(m.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Merge(m); err != nil {
		t.Fatal(err)
	}
	if got := mustGit(t, e.Root, "rev-parse", "HEAD"); got != expected {
		t.Fatal("recovery duplicated or replaced intended root merge")
	}
	for _, r := range m.Repositories[1:] {
		if _, err := os.Stat(filepath.Join(r.Origin, "feature.txt")); err != nil {
			t.Fatal("lost merged downstream content:", err)
		}
	}
	if _, err := os.Stat(m.Workspace); !os.IsNotExist(err) {
		t.Fatal("owned Change workspace remains after completed recovery")
	}
}
