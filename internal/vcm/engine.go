package vcm

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type Engine struct {
	Root               string
	Config             Config
	store              store
	Out                io.Writer
	Style              func(LogSemantic, string) string
	Force              bool
	IgnoreHookFailures bool
	SkipMergeHooks     map[string]bool
	SkipHookGitHooks   bool
	Keep               bool
	DryRun             bool
}

type RepositoryStatus struct {
	Name               string
	Path               string
	Source             string
	Target             string
	RecordedTarget     string
	RecoveryState      string
	Merged             bool
	Removed            bool
	Intent             string
	Error              string
	DirtyOrInterrupted string
	OwnershipError     string
	TargetChanged      bool
}

type PendingSyncStatus struct {
	Path   string
	Intent SyncIntentStatus
	Error  string
}

type SyncIntentStatus struct {
	TrunkBefore string
	Repository  string
	Path        string
	Before      string
	Target      string
	Operation   string
}

type StatusReport struct {
	Repositories      []RepositoryStatus
	RecoveryDirectory string
	PendingSync       []PendingSyncStatus
}

type PlanStep struct {
	Phase      string `json:"phase"`
	Repository string `json:"repository"`
	Target     string `json:"target"`
	Effect     string `json:"effect"`
	Checkpoint string `json:"checkpoint"`
}

