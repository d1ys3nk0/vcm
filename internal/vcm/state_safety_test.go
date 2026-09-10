package vcm

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func safetyFixture(t *testing.T) (store, *Manifest) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	common, err := commonDir(root)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{Version: 1, Root: Root{Trunk: "main"}, Children: []Repository{{Name: "api", Path: "repos/api", URL: "https://example.invalid/api.git", Trunk: "main"}}}
	s := store{dir: filepath.Join(common, "vcm"), root: root, config: config}
	tag := newTag("safety")
	change := changeWorkspace(root, tag)
	m := &Manifest{Version: 1, Tag: tag, Slug: "safety", Workspace: change, Origin: root, Config: config, State: "creating", Hooks: map[string]HookState{}}
	m.Repositories = []RepoState{{Repository: Repository{Name: "root", URL: "https://example.invalid/root.git", Trunk: "main"}, Origin: root, Path: change}, {Repository: config.Children[0], Origin: filepath.Join(root, "repos/api"), Path: filepath.Join(change, "repos/api")}}
	return s, m
}

func TestManifestRejectsUnsafeOwnership(t *testing.T) {
	cases := map[string]func(*Manifest){
		"owned without base":             func(m *Manifest) { m.Repositories[1].Owned = true },
		"merged without revisions":       func(m *Manifest) { m.Repositories[1].Merged = true },
		"merge without intent revisions": func(m *Manifest) { m.Repositories[1].Intent = "merge" },
		"missing root":                   func(m *Manifest) { m.Repositories = m.Repositories[1:] },
		"invalid repository identity":    func(m *Manifest) { m.Repositories[1].Repository.Name = "Not Valid" },
		"traversal identity":             func(m *Manifest) { m.Tag = "../outside" },
		"invalid revision":               func(m *Manifest) { m.Repositories[1].Base = "HEAD" },
		"invalid hook key":               func(m *Manifest) { m.Hooks["api/create/not_valid"] = HookState{Status: "complete"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, m := safetyFixture(t)
			mutate(m)
			if err := s.save(m); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
}

func TestManifestRoundTripAndStrictDecoding(t *testing.T) {
	s, m := safetyFixture(t)
	if err := s.save(m); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(s.dir, m.Tag+".json")
	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted["version"] != float64(2) {
		t.Fatalf("state version = %v, want 1", persisted["version"])
	}
	for _, field := range []string{"tag", "slug", "workspace", "origin", "config"} {
		if _, ok := persisted[field]; ok {
			t.Fatalf("manifest persisted derived or configured field %q", field)
		}
	}
	repositories := persisted["repositories"].(map[string]any)
	api := repositories["api"].(map[string]any)
	for _, field := range []string{"repository", "name", "path", "origin", "owned", "merged", "removed"} {
		if _, ok := api[field]; ok {
			t.Fatalf("manifest persisted redundant repository field %q", field)
		}
	}
	got, err := s.load(m.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != m.Tag {
		t.Fatal("wrong Change")
	}
	for name, change := range map[string]func([]byte) []byte{
		"unknown field": func(b []byte) []byte {
			var data map[string]any
			_ = json.Unmarshal(b, &data)
			data["unexpected"] = true
			b, _ = json.Marshal(data)
			return b
		},
		"trailing object": func(b []byte) []byte { return append(b, []byte("{}")...) },
		"full manifest fields": func(b []byte) []byte {
			var data map[string]any
			_ = json.Unmarshal(b, &data)
			data["tag"] = m.Tag
			data["slug"] = m.Slug
			data["workspace"] = m.Workspace
			data["origin"] = m.Origin
			data["config"] = map[string]any{"version": 1}
			b, _ = json.Marshal(data)
			return b
		},
		"invalid version": func(b []byte) []byte {
			var data map[string]any
			_ = json.Unmarshal(b, &data)
			data["version"] = float64(3)
			b, _ = json.Marshal(data)
			return b
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := s.save(m); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filename, change(b), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = s.load(m.Tag); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestManifestDoesNotPersistConfiguration(t *testing.T) {
	s, m := safetyFixture(t)
	m.Config.Root.Hooks = Hooks{HookCreateAfter: {{ID: "current", Shell: "true"}}}
	s.config = m.Config
	if err := s.save(m); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, m.Tag+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err = json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if _, ok := persisted["config"]; ok {
		t.Fatal("manifest persisted configuration")
	}
	loaded, err := s.load(m.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Config.Root.Hooks[HookCreateAfter]) != 1 {
		t.Fatal("manifest did not hydrate current hooks")
	}
}

func TestVersionOneStateUpgradesOnMutation(t *testing.T) {
	s, m := safetyFixture(t)
	if err := s.save(m); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(s.dir, m.Tag+".json")
	b, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err = json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	data["version"] = float64(1)
	delete(data, "merge_message")
	b, _ = json.Marshal(data)
	if err = os.WriteFile(filename, b, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.load(m.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != 2 {
		t.Fatalf("hydrated version = %d", loaded.Version)
	}
	if err = s.save(loaded); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filename)
	_ = json.Unmarshal(b, &data)
	if data["version"] != float64(2) {
		t.Fatalf("written version = %v", data["version"])
	}
}

func TestManifestHydratesOnlyRecordedRepositoriesFromCurrentConfig(t *testing.T) {
	s, m := safetyFixture(t)
	if err := s.save(m); err != nil {
		t.Fatal(err)
	}
	s.config.Children = append(s.config.Children, Repository{Name: "new-child", Path: "repos/new", URL: "https://example.invalid/new.git", Trunk: "main"})
	loaded, err := s.load(m.Tag)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Repositories) != 2 || loaded.Repositories[1].Repository.Name != "api" {
		t.Fatalf("newly configured repository entered existing Change: %+v", loaded.Repositories)
	}
	if loaded.Repositories[1].Repository.Trunk != "main" || loaded.Repositories[1].Path != filepath.Join(loaded.Workspace, "repos/api") {
		t.Fatalf("recorded repository was not hydrated from current configuration: %+v", loaded.Repositories[1])
	}
}

func TestStateRejectsSymlinksAndBroadPermissions(t *testing.T) {
	t.Run("directory symlink", func(t *testing.T) {
		s, m := safetyFixture(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, s.dir); err != nil {
			t.Fatal(err)
		}
		if err := s.save(m); err == nil {
			t.Fatal("symlink state directory accepted")
		}
	})
	t.Run("manifest symlink", func(t *testing.T) {
		s, m := safetyFixture(t)
		if err := s.save(m); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(s.dir, m.Tag+".json")
		target := file + ".old"
		if err := os.Rename(file, target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, file); err != nil {
			t.Fatal(err)
		}
		if _, err := s.load(m.Tag); err == nil {
			t.Fatal("symlink manifest accepted")
		}
	})
	t.Run("lock symlink", func(t *testing.T) {
		s, _ := safetyFixture(t)
		if err := os.Mkdir(s.dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "target"), filepath.Join(s.dir, "mutation.lock")); err != nil {
			t.Fatal(err)
		}
		if unlock, err := s.lock(); err == nil {
			unlock()
			t.Fatal("symlink lock accepted")
		}
	})
	t.Run("manifest permissions", func(t *testing.T) {
		s, m := safetyFixture(t)
		if err := s.save(m); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(s.dir, m.Tag+".json"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.load(m.Tag); err == nil {
			t.Fatal("public manifest accepted")
		}
	})
	t.Run("derived workspace symlink", func(t *testing.T) {
		s, m := safetyFixture(t)
		if err := s.save(m); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), m.Workspace); err != nil {
			t.Fatal(err)
		}
		if _, err := s.load(m.Tag); err == nil {
			t.Fatal("symlink at derived workspace path accepted")
		}
	})
}

func TestConfigurationRejectsSymlinkAliases(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "real"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	config := Config{Version: 1, Root: Root{Trunk: "main"}, Children: []Repository{{Name: "api", Path: "alias/api", URL: "https://example.invalid/api.git", Trunk: "main"}}}
	if err := config.Validate(root); err == nil {
		t.Fatal("in-workspace symlink alias accepted")
	}
}
