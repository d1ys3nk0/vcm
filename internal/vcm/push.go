package vcm

import (
	"fmt"
	"path/filepath"
	"strings"
)

type pushRepository struct {
	repository  Repository
	path        string
	local       string
	destination string
}

func (e *Engine) pushRepositories() ([]pushRepository, error) {
	ordered, err := e.Config.Order()
	if err != nil {
		return nil, err
	}
	repositories := make([]pushRepository, 0, len(ordered)+1)
	for _, repository := range ordered {
		repositories = append(repositories, pushRepository{repository: repository, path: filepath.Join(e.Root, repository.Path)})
	}
	rootURL, err := git(e.Root, "remote", "get-url", "origin")
	if err != nil {
		return nil, fmt.Errorf("repository root: resolve origin: %w", err)
	}
	repositories = append(repositories, pushRepository{
		repository: Repository{Name: "root", URL: rootURL, Trunk: e.Config.Root.Trunk},
		path:       e.Root,
	})
	return repositories, nil
}

func (e *Engine) preflightPush(repository *pushRepository) error {
	r := repository.repository
	if err := validateOrigin(repository.path, r); err != nil {
		return fmt.Errorf("repository %s push preflight: %w", r.Name, err)
	}
	if err := e.reconcileSync(r, repository.path); err != nil {
		return fmt.Errorf("repository %s push preflight: %w", r.Name, err)
	}
	if err := clean(repository.path); err != nil {
		return fmt.Errorf("repository %s push preflight: %w", r.Name, err)
	}
	currentBranch, err := branch(repository.path)
	if err != nil {
		return fmt.Errorf("repository %s push preflight: configured trunk %s is not checked out: %w", r.Name, r.Trunk, err)
	}
	if currentBranch != r.Trunk {
		return fmt.Errorf("repository %s push preflight: checked out branch is %s, expected configured trunk %s", r.Name, currentBranch, r.Trunk)
	}
	local, err := git(repository.path, "rev-parse", "refs/heads/"+r.Trunk)
	if err != nil {
		return fmt.Errorf("repository %s push preflight: resolve local trunk %s: %w", r.Name, r.Trunk, err)
	}
	current, err := head(repository.path)
	if err != nil {
		return fmt.Errorf("repository %s push preflight: resolve HEAD: %w", r.Name, err)
	}
	if current != local {
		return fmt.Errorf("repository %s push preflight: HEAD does not match local trunk %s", r.Name, r.Trunk)
	}
	destination, err := pushDestination(repository.path)
	if err != nil {
		return err
	}
	repository.destination = destination
	if _, err = git(repository.path, "fetch", "--no-tags", "--", destination, "refs/heads/"+r.Trunk); err != nil {
		return fmt.Errorf("repository %s push preflight: fetch remote trunk %s: %w", r.Name, r.Trunk, err)
	}
	remote, err := git(repository.path, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return fmt.Errorf("repository %s push preflight: resolve fetched remote trunk %s: %w", r.Name, r.Trunk, err)
	}
	if !ancestor(repository.path, remote, local) {
		return fmt.Errorf("repository %s push preflight: remote trunk %s is not an ancestor of the local trunk; local trunk is behind or divergent", r.Name, r.Trunk)
	}
	repository.local = local
	e.logOperationOutcome("push", r.Name, repository.path, "", "preflight complete", LogSuccess, " for trunk %s at %s", r.Trunk, abbreviateRevision(local))
	return nil
}

// Push validates every canonical checkout before publishing any repository.
// Children are published in dependency order and the workspace root last.
func (e *Engine) Push() error {
	repositories, err := e.pushRepositories()
	if err != nil {
		return err
	}
	publicationError := func(index int, published bool, err error) error {
		result := &PublicationError{Err: failure("preflight", repositories[index].repository.Name, err), Blocked: repositories[index].repository.Name, Completed: []string{}, Pending: []string{}}
		for i, r := range repositories {
			if i == index {
				continue
			}
			if published && i < index {
				result.Completed = append(result.Completed, r.repository.Name)
			} else {
				result.Pending = append(result.Pending, r.repository.Name)
			}
		}
		return result
	}
	for i := range repositories {
		if err := e.preflightPush(&repositories[i]); err != nil {
			return publicationError(i, false, err)
		}
	}
	for index, repository := range repositories {
		r := repository.repository
		if err := validateOrigin(repository.path, r); err != nil {
			return publicationError(index, true, err)
		}
		current, err := localBaseline(&RepoState{Repository: r, Origin: repository.path})
		if err != nil {
			return publicationError(index, true, err)
		}
		if current != repository.local {
			return publicationError(index, true, fmt.Errorf("repository %s: local trunk changed after publication preflight", r.Name))
		}
		destination, err := pushDestination(repository.path)
		if err != nil {
			return publicationError(index, true, err)
		}
		if destination != repository.destination {
			return publicationError(index, true, fmt.Errorf("repository %s: publication destination changed after preflight", r.Name))
		}
		refspec := repository.local + ":refs/heads/" + r.Trunk
		if _, err := git(repository.path, "push", "--", repository.destination, refspec); err != nil {
			return publicationError(index, true, failure("external_command", r.Name, fmt.Errorf("repository %s push: %w; earlier repositories may already be published, inspect remotes and retry", r.Name, err)))
		}
		remoteTrackingRef := "refs/remotes/origin/" + r.Trunk
		if _, err := git(repository.path, "update-ref", remoteTrackingRef, repository.local); err != nil {
			return publicationError(index, true, fmt.Errorf("repository %s: trunk was published but cached origin/%s could not be updated: %w", r.Name, r.Trunk, err))
		}
		e.logOperationOutcome("push", r.Name, repository.path, "", "pushed", LogChanged, " trunk %s at %s", r.Trunk, abbreviateRevision(repository.local))
	}
	return nil
}

func pushDestination(path string) (string, error) {
	value, err := git(path, "remote", "get-url", "--push", "--all", "origin")
	if err != nil {
		return "", err
	}
	urls := strings.Split(value, "\n")
	if len(urls) != 1 || urls[0] == "" {
		return "", fmt.Errorf("publication requires exactly one origin push URL")
	}
	return urls[0], nil
}
