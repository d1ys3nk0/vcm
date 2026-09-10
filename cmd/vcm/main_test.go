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
)

func runOutput(t *testing.T, args ...string) (map[string]any, error) {
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
		return nil, callErr
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
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

func TestVersionReportsBuildMetadata(t *testing.T) {
	oldVersion, oldCommit := version, commit
	version, commit = "test-release", "test-source"
	defer func() { version, commit = oldVersion, oldCommit }()
	for _, args := range [][]string{{"version"}, {"version", "--json"}} {
		result, err := runOutput(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if result["version"] != version || result["commit"] != commit {
			t.Fatalf("incorrect build metadata: %v", result)
		}
	}
}

func TestHelpDocumentsEveryCommandAndOption(t *testing.T) {
	output, err := runErrorOutput(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{
		"validate", "bootstrap", "sync", "create <slug>", "list", "status [change]", "merge [change]", "drop [change]", "version",
		"--workspace PATH", "--json", "--dry-run", "--force", "Change selection:", "Examples:",
	} {
		if !strings.Contains(output, phrase) {
			t.Errorf("help is missing %q:\n%s", phrase, output)
		}
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
		result, err := runOutput(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if result["valid"] != true || result["workspace"] != root {
			t.Fatalf("wrong workspace: %v", result)
		}
	}
}

func TestDryRunCreatesNoResources(t *testing.T) {
	root := cliWorkspace(t)
	for _, args := range [][]string{{"bootstrap", "--dry-run", "--workspace", root}, {"create", "example-change", "--dry-run", "--workspace", root}, {"sync", "--force", "--dry-run", "--workspace", root}} {
		result, err := runOutput(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if result["dry_run"] != true {
			t.Fatalf("not a dry-run result: %v", result)
		}
		if path, ok := result["workspace"].(string); ok && path != root {
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
