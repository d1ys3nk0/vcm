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
	commitFile(t, e.Root, "vcm.yml", string(data))
	mustGit(t, e.Root, "push", "origin", "main")
}

func TestRecoveryContractHookOrder(t *testing.T) {
	e := fixture(t, 2)
	log := filepath.Join(filepath.Dir(e.Root), "hook-events")
	t.Setenv("VCM_TEST_EVENTS", log)
	e.Config.Children[0].DependsOn = []string{e.Config.Children[1].Name}
	hook := func(phase string) Hook {
		return Hook{ID: phase, Shell: `case "$VCM_HOOK_PHASE" in create-before|merge-after|drop-after) expected="$VCM_REPOSITORY_ORIGIN" ;; *) expected="$VCM_REPOSITORY_PATH" ;; esac; test "$PWD" = "$expected"; test "$VCM_ROOT" != "$VCM_ROOT_ORIGIN"; test "$VCM_HOOK_PHASE" = "` + phase + `"; test "$VCM_HOOK_ID" = "` + phase + `"; printf '%s/` + phase + `\n' "$VCM_REPOSITORY_NAME" >> "$VCM_TEST_EVENTS"`}
	}
	for i := range e.Config.Children {
		e.Config.Children[i].Hooks = Hooks{}
		for _, phase := range []string{HookCreateBefore, HookCreateAfter, HookMergeBefore, HookMergeAfter} {
			e.Config.Children[i].Hooks[phase] = []Hook{hook(phase)}
		}
	}
	e.Config.Root.Hooks = Hooks{}
	for _, phase := range []string{HookCreateBefore, HookCreateAfter, HookMergeBefore, HookMergeAfter} {
		e.Config.Root.Hooks[phase] = []Hook{hook(phase)}
	}
	e.Config.Root.Hooks[HookCreateBefore][0].Shell += `; test ! -e "$VCM_ROOT"`
	e.Config.Root.Hooks[HookCreateAfter][0].Shell += `; test -e "$VCM_ROOT/repo0/.git"; test -e "$VCM_ROOT/repo1/.git"`
	e.Config.Root.Hooks[HookMergeBefore][0].Shell += `; test -e "$VCM_ROOT/repo0/.git"; test -e "$VCM_ROOT/repo1/.git"`
	e.Config.Root.Hooks[HookMergeAfter][0].Shell += `; test ! -e "$VCM_ROOT"`
	saveContractConfig(t, e)
	m, err := e.Create("hook-order")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Repositories {
		commitFile(t, r.Path, "feature.txt", "reviewed content\n")
	}
	if err := e.Merge(m, "feat: test change"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "root/create-before\nrepo1/create-before\nrepo1/create-after\nrepo0/create-before\nrepo0/create-after\nroot/create-after\nroot/merge-before\nrepo1/merge-before\nrepo0/merge-before\nrepo1/merge-after\nrepo0/merge-after\nroot/merge-after\n"
	if string(data) != want {
		t.Fatalf("unexpected hook sequence:\n%s\nwant:\n%s", data, want)
	}
}

func TestRecoveryContractDropOrderAndPartialSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		only   string
		want   string
		absent string
	}{
		{name: "all", want: "root/drop-before\nrepo0/drop-before\nrepo0/drop-after\nrepo1/drop-before\nrepo1/drop-after\nroot/drop-after\n"},
		{name: "partial", only: "repo1", want: "root/drop-before\nrepo1/drop-before\nrepo1/drop-after\nroot/drop-after\n", absent: "repo0/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fixture(t, 2)
			e.Config.Children[0].DependsOn = []string{e.Config.Children[1].Name}
			log := filepath.Join(filepath.Dir(e.Root), "drop-events")
			t.Setenv("VCM_TEST_EVENTS", log)
			hook := func(phase string) Hook {
				return Hook{ID: phase, Shell: `case "$VCM_HOOK_PHASE" in drop-before) test "$PWD" = "$VCM_REPOSITORY_PATH" ;; drop-after) test "$PWD" = "$VCM_REPOSITORY_ORIGIN"; test ! -e "$VCM_REPOSITORY_PATH" ;; esac; printf '%s/` + phase + `\n' "$VCM_REPOSITORY_NAME" >> "$VCM_TEST_EVENTS"`}
			}
			e.Config.Root.Hooks = Hooks{HookDropBefore: {hook(HookDropBefore)}, HookDropAfter: {hook(HookDropAfter)}}
			for i := range e.Config.Children {
				e.Config.Children[i].Hooks = Hooks{HookDropBefore: {hook(HookDropBefore)}, HookDropAfter: {hook(HookDropAfter)}}
			}
			saveContractConfig(t, e)
			m, err := e.CreateSelected("drop-order", tc.only, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Drop(m); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.want || tc.absent != "" && strings.Contains(string(data), tc.absent) {
				t.Fatalf("unexpected drop hook sequence:\n%s\nwant:\n%s", data, tc.want)
			}
		})
	}
}

