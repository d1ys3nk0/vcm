package vcm

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Hook struct {
	ID     string `yaml:"id" json:"id"`
	Shell  string `yaml:"shell,omitempty" json:"shell,omitempty"`
	Python string `yaml:"python,omitempty" json:"python,omitempty"`
}
type Hooks map[string][]Hook
type Runners struct {
	Shell  string `yaml:"shell,omitempty" json:"shell"`
	Python string `yaml:"python,omitempty" json:"python"`
}
type Root struct {
	Trunk string `yaml:"trunk" json:"trunk"`
	Hooks Hooks  `yaml:"hooks,omitempty" json:"hooks,omitempty"`
}
type Repository struct {
	Name      string   `yaml:"name" json:"name"`
	Path      string   `yaml:"path" json:"path"`
	URL       string   `yaml:"url" json:"url"`
	Trunk     string   `yaml:"trunk" json:"trunk"`
	DependsOn []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	Hooks     Hooks    `yaml:"hooks,omitempty" json:"hooks,omitempty"`
}
type Config struct {
	Version  int          `yaml:"version" json:"version"`
	Runners  Runners      `yaml:"runners,omitempty" json:"runners"`
	Root     Root         `yaml:"root" json:"root"`
	Children []Repository `yaml:"children" json:"children"`
}

func (c *Config) applyDefaults() {
	if c.Runners.Shell == "" {
		c.Runners.Shell = "bash"
	}
	if c.Runners.Python == "" {
		c.Runners.Python = "python"
	}
}

var identity = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func validBranch(s string) bool {
	if s == "" || s == "@" || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") || strings.Contains(s, "@{") || strings.Contains(s, "//") {
		return false
	}
	for _, p := range strings.Split(s, "/") {
		if strings.HasPrefix(p, ".") || strings.HasSuffix(p, ".lock") {
			return false
		}
	}
	for _, r := range s {
		if r <= 32 || r == 127 || strings.ContainsRune(`~^:?*[\`, r) {
			return false
		}
	}
	return true
}
func readConfig(root string) (Config, error) {
	var c Config
	b, e := os.ReadFile(filepath.Join(root, "vcm.yml"))
	if e != nil {
		return c, e
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		return c, fmt.Errorf("vcm.yml must contain one document")
	}
	if e = validateExplicitRunners(b); e != nil {
		return c, e
	}
	c.applyDefaults()
	return c, c.Validate(root)
}

func validateExplicitRunners(b []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(b, &document); err != nil || len(document.Content) == 0 {
		return err
	}
	root := document.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "runners" {
			continue
		}
		runners := root.Content[i+1]
		if runners.Kind != yaml.MappingNode {
			return fmt.Errorf("runners must be an object")
		}
		for j := 0; j+1 < len(runners.Content); j += 2 {
			if (runners.Content[j].Value == "shell" || runners.Content[j].Value == "python") && (runners.Content[j+1].Tag == "!!null" || strings.TrimSpace(runners.Content[j+1].Value) == "") {
				return fmt.Errorf("runner executable must not be blank")
			}
		}
	}
	return nil
}
func checkHooks(h Hooks, root bool) error {
	for phase, entries := range h {
		if phase != "create" && phase != "drop" && !(root && (phase == "pre-merge" || phase == "post-merge")) && !(!root && phase == "merge") {
			return fmt.Errorf("unsupported hook phase %q", phase)
		}
		ids := map[string]bool{}
		for _, x := range entries {
			bodies := 0
			if strings.TrimSpace(x.Shell) != "" {
				bodies++
			}
			if strings.TrimSpace(x.Python) != "" {
				bodies++
			}
			if !identity.MatchString(x.ID) || bodies != 1 || ids[x.ID] {
				return fmt.Errorf("invalid or duplicate hook %q", x.ID)
			}
			ids[x.ID] = true
		}
	}
	return nil
}
func safePath(root, rel string) error {
	if rel == "" || filepath.IsAbs(rel) || filepath.Clean(rel) != rel || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("invalid repository path %q", rel)
	}
	for _, p := range strings.Split(filepath.ToSlash(rel), "/") {
		if p == ".git" {
			return fmt.Errorf("repository path cannot contain .git")
		}
	}
	current := root
	for _, p := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, p)
		info, e := os.Lstat(current)
		if os.IsNotExist(e) {
			break
		} else if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("repository path %q contains a symlink", rel)
		}
	}
	return nil
}
func (c Config) Validate(root string) error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported configuration version %d", c.Version)
	}
	if c.Children == nil {
		return fmt.Errorf("children must be an explicit list; use [] for a root-only configuration")
	}
	if !validBranch(c.Root.Trunk) {
		return fmt.Errorf("invalid root trunk %q", c.Root.Trunk)
	}
	if strings.TrimSpace(c.Runners.Shell) == "" && c.Runners.Shell != "" || strings.TrimSpace(c.Runners.Python) == "" && c.Runners.Python != "" {
		return fmt.Errorf("runner executable must not be blank")
	}
	if e := checkHooks(c.Root.Hooks, true); e != nil {
		return e
	}
	names := map[string]bool{"root": true}
	paths := []string{}
	for _, r := range c.Children {
		if !identity.MatchString(r.Name) || names[r.Name] {
			return fmt.Errorf("duplicate or invalid repository name %q", r.Name)
		}
		names[r.Name] = true
		if strings.TrimSpace(r.URL) == "" || strings.HasPrefix(r.URL, "-") || !validBranch(r.Trunk) {
			return fmt.Errorf("repository %s: invalid url or trunk", r.Name)
		}
		if e := safePath(root, r.Path); e != nil {
			return e
		}
		for _, p := range paths {
			if p == r.Path || strings.HasPrefix(p, r.Path+"/") || strings.HasPrefix(r.Path, p+"/") {
				return fmt.Errorf("overlapping repository paths %q and %q", p, r.Path)
			}
		}
		paths = append(paths, r.Path)
		if e := checkHooks(r.Hooks, false); e != nil {
			return fmt.Errorf("repository %s: %w", r.Name, e)
		}
	}
	_, e := c.Order()
	return e
}
func (c Config) Order() ([]Repository, error) {
	var result []Repository
	done := map[string]bool{}
	names := map[string]bool{}
	for _, r := range c.Children {
		names[r.Name] = true
	}
	for _, r := range c.Children {
		seen := map[string]bool{}
		for _, d := range r.DependsOn {
			if !names[d] || seen[d] {
				return nil, fmt.Errorf("repository %s: missing or duplicate dependency %s", r.Name, d)
			}
			seen[d] = true
		}
	}
	for len(result) < len(c.Children) {
		found := false
		for _, r := range c.Children {
			if done[r.Name] {
				continue
			}
			ready := true
			for _, d := range r.DependsOn {
				if !done[d] {
					ready = false
				}
			}
			if ready {
				result = append(result, r)
				done[r.Name] = true
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("repository dependency cycle")
		}
	}
	return result, nil
}
