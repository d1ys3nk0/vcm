package vcm

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type TreeRepository struct {
	Name           string
	Path           string
	Available      bool
	Branch         string
	Clean          bool
	TrackedChanges int
	UntrackedFiles int
	TreeState      string
	SyncState      string
	SyncTarget     string
	Ahead          int
	Behind         int
	Cached         bool
	Detail         string
}

type TreeReport struct {
	Manifest     *Manifest `json:"-"`
	Workspace    string
	Context      string
	Change       string
	Repositories []TreeRepository
}

func (e *Engine) changeAt(path string) (*Manifest, bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, false, err
	}
	abs = filepath.Clean(abs)
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	manifests, err := e.All()
	if err != nil {
		return nil, false, err
	}
	for _, manifest := range manifests {
		workspace := filepath.Clean(manifest.Workspace)
		if resolved, resolveErr := filepath.EvalSymlinks(workspace); resolveErr == nil {
			workspace = resolved
		}
		if abs == workspace || strings.HasPrefix(abs, workspace+string(filepath.Separator)) {
			return manifest, true, nil
		}
	}
	return nil, false, nil
}

func worktreeCounts(path string) (int, int, error) {
	output, err := gitRaw(path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=no")
	if err != nil {
		return 0, 0, err
	}
	tracked := map[string]struct{}{}
	untracked := 0
	entries := strings.Split(output, "\x00")
	for index := 0; index < len(entries); index++ {
		entry := entries[index]
		if entry == "" {
			continue
		}
		if len(entry) < 3 {
			return 0, 0, fmt.Errorf("unexpected Git status entry")
		}
		status := entry[:2]
		path := entry[3:]
		switch status {
		case "??":
			untracked++
		case "!!":
			continue
		default:
			tracked[path] = struct{}{}
			if strings.ContainsAny(status, "RC") {
				index++ // porcelain -z emits the original rename/copy path next.
			}
		}
	}
	return len(tracked), untracked, nil
}

func divergence(path, target string) (int, int, error) {
	output, err := git(path, "rev-list", "--left-right", "--count", "HEAD..."+target)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(output)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected Git divergence output %q", output)
	}
	ahead, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, err
	}
	behind, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, err
	}
	return ahead, behind, nil
}

func divergenceState(ahead, behind int) string {
	switch {
	case ahead > 0 && behind > 0:
		return "diverged"
	case ahead > 0:
		return "ahead"
	case behind > 0:
		return "behind"
	default:
		return "current"
	}
}

func inspectTreeRepository(item TreeRepository, targetRef string) TreeRepository {
	if _, err := os.Stat(item.Path); err != nil {
		item.TreeState, item.SyncState = "missing", "unavailable"
		if !os.IsNotExist(err) {
			item.Detail = err.Error()
		}
		return item
	}
	item.Available = true
	tracked, untracked, err := worktreeCounts(item.Path)
	if err != nil {
		item.TreeState, item.SyncState, item.Detail = "error", "error", err.Error()
		return item
	}
	item.TrackedChanges, item.UntrackedFiles = tracked, untracked
	item.Clean = tracked == 0 && untracked == 0
	if item.Clean {
		item.TreeState = "clean"
	} else {
		item.TreeState = "changed"
	}
	if item.Clean {
		if cleanErr := clean(item.Path); cleanErr != nil {
			item.TreeState = "error"
			item.Detail = cleanErr.Error()
		}
	}
	item.Branch, err = branch(item.Path)
	if err != nil {
		item.SyncState, item.Detail = "error", err.Error()
		return item
	}
	if _, err = git(item.Path, "rev-parse", "--verify", "--quiet", targetRef); err != nil {
		item.SyncState = "target missing"
		if item.Cached {
			item.Detail = "cached remote target is missing; run vcm fetch"
		} else {
			item.Detail = "configured local base branch is missing"
		}
		return item
	}
	item.Ahead, item.Behind, err = divergence(item.Path, targetRef)
	if err != nil {
		item.SyncState, item.Detail = "error", err.Error()
		return item
	}
	item.SyncState = divergenceState(item.Ahead, item.Behind)
	return item
}

// Tree reports worktree cleanliness and local synchronization state without
// fetching or changing refs, indexes, or working trees.
func (e *Engine) Tree(cwd string) (TreeReport, error) {
	manifest, inChange, err := e.changeAt(cwd)
	if err != nil {
		return TreeReport{}, err
	}
	if !inChange {
		manifest = nil
	}
	return e.TreeManifest(manifest)
}

// TreeManifest inspects an already loaded Change without reloading the store.
// A nil manifest inspects the canonical workspace.
func (e *Engine) TreeManifest(manifest *Manifest) (TreeReport, error) {
	inChange := manifest != nil
	report := TreeReport{Manifest: manifest, Workspace: e.Root, Context: "base"}
	if inChange {
		report.Workspace = manifest.Workspace
		report.Context = "change"
		report.Change = manifest.Tag
	}

	selected := map[string]RepoState{}
	if inChange {
		for _, repository := range manifest.Repositories {
			selected[repository.Repository.Name] = repository
		}
	}
	appendRepository := func(name, relativePath, trunk string) {
		path := e.Root
		branchTarget := "refs/remotes/origin/"
		cached := true
		if relativePath != "" {
			path = filepath.Join(e.Root, relativePath)
		}
		if inChange {
			path = manifest.Workspace
			if relativePath != "" {
				path = filepath.Join(manifest.Workspace, relativePath)
			}
			branchTarget = "refs/heads/"
			cached = false
			state, ok := selected[name]
			if !ok || state.Removed || !state.Owned {
				label := "missing"
				if !ok {
					label = "not selected"
				} else if state.Removed {
					label = "removed"
				}
				report.Repositories = append(report.Repositories, TreeRepository{Name: name, Path: path, TreeState: label, SyncState: label})
				return
			}
			path = state.Path
			if _, statErr := os.Stat(path); statErr == nil {
				if ownershipErr := e.owned(manifest, &state); ownershipErr != nil {
					report.Repositories = append(report.Repositories, TreeRepository{Name: name, Path: path, Available: true, TreeState: "error", SyncState: "error", Detail: ownershipErr.Error()})
					return
				}
			}
		}
		target := branchTarget + trunk
		displayTarget := strings.TrimPrefix(target, "refs/remotes/")
		displayTarget = strings.TrimPrefix(displayTarget, "refs/heads/")
		item := TreeRepository{Name: name, Path: path, SyncTarget: displayTarget, Cached: cached}
		if !inChange {
			if current, branchErr := branch(path); branchErr == nil {
				target = "refs/remotes/origin/" + current
				item.SyncTarget = "origin/" + current
			}
		}
		report.Repositories = append(report.Repositories, inspectTreeRepository(item, target))
	}
	appendRepository("root", "", e.Config.Root.Trunk)
	for _, repository := range e.Config.Children {
		appendRepository(repository.Name, repository.Path, repository.Trunk)
	}
	if inChange {
		for _, name := range manifest.Missing {
			report.Repositories = append(report.Repositories, TreeRepository{Name: name, TreeState: "error", SyncState: "error", Detail: "selected repository is absent from current configuration"})
		}
	}
	return report, nil
}
