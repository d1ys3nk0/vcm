package vcm

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrefixedLineWriterPrefixesFragmentedLinesAndFlushesTail(t *testing.T) {
	var output bytes.Buffer
	writer := newPrefixedLineWriter(&output, "[hook/api/create-after/lint @ /work/api] ")
	for _, fragment := range []string{"All", " good\nSecond\npart", "ial"} {
		if _, err := writer.Write([]byte(fragment)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	want := "[hook/api/create-after/lint @ /work/api] All good\n" +
		"[hook/api/create-after/lint @ /work/api] Second\n" +
		"[hook/api/create-after/lint @ /work/api] partial\n"
	if output.String() != want {
		t.Fatalf("prefixed output:\n%s\nwant:\n%s", output.String(), want)
	}
}

func TestColoredFragmentedHookOutputStylesOnlyContext(t *testing.T) {
	const prefix = "\x1b[1;36m[hook/api/create-after/lint @ \x1b[0m/work/api\x1b[1;36m]\x1b[0m "
	var output bytes.Buffer
	writer := newPrefixedLineWriter(&output, prefix)
	for _, fragment := range []string{"payload says fa", "iled\nraw \x1b[31mbytes\n", "tail"} {
		if _, err := writer.Write([]byte(fragment)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	want := prefix + "payload says failed\n" + prefix + "raw \x1b[31mbytes\n" + prefix + "tail\n"
	if output.String() != want {
		t.Fatalf("colored prefixed output = %q, want %q", output.String(), want)
	}
}

func TestTypedLogSemanticsColorExplicitTokensOnly(t *testing.T) {
	var output bytes.Buffer
	e := &Engine{Out: &output, Style: func(semantic LogSemantic, text string) string {
		return fmt.Sprintf("<%d>%s</%d>", semantic, text, semantic)
	}}
	if err := e.logHook("api", "merge-before", "gate", "/work/api", "failed", LogFailure, " detail says completed"); err != nil {
		t.Fatal(err)
	}
	e.logOperationOutcome("prune", "api", "/work/api", "delete_branch ", "declined", LogWarning, " for topic")
	e.logOperationOutcome("bootstrap", "api", "/work/api", "", "cloned", LogChanged, " trunk %s", "main")
	e.logOperationOutcome("refresh", "api", "/work/api", "", "already current", LogSuccess, " at base %s", "abcdef")
	want := "<0>[hook/api/merge-before/gate @ </0>/work/api<0>]</0> <4>failed</4> detail says completed\n" +
		"<0>[prune/api @ </0>/work/api<0>]</0> delete_branch <2>declined</2> for topic\n" +
		"<0>[bootstrap/api @ </0>/work/api<0>]</0> <1>cloned</1> trunk main\n" +
		"<0>[refresh/api @ </0>/work/api<0>]</0> <3>already current</3> at base abcdef\n"
	if output.String() != want {
		t.Fatalf("semantic log output = %q, want %q", output.String(), want)
	}
}

func TestHookLoggingIncludesLifecycleOutputAndExecutionPath(t *testing.T) {
	e := fixture(t, 0)
	e.Config.Root.Hooks = Hooks{HookCreateBefore: {{
		ID:    "lint",
		Shell: `printf 'All '; printf 'good\nSecond\n' >&2; printf tail`,
	}}}
	saveContractConfig(t, e)
	var output bytes.Buffer
	e.Out = &output
	if _, err := e.Create("logged-hook"); err != nil {
		t.Fatal(err)
	}
	prefix := "[hook/root/create-before/lint @ " + e.Root + "] "
	want := []string{
		prefix + "started\n",
		prefix + "All good\n",
		prefix + "Second\n",
		prefix + "tail\n",
		prefix + "completed\n",
	}
	log := output.String()
	position := 0
	for _, line := range want {
		next := strings.Index(log[position:], line)
		if next < 0 {
			t.Fatalf("missing ordered hook log %q in:\n%s", line, log)
		}
		position += next + len(line)
	}
}

func TestFailedHookFlushesOutputWithoutCompletion(t *testing.T) {
	e := fixture(t, 0)
	e.Config.Root.Hooks = Hooks{HookCreateBefore: {{ID: "gate", Shell: `printf unfinished; false`}}}
	saveContractConfig(t, e)
	var output bytes.Buffer
	e.Out = &output
	if _, err := e.Create("failed-hook"); err == nil {
		t.Fatal("failing hook succeeded")
	}
	prefix := "[hook/root/create-before/gate @ " + e.Root + "] "
	log := output.String()
	if !strings.Contains(log, prefix+"started\n") || !strings.Contains(log, prefix+"unfinished\n") {
		t.Fatalf("missing failed hook context:\n%s", log)
	}
	if strings.Contains(log, prefix+"completed\n") {
		t.Fatalf("failed hook emitted completion:\n%s", log)
	}
}

func TestBootstrapLoggingIdentifiesClonedRepository(t *testing.T) {
	e := fixture(t, 1)
	path := filepath.Join(e.Root, "repo0")
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	e.Out = &output
	if err := e.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	want := "[bootstrap/repo0 @ " + path + "] cloned trunk main\n"
	if output.String() != want {
		t.Fatalf("bootstrap log %q, want %q", output.String(), want)
	}
}

func TestOperationLoggingCoversLifecycleOutcomes(t *testing.T) {
	e := fixture(t, 1)
	var output bytes.Buffer
	e.Out = &output
	childOrigin := filepath.Join(e.Root, "repo0")
	if err := e.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := e.Sync(); err != nil {
		t.Fatal(err)
	}
	m, err := e.Create("logged-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	commitFile(t, childOrigin, "advanced.txt", "advanced\n")
	if err = e.Refresh(m); err != nil {
		t.Fatal(err)
	}
	commitFile(t, m.Repositories[0].Path, "logged.txt", "changed\n")
	if err = e.Merge(m, "feat: log lifecycle"); err != nil {
		t.Fatal(err)
	}
	log := output.String()
	for _, want := range []string{
		"[bootstrap/repo0 @ " + childOrigin + "] validated existing checkout on trunk main",
		"[sync/root @ " + e.Root + "] already current cached origin/main at ",
		"[sync/repo0 @ " + childOrigin + "] already current cached origin/main at ",
		"[create/root @ " + e.Root + "] synchronized trunk main ",
		" (rebase)",
		"[create/root @ " + m.Repositories[0].Path + "] created managed worktree at ",
		"[refresh/root @ " + m.Repositories[0].Path + "] already current at base ",
		"[refresh/repo0 @ " + m.Repositories[1].Path + "] updated base ",
		"[merge/repo0 @ " + childOrigin + "] trunk main unchanged at ",
		"[merge/root @ " + e.Root + "] applied trunk main ",
		"[merge/root @ " + m.Repositories[0].Path + "] removed managed worktree and branch " + m.Tag,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q in operation log:\n%s", want, log)
		}
	}
}

func TestDropLogsRecoveryBackupAndRemoval(t *testing.T) {
	e := fixture(t, 0)
	m, err := e.Create("logged-drop")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Workspace, "untracked.txt"), []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	e.Out = &output
	e.Force = true
	if err = e.Drop(m); err != nil {
		t.Fatal(err)
	}
	prefix := "[drop/root @ " + m.Workspace + "] "
	for _, want := range []string{prefix + "created recovery backup ", prefix + "removed managed worktree and branch " + m.Tag} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing %q in drop log:\n%s", want, output.String())
		}
	}
}

func TestPruneLogsCompletedDeclinedAndBlockedActions(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		e := fixture(t, 0)
		mustGit(t, e.Root, "branch", "remove-me")
		var output bytes.Buffer
		e.Out = &output
		report, err := e.Prune(func(PruneAction) bool { return true })
		if err != nil || !report.Complete {
			t.Fatalf("prune: %+v %v", report, err)
		}
		want := "[prune/root @ " + e.Root + "] delete_branch completed for remove-me"
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing completed log %q in:\n%s", want, output.String())
		}
	})

	t.Run("declined and blocked", func(t *testing.T) {
		e := fixture(t, 0)
		unexpected := filepath.Join(filepath.Dir(e.Root), "unexpected-worktree")
		mustGit(t, e.Root, "worktree", "add", "-b", "keep-me", unexpected)
		var output bytes.Buffer
		e.Out = &output
		report, err := e.Prune(func(action PruneAction) bool {
			return action.Action == PruneDeleteBranch
		})
		if err != nil || report.Complete {
			t.Fatalf("prune: %+v %v", report, err)
		}
		for _, want := range []string{
			"[prune/root @ " + unexpected + "] remove_worktree declined",
			"[prune/root @ " + e.Root + "] delete_branch blocked for keep-me",
		} {
			if !strings.Contains(output.String(), want) {
				t.Errorf("missing %q in prune log:\n%s", want, output.String())
			}
		}
	})

	t.Run("failed", func(t *testing.T) {
		e := fixture(t, 0)
		mustGit(t, e.Root, "branch", "changed-during-prune")
		var output bytes.Buffer
		e.Out = &output
		report, err := e.Prune(func(action PruneAction) bool {
			commitFile(t, e.Root, "advance.txt", "advance\n")
			mustGit(t, e.Root, "branch", "-f", action.Target, "HEAD")
			return true
		})
		if err != nil || len(report.Actions) != 1 || report.Actions[0].Status != PruneFailed {
			t.Fatalf("prune failure result: %+v %v", report, err)
		}
		want := "[prune/root @ " + e.Root + "] delete_branch failed for changed-during-prune"
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing failed prune log %q in:\n%s", want, output.String())
		}
	})
}
