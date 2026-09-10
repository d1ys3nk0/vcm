package vcm

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const (
	PruneReset          = "reset"
	PruneRemoveWorktree = "remove_worktree"
	PruneDeleteBranch   = "delete_branch"

	PruneCompleted = "completed"
	PruneDeclined  = "declined"
	PruneBlocked   = "blocked"
	PruneFailed    = "failed"
)

type PruneAction struct {
	Repository string `json:"repository"`
	Action     string `json:"action"`
	Target     string `json:"target"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
}

type PruneReport struct {
	Complete        bool          `json:"complete"`
	Workspace       string        `json:"workspace"`
	Actions         []PruneAction `json:"actions"`
	RemainingIssues []AuditIssue  `json:"remaining_issues"`
}

type ConfirmPrune func(PruneAction) bool

func sortedPaths[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func abortGitOperation(path string) error {
	operations := []struct {
		marker string
		args   []string
	}{
		{"rebase-merge", []string{"rebase", "--abort"}},
		{"rebase-apply", []string{"rebase", "--abort"}},
		{"CHERRY_PICK_HEAD", []string{"cherry-pick", "--abort"}},
		{"REVERT_HEAD", []string{"revert", "--abort"}},
		{"MERGE_HEAD", []string{"merge", "--abort"}},
	}
	for _, operation := range operations {
		marker, err := git(path, "rev-parse", "--git-path", operation.marker)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(marker) {
			marker = filepath.Join(path, marker)
		}
		if _, err = os.Stat(marker); err == nil {
			_, err = git(path, operation.args...)
			return err
		}
	}
	return nil
}

func resetCheckout(path string) error {
	if err := abortGitOperation(path); err != nil {
		return err
	}
	if _, err := git(path, "reset", "--hard", "HEAD"); err != nil {
		return err
	}
	_, err := git(path, "clean", "-fd")
	return err
}

func removeWorktree(repository *repositoryAudit, worktree worktreeInfo) error {
	if worktree.Locked {
		if _, err := git(repository.Origin, "worktree", "unlock", worktree.Path); err != nil {
			return err
		}
	}
	_, err := git(repository.Origin, "worktree", "remove", "--force", worktree.Path)
	return err
}

func branchCheckedOut(repository *repositoryAudit, branch string) bool {
	for _, worktree := range repository.Worktrees {
		if worktree.Branch == branch {
			return true
		}
	}
	return false
}

func plannedActions(inv auditInventory) []PruneAction {
	actions := []PruneAction{}
	if inv.Unsafe {
		return actions
	}
	names := inv.RepositoryOrder
	for _, name := range names {
		repository := inv.Repositories[name]
		for _, path := range sortedPaths(repository.DirtyExpected) {
			actions = append(actions, PruneAction{Repository: name, Action: PruneReset, Target: path, Status: PruneBlocked, Detail: "dry run"})
		}
	}
	for _, name := range names {
		repository := inv.Repositories[name]
		for _, path := range sortedPaths(repository.UnexpectedWorktrees) {
			actions = append(actions, PruneAction{Repository: name, Action: PruneRemoveWorktree, Target: path, Status: PruneBlocked, Detail: "dry run"})
		}
	}
	for _, name := range names {
		repository := inv.Repositories[name]
		for _, branch := range sortedPaths(repository.UnexpectedBranches) {
			actions = append(actions, PruneAction{Repository: name, Action: PruneDeleteBranch, Target: branch, Status: PruneBlocked, Detail: "dry run"})
		}
	}
	return actions
}

func (e *Engine) PruneDryRun() (PruneReport, error) {
	inv, err := e.audit()
	if err != nil {
		return PruneReport{}, err
	}
	return PruneReport{Complete: inv.Report.Clean, Workspace: e.Root, Actions: plannedActions(inv), RemainingIssues: inv.Report.Issues}, nil
}

func (e *Engine) Prune(confirm ConfirmPrune) (PruneReport, error) {
	inv, err := e.audit()
	if err != nil {
		return PruneReport{}, err
	}
	report := PruneReport{Workspace: e.Root, Actions: []PruneAction{}}
	if inv.Unsafe {
		report.RemainingIssues = inv.Report.Issues
		return report, nil
	}
	names := inv.RepositoryOrder
	for _, name := range names {
		repository := inv.Repositories[name]
		for _, path := range sortedPaths(repository.DirtyExpected) {
			action := PruneAction{Repository: name, Action: PruneReset, Target: path}
			if !confirm(action) {
				action.Status = PruneDeclined
				report.Actions = append(report.Actions, action)
				continue
			}
			if err := resetCheckout(path); err != nil {
				action.Status, action.Detail = PruneFailed, err.Error()
			} else {
				action.Status = PruneCompleted
			}
			report.Actions = append(report.Actions, action)
		}
	}
	for _, name := range names {
		repository := inv.Repositories[name]
		for _, path := range sortedPaths(repository.UnexpectedWorktrees) {
			action := PruneAction{Repository: name, Action: PruneRemoveWorktree, Target: path}
			if !confirm(action) {
				action.Status = PruneDeclined
				report.Actions = append(report.Actions, action)
				continue
			}
			if err := removeWorktree(repository, repository.UnexpectedWorktrees[path]); err != nil {
				action.Status, action.Detail = PruneFailed, err.Error()
			} else {
				action.Status = PruneCompleted
			}
			report.Actions = append(report.Actions, action)
		}
	}

	// Worktree removal can make branches deletable, so inventory again before the
	// compare-and-delete phase.
	branchesInv, err := e.audit()
	if err != nil {
		return report, err
	}
	if branchesInv.Unsafe {
		report.RemainingIssues = branchesInv.Report.Issues
		return report, nil
	}
	names = branchesInv.RepositoryOrder
	for _, name := range names {
		repository := branchesInv.Repositories[name]
		for _, branch := range sortedPaths(repository.UnexpectedBranches) {
			action := PruneAction{Repository: name, Action: PruneDeleteBranch, Target: branch}
			if !confirm(action) {
				action.Status = PruneDeclined
				report.Actions = append(report.Actions, action)
				continue
			}
			if branchCheckedOut(repository, branch) {
				action.Status, action.Detail = PruneBlocked, "branch is checked out in a registered worktree"
				report.Actions = append(report.Actions, action)
				continue
			}
			object := repository.UnexpectedBranches[branch]
			if _, err := git(repository.Origin, "update-ref", "-d", "refs/heads/"+branch, object); err != nil {
				action.Status, action.Detail = PruneFailed, fmt.Sprintf("branch changed since audit: %v", err)
			} else {
				action.Status = PruneCompleted
			}
			report.Actions = append(report.Actions, action)
		}
	}
	final, err := e.audit()
	if err != nil {
		return report, err
	}
	report.Complete = final.Report.Clean
	report.RemainingIssues = final.Report.Issues
	return report, nil
}
