package vcm

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// Snapshot is portable revision data, never executable configuration.
type Snapshot struct {
	Version      int                  `json:"version"`
	Tag          string               `json:"tag"`
	Repositories []SnapshotRepository `json:"repositories"`
}
type SnapshotRepository struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	URL            string `json:"url"`
	Trunk          string `json:"trunk"`
	Base           string `json:"base"`
	Source         string `json:"source"`
	PublicationRef string `json:"publication_ref,omitempty"`
}

func portableURL(raw string) string {
	if filepath.IsAbs(raw) || strings.HasPrefix(raw, "file:") || strings.HasPrefix(raw, ".") {
		return ""
	}
	u, err := url.Parse(raw)
	if err == nil && u.Scheme != "" {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	}
	// SCP syntax may contain an SSH username, but never a password.
	if !strings.Contains(raw, ":") {
		return ""
	}
	if at := strings.Index(raw, "@"); at >= 0 {
		return raw[at+1:]
	}
	return raw
}
func (e *Engine) Export(m *Manifest) (*Snapshot, error) {
	if m.State != "ready" {
		return nil, fmt.Errorf("Change must be ready to export")
	}
	if err := e.ensureCurrentSelection(m); err != nil {
		return nil, err
	}
	s := &Snapshot{Version: 1, Tag: m.Tag, Repositories: []SnapshotRepository{}}
	for i := range m.Repositories {
		r := &m.Repositories[i]
		p := changePublication{state: r}
		if err := e.publicationLocal(m, &p); err != nil {
			return nil, err
		}
		path := r.Repository.Path
		if r.Repository.Name == "root" {
			path = "."
		}
		row := SnapshotRepository{Name: r.Repository.Name, Path: path, URL: portableURL(r.Repository.URL), Trunk: r.Repository.Trunk, Base: r.Base, Source: p.sha}
		if m.Published[r.Repository.Name] == p.sha {
			row.PublicationRef = "refs/heads/" + m.Tag
		}
		if row.URL == "" {
			return nil, fmt.Errorf("repository %s: local transport URL cannot be exported; configure a portable repository origin", row.Name)
		}
		s.Repositories = append(s.Repositories, row)
	}
	return s, nil
}
func (e *Engine) snapshotManifest(s *Snapshot, name string) (*Manifest, error) {
	if s == nil || s.Version != 1 {
		return nil, fmt.Errorf("unsupported snapshot version")
	}
	if err := validateIdentity(s.Tag); err != nil {
		return nil, err
	}
	if !slugPattern.MatchString(name) {
		return nil, fmt.Errorf("name must use lowercase kebab-case with digits")
	}
	names := []string{}
	seen := map[string]bool{}
	for _, r := range s.Repositories {
		if seen[r.Name] {
			return nil, fmt.Errorf("duplicate snapshot repository %s", r.Name)
		}
		seen[r.Name] = true
		if !objectPattern.MatchString(r.Base) || !objectPattern.MatchString(r.Source) {
			return nil, fmt.Errorf("repository %s: invalid snapshot revision", r.Name)
		}
		if r.Name != "root" {
			names = append(names, r.Name)
		}
	}
	if !seen["root"] {
		return nil, fmt.Errorf("snapshot has no root repository")
	}
	// Empty --only normally means all repositories; construct root-only explicitly.
	m, err := e.createPlanManifest(name, strings.Join(names, ","), "")
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		m.Repositories = m.Repositories[:1]
	}
	if len(m.Repositories) != len(s.Repositories) {
		return nil, fmt.Errorf("snapshot selection does not match configured repositories")
	}
	for i := range m.Repositories {
		r := &m.Repositories[i]
		for _, sr := range s.Repositories {
			if sr.Name == r.Repository.Name {
				path := r.Repository.Path
				if r.Repository.Name == "root" {
					path = "."
				}
				if sr.URL == "" || portableURL(sr.URL) != sr.URL || sr.Path != path || sr.Trunk != r.Repository.Trunk || sr.URL != portableURL(r.Repository.URL) {
					return nil, fmt.Errorf("repository %s: snapshot does not match trusted workspace configuration", sr.Name)
				}
				r.Base = sr.Base
				r.Source = sr.Source
			}
		}
	}
	m.Version = 4
	m.State = "restoring"
	m.RestoreSnapshot = s
	m.Recorded = e.recordConfiguration(m)
	return m, nil
}
func (e *Engine) restoreObjects(m *Manifest, fetch bool) error {
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if err := validateOrigin(r.Origin, r.Repository); err != nil {
			return err
		}
		for _, sha := range []string{r.Base, r.Source} {
			kind, err := git(r.Origin, "cat-file", "-t", sha)
			if err != nil && fetch {
				fetchRef := sha
				for _, sr := range m.RestoreSnapshot.Repositories {
					if sr.Name == r.Repository.Name && sr.PublicationRef == "refs/heads/"+m.RestoreSnapshot.Tag {
						fetchRef = sr.PublicationRef
					}
				}
				if _, err = git(r.Origin, "fetch", "--no-tags", "origin", fetchRef); err != nil {
					return fmt.Errorf("repository %s: fetch missing object %s: %w", r.Repository.Name, sha, err)
				}
				kind, err = git(r.Origin, "cat-file", "-t", sha)
			}
			if err != nil {
				return &missingSnapshotObject{repository: r.Repository.Name, sha: sha}
			}
			if kind != "commit" {
				return fmt.Errorf("repository %s: snapshot object %s is not a commit", r.Repository.Name, sha)
			}
		}
		if !ancestor(r.Origin, r.Base, r.Source) {
			return fmt.Errorf("repository %s: snapshot base is not an ancestor of source", r.Repository.Name)
		}
	}
	return nil
}
func (e *Engine) restoreCollision(m *Manifest) error {
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if r.Owned || r.Intent == "create" {
			if _, err := os.Lstat(r.Path); err == nil {
				if err = e.owned(m, r); err != nil {
					return err
				}
				h, err := head(r.Path)
				if err != nil {
					return err
				}
				if h != r.Source {
					return fmt.Errorf("repository %s: restored worktree revision changed", r.Repository.Name)
				}
				if err = clean(r.Path); err != nil {
					return err
				}
				continue
			}
		}
		if _, err := os.Lstat(r.Path); !os.IsNotExist(err) {
			return fmt.Errorf("repository %s: restore path collision %s", r.Repository.Name, r.Path)
		}
		if _, err := git(r.Origin, "show-ref", "--verify", "refs/heads/"+m.Tag); err == nil {
			return fmt.Errorf("repository %s: restore branch collision", r.Repository.Name)
		}
	}
	return nil
}

