package vcm

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpansionPreservesWorkAndResumesHooks(t *testing.T) {
	e := fixture(t, 2)
	log := filepath.Join(t.TempDir(), "root-hooks")
	gate := filepath.Join(t.TempDir(), "allow")
	t.Setenv("VCM_TEST_EVENTS", log)
	t.Setenv("VCM_TEST_GATE", gate)
	e.Config.Root.Hooks = Hooks{HookCreateAfter: {{ID: "inventory", Shell: `echo root >> "$VCM_TEST_EVENTS"`}}}
	e.Config.Children[0].Hooks = Hooks{HookCreateAfter: {{ID: "historical", Shell: `echo child >> "$VCM_TEST_EVENTS"`}}}
	e.Config.Children[1].Hooks = Hooks{HookCreateAfter: {{ID: "gate", Shell: `test -f "$VCM_TEST_GATE"`}}}
	saveContractConfig(t, e)
	e, err := Open(e.Root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.CreateSelected("expansion", "repo0", "")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, m.Repositories[1].Path, "feature.txt", "existing work")
	original := mustGit(t, m.Repositories[1].Path, "rev-parse", "HEAD")
	// Adopting a newly named creation hook must not retroactively run it for an existing child.
	e.Config.Children[0].Hooks = Hooks{HookCreateAfter: {{ID: "new-historical", Shell: "exit 99"}}}
	saveContractConfig(t, e)
	e, err = Open(e.Root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.AdoptConfiguration(m.Tag, e.Root); err != nil {
		t.Fatal(err)
	}
	m, err = e.Select(m.Tag, e.Root)
	if err != nil {
		t.Fatal(err)
	}

	if err = e.Add(m, "repo1"); err == nil {
		t.Fatal("hook should fail")
	}
	if m.State != "expanding" {
		t.Fatal(m.State)
	}
	put(t, gate, "yes")
	if err = e.Add(m, "repo1"); err != nil {
		t.Fatal(err)
	}
	if len(m.Repositories) != 3 || m.State != "ready" {
		t.Fatal("expansion incomplete")
	}
	if got := mustGit(t, m.Repositories[1].Path, "rev-parse", "HEAD"); got != original {
		t.Fatal("existing work changed")
	}
	if err = e.Add(m, "repo1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "root\n") != 2 {
		t.Fatalf("root hook generations: %q", data)
	}
}
func TestExpansionRejectsMissingDependencyAndDirtyWork(t *testing.T) {
	e := fixture(t, 3)
	e.Config.Children[2].DependsOn = []string{"repo1"}
	saveContractConfig(t, e)
	e, err := Open(e.Root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.CreateSelected("expansion", "repo0", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Add(m, "repo2"); err == nil {
		t.Fatal("missing dependency accepted")
	}
	put(t, filepath.Join(m.Workspace, "dirty.txt"), "work")
	if err = e.Add(m, "repo1,repo2"); err == nil {
		t.Fatal("dirty work accepted")
	}
	if len(m.Repositories) != 2 || m.State != "ready" {
		t.Fatal("failed preflight mutated selection")
	}
}
func TestRetainedCleanupAllowsDescendantsAndRejectsWork(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("retained")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, m.Repositories[1].Path, "feature.txt", "feature")
	e.Keep = true
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	if m.State != "integrated" {
		t.Fatal(m.State)
	}
	if _, err = os.Stat(m.Workspace); err != nil {
		t.Fatal("worktree removed")
	}
	if err = e.Add(m, "repo0"); err == nil {
		t.Fatal("integrated expansion accepted")
	}
	if err = e.Publish(m); err == nil {
		t.Fatal("integrated publication accepted")
	}
	commitFile(t, e.Root, "later.txt", "later")
	commitFile(t, m.Repositories[1].Origin, "later.txt", "later")
	put(t, filepath.Join(m.Workspace, "dirty.txt"), "work")
	if err = e.Cleanup(m); err == nil {
		t.Fatal("dirty cleanup accepted")
	}
	if err = os.Remove(filepath.Join(m.Workspace, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	if err = e.Cleanup(m); err != nil {
		t.Fatal(err)
	}
	if m.State != "dropped" {
		t.Fatal(m.State)
	}
}
func TestRetainedCleanupRetriesAfterHookFailure(t *testing.T) {
	e := fixture(t, 0)
	gate := filepath.Join(t.TempDir(), "allow")
	t.Setenv("VCM_TEST_GATE", gate)
	e.Config.Root.Hooks = Hooks{HookMergeAfter: {{ID: "gate", Shell: `test -f "$VCM_TEST_GATE"`}}}
	saveContractConfig(t, e)
	e, err := Open(e.Root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.Create("retained")
	if err != nil {
		t.Fatal(err)
	}
	e.Keep = true
	if err = e.Merge(m); err != nil {
		t.Fatal(err)
	}
	if err = e.Cleanup(m); err == nil {
		t.Fatal("hook should fail")
	}
	if _, err = os.Stat(m.Workspace); !os.IsNotExist(err) {
		t.Fatal("cleanup hook ran before removal")
	}
	put(t, gate, "yes")
	if err = e.Cleanup(m); err != nil {
		t.Fatal(err)
	}
}
func TestConfigurationAdoptionIsExplicitAndPreservesHistoricalHooks(t *testing.T) {
	e := fixture(t, 0)
	log := filepath.Join(t.TempDir(), "hooks")
	t.Setenv("VCM_TEST_EVENTS", log)
	e.Config.Root.Hooks = Hooks{HookCreateAfter: {{ID: "history", Shell: `echo before >> "$VCM_TEST_EVENTS"`}}}
	saveContractConfig(t, e)
	e, err := Open(e.Root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.Create("config")
	if err != nil {
		t.Fatal(err)
	}
	e.Config.Root.Hooks[HookCreateAfter][0].Shell = `echo after >> "$VCM_TEST_EVENTS"`
	saveContractConfig(t, e)
	e, err = Open(e.Root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m, err = e.Select(m.Tag, e.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.ensureCurrentSelection(m); err == nil {
		t.Fatal("drift not blocked")
	}
	e.DryRun = true
	preview, err := e.AdoptConfiguration(m.Tag, e.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Changes) == 0 {
		t.Fatal("missing drift report")
	}
	m, err = e.Select(m.Tag, e.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.ensureCurrentSelection(m); err == nil {
		t.Fatal("dry run adopted changes")
	}
	e.DryRun = false
	if _, err = e.AdoptConfiguration(m.Tag, e.Root); err != nil {
		t.Fatal(err)
	}
	m, err = e.Select(m.Tag, e.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.ensureCurrentSelection(m); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before\n" {
		t.Fatal("adoption executed historical hook")
	}
}

func TestMergeRetryRejectsChangedRetentionChoice(t *testing.T) {
	e := fixture(t, 0)
	e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "block", Shell: "exit 1"}}}
	saveContractConfig(t, e)
	e, err := Open(e.Root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.Create("retention")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Merge(m); err == nil {
		t.Fatal("merge hook should fail")
	}
	if m.State != "merging" {
		t.Fatal(m.State)
	}
	e.Keep = true
	if err = e.Merge(m); err == nil || !strings.Contains(err.Error(), "retention choice") {
		t.Fatalf("retry accepted retention change: %v", err)
	}
	if m.Keep || m.State != "merging" {
		t.Fatal("retry changed journal")
	}
	if _, err = os.Stat(m.Workspace); err != nil {
		t.Fatal("retry cleaned worktree")
	}
}

func TestExpansionRetryPreviewReportsCheckpointsWithoutMutation(t *testing.T) {
	e := fixture(t, 2)
	e.Config.Root.Hooks = Hooks{HookCreateAfter: {{ID: "inventory", Shell: "true"}}}
	e.Config.Children[1].Hooks = Hooks{HookCreateAfter: {{ID: "gate", Shell: "exit 1"}}}
	saveContractConfig(t, e)
	e, err := Open(e.Root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.CreateSelected("preview", "repo0", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Add(m, "repo1"); err == nil {
		t.Fatal("hook should fail")
	}
	outcome := m.Hooks["repo1/create-after/gate"]
	outcome.Status = "running"
	m.Hooks["repo1/create-after/gate"] = outcome
	if err = e.store.save(m); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(e.store.dir, m.Tag+".json")
	before, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	plan := e.AddPlan(m, "repo1")
	foundWorktree, foundChildHook, foundRoot := false, false, false
	for _, step := range plan.Steps {
		if step.Repository == "repo1" && step.Phase == "create" && step.Checkpoint == "complete" {
			foundWorktree = true
		}
		if step.Repository == "repo1" && step.Phase == HookCreateAfter && step.Checkpoint == "running" {
			foundChildHook = true
		}
		if step.Repository == "root" && step.Phase == HookCreateAfter && step.Checkpoint == "pending" {
			foundRoot = true
		}
	}
	if !foundWorktree || !foundChildHook || !foundRoot || len(plan.Blockers) == 0 {
		t.Fatalf("incomplete expansion preview: %+v", plan)
	}
	after, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || m.Hooks["repo1/create-after/gate"].Status != "running" {
		t.Fatal("preview changed checkpoint")
	}
}

func TestExpansionRejectsMalformedSavedJournal(t *testing.T) {
	e := fixture(t, 2)
	e.Config.Children[1].Hooks = Hooks{HookCreateAfter: {{ID: "gate", Shell: "exit 1"}}}
	saveContractConfig(t, e)
	e, err := Open(e.Root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.CreateSelected("journal", "repo0", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Add(m, "repo1"); err == nil {
		t.Fatal("hook should fail")
	}
	filename := filepath.Join(e.store.dir, m.Tag+".json")
	original, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, field string
		values      []string
	}{
		{"missing-new-repositories", "expansion_added", nil},
		{"duplicate-new-repositories", "expansion_added", []string{"repo1", "repo1"}},
		{"new-repository-not-requested", "expansion_added", []string{"repo0"}},
		{"duplicate-requested-repositories", "expansion", []string{"repo1", "repo1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var document map[string]json.RawMessage
			if err := json.Unmarshal(original, &document); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(tc.values)
			if err != nil {
				t.Fatal(err)
			}
			document[tc.field] = data
			data, err = json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filename, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = e.All(); err == nil {
				t.Fatal("malformed expansion journal accepted")
			}
		})
	}
	if err = os.WriteFile(filename, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = e.All(); err != nil {
		t.Fatalf("valid journal rejected: %v", err)
	}
}
