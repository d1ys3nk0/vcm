package vcm

import (
	"fmt"
	"strings"
)

type changePublication struct {
	state            *RepoState
	sha, destination string
}

func (e *Engine) publicationRepositories(m *Manifest) ([]changePublication, error) {
	if m.State != "ready" {
		return nil, fmt.Errorf("Change must be ready to publish")
	}
	if err := e.ensureCurrentSelection(m); err != nil {
		return nil, err
	}
	result := []changePublication{}
	for i := 1; i <= len(m.Repositories); i++ {
		r := &m.Repositories[i%len(m.Repositories)]
		destination, err := pushDestination(r.Path)
		if err != nil {
			return nil, err
		}
		result = append(result, changePublication{state: r, destination: destination})
	}
	return result, nil
}
func (e *Engine) publicationLocal(m *Manifest, p *changePublication) error {
	r := p.state
	if !r.Owned || r.Removed {
		return fmt.Errorf("repository %s has no owned worktree", r.Repository.Name)
	}
	if err := validateOrigin(r.Origin, r.Repository); err != nil {
		return err
	}
	if err := e.owned(m, r); err != nil {
		return err
	}
	if err := clean(r.Path); err != nil {
		return err
	}
	sha, err := head(r.Path)
	if err != nil {
		return err
	}
	if p.sha != "" && p.sha != sha {
		return fmt.Errorf("repository %s changed after publication preflight", r.Repository.Name)
	}
	if p.destination != "" {
		destination, err := pushDestination(r.Path)
		if err != nil {
			return err
		}
		if destination != p.destination {
			return fmt.Errorf("repository %s: push destination changed after preflight", r.Repository.Name)
		}
	}
	p.sha = sha
	return nil
}
func publicationFailure(repos []changePublication, index int, published bool, err error) error {
	result := &PublicationError{Err: err, Blocked: repos[index].state.Repository.Name, Completed: []string{}, Pending: []string{}}
	for i, p := range repos {
		if i == index {
			continue
		}
		if published && i < index {
			result.Completed = append(result.Completed, p.state.Repository.Name)
		} else {
			result.Pending = append(result.Pending, p.state.Repository.Name)
		}
	}
	return result
}

// Publish publishes frozen Change revisions after preflighting every destination.
func (e *Engine) Publish(m *Manifest) error {
	repos, err := e.publicationRepositories(m)
	if err != nil {
		return err
	}
	ref := "refs/heads/" + m.Tag
	for i := range repos {
		p := &repos[i]
		if err = e.publicationLocal(m, p); err != nil {
			return publicationFailure(repos, i, false, err)
		}
		remote, er := git(p.state.Path, "ls-remote", "--refs", "--", p.destination, ref)
		if er != nil {
			return publicationFailure(repos, i, false, er)
		}
		if remote != "" {
			if _, er = git(p.state.Path, "fetch", "--no-tags", "--", p.destination, ref); er != nil {
				return publicationFailure(repos, i, false, er)
			}
			sha, er := git(p.state.Path, "rev-parse", "FETCH_HEAD")
			if er != nil {
				return publicationFailure(repos, i, false, er)
			}
			if !ancestor(p.state.Path, sha, p.sha) {
				return publicationFailure(repos, i, false, fmt.Errorf("repository %s: remote Change branch is not an ancestor of requested publication", p.state.Repository.Name))
			}
		}
	}
	for i := range repos {
		p := &repos[i]
		if err = e.publicationLocal(m, p); err != nil {
			return publicationFailure(repos, i, true, err)
		}
		if _, err = git(p.state.Path, "push", "--", p.destination, p.sha+":"+ref); err != nil {
			return publicationFailure(repos, i, true, err)
		}
		if m.Published == nil {
			m.Published = map[string]string{}
		}
		m.Published[p.state.Repository.Name] = p.sha
		if err = e.store.save(m); err != nil {
			result := &PublicationError{Err: err, Completed: []string{}, Pending: []string{}}
			for j, row := range repos {
				if j <= i {
					result.Completed = append(result.Completed, row.state.Repository.Name)
				} else {
					result.Pending = append(result.Pending, row.state.Repository.Name)
				}
			}
			return result
		}
		e.logOperationOutcome("publish", p.state.Repository.Name, p.state.Path, "", "published", LogChanged, " %s at %s", ref, p.sha)
	}
	return nil
}
func (e *Engine) PublishPlan(m *Manifest) OperationPlan {
	plan := OperationPlan{Command: "publish", DryRun: true, Tag: m.Tag, Workspace: m.Workspace, Steps: []PlanStep{}, Blockers: []string{}, Unverified: []string{"remote availability and current remote revisions"}}
	repos, err := e.publicationRepositories(m)
	if err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
		return plan
	}
	for i := range repos {
		p := &repos[i]
		if err = e.publicationLocal(m, p); err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
		}
		plan.Steps = append(plan.Steps, PlanStep{Phase: "publish", Repository: p.state.Repository.Name, Target: "refs/heads/" + m.Tag, Effect: strings.TrimSpace("publish exact revision " + p.sha), Checkpoint: "pending"})
	}
	return plan
}
