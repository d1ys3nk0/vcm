package vcm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (e *Engine) preflight(m *Manifest) error {
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if r.Removed {
			continue
		}
		if !r.Owned {
			return fmt.Errorf("repository %s is not owned; finish creation first", r.Repository.Name)
		}
		if err := e.owned(m, r); err != nil {
			return err
		}
		if r.Intent == "merge" {
			actual, err := head(r.Origin)
			if err != nil {
				return err
			}
			if actual == r.TargetBefore {
				idx, err := git(r.Origin, "write-tree")
				if err != nil {
					return err
				}
				if idx == r.MergeTree {
					diff, err := git(r.Origin, "diff", "--name-only")
					if err != nil || diff != "" {
						return fmt.Errorf("repository %s: unexpected worktree changes during merge", r.Repository.Name)
					}
					b, err := branch(r.Origin)
					if err != nil || b != r.Repository.Trunk {
						return fmt.Errorf("repository %s: branch changed during merge", r.Repository.Name)
					}
					source, err := head(r.Path)
					if err != nil || source != r.Source {
						return fmt.Errorf("repository %s: source changed during merge", r.Repository.Name)
					}
					if _, err = git(r.Origin, "update-ref", "refs/heads/"+r.Repository.Trunk, r.MergeCommit, r.TargetBefore); err != nil {
						return err
					}
					actual = r.MergeCommit
				}
			}
			if actual == r.MergeCommit {
				b, err := branch(r.Origin)
				if err != nil || b != r.Repository.Trunk {
					return fmt.Errorf("repository %s: origin branch changed during merge", r.Repository.Name)
				}
				source, err := head(r.Path)
				if err != nil || source != r.Source {
					return fmt.Errorf("repository %s: source changed during merge", r.Repository.Name)
				}
				// A completed ref swap must have the exact prepared index and worktree.
				index, err := git(r.Origin, "write-tree")
				if err != nil {
					return err
				}
				diff, err := git(r.Origin, "diff", "--name-only")
				if err != nil || diff != "" {
					return fmt.Errorf("repository %s: worktree changed during interrupted merge; repair before retry", r.Repository.Name)
				}
				if index != r.MergeTree {
					return fmt.Errorf("repository %s: index changed during interrupted merge", r.Repository.Name)
				}
			}
		}
		if err := clean(r.Path); err != nil {
			return err
		}
		if err := clean(r.Origin); err != nil {
			return err
		}
		b, err := branch(r.Origin)
		if err != nil || b != r.Repository.Trunk {
			return fmt.Errorf("repository %s: origin must be on trunk %s", r.Repository.Name, r.Repository.Trunk)
		}
		source, err := head(r.Path)
		if err != nil {
			return err
		}
		target, err := head(r.Origin)
		if err != nil {
			return err
		}
		if m.State == "merging" && !r.Merged && r.Source != "" && source != r.Source {
			repair := false
			for key, h := range m.Hooks {
				if strings.HasPrefix(key, r.Repository.Name+"/") && h.Status == "failed" {
					repair = true
				}
			}
			if !repair {
				return fmt.Errorf("repository %s: source changed after merge gate; restore recorded source before retry", r.Repository.Name)
			}
		}
		if !ancestor(r.Path, r.Base, source) {
			return fmt.Errorf("repository %s: source no longer contains recorded base", r.Repository.Name)
		}
		if r.Merged {
			if target != r.Target || source != r.Source {
				return fmt.Errorf("repository %s: completed merge source or target changed; restore recorded revisions before retry", r.Repository.Name)
			}
			continue
		}
		if r.Intent == "merge" {
			if source != r.Source {
				return fmt.Errorf("repository %s: source changed during merge intent", r.Repository.Name)
			}
			if target == r.MergeCommit {
				r.Target = target
				r.Merged = true
				r.Intent = ""
				if err = e.store.save(m); err != nil {
					return err
				}
				continue
			}
			if target != r.TargetBefore {
				return fmt.Errorf("repository %s: target changed during merge intent; inspect recorded intent", r.Repository.Name)
			}
		}
		if !ancestor(r.Origin, r.Base, target) {
			return fmt.Errorf("repository %s: target no longer contains recorded base", r.Repository.Name)
		}
		if _, err = git(r.Origin, "merge-tree", "--write-tree", target, source); err != nil {
			return fmt.Errorf("repository %s conflict preflight: %w; resolve source against trunk and retry", r.Repository.Name, err)
		}
	}
	return nil
}
func (e *Engine) mergeOne(m *Manifest, r *RepoState) error {
	if r.Merged {
		return nil
	}
	if err := e.owned(m, r); err != nil {
		return err
	}
	b, err := branch(r.Origin)
	if err != nil || b != r.Repository.Trunk {
		return fmt.Errorf("repository %s: origin trunk changed after hook", r.Repository.Name)
	}
	if err := clean(r.Path); err != nil {
		return err
	}
	if err := clean(r.Origin); err != nil {
		return err
	}
	source, err := head(r.Path)
	if err != nil {
		return err
	}
	target, err := head(r.Origin)
	if err != nil {
		return err
	}
	if !ancestor(r.Path, r.Base, source) || !ancestor(r.Origin, r.Base, target) {
		return fmt.Errorf("repository %s: recorded base changed after hook", r.Repository.Name)
	}
	if r.TargetBefore != "" && target != r.TargetBefore {
		return fmt.Errorf("repository %s: target changed after merge gate", r.Repository.Name)
	}
	if r.Intent != "merge" {
		tree, err := git(r.Origin, "merge-tree", "--write-tree", target, source)
		if err != nil {
			return fmt.Errorf("repository %s conflict after hook: %w", r.Repository.Name, err)
		}
		tree = strings.Split(tree, "\n")[0]
		existing, err := git(r.Origin, "rev-parse", target+"^{tree}")
		if err != nil {
			return err
		}
		r.Source = source
		r.TargetBefore = target
		r.MergeTree = tree
		if tree == existing {
			r.Target = target
			r.Merged = true
			return e.store.save(m)
		}
		summary, err := git(r.Path, "log", "-1", "--format=%s", source)
		if err != nil {
			return err
		}
		message := "feat: merge " + m.Tag + " (" + summary + ")"
		commit, err := git(r.Origin, "commit-tree", tree, "-p", target, "-m", message)
		if err != nil {
			return err
		}
		r.MergeCommit = commit
		r.Intent = "merge"
		if err = e.store.save(m); err != nil {
			return err
		}
	} else if source != r.Source || target != r.TargetBefore {
		return fmt.Errorf("repository %s: pending merge source or target changed", r.Repository.Name)
	}
	if err = preserveIgnoredOrigin(r.Origin, r.MergeCommit); err != nil {
		return err
	}
	if _, err = git(r.Origin, "read-tree", "-u", "-m", r.TargetBefore, r.MergeCommit); err != nil {
		return err
	}
	if _, err = git(r.Origin, "update-ref", "refs/heads/"+r.Repository.Trunk, r.MergeCommit, r.TargetBefore); err != nil {
		return err
	}
	r.Target = r.MergeCommit
	r.Merged = true
	r.Intent = ""
	return e.store.save(m)
}
func (e *Engine) Merge(m *Manifest) error {
	if err := e.ensureCurrentSelection(m); err != nil {
		return err
	}
	if m.State == "dropped" {
		return nil
	}
	if m.State == "creating" {
		return fmt.Errorf("Change creation incomplete; retry create %s", m.Slug)
	}
	if m.State == "dropping" {
		return e.Drop(m)
	}
	if err := e.preflight(m); err != nil {
		return err
	}
	root := &m.Repositories[0]
	if m.State != "merging" {
		for i := range m.Repositories {
			r := &m.Repositories[i]
			h, err := head(r.Path)
			if err != nil {
				return err
			}
			r.Source = h
			target, err := head(r.Origin)
			if err != nil {
				return err
			}
			r.TargetBefore = target
		}
		m.State = "merging"
		if err := e.store.save(m); err != nil {
			return err
		}
	}
	if err := e.hooks(m, root, "pre-merge"); err != nil {
		return err
	}
	for i := 1; i < len(m.Repositories); i++ {
		r := &m.Repositories[i]
		if r.Merged {
			continue
		}
		if err := e.checkPendingSource(m, r); err != nil {
			return err
		}
		if err := e.hooks(m, r, "merge"); err != nil {
			return err
		}
		if err := e.checkCompleted(m); err != nil {
			return err
		}
		if err := e.mergeOne(m, r); err != nil {
			return err
		}
	}
	if !root.Merged {
		if err := e.checkPendingSource(m, root); err != nil {
			return err
		}
		if err := e.hooks(m, root, "post-merge"); err != nil {
			return err
		}
		if err := e.checkCompleted(m); err != nil {
			return err
		}
		if err := e.mergeOne(m, root); err != nil {
			return err
		}
	}
	return e.Drop(m)
}
func (e *Engine) Drop(m *Manifest) error {
	if err := e.ensureCurrentSelection(m); err != nil {
		return err
	}
	if m.State == "dropped" {
		return nil
	}
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if r.Removed || !r.Owned {
			continue
		}
		if r.Intent == "remove" {
			if _, err := os.Lstat(r.Path); os.IsNotExist(err) {
				continue
			}
		}
		if err := e.owned(m, r); err != nil {
			return err
		}
		if err := e.checkDropTarget(r); err != nil {
			return err
		}
		if err := e.cleanupSafety(m, r); err != nil {
			return err
		}
		if !e.Force {
			if err := clean(r.Path); err != nil {
				return err
			}
			h, err := head(r.Path)
			if err != nil {
				return err
			}
			if !r.Merged && h != r.Base {
				return fmt.Errorf("repository %s: unmerged changes; merge first or use --force with recovery backup", r.Repository.Name)
			}
			if r.Merged && h != r.Source {
				return fmt.Errorf("repository %s: source changed after merge", r.Repository.Name)
			}
		}
	}
	if e.Force && len(m.Backups) == 0 {
		for i := range m.Repositories {
			r := &m.Repositories[i]
			if !r.Removed && r.Owned {
				if _, err := os.Lstat(r.Path); err == nil {
					if err = e.backup(r.Path, r.Repository.Name, m); err != nil {
						return err
					}
				}
			}
		}
	}
	m.State = "dropping"
	if err := e.store.save(m); err != nil {
		return err
	}
	if !m.Repositories[0].Removed {
		if err := e.hooks(m, &m.Repositories[0], "drop"); err != nil {
			return err
		}
	}
	// Workspace drop hooks run while all resources exist; validate their effects before removing any checkout.
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if !r.Owned || r.Removed {
			continue
		}
		if r.Intent == "remove" {
			if _, err := os.Lstat(r.Path); os.IsNotExist(err) {
				continue
			}
		}
		if err := e.owned(m, r); err != nil {
			return err
		}
		if err := e.cleanupSafety(m, r); err != nil {
			return err
		}
		if err := e.checkDropTarget(r); err != nil {
			return err
		}
		if !e.Force {
			if err := clean(r.Path); err != nil {
				return err
			}
			actual, err := head(r.Path)
			if err != nil {
				return err
			}
			expected := r.Source
			if expected == "" {
				expected = r.Base
			}
			if actual != expected {
				return fmt.Errorf("repository %s: workspace drop hook changed committed source; preserve work before cleanup", r.Repository.Name)
			}
		}
	}
	for i := len(m.Repositories) - 1; i >= 0; i-- {
		r := &m.Repositories[i]
		if r.Removed || !r.Owned {
			continue
		}
		if _, err := os.Lstat(r.Path); err == nil {
			if i > 0 {
				if err = e.hooks(m, r, "drop"); err != nil {
					return err
				}
			}
			if err = e.owned(m, r); err != nil {
				return err
			}
			if err = e.cleanupSafety(m, r); err != nil {
				return err
			}
			if e.Force {
				if err = e.backup(r.Path, r.Repository.Name, m); err != nil {
					return err
				}
			} else if err = clean(r.Path); err != nil {
				return err
			}
			revision, err := head(r.Path)
			if err != nil {
				return err
			}
			expected := r.Source
			if expected == "" {
				expected = r.Base
			}
			if !e.Force && revision != expected {
				return fmt.Errorf("repository %s: drop hook changed source; preserve or merge its commit before cleanup", r.Repository.Name)
			}
			if e.Force {
				r.Source = revision
			}
			if err := e.checkDropTarget(r); err != nil {
				return err
			}
			r.Intent = "remove"
			if err = e.store.save(m); err != nil {
				return err
			}
			args := []string{"worktree", "remove"}
			if e.Force {
				args = append(args, "--force")
			}
			args = append(args, r.Path)
			if _, err = git(r.Origin, args...); err != nil {
				return fmt.Errorf("repository %s cleanup: %w; inspect preserved path and retry", r.Repository.Name, err)
			}
		} else if !os.IsNotExist(err) || r.Intent != "remove" {
			return fmt.Errorf("repository %s: owned worktree disappeared unexpectedly", r.Repository.Name)
		}
		ref := "refs/heads/" + m.Tag
		revision, err := git(r.Origin, "rev-parse", "--verify", ref)
		if err == nil {
			expected := r.Source
			if expected == "" {
				expected = r.Base
			}
			if revision != expected {
				return fmt.Errorf("repository %s: branch changed before cleanup", r.Repository.Name)
			}
			if _, err = git(r.Origin, "update-ref", "-d", ref, revision); err != nil {
				return err
			}
		}
		r.Removed = true
		r.Intent = ""
		if err = e.store.save(m); err != nil {
			return err
		}
	}
	m.State = "dropped"
	return e.store.save(m)
}

