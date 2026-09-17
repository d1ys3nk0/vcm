package vcm

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func adoptionEngine(t *testing.T, e *Engine) *Engine {
	t.Helper()
	saveContractConfig(t, e)
	next, err := Open(e.Root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

// Keep the canonical revision fixed while editing a deliberately local configuration.
func adoptionLocalConfig(t *testing.T, e *Engine) *Engine {
	t.Helper()
	mustGit(t, e.Root, "update-index", "--skip-worktree", "vcm.yml")
	data, err := yaml.Marshal(e.Config)
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(e.Root, "vcm.yml"), string(data))
	next, err := Open(e.Root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func adoptionCheckpoint(t *testing.T, e *Engine, m *Manifest) {
	t.Helper()
	if err := e.store.save(m); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptPendingAndFailedMergeHooksWithoutExecution(t *testing.T) {
	for _, status := range []string{"pending", "failed"} {
		t.Run(status, func(t *testing.T) {
			e := fixture(t, 0)
			e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "gate", Shell: "exit 1"}}}
			e = adoptionEngine(t, e)
			m, err := e.Create("adopt-gate")
			if err != nil {
				t.Fatal(err)
			}
			m.State = "merging"
			if status == "failed" {
				m.Hooks["root/merge-before/gate"] = HookState{Status: "failed", Error: "gate failed"}
			}
			adoptionCheckpoint(t, e, m)
			marker := filepath.Join(t.TempDir(), "executed")
			t.Setenv("VCM_ADOPTION_MARKER", marker)
			e.Config.Root.Hooks[HookMergeBefore][0].Shell = `touch "$VCM_ADOPTION_MARKER"`
			e = adoptionLocalConfig(t, e)
			if _, err = e.AdoptConfiguration(m.Tag, e.Root); err != nil {
				t.Fatal(err)
			}
			loaded, err := e.Select(m.Tag, e.Root)
			if err != nil {
				t.Fatal(err)
			}
			if len(e.ConfigurationDrift(loaded)) != 0 {
				t.Fatal("adopted configuration still drifts")
			}
			if _, err = os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("adoption executed hook")
			}
			if state := loaded.Hooks["root/merge-before/gate"]; state.Status != "" {
				t.Fatalf("changed gate is not pending: %+v", state)
			}
		})
	}
}

func TestAdoptRemovedRunningHookRequiresAcknowledgment(t *testing.T) {
	e := fixture(t, 0)
	e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "gate", Shell: "true"}}}
	e = adoptionEngine(t, e)
	m, err := e.Create("removed-running")
	if err != nil {
		t.Fatal(err)
	}
	m.State = "merging"
	key := "root/merge-before/gate"
	m.Hooks[key] = HookState{Status: "running"}
	adoptionCheckpoint(t, e, m)
	e.Config.Root.Hooks = nil
	e = adoptionLocalConfig(t, e)
	if _, err = e.AdoptConfiguration(m.Tag, e.Root); err == nil {
		t.Fatal("adopted running hook removal without acknowledgment")
	}
	if _, err = e.Recover(m.Tag, e.Root, key, false); err == nil {
		t.Fatal("retry accepted without effects acknowledgment")
	}
	if _, err = e.Recover(m.Tag, e.Root, key, true); err != nil {
		t.Fatalf("cannot acknowledge removed running hook from recorded definition: %v", err)
	}
	if _, err = e.AdoptConfiguration(m.Tag, e.Root); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptCompletedGateRejectsDownstreamEffects(t *testing.T) {
	for _, effect := range []string{"merged", "removed"} {
		t.Run(effect, func(t *testing.T) {
			e := fixture(t, 0)
			e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "gate", Shell: "true"}}}
			e = adoptionEngine(t, e)
			m, err := e.Create("downstream")
			if err != nil {
				t.Fatal(err)
			}
			m.State = "merging"
			m.Hooks["root/merge-before/gate"] = HookState{Status: "complete"}
			r := &m.Repositories[0]
			r.Source, r.Target = r.Base, r.Base
			if effect == "merged" {
				r.Merged = true
			} else {
				r.Removed = true
			}
			adoptionCheckpoint(t, e, m)
			e.Config.Root.Hooks[HookMergeBefore][0].Shell = "echo changed"
			e = adoptionLocalConfig(t, e)
			if _, err = e.AdoptConfiguration(m.Tag, e.Root); err == nil || !strings.Contains(err.Error(), "downstream") {
				t.Fatalf("downstream adoption: %v", err)
			}
		})
	}
}

