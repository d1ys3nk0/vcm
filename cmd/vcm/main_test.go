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
		"validate", "bootstrap", "sync", "create <slug>", "list", "status [change]", "merge [change]", "drop [change]", "version",
		"--workspace PATH", "--json", "--dry-run", "--force", "--only NAMES", "--except NAMES", "Change selection:", "Examples:",
	} {
		if !strings.Contains(output, phrase) {
			t.Errorf("help is missing %q:\n%s", phrase, output)
		}
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
	for _, args := range [][]string{{"version", "extra"}, {"validate", "extra"}, {"list", "extra"}, {"sync", "extra"}, {"create", "one", "two"}, {"version", "--force"}, {"version", "--unknown"}, {"validate", "--workspace"}} {
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

func TestDryRunCreatesNoResources(t *testing.T) {
	root := cliWorkspace(t)
	for _, args := range [][]string{{"bootstrap", "--dry-run", "--json", "--workspace", root}, {"create", "example-change", "--dry-run", "--json", "--workspace", root}, {"sync", "--force", "--dry-run", "--json", "--workspace", root}} {
		result, err := runJSON[dryRunResult](t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if !result.DryRun {
			t.Fatalf("not a dry-run result: %+v", result)
		}
		if path := result.Workspace; path != "" && path != root {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("dry run created %s", path)
			}
		}
	}
	for _, path := range []string{filepath.Join(root, "repos"), filepath.Join(root, ".git", "vcm")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("dry run created %s", path)
		}
	}
}

func TestStatusRequiresManagedSelection(t *testing.T) {
	root := cliWorkspace(t)
	if _, err := runOutput(t, "status", "--workspace", root); err == nil {
		t.Fatal("status accepted unmanaged current directory")
	}
	if _, err := runOutput(t, "status", "missing-change", "--workspace", root); err == nil {
		t.Fatal("status accepted unknown Change")
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
