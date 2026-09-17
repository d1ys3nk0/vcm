package vcm

import (
	"fmt"
	"path/filepath"
)

type canonicalRepository struct {
	repository Repository
	path       string
}

func (e *Engine) canonicalRepositories(rootLast bool) ([]canonicalRepository, error) {
	rootURL, err := git(e.Root, "remote", "get-url", "origin")
	if err != nil {
		return nil, fmt.Errorf("repository root: resolve origin: %w", err)
	}
	root := canonicalRepository{
		repository: Repository{Name: "root", URL: rootURL, Trunk: e.Config.Root.Trunk},
		path:       e.Root,
	}
	children := e.Config.Children
	if rootLast {
		ordered, orderErr := e.Config.Order()
		if orderErr != nil {
			return nil, orderErr
		}
		children = ordered
	}
	repositories := make([]canonicalRepository, 0, len(children)+1)
	if !rootLast {
		repositories = append(repositories, root)
	}
	for _, repository := range children {
		repositories = append(repositories, canonicalRepository{
			repository: repository,
			path:       filepath.Join(e.Root, repository.Path),
		})
	}
	if rootLast {
		repositories = append(repositories, root)
	}
	return repositories, nil
}
