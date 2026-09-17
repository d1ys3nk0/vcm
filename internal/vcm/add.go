package vcm

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

func (e *Engine) expansion(m *Manifest, only string) ([]RepoState, []string, error) {
	if err := e.ensureCurrentSelection(m); err != nil {
		return nil, nil, err
	}
	if m.State != "ready" && m.State != "expanding" {
		return nil, nil, fmt.Errorf("Change must be ready or an interrupted expansion")
	}
	if only == "" {
		return nil, nil, fmt.Errorf("add requires --only")
	}
	names, err := parseSelectionList("only", only)
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(names)
	if m.State == "expanding" {
		if !reflect.DeepEqual(names, m.Expansion) {
			return nil, nil, fmt.Errorf("retry expansion with the originally requested --only repositories")
		}
		for i := range m.Repositories {
			r := &m.Repositories[i]
			if r.Owned || r.Intent == "create" {
				if _, err := os.Stat(r.Path); err == nil {
					if err := e.owned(m, r); err != nil {
						return nil, nil, err
					}
					if err := clean(r.Path); err != nil {
						return nil, nil, err
					}
					continue
				}
				if r.Owned {
					return nil, nil, fmt.Errorf("repository %s owned worktree missing", r.Repository.Name)
				}
			}
			if err := validateOrigin(r.Origin, r.Repository); err != nil {
				return nil, nil, err
			}
			if _, err := localBaseline(r); err != nil {
				return nil, nil, err
			}
		}
		return nil, names, nil
	}
	selected := selectedNames(m)
	requested := map[string]bool{}
	for _, name := range names {
		requested[name] = true
	}
	ordered, err := e.Config.Order()
	if err != nil {
		return nil, nil, err
	}
	additions := []RepoState{}
	for _, r := range ordered {
		if !requested[r.Name] {
			continue
		}
		delete(requested, r.Name)
		if selected[r.Name] {
			continue
		}
		additions = append(additions, RepoState{Repository: r, Origin: filepath.Join(e.Root, r.Path), Path: filepath.Join(m.Workspace, r.Path)})
		selected[r.Name] = true
	}
	if len(requested) > 0 {
		return nil, nil, fmt.Errorf("unknown repositories in --only")
	}
	for _, r := range additions {
		for _, dep := range r.Repository.DependsOn {
			if !selected[dep] {
				return nil, nil, fmt.Errorf("repository %s depends on unselected repository %s", r.Repository.Name, dep)
			}
		}
	}
	if len(additions) == 0 {
		return additions, names, nil
	}
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if err := e.owned(m, r); err != nil {
			return nil, nil, err
		}
		if err := clean(r.Path); err != nil {
			return nil, nil, err
		}
	}
	preview := *m
	preview.Repositories = additions
	if err := e.preflightCreate(&preview); err != nil {
		return nil, nil, err
	}
	return additions, names, nil
}

func (e *Engine) AddPlan(m *Manifest, only string) OperationPlan {
	plan := OperationPlan{Command: "add", DryRun: true, Tag: m.Tag, Workspace: m.Workspace}
	additions, _, err := e.expansion(m, only)
	if err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
		return plan
	}
	if m.State == StateExpanding {
		selected := map[string]bool{}
		for _, name := range m.ExpansionAdded {
			selected[name] = true
		}
		for _, r := range m.Repositories {
			if selected[r.Repository.Name] {
				additions = append(additions, r)
			}
		}
	}
	hookSteps := func(r RepoState, phase, path string) {
		for _, h := range r.Repository.Hooks[phase] {
			outcome := m.Hooks[r.Repository.Name+"/"+phase+"/"+h.ID]
			state := outcome.Status
			if state == "" {
				state = "pending"
			}
			if r.Repository.Name == "root" && m.State == StateReady {
				state = "pending"
			}
			plan.Steps = append(plan.Steps, PlanStep{Phase: phase, Repository: r.Repository.Name, Target: path, Effect: "hook " + h.ID, Checkpoint: state})
			if state == "running" {
				plan.Blockers = append(plan.Blockers, "hook "+r.Repository.Name+"/"+phase+"/"+h.ID+" interrupted; acknowledge external effects before retry")
			}
			if state != "complete" {
				plan.Unverified = append(plan.Unverified, "hook "+h.ID+" effects and result")
			}
		}
	}
	for _, r := range additions {
		plan.Resources = append(plan.Resources, r.Path)
		hookSteps(r, HookCreateBefore, r.Origin)
		state := "pending"
		if r.Owned {
			state = "complete"
		}
		plan.Steps = append(plan.Steps, PlanStep{Phase: "create", Repository: r.Repository.Name, Target: r.Path, Effect: "create owned worktree", Checkpoint: state})
		hookSteps(r, HookCreateAfter, r.Path)
	}
	if len(additions) > 0 || m.State == StateExpanding {
		hookSteps(m.Repositories[0], HookCreateAfter, m.Workspace)
	}
	return plan
}

func (e *Engine) Add(m *Manifest, only string) error {
	additions, names, err := e.expansion(m, only)
	if err != nil {
		return err
	}
	if m.State == "ready" {
		if len(additions) == 0 {
			return nil
		}
		for _, r := range additions {
			m.ExpansionAdded = append(m.ExpansionAdded, r.Repository.Name)
		}
		m.Repositories = append(m.Repositories, additions...)
		// Preserve dependency order for subsequent integration/publication.
		order, _ := e.Config.Order()
		rank := map[string]int{"root": 0}
		for i, r := range order {
			rank[r.Name] = i + 1
		}
		sort.SliceStable(m.Repositories, func(i, j int) bool {
			return rank[m.Repositories[i].Repository.Name] < rank[m.Repositories[j].Repository.Name]
		})
		m.State = "expanding"
		m.Expansion = names
		m.Generation++
		if m.HookHistory == nil {
			m.HookHistory = map[string][]HookState{}
		}
		for key, outcome := range m.Hooks {
			if strings.HasPrefix(key, "root/"+HookCreateAfter+"/") {
				m.HookHistory[key] = append(m.HookHistory[key], outcome)
				delete(m.Hooks, key)
			}
		}
		m.Recorded = e.recordConfiguration(m)
		if err := e.store.save(m); err != nil {
			return err
		}
	}
	if err := e.resumeCreate(m); err != nil {
		return err
	}
	m.Expansion = nil
	m.ExpansionAdded = nil
	return e.store.save(m)
}
