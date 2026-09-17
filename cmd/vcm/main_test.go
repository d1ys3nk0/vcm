package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d1ys3nk0/vcm/internal/vcm"
)

func runOutput(t *testing.T, args ...string) (string, error) {
	t.Helper()
	old := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = old; reader.Close(); writer.Close() }()
	callErr := run(args)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if callErr != nil {
		return string(data), callErr
	}
	return string(data), nil
}

func runJSON[T any](t *testing.T, args ...string) (T, error) {
	t.Helper()
	var result T
	data, err := runOutput(t, args...)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		t.Fatalf("invalid result %q: %v", data, err)
	}
	return result, nil
}

func runErrorOutput(t *testing.T, args ...string) (string, error) {
	t.Helper()
	old := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	defer func() { os.Stderr = old; reader.Close(); writer.Close() }()
	callErr := run(args)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err := io.Copy(&output, reader); err != nil {
		t.Fatal(err)
	}
	return output.String(), callErr
}

func runCapturedOutput(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = stdoutWriter, stderrWriter
	defer func() {
		os.Stdout, os.Stderr = oldStdout, oldStderr
		stdoutReader.Close()
		stderrReader.Close()
		stdoutWriter.Close()
		stderrWriter.Close()
	}()
	callErr := run(args)
	if err := stdoutWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stderrWriter.Close(); err != nil {
		t.Fatal(err)
	}
	stdout, err := io.ReadAll(stdoutReader)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := io.ReadAll(stderrReader)
	if err != nil {
		t.Fatal(err)
	}
	return string(stdout), string(stderr), callErr
}

func cliWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	config := "version: 1\nroot:\n  trunk: main\nchildren:\n- name: api\n  path: repos/api\n  url: /nonexistent-disposable-remote\n  trunk: main\n"
	if err := os.WriteFile(filepath.Join(root, "vcm.yml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func cliCleanWorkspace(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "VCM Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "vcm@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "VCM Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "vcm@example.test")
	root := filepath.Join(t.TempDir(), "workspace")
	for _, args := range [][]string{{"init", "--initial-branch=main", root}, {"-C", root, "config", "user.name", "VCM Test"}, {"-C", root, "config", "user.email", "vcm@example.test"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	config := "version: 1\nroot:\n  trunk: main\nchildren: []\n"
	if err := os.WriteFile(filepath.Join(root, "vcm.yml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", root, "add", "vcm.yml"}, {"-C", root, "commit", "-m", "chore: configure workspace"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func cliManagedChange(t *testing.T) (string, *vcm.Manifest) {
	t.Helper()
	root := cliCleanWorkspace(t)
	remote := filepath.Join(filepath.Dir(root), "workspace.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init remote: %s %v", out, err)
	}
	for _, args := range [][]string{{"-C", root, "remote", "add", "origin", remote}, {"-C", root, "push", "origin", "main"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	engine, err := vcm.Open(root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var manifest *vcm.Manifest
	if err := engine.Mutate(func() error {
		var createErr error
		manifest, createErr = engine.Create("context-selection")
		return createErr
	}); err != nil {
		t.Fatal(err)
	}
	return root, manifest
}

func TestVersionReportsBuildMetadata(t *testing.T) {
	oldVersion, oldCommit := version, commit
	version, commit = "test-release", "test-source"
	defer func() { version, commit = oldVersion, oldCommit }()
	human, err := runOutput(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	if human != "vcm test-release (test-source)\n" {
		t.Fatalf("unexpected human version: %q", human)
	}
	result, err := runJSON[versionResult](t, "version", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != version || result.Commit != commit {
		t.Fatalf("incorrect build metadata: %+v", result)
	}
}

func TestHelpDocumentsEveryCommandAndOption(t *testing.T) {
	output, err := runErrorOutput(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{
		"validate", "bootstrap", "tree", "sync", "pull", "push                 Validate and publish", "create <slug>", "list", "status [change]", "refresh              Merge", "merge [change]", "drop [change]", "version",
		"--workspace PATH", "--json", "--dry-run", "--force", "--only NAMES", "--except NAMES", "--skip-hooks PHASES", "--skip-git-hooks", "feat: <manifest slug>", "Change selection:", "Examples:",
		"inside its managed", "Refresh is available only from inside",
	} {
		if !strings.Contains(output, phrase) {
			t.Errorf("help is missing %q:\n%s", phrase, output)
		}
	}
	if strings.Contains(strings.ToLower(output), "verification") {
		t.Fatalf("help prescribes a downstream verification workflow:\n%s", output)
	}
}

func TestMergeOverrideFlagsValidationAndDryRun(t *testing.T) {
	root, manifest := cliManagedChange(t)
	result, err := runJSON[dryRunResult](t, "merge", manifest.Tag, "-f", "--skip-hooks", "merge-after,merge-before", "--skip-git-hooks", "--dry-run", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Force || !result.SkipGitHooks || strings.Join(result.SkippedHookPhases, ",") != "merge-before,merge-after" {
		t.Fatalf("merge override dry-run omitted options: %+v", result)
	}
	if !result.DeletesIgnoredContent {
		t.Fatalf("merge dry-run omitted ignored-content deletion: %+v", result)
	}
	human, err := runOutput(t, "merge", manifest.Tag, "--force", "--skip-hooks=merge-before", "--skip-git-hooks", "--dry-run", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Force: true", "Delete ignored content: true", "Skipped hook phases: merge-before", "Git hooks suppressed: true"} {
		if !strings.Contains(human, want) {
			t.Fatalf("human dry-run missing %q:\n%s", want, human)
		}
	}
	for _, command := range []string{"drop"} {
		other, dryRunErr := runJSON[dryRunResult](t, command, manifest.Tag, "--dry-run", "--json", "--workspace", root)
		if dryRunErr != nil {
			t.Fatal(dryRunErr)
		}
		if other.DeletesIgnoredContent {
			t.Fatalf("%s dry-run claims ignored-content deletion: %+v", command, other)
		}
	}
	for name, args := range map[string][]string{
		"blank":           {"merge", manifest.Tag, "--skip-hooks=", "--workspace", root},
		"trailing":        {"merge", manifest.Tag, "--skip-hooks", "merge-before,", "--workspace", root},
		"whitespace":      {"merge", manifest.Tag, "--skip-hooks", "merge-before, merge-after", "--workspace", root},
		"duplicate":       {"merge", manifest.Tag, "--skip-hooks", "merge-before,merge-before", "--workspace", root},
		"unsupported":     {"merge", manifest.Tag, "--skip-hooks", "create-after", "--workspace", root},
		"placement":       {"status", manifest.Tag, "--skip-hooks", "merge-before", "--workspace", root},
		"git-placement":   {"status", manifest.Tag, "--skip-git-hooks", "--workspace", root},
		"force-placement": {"status", manifest.Tag, "-f", "--workspace", root},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runOutput(t, args...); err == nil {
				t.Fatal("invalid merge override accepted")
			}
		})
	}
}

func TestCLIUsesDefaultMergeMessage(t *testing.T) {
	root, manifest := cliManagedChange(t)
	if _, err := runOutput(t, "merge", manifest.Tag, "--workspace", root); err != nil {
		t.Fatal(err)
	}
	engine, err := vcm.Open(root, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := engine.Select(manifest.Tag, root)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MergeMessage != "feat: context-selection" {
		t.Fatalf("default message = %q", stored.MergeMessage)
	}
}

func TestCreateSelectionFlags(t *testing.T) {
	root := cliWorkspace(t)
	result, err := runJSON[dryRunResult](t, "create", "partial", "--only", "api", "--dry-run", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Resources) != 2 || result.Resources[1] != filepath.Join(result.Workspace, "repos/api") {
		t.Fatalf("--only dry-run resources: %+v", result.Resources)
	}
	result, err = runJSON[dryRunResult](t, "create", "root-only", "--except=api", "--dry-run", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Resources) != 1 || result.Resources[0] != result.Workspace {
		t.Fatalf("--except dry-run resources: %+v", result.Resources)
	}
	for name, args := range map[string][]string{
		"unsupported": {"status", "--only", "api", "--workspace", root},
		"both":        {"create", "bad", "--only", "api", "--except", "api", "--workspace", root},
		"empty":       {"create", "bad", "--only=", "--workspace", root},
		"blank":       {"create", "bad", "--only", "api,", "--workspace", root},
		"duplicate":   {"create", "bad", "--only", "api,api", "--workspace", root},
		"root":        {"create", "bad", "--only", "root", "--workspace", root},
		"unknown":     {"create", "bad", "--only", "web", "--workspace", root},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runOutput(t, args...); err == nil {
				t.Fatal("invalid selection flags accepted")
			}
		})
	}
}

func TestMissingCommandShowsHelp(t *testing.T) {
	output, err := runErrorOutput(t)
	if err == nil || err.Error() != "command required" {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "Commands:") || !strings.Contains(output, "create <slug>") {
		t.Fatalf("missing command did not show help:\n%s", output)
	}
}

func TestCommandRejectsUnexpectedArguments(t *testing.T) {
	for _, args := range [][]string{{"version", "extra"}, {"validate", "extra"}, {"list", "extra"}, {"tree", "extra"}, {"sync", "extra"}, {"sync", "--force"}, {"pull", "extra"}, {"pull", "--force"}, {"refresh", "change"}, {"push", "extra"}, {"push", "--force"}, {"push", "--only", "api"}, {"create", "one", "two"}, {"version", "--force"}, {"version", "--unknown"}, {"validate", "--workspace"}} {
		t.Run(args[0]+"/"+args[len(args)-1], func(t *testing.T) {
			if _, err := runOutput(t, args...); err == nil {
				t.Fatal("unexpected arguments accepted")
			}
		})
	}
}

func TestWorkspaceOverrideAndFlagsAfterCommand(t *testing.T) {
	root := cliWorkspace(t)
	for _, args := range [][]string{{"--workspace", root, "--json", "validate"}, {"validate", "--workspace", root, "--json"}, {"validate", "--workspace=" + root, "--json"}} {
		result, err := runJSON[validateResult](t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Valid || result.Workspace != root {
			t.Fatalf("wrong workspace: %+v", result)
		}
	}
}

func TestTreeWorkspaceOverrideSelectsRequestedContext(t *testing.T) {
	root, manifest := cliManagedChange(t)
	base, err := runJSON[treeResult](t, "tree", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if base.Context != "base" || base.Change != "" || base.Workspace != root {
		t.Fatalf("explicit base workspace selected wrong context: %+v", base)
	}
	change, err := runJSON[treeResult](t, "tree", "--json", "--workspace", manifest.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if change.Context != "change" || change.Change != manifest.Tag || change.Workspace != manifest.Workspace {
		t.Fatalf("explicit Change workspace selected wrong context: %+v", change)
	}
}

func TestDryRunCreatesNoResources(t *testing.T) {
	root := cliWorkspace(t)
	for _, args := range [][]string{{"bootstrap", "--dry-run", "--json", "--workspace", root}, {"create", "example-change", "--dry-run", "--json", "--workspace", root}, {"sync", "--dry-run", "--json", "--workspace", root}, {"pull", "--dry-run", "--json", "--workspace", root}, {"push", "--dry-run", "--json", "--workspace", root}} {
		result, err := runJSON[dryRunResult](t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if !result.DryRun {
			t.Fatalf("not a dry-run result: %+v", result)
		}
		if result.DeletesIgnoredContent {
			t.Fatalf("non-merge dry-run claims ignored-content deletion: %+v", result)
		}
		if path := result.Workspace; path != "" && path != root {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("dry run created %s", path)
			}
		}
	}
	result, err := runJSON[dryRunResult](t, "push", "--dry-run", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Resources) != 2 || result.Resources[0] != root || result.Resources[1] != filepath.Join(root, "repos", "api") {
		t.Fatalf("push dry-run resources: %+v", result.Resources)
	}
	for _, path := range []string{filepath.Join(root, "repos"), filepath.Join(root, ".git", "vcm")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("dry run created %s", path)
		}
	}
}

func TestCLIPushReportsResultAndProgress(t *testing.T) {
	root := cliCleanWorkspace(t)
	remote := filepath.Join(filepath.Dir(root), "workspace.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init remote: %s %v", out, err)
	}
	for _, args := range [][]string{{"-C", root, "remote", "add", "origin", remote}, {"-C", root, "push", "origin", "main"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "published.txt"), []byte("published\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", root, "add", "published.txt"}, {"-C", root, "commit", "-m", "feat: publish workspace"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	stdout, stderr, err := runCapturedOutput(t, "push", "--json", "--color=always", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	var result commandResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON stdout %q: %v", stdout, err)
	}
	if result.Command != "push" || !result.Complete || result.Workspace != root {
		t.Fatalf("unexpected push result: %+v", result)
	}
	if strings.Contains(stdout, "\x1b[") || strings.Contains(stderr, "\x1b[") {
		t.Fatalf("JSON mode emitted ANSI: stdout %q, stderr %q", stdout, stderr)
	}
	for _, want := range []string{"[push/root @ " + root + "] preflight complete for trunk main", "[push/root @ " + root + "] pushed trunk main"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("push progress missing %q:\n%s", want, stderr)
		}
	}
	human, err := runOutput(t, "push", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if human != "Push complete.\nWorkspace: "+root+"\n" {
		t.Fatalf("unexpected human push result: %q", human)
	}
}

func TestJSONResultRemainsOnStdoutWhileProgressUsesStderr(t *testing.T) {
	root := cliCleanWorkspace(t)
	remote := filepath.Join(filepath.Dir(root), "workspace.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init remote: %s %v", out, err)
	}
	for _, args := range [][]string{{"-C", root, "remote", "add", "origin", remote}, {"-C", root, "push", "origin", "main"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	stdout, stderr, err := runCapturedOutput(t, "create", "json-progress", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	var result createResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("invalid JSON stdout %q: %v", stdout, err)
	}
	if !strings.Contains(stderr, "[create/root @ "+root+"] synchronized trunk main ") || strings.Contains(stdout, "[create/") {
		t.Fatalf("stdout/stderr were not isolated:\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

func TestStatusRequiresManagedSelection(t *testing.T) {
	root := cliWorkspace(t)
	if _, err := runOutput(t, "status", "--workspace", root); err == nil || err.Error() != "Change argument is required outside a managed Change worktree" {
		t.Fatalf("unexpected omitted-selection error: %v", err)
	}
	if _, err := runOutput(t, "status", "missing-change", "--workspace", root); err == nil {
		t.Fatal("status accepted unknown Change")
	}
}

func TestChangeCommandsUseContextAwareSelection(t *testing.T) {
	root, manifest := cliManagedChange(t)
	nested := filepath.Join(manifest.Workspace, "nested", "deeper")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	originalCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalCWD); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	commands := []struct {
		name string
		args []string
	}{
		{name: "status", args: []string{"status", "--json"}},
		{name: "refresh", args: []string{"refresh", "--dry-run", "--json"}},
		{name: "merge", args: []string{"merge", "--dry-run", "--message", "feat: select contextual change", "--json"}},
		{name: "drop", args: []string{"drop", "--dry-run", "--json"}},
	}
	type selectedResult struct {
		Tag string `json:"tag"`
	}
	assertSelected := func(t *testing.T, command []string, selector string, workspaceOverride bool) {
		t.Helper()
		args := append([]string{}, command...)
		if selector != "" {
			args = append(args, selector)
		}
		if workspaceOverride {
			args = append(args, "--workspace", root)
		}
		result, err := runJSON[selectedResult](t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if result.Tag != manifest.Tag {
			t.Fatalf("selected %q, want %q", result.Tag, manifest.Tag)
		}
	}

	for _, cwd := range []string{manifest.Workspace, nested} {
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
		for _, command := range commands {
			t.Run(command.name+"/implicit/"+filepath.Base(cwd), func(t *testing.T) {
				assertSelected(t, command.args, "", false)
			})
		}
	}

	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	for _, command := range commands {
		t.Run(command.name+"/outside", func(t *testing.T) {
			args := append(append([]string{}, command.args...), "--workspace", root)
			if _, err := runOutput(t, args...); err == nil || err.Error() != "Change argument is required outside a managed Change worktree" {
				t.Fatalf("unexpected omitted-selection error: %v", err)
			}
		})
		if command.name == "refresh" {
			t.Run(command.name+"/explicit-rejected", func(t *testing.T) {
				args := append(append([]string{}, command.args...), manifest.Tag, "--workspace", root)
				if _, err := runOutput(t, args...); err == nil || err.Error() != "refresh takes no arguments" {
					t.Fatalf("unexpected explicit refresh error: %v", err)
				}
			})
			continue
		}
		for _, selector := range []string{manifest.Tag, manifest.Workspace} {
			t.Run(command.name+"/explicit/"+filepath.Base(selector), func(t *testing.T) {
				assertSelected(t, command.args, selector, true)
			})
		}
		t.Run(command.name+"/unknown", func(t *testing.T) {
			args := append(append([]string{}, command.args...), "missing-change", "--workspace", root)
			if _, err := runOutput(t, args...); err == nil || !strings.Contains(err.Error(), `no managed Change matches "missing-change"`) {
				t.Fatalf("unexpected explicit-selection error: %v", err)
			}
		})
	}
}

func TestCheckAndPruneCLIContracts(t *testing.T) {
	root := cliCleanWorkspace(t)
	check, err := runJSON[vcm.AuditReport](t, "check", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Clean || check.Workspace != root || len(check.Repositories) != 1 {
		t.Fatalf("unexpected clean check result: %+v", check)
	}
	if out, err := exec.Command("git", "-C", root, "branch", "unexpected").CombinedOutput(); err != nil {
		t.Fatalf("create branch: %s %v", out, err)
	}

	data, err := runOutput(t, "check", "--json", "--workspace", root)
	if err == nil {
		t.Fatal("dirty check returned success")
	}
	var dirty vcm.AuditReport
	if json.Unmarshal([]byte(data), &dirty) != nil || dirty.Clean || len(dirty.Issues) == 0 {
		t.Fatalf("invalid dirty check payload: %q", data)
	}

	data, err = runOutput(t, "prune", "--dry-run", "--json", "--workspace", root)
	if err == nil {
		t.Fatal("incomplete prune preview returned success")
	}
	var preview vcm.PruneReport
	if json.Unmarshal([]byte(data), &preview) != nil || preview.Complete || len(preview.Actions) != 1 {
		t.Fatalf("invalid prune preview: %q", data)
	}
	if out, err := exec.Command("git", "-C", root, "show-ref", "--verify", "refs/heads/unexpected").CombinedOutput(); err != nil {
		t.Fatalf("dry run removed branch: %s %v", out, err)
	}

	oldTerminal, oldInput := stdinIsTerminal, commandInput
	defer func() { stdinIsTerminal, commandInput = oldTerminal, oldInput }()
	stdinIsTerminal = func() bool { return false }
	if _, err := runOutput(t, "prune", "--workspace", root); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("non-interactive prune accepted: %v", err)
	}
	stdinIsTerminal = func() bool { return true }
	commandInput = strings.NewReader("yes\n")
	pruned, err := runJSON[vcm.PruneReport](t, "prune", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if !pruned.Complete || len(pruned.Actions) != 1 || pruned.Actions[0].Status != vcm.PruneCompleted {
		t.Fatalf("unexpected prune result: %+v", pruned)
	}
}
