package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/d1ys3nk0/vcm/internal/vcm"
)

// integrationResult deliberately describes effects instead of the generated
// settings. Harness settings are implementation details and may evolve.
type integrationResult struct {
	Harness string   `json:"harness"`
	Action  string   `json:"action"`
	Files   []string `json:"files"`
	DryRun  bool     `json:"dry_run"`
}

const integrationCommand = "vcm _integrate-adapter"

func integrationFile(root, harness string) (string, error) {
	switch harness {
	case "codex":
		return filepath.Join(root, ".codex", "hooks.json"), nil
	case "claude":
		return filepath.Join(root, ".claude", "settings.json"), nil
	case "opencode":
		return filepath.Join(root, ".opencode", "plugins", "vcm.ts"), nil
	default:
		return "", fmt.Errorf("unsupported harness %q; expected codex, claude, or opencode", harness)
	}
}

func hookEntry(harness string) map[string]any {
	command := integrationCommand + " " + harness
	hook := map[string]any{"type": "command", "command": command}
	if harness == "codex" {
		hook["timeout"] = 30
		hook["continue"] = false
		hook["systemMessage"] = "VCM workspace setup failed; inspect the hook error and retry vcm create --existing-root ."
	}
	return map[string]any{"matcher": "^(startup|resume)$", "hooks": []any{hook}}
}

func openCodePlugin() string {
	return `// Managed by VCM. Remove with: vcm integrate opencode --remove
export const VCM_INTEGRATION = "vcm-opencode-worktree-ready-v1";

export const plugin = async ({ $ }: { $: { subprocess: (args: string[]) => Promise<unknown> } }) => ({
  event: async ({ event, properties }: { event: { type: string }, properties: { directory?: string } }) => {
    if (event.type !== "worktree.ready") return;
    const directory = properties.directory;
    if (!directory) throw new Error("VCM could not determine the ready worktree directory");
    await $.subprocess(["vcm", "_integrate-adapter", "opencode"]);
  },
});
`
}

func readObject(path string) (map[string]any, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var value map[string]any
	if err := json.Unmarshal(b, &value); err != nil {
		return nil, false, fmt.Errorf("read %s: expected a JSON object: %w", path, err)
	}
	if value == nil {
		return nil, false, fmt.Errorf("read %s: expected a JSON object", path)
	}
	return value, true, nil
}

func equalJSON(a, b any) bool {
	aa, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(aa) == string(bb)
}

func integrateJSON(root, harness string, remove, dry bool) (integrationResult, error) {
	path, err := integrationFile(root, harness)
	if err != nil {
		return integrationResult{}, err
	}
	value, exists, err := readObject(path)
	if err != nil {
		return integrationResult{}, err
	}
	hooks, ok := value["hooks"].(map[string]any)
	if !ok && value["hooks"] != nil {
		return integrationResult{}, fmt.Errorf("%s has a non-object hooks setting", path)
	}
	if hooks == nil {
		hooks = map[string]any{}
	}
	entries, ok := hooks["SessionStart"].([]any)
	if !ok && hooks["SessionStart"] != nil {
		return integrationResult{}, fmt.Errorf("%s has a non-array hooks.SessionStart setting", path)
	}
	wanted := hookEntry(harness)
	found := -1
	for i, entry := range entries {
		if candidate, ok := entry.(map[string]any); ok {
			if hooksValue, ok := candidate["hooks"].([]any); ok && len(hooksValue) == 1 {
				if hook, ok := hooksValue[0].(map[string]any); ok && hook["command"] == integrationCommand+" "+harness {
					if !equalJSON(candidate, wanted) {
						return integrationResult{}, fmt.Errorf("%s contains a modified VCM integration; restore it or remove it manually", path)
					}
					found = i
				}
			}
		}
	}
	action := "installed"
	if remove {
		action = "removed"
		if found < 0 {
			return integrationResult{Harness: harness, Action: "already removed", Files: []string{path}, DryRun: dry}, nil
		}
		entries = append(entries[:found], entries[found+1:]...)
		if len(entries) == 0 {
			delete(hooks, "SessionStart")
		} else {
			hooks["SessionStart"] = entries
		}
		if len(hooks) == 0 {
			delete(value, "hooks")
		} else {
			value["hooks"] = hooks
		}
	} else if found >= 0 {
		return integrationResult{Harness: harness, Action: "already installed", Files: []string{path}, DryRun: dry}, nil
	} else {
		hooks["SessionStart"] = append(entries, wanted)
		value["hooks"] = hooks
	}
	if !dry {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return integrationResult{}, err
		}
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return integrationResult{}, err
		}
		if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
			return integrationResult{}, err
		}
		if remove && !exists {
			return integrationResult{}, fmt.Errorf("cannot remove missing integration")
		}
	}
	return integrationResult{Harness: harness, Action: action, Files: []string{path}, DryRun: dry}, nil
}