type OperationPlan struct {
	Steps                 []PlanStep
	Blockers              []string
	Unverified            []string
	Command               string
	DryRun                bool
	Force                 bool
	IgnoreHookFailures    bool
	DeletesIgnoredContent bool
	SkippedHookPhases     []string
	SkipHookGitHooks      bool
	Tag                   string
	Workspace             string
	Resources             []string
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
		return nil, failure("configuration", "", e)
	}
	common, e := commonDir(origin)
	if e != nil {
		return nil, e
	}
	return &Engine{Root: origin, Config: c, store: store{dir: filepath.Join(common, "vcm"), root: origin, config: c}, Out: out}, nil
}
func (e *Engine) All() ([]*Manifest, error) { return e.store.all() }
func (e *Engine) Select(selector, cwd string) (selected *Manifest, selectionErr error) {
	defer classify(&selectionErr, "selection", "")
	all, err := e.All()
	if err != nil {
		return nil, err
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	implicit := selector == ""
	if !implicit {
		for _, m := range all {
			if m.Tag == selector {
				return m, nil
			}
		}
		if !filepath.IsAbs(selector) && !strings.ContainsAny(selector, `/\`) && !strings.HasPrefix(selector, ".") {
			matches := []*Manifest{}
			for _, m := range all {
				if m.Slug == selector {
					matches = append(matches, m)
				}
			}
			if len(matches) == 1 {
				return matches[0], nil
			}
			if len(matches) > 1 {
				tags := []string{}
				for _, m := range matches {
					tags = append(tags, m.Tag)
				}
				return nil, failure("selection", "", fmt.Errorf("ambiguous Change %q; select an exact tag: %s", selector, strings.Join(tags, ", ")))
			}
		}
	}
	if implicit {
		selector = cwd
	}
	if !filepath.IsAbs(selector) && (implicit || strings.ContainsAny(selector, `/\\`) || strings.HasPrefix(selector, ".")) {
		selector = filepath.Join(cwd, selector)
	}
	abs, err := filepath.Abs(selector)
	if err != nil {
		return nil, err
	}
	abs = filepath.Clean(abs)
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	for _, m := range all {
		workspace := filepath.Clean(m.Workspace)
		if resolved, resolveErr := filepath.EvalSymlinks(workspace); resolveErr == nil {
			workspace = resolved
		}
		withinWorkspace := abs == workspace || strings.HasPrefix(abs, workspace+string(filepath.Separator))
		if selector == m.Tag || withinWorkspace {
			if m.Origin != e.Root {
				return nil, fmt.Errorf("manifest workspace ownership mismatch")
			}
			return m, nil
		}
	}
	if implicit {
		return nil, fmt.Errorf("Change argument is required outside a managed Change worktree")
	}
	return nil, fmt.Errorf("no managed Change matches %q; specify a Change name, tag, or workspace path", selector)
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
	e.store.config = current
	if err := e.legacyMutationGuard(); err != nil {
		return err
	}
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
			e.logOperationOutcome("bootstrap", r.Name, path, "", "validated", LogSuccess, " existing checkout on trunk %s", r.Trunk)
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
		e.logOperationOutcome("bootstrap", r.Name, path, "", "cloned", LogChanged, " trunk %s", r.Trunk)
	}
	return nil
}
func (e *Engine) backup(operation, path, label string, m *Manifest) error {
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
	if m != nil {
		m.Backups = append(m.Backups, filename)
		if err := e.store.save(m); err != nil {
			return err
		}
	}
	e.logOperationOutcome(operation, label, path, "", "created", LogChanged, " recovery backup %s; history %s", filename, ref)
	return nil
}
func (e *Engine) Fetch() error {
	repositories, err := e.canonicalRepositories(false)
	if err != nil {
		return err
	}
	for _, item := range repositories {
		r := item.repository
		if err := validateOrigin(item.path, r); err != nil {
			return fmt.Errorf("repository %s fetch: %w", r.Name, err)
		}
		before, _ := git(item.path, "rev-parse", "--verify", "refs/remotes/origin/"+r.Trunk)
		refspec := "+refs/heads/" + r.Trunk + ":refs/remotes/origin/" + r.Trunk
		if _, err := git(item.path, "fetch", "--no-tags", "origin", refspec); err != nil {
			return fmt.Errorf("repository %s fetch: fetch remote trunk %s: %w", r.Name, r.Trunk, err)
		}
		after, err := git(item.path, "rev-parse", "refs/remotes/origin/"+r.Trunk)
		if err != nil {
			return fmt.Errorf("repository %s fetch: resolve cached remote trunk %s: %w", r.Name, r.Trunk, err)
		}
		semantic, outcome := LogChanged, "fetched"
		if before == after {
			semantic, outcome = LogSuccess, "already current"
		}
		e.logOperationOutcome("fetch", r.Name, item.path, "", outcome, semantic, " cached origin/%s at %s", r.Trunk, abbreviateRevision(after))
	}
	return nil
}
func (e *Engine) Create(slug string) (*Manifest, error) {
	return e.CreateSelected(slug, "", "")
}

func parseSelectionList(flag, value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	parts := strings.Split(value, ",")
	for _, name := range parts {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("--%s contains a blank repository name", flag)
		}
		if name != strings.TrimSpace(name) {
			return nil, fmt.Errorf("--%s repository names must not contain surrounding whitespace", flag)
		}
		if name == "root" {
			return nil, fmt.Errorf("--%s must not include root; the root worktree is always selected", flag)
		}
		if seen[name] {
			return nil, fmt.Errorf("--%s contains duplicate repository %s", flag, name)
		}
		seen[name] = true
	}
	return parts, nil
}

func (e *Engine) selectedRepositories(only, except string) ([]Repository, error) {
	if only != "" && except != "" {
		return nil, fmt.Errorf("--only and --except are mutually exclusive")
	}
	flag, raw := "only", only
	if except != "" {
		flag, raw = "except", except
	}
	names, err := parseSelectionList(flag, raw)
	if err != nil {
		return nil, err
	}
	requested := map[string]bool{}
	for _, name := range names {
		requested[name] = true
	}
	configured := map[string]bool{}
	for _, repository := range e.Config.Children {
		configured[repository.Name] = true
	}
	for name := range requested {
		if !configured[name] {
			return nil, fmt.Errorf("unknown repository %q in --%s", name, flag)
		}
	}
	ordered, _ := e.Config.Order()
	selected := make([]Repository, 0, len(ordered))
	selectedNames := map[string]bool{}
	for _, repository := range ordered {
		include := raw == "" || only != "" && requested[repository.Name] || except != "" && !requested[repository.Name]
		if include {
			selected = append(selected, repository)
			selectedNames[repository.Name] = true
		}
	}
	for _, repository := range selected {
		for _, dependency := range repository.DependsOn {
			if !selectedNames[dependency] {
				return nil, fmt.Errorf("repository %s depends on unselected repository %s", repository.Name, dependency)
			}
		}
	}
	return selected, nil
}

func selectedNames(m *Manifest) map[string]bool {
	names := make(map[string]bool, len(m.Repositories))
	for _, repository := range m.Repositories {
		names[repository.Repository.Name] = true
	}
	return names
}

func sameSelection(m *Manifest, selected []Repository) bool {
	want := map[string]bool{"root": true}
	for _, repository := range selected {
		want[repository.Name] = true
	}
	got := selectedNames(m)
	return reflect.DeepEqual(got, want)
}

func (e *Engine) ensureCurrentSelection(m *Manifest) error {
	if len(m.Missing) > 0 {
		return fmt.Errorf("selected repository %s is absent from current configuration; restore its vcm.yml entry before retrying", strings.Join(m.Missing, ", "))
	}
	if err := e.requireConfiguration(m); err != nil {
		return err
	}
	names := selectedNames(m)
	for _, repository := range e.Config.Children {
		if !names[repository.Name] {
			continue
		}
		for _, dependency := range repository.DependsOn {
			if !names[dependency] {
				return fmt.Errorf("repository %s depends on unselected repository %s", repository.Name, dependency)
			}
		}
	}
	return nil
}

func (e *Engine) CreateSelected(slug, only, except string) (*Manifest, error) {
	if !slugPattern.MatchString(slug) {
		return nil, fmt.Errorf("slug must use lowercase kebab-case with digits")
	}
	selected, err := e.selectedRepositories(only, except)
	if err != nil {
		return nil, err
	}
	all, err := e.All()
	if err != nil {
		return nil, err
	}
	for _, m := range all {
		if m.Slug == slug && m.State == "creating" {
			if m.Version < 3 {
				return m, legacyCreationError(m.Tag)
			}
			if err := e.ensureCurrentSelection(m); err != nil {
				return m, err
			}
			if !sameSelection(m, selected) {
				return m, fmt.Errorf("create selection does not match the interrupted Change; retry with the originally selected repositories")
			}
			return m, e.resumeCreate(m)
		}
		if m.Slug == slug && m.State != "dropped" {
			return nil, fmt.Errorf("active Change already exists: %s", m.Tag)
		}
	}
	tag := newTag(slug)
	path := changeWorkspace(e.Root, tag)
	rootURL, err := git(e.Root, "remote", "get-url", "origin")
	if err != nil {
		return nil, err
	}
	m := &Manifest{Version: 4, Tag: tag, Slug: slug, Workspace: path, Origin: e.Root, Config: e.Config, State: "creating", Hooks: map[string]HookState{}}
	m.Repositories = append(m.Repositories, RepoState{Repository: Repository{Name: "root", URL: rootURL, Trunk: e.Config.Root.Trunk, Hooks: e.Config.Root.Hooks}, Origin: e.Root, Path: path})
	for _, r := range selected {
		m.Repositories = append(m.Repositories, RepoState{Repository: r, Origin: filepath.Join(e.Root, r.Path), Path: filepath.Join(path, r.Path)})
	}
	m.Recorded = e.recordConfiguration(m)
	if err = e.preflightCreate(m); err != nil {
		return nil, err
	}
	if err = e.store.save(m); err != nil {
		return nil, err
	}
	return m, e.resumeCreate(m)
}

func (e *Engine) preflightCreate(m *Manifest) error {
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if err := validateOrigin(r.Origin, r.Repository); err != nil {
			return err
		}
		if err := e.reconcileSync(r.Repository, r.Origin); err != nil {
			return err
		}
		if _, err := localBaseline(r); err != nil {
			return err
		}
		if _, err := os.Lstat(r.Path); !os.IsNotExist(err) {
			if i == 0 {
				return fmt.Errorf("Change path collision: %s", r.Path)
			}
			return fmt.Errorf("repository %s: existing unowned path %s", r.Repository.Name, r.Path)
		}
		if _, err := git(r.Origin, "show-ref", "--verify", "refs/heads/"+m.Tag); err == nil {
			return fmt.Errorf("repository %s: branch collision %s", r.Repository.Name, m.Tag)
		}
	}
	return nil
}

func (e *Engine) owned(m *Manifest, r *RepoState) (errOut error) {
	defer classify(&errOut, "ownership", r.Repository.Name)
	expected := m.Workspace
	if r.Repository.Name != "root" {
		expected = filepath.Join(m.Workspace, r.Repository.Path)
	}
	if r.Path != expected || !isChangeWorkspace(e.Root, m.Tag, m.Workspace) || r.Origin != filepath.Join(e.Root, r.Repository.Path) {
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
	if m.Version < 3 {
		return legacyCreationError(m.Tag)
	}
	// Record local baselines for all selected repositories before project hooks run.
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if r.Base == "" {
			base, err := localBaseline(r)
			if err != nil {
				return err
			}
			r.Base = base
			if err := e.store.save(m); err != nil {
				return err
			}
		}
	}
	root := &m.Repositories[0]
	if !root.Owned {
		if err := e.hooksAt(m, root, HookCreateBefore, root.Origin, false); err != nil {
			return err
		}
		base, err := localBaseline(root)
		if err != nil {
			return err
		}
		root.Base = base
		if err = e.store.save(m); err != nil {
			return err
		}
	}
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if m.State == StateExpanding && i > 0 {
			added := false
			for _, name := range m.ExpansionAdded {
				if name == r.Repository.Name {
					added = true
				}
			}
			if !added {
				continue
			}
		}
		if !r.Owned {
			if i > 0 {
				if err := e.hooksAt(m, r, HookCreateBefore, r.Origin, false); err != nil {
					return err
				}
				base, err := localBaseline(r)
				if err != nil {
					return err
				}
				r.Base = base
				if err = e.store.save(m); err != nil {
					return err
				}
			}
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
			e.logOperationOutcome("create", r.Repository.Name, r.Path, "", "created", LogChanged, " managed worktree at %s", abbreviateRevision(r.Base))
		}
		if err := e.owned(m, r); err != nil {
			return err
		}
		if i > 0 {
			if err := e.hooksAt(m, r, HookCreateAfter, r.Path, true); err != nil {
				return err
			}
		}
	}
	if err := e.hooksAt(m, root, HookCreateAfter, root.Path, true); err != nil {
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
	return e.hooksAt(m, r, phase, r.Path, phase != HookDropBefore)
}

func (e *Engine) hooksAt(m *Manifest, r *RepoState, phase, directory string, updateSource bool) (hookErr error) {
	defer classify(&hookErr, "hook_failure", r.Repository.Name)
	if err := e.requireConfiguration(m); err != nil {
		return err
	}
	current, err := readConfig(m.Origin)
	if err != nil {
		return fmt.Errorf("read current hook configuration: %w", err)
	}
	fresh := *e
	fresh.Config = current
	if err := fresh.requireConfiguration(m); err != nil {
		return err
	}
	hooks, err := hooksFor(current, r.Repository.Name)
	if err != nil {
		return err
	}
	if (phase == HookMergeBefore || phase == HookMergeAfter) && e.SkipMergeHooks[phase] {
		for _, h := range hooks[phase] {
			if err := e.logHook(r.Repository.Name, phase, h.ID, directory, "skipped", LogSuccess, " by --skip-hooks"); err != nil {
				return fmt.Errorf("repository %s phase %s hook %s diagnostic: %w", r.Repository.Name, phase, h.ID, err)
			}
		}
		return nil
	}
	basePhase := directory == r.Origin
	if !updateSource && basePhase {
		expected := r.Target
		if phase == HookCreateBefore {
			expected = r.Base
		}
		failed := false
		for _, h := range hooks[phase] {
			if m.Hooks[r.Repository.Name+"/"+phase+"/"+h.ID].Status == "failed" {
				failed = true
				break
			}
		}
		if expected != "" && !failed {
			actual, headErr := head(directory)
			if headErr != nil {
				return headErr
			}
			if actual != expected && !(phase == HookMergeAfter && m.Keep && ancestor(directory, expected, actual)) {
				return fmt.Errorf("repository %s: base changed after %s checkpoint; restore recorded revision before retry", r.Repository.Name, phase)
			}
		}
	}
	for _, h := range hooks[phase] {
		key := r.Repository.Name + "/" + phase + "/" + h.ID
		old := m.Hooks[key]
		if old.Status == "complete" {
			continue
		}
		if old.Status == "running" {
			return failure("interruption", r.Repository.Name, fmt.Errorf("hook %s interrupted; inspect its external effects, then authorize retry with vcm recover %s --retry-hook %s --acknowledge-effects", key, m.Tag, key))
		}
		selected := make([]string, 0, len(m.Repositories))
		for _, repository := range m.Repositories {
			selected = append(selected, repository.Repository.Name)
		}
		hookEnvironment := append(os.Environ(), "VCM_CHANGE_TAG="+m.Tag, "VCM_CHANGE_SLUG="+m.Slug, "VCM_ROOT="+m.Workspace, "VCM_ROOT_ORIGIN="+m.Origin, "VCM_SELECTED_REPOSITORIES="+strings.Join(selected, ","), "VCM_REPOSITORY_NAME="+r.Repository.Name, "VCM_REPOSITORY_ORIGIN="+r.Origin, "VCM_REPOSITORY_PATH="+r.Path, "VCM_HOOK_PHASE="+phase, "VCM_HOOK_ID="+h.ID)
		if e.SkipHookGitHooks && (phase == HookMergeBefore || phase == HookMergeAfter) {
			hookEnvironment, err = suppressGitHooks(hookEnvironment)
			if err != nil {
				return fmt.Errorf("repository %s phase %s hook %s Git configuration: %w", r.Repository.Name, phase, h.ID, err)
			}
		}
		m.Hooks[key] = HookState{Status: "running", Definition: fingerprint(h), Generation: m.Generation}
		if err := e.store.save(m); err != nil {
			return err
		}
		var cmd *exec.Cmd
		if h.Shell != "" {
			cmd = exec.CommandContext(context.Background(), current.Runners.Shell, "-eu", "-o", "pipefail", "-c", h.Shell)
		} else {
			cmd = exec.CommandContext(context.Background(), current.Runners.Python, "-c", h.Python)
		}
		cmd.Dir = directory
		cmd.Env = hookEnvironment
		prefix := e.logPrefix("hook/"+r.Repository.Name+"/"+phase, h.ID, directory) + " "
		hookOutput := newPrefixedLineWriter(e.Out, prefix)
		cmd.Stdout = hookOutput
		cmd.Stderr = hookOutput
		if err := e.logHook(r.Repository.Name, phase, h.ID, directory, "started", LogChanged, ""); err != nil {
			return fmt.Errorf("repository %s phase %s hook %s diagnostic: %w", r.Repository.Name, phase, h.ID, err)
		}
		processErr := cmd.Run()
		if flushErr := hookOutput.Flush(); flushErr != nil {
			m.Hooks[key] = HookState{Status: "failed", Definition: fingerprint(h), Generation: m.Generation, Error: flushErr.Error()}
			if saveErr := e.store.save(m); saveErr != nil {
				return saveErr
			}
			return fmt.Errorf("repository %s phase %s hook %s output: %w", r.Repository.Name, phase, h.ID, flushErr)
		}
		if cleanErr := clean(directory); cleanErr != nil {
			detail := cleanErr.Error()
			if processErr != nil {
				detail = processErr.Error() + "; " + detail
			}
			m.Hooks[key] = HookState{Status: "failed", Definition: fingerprint(h), Generation: m.Generation, Error: detail}
			if saveErr := e.store.save(m); saveErr != nil {
				return saveErr
			}
			return fmt.Errorf("repository %s phase %s hook %s: %s; repair preserved checkout and retry", r.Repository.Name, phase, h.ID, detail)
		}
		if processErr != nil {
			var exitErr *exec.ExitError
			if e.IgnoreHookFailures && (phase == HookMergeBefore || phase == HookMergeAfter) && errors.As(processErr, &exitErr) {
				if revisionErr := e.recordHookRevision(r, phase, directory, updateSource, basePhase); revisionErr != nil {
					return revisionErr
				}
				m.Hooks[key] = HookState{Status: "failed", Definition: fingerprint(h), Generation: m.Generation, Error: processErr.Error()}
				if saveErr := e.store.save(m); saveErr != nil {
					return saveErr
				}
				if logErr := e.logHook(r.Repository.Name, phase, h.ID, directory, "failed", LogFailure, fmt.Sprintf(" (ignored by --ignore-hook-failures): %v", processErr)); logErr != nil {
					return fmt.Errorf("repository %s phase %s hook %s diagnostic: %w", r.Repository.Name, phase, h.ID, logErr)
				}
				continue
			}
			m.Hooks[key] = HookState{Status: "failed", Definition: fingerprint(h), Generation: m.Generation, Error: processErr.Error()}
			if saveErr := e.store.save(m); saveErr != nil {
				return saveErr
			}
			return fmt.Errorf("repository %s phase %s hook %s: %w; repair preserved checkout and retry", r.Repository.Name, phase, h.ID, processErr)
		}
		if err = e.recordHookRevision(r, phase, directory, updateSource, basePhase); err != nil {
			return err
		}
		m.Hooks[key] = HookState{Status: "complete", Definition: fingerprint(h), Generation: m.Generation}
		if err = e.store.save(m); err != nil {
			return err
		}
		if err = e.logHook(r.Repository.Name, phase, h.ID, directory, "completed", LogSuccess, ""); err != nil {
			return fmt.Errorf("repository %s phase %s hook %s diagnostic: %w", r.Repository.Name, phase, h.ID, err)
		}
	}
	return nil
}

func suppressGitHooks(environment []string) ([]string, error) {
	values := map[string][]string{}
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = append(values[name], value)
		}
	}
	counts := values["GIT_CONFIG_COUNT"]
	if len(counts) > 1 {
		return nil, fmt.Errorf("GIT_CONFIG_COUNT is defined more than once")
	}
	count := 0
	if len(counts) == 1 {
		parsed, err := strconv.ParseUint(counts[0], 10, 31)
		if err != nil {
			return nil, fmt.Errorf("invalid GIT_CONFIG_COUNT %q", counts[0])
		}
		count = int(parsed)
	}
	if count > len(environment)/2 {
		return nil, fmt.Errorf("GIT_CONFIG_COUNT %d exceeds the available configuration entries", count)
	}
	for i := 0; i < count; i++ {
		keyName := fmt.Sprintf("GIT_CONFIG_KEY_%d", i)
		valueName := fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)
		if len(values[keyName]) != 1 || values[keyName][0] == "" || len(values[valueName]) != 1 {
			return nil, fmt.Errorf("GIT_CONFIG_COUNT entry %d must have exactly one nonblank key and one value", i)
		}
	}
	nextKey := fmt.Sprintf("GIT_CONFIG_KEY_%d", count)
	nextValue := fmt.Sprintf("GIT_CONFIG_VALUE_%d", count)
	if len(values[nextKey]) > 0 || len(values[nextValue]) > 0 {
		return nil, fmt.Errorf("Git configuration entry %d already exists outside GIT_CONFIG_COUNT", count)
	}
	result := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if name != "GIT_CONFIG_COUNT" {
			result = append(result, entry)
		}
	}
	result = append(result, fmt.Sprintf("GIT_CONFIG_COUNT=%d", count+1), nextKey+"=core.hooksPath", nextValue+"=/dev/null")
	return result, nil
}

func (e *Engine) recordHookRevision(r *RepoState, phase, directory string, updateSource, basePhase bool) error {
	if !updateSource && !basePhase {
		return nil
	}
	revision, err := head(directory)
	if err != nil {
		return err
	}
	if updateSource {
		r.Source = revision
	} else if phase == HookCreateBefore {
		r.Base = revision
	} else {
		r.Target = revision
	}
	return nil
}

func sameChangeConfig(current, recorded Config) bool {
	current.Root.Hooks = nil
	for i := range current.Children {
		current.Children[i].Hooks = nil
	}
	recorded.Root.Hooks = nil
	for i := range recorded.Children {
		recorded.Children[i].Hooks = nil
	}
	return reflect.DeepEqual(current, recorded)
}

func hooksFor(config Config, repository string) (Hooks, error) {
	if repository == "root" {
		return config.Root.Hooks, nil
	}
	for _, child := range config.Children {
		if child.Name == repository {
			return child.Hooks, nil
		}
	}
	return nil, fmt.Errorf("repository %s is absent from current configuration", repository)
}

func (e *Engine) Status(m *Manifest) StatusReport {
	repos := []RepositoryStatus{}
	for _, r := range m.Repositories {
		state := RepositoryStatus{Name: r.Repository.Name, Path: r.Path, Merged: r.Merged, Removed: r.Removed, Intent: string(r.Intent), RecordedTarget: r.TargetBefore}
		if r.Repository.Name != "root" && r.Path == "" {
			state.Error = fmt.Sprintf("selected repository %s is absent from current configuration; restore its vcm.yml entry", r.Repository.Name)
			repos = append(repos, state)
			continue
		}
		if !r.Removed {
			h, err := head(r.Path)
			state.Source = h
			if err != nil {
				state.Error = err.Error()
			}
			if err = clean(r.Path); err != nil {
				state.DirtyOrInterrupted = err.Error()
			}
			if err = e.owned(m, &r); err != nil {
				state.OwnershipError = err.Error()
			}
			if r.Intent == "refresh" {
				recovery, recoveryErr := inspectRefreshRecovery(&r)
				if recoveryErr != nil && state.Error == "" {
					state.Error = recoveryErr.Error()
				} else {
					state.RecoveryState = recovery.state
				}
			}
		}
		target, err := head(r.Origin)
		if err == nil {
			state.Target = target
			expected := r.Base
			if r.Merged {
				expected = r.Target
			}
			state.TargetChanged = expected != "" && target != expected
		}
		repos = append(repos, state)
	}
	return StatusReport{Repositories: repos, RecoveryDirectory: filepath.Join(e.store.dir, "recovery"), PendingSync: e.pendingSync()}
}
func (e *Engine) CreatePlan(slug string) (OperationPlan, error) {
	return e.CreatePlanSelected(slug, "", "")
}

func (e *Engine) CreatePlanSelected(slug, only, except string) (OperationPlan, error) {
	if !slugPattern.MatchString(slug) {
		return OperationPlan{}, fmt.Errorf("slug must use lowercase kebab-case with digits")
	}
	m, err := e.createPlanManifest(slug, only, except)
	if err != nil {
		return OperationPlan{}, err
	}
	all, err := e.All()
	if err != nil {
		return OperationPlan{}, err
	}
	var blocker string
	selected, err := e.selectedRepositories(only, except)
	if err != nil {
		return OperationPlan{}, err
	}
	for _, existing := range all {
		if existing.Slug != slug || existing.State == "dropped" {
			continue
		}
		if existing.State == "creating" {
			m = existing
			if !sameSelection(m, selected) {
				blocker = "create selection does not match interrupted Change"
			}
		} else {
			blocker = "active Change already exists: " + existing.Tag
		}
		break
	}
	resources := []string{}
	for _, r := range m.Repositories {
		resources = append(resources, r.Path)
	}
	plan := e.enrichPlan(OperationPlan{Command: "create", DryRun: true, Tag: m.Tag, Workspace: m.Workspace, Resources: resources}, m)
	if blocker != "" {
		plan.Blockers = append(plan.Blockers, blocker)
	}
	return plan, nil
}

func (e *Engine) pendingSync() []PendingSyncStatus {
	out := []PendingSyncStatus{}
	entries, _ := os.ReadDir(e.store.dir)
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".sync" {
			continue
		}
		path := filepath.Join(e.store.dir, entry.Name())
		intent, err := readSync(path)
		item := PendingSyncStatus{Path: path, Intent: SyncIntentStatus{
			TrunkBefore: intent.TrunkBefore,
			Repository:  intent.Repository,
			Path:        intent.Path,
			Before:      intent.Before,
			Target:      intent.Target,
			Operation:   intent.Operation,
		}}
		if err != nil {
			item.Error = err.Error()
		}
		out = append(out, item)
	}
	return out
}
