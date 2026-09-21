package vcm

import (
	"fmt"
	"sort"
	"strings"
)

type pullRepository struct {
	canonicalRepository
	local  string
	remote string
}

func (e *Engine) baselineUsers(repository Repository, path, local, remote string) ([]string, error) {
	if ancestor(path, remote, local) {
		return nil, nil
	}
	manifests, err := e.All()
	if err != nil {
		return nil, err
	}
	changeSet := map[string]struct{}{}
	for _, manifest := range manifests {
		if manifest.State == "dropped" {
			continue
		}
		for _, state := range manifest.Repositories {
			if state.Repository.Name != repository.Name || state.Base == "" {
				continue
			}
			if ancestor(path, state.Base, local) && !ancestor(path, state.Base, remote) {
				changeSet[workspaceSelector(manifest)] = struct{}{}
			}
		}
	}
	changes := make([]string, 0, len(changeSet))
	for tag := range changeSet {
		changes = append(changes, tag)
	}
	sort.Strings(changes)
	return changes, nil
}

// Pull rebases clean canonical trunks onto their fetched remote trunks. All
// repositories are preflighted and fetched before any local branch is moved.
func (e *Engine) Pull() error {
	canonical, err := e.canonicalRepositories(true)
	if err != nil {
		return err
	}
	repositories := make([]pullRepository, len(canonical))
	for index, item := range canonical {
		repositories[index].canonicalRepository = item
		r := item.repository
		if err := validateOrigin(item.path, r); err != nil {
			return fmt.Errorf("repository %s pull preflight: %w", r.Name, err)
		}
		if err := e.reconcileSync(r, item.path); err != nil {
			return fmt.Errorf("repository %s pull preflight: %w", r.Name, err)
		}
		if err := clean(item.path); err != nil {
			return fmt.Errorf("repository %s pull preflight: %w", r.Name, err)
		}
		current, branchErr := branch(item.path)
		if branchErr != nil {
			return fmt.Errorf("repository %s pull preflight: configured trunk %s is not checked out: %w", r.Name, r.Trunk, branchErr)
		}
		if current != r.Trunk {
			return fmt.Errorf("repository %s pull preflight: checked out branch is %s, expected configured trunk %s", r.Name, current, r.Trunk)
		}
		repositories[index].local, err = head(item.path)
		if err != nil {
			return fmt.Errorf("repository %s pull preflight: resolve HEAD: %w", r.Name, err)
		}
		e.logOperationOutcome("pull", r.Name, item.path, "", "preflight complete", LogSuccess, " for trunk %s at %s", r.Trunk, abbreviateRevision(repositories[index].local))
	}
	for index := range repositories {
		item, r := &repositories[index], repositories[index].repository
		refspec := "+refs/heads/" + r.Trunk + ":refs/remotes/origin/" + r.Trunk
		if _, err := git(item.path, "fetch", "--no-tags", "origin", refspec); err != nil {
			return fmt.Errorf("repository %s pull: fetch remote trunk %s: %w", r.Name, r.Trunk, err)
		}
		item.remote, err = git(item.path, "rev-parse", "refs/remotes/origin/"+r.Trunk)
		if err != nil {
			return fmt.Errorf("repository %s pull: resolve fetched remote trunk %s: %w", r.Name, r.Trunk, err)
		}
		users, usersErr := e.baselineUsers(r, item.path, item.local, item.remote)
		if usersErr != nil {
			return fmt.Errorf("repository %s pull: inspect active Change baselines: %w", r.Name, usersErr)
		}
		if len(users) > 0 {
			return fmt.Errorf("repository %s pull would rewrite commits recorded as baselines by active Changes: %s; refresh, merge, or drop those Changes before retrying", r.Name, strings.Join(users, ", "))
		}
	}
	for _, item := range repositories {
		r := item.repository
		if _, err := git(item.path, "rebase", "refs/remotes/origin/"+r.Trunk); err != nil {
			return fmt.Errorf("repository %s pull rebase interrupted: %w; resolve or abort the rebase in %s before retrying", r.Name, err, item.path)
		}
		after, err := head(item.path)
		if err != nil {
			return fmt.Errorf("repository %s pull: resolve updated HEAD: %w", r.Name, err)
		}
		semantic, outcome := LogChanged, "rebased"
		if after == item.local {
			semantic, outcome = LogSuccess, "already current"
		}
		e.logOperationOutcome("pull", r.Name, item.path, "", outcome, semantic, " trunk %s %s -> %s", r.Trunk, abbreviateRevision(item.local), abbreviateRevision(after))
	}
	return nil
}
