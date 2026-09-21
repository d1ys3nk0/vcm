package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d1ys3nk0/vcm/internal/vcm"
)

func extendedGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", path}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func TestExtendedPublishPreviewAndJSON(t *testing.T) {
	root, m := cliManagedChange(t)
	remote := extendedGit(t, root, "remote", "get-url", "origin")
	preview, err := runJSON[dryRunResult](t, "publish", m.WorkspaceID, "--dry-run", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Command != "publish" || len(preview.Steps) != 1 || len(preview.Unverified) == 0 {
		t.Fatalf("incomplete preview: %+v", preview)
	}
	if got := extendedGit(t, root, "ls-remote", remote, "refs/heads/"+m.Name); got != "" {
		t.Fatal("preview published")
	}
	result, err := runJSON[publicationResult](t, "publish", m.WorkspaceID, "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkspaceID != m.WorkspaceID || len(result.Repositories) != 1 {
		t.Fatalf("publication JSON: %+v", result)
	}
	row := result.Repositories[0]
	if row.Ref != "refs/heads/"+m.Name || row.SHA != extendedGit(t, m.Workspace, "rev-parse", "HEAD") {
		t.Fatalf("publication revision: %+v", row)
	}
	if got := extendedGit(t, root, "ls-remote", remote, row.Ref); !strings.HasPrefix(got, row.SHA) {
		t.Fatal("reported SHA was not published")
	}
}
func TestExtendedDiffCommittedPatchAndUnavailableJSON(t *testing.T) {
	root, m := cliManagedChange(t)
	file := filepath.Join(m.Workspace, "vcm.yml")
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("# working edit\n"); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := runJSON[vcm.DiffReport](t, "diff", m.WorkspaceID, "--stat", "--patch", "--only=root", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Repositories) != 1 || !strings.Contains(report.Repositories[0].Patch, "working edit") {
		t.Fatalf("missing working patch: %+v", report)
	}
	committed, err := runJSON[vcm.DiffReport](t, "diff", m.WorkspaceID, "--committed", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if !committed.Committed || len(committed.Repositories[0].Files) != 0 {
		t.Fatalf("committed diff included working edit: %+v", committed)
	}
	gitFile := filepath.Join(m.Workspace, ".git")
	if err = os.Rename(gitFile, gitFile+".saved"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(gitFile+".saved", gitFile)
	report, err = runJSON[vcm.DiffReport](t, "diff", m.WorkspaceID, "--json", "--workspace", root)
	if err == nil || len(report.Repositories) != 1 || report.Repositories[0].Available || report.Repositories[0].Error == "" {
		t.Fatalf("partial output lost: %+v, %v", report, err)
	}
}
func TestExtendedKeepCleanupAndRecoveryOptions(t *testing.T) {
	root, m := cliManagedChange(t)
	for _, extra := range [][]string{{"--retry-hook", "root/create-after/hook"}, {"--acknowledge-effects"}} {
		args := append([]string{"recover", m.WorkspaceID, "--adopt-config", "--workspace", root}, extra...)
		if _, err := runOutput(t, args...); err == nil {
			t.Fatalf("conflicting recovery accepted: %v", args)
		}
	}
	adoption, err := runJSON[vcm.ConfigurationAdoption](t, "recover", m.WorkspaceID, "--adopt-config", "--dry-run", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if !adoption.DryRun || adoption.WorkspaceID != m.WorkspaceID {
		t.Fatalf("adoption preview: %+v", adoption)
	}
	merged, err := runJSON[mergeResult](t, "merge", m.WorkspaceID, "--keep", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if merged.State != "integrated" {
		t.Fatalf("keep result: %+v", merged)
	}
	if _, err = os.Stat(m.Workspace); err != nil {
		t.Fatal("keep removed worktree")
	}
	preview, err := runJSON[dryRunResult](t, "cleanup", m.WorkspaceID, "--dry-run", "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Command != "cleanup" {
		t.Fatalf("cleanup preview: %+v", preview)
	}
	if _, err = os.Stat(m.Workspace); err != nil {
		t.Fatal("cleanup preview removed worktree")
	}
	result, err := runJSON[commandResult](t, "cleanup", m.WorkspaceID, "--json", "--workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Command != "cleanup" {
		t.Fatalf("cleanup result: %+v", result)
	}
	if _, err = os.Stat(m.Workspace); !os.IsNotExist(err) {
		t.Fatal("cleanup left worktree")
	}
}

func TestExtendedFailureRecoveryCommands(t *testing.T) {
	root, m := cliManagedChange(t)
	remote := extendedGit(t, root, "remote", "get-url", "origin")
	hook := filepath.Join(remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := runOutput(t, "publish", m.WorkspaceID, "--json", "--workspace", root)
	var failure *operationError
	if !errors.As(err, &failure) {
		t.Fatalf("missing operation error: %v", err)
	}
	if len(failure.Progress) != 1 || failure.Progress[0].Repository != "root" || failure.Progress[0].Publication != "blocked" {
		t.Fatalf("duplicated publication progress: %+v", failure.Progress)
	}
	data, err := json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Progress   []progressEntry `json:"progress"`
		NextAction string          `json:"next_action"`
	}
	if err = json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Progress) != 1 {
		t.Fatal("publication error JSON duplicated progress")
	}
	_, err = runOutput(t, "add", m.WorkspaceID, "--only=missing", "--json", "--workspace", root)
	if !errors.As(err, &failure) || !strings.Contains(failure.NextAction, "--only 'missing'") {
		t.Fatalf("add retry lost selection: %v", err)
	}
	portable := "ssh://example.test/workspace.git"
	extendedGit(t, root, "remote", "set-url", "origin", portable)
	snapshot := vcm.Snapshot{Version: 2, WorkspaceName: m.Name, Repositories: []vcm.SnapshotRepository{{Name: "root", Path: ".", URL: portable, Trunk: "main", Base: extendedGit(t, root, "rev-parse", "HEAD"), Source: strings.Repeat("1", 40)}}}
	file := filepath.Join(t.TempDir(), "snapshot with spaces.json")
	data, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", "false")
	t.Setenv("GIT_SSH_VARIANT", "ssh")
	_, err = runOutput(t, "restore", file, "--name=copy", "--fetch", "--json", "--workspace", root)
	if !errors.As(err, &failure) {
		t.Fatalf("restore failure missing recovery: %v", err)
	}
	for _, required := range []string{"restore " + shellQuote(file), "--name 'copy'", "--fetch"} {
		if !strings.Contains(failure.NextAction, required) {
			t.Fatalf("restore retry lost %q: %s", required, failure.NextAction)
		}
	}
}
