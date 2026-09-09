package vcm

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

type Engine struct {
	Root   string
	Config Config
	store  store
	Out    io.Writer
	Force  bool
	DryRun bool
}

func Open(path string, out io.Writer) (*Engine, error) {
	root, e := discover(path)
	if e != nil {
		return nil, e
	}
	origin, e := originRoot(root)
	if e != nil {
		return nil, e
	}
	c, e := readConfig(origin)
	if e != nil {
		return nil, e
	}
	common, e := commonDir(origin)
	if e != nil {
		return nil, e
	}
	return &Engine{Root: origin, Config: c, store: store{filepath.Join(common, "vcm")}, Out: out}, nil
}
func (e *Engine) All() ([]*Manifest, error) { return e.store.all() }
func (e *Engine) Select(selector, cwd string) (*Manifest, error) {
	all, err := e.All()
	if err != nil {
		return nil, err
	}
	if selector == "" {
		selector = cwd
	}
	abs, _ := filepath.Abs(selector)
	for _, m := range all {
		if selector == m.Tag || abs == m.Workspace || strings.HasPrefix(abs, m.Workspace+string(filepath.Separator)) {
			if m.Origin != e.Root {
				return nil, fmt.Errorf("manifest workspace ownership mismatch")
			}
			return m, nil
		}
	}
	return nil, fmt.Errorf("no managed Change matches %q; specify a Change tag or workspace path", selector)
}
func (e *Engine) Mutate(fn func() error) error {
	if e.DryRun {
		return nil
	}
	unlock, err := e.store.lock()
	if err != nil {
		return err
	}
	defer unlock()
	current, err := readConfig(e.Root)
	if err != nil {
		return err
	}
	e.Config = current
	return fn()
}
func (e *Engine) Bootstrap() error {
	ordered, _ := e.Config.Order()
	for _, r := range ordered {
		path := filepath.Join(e.Root, r.Path)
		if _, err := os.Lstat(path); err == nil {
			if err = validateOrigin(path, r); err != nil {
				return err
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if _, err := git(e.Root, "clone", "--branch", r.Trunk, "--", r.URL, path); err != nil {
			return fmt.Errorf("repository %s bootstrap: %w; remove incomplete clone after inspection and retry", r.Name, err)
		}
	}
	return nil
}
func (e *Engine) backup(path, label string, m *Manifest) error {
	dir := filepath.Join(e.store.dir, "recovery")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	filename := filepath.Join(dir, time.Now().UTC().Format("20060102T150405.000000000")+"-"+label+".tar.gz")
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	err = filepath.Walk(path, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, er := filepath.Rel(path, p)
		if er != nil {
			return er
		}
		if rel == "." {
			return nil
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, er = os.Readlink(p)
			if er != nil {
				return er
			}
		}
		h, er := tar.FileInfoHeader(info, link)
		if er != nil {
			return er
		}
		h.Name = rel
		if er = tw.WriteHeader(h); er != nil {
			return er
		}
		if info.Mode().IsRegular() {
			in, er := os.Open(p)
			if er != nil {
				return er
			}
			_, er = io.Copy(tw, in)
			in.Close()
			return er
		}
		return nil
	})
	for _, closer := range []func() error{tw.Close, gz.Close, f.Sync, f.Close} {
		if x := closer(); err == nil {
			err = x
		}
	}
	if err != nil {
		return err
	}
	rev, err := head(path)
	if err != nil {
		return err
	}
	ref := "refs/vcm/recovery/" + time.Now().UTC().Format("20060102T150405.000000000") + "-" + label
	if _, err = git(path, "update-ref", ref, rev); err != nil {
		return err
	}
	fmt.Fprintf(e.Out, "Recovery backup: %s; history: %s\n", filename, ref)
	if m != nil {
		m.Backups = append(m.Backups, filename)
		return e.store.save(m)
	}
	return nil
}
func (e *Engine) syncOne(r Repository, create bool) (string, error) {
	p := filepath.Join(e.Root, r.Path)
	if err := validateOrigin(p, r); err != nil {
		return "", err
	}
	if err := e.reconcileSync(r, p); err != nil {
		return "", err
	}
	if err := clean(p); err != nil && (!e.Force || create) {
		return "", err
	}
	if _, err := git(p, "fetch", "origin", r.Trunk); err != nil {
		return "", err
	}
	target, err := git(p, "rev-parse", "refs/remotes/origin/"+r.Trunk)
	if err != nil {
		return "", err
	}
	trunk, err := git(p, "rev-parse", "refs/heads/"+r.Trunk)
	if err != nil {
		return "", err
	}
	if !create && !e.Force && !ancestor(p, trunk, target) {
		return "", fmt.Errorf("repository %s sync: trunk diverges or has local commits; inspect or use --force with recovery backup", r.Name)
	}
	operation := "fast-forward"
	if create {
		operation = "rebase"
	} else if e.Force {
		operation = "reset"
	}
	if e.Force && !create {
		if err = e.backup(p, r.Name, nil); err != nil {
			return "", err
		}
		if _, err = git(p, "update-ref", "refs/vcm/recovery/"+time.Now().UTC().Format("20060102T150405.000000000")+"-trunk", trunk); err != nil {
			return "", err
		}
	}
	complete, err := e.syncIntent(r, p, target, operation)
	if err != nil {
		return "", err
	}
	if e.Force && !create {
		if _, err = git(p, "reset", "--hard", "HEAD"); err != nil {
			return "", err
		}
		if _, err = git(p, "clean", "-fdx"); err != nil {
			return "", err
		}
	}
	if _, err = git(p, "checkout", r.Trunk); err != nil {
		return "", err
	}
	if create {
		_, err = git(p, "rebase", target)
	} else if e.Force {
		_, err = git(p, "reset", "--hard", target)
	} else {
		_, err = git(p, "merge", "--ff-only", target)
	}
	if err != nil {
		return "", fmt.Errorf("repository %s sync: %w; repair interrupted Git operation before retry", r.Name, err)
	}
	if err = complete(); err != nil {
		return "", err
	}
	return head(p)
}
func (e *Engine) Sync() error {
	ordered, _ := e.Config.Order()
	for _, r := range ordered {
		if _, err := e.syncOne(r, false); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) Create(slug string) (*Manifest, error) {
	if !slugPattern.MatchString(slug) {
		return nil, fmt.Errorf("slug must use lowercase kebab-case with digits")
	}
	all, err := e.All()
	if err != nil {
		return nil, err
	}
	for _, m := range all {
		if m.Slug == slug && m.State == "creating" {
			return m, e.resumeCreate(m)
		}
		if m.Slug == slug && m.State != "dropped" {
			return nil, fmt.Errorf("active Change already exists: %s", m.Tag)
		}
	}
	if err = clean(e.Root); err != nil {
		return nil, err
	}
	tag := newTag(slug)
	path := filepath.Join(filepath.Dir(e.Root), filepath.Base(e.Root)+"-"+tag)
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		return nil, fmt.Errorf("Change path collision: %s", path)
	}
	rootURL, err := git(e.Root, "remote", "get-url", "origin")
	if err != nil {
		return nil, err
	}
	m := &Manifest{Version: 1, Tag: tag, Slug: slug, Workspace: path, Origin: e.Root, Config: e.Config, State: "creating", Hooks: map[string]HookState{}}
	m.Repositories = append(m.Repositories, RepoState{Repository: Repository{Name: "workspace", URL: rootURL, Trunk: e.Config.Trunk, Hooks: e.Config.Hooks}, Origin: e.Root, Path: path})
	ordered, _ := e.Config.Order()
	for _, r := range ordered {
		m.Repositories = append(m.Repositories, RepoState{Repository: r, Origin: filepath.Join(e.Root, r.Path), Path: filepath.Join(path, r.Path)})
	}
	if err = e.store.save(m); err != nil {
		return nil, err
	}
	return m, e.resumeCreate(m)
}
func (e *Engine) owned(m *Manifest, r *RepoState) error {
	expected := m.Workspace
	if r.Repository.Name != "workspace" {
		expected = filepath.Join(m.Workspace, r.Repository.Path)
	}
	if r.Path != expected || m.Workspace != filepath.Join(filepath.Dir(e.Root), filepath.Base(e.Root)+"-"+m.Tag) || r.Origin != filepath.Join(e.Root, r.Repository.Path) {
		return fmt.Errorf("repository %s: ownership paths mismatch", r.Repository.Name)
	}
	actual, err := filepath.EvalSymlinks(r.Path)
	if err != nil || actual != r.Path {
		return fmt.Errorf("repository %s: aliased owned path", r.Repository.Name)
	}
	top, err := git(r.Path, "rev-parse", "--show-toplevel")
	if err != nil || top != r.Path {
		return fmt.Errorf("repository %s: owned path is not a checkout root", r.Repository.Name)
	}
	common, err := commonDir(r.Path)
	if err != nil {
		return err
	}
	originCommon, err := commonDir(r.Origin)
	if err != nil {
		return err
	}
	if common != originCommon {
		return fmt.Errorf("repository %s: worktree ownership mismatch", r.Repository.Name)
	}
	b, err := branch(r.Path)
	if err != nil || b != m.Tag {
		return fmt.Errorf("repository %s: expected owned branch %s", r.Repository.Name, m.Tag)
	}
	return nil
}
func (e *Engine) resumeCreate(m *Manifest) error {
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if r.Base == "" {
			base, err := e.syncOne(r.Repository, true)
			if err != nil {
				return err
			}
			if i == 0 {
				current, err := readConfig(e.Root)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(current, m.Config) {
					return fmt.Errorf("workspace configuration changed during synchronization; inspect and drop incomplete Change before retry")
				}
			}
			r.Base = base
			if err = e.store.save(m); err != nil {
				return err
			}
		}
		if !r.Owned {
			if r.Intent == "create" {
				if _, err := os.Stat(r.Path); err == nil {
					if err = e.owned(m, r); err != nil {
						return err
					}
					r.Owned = true
				}
			}
			if !r.Owned {
				if _, err := os.Lstat(r.Path); !os.IsNotExist(err) {
					return fmt.Errorf("repository %s: existing unowned path %s", r.Repository.Name, r.Path)
				}
				if _, err := git(r.Origin, "show-ref", "--verify", "refs/heads/"+m.Tag); err == nil {
					return fmt.Errorf("repository %s: branch collision %s", r.Repository.Name, m.Tag)
				}
				r.Intent = "create"
				if err := e.store.save(m); err != nil {
					return err
				}
				if _, err := git(r.Origin, "worktree", "add", "-b", m.Tag, r.Path, r.Base); err != nil {
					return err
				}
				r.Owned = true
			}
			r.Intent = ""
			if err := e.store.save(m); err != nil {
				return err
			}
		}
		if err := e.owned(m, r); err != nil {
			return err
		}
		if i > 0 {
			if err := e.hooks(m, r, "create"); err != nil {
				return err
			}
		}
	}
	if err := e.hooks(m, &m.Repositories[0], "create"); err != nil {
		return err
	}
	for i := range m.Repositories {
		h, err := head(m.Repositories[i].Path)
		if err != nil {
			return err
		}
		m.Repositories[i].Source = h
	}
	m.State = "ready"
	return e.store.save(m)
}
func (e *Engine) hooks(m *Manifest, r *RepoState, phase string) error {
	for _, h := range r.Repository.Hooks[phase] {
		key := r.Repository.Name + "/" + phase + "/" + h.ID
		old := m.Hooks[key]
		if old.Status == "complete" {
			continue
		}
		if old.Status == "running" {
			return fmt.Errorf("repository %s hook %s interrupted; inspect effects and use status to locate manifest; mark outcome failed to retry idempotently", r.Repository.Name, key)
		}
		m.Hooks[key] = HookState{Status: "running"}
		if err := e.store.save(m); err != nil {
			return err
		}
		cmd := exec.CommandContext(context.Background(), "/bin/sh", "-eu", "-c", h.Command)
		cmd.Dir = r.Path
		cmd.Env = append(os.Environ(), "VCM_CHANGE_TAG="+m.Tag, "VCM_CHANGE_SLUG="+m.Slug, "VCM_WORKSPACE="+m.Workspace, "VCM_WORKSPACE_ORIGIN="+m.Origin, "VCM_REPOSITORY_NAME="+r.Repository.Name, "VCM_REPOSITORY_ORIGIN="+r.Origin, "VCM_REPOSITORY_PATH="+r.Path)
		cmd.Stdout = e.Out
		cmd.Stderr = e.Out
		err := cmd.Run()
		if err == nil {
			err = clean(r.Path)
		}
		if err != nil {
			m.Hooks[key] = HookState{Status: "failed", Error: err.Error()}
			if saveErr := e.store.save(m); saveErr != nil {
				return saveErr
			}
			return fmt.Errorf("repository %s phase %s hook %s: %w; repair preserved checkout and retry", r.Repository.Name, phase, h.ID, err)
		}
		revision, revErr := head(r.Path)
		if revErr != nil {
			return revErr
		}
		if phase != "drop" {
			r.Source = revision
		}
		m.Hooks[key] = HookState{Status: "complete"}
		if err = e.store.save(m); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) Status(m *Manifest) map[string]any {
	repos := []map[string]any{}
	for _, r := range m.Repositories {
		state := map[string]any{"name": r.Repository.Name, "path": r.Path, "merged": r.Merged, "removed": r.Removed, "intent": r.Intent}
		if !r.Removed {
			h, err := head(r.Path)
			state["source"] = h
			if err != nil {
				state["error"] = err.Error()
			}
			if err = clean(r.Path); err != nil {
				state["dirty_or_interrupted"] = err.Error()
			}
			if err = e.owned(m, &r); err != nil {
				state["ownership_error"] = err.Error()
			}
		}
		target, err := head(r.Origin)
		if err == nil {
			state["target"] = target
			state["target_changed"] = r.Merged && target != r.Target
		}
		repos = append(repos, state)
	}
	return map[string]any{"manifest": m, "repositories": repos, "recovery_directory": filepath.Join(e.store.dir, "recovery"), "pending_sync": e.pendingSync()}
}
func (e *Engine) CreatePlan(slug string) (map[string]any, error) {
	if !slugPattern.MatchString(slug) {
		return nil, fmt.Errorf("slug must use lowercase kebab-case with digits")
	}
	tag := newTag(slug)
	path := filepath.Join(filepath.Dir(e.Root), filepath.Base(e.Root)+"-"+tag)
	resources := []string{path}
	ordered, _ := e.Config.Order()
	for _, r := range ordered {
		resources = append(resources, filepath.Join(path, r.Path))
	}
	return map[string]any{"command": "create", "dry_run": true, "tag": tag, "workspace": path, "resources": resources}, nil
}

func (e *Engine) pendingSync() []map[string]any {
	out := []map[string]any{}
	entries, _ := os.ReadDir(e.store.dir)
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".sync" {
			continue
		}
		path := filepath.Join(e.store.dir, entry.Name())
		intent, err := readSync(path)
		item := map[string]any{"path": path, "intent": intent}
		if err != nil {
			item["error"] = err.Error()
		}
		out = append(out, item)
	}
	return out
}
