package vcm

import (
	"fmt"
	"strings"
)

// Refresh merges advanced canonical local trunks into the selected Change
// worktrees. It never fetches; callers update canonical trunks explicitly with
// vcm pull.
func (e *Engine) Refresh(m *Manifest) error {
	if err := e.ensureCurrentSelection(m); err != nil {
		return err
	}
	if m.State != "ready" && m.State != "refreshing" {
		return fmt.Errorf("Change %s is not ready or recoverable-refresh", m.Tag)
	}
	// Validate every canonical checkout before recording or applying refresh.
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if err := validateOrigin(r.Origin, r.Repository); err != nil {
			return err
		}
		if _, err := localBaseline(r); err != nil {
			return err
		}
		if err := e.owned(m, r); err != nil {
			return err
		}
	}
	if m.State == "ready" {
		m.State = "refreshing"
		if err := e.store.save(m); err != nil {
			return err
		}
	}
	refreshRepository := func(r *RepoState) error {
		if err := e.owned(m, r); err != nil {
			return err
		}
		for reconciliation := 0; r.Intent == "refresh"; reconciliation++ {
			if reconciliation >= 2 {
				return fmt.Errorf("repository %s: refresh reconciliation exceeded its progress bound", r.Repository.Name)
			}
			before := refreshCheckpoint(r)
			recovery, err := inspectRefreshRecovery(r)
			if err != nil {
				return err
			}
			if recovery.state == refreshRecoveryIncomplete {
				target, targetErr := localBaseline(r)
				if targetErr != nil {
					return targetErr
				}
				if target != r.TargetBefore && !ancestor(r.Origin, r.TargetBefore, target) {
					return fmt.Errorf("repository %s: current canonical target %s does not contain recorded refresh target %s; inspect rewritten target before retrying", r.Repository.Name, target, r.TargetBefore)
				}
				if recovery.source != r.Source && !ancestor(r.Path, r.TargetBefore, recovery.source) {
					return fmt.Errorf("repository %s: Change HEAD %s does not contain recorded refresh target %s; restore or complete the recorded refresh before retrying", r.Repository.Name, recovery.source, r.TargetBefore)
				}
				if cleanErr := clean(r.Path); cleanErr != nil {
					return fmt.Errorf("repository %s: recorded refresh is incomplete with canonical target %s (recorded %s): %w", r.Repository.Name, target, r.TargetBefore, cleanErr)
				}
				return fmt.Errorf("repository %s: recorded refresh is incomplete with Change HEAD %s, recorded target %s, and current canonical target %s; complete the recorded refresh before retrying", r.Repository.Name, recovery.source, r.TargetBefore, target)
			}
			if err = e.finalizeRefreshRecovery(m, r, recovery); err != nil {
				return err
			}
			if refreshCheckpoint(r) == before {
				return fmt.Errorf("repository %s: refresh reconciliation made no observable progress", r.Repository.Name)
			}
		}
		target, err := localBaseline(r)
		if err != nil {
			return err
		}
		source, err := head(r.Path)
		if err != nil {
			return err
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
			return failure("conflict", r.Repository.Name, fmt.Errorf("repository %s refresh conflict preserved for repair; resolve and commit, then retry: %w", r.Repository.Name, mergeErr))
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
	refreshOne := func(r *RepoState) error {
		baseBefore, sourceBefore := r.Base, r.Source
		if err := refreshRepository(r); err != nil {
			return err
		}
		if r.Base == baseBefore {
			e.logOperationOutcome("refresh", r.Repository.Name, r.Path, "", "already current", LogSuccess, " at base %s; source %s", abbreviateRevision(r.Base), abbreviateRevision(r.Source))
			return nil
		}
		e.logOperationOutcome("refresh", r.Repository.Name, r.Path, "", "updated", LogChanged, " base %s -> %s; source %s -> %s", abbreviateRevision(baseBefore), abbreviateRevision(r.Base), abbreviateRevision(sourceBefore), abbreviateRevision(r.Source))
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
