package vcm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

type HookState struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// RepoState is hydrated from current configuration; only lifecycle fields persist.
type RepoState struct {
	Repository   Repository `json:"-"`
	Origin       string     `json:"-"`
	Path         string     `json:"-"`
	Base         string     `json:"base,omitempty"`
	Source       string     `json:"source,omitempty"`
	Target       string     `json:"target,omitempty"`
	TargetBefore string     `json:"target_before,omitempty"`
	MergeTree    string     `json:"merge_tree,omitempty"`
	MergeCommit  string     `json:"merge_commit,omitempty"`
	Intent       string     `json:"intent,omitempty"`
	Owned        bool       `json:"owned"`
	Merged       bool       `json:"merged"`
	Removed      bool       `json:"removed"`
}
type Manifest struct {
	Version      int                  `json:"-"`
	Tag          string               `json:"-"`
	Slug         string               `json:"-"`
	Workspace    string               `json:"-"`
	Origin       string               `json:"-"`
	Config       Config               `json:"-"`
	State        string               `json:"-"`
	Repositories []RepoState          `json:"-"`
	Hooks        map[string]HookState `json:"-"`
	Backups      []string             `json:"-"`
	Missing      []string             `json:"-"`
	MergeMessage string               `json:"-"`
}
type persistedManifest struct {
	Version      int                           `json:"version"`
	State        string                        `json:"state"`
	Repositories map[string]persistedRepoState `json:"repositories"`
	Hooks        map[string]HookState          `json:"hooks"`
	Backups      []string                      `json:"backups,omitempty"`
	MergeMessage string                        `json:"merge_message,omitempty"`
}
type persistedRepoState struct {
	Base         string `json:"base,omitempty"`
	Source       string `json:"source,omitempty"`
	Target       string `json:"target,omitempty"`
	TargetBefore string `json:"target_before,omitempty"`
	MergeTree    string `json:"merge_tree,omitempty"`
	MergeCommit  string `json:"merge_commit,omitempty"`
	Intent       string `json:"intent,omitempty"`
	Owned        bool   `json:"owned,omitempty"`
	Merged       bool   `json:"merged,omitempty"`
	Removed      bool   `json:"removed,omitempty"`
}
type store struct {
	dir    string
	root   string
	config Config
}

