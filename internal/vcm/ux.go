package vcm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Error identifies a failure at a public operation boundary.
type Error struct {
	Code       string
	Repository string
	Err        error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }
func failure(code, repository string, err error) error {
	var existing *Error
	if errors.As(err, &existing) {
		if existing.Repository == "" && repository != "" {
			return &Error{Code: existing.Code, Repository: repository, Err: err}
		}
		return err
	}
	return &Error{Code: code, Repository: repository, Err: err}
}
func classify(err *error, code, repository string) {
	if *err != nil {
		*err = failure(code, repository, *err)
	}
}
func (e *Engine) legacyMutationGuard() error {
	pending := e.pendingSync()
	if len(pending) > 0 {
		return failure("interruption", pending[0].Intent.Repository, fmt.Errorf("legacy synchronization journal %s; use the previous VCM binary to finish or discard the interrupted creation", pending[0].Path))
	}
	entries, err := os.ReadDir(e.store.dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		m, err := e.store.load(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return failure("inspection", "", fmt.Errorf("cannot inspect state %s: %w", entry.Name(), err))
		}
		if m.Version < 3 && m.State == "creating" {
			return legacyCreationError(m.Tag)
		}
	}

	return nil
}
func localBaseline(r *RepoState) (string, error) {
	if err := clean(r.Origin); err != nil {
		return "", failure("preflight", r.Repository.Name, err)
	}
	b, err := branch(r.Origin)
	if err != nil || b != r.Repository.Trunk {
		return "", failure("preflight", r.Repository.Name, fmt.Errorf("repository %s: canonical checkout must be on trunk %s", r.Repository.Name, r.Repository.Trunk))
	}
	return head(r.Origin)
}

// Path resolves an existing managed checkout without executing hooks.
func (e *Engine) Path(selector, context string, base bool) (string, error) {
	path := e.Root
	if !base {
		m, err := e.Select(selector, context)
		if err != nil {
			return "", err
		}
		if len(m.Repositories) == 0 || m.Repositories[0].Removed || !m.Repositories[0].Owned {
			return "", fmt.Errorf("Change checkout is removed or not yet created")
		}
		if err := e.owned(m, &m.Repositories[0]); err != nil {
			return "", err
		}
		path = m.Workspace
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("checkout is not a directory: %s", path)
	}
	return path, nil
}

type RecoveryResult struct {
	Change       string `json:"change"`
	Hook         string `json:"hook"`
	DryRun       bool   `json:"dry_run"`
	RetryCommand string `json:"retry_command"`
}