// Git's read-tree can overwrite ignored content, including directory/file
// collisions. Reject those paths before it changes either the index or checkout.
func preserveIgnoredOrigin(origin, incoming string) error {
	ignored, err := gitRaw(origin, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil || ignored == "" {
		return err
	}
	tracked, err := gitRaw(origin, "ls-tree", "-r", "--name-only", "-z", incoming)
	if err != nil {
		return err
	}
	// Index both leaves and directory prefixes so ignored build trees do not
	// require comparing every ignored file with every tracked file.
	paths := map[string]map[string]bool{}
	for _, path := range strings.Split(tracked, "\x00") {
		for prefix := path; prefix != "" && prefix != "."; prefix = filepath.Dir(prefix) {
			key := strings.ToLower(prefix)
			if paths[key] == nil {
				paths[key] = map[string]bool{}
			}
			paths[key][prefix] = paths[key][prefix] || prefix == path
		}
	}
	for _, local := range strings.Split(ignored, "\x00") {
		if local == "" {
			continue
		}
		local = strings.TrimSuffix(local, "/")
		for prefix := local; prefix != "."; prefix = filepath.Dir(prefix) {
			for path, leaf := range paths[strings.ToLower(prefix)] {
				if (prefix == local || leaf) && originPathsCollide(origin, local, path) {
					return fmt.Errorf("ignored origin content %q in %s collides with incoming path %q; preserve it before retry", local, origin, path)
				}
			}
		}
	}
	return nil
}

func originPathsCollide(origin, local, incoming string) bool {
	if incoming == local || strings.HasPrefix(incoming, local+"/") || strings.HasPrefix(local, incoming+"/") {
		return true
	}
	// On case-insensitive filesystems differently spelled paths can overwrite the
	// same resource. Confirm the alias on disk instead of assuming case folding.
	left, right := strings.Split(local, "/"), strings.Split(incoming, "/")
	n := min(len(left), len(right))
	for i := range n {
		if !strings.EqualFold(left[i], right[i]) {
			return false
		}
	}
	a, err := os.Lstat(filepath.Join(origin, filepath.Join(left[:n]...)))
	if err != nil {
		return false
	}
	b, err := os.Lstat(filepath.Join(origin, filepath.Join(right[:n]...)))
	return err == nil && os.SameFile(a, b)
}

// Plan exposes resources without performing Git writes, hooks, or state changes.
func (e *Engine) Plan(command string, m *Manifest) OperationPlan {
	paths := []string{e.Root}
	for _, r := range e.Config.Children {
		paths = append(paths, filepath.Join(e.Root, r.Path))
	}
	if m != nil {
		paths = []string{}
		for _, r := range m.Repositories {
			paths = append(paths, r.Path)
		}
	}
	return OperationPlan{Command: command, DryRun: true, Force: e.Force, Resources: paths}
}

func (e *Engine) checkCompleted(m *Manifest) error {
	for _, r := range m.Repositories {
		if !r.Merged || r.Removed {
			continue
		}
		source, err := head(r.Path)
		if err != nil {
			return err
		}
		target, err := head(r.Origin)
		if err != nil {
			return err
		}
		if source != r.Source || target != r.Target {
			return fmt.Errorf("repository %s: completed merge changed during later hook", r.Repository.Name)
		}
	}
	return nil
}

func (e *Engine) cleanupSafety(m *Manifest, r *RepoState) error {
	managed := map[string]bool{}
	for _, other := range m.Repositories {
		if other.Owned && !other.Removed && strings.HasPrefix(other.Path, r.Path+string(filepath.Separator)) {
			managed[other.Path] = true
		}
	}
	err := filepath.Walk(r.Path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == r.Path {
			return nil
		}
		if managed[p] && info.IsDir() {
			return filepath.SkipDir
		}
		if filepath.Base(p) == ".git" {
			if filepath.Dir(p) == r.Path {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return fmt.Errorf("repository %s: unrelated nested Git repository %s blocks cleanup", r.Repository.Name, filepath.Dir(p))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !e.Force {
		ignored, err := gitRaw(r.Path, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
		if err != nil {
			return err
		}
		for _, rel := range strings.Split(ignored, "\x00") {
			if rel == "" {
				continue
			}
			p := filepath.Join(r.Path, rel)
			owned := false
			for child := range managed {
				if p == child || strings.HasPrefix(p, child+string(filepath.Separator)) {
					owned = true
					break
				}
			}
			if !owned {
				return fmt.Errorf("repository %s: ignored filesystem content %s requires --force recovery backup", r.Repository.Name, rel)
			}
		}
	}
	return nil
}

func (e *Engine) checkPendingSource(m *Manifest, r *RepoState) error {
	current, err := head(r.Path)
	if err != nil {
		return err
	}
	if current != r.Source {
		for key, h := range m.Hooks {
			if strings.HasPrefix(key, r.Repository.Name+"/") && h.Status == "failed" {
				r.Source = current
				return e.store.save(m)
			}
		}
		return fmt.Errorf("repository %s: source changed after merge gate or another repository hook", r.Repository.Name)
	}
	return nil
}

func (e *Engine) checkDropTarget(r *RepoState) error {
	if !r.Merged {
		return nil
	}
	target, err := head(r.Origin)
	if err != nil {
		return err
	}
	if target != r.Target {
		return fmt.Errorf("repository %s: merged target changed before cleanup; restore recorded target before retry", r.Repository.Name)
	}
	return nil
}
