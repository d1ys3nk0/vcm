package vcm

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConfigurationDecodeRejectsMalformedInput(t *testing.T) {
	cases := map[string]string{
		"unknown root field":   "version: 1\ntrunk: main\nrepositories: []\nextra: true\n",
		"unknown nested field": "version: 1\ntrunk: main\nrepositories:\n- name: api\n  path: api\n  url: /remote\n  trunk: main\n  extra: true\n",
		"unknown hook field":   "version: 1\ntrunk: main\nrepositories: []\nhooks:\n  create:\n  - id: prepare\n    command: 'true'\n    extra: true\n",
		"duplicate YAML key":   "version: 1\ntrunk: main\ntrunk: develop\nrepositories: []\n",
		"multiple documents":   "version: 1\ntrunk: main\nrepositories: []\n---\nversion: 1\n",
		"missing repositories": "version: 1\ntrunk: main\n",
		"null repositories":    "version: 1\ntrunk: main\nrepositories: null\n",
		"missing version":      "trunk: main\nrepositories: []\n",
		"missing trunk":        "version: 1\nrepositories: []\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "workspace.yml"), []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readConfig(root); err == nil {
				t.Fatal("malformed configuration accepted")
			}
		})
	}
	t.Run("empty explicit list", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "workspace.yml"), []byte("version: 1\ntrunk: main\nrepositories: []\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readConfig(root); err != nil {
			t.Fatal(err)
		}
	})
}

func TestConfigurationRejectsInvalidContracts(t *testing.T) {
	cases := map[string]func(*Config){
		"duplicate identity":   func(c *Config) { c.Repositories[1].Name = c.Repositories[0].Name },
		"reserved identity":    func(c *Config) { c.Repositories[0].Name = "workspace" },
		"duplicate path":       func(c *Config) { c.Repositories[1].Path = c.Repositories[0].Path },
		"overlapping paths":    func(c *Config) { c.Repositories[1].Path = c.Repositories[0].Path + "/nested" },
		"escaping path":        func(c *Config) { c.Repositories[0].Path = "../outside" },
		"absolute path":        func(c *Config) { c.Repositories[0].Path = "/outside" },
		"git path":             func(c *Config) { c.Repositories[0].Path = ".git/repo" },
		"noncanonical path":    func(c *Config) { c.Repositories[0].Path = "repos/../api" },
		"missing dependency":   func(c *Config) { c.Repositories[0].DependsOn = []string{"absent"} },
		"duplicate dependency": func(c *Config) { c.Repositories[1].DependsOn = []string{"api", "api"} },
		"cycle": func(c *Config) {
			c.Repositories[0].DependsOn = []string{"web"}
			c.Repositories[1].DependsOn = []string{"api"}
		},
		"self dependency":          func(c *Config) { c.Repositories[0].DependsOn = []string{"api"} },
		"invalid root phase":       func(c *Config) { c.Hooks = Hooks{"merge": {{ID: "prepare", Command: "true"}}} },
		"invalid repository phase": func(c *Config) { c.Repositories[0].Hooks = Hooks{"post-merge": {{ID: "prepare", Command: "true"}}} },
		"duplicate hook identity": func(c *Config) {
			c.Hooks = Hooks{"create": {{ID: "prepare", Command: "true"}, {ID: "prepare", Command: "true"}}}
		},
		"blank command": func(c *Config) { c.Hooks = Hooks{"create": {{ID: "prepare", Command: " "}}} },
	}
	for _, branch := range []string{"-option", "feature..x", "feature.lock", "refs//x", "bad name", "feature@{x}", ".hidden", "feature/"} {
		b := branch
		cases["branch "+branch] = func(c *Config) { c.Repositories[0].Trunk = b }
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := Config{Version: 1, Trunk: "main", Repositories: []Repository{{Name: "api", Path: "repos/api", URL: "/remotes/api", Trunk: "main"}, {Name: "web", Path: "repos/web", URL: "/remotes/web", Trunk: "main"}}}
			mutate(&c)
			if err := c.Validate(t.TempDir()); err == nil {
				t.Fatal("invalid contract accepted")
			}
		})
	}
}

func TestDependenciesUseDeclarationOrderForReadyRepositories(t *testing.T) {
	c := Config{Repositories: []Repository{{Name: "web", DependsOn: []string{"api"}}, {Name: "docs"}, {Name: "api"}, {Name: "worker", DependsOn: []string{"api"}}}}
	ordered, err := c.Order()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range ordered {
		names = append(names, r.Name)
	}
	if !reflect.DeepEqual(names, []string{"docs", "api", "web", "worker"}) {
		t.Fatalf("dependency order: %v", names)
	}
}