var tagPattern = regexp.MustCompile(`^[0-9]{12}-[a-z0-9]+(?:-[a-z0-9]+)*$`)
var objectPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func ownerOnly(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0077 == 0
}
func secureDirectory(path string, create bool) error {
	if create {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || !ownerOnly(info) {
		return fmt.Errorf("state directory %s must be a real directory accessible only by its owner", path)
	}
	return nil
}
func validateIdentity(tag string) error {
	if !tagPattern.MatchString(tag) {
		return fmt.Errorf("invalid manifest Change identity")
	}
	if _, err := time.Parse("060102150405", tag[:12]); err != nil {
		return fmt.Errorf("invalid Change timestamp: %w", err)
	}
	return nil
}
func validatePersisted(s store, tag string, p *persistedManifest) error {
	if p.Version != 1 && p.Version != 2 {
		return fmt.Errorf("invalid state version %d", p.Version)
	}
	if err := validateIdentity(tag); err != nil {
		return err
	}
	if p.MergeMessage != "" {
		if err := ValidateMergeMessage(p.MergeMessage); err != nil {
			return fmt.Errorf("invalid persisted merge message: %w", err)
		}
	}
	if _, ok := p.Repositories["root"]; !ok {
		return fmt.Errorf("manifest has no root repository state")
	}
	for name, r := range p.Repositories {
		if name != "root" && !identity.MatchString(name) {
			return fmt.Errorf("invalid recorded repository name %q", name)
		}
		for _, hash := range []string{r.Base, r.Source, r.Target, r.TargetBefore, r.MergeTree, r.MergeCommit} {
			if hash != "" && !objectPattern.MatchString(hash) {
				return fmt.Errorf("repository %s has invalid recorded Git object", name)
			}
		}
		if r.Owned && r.Base == "" {
			return fmt.Errorf("repository %s owns a worktree without a recorded base", name)
		}
		if r.Merged && (r.Source == "" || r.Target == "") {
			return fmt.Errorf("repository %s has an incomplete merged checkpoint", name)
		}
		if r.Intent == "merge" && (r.Source == "" || r.TargetBefore == "" || r.MergeTree == "" || r.MergeCommit == "") {
			return fmt.Errorf("repository %s has an incomplete merge intent", name)
		}
		switch r.Intent {
		case "", "create", "merge", "refresh", "remove":
		default:
			return fmt.Errorf("repository %s has invalid intent", name)
		}
	}
	switch p.State {
	case "creating", "ready", "refreshing", "merging", "merge-finalizing", "dropping", "dropped":
	default:
		return fmt.Errorf("invalid manifest lifecycle state")
	}
	if p.Hooks == nil {
		return fmt.Errorf("manifest hook outcomes must be an object")
	}
	for key, h := range p.Hooks {
		if !validHookOutcomeKey(key, p.Repositories) {
			return fmt.Errorf("unknown recorded hook %s", key)
		}
		switch h.Status {
		case "running", "failed", "complete":
		default:
			return fmt.Errorf("invalid hook outcome")
		}
	}
	for _, path := range p.Backups {
		if filepath.Dir(path) != filepath.Join(s.dir, "recovery") || filepath.Ext(path) != ".gz" {
			return fmt.Errorf("invalid recovery backup path")
		}
	}
	return nil
}
func validHookOutcomeKey(key string, repositories map[string]persistedRepoState) bool {
	parts := strings.Split(key, "/")
	if len(parts) != 3 || !identity.MatchString(parts[2]) {
		return false
	}
	if _, ok := repositories[parts[0]]; !ok {
		return false
	}
	return hookPhases[parts[1]]
}
func persisted(m *Manifest) persistedManifest {
	p := persistedManifest{Version: 2, State: m.State, Repositories: map[string]persistedRepoState{}, Hooks: m.Hooks, Backups: m.Backups, MergeMessage: m.MergeMessage}
	for _, r := range m.Repositories {
		p.Repositories[r.Repository.Name] = persistedRepoState{r.Base, r.Source, r.Target, r.TargetBefore, r.MergeTree, r.MergeCommit, r.Intent, r.Owned, r.Merged, r.Removed}
	}
	return p
}
func (s store) save(m *Manifest) error {
	if m.Version != 1 && m.Version != 2 {
		return fmt.Errorf("invalid state version %d", m.Version)
	}
	p := persisted(m)
	if err := validatePersisted(s, m.Tag, &p); err != nil {
		return err
	}
	if err := secureDirectory(s.dir, true); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(s.dir, m.Tag+".json")); err != nil {
		return err
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (s store) hydrate(tag string, p persistedManifest) (*Manifest, error) {
	common := filepath.Dir(s.dir)
	if filepath.Base(common) != ".git" {
		return nil, fmt.Errorf("state directory is not owned by a primary workspace checkout")
	}
	root := filepath.Dir(common)
	if s.root != "" && filepath.Clean(s.root) != root {
		return nil, fmt.Errorf("state directory does not belong to the current workspace")
	}
	config := s.config
	if config.Version == 0 {
		var err error
		config, err = readConfig(root)
		if err != nil {
			return nil, fmt.Errorf("read current configuration: %w", err)
		}
	}
	workspace := changeWorkspace(root, tag)
	if info, err := os.Lstat(workspace); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("derived Change workspace path is a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	m := &Manifest{Version: 2, Tag: tag, Slug: tag[13:], Workspace: workspace, Origin: root, Config: config, State: p.State, Hooks: p.Hooks, Backups: p.Backups, MergeMessage: p.MergeMessage}
	rootURL, _ := git(root, "remote", "get-url", "origin")
	appendState := func(repository Repository) {
		state := p.Repositories[repository.Name]
		origin, path := root, workspace
		if repository.Name != "root" {
			origin = filepath.Join(root, repository.Path)
			path = filepath.Join(workspace, repository.Path)
		}
		m.Repositories = append(m.Repositories, RepoState{repository, origin, path, state.Base, state.Source, state.Target, state.TargetBefore, state.MergeTree, state.MergeCommit, state.Intent, state.Owned, state.Merged, state.Removed})
	}
	appendState(Repository{Name: "root", URL: rootURL, Trunk: config.Root.Trunk, Hooks: config.Root.Hooks})
	seen := map[string]bool{"root": true}
	ordered, _ := config.Order()
	for _, repository := range ordered {
		if _, ok := p.Repositories[repository.Name]; ok {
			appendState(repository)
			seen[repository.Name] = true
		}
	}
	for name := range p.Repositories {
		if !seen[name] {
			m.Missing = append(m.Missing, name)
		}
	}
	sort.Strings(m.Missing)
	for _, name := range m.Missing {
		state := p.Repositories[name]
		m.Repositories = append(m.Repositories, RepoState{Repository: Repository{Name: name}, Base: state.Base, Source: state.Source, Target: state.Target, TargetBefore: state.TargetBefore, MergeTree: state.MergeTree, MergeCommit: state.MergeCommit, Intent: state.Intent, Owned: state.Owned, Merged: state.Merged, Removed: state.Removed})
	}
	return m, nil
}
func (s store) load(tag string) (*Manifest, error) {
	if err := validateIdentity(tag); err != nil {
		return nil, err
	}
	if err := secureDirectory(s.dir, false); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, tag+".json"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !ownerOnly(info) {
		return nil, fmt.Errorf("manifest must be an owner-only regular file")
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	var p persistedManifest
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&p); err != nil {
		return nil, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("manifest must contain one JSON object")
	}
	if err = validatePersisted(s, tag, &p); err != nil {
		return nil, err
	}
	return s.hydrate(tag, p)
}
func (s store) all() ([]*Manifest, error) {
	if err := secureDirectory(s.dir, false); os.IsNotExist(err) {
		return []*Manifest{}, nil
	} else if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := []*Manifest{}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		m, err := s.load(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
func (s store) lock() (func(), error) {
	if err := secureDirectory(s.dir, true); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "mutation.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownerOnly(info) {
		f.Close()
		return nil, fmt.Errorf("mutation lock must be an owner-only regular file")
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("workspace mutation already in progress: %w", err)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
func newTag(slug string) string { return time.Now().UTC().Format("060102150405") + "-" + slug }
func changeWorkspace(origin, tag string) string {
	return filepath.Join(filepath.Dir(origin), filepath.Base(origin)+"."+tag)
}
func isChangeWorkspace(origin, tag, workspace string) bool {
	return workspace == changeWorkspace(origin, tag)
}