func TestAdoptChangedRootGateInvalidatesLaterChildGates(t *testing.T) {
	e := fixture(t, 1)
	e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "root-gate", Shell: "true"}}}
	e.Config.Children[0].Hooks = Hooks{HookMergeBefore: {{ID: "child-gate", Shell: "true"}}}
	e = adoptionEngine(t, e)
	m, err := e.Create("later-gates")
	if err != nil {
		t.Fatal(err)
	}
	m.State = "merging"
	for _, key := range []string{"root/merge-before/root-gate", "repo0/merge-before/child-gate"} {
		m.Hooks[key] = HookState{Status: "complete"}
	}
	adoptionCheckpoint(t, e, m)
	e.Config.Root.Hooks[HookMergeBefore][0].Shell = "echo changed"
	e = adoptionLocalConfig(t, e)
	if _, err = e.AdoptConfiguration(m.Tag, e.Root); err != nil {
		t.Fatal(err)
	}
	m, err = e.Select(m.Tag, e.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"root/merge-before/root-gate", "repo0/merge-before/child-gate"} {
		if m.Hooks[key].Status != "" {
			t.Fatalf("later gate not invalidated: %s", key)
		}
	}
}

func TestAdoptPythonRunnerChangePreservesShellOnlyPhase(t *testing.T) {
	e := fixture(t, 0)
	e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "shell-gate", Shell: "true"}}, HookMergeAfter: {{ID: "python-after", Python: "pass"}}}
	e = adoptionEngine(t, e)
	m, err := e.Create("phase-runner")
	if err != nil {
		t.Fatal(err)
	}
	m.State = "merging"
	key := "root/merge-before/shell-gate"
	m.Hooks[key] = HookState{Status: "complete"}
	adoptionCheckpoint(t, e, m)
	e.Config.Runners.Python = "python3"
	e = adoptionLocalConfig(t, e)
	if _, err = e.AdoptConfiguration(m.Tag, e.Root); err != nil {
		t.Fatal(err)
	}
	m, err = e.Select(m.Tag, e.Root)
	if err != nil {
		t.Fatal(err)
	}
	if m.Hooks[key].Status != "complete" {
		t.Fatal("Python runner change invalidated completed shell-only merge gate")
	}
}

func TestAdoptFailedLaterGatePreservesCompletedPrefix(t *testing.T) {
	for _, location := range []string{"child", "same-repository"} {
		t.Run(location, func(t *testing.T) {
			children := 0
			if location == "child" {
				children = 1
			}
			e := fixture(t, children)
			events := filepath.Join(t.TempDir(), "events")
			t.Setenv("VCM_ADOPTION_EVENTS", events)
			first := Hook{ID: "first", Shell: `echo first >> "$VCM_ADOPTION_EVENTS"`}
			failing := Hook{ID: "later", Shell: `echo old >> "$VCM_ADOPTION_EVENTS"; exit 1`}
			e.Config.Root.Hooks = Hooks{HookMergeBefore: {first}}
			if location == "child" {
				e.Config.Children[0].Hooks = Hooks{HookMergeBefore: {failing}}
			} else {
				e.Config.Root.Hooks[HookMergeBefore] = append(e.Config.Root.Hooks[HookMergeBefore], failing)
			}
			e = adoptionEngine(t, e)
			m, err := e.Create("prefix")
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Merge(m); err == nil {
				t.Fatal("expected initial gate failure")
			}
			changed := `echo new >> "$VCM_ADOPTION_EVENTS"; exit 1`
			if location == "child" {
				e.Config.Children[0].Hooks[HookMergeBefore][0].Shell = changed
			} else {
				e.Config.Root.Hooks[HookMergeBefore][1].Shell = changed
			}
			e = adoptionLocalConfig(t, e)
			if _, err = e.AdoptConfiguration(m.Tag, e.Root); err != nil {
				t.Fatal(err)
			}
			m, err = e.Select(m.Tag, e.Root)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Merge(m); err == nil {
				t.Fatal("expected replacement gate failure")
			}
			data, err := os.ReadFile(events)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "first\nold\nnew\n" {
				t.Fatalf("adoption reran completed earlier gate or skipped replacement: %q", data)
			}
		})
	}
}