func integrateHarness(root, harness string, remove, dry bool) (integrationResult, error) {
	if harness == "opencode" {
		path, err := integrationFile(root, harness)
		if err != nil {
			return integrationResult{}, err
		}
		current, err := os.ReadFile(path)
		if remove {
			if os.IsNotExist(err) {
				return integrationResult{Harness: harness, Action: "already removed", Files: []string{path}, DryRun: dry}, nil
			}
			if err != nil {
				return integrationResult{}, err
			}
			if string(current) != openCodePlugin() {
				return integrationResult{}, fmt.Errorf("%s contains a modified VCM integration; remove it manually", path)
			}
			if !dry {
				if err := os.Remove(path); err != nil {
					return integrationResult{}, err
				}
			}
			return integrationResult{Harness: harness, Action: "removed", Files: []string{path}, DryRun: dry}, nil
		}
		if err == nil {
			if string(current) != openCodePlugin() {
				return integrationResult{}, fmt.Errorf("%s exists and is not the VCM integration", path)
			}
			return integrationResult{Harness: harness, Action: "already installed", Files: []string{path}, DryRun: dry}, nil
		}
		if !os.IsNotExist(err) {
			return integrationResult{}, err
		}
		if !dry {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return integrationResult{}, err
			}
			if err := os.WriteFile(path, []byte(openCodePlugin()), 0644); err != nil {
				return integrationResult{}, err
			}
		}
		return integrationResult{Harness: harness, Action: "installed", Files: []string{path}, DryRun: dry}, nil
	}
	return integrateJSON(root, harness, remove, dry)
}

func eventDirectory(r io.Reader) (string, error) {
	var event any
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&event); err != nil {
		return "", fmt.Errorf("read harness event: %w", err)
	}
	var visit func(any) string
	visit = func(value any) string {
		switch v := value.(type) {
		case map[string]any:
			for _, key := range []string{"cwd", "directory"} {
				if path, ok := v[key].(string); ok && path != "" {
					return path
				}
			}
			for _, child := range v {
				if path := visit(child); path != "" {
					return path
				}
			}
		case []any:
			for _, child := range v {
				if path := visit(child); path != "" {
					return path
				}
			}
		}
		return ""
	}
	if cwd := visit(event); cwd != "" {
		return cwd, nil
	}
	return os.Getwd()
}

func runIntegrationAdapter(harness string) error {
	if harness != "codex" && harness != "claude" && harness != "opencode" {
		return fmt.Errorf("unsupported harness adapter %q", harness)
	}
	cwd, err := eventDirectory(commandInput)
	if err != nil {
		return err
	}
	engine, err := vcm.Open(cwd, io.Discard)
	if err != nil {
		return err
	}
	if filepath.Clean(cwd) == filepath.Clean(engine.Root) {
		return nil
	}
	if _, err := engine.Select(cwd, cwd); err == nil {
		return nil
	}
	return engine.Mutate(func() error {
		_, err := engine.CreateExisting("", cwd, "", "")
		return err
	})
}

func integrationTrustGuidance(harness string) string {
	if harness == "codex" {
		return "Codex project hooks require trust; review and enable them with /hooks."
	}
	return ""
}

func normalizeHarnessOutput(result integrationResult) integrationResult {
	result.Action = strings.TrimSpace(result.Action)
	return result
}
