package vcm

import (
	"fmt"
	"strings"
)

const (
	refreshRecoveryRecordedMergeCommit = "recorded_merge_commit"
	refreshRecoveryPreparedIndex       = "prepared_index"
	refreshRecoveryCommittedResolution = "committed_resolution"
	refreshRecoveryIncomplete          = "incomplete"
)

type refreshRecovery struct {
	state  string
	source string
}

func inspectRefreshRecovery(r *RepoState) (refreshRecovery, error) {
	if r.Intent != "refresh" {
		return refreshRecovery{}, nil
	}
	if r.Source == "" || r.TargetBefore == "" {
		return refreshRecovery{}, fmt.Errorf("repository %s: recorded refresh checkpoint is incomplete", r.Repository.Name)
	}
	source, err := head(r.Path)
	if err != nil {
		return refreshRecovery{}, err
	}
	if r.MergeCommit != "" && source == r.MergeCommit {
		if err := clean(r.Path); err != nil {
			return refreshRecovery{}, fmt.Errorf("repository %s: completed refresh recovery is not clean: %w", r.Repository.Name, err)
		}
		return refreshRecovery{state: refreshRecoveryRecordedMergeCommit, source: source}, nil
	}
	if r.MergeCommit != "" && r.MergeTree != "" && source == r.Source {
		index, indexErr := git(r.Path, "write-tree")
		worktreeDiff, diffErr := git(r.Path, "diff", "--name-only")
		if indexErr == nil && diffErr == nil && index == r.MergeTree && worktreeDiff == "" {
			status, statusErr := git(r.Path, "status", "--porcelain", "--untracked-files=all")
			if statusErr != nil {
				return refreshRecovery{}, statusErr
			}
			for _, entry := range strings.Split(status, "\n") {
				if strings.HasPrefix(entry, "?? ") {
					return refreshRecovery{}, fmt.Errorf("repository %s: prepared refresh recovery contains unrelated untracked content; preserve or remove it before retrying", r.Repository.Name)
				}
			}
			return refreshRecovery{state: refreshRecoveryPreparedIndex, source: source}, nil
		}
	}
	if source != r.Source && ancestor(r.Path, r.TargetBefore, source) {
		if err := clean(r.Path); err != nil {
			return refreshRecovery{}, fmt.Errorf("repository %s: completed refresh recovery is not clean: %w", r.Repository.Name, err)
		}
		return refreshRecovery{state: refreshRecoveryCommittedResolution, source: source}, nil
	}
	return refreshRecovery{state: refreshRecoveryIncomplete, source: source}, nil
}

func (e *Engine) finalizeRefreshRecovery(m *Manifest, r *RepoState, recovery refreshRecovery) error {
	switch recovery.state {
	case refreshRecoveryRecordedMergeCommit, refreshRecoveryCommittedResolution:
		// The branch already names the completed result.
	case refreshRecoveryPreparedIndex:
		if _, err := git(r.Path, "update-ref", "refs/heads/"+m.Tag, r.MergeCommit, r.Source); err != nil {
			return err
		}
		recovery.source = r.MergeCommit
	default:
		return fmt.Errorf("repository %s: recorded refresh is not complete", r.Repository.Name)
	}
	r.Base, r.Source = r.TargetBefore, recovery.source
	r.TargetBefore, r.MergeTree, r.MergeCommit, r.Intent = "", "", "", ""
	return e.store.save(m)
}

func refreshCheckpoint(r *RepoState) string {
	return r.Base + "\x00" + r.Source + "\x00" + r.TargetBefore + "\x00" + r.MergeTree + "\x00" + r.MergeCommit + "\x00" + r.Intent
}