func TestAdoptCommittedConfigRepairAllowsRefreshAndMerge(t *testing.T) {
	e := fixture(t, 1)
	events := filepath.Join(t.TempDir(), "events")
	t.Setenv("VCM_ADOPTION_EVENTS", events)
	e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "root-gate", Shell: `echo root >> "$VCM_ADOPTION_EVENTS"`}}}
	e.Config.Children[0].Hooks = Hooks{HookMergeBefore: {{ID: "repair", Shell: `echo failed >> "$VCM_ADOPTION_EVENTS"; exit 1`}}}
	e = adoptionEngine(t, e)
	m, err := e.Create("committed-repair")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, filepath.Join(m.Workspace, "repo0"), "feature.txt", "feature\n")
	if err = e.Merge(m); err == nil {
		t.Fatal("expected initial gate failure")
	}
	e.Config.Children[0].Hooks[HookMergeBefore][0].Shell = `echo repaired >> "$VCM_ADOPTION_EVENTS"`
	e = adoptionEngine(t, e)
	if _, err = e.AdoptConfiguration(m.Tag, e.Root); err != nil {
		t.Fatal(err)
	}
	m, err = e.Select(m.Tag, e.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Refresh(m); err != nil {
		t.Fatalf("committed hook repair cannot refresh after adoption: %v", err)
	}
	if err = e.Merge(m); err != nil {
		t.Fatalf("committed hook repair cannot merge after refresh: %v", err)
	}
	data, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "root\nfailed\nroot\nrepaired\n" {
		t.Fatalf("refreshed change did not rerun gates correctly: %q", data)
	}
	if got := mustGit(t, filepath.Join(e.Root, "repo0"), "show", "HEAD:feature.txt"); got != "feature" {
		t.Fatalf("feature not integrated: %q", got)
	}
}

func TestAdoptFailedHookCommittedSourceCanRetryMerge(t *testing.T) {
	e := fixture(t, 0)
	events := filepath.Join(t.TempDir(), "events")
	t.Setenv("VCM_ADOPTION_EVENTS", events)
	e.Config.Root.Hooks = Hooks{HookMergeBefore: {{ID: "commit-and-fail", Shell: `echo first >> "$VCM_ADOPTION_EVENTS"; printf "generated\n" > generated.txt; git add generated.txt; git commit -m "feat: generated content"; exit 1`}}}
	e = adoptionEngine(t, e)
	m, err := e.Create("source-repair")
	if err != nil {
		t.Fatal(err)
	}
	e.Keep = true
	const subject = "feat: repaired source"
	if err = e.Merge(m, subject); err == nil {
		t.Fatal("expected committing hook failure")
	}
	e.Config.Root.Hooks[HookMergeBefore][0].Shell = `echo repaired >> "$VCM_ADOPTION_EVENTS"`
	e = adoptionLocalConfig(t, e)
	if _, err = e.AdoptConfiguration(m.Tag, e.Root); err != nil {
		t.Fatal(err)
	}
	m, err = e.Select(m.Tag, e.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Merge(m); err != nil {
		t.Fatalf("adopted failed hook source cannot merge: %v", err)
	}
	if m.State != "integrated" {
		t.Fatalf("retry lost persisted retention: %s", m.State)
	}
	if got := mustGit(t, e.Root, "log", "-1", "--format=%s"); got != subject {
		t.Fatalf("retry lost merge subject: %q", got)
	}
	if got := mustGit(t, e.Root, "show", "HEAD:generated.txt"); got != "generated" {
		t.Fatalf("hook commit not integrated: %q", got)
	}
	if _, err = os.Stat(m.Workspace); err != nil {
		t.Fatalf("retained checkout removed: %v", err)
	}
	data, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first\nrepaired\n" {
		t.Fatalf("unexpected repaired hook executions: %q", data)
	}
}
