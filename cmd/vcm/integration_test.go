package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONHarnessIntegrationPreservesSettingsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"SessionStart":[{"matcher":"^startup$","hooks":[{"type":"command","command":"existing"}]}]}}`), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := integrateHarness(root, "codex", false, false)
	if err != nil || result.Action != "installed" {
		t.Fatalf("install: %+v %v", result, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"command": "existing"`) || !strings.Contains(string(data), `vcm _integrate-adapter codex`) {
		t.Fatalf("settings were not merged: %s %v", data, err)
	}
	result, err = integrateHarness(root, "codex", false, false)
	if err != nil || result.Action != "already installed" {
		t.Fatalf("idempotent install: %+v %v", result, err)
	}
	result, err = integrateHarness(root, "codex", true, true)
	if err != nil || result.Action != "removed" || !result.DryRun {
		t.Fatalf("dry removal: %+v %v", result, err)
	}
	result, err = integrateHarness(root, "codex", true, false)
	if err != nil || result.Action != "removed" {
		t.Fatalf("removal: %+v %v", result, err)
	}
	data, err = os.ReadFile(path)
	if err != nil || strings.Contains(string(data), `vcm _integrate-adapter codex`) || !strings.Contains(string(data), `"command": "existing"`) {
		t.Fatalf("removal damaged settings: %s %v", data, err)
	}
}

func TestOpenCodeIntegrationRejectsModifiedPlugin(t *testing.T) {
	root := t.TempDir()
	result, err := integrateHarness(root, "opencode", false, true)
	if err != nil || result.Action != "installed" || !result.DryRun {
		t.Fatalf("dry install: %+v %v", result, err)
	}
	if _, err = integrateHarness(root, "opencode", false, false); err != nil {
		t.Fatal(err)
	}
	path, err := integrationFile(root, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = integrateHarness(root, "opencode", true, false); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("modified plugin removal error = %v", err)
	}
}

func TestEventDirectoryUsesHarnessCWD(t *testing.T) {
	path, err := eventDirectory(strings.NewReader(`{"session":{"cwd":"/tmp/worktree"}}`))
	if err != nil || path != "/tmp/worktree" {
		t.Fatalf("event directory = %q, %v", path, err)
	}
}
