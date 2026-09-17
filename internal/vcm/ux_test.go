package vcm

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateUsesLocalTrunksWithoutRemoteAccess(t *testing.T) {
	e := fixture(t, 1)
	// Make remotes unavailable without changing configured URLs or local refs.
	for _, r := range append([]Repository{{URL: mustGit(t, e.Root, "remote", "get-url", "origin")}}, e.Config.Children...) {
		if err := os.Rename(r.URL, r.URL+".offline"); err != nil {
			t.Fatal(err)
		}
	}
	rootBefore := mustGit(t, e.Root, "rev-parse", "HEAD")
	child := filepath.Join(e.Root, e.Config.Children[0].Path)
	childBefore := mustGit(t, child, "rev-parse", "HEAD")
	m, err := e.Create("local-only")
	if err != nil {
		t.Fatal(err)
	}
	if m.Repositories[0].Base != rootBefore || m.Repositories[1].Base != childBefore {
		t.Fatal("creation changed local baselines")
	}
	if mustGit(t, e.Root, "rev-parse", "HEAD") != rootBefore || mustGit(t, child, "rev-parse", "HEAD") != childBefore {
		t.Fatal("canonical refs moved")
	}
}
func TestCreatePreflightsEveryTrunkBeforeHooks(t *testing.T) {
	e := fixture(t, 1)
	marker := filepath.Join(filepath.Dir(e.Root), "hook-ran")
	e.Config.Root.Hooks = Hooks{HookCreateBefore: {{ID: "marker", Shell: "touch " + quoteArgument(marker)}}}
	saveContractConfig(t, e)
	child := filepath.Join(e.Root, e.Config.Children[0].Path)
	mustGit(t, child, "checkout", "-b", "other")
	if _, err := e.Create("wrong-trunk"); err == nil {
		t.Fatal("accepted non-trunk checkout")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("hook ran before all-repository preflight")
	}
	if mustGit(t, child, "branch", "--show-current") != "other" {
		t.Fatal("creation silently changed branch")
	}
}
func TestCreateHookCannotSwitchCanonicalBranch(t *testing.T) {
	e := fixture(t, 0)
	e.Config.Root.Hooks = Hooks{HookCreateBefore: {{ID: "switch", Shell: "git checkout -b other"}}}
	saveContractConfig(t, e)
	if _, err := e.Create("hook-switch"); err == nil || !strings.Contains(err.Error(), "must be on trunk") {
		t.Fatalf("accepted branch-changing hook: %v", err)
	}
}
func TestSelectUniqueNamesAndHistoricalAmbiguity(t *testing.T) {
	e := fixture(t, 0)
	m, err := e.Create("named")
	if err != nil {
		t.Fatal(err)
	}
	if found, err := e.Select("named", e.Root); err != nil || found.Tag != m.Tag {
		t.Fatalf("slug selection: %v", err)
	}
	if found, err := e.Select("../"+filepath.Base(m.Workspace), e.Root); err != nil || found.Tag != m.Tag {
		t.Fatalf("relative context: %v", err)
	}
	copy := *m
	copy.Tag = "250101010101-named"
	copy.Workspace = changeWorkspace(e.Root, copy.Tag)
	copy.State = "dropped"
	if err = e.store.save(&copy); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Select("named", e.Root); err == nil || !strings.Contains(err.Error(), m.Tag) || !strings.Contains(err.Error(), copy.Tag) {
		t.Fatalf("missing ambiguity candidates: %v", err)
	}
	if found, err := e.Select(m.Tag, e.Root); err != nil || found.Tag != m.Tag {
		t.Fatalf("exact tag: %v", err)
	}
}
func TestRecoveryAcknowledgesOnlyInterruptedHook(t *testing.T) {
	e := fixture(t, 0)
	e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "verify", Shell: "true"}}}
	saveContractConfig(t, e)
	m, err := e.Create("recover-hook")
	if err != nil {
		t.Fatal(err)
	}
	m.State = "merging"
	key := "root/merge-before/verify"
	m.Hooks[key] = HookState{Status: "running"}
	if err = e.store.save(m); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(e.store.dir, m.Tag+".json")
	before, _ := os.ReadFile(filename)
	if _, err = e.Recover(m.Tag, e.Root, key, false); err == nil {
		t.Fatal("accepted missing acknowledgment")
	}
	after, _ := os.ReadFile(filename)
	if !bytes.Equal(before, after) {
		t.Fatal("missing acknowledgment changed state")
	}
	e.DryRun = true
	if _, err = e.Recover(m.Tag, e.Root, key, true); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(filename)
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed state")
	}
	e.DryRun = false
	result, err := e.Recover(m.Tag, e.Root, key, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.RetryCommand, "--workspace") {
		t.Fatal("retry lacks canonical context")
	}
	loaded, err := e.Select(m.Tag, e.Root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Hooks[key].Status != "failed" || loaded.Repositories[0].Base != m.Repositories[0].Base {
		t.Fatal("recovery modified wrong state")
	}
	if _, err = e.Recover(m.Tag, e.Root, key, true); err == nil {
		t.Fatal("accepted already acknowledged hook")
	}
}
func TestLegacyCreationAndJournalsBlockWithoutChangingEvidence(t *testing.T) {
	for _, kind := range []string{"creating", "journal"} {
		t.Run(kind, func(t *testing.T) {
			e := fixture(t, 0)
			m, err := e.Create("legacy")
			if err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(e.store.dir, m.Tag+".json")
			if kind == "creating" {
				raw, _ := os.ReadFile(filename)
				var state persistedManifest
				if err = json.Unmarshal(raw, &state); err != nil {
					t.Fatal(err)
				}
				state.Version = 2
				state.State = "creating"
				raw, _ = json.Marshal(state)
				if err = os.WriteFile(filename, raw, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				filename = filepath.Join(e.store.dir, "root.sync")
				if err = os.WriteFile(filename, []byte("legacy evidence"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(filename)
			called := false
			err = e.Mutate(func() error { called = true; return nil })
			if err == nil || called || !strings.Contains(err.Error(), "previous VCM binary") {
				t.Fatalf("legacy mutation not blocked: %v", err)
			}
			after, _ := os.ReadFile(filename)
			if !bytes.Equal(before, after) {
				t.Fatal("legacy evidence changed")
			}
		})
	}
}
func TestDryRunReportsBlockedDropWithoutMutation(t *testing.T) {
	e := fixture(t, 1)
	m, err := e.Create("preview-drop")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, m.Repositories[1].Path, "new.txt", "work\n")
	filename := filepath.Join(e.store.dir, m.Tag+".json")
	before, _ := os.ReadFile(filename)
	plan := e.Plan("drop", m)
	if len(plan.Blockers) == 0 {
		t.Fatal("preview omitted unmerged content blocker")
	}
	after, _ := os.ReadFile(filename)
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed manifest")
	}
	if _, err = os.Stat(m.Workspace); err != nil {
		t.Fatal(err)
	}
}

func TestCreatePreviewReusesInterruptedCheckpointAndBlocksRunningHook(t *testing.T) {
	e := fixture(t, 0)
	e.Config.Root.Hooks = Hooks{HookCreateAfter: {{ID: "stop", Shell: "false"}}}
	saveContractConfig(t, e)
	m, err := e.Create("preview-retry")
	if err == nil || m == nil {
		t.Fatal("expected interrupted creation")
	}
	m.Hooks["root/create-after/stop"] = HookState{Status: "running"}
	if err = e.store.save(m); err != nil {
		t.Fatal(err)
	}
	plan, err := e.CreatePlan(m.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tag != m.Tag || len(plan.Blockers) == 0 {
		t.Fatalf("retry preview lost identity or interruption: %+v", plan)
	}
	complete := false
	for _, step := range plan.Steps {
		if step.Phase == "create" && step.Checkpoint == "complete" {
			complete = true
		}
	}
	if !complete {
		t.Fatal("existing worktree creation not checkpointed in preview")
	}
}
func TestFinalizationPreviewSkipsEarlierHooks(t *testing.T) {
	e := fixture(t, 0)
	e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "before", Shell: "true"}}, HookMergeAfter: {{ID: "after", Shell: "false"}}}
	saveContractConfig(t, e)
	m, err := e.Create("preview-finalization")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Merge(m); err == nil {
		t.Fatal("expected finalization failure")
	}
	plan := e.Plan("merge", m)
	for _, step := range plan.Steps {
		if step.Phase == HookMergeBefore {
			t.Fatal("preview reruns pre-merge hooks after integration")
		}
		if step.Phase == "cleanup" && step.Checkpoint != "complete" {
			t.Fatal("preview lost completed cleanup")
		}
	}
}
