package vcm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type HookState struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}
type RepoState struct {
	Repository   Repository `json:"repository"`
	Origin       string     `json:"origin"`
	Path         string     `json:"path"`
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
	Version      int                  `json:"version"`
	Tag          string               `json:"tag"`
	Slug         string               `json:"slug"`
	Workspace    string               `json:"workspace"`
	Origin       string               `json:"origin"`
	Config       Config               `json:"config"`
	State        string               `json:"state"`
	Repositories []RepoState          `json:"repositories"`
	Hooks        map[string]HookState `json:"hooks"`
	Backups      []string             `json:"backups,omitempty"`
}
type store struct{ dir string }

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

func (s store) validate(m *Manifest) error {
	if m.Version != 1 || !tagPattern.MatchString(m.Tag) || !slugPattern.MatchString(m.Slug) || m.Tag[13:] != m.Slug {
		return fmt.Errorf("invalid manifest Change identity")
	}
	if _, err := time.Parse("060102150405", m.Tag[:12]); err != nil {
		return fmt.Errorf("invalid Change timestamp: %w", err)
	}
	if !filepath.IsAbs(m.Origin) || filepath.Clean(m.Origin) != m.Origin {
		return fmt.Errorf("invalid manifest origin")
	}
	common, err := commonDir(m.Origin)
	if err != nil {
		return err
	}
	if filepath.Join(common, "vcm") != s.dir {
		return fmt.Errorf("manifest origin does not own this state directory")
	}
	if filepath.Base(common) != ".git" || filepath.Dir(common) != m.Origin {
		return fmt.Errorf("manifest origin must be the primary workspace checkout")
	}
	wantWorkspace := changeWorkspace(m.Origin, m.Tag)
	if m.Workspace != wantWorkspace {
		return fmt.Errorf("manifest workspace path does not match its identity")
	}
	if info, err := os.Lstat(m.Workspace); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("manifest workspace path is a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := m.Config.Validate(m.Origin); err != nil {
		return fmt.Errorf("invalid manifest configuration: %w", err)
	}
	ordered, _ := m.Config.Order()
	if len(m.Repositories) == 0 {
		return fmt.Errorf("manifest has no workspace repository")
	}
	rootURL := m.Repositories[0].Repository.URL
	if strings.TrimSpace(rootURL) == "" || strings.HasPrefix(rootURL, "-") {
		return fmt.Errorf("manifest has invalid workspace origin URL")
	}
	want := append([]Repository{{Name: "root", URL: rootURL, Trunk: m.Config.Root.Trunk, Hooks: m.Config.Root.Hooks}}, ordered...)
	if len(m.Repositories) != len(want) {
		return fmt.Errorf("manifest repository set does not match configuration")
	}
	for i, r := range m.Repositories {
		if !reflect.DeepEqual(r.Repository, want[i]) || r.Origin != filepath.Join(m.Origin, want[i].Path) || r.Path != filepath.Join(m.Workspace, want[i].Path) {
			return fmt.Errorf("manifest repository %d ownership does not match configuration", i)
		}
		if i > 0 {
			if err := safePath(m.Workspace, want[i].Path); err != nil {
				return err
			}
		}
		for _, hash := range []string{r.Base, r.Source, r.Target, r.TargetBefore, r.MergeTree, r.MergeCommit} {
			if hash != "" && !objectPattern.MatchString(hash) {
				return fmt.Errorf("repository %s has invalid recorded Git object", r.Repository.Name)
			}
		}
		if r.Owned && r.Base == "" {
			return fmt.Errorf("repository %s owns a worktree without a recorded base", r.Repository.Name)
		}
		if r.Merged && (r.Source == "" || r.Target == "") {
			return fmt.Errorf("repository %s has an incomplete merged checkpoint", r.Repository.Name)
		}
		if r.Intent == "merge" && (r.Source == "" || r.TargetBefore == "" || r.MergeTree == "" || r.MergeCommit == "") {
			return fmt.Errorf("repository %s has an incomplete merge intent", r.Repository.Name)
		}
		switch r.Intent {
		case "", "create", "merge", "remove":
		default:
			return fmt.Errorf("repository %s has invalid intent", r.Repository.Name)
		}
	}
	switch m.State {
	case "creating", "ready", "merging", "dropping", "dropped":
	default:
		return fmt.Errorf("invalid manifest lifecycle state")
	}
	if m.Hooks == nil {
		return fmt.Errorf("manifest hook outcomes must be an object")
	}
	knownHooks := map[string]bool{}
	for _, r := range want {
		for phase, hooks := range r.Hooks {
			for _, h := range hooks {
				knownHooks[r.Name+"/"+phase+"/"+h.ID] = true
			}
		}
	}
	for key, h := range m.Hooks {
		if !knownHooks[key] {
			return fmt.Errorf("unknown recorded hook %s", key)
		}
		switch h.Status {
		case "running", "failed", "complete":
		default:
			return fmt.Errorf("invalid hook outcome")
		}
	}
	for _, path := range m.Backups {
		if filepath.Dir(path) != filepath.Join(s.dir, "recovery") || filepath.Ext(path) != ".gz" {
			return fmt.Errorf("invalid recovery backup path")
		}
	}
	return nil
}