type missingSnapshotObject struct{ repository, sha string }

func (e *missingSnapshotObject) Error() string {
	return fmt.Sprintf("repository %s: missing commit %s; publish the source and retry with --fetch", e.repository, e.sha)
}
func (e *Engine) resolveRestore(m *Manifest, s *Snapshot, name string) (*Manifest, bool, error) {
	all, err := e.All()
	if err != nil {
		return nil, false, err
	}
	for _, existing := range all {
		if existing.Slug == name && existing.State != "dropped" {
			if existing.State != "restoring" || !reflect.DeepEqual(existing.RestoreSnapshot, s) {
				return nil, false, fmt.Errorf("active Change already exists: %s", existing.Tag)
			}
			return existing, false, nil
		}
	}
	return m, true, nil
}
func (e *Engine) Restore(s *Snapshot, name string, fetch bool) (*Manifest, error) {
	m, err := e.snapshotManifest(s, name)
	if err != nil {
		return nil, err
	}
	m, fresh, err := e.resolveRestore(m, s, name)
	if err != nil {
		return nil, err
	}
	if err = e.ensureCurrentSelection(m); err != nil {
		return m, err
	}
	if err = e.restoreCollision(m); err != nil {
		return m, err
	}
	if err = e.restoreObjects(m, fetch); err != nil {
		return m, err
	}
	if fresh {
		if err = e.store.save(m); err != nil {
			return m, err
		}
	}
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if !r.Owned {
			if r.Intent == "create" {
				if _, er := os.Lstat(r.Path); er == nil {
					r.Owned = true
				}
			}
			if !r.Owned {
				r.Intent = "create"
				if err = e.store.save(m); err != nil {
					return m, err
				}
				// Disable Git post-checkout hooks as well as VCM lifecycle hooks.
				if _, err = git(r.Origin, "-c", "core.hooksPath=/dev/null", "worktree", "add", "-b", m.Tag, r.Path, r.Source); err != nil {
					return m, err
				}
				r.Owned = true
			}
			r.Intent = ""
			if err = e.store.save(m); err != nil {
				return m, err
			}
		}
	}
	m.State = "ready"
	return m, e.store.save(m)
}
func (e *Engine) RestorePlan(s *Snapshot, name string, fetch bool) OperationPlan {
	p := OperationPlan{Command: "restore", DryRun: true, Steps: []PlanStep{}, Blockers: []string{}, Unverified: []string{}}
	m, err := e.snapshotManifest(s, name)
	if err != nil {
		p.Blockers = append(p.Blockers, err.Error())
		return p
	}
	m, _, err = e.resolveRestore(m, s, name)
	if err != nil {
		p.Blockers = append(p.Blockers, err.Error())
		return p
	}
	if err = e.ensureCurrentSelection(m); err != nil {
		p.Blockers = append(p.Blockers, err.Error())
	}
	p.Tag = m.Tag
	p.Workspace = m.Workspace
	if fetch {
		p.Unverified = append(p.Unverified, "remote availability and missing snapshot objects")
	}
	if err = e.restoreCollision(m); err != nil {
		p.Blockers = append(p.Blockers, err.Error())
	}
	if err = e.restoreObjects(m, false); err != nil {
		if _, missing := err.(*missingSnapshotObject); fetch && missing {
			p.Unverified = append(p.Unverified, err.Error())
		} else {
			p.Blockers = append(p.Blockers, err.Error())
		}
	}
	for _, r := range m.Repositories {
		p.Steps = append(p.Steps, PlanStep{Phase: "restore", Repository: r.Repository.Name, Target: filepath.Clean(r.Path), Effect: "create owned worktree at " + r.Source, Checkpoint: "pending"})
	}
	return p
}