func TestRecoveryContractHookGeneratedCommitsReachExpectedBaseline(t *testing.T) {
	e := fixture(t, 1)
	commit := func(name string) Hook {
		return Hook{ID: name, Shell: `printf '%s\n' "$VCM_HOOK_PHASE" > ` + name + `.txt; git add ` + name + `.txt; git commit -m 'chore: ` + name + `'`}
	}
	e.Config.Root.Hooks = Hooks{
		HookCreateBefore: {commit("root-created")},
		HookMergeBefore:  {commit("root-archived")},
	}
	e.Config.Children[0].Hooks = Hooks{HookCreateBefore: {commit("child-created")}}
	saveContractConfig(t, e)
	m, err := e.Create("hook-commits")
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{0, 1} {
		if got := mustGit(t, m.Repositories[index].Path, "rev-parse", "HEAD"); got != m.Repositories[index].Base {
			t.Fatalf("repository %s creation baseline excludes create-before commit", m.Repositories[index].Repository.Name)
		}
	}
	if _, err = os.Stat(filepath.Join(m.Workspace, "root-created.txt")); err != nil {
		t.Fatal("root create-before output missing from Change worktree:", err)
	}
	if _, err = os.Stat(filepath.Join(m.Repositories[1].Path, "child-created.txt")); err != nil {
		t.Fatal("child create-before output missing from Change worktree:", err)
	}
	if err = e.Merge(m, "feat: test change"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(e.Root, "root-archived.txt")); err != nil {
		t.Fatal("root merge-before commit did not reach base integration:", err)
	}
}

func TestRecoveryContractPendingSourceDrift(t *testing.T) {
	e := fixture(t, 2)
	allow := filepath.Join(filepath.Dir(e.Root), "allow-merge")
	t.Setenv("VCM_TEST_ALLOW", allow)
	e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "reviewed", Shell: "true"}}}
	e.Config.Children[0].Hooks = Hooks{HookMergeBefore: {{ID: "blocked", Shell: `test -f "$VCM_TEST_ALLOW"`}}}
	saveContractConfig(t, e)
	m, err := e.Create("pending-source")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Repositories[1:] {
		commitFile(t, r.Path, "feature.txt", "reviewed\n")
	}
	before := mustGit(t, m.Repositories[1].Origin, "rev-parse", "HEAD")
	if err := e.Merge(m, "feat: test change"); err == nil {
		t.Fatal("expected merge hook failure")
	}
	changed := m.Repositories[2]
	commitFile(t, changed.Path, "later.txt", "not reviewed\n")
	put(t, allow, "allowed")
	m, err = e.store.load(m.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Merge(m, "feat: test change"); err == nil {
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
	for _, owner := range []string{"root", "downstream"} {
		t.Run(owner, func(t *testing.T) {
			e := fixture(t, 1)
			hooks := Hooks{HookDropBefore: {{ID: "retained-output", Shell: `printf 'retain this output\n' > hook-output.txt; git add hook-output.txt; git commit -m 'chore: retain hook output'`}}}
			if owner == "root" {
				e.Config.Root.Hooks = hooks
			} else {
				e.Config.Children[0].Hooks = hooks
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
	for i := range m.Repositories {
		m.Repositories[i].Source = mustGit(t, m.Repositories[i].Path, "rev-parse", "HEAD")
	}
	m.MergeMessage = "feat: test change"
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
	m, err = e.store.load(m.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Merge(m, "feat: test change"); err != nil {
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
