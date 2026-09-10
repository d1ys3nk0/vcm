package vcm

import (
	"fmt"
	"strings"
)

// Refresh merges advanced canonical local trunks into the selected Change
// worktrees. It never fetches; callers synchronize explicitly with vcm sync.
func (e *Engine) Refresh(m *Manifest) error {
	if err := e.ensureCurrentSelection(m); err != nil {
		return err
	}
	if m.State != "ready" && m.State != "refreshing" {
		return fmt.Errorf("Change %s is not ready or recoverable-refresh", m.Tag)
	}
	if m.State == "ready" {
		m.State = "refreshing"
		if err := e.store.save(m); err != nil {
			return err
		}
	}
	refreshOne := func(r *RepoState) error {
		if err := e.owned(m, r); err != nil {
			return err
		}
		target, err := head(r.Origin)
		if err != nil {
			return err
		}
		if r.Intent == "refresh" && r.TargetBefore != "" && target != r.TargetBefore {
			return fmt.Errorf("repository %s: target drifted during refresh; restore recorded target and retry", r.Repository.Name)
		}
		source, err := head(r.Path)
		if err != nil {
			return err
		}
		if r.Intent == "refresh" && r.MergeCommit != "" {
			if source == r.MergeCommit {
				r.Base, r.Source, r.TargetBefore, r.MergeTree, r.MergeCommit, r.Intent = r.TargetBefore, source, "", "", "", ""
				return e.store.save(m)
			}
			index, indexErr := git(r.Path, "write-tree")
			worktreeDiff, diffErr := git(r.Path, "diff", "--name-only")
			if source == r.Source && indexErr == nil && diffErr == nil && index == r.MergeTree && worktreeDiff == "" {
				if _, err = git(r.Path, "update-ref", "refs/heads/"+m.Tag, r.MergeCommit, source); err != nil {
					return err
				}
				r.Base, r.Source, r.TargetBefore, r.MergeTree, r.MergeCommit, r.Intent = r.TargetBefore, r.MergeCommit, "", "", "", ""
				return e.store.save(m)
			}
		}
		if r.Intent == "refresh" && source != r.Source && ancestor(r.Path, r.TargetBefore, source) {
			r.Base, r.Source, r.TargetBefore, r.MergeTree, r.MergeCommit, r.Intent = r.TargetBefore, source, "", "", "", ""
			if err = e.store.save(m); err != nil {
				return err
			}
			return nil
		}
		if err := clean(r.Origin); err != nil {
			return err
		}
		if target == r.Base {
			r.Source = source
			return nil
		}
		if !ancestor(r.Origin, r.Base, target) {
			return fmt.Errorf("repository %s: canonical target no longer contains recorded base", r.Repository.Name)
		}
		if err := clean(r.Path); err != nil {
			return fmt.Errorf("repository %s refresh recovery: %w", r.Repository.Name, err)
		}
		tree, mergeErr := git(r.Path, "merge-tree", "--write-tree", source, target)
		if mergeErr != nil {
			r.Source, r.TargetBefore, r.Intent = source, target, "refresh"
			if err = e.store.save(m); err != nil {
				return err
			}
			_, _ = git(r.Path, "merge", "--no-ff", "--no-commit", target)
			return fmt.Errorf("repository %s refresh conflict preserved for repair; resolve and commit, then retry: %w", r.Repository.Name, mergeErr)
		}
		tree = strings.Split(tree, "\n")[0]
		commit, err := git(r.Path, "commit-tree", tree, "-p", source, "-p", target, "-m", "chore(vcm): refresh "+m.Tag)
		if err != nil {
			return err
		}
		r.Source, r.TargetBefore, r.MergeTree, r.MergeCommit, r.Intent = source, target, tree, commit, "refresh"
		if err = e.store.save(m); err != nil {
			return err
		}
		if _, err = git(r.Path, "read-tree", "-u", "-m", source, commit); err != nil {
			return err
		}
		if _, err = git(r.Path, "update-ref", "refs/heads/"+m.Tag, commit, source); err != nil {
			return err
		}
		r.Base, r.Source, r.TargetBefore, r.MergeTree, r.MergeCommit, r.Intent = target, commit, "", "", "", ""
		if err = e.store.save(m); err != nil {
			return err
		}
		return nil
	}
	for i := 1; i < len(m.Repositories); i++ {
		if err := refreshOne(&m.Repositories[i]); err != nil {
			return err
		}
	}
	if err := refreshOne(&m.Repositories[0]); err != nil {
		return err
	}
	m.State = "ready"
	if err := e.store.save(m); err != nil {
		return err
	}
	return nil
}
