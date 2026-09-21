package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
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

func TestCodexIntegrationUsesSupportedHandlerFields(t *testing.T) {
	root := t.TempDir()
	if _, err := integrateHarness(root, "codex", false, false); err != nil {
		t.Fatal(err)
	}
	path, _ := integrationFile(root, "codex")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	entry := settings["hooks"].(map[string]any)["SessionStart"].([]any)[0].(map[string]any)
	handler := entry["hooks"].([]any)[0].(map[string]any)
	if handler["statusMessage"] == nil || handler["timeout"] == nil {
		t.Fatalf("supported Codex handler metadata missing: %#v", handler)
	}
	for _, outputOnly := range []string{"continue", "stopReason", "systemMessage"} {
		if _, exists := handler[outputOnly]; exists {
			t.Fatalf("Codex output field %q was written into handler config: %#v", outputOnly, handler)
		}
	}
}

func TestJSONRemovalNoOpPreservesBytesAndRemovesDuplicates(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := []byte("{\n    \"unrelated\" : true\n}\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := integrateHarness(root, "claude", true, false)
	if err != nil || result.Action != "already removed" {
		t.Fatalf("remove no-op: %+v %v", result, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("no-op removal rewrote unrelated config: %q %v", got, err)
	}

	wanted := hookEntry("claude")
	settings := map[string]any{"unrelated": true, "hooks": map[string]any{"SessionStart": []any{wanted, map[string]any{"matcher": "x", "hooks": []any{}}, wanted}}}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := integrateHarness(root, "claude", true, false); err != nil {
		t.Fatal(err)
	}
	value, _, err := readObject(path)
	if err != nil {
		t.Fatal(err)
	}
	entries := value["hooks"].(map[string]any)["SessionStart"].([]any)
	if len(entries) != 1 || value["unrelated"] != true {
		t.Fatalf("duplicate removal damaged config: %#v", value)
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

func TestOpenCodePluginDispatchesReadyWorktreeToAdapter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "vcm.mjs")
	if err := os.WriteFile(pluginPath, []byte(openCodePlugin()), 0644); err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(dir, "runner.mjs")
	script := `import { pathToFileURL } from "node:url";
const calls = [];
const shell = (strings, ...values) => { calls.push({ strings, values }); return Promise.resolve(); };
const pluginModule = await import(pathToFileURL(process.argv[2]));
const hooks = await pluginModule.default({ $: shell, worktree: "/tmp/ready-worktree" });
await hooks.event({ event: { type: "session.updated", properties: {} } });
await hooks.event({ event: { type: "worktree.ready", properties: { name: "abc", branch: "main" } } });
if (calls.length !== 1 || calls[0].strings.join("<path>") !== "vcm _integrate-adapter opencode <path>" || calls[0].values[0] !== "/tmp/ready-worktree") process.exit(7);
`
	if err := os.WriteFile(runner, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, runner, pluginPath).CombinedOutput(); err != nil {
		t.Fatalf("generated OpenCode plugin behavior: %s: %v", output, err)
	}
}

func TestCodexAdapterFailureEmitsBlockingHookOutput(t *testing.T) {
	oldInput, oldStdout := commandInput, os.Stdout
	defer func() { commandInput, os.Stdout = oldInput, oldStdout }()
	commandInput = strings.NewReader("not-json")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	err = runIntegrationAdapter("codex", "")
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	data, readErr := io.ReadAll(reader)
	reader.Close()
	if err == nil || readErr != nil {
		t.Fatalf("adapter failure = %v, read = %v", err, readErr)
	}
	var output codexHookOutput
	if jsonErr := json.Unmarshal(data, &output); jsonErr != nil {
		t.Fatalf("invalid Codex hook output %q: %v", data, jsonErr)
	}
	if output.Continue || output.StopReason == "" || !strings.Contains(output.SystemMessage, "vcm create <name> --existing-root <cwd>") {
		t.Fatalf("non-blocking or unactionable Codex hook output: %+v", output)
	}
}

func TestEventDirectoryUsesHarnessCWD(t *testing.T) {
	path, err := eventDirectory(strings.NewReader(`{"session":{"cwd":"/tmp/worktree"}}`))
	if err != nil || path != "/tmp/worktree" {
		t.Fatalf("event directory = %q, %v", path, err)
	}
}
