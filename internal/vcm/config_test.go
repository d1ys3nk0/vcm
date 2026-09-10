package vcm

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConfigurationDecodeRejectsMalformedInput(t *testing.T) {
	cases := map[string]string{
		"unknown root field":    "version: 1\nroot:\n  trunk: main\nchildren: []\nextra: true\n",
		"unknown child field":   "version: 1\nroot:\n  trunk: main\nchildren:\n- name: api\n  path: api\n  url: /remote\n  trunk: main\n  extra: true\n",
		"unknown hook field":    "version: 1\nroot:\n  trunk: main\n  hooks:\n    create:\n    - id: prepare\n      shell: 'true'\n      extra: true\nchildren: []\n",
		"legacy command":        "version: 1\nroot:\n  trunk: main\n  hooks:\n    create:\n    - id: prepare\n      command: 'true'\nchildren: []\n",
		"legacy shape":          "version: 1\ntrunk: main\nrepositories: []\n",
		"duplicate YAML key":    "version: 1\nroot:\n  trunk: main\n  trunk: develop\nchildren: []\n",
		"multiple documents":    "version: 1\nroot:\n  trunk: main\nchildren: []\n---\nversion: 1\n",
		"missing children":      "version: 1\nroot:\n  trunk: main\n",
		"null children":         "version: 1\nroot:\n  trunk: main\nchildren: null\n",
		"missing version":       "root:\n  trunk: main\nchildren: []\n",
		"missing root":          "version: 1\nchildren: []\n",
		"blank shell runner":    "version: 1\nrunners:\n  shell: '  '\nroot:\n  trunk: main\nchildren: []\n",
		"blank python runner":   "version: 1\nrunners:\n  python: ''\nroot:\n  trunk: main\nchildren: []\n",
		"null runners":          "version: 1\nrunners: null\nroot:\n  trunk: main\nchildren: []\n",
		"null shell runner":     "version: 1\nrunners:\n  shell: null\nroot:\n  trunk: main\nchildren: []\n",
		"sequence shell runner": "version: 1\nrunners:\n  shell: [bash, -x]\nroot:\n  trunk: main\nchildren: []\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "vcm.yml"), []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readConfig(root); err == nil {
				t.Fatal("malformed configuration accepted")
			}
		})
	}
}

func TestConfigurationRunnerDefaultsAndOverrides(t *testing.T) {
	for name, input := range map[string]string{
		"defaults":  "version: 1\nroot:\n  trunk: main\nchildren: []\n",
		"overrides": "version: 1\nrunners:\n  shell: /opt/bin/bash\n  python: /opt/bin/python3\nroot:\n  trunk: main\nchildren: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "vcm.yml"), []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			config, err := readConfig(root)
			if err != nil {
				t.Fatal(err)
			}
			want := Runners{Shell: "bash", Python: "python"}
			if name == "overrides" {
				want = Runners{Shell: "/opt/bin/bash", Python: "/opt/bin/python3"}
			}
			if config.Runners != want {
				t.Fatalf("runners: %+v, want %+v", config.Runners, want)
			}
		})
	}
}

func TestConfigurationUsesOnlyVCMFilename(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "workspace.yml"), []byte("version: 1\nroot:\n  trunk: main\nchildren: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(root); !os.IsNotExist(err) {
		t.Fatalf("legacy configuration was read: %v", err)
	}
}

func TestConfigurationRejectsInvalidContracts(t *testing.T) {
	cases := map[string]func(*Config){
		"duplicate identity":   func(c *Config) { c.Children[1].Name = c.Children[0].Name },
		"reserved identity":    func(c *Config) { c.Children[0].Name = "root" },
		"duplicate path":       func(c *Config) { c.Children[1].Path = c.Children[0].Path },
		"overlapping paths":    func(c *Config) { c.Children[1].Path = c.Children[0].Path + "/nested" },
		"escaping path":        func(c *Config) { c.Children[0].Path = "../outside" },
		"absolute path":        func(c *Config) { c.Children[0].Path = "/outside" },
		"git path":             func(c *Config) { c.Children[0].Path = ".git/repo" },
		"noncanonical path":    func(c *Config) { c.Children[0].Path = "repos/../api" },
		"missing dependency":   func(c *Config) { c.Children[0].DependsOn = []string{"absent"} },
		"duplicate dependency": func(c *Config) { c.Children[1].DependsOn = []string{"api", "api"} },
		"cycle": func(c *Config) {
			c.Children[0].DependsOn = []string{"web"}
			c.Children[1].DependsOn = []string{"api"}
		},
		"self dependency":     func(c *Config) { c.Children[0].DependsOn = []string{"api"} },
		"invalid root phase":  func(c *Config) { c.Root.Hooks = Hooks{"merge": {{ID: "prepare", Shell: "true"}}} },
		"invalid child phase": func(c *Config) { c.Children[0].Hooks = Hooks{"post-merge": {{ID: "prepare", Shell: "true"}}} },
		"duplicate hook identity": func(c *Config) {
			c.Root.Hooks = Hooks{HookCreateBefore: {{ID: "prepare", Shell: "true"}, {ID: "prepare", Python: "pass"}}}
		},
		"missing body": func(c *Config) { c.Root.Hooks = Hooks{HookCreateBefore: {{ID: "prepare"}}} },
		"blank shell":  func(c *Config) { c.Root.Hooks = Hooks{HookCreateBefore: {{ID: "prepare", Shell: " "}}} },
		"blank python": func(c *Config) { c.Root.Hooks = Hooks{HookCreateBefore: {{ID: "prepare", Python: " "}}} },
		"two bodies": func(c *Config) {
			c.Root.Hooks = Hooks{HookCreateBefore: {{ID: "prepare", Shell: "true", Python: "pass"}}}
		},
	}
	for _, branch := range []string{"-option", "feature..x", "feature.lock", "refs//x", "bad name", "feature@{x}", ".hidden", "feature/"} {
		b := branch
		cases["branch "+branch] = func(c *Config) { c.Children[0].Trunk = b }
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := Config{Version: 1, Root: Root{Trunk: "main"}, Children: []Repository{{Name: "api", Path: "repos/api", URL: "/remotes/api", Trunk: "main"}, {Name: "web", Path: "repos/web", URL: "/remotes/web", Trunk: "main"}}}
			mutate(&c)
			if err := c.Validate(t.TempDir()); err == nil {
				t.Fatal("invalid contract accepted")
			}
		})
	}
}

func TestDependenciesUseDeclarationOrderForReadyChildren(t *testing.T) {
	c := Config{Children: []Repository{{Name: "web", DependsOn: []string{"api"}}, {Name: "docs"}, {Name: "api"}, {Name: "worker", DependsOn: []string{"api"}}}}
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
