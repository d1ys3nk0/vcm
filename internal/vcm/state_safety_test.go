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
	s := store{filepath.Join(common, "vcm")}
	config := Config{Version: 1, Root: Root{Trunk: "main"}, Children: []Repository{{Name: "api", Path: "repos/api", URL: "https://example.invalid/api.git", Trunk: "main"}}}
	tag := newTag("safety")
	change := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-"+tag)
	m := &Manifest{Version: 1, Tag: tag, Slug: "safety", Workspace: change, Origin: root, Config: config, State: "creating", Hooks: map[string]HookState{}}
	m.Repositories = []RepoState{{Repository: Repository{Name: "root", URL: "https://example.invalid/root.git", Trunk: "main"}, Origin: root, Path: change}, {Repository: config.Children[0], Origin: filepath.Join(root, "repos/api"), Path: filepath.Join(change, "repos/api")}}
	return s, m
}

func TestManifestRejectsUnsafeOwnership(t *testing.T) {
	cases := map[string]func(*Manifest){
		"owned without base":             func(m *Manifest) { m.Repositories[1].Owned = true },
		"merged without revisions":       func(m *Manifest) { m.Repositories[1].Merged = true },
		"merge without intent revisions": func(m *Manifest) { m.Repositories[1].Intent = "merge" },
		"workspace":                      func(m *Manifest) { m.Workspace = filepath.Dir(m.Origin) },
		"origin":                         func(m *Manifest) { m.Origin = filepath.Dir(m.Origin) },
		"repository path":                func(m *Manifest) { m.Repositories[1].Path = m.Origin },
		"repository origin":              func(m *Manifest) { m.Repositories[1].Origin = m.Origin },
		"repository metadata":            func(m *Manifest) { m.Repositories[1].Repository.Trunk = "other" },
		"missing repository":             func(m *Manifest) { m.Repositories = m.Repositories[:1] },
		"traversal identity":             func(m *Manifest) { m.Tag = "../outside" },
		"slug mismatch":                  func(m *Manifest) { m.Slug = "different" },
		"invalid revision":               func(m *Manifest) { m.Repositories[1].Base = "HEAD" },
		"unknown hook":                   func(m *Manifest) { m.Hooks["api/create/unknown"] = HookState{Status: "complete"} },
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
	var persisted Manifest
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Config.Runners != (Runners{Shell: "bash", Python: "python"}) {
		t.Fatalf("manifest omitted runner defaults: %+v", persisted.Config.Runners)
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
		"legacy hook command": func(b []byte) []byte {
			var data map[string]any
			_ = json.Unmarshal(b, &data)
			config := data["config"].(map[string]any)
			root := config["root"].(map[string]any)
			root["hooks"] = map[string]any{"create": []any{map[string]any{"id": "legacy", "command": "true"}}}
			b, _ = json.Marshal(data)
			return b
		},
		"blank runner": func(b []byte) []byte {
			var data map[string]any
			_ = json.Unmarshal(b, &data)
			config := data["config"].(map[string]any)
			config["runners"].(map[string]any)["python"] = ""
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
	t.Run("workspace symlink", func(t *testing.T) {
		s, m := safetyFixture(t)
		if err := os.Symlink(t.TempDir(), m.Workspace); err != nil {
			t.Fatal(err)
		}
		if err := s.save(m); err == nil {
			t.Fatal("symlink workspace accepted")
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
