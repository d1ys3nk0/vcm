package vcm

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var conventionalSubject = regexp.MustCompile(`^[a-z][a-z0-9-]*(\([[:alnum:]_.-]+\))?!?: [^\r\n]+$`)

func ValidateMergeMessage(message string) error {
	if message == "" || strings.ContainsAny(message, "\r\n") || !conventionalSubject.MatchString(message) {
		return fmt.Errorf("--message must be one single-line Conventional Commit subject")
	}
	return nil
}

func squashCommitMessage(path, subject, source, target string) (string, error) {
	history, err := gitRaw(path, "log", "--topo-order", "--abbrev=7", "--format=%h%x00%s", source, "--not", target)
	if err != nil {
		return "", err
	}
	var message strings.Builder
	message.WriteString(subject)
	message.WriteString("\n\nCommits:")
	for _, entry := range strings.Split(strings.TrimSuffix(history, "\n"), "\n") {
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "\x00", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("unexpected Git history entry")
		}
		message.WriteString("\n- ")
		message.WriteString(parts[0])
		message.WriteByte(' ')
		message.WriteString(parts[1])
	}
	return message.String(), nil
}

func (e *Engine) Drop(m *Manifest) error {
	if err := e.ensureCurrentSelection(m); err != nil {
		return err
	}
	if m.State == "dropped" {
		return nil
	}
	if m.State == "integrated" || m.State == "merge-finalizing" || m.State == "merging" {
		return fmt.Errorf("workspace merge is incomplete; retry merge %s", workspaceSelector(m))
	}
	if m.State == "refreshing" {
		return fmt.Errorf("workspace refresh is incomplete; run vcm refresh from %s", m.Workspace)
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
		if err := e.cleanupSafety("drop", m, r); err != nil {
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
					if err = e.backup("drop", r.Path, r.Repository.Name, m); err != nil {
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
	root := &m.Repositories[0]
	if root.Owned && !root.Removed {
		if err := e.hooks(m, root, HookDropBefore); err != nil {
			return err
		}
		if !e.Force {
			if err := clean(root.Path); err != nil {
				return err
			}
			actual, err := head(root.Path)
			if err != nil {
				return err
			}
			expected := root.Source
			if expected == "" {
				expected = root.Base
			}
			if actual != expected {
				return fmt.Errorf("repository root: workspace drop hook changed committed source; preserve work before cleanup")
			}
		}
	}
	for i := len(m.Repositories) - 1; i >= 1; i-- {
		r := &m.Repositories[i]
		if !r.Owned {
			continue
		}
		if !r.Removed {
			if r.Intent == "remove" {
				if _, statErr := os.Lstat(r.Path); os.IsNotExist(statErr) {
					if err := e.removeOne("drop", m, r); err != nil {
						return err
					}
					goto afterRemoval
				}
			}
			if err := e.hooks(m, r, HookDropBefore); err != nil {
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
			if err := e.removeOne("drop", m, r); err != nil {
				return err
			}
		}
	afterRemoval:
		if err := e.hooksAt(m, r, HookDropAfter, r.Origin, false); err != nil {
			return err
		}
	}
	if root.Owned && root.CheckoutCustody != "external" && !root.Removed {
		if err := e.removeOne("drop", m, root); err != nil {
			return err
		}
	}
	if root.CheckoutCustody == "external" && !root.Removed {
		if err := e.releaseExternalRoot(m, root); err != nil {
			return err
		}
	}
	if root.Owned {
		if err := e.hooksAt(m, root, HookDropAfter, root.Origin, false); err != nil {
			return err
		}
	}
	m.State = "dropped"
	return e.store.save(m)
}

func (e *Engine) removeAll(operation string, m *Manifest) error {
	for i := len(m.Repositories) - 1; i >= 0; i-- {
		r := &m.Repositories[i]
		if r.Owned && r.CheckoutCustody != "external" && !r.Removed {
			if err := e.removeOne(operation, m, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) releaseExternalRoot(m *Manifest, root *RepoState) error {
	if _, err := os.Stat(root.Path); err != nil {
		return fmt.Errorf("external root worktree disappeared before release; restore it or use the previous VCM binary to recover")
	}
	if err := e.owned(m, root); err != nil {
		return err
	}
	if err := clean(root.Path); err != nil {
		return err
	}
	if root.BranchCustody == "vcm" {
		if _, err := git(root.Path, "checkout", "--detach"); err != nil {
			return fmt.Errorf("release external root branch: %w", err)
		}
		if _, err := git(root.Origin, "branch", "-D", workspaceName(m)); err != nil {
			return fmt.Errorf("release external root branch: %w", err)
		}
	}
	root.Removed = true
	root.Intent = ""
	return e.store.save(m)
}

func (e *Engine) removeOne(operation string, m *Manifest, r *RepoState) (errOut error) {
	defer classify(&errOut, "preflight", r.Repository.Name)
	if _, err := os.Lstat(r.Path); err == nil {
		if err = e.owned(m, r); err != nil {
			return err
		}
		if err = e.cleanupSafety(operation, m, r); err != nil {
			return err
		}
		if e.Force {
			if err = e.backup(operation, r.Path, r.Repository.Name, m); err != nil {
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
			return fmt.Errorf("repository %s: hook changed source; preserve or merge its commit before cleanup", r.Repository.Name)
		}
		if e.Force {
			r.Source = revision
		}
		if err = e.checkDropTarget(r); err != nil {
			return err
		}
		r.Intent = "remove"
		if err = e.store.save(m); err != nil {
			return err
		}
		args := []string{"worktree", "remove"}
		if e.Force || operation == "merge" {
			args = append(args, "--force")
		}
		args = append(args, r.Path)
		if _, err = git(r.Origin, args...); err != nil {
			return fmt.Errorf("repository %s cleanup: %w; inspect preserved path and retry", r.Repository.Name, err)
		}
	} else if !os.IsNotExist(err) || r.Intent != "remove" {
		return fmt.Errorf("repository %s: owned worktree disappeared unexpectedly", r.Repository.Name)
	}
	ref := "refs/heads/" + workspaceName(m)
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
	r.Removed, r.Intent = true, ""
	if err := e.store.save(m); err != nil {
		return err
	}
	e.logOperationOutcome(operation, r.Repository.Name, r.Path, "", "removed", LogChanged, " managed worktree and branch %s", workspaceName(m))
	return nil
}

func preserveIgnoredOrigin(origin, incoming string) error {
	ignored, err := gitRaw(origin, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil || ignored == "" {
		return err
	}
	tracked, err := gitRaw(origin, "ls-tree", "-r", "--name-only", "-z", incoming)
	if err != nil {
		return err
	}
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

func (e *Engine) cleanupSafety(operation string, m *Manifest, r *RepoState) error {
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
	if operation != "merge" && !e.Force {
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

func (e *Engine) checkDropTarget(r *RepoState) error {
	if !r.Merged {
		return nil
	}
	target, err := localBaseline(r)
	if err != nil {
		return err
	}
	if target != r.Target && !ancestor(r.Origin, r.Target, target) {
		return fmt.Errorf("repository %s: merged target changed before cleanup; restore recorded target before retry", r.Repository.Name)
	}
	return nil
}

func (e *Engine) checkPendingSource(m *Manifest, r *RepoState) error {
	current, err := head(r.Path)
	if err != nil {
		return err
	}
	if current == r.Source {
		return nil
	}
	for key, h := range m.Hooks {
		if strings.HasPrefix(key, r.Repository.Name+"/") && h.Status == "failed" {
			r.Source = current
			return e.store.save(m)
		}
	}
	return fmt.Errorf("repository %s: source changed after merge gate or another repository hook", r.Repository.Name)
}

func (e *Engine) Plan(command string, m *Manifest) OperationPlan {
	paths := []string{e.Root}
	for _, r := range e.Config.Children {
		paths = append(paths, filepath.Join(e.Root, r.Path))
	}
	if m != nil {
		paths = nil
		for _, r := range m.Repositories {
			paths = append(paths, r.Path)
		}
	}
	skipped := []string{}
	for _, phase := range []string{HookMergeBefore, HookMergeAfter} {
		if e.SkipMergeHooks[phase] {
			skipped = append(skipped, phase)
		}
	}
	force := e.Force

	return e.enrichPlan(OperationPlan{Command: command, DryRun: true, Force: force, IgnoreHookFailures: e.IgnoreHookFailures, DeletesIgnoredContent: command == "merge", SkippedHookPhases: skipped, SkipHookGitHooks: e.SkipHookGitHooks, WorkspaceID: func() string {
		if m != nil {
			return workspaceSelector(m)
		}
		return ""
	}(), Workspace: func() string {
		if m != nil {
			return m.Workspace
		}
		return e.Root
	}(), Resources: paths}, m)
}

func (e *Engine) preflight(m *Manifest) (errOut error) {
	defer classify(&errOut, "preflight", "")
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if r.Removed || r.Merged {
			continue
		}
		if !r.Owned {
			return fmt.Errorf("repository %s is not owned; finish creation first", r.Repository.Name)
		}
		if err := e.owned(m, r); err != nil {
			return err
		}
		if err := clean(r.Path); err != nil {
			return failure("preflight", r.Repository.Name, err)
		}
		target, err := head(r.Origin)
		if err != nil {
			return err
		}
		prepared := false
		if r.Intent == "merge" {
			if target == r.MergeCommit {
				r.Target, r.Merged, r.Intent = target, true, ""
				if err := e.store.save(m); err != nil {
					return err
				}
				e.logOperationOutcome("merge", r.Repository.Name, r.Origin, "", "applied", LogChanged, " trunk %s %s -> %s", r.Repository.Trunk, abbreviateRevision(r.TargetBefore), abbreviateRevision(r.Target))
				continue
			}
			if target == r.TargetBefore {
				index, indexErr := git(r.Origin, "write-tree")
				worktreeDiff, diffErr := git(r.Origin, "diff", "--name-only")
				prepared = indexErr == nil && diffErr == nil && index == r.MergeTree && worktreeDiff == ""
			}
		}
		if !prepared {
			if err := clean(r.Origin); err != nil {
				return err
			}
		}
		b, err := branch(r.Origin)
		if err != nil || b != r.Repository.Trunk {
			return fmt.Errorf("repository %s: origin must be on trunk %s", r.Repository.Name, r.Repository.Trunk)
		}
		if target != r.Base {
			return fmt.Errorf("repository %s: target advanced from recorded base; run vcm refresh %s", r.Repository.Name, workspaceSelector(m))
		}
	}
	return nil
}

func (e *Engine) freezeMerge(m *Manifest) error {
	unchanged := []*RepoState{}
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if r.Merged || r.Intent == "merge" {
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
		if target != r.Base {
			return fmt.Errorf("repository %s: target changed after merge gate; run vcm refresh %s", r.Repository.Name, workspaceSelector(m))
		}
		if r.Source != "" && source != r.Source {
			return fmt.Errorf("repository %s: source changed after merge gate", r.Repository.Name)
		}
		tree, err := git(r.Origin, "merge-tree", "--write-tree", target, source)
		if err != nil {
			return failure("conflict", r.Repository.Name, fmt.Errorf("repository %s conflict after merge gate: %w", r.Repository.Name, err))
		}
		tree = strings.Split(tree, "\n")[0]
		r.Source, r.TargetBefore, r.MergeTree = source, target, tree
		existing, err := git(r.Origin, "rev-parse", target+"^{tree}")
		if err != nil {
			return err
		}
		if tree == existing {
			r.Target, r.Merged, r.Intent = target, true, ""
			unchanged = append(unchanged, r)
			continue
		}
		message, err := squashCommitMessage(r.Origin, m.MergeMessage, source, target)
		if err != nil {
			return fmt.Errorf("repository %s commit history: %w", r.Repository.Name, err)
		}
		commit, err := git(r.Origin, "commit-tree", tree, "-p", target, "-m", message)
		if err != nil {
			return err
		}
		r.MergeCommit, r.Intent = commit, "merge"
	}
	if err := e.store.save(m); err != nil {
		return err
	}
	for _, r := range unchanged {
		e.logOperationOutcome("merge", r.Repository.Name, r.Origin, fmt.Sprintf("trunk %s ", r.Repository.Trunk), "unchanged", LogSuccess, " at %s", abbreviateRevision(r.Target))
	}
	return nil
}

func (e *Engine) applyMerge(m *Manifest, r *RepoState) (errOut error) {
	defer classify(&errOut, "preflight", r.Repository.Name)
	if r.Merged {
		return nil
	}
	if r.Intent != "merge" || r.MergeCommit == "" {
		return fmt.Errorf("repository %s: missing frozen merge intent", r.Repository.Name)
	}
	source, err := head(r.Path)
	if err != nil {
		return err
	}
	target, err := head(r.Origin)
	if err != nil {
		return err
	}
	if source != r.Source {
		return fmt.Errorf("repository %s: source changed after merge gate", r.Repository.Name)
	}
	if target == r.MergeCommit {
		r.Target, r.Merged, r.Intent = target, true, ""
		if err := e.store.save(m); err != nil {
			return err
		}
		e.logOperationOutcome("merge", r.Repository.Name, r.Origin, "", "applied", LogChanged, " trunk %s %s -> %s", r.Repository.Trunk, abbreviateRevision(r.TargetBefore), abbreviateRevision(r.Target))
		return nil
	}
	if target != r.TargetBefore {
		return fmt.Errorf("repository %s: target changed after merge gate", r.Repository.Name)
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
	r.Target, r.Merged, r.Intent = r.MergeCommit, true, ""
	if err := e.store.save(m); err != nil {
		return err
	}
	e.logOperationOutcome("merge", r.Repository.Name, r.Origin, "", "applied", LogChanged, " trunk %s %s -> %s", r.Repository.Trunk, abbreviateRevision(r.TargetBefore), abbreviateRevision(r.Target))
	return nil
}

func (e *Engine) mergeOne(m *Manifest, r *RepoState) error {
	if r.Intent != "merge" {
		if err := e.freezeMerge(m); err != nil {
			return err
		}
	}
	return e.applyMerge(m, r)
}

func (e *Engine) Merge(m *Manifest, messages ...string) error {
	if e.Keep && !m.Keep && (m.State == StateMerging || m.State == StateFinalizing) {
		return fmt.Errorf("merge already started without --keep; retention choice cannot change during retry")
	}
	if m.State == "integrated" || m.Keep && m.State == "merge-finalizing" {
		return fmt.Errorf("workspace integrated; run vcm cleanup %s", workspaceSelector(m))
	}
	if m.State == "expanding" || m.State == "restoring" {
		return fmt.Errorf("workspace %s operation is incomplete", m.State)
	}
	if err := e.ensureCurrentSelection(m); err != nil {
		return err
	}
	if m.State == "dropped" {
		return nil
	}
	if m.State == "creating" {
		return fmt.Errorf("workspace creation incomplete; retry create %s", workspaceSelector(m))
	}
	if m.State == "refreshing" {
		return fmt.Errorf("workspace refresh incomplete; run vcm refresh from %s", m.Workspace)
	}
	if m.State == "dropping" {
		return fmt.Errorf("workspace is being dropped; retry drop %s", workspaceSelector(m))
	}
	provided := ""
	if len(messages) > 0 {
		provided = messages[0]
	}
	effective := m.MergeMessage
	if effective == "" {
		effective = provided
		if effective == "" {
			effective = "chore(vcm): integrate workspace"
		}
		if err := ValidateMergeMessage(effective); err != nil {
			return err
		}
	} else if provided != "" && provided != m.MergeMessage {
		return fmt.Errorf("merge already started with message %q; retries must reuse it", m.MergeMessage)
	}
	if m.MergeMessage == "" && (m.State == "merging" || m.State == "merge-finalizing") {
		m.MergeMessage = effective
		if err := e.store.save(m); err != nil {
			return err
		}
	}
	if m.State != "merging" && m.State != "merge-finalizing" {
		if err := e.preflight(m); err != nil {
			return err
		}
		for i := range m.Repositories {
			revision, err := head(m.Repositories[i].Path)
			if err != nil {
				return err
			}
			m.Repositories[i].Source = revision
		}
		m.MergeMessage = effective
		m.Keep = m.Keep || e.Keep
		m.State = "merging"
		if err := e.store.save(m); err != nil {
			return err
		}
	}
	root := &m.Repositories[0]
	if m.State == "merging" {
		if err := e.checkPendingSource(m, root); err != nil {
			return err
		}
		if err := e.hooks(m, root, HookMergeBefore); err != nil {
			return err
		}
		for i := 1; i < len(m.Repositories); i++ {
			if err := e.checkPendingSource(m, &m.Repositories[i]); err != nil {
				return err
			}
			if err := e.hooks(m, &m.Repositories[i], HookMergeBefore); err != nil {
				return err
			}
		}
		if err := e.preflight(m); err != nil {
			return err
		}
		if err := e.freezeMerge(m); err != nil {
			return err
		}
		for i := 1; i < len(m.Repositories); i++ {
			if err := e.applyMerge(m, &m.Repositories[i]); err != nil {
				return err
			}
		}
		if err := e.applyMerge(m, root); err != nil {
			return err
		}
		m.State = "merge-finalizing"
		if m.Keep {
			m.State = "integrated"
		}
		if err := e.store.save(m); err != nil {
			return err
		}
	}
	if m.State == "integrated" {
		return nil
	}
	return e.finalizeMerge(m)
}

func (e *Engine) Cleanup(m *Manifest) error {
	if err := e.ensureCurrentSelection(m); err != nil {
		return err
	}
	if m.State != "integrated" && !(m.State == "merge-finalizing" && m.Keep) {
		return fmt.Errorf("managed workspace has no retained integration awaiting cleanup")
	}
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if err := validateOrigin(r.Origin, r.Repository); err != nil {
			return err
		}
		if _, err := localBaseline(r); err != nil {
			return err
		}
		if err := e.checkDropTarget(r); err != nil {
			return err
		}
		if !r.Removed {
			if r.Intent == "remove" {
				if _, err := os.Stat(r.Path); os.IsNotExist(err) {
					continue
				}
			}
			if err := e.owned(m, r); err != nil {
				return err
			}
			if err := clean(r.Path); err != nil {
				return err
			}
			source, err := head(r.Path)
			if err != nil {
				return err
			}
			if source != r.Source {
				return fmt.Errorf("repository %s source changed after integration", r.Repository.Name)
			}
		}
	}
	m.State = "merge-finalizing"
	if err := e.store.save(m); err != nil {
		return err
	}
	return e.finalizeMerge(m)
}

func (e *Engine) finalizeMerge(m *Manifest) error {
	root := &m.Repositories[0]
	if err := e.removeAll("merge", m); err != nil {
		return err
	}
	if root.CheckoutCustody == "external" && !root.Removed {
		if err := e.releaseExternalRoot(m, root); err != nil {
			return err
		}
	}
	for i := 1; i < len(m.Repositories); i++ {
		if m.Repositories[i].Owned {
			if err := e.hooksAt(m, &m.Repositories[i], HookMergeAfter, m.Repositories[i].Origin, false); err != nil {
				return err
			}
		}
	}
	if root.Owned {
		if err := e.hooksAt(m, root, HookMergeAfter, root.Origin, false); err != nil {
			return err
		}
	}
	m.State = "dropped"
	return e.store.save(m)
}