func (s store) save(m *Manifest) error {
	m.Config.applyDefaults()
	if e := s.validate(m); e != nil {
		return e
	}
	if e := secureDirectory(s.dir, true); e != nil {
		return e
	}
	b, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(s.dir, ".manifest-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(append(b, 10))
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), filepath.Join(s.dir, m.Tag+".json")); e != nil {
		return e
	}
	d, e := os.Open(s.dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (s store) load(tag string) (*Manifest, error) {
	if !tagPattern.MatchString(tag) {
		return nil, fmt.Errorf("invalid Change tag")
	}
	if err := secureDirectory(s.dir, false); err != nil {
		return nil, err
	}
	f, e := os.OpenFile(filepath.Join(s.dir, tag+".json"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || !ownerOnly(info) {
		return nil, fmt.Errorf("manifest must be an owner-only regular file")
	}
	b, e := io.ReadAll(f)
	if e != nil {
		return nil, e
	}
	var m Manifest
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&m); e != nil {
		return nil, e
	}
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		return nil, fmt.Errorf("manifest must contain one JSON object")
	}
	if e = validateManifestRunners(b); e != nil {
		return nil, e
	}
	if m.Tag != tag {
		return nil, fmt.Errorf("invalid manifest %s", tag)
	}
	m.Config.applyDefaults()
	if e = s.validate(&m); e != nil {
		return nil, e
	}
	return &m, nil
}

func validateManifestRunners(b []byte) error {
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(b, &manifest); err != nil {
		return err
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(manifest["config"], &config); err != nil {
		return err
	}
	raw, present := config["runners"]
	if !present {
		return nil
	}
	if string(raw) == "null" {
		return fmt.Errorf("invalid manifest runners")
	}
	var runners map[string]*string
	if err := json.Unmarshal(raw, &runners); err != nil {
		return fmt.Errorf("invalid manifest runners: %w", err)
	}
	for _, name := range []string{"shell", "python"} {
		if value, ok := runners[name]; ok && (value == nil || strings.TrimSpace(*value) == "") {
			return fmt.Errorf("invalid manifest %s runner", name)
		}
	}
	return nil
}
func (s store) all() ([]*Manifest, error) {
	if err := secureDirectory(s.dir, false); os.IsNotExist(err) {
		return []*Manifest{}, nil
	} else if err != nil {
		return nil, err
	}
	entries, e := os.ReadDir(s.dir)
	if os.IsNotExist(e) {
		return []*Manifest{}, nil
	}
	if e != nil {
		return nil, e
	}
	out := []*Manifest{}
	for _, x := range entries {
		if filepath.Ext(x.Name()) != ".json" {
			continue
		}
		m, e := s.load(x.Name()[:len(x.Name())-5])
		if e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, nil
}
func (s store) lock() (func(), error) {
	if e := secureDirectory(s.dir, true); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(s.dir, "mutation.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || !ownerOnly(info) {
		f.Close()
		return nil, fmt.Errorf("mutation lock must be an owner-only regular file")
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("workspace mutation already in progress: %w", e)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
func newTag(slug string) string { return time.Now().UTC().Format("060102150405") + "-" + slug }

func changeWorkspace(origin, tag string) string {
	return filepath.Join(filepath.Dir(origin), filepath.Base(origin)+"."+tag)
}
