package vcm

import (
	"fmt"
	"os"
	"path/filepath"
)

func (e *Engine) enrichPlan(plan OperationPlan, m *Manifest) OperationPlan {
	plan.Steps = []PlanStep{}
	plan.Blockers = []string{}
	plan.Unverified = []string{}
	if err := e.legacyMutationGuard(); err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
	}
	command := plan.Command
	if m != nil && (command == "merge" || command == "drop") && m.State == "dropped" {
		if err := e.ensureCurrentSelection(m); err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
		}
		plan.DeletesIgnoredContent = false
		plan.Steps = append(plan.Steps, PlanStep{Phase: "complete", Target: m.Workspace, Effect: "operation already complete; no changes", Checkpoint: "complete"})
		return plan
	}
	if command == "merge" && m != nil {
		plan.DeletesIgnoredContent = false
		for _, r := range m.Repositories {
			if r.Owned && !r.Removed {
				plan.DeletesIgnoredContent = true
			}
		}
	}
	if command == "fetch" || command == "pull" || command == "push" || command == "bootstrap" {
		plan.Unverified = append(plan.Unverified, "remote availability and current remote revisions")
	}
	var states []RepoState
	if m != nil {
		states = m.Repositories
	} else if command == "bootstrap" {
		ordered, _ := e.Config.Order()
		for _, r := range ordered {
			path := filepath.Join(e.Root, r.Path)
			states = append(states, RepoState{Repository: r, Origin: path, Path: path})
		}
	} else {
		canonical, err := e.canonicalRepositories(command == "push" || command == "pull")
		if err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
			return plan
		}
		for _, r := range canonical {
			states = append(states, RepoState{Repository: r.repository, Origin: r.path, Path: r.path})
		}
	}

	add := func(r RepoState, phase, target, effect, checkpoint string) {
		plan.Steps = append(plan.Steps, PlanStep{phase, r.Repository.Name, target, effect, checkpoint})
	}
	hooks := func(r RepoState, phase, path string) {
		if m != nil {
			if phase == HookMergeBefore && m.State == "merge-finalizing" || phase == HookCreateBefore && r.Owned || phase == HookDropBefore && (!r.Owned || r.Removed) || phase == HookDropAfter && !r.Owned {
				return
			}
		}
		h, _ := hooksFor(e.Config, r.Repository.Name)
		for _, hook := range h[phase] {
			state := "pending"
			if m != nil {
				if outcome, ok := m.Hooks[r.Repository.Name+"/"+phase+"/"+hook.ID]; ok {
					state = outcome.Status
				}
			}
			if e.SkipMergeHooks[phase] {
				state = "skipped"
			}
			if state == "running" {
				plan.Blockers = append(plan.Blockers, "hook "+r.Repository.Name+"/"+phase+"/"+hook.ID+" was interrupted; inspect effects and use vcm recover before retry")
			}

			effect := "run hook " + hook.ID
			if state == "complete" {
				effect = "retain completed hook " + hook.ID
			} else if state == "skipped" {
				effect = "skip hook " + hook.ID
			}
			add(r, phase, path, effect, state)
			if state != "complete" && state != "skipped" {
				plan.Unverified = append(plan.Unverified, "hook "+r.Repository.Name+"/"+phase+"/"+hook.ID+" effects and result")
			}
		}
	}
	if command == "merge" || command == "refresh" || command == "drop" {
		if m == nil {
			return plan
		}
		if command == "refresh" && m.State != "ready" && m.State != "refreshing" {
			plan.Blockers = append(plan.Blockers, "Change is not active or refreshing")
		}
		if command == "merge" && m.State != "ready" && m.State != "merging" && m.State != "merge-finalizing" {
			plan.Blockers = append(plan.Blockers, "Change is not active or merging")
		}
		if command == "drop" && (m.State == "merging" || m.State == "merge-finalizing" || m.State == "refreshing") {
			plan.Blockers = append(plan.Blockers, "finish the interrupted operation before dropping this Change")
		}
		if err := e.ensureCurrentSelection(m); err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
		}
		for i := range states {
			r := &states[i]
			if r.Removed {
				continue
			}
			if !r.Owned && command != "drop" {
				plan.Blockers = append(plan.Blockers, "repository "+r.Repository.Name+": finish creation before this operation")
			}
			if r.Owned {
				if err := e.owned(m, r); err != nil {
					plan.Blockers = append(plan.Blockers, err.Error())
				}
				if command != "drop" || !e.Force {
					if err := clean(r.Path); err != nil {
						plan.Blockers = append(plan.Blockers, err.Error())
					}
				}
			}
			if command == "drop" && r.Owned {
				if err := e.checkDropTarget(r); err != nil {
					plan.Blockers = append(plan.Blockers, err.Error())
				}
				if err := e.cleanupSafety("drop", m, r); err != nil {
					plan.Blockers = append(plan.Blockers, err.Error())
				}
				if !e.Force {
					h, err := head(r.Path)
					if err == nil && (!r.Merged && h != r.Base || r.Merged && h != r.Source) {
						plan.Blockers = append(plan.Blockers, "repository "+r.Repository.Name+": unmerged or changed committed content requires --force")
					}
				}
			}
			if command == "merge" && !r.Merged && r.Intent == "" {
				if _, err := localBaseline(r); err != nil {
					plan.Blockers = append(plan.Blockers, err.Error())
				}
				actual, err := head(r.Origin)
				if err == nil && actual != r.Base {
					plan.Blockers = append(plan.Blockers, fmt.Sprintf("repository %s: local trunk advanced; run vcm refresh %s", r.Repository.Name, m.Tag))
				}
			}
		}
	}
	switch command {
	case "create":
		if m == nil {
			return plan
		}
		fresh := true
		for _, r := range states {
			if r.Base != "" || r.Owned || r.Intent != "" {
				fresh = false
			}
		}
		if fresh {
			if err := e.preflightCreate(m); err != nil {
				plan.Blockers = append(plan.Blockers, err.Error())
			}
		} else {
			for i := range states {
				r := &states[i]
				if r.Owned {
					if err := e.owned(m, r); err != nil {
						plan.Blockers = append(plan.Blockers, err.Error())
					}
				} else if _, err := localBaseline(r); err != nil {
					plan.Blockers = append(plan.Blockers, err.Error())
				}
			}
		}
		for _, r := range states {
			checkpoint := "pending"
			if r.Base != "" {
				checkpoint = "complete"
			}
			add(r, "baseline", r.Origin, "record local trunk "+r.Repository.Trunk, checkpoint)
		}
		hooks(states[0], HookCreateBefore, states[0].Origin)
		for i, r := range states {
			if i > 0 {
				hooks(r, HookCreateBefore, r.Origin)
			}
			checkpoint := "pending"
			if r.Owned {
				checkpoint = "complete"
			}
			add(r, "create", r.Path, "create branch "+m.Tag+" and worktree from local trunk", checkpoint)
			if i > 0 {
				hooks(r, HookCreateAfter, r.Path)
			}
		}
		hooks(states[0], HookCreateAfter, states[0].Path)
	case "merge":
		// Merge gates run root first, followed by dependency-ordered children.
		for _, r := range states {
			hooks(r, HookMergeBefore, r.Path)
		}
		for i := 1; i < len(states); i++ {
			r := states[i]
			state := "pending"
			if r.Merged {
				state = "complete"
			}
			add(r, "integration", r.Origin, "squash into local trunk "+r.Repository.Trunk, state)
		}
		r := states[0]
		state := "pending"
		if r.Merged {
			state = "complete"
		}
		add(r, "integration", r.Origin, "squash into local trunk "+r.Repository.Trunk, state)
		for i := len(states) - 1; i >= 0; i-- {
			r := states[i]
			state := "pending"
			if r.Removed {
				state = "complete"
			}
			effect := "remove owned branch and worktree including ignored content, without backup"
			if r.Removed {
				effect = "retain completed cleanup"
			}
			add(r, "cleanup", r.Path, effect, state)
		}
		for i := 1; i < len(states); i++ {
			hooks(states[i], HookMergeAfter, states[i].Origin)
		}
		hooks(states[0], HookMergeAfter, states[0].Origin)
	case "drop":
		hooks(states[0], HookDropBefore, states[0].Path)
		for i := len(states) - 1; i >= 0; i-- {
			r := states[i]
			if i > 0 {
				hooks(r, HookDropBefore, r.Path)
			}
			effect := "remove owned resources; refuse dirty, ignored or unmerged content"
			if e.Force {
				effect = "back up content, then remove owned resources"
			}
			state := "pending"
			if r.Removed {
				state = "complete"
				effect = "retain completed cleanup"
			}
			if !r.Owned {
				continue
			}
			add(r, "cleanup", r.Path, effect, state)
			hooks(r, HookDropAfter, r.Origin)
		}

	case "refresh":
		ordered := append(append([]RepoState{}, states[1:]...), states[0])
		for _, r := range ordered {
			add(r, "refresh", r.Path, "merge local trunk "+r.Repository.Trunk+" into Change", "pending")
		}
	default:
		for _, r := range states {
			if command == "bootstrap" {
				if _, err := os.Stat(r.Origin); os.IsNotExist(err) {
					add(r, "clone", r.Origin, "clone configured origin at "+r.Repository.Trunk, "pending")
					continue
				}
			}
			if err := validateOrigin(r.Origin, r.Repository); err != nil {
				plan.Blockers = append(plan.Blockers, err.Error())
			}
			if command == "pull" || command == "push" {
				if _, err := localBaseline(&r); err != nil {
					plan.Blockers = append(plan.Blockers, err.Error())
				}
			}
			effect := map[string]string{"bootstrap": "validate existing origin", "fetch": "fetch remote trunk into cached origin/" + r.Repository.Trunk, "pull": "fetch and rebase local trunk " + r.Repository.Trunk, "push": "fetch, validate, and publish trunk " + r.Repository.Trunk}[command]
			if command == "push" {
				effect = "fetch remote trunk and validate publication"
			}
			if command == "pull" {
				effect = "fetch remote trunk and validate rebase"
			}
			add(r, command, r.Origin, effect, "pending")
		}
		if command == "push" || command == "pull" {
			for _, r := range states {
				effect := "publish local trunk " + r.Repository.Trunk
				if command == "pull" {
					effect = "rebase local trunk " + r.Repository.Trunk + " on fetched remote trunk"
				}
				add(r, command, r.Origin, effect, "pending")
			}
		}
	}
	return plan
}
func (e *Engine) createPlanManifest(slug, only, except string) (*Manifest, error) {
	selected, err := e.selectedRepositories(only, except)
	if err != nil {
		return nil, err
	}
	tag := newTag(slug)
	path := changeWorkspace(e.Root, tag)
	url, err := git(e.Root, "remote", "get-url", "origin")
	if err != nil {
		return nil, err
	}
	m := &Manifest{Version: 3, Tag: tag, Slug: slug, Origin: e.Root, Workspace: path, Config: e.Config, State: "creating", Hooks: map[string]HookState{}}
	m.Repositories = append(m.Repositories, RepoState{Repository: Repository{Name: "root", URL: url, Trunk: e.Config.Root.Trunk}, Origin: e.Root, Path: path})
	for _, r := range selected {
		m.Repositories = append(m.Repositories, RepoState{Repository: r, Origin: filepath.Join(e.Root, r.Path), Path: filepath.Join(path, r.Path)})
	}
	return m, nil
}
