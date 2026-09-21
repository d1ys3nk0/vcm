package vcm

import (
	"bytes"
	"crypto/rand"
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

// WorkspaceID is the stable, opaque identity of a managed workspace.  Git
// branch names remain an implementation detail and must never be used to
// select a manifest.
func WorkspaceID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate workspace ID: %w", err)
	}
	return fmt.Sprintf("ws-%x", b), nil
}

type HookState struct {
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Definition string `json:"definition,omitempty"`
	Generation int    `json:"generation,omitempty"`
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
	Intent       Intent     `json:"intent,omitempty"`
	// CheckoutCustody and BranchCustody are deliberately separate.  An
	// externally-created root may be checked out by a harness while VCM owns
	// only the branch it attached to that checkout.
	CheckoutCustody string `json:"checkout_custody,omitempty"`
	BranchCustody   string `json:"branch_custody,omitempty"`
	Owned           bool   `json:"owned"` // v4 compatibility while reading historical state
	Merged          bool   `json:"merged"`
	Removed         bool   `json:"removed"`
}
type Manifest struct {
	ExpansionAdded  []string               `json:"-"`
	HookHistory     map[string][]HookState `json:"-"`
	Recorded        []RecordedRepository   `json:"-"`
	Generation      int                    `json:"-"`
	Expansion       []string               `json:"-"`
	Keep            bool                   `json:"-"`
	RestoreSnapshot *Snapshot              `json:"-"`
	Published       map[string]string      `json:"-"`
	Version         int                    `json:"-"`
	WorkspaceID     string                 `json:"-"`
	Name            string                 `json:"-"`
	CreatedAt       time.Time              `json:"-"`
	RootOrigin      string                 `json:"-"`
	RootCustody     string                 `json:"-"`
	// Tag and Slug are retained only to read v1-v4 manifests. New v5
	// manifests never persist or expose them.
	Tag          string               `json:"-"`
	Slug         string               `json:"-"`
	Workspace    string               `json:"-"`
	Origin       string               `json:"-"`
	Config       Config               `json:"-"`
	State        Lifecycle            `json:"-"`
	Repositories []RepoState          `json:"-"`
	Hooks        map[string]HookState `json:"-"`
	Backups      []string             `json:"-"`
	Missing      []string             `json:"-"`
	MergeMessage string               `json:"-"`
}
type persistedManifest struct {
	WorkspaceID     string                        `json:"workspace_id,omitempty"`
	Name            string                        `json:"name,omitempty"`
	CreatedAt       string                        `json:"created_at,omitempty"`
	RootOrigin      string                        `json:"root_origin,omitempty"`
	RootCustody     string                        `json:"root_custody,omitempty"`
	Workspace       string                        `json:"workspace,omitempty"`
	ExpansionAdded  []string                      `json:"expansion_added,omitempty"`
	HookHistory     map[string][]HookState        `json:"hook_history,omitempty"`
	Recorded        []RecordedRepository          `json:"configuration,omitempty"`
	Generation      int                           `json:"generation,omitempty"`
	Expansion       []string                      `json:"expansion,omitempty"`
	Keep            bool                          `json:"keep,omitempty"`
	RestoreSnapshot *Snapshot                     `json:"restore_snapshot,omitempty"`
	Published       map[string]string             `json:"published,omitempty"`
	Version         int                           `json:"version"`
	State           Lifecycle                     `json:"state"`
	Repositories    map[string]persistedRepoState `json:"repositories"`
	Hooks           map[string]HookState          `json:"hooks"`
	Backups         []string                      `json:"backups,omitempty"`
	MergeMessage    string                        `json:"merge_message,omitempty"`
}
type persistedRepoState struct {
	Base            string `json:"base,omitempty"`
	Source          string `json:"source,omitempty"`
	Target          string `json:"target,omitempty"`
	TargetBefore    string `json:"target_before,omitempty"`
	MergeTree       string `json:"merge_tree,omitempty"`
	MergeCommit     string `json:"merge_commit,omitempty"`
	Intent          Intent `json:"intent,omitempty"`
	Owned           bool   `json:"owned,omitempty"`
	CheckoutCustody string `json:"checkout_custody,omitempty"`
	BranchCustody   string `json:"branch_custody,omitempty"`
	Merged          bool   `json:"merged,omitempty"`
	Removed         bool   `json:"removed,omitempty"`
}
type store struct {
	dir    string
	root   string
	config Config
}

