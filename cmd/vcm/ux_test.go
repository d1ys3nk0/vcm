package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitPreservesExistingConfigurationAndSupportsDetachedHead(t *testing.T) {
	root := cliCleanWorkspace(t)
	filename := filepath.Join(root, "vcm.yml")
	original, _ := os.ReadFile(filename)
	if _, err := runOutput(t, "init", "--workspace", root); err == nil {
		t.Fatal("overwrote configuration")
	}
	after, _ := os.ReadFile(filename)
	if string(after) != string(original) {
		t.Fatal("existing config changed")
	}
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "checkout", "--detach").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := runOutput(t, "init", "--workspace", root); err == nil {
		t.Fatal("accepted detached HEAD without trunk")
	}
	if _, err := runOutput(t, "init", "--trunk", "main", "--dry-run", "--workspace", root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		t.Fatal("preview wrote config")
	}
	if _, err := runOutput(t, "init", "--trunk", "main", "--workspace", root); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filename)
	if err != nil || !strings.Contains(string(raw), "children: []") {
		t.Fatalf("invalid config %s %v", raw, err)
	}
}
func TestStatusAndListExposeUnknownInspectionAndHistory(t *testing.T) {
	root, m := cliManagedChange(t)
	// A missing selected checkout is a report plus nonzero, with unknown counts.
	if err := os.Rename(m.Workspace, m.Workspace+".moved"); err != nil {
		t.Fatal(err)
	}
	raw, err := runOutput(t, "status", m.Slug, "--json", "--workspace", root)
	if err == nil {
		t.Fatal("missing checkout reported success")
	}
	var status inspectionResult
	if err = json.Unmarshal([]byte(raw), &status); err != nil {
		t.Fatal(err)
	}
	if status.Repositories[0].Clean != nil || status.Repositories[0].Ahead != nil {
		t.Fatal("unknown facts reported as zero or clean")
	}
	raw, err = runOutput(t, "list", "--json", "--workspace", root)
	if err == nil {
		t.Fatal("missing checkout list reported success")
	}
	var list overviewResult
	if err = json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	if list.Changes[0].Dirty != nil {
		t.Fatal("unknown dirty count represented as zero")
	}
	if err = os.Rename(m.Workspace+".moved", m.Workspace); err != nil {
		t.Fatal(err)
	}
	if _, err = runOutput(t, "merge", m.Slug, "--workspace", root, "--no-cd"); err != nil {
		t.Fatal(err)
	}
	active, err := runJSON[overviewResult](t, "list", "--json", "--workspace", root)
	if err != nil || len(active.Changes) != 0 {
		t.Fatalf("completed Change in active list: %+v %v", active, err)
	}
	all, err := runJSON[overviewResult](t, "list", "--all", "--json", "--workspace", root)
	if err != nil || len(all.Changes) != 1 || all.Changes[0].Operation != "merged" || all.Changes[0].Dirty != nil {
		t.Fatalf("history: %+v %v", all, err)
	}
	if _, err = runOutput(t, "path", m.Tag, "--workspace", root); err == nil {
		t.Fatal("navigation accepted removed checkout")
	}
}
func TestShellNavigationPreservesPathsAndExitCodes(t *testing.T) {
	binaryDir := t.TempDir()
	binary := filepath.Join(binaryDir, "vcm")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", output, err)
	}
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skip("shell unavailable")
			}
			root := cliCleanWorkspace(t)
			newRoot := root + " space ' dollar$"
			if err := os.Rename(root, newRoot); err != nil {
				t.Fatal(err)
			}
			root = newRoot
			for _, args := range [][]string{{"remote", "add", "origin", "/unavailable-shell-test-remote"}} {
				if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("%s %v", out, err)
				}
			}
			config, err := os.ReadFile(filepath.Join(root, "vcm.yml"))
			if err != nil {
				t.Fatal(err)
			}
			config = []byte(strings.Replace(string(config), "  trunk: main", "  trunk: main\n  hooks:\n    merge-after:\n      - id: final\n        shell: test \"${VCM_FAIL_FINALIZE:-0}\" = 0", 1))
			if err = os.WriteFile(filepath.Join(root, "vcm.yml"), config, 0600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"add", "vcm.yml"}, {"commit", "-m", "chore: configure finalization"}} {
				if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("%s %v", out, err)
				}
			}
			script := `set -eu
 eval "$(command vcm shell init "$VCM_TEST_SHELL")"
 vcm create shell-navigation --workspace "$VCM_TEST_ROOT"
 selected=$PWD
 test "$selected" != "$VCM_TEST_ROOT"
 vcm switch --base
 test "$PWD" = "$VCM_TEST_ROOT"
 vcm switch shell-navigation --no-cd
 test "$PWD" = "$VCM_TEST_ROOT"
 vcm switch shell-navigation
 test "$PWD" = "$selected"
 vcm merge --no-cd
 cd "$VCM_TEST_ROOT"
 vcm create shell-final
 vcm merge
 test "$PWD" = "$VCM_TEST_ROOT"
 if vcm unknown; then exit 9; fi
 test "$PWD" = "$VCM_TEST_ROOT"
 export VCM_FAIL_FINALIZE=1
 vcm create failed-finalization
 if vcm merge; then exit 9; else vcm_merge_exit=$?; fi
 test "$vcm_merge_exit" -eq 1
 test "$PWD" = "$VCM_TEST_ROOT"
 `
			cmd := exec.Command(shell, "-c", script)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+binaryDir+string(os.PathListSeparator)+os.Getenv("PATH"), "VCM_TEST_ROOT="+root, "VCM_TEST_SHELL="+shell)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s shell: %s %v", shell, output, err)
			}
		})
	}
}
func TestNavigationFileRejectedWithoutTruncation(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "unsafe")
	if err := os.WriteFile(filename, []byte("preserve"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VCM_CD_FILE", filename)
	if err := navigate("/tmp"); err == nil {
		t.Fatal("accepted unsafe navigation file")
	}
	raw, _ := os.ReadFile(filename)
	if string(raw) != "preserve" {
		t.Fatal("rejected navigation file was truncated")
	}
}

func TestInitRejectsLinkedWorktree(t *testing.T) {
	root, m := cliManagedChange(t)
	path := filepath.Join(m.Workspace, "vcm.yml")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := runOutput(t, "init", "--workspace", m.Workspace); err == nil {
		t.Fatal("init accepted linked worktree")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("init wrote configuration")
	}
	_ = root
}

func TestMissingConfiguredSelectedRepositoryIsReported(t *testing.T) {
	root, m := cliManagedChange(t)
	// Record a selected child whose configuration was subsequently removed.
	filename := filepath.Join(root, ".git", "vcm", m.Tag+".json")
	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state["repositories"].(map[string]any)["retired"] = map[string]any{}
	raw, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"status", "list"} {
		args := []string{command, "--json", "--workspace", root}
		if command == "status" {
			args = append(args, m.Tag)
		}
		output, err := runOutput(t, args...)
		if err == nil || !strings.Contains(output, "absent from current configuration") {
			t.Fatalf("%s concealed missing selected configuration: %s %v", command, output, err)
		}
	}
}

func TestPruneReportsUnreadableStateWithoutMutation(t *testing.T) {
	root, m := cliManagedChange(t)
	filename := filepath.Join(root, ".git", "vcm", m.Tag+".json")
	if err := os.WriteFile(filename, []byte("{\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := runOutput(t, "prune", "--json", "--workspace", root)
	if err == nil || !strings.Contains(result, "inspection_error") {
		t.Fatalf("missing full blocked report: %s %v", result, err)
	}
	if _, err := os.Stat(m.Workspace); err != nil {
		t.Fatal("prune changed owned resources")
	}
	raw, _ := os.ReadFile(filename)
	if string(raw) != "{\n" {
		t.Fatal("prune changed invalid evidence")
	}
}