// Recover only authorizes a retry. It never executes external effects.
func (e *Engine) Recover(selector, context, key string, acknowledged bool) (RecoveryResult, error) {
	result := RecoveryResult{Hook: key, DryRun: e.DryRun}
	action := func() error {
		m, err := e.Select(selector, context)
		if err != nil {
			return err
		}
		result.Change = m.Tag
		parts := strings.Split(key, "/")
		if len(parts) != 3 || m.Hooks[key].Status != "running" {
			return failure("interruption", "", fmt.Errorf("retry-hook must identify an exact recorded running hook"))
		}
		phase := parts[1]
		if m.State == "merging" && phase != HookMergeBefore || m.State == "merge-finalizing" && phase != HookMergeAfter {
			return fmt.Errorf("hook phase does not match lifecycle %s", m.State)
		}
		operation := ""
		switch m.State {
		case "creating":
			operation = "create"
		case "merging", "merge-finalizing":
			operation = "merge"
		case "dropping":
			operation = "drop"
		}
		if operation == "" || !strings.HasPrefix(phase, operation+"-") {
			return fmt.Errorf("hook %s is not applicable to lifecycle %s", key, m.State)
		}
		hooks, err := hooksFor(e.Config, parts[0])
		if err != nil {
			return err
		}
		found := false
		for _, h := range hooks[phase] {
			if h.ID == parts[2] {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("hook %s no longer exists in current configuration", key)
		}
		var repository *RepoState
		for i := range m.Repositories {
			if m.Repositories[i].Repository.Name == parts[0] {
				repository = &m.Repositories[i]
			}
		}
		if repository == nil {
			return fmt.Errorf("hook repository is not selected")
		}
		if phase == HookCreateBefore && repository.Owned || (phase == HookCreateAfter || phase == HookMergeBefore || phase == HookDropBefore) && (!repository.Owned || repository.Removed) || phase == HookDropAfter && !repository.Removed {
			return fmt.Errorf("hook %s is inconsistent with recorded resource state", key)
		}
		if phase == HookMergeAfter {
			for _, r := range m.Repositories {
				if !r.Merged || !r.Removed {
					return fmt.Errorf("merge-after recovery requires completed integration and cleanup")
				}
			}
		}

		result.RetryCommand = "vcm " + operation + " " + m.Tag
		if operation == "create" {
			names := []string{}
			for _, r := range m.Repositories {
				if r.Repository.Name != "root" {
					names = append(names, r.Repository.Name)
				}
			}
			result.RetryCommand = "vcm create " + m.Slug
			if len(names) > 0 {
				result.RetryCommand += " --only " + strings.Join(names, ",")
			} else if len(e.Config.Children) > 0 {
				all := []string{}
				for _, r := range e.Config.Children {
					all = append(all, r.Name)
				}
				result.RetryCommand += " --except " + strings.Join(all, ",")
			}
		}
		result.RetryCommand += " --workspace " + quoteArgument(m.Origin)
		if !acknowledged {
			return fmt.Errorf("inspect external effects of %s, then pass --acknowledge-effects to authorize its retry", key)
		}
		if e.DryRun {
			return nil
		}
		m.Hooks[key] = HookState{Status: "failed", Error: "retry explicitly authorized after inspection of external effects"}
		return e.store.save(m)
	}
	var err error
	if e.DryRun {
		err = e.legacyMutationGuard()
		if err == nil {
			err = action()
		}
	} else {
		err = e.Mutate(action)
	}
	return result, err
}

type InitResult struct {
	Workspace string `json:"workspace"`
	Path      string `json:"path"`
	Trunk     string `json:"trunk"`
	DryRun    bool   `json:"dry_run"`
}

func Init(path, trunk string, dry bool) (InitResult, error) {
	result := InitResult{DryRun: dry}
	root, err := git(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return result, err
	}
	common, err := commonDir(root)
	if err != nil {
		return result, err
	}
	if common != filepath.Join(root, ".git") {
		return result, fmt.Errorf("init requires a canonical Git checkout, not a linked worktree")
	}
	if _, err = head(root); err != nil {
		return result, fmt.Errorf("init requires a repository with a commit: %w", err)
	}
	if trunk == "" {
		trunk, err = branch(root)
		if err != nil {
			return result, fmt.Errorf("detached HEAD requires --trunk")
		}
	}
	if !validBranch(trunk) {
		return result, fmt.Errorf("invalid trunk %q", trunk)
	}
	if _, err = git(root, "rev-parse", "--verify", "refs/heads/"+trunk); err != nil {
		return result, err
	}
	result.Workspace, result.Path, result.Trunk = root, filepath.Join(root, "vcm.yml"), trunk
	if _, err = os.Lstat(result.Path); !os.IsNotExist(err) {
		return result, fmt.Errorf("configuration already exists: %s", result.Path)
	}
	if dry {
		return result, nil
	}
	data, err := yaml.Marshal(Config{Version: 1, Root: Root{Trunk: trunk}, Children: []Repository{}})
	if err != nil {
		return result, err
	}
	f, err := os.OpenFile(result.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return result, err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return result, err
}

func quoteArgument(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// PublicationError preserves partial multi-repository publication facts.
type PublicationError struct {
	Err       error
	Completed []string
	Blocked   string
	Pending   []string
}

func (e *PublicationError) Error() string { return e.Err.Error() }
func (e *PublicationError) Unwrap() error { return e.Err }

func legacyCreationError(tag string) error {
	return failure("interruption", "", fmt.Errorf("Change %s has unfinished legacy creation; use the previous VCM binary to finish or discard it", tag))
}