var tagPattern = regexp.MustCompile(`^[0-9]{12}-[a-z0-9]+(?:-[a-z0-9]+)*$`)
var workspaceIDPattern = regexp.MustCompile(`^ws-[0-9a-f]{32}$`)
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
		return fmt.Errorf("invalid legacy workspace identity")
	}
	if _, err := time.Parse("060102150405", tag[:12]); err != nil {
		return fmt.Errorf("invalid legacy workspace timestamp: %w", err)
	}
	return nil
}
func validatePersisted(s store, tag string, p *persistedManifest) error {
	if p.Version == 4 || p.Version == 5 {
		if err := validateVersionFour(p); err != nil {
			return err
		}
	}
	if p.Version != 1 && p.Version != 2 && p.Version != 3 && p.Version != 4 && p.Version != 5 {
		return fmt.Errorf("invalid state version %d", p.Version)
	}
	if p.Version == 5 {
		if tag != p.WorkspaceID || !workspaceIDPattern.MatchString(p.WorkspaceID) {
			return fmt.Errorf("invalid workspace ID")
		}
		if !validBranch(p.Name) {
			return fmt.Errorf("invalid workspace name")
		}
		if _, err := time.Parse(time.RFC3339Nano, p.CreatedAt); err != nil {
			return fmt.Errorf("invalid workspace creation time: %w", err)
		}
		if !filepath.IsAbs(p.Workspace) || p.RootOrigin == "" || (p.RootCustody != "vcm" && p.RootCustody != "external") {
			return fmt.Errorf("invalid workspace root metadata")
		}
	} else if err := validateIdentity(tag); err != nil {
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
		if p.Version == 5 {
			if r.CheckoutCustody != "vcm" && r.CheckoutCustody != "external" {
				return fmt.Errorf("repository %s has invalid checkout custody", name)
			}
			if r.BranchCustody != "vcm" && r.BranchCustody != "external" {
				return fmt.Errorf("repository %s has invalid branch custody", name)
			}
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
	case "creating", "ready", "refreshing", "merging", "merge-finalizing", "dropping", "dropped", "expanding", "restoring", "integrated":
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
	p := persistedManifest{WorkspaceID: m.WorkspaceID, Name: m.Name, RootOrigin: m.RootOrigin, RootCustody: m.RootCustody, Version: m.Version, ExpansionAdded: m.ExpansionAdded, HookHistory: m.HookHistory, Recorded: m.Recorded, Generation: m.Generation, Expansion: m.Expansion, Keep: m.Keep, RestoreSnapshot: m.RestoreSnapshot, Published: m.Published, State: m.State, Repositories: map[string]persistedRepoState{}, Hooks: m.Hooks, Backups: m.Backups, MergeMessage: m.MergeMessage}
	if m.Version == 5 {
		p.Workspace = m.Workspace
	}
	if !m.CreatedAt.IsZero() {
		p.CreatedAt = m.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	for _, r := range m.Repositories {
		p.Repositories[r.Repository.Name] = persistedRepoState{Base: r.Base, Source: r.Source, Target: r.Target, TargetBefore: r.TargetBefore, MergeTree: r.MergeTree, MergeCommit: r.MergeCommit, Intent: r.Intent, Owned: r.Owned, CheckoutCustody: r.CheckoutCustody, BranchCustody: r.BranchCustody, Merged: r.Merged, Removed: r.Removed}
	}
	return p
}
func (s store) save(m *Manifest) error {
	if m.Version != 1 && m.Version != 2 && m.Version != 3 && m.Version != 4 && m.Version != 5 {
		return fmt.Errorf("invalid state version %d", m.Version)
	}
	if m.Version < 3 && m.State == "creating" {
		return legacyCreationError(m.Tag)
	}
	p := persisted(m)
	if err := validatePersisted(s, manifestKey(m), &p); err != nil {
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
	if err = os.Rename(f.Name(), filepath.Join(s.dir, manifestKey(m)+".json")); err != nil {
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
	workspaceID, name, createdAt, rootOrigin, rootCustody := "", "", time.Time{}, root, "vcm"
	if p.Version == 5 {
		workspaceID, name, rootOrigin, rootCustody, workspace = p.WorkspaceID, p.Name, p.RootOrigin, p.RootCustody, p.Workspace
		var err error
		createdAt, err = time.Parse(time.RFC3339Nano, p.CreatedAt)
		if err != nil {
			return nil, err
		}
	}
	if info, err := os.Lstat(workspace); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("derived legacy workspace path is a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	legacyTag := ""
	if p.Version < 5 {
		legacyTag = tag
	}
	m := &Manifest{Version: p.Version, ExpansionAdded: p.ExpansionAdded, HookHistory: p.HookHistory, Recorded: p.Recorded, Generation: p.Generation, Expansion: p.Expansion, Keep: p.Keep, RestoreSnapshot: p.RestoreSnapshot, Published: p.Published, WorkspaceID: workspaceID, Name: name, CreatedAt: createdAt, RootOrigin: rootOrigin, RootCustody: rootCustody, Tag: legacyTag, Workspace: workspace, Origin: root, Config: config, State: p.State, Hooks: p.Hooks, Backups: p.Backups, MergeMessage: p.MergeMessage}
	if p.Version < 5 {
		m.Slug = tag[13:]
	}
	rootURL, _ := git(root, "remote", "get-url", "origin")
	appendState := func(repository Repository) {
		state := p.Repositories[repository.Name]
		origin, path := root, workspace
		if repository.Name != "root" {
			origin = filepath.Join(root, repository.Path)
			path = filepath.Join(workspace, repository.Path)
		}
		m.Repositories = append(m.Repositories, RepoState{Repository: repository, Origin: origin, Path: path, Base: state.Base, Source: state.Source, Target: state.Target, TargetBefore: state.TargetBefore, MergeTree: state.MergeTree, MergeCommit: state.MergeCommit, Intent: state.Intent, Owned: state.Owned, CheckoutCustody: state.CheckoutCustody, BranchCustody: state.BranchCustody, Merged: state.Merged, Removed: state.Removed})
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
		m.Repositories = append(m.Repositories, RepoState{Repository: Repository{Name: name}, Base: state.Base, Source: state.Source, Target: state.Target, TargetBefore: state.TargetBefore, MergeTree: state.MergeTree, MergeCommit: state.MergeCommit, Intent: state.Intent, Owned: state.Owned, CheckoutCustody: state.CheckoutCustody, BranchCustody: state.BranchCustody, Merged: state.Merged, Removed: state.Removed})
	}
	return m, nil
}
func (s store) load(tag string) (*Manifest, error) {
	if err := validateIdentity(tag); err != nil && !workspaceIDPattern.MatchString(tag) {
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
func manifestKey(m *Manifest) string {
	if m.Version == 5 {
		return m.WorkspaceID
	}
	return m.Tag
}
func workspaceName(m *Manifest) string {
	if m.Version == 5 {
		return m.Name
	}
	return m.Tag
}
func workspaceSelector(m *Manifest) string {
	if m.Version == 5 {
		return m.WorkspaceID
	}
	return m.Tag
}
func changeWorkspace(origin, tag string) string {
	return filepath.Join(filepath.Dir(origin), filepath.Base(origin)+"."+tag)
}
func isChangeWorkspace(origin, tag, workspace string) bool {
	return workspace == changeWorkspace(origin, tag)
}
