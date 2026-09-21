package vcm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// RecordedRepository freezes the selected execution contract, not script files.
type RecordedRepository struct {
	Repository  Repository `json:"repository"`
	Hooks       Hooks      `json:"hooks,omitempty"`
	Runners     Runners    `json:"runners"`
	Fingerprint string     `json:"fingerprint"`
}

func fingerprint(value any) string {
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (e *Engine) recordConfiguration(m *Manifest) []RecordedRepository {
	result := make([]RecordedRepository, 0, len(m.Repositories))
	for _, state := range m.Repositories {
		r := state.Repository
		if r.Name == "root" {
			r.Trunk = e.Config.Root.Trunk
			r.Hooks = e.Config.Root.Hooks
			r.URL, _ = git(e.Root, "remote", "get-url", "origin")
		} else {
			for _, current := range e.Config.Children {
				if current.Name == r.Name {
					r = current
					break
				}
			}
		}
		hooks := Hooks(nil)
		if r.Hooks != nil {
			hooks = Hooks{}
			for phase, definitions := range r.Hooks {
				hooks[phase] = append([]Hook(nil), definitions...)
			}
		}
		r.DependsOn = append([]string(nil), r.DependsOn...)
		runners := Runners{}
		for _, phase := range hooks {
			for _, hook := range phase {
				if hook.Shell != "" {
					runners.Shell = e.Config.Runners.Shell
				}
				if hook.Python != "" {
					runners.Python = e.Config.Runners.Python
				}
			}
		}
		r.Hooks = nil
		item := RecordedRepository{Repository: r, Hooks: hooks, Runners: runners}
		item.Fingerprint = fingerprint(item)
		result = append(result, item)
	}
	ordered, _ := e.Config.Order()
	rank := map[string]int{"root": 0}
	for i, r := range ordered {
		rank[r.Name] = i + 1
	}
	sort.SliceStable(result, func(i, j int) bool { return rank[result[i].Repository.Name] < rank[result[j].Repository.Name] })
	return result
}

func (e *Engine) ConfigurationDrift(m *Manifest) []string {
	if m.Version < 4 || len(m.Recorded) == 0 {
		return []string{"configuration baseline adoption required: vcm recover " + workspaceSelector(m) + " --adopt-config"}
	}
	current := e.recordConfiguration(m)
	changes := []string{}
	if len(m.Missing) > 0 {
		changes = append(changes, "selected repositories missing: "+strings.Join(m.Missing, ", "))
	}
	for i, old := range m.Recorded {
		if i >= len(current) || old.Fingerprint != current[i].Fingerprint {
			changes = append(changes, "execution configuration changed: "+old.Repository.Name)
		}
	}
	return changes
}

func (e *Engine) requireConfiguration(m *Manifest) error {
	if m.Version < 4 {
		if m.State != "ready" && m.State != "dropped" {
			return fmt.Errorf("legacy interrupted Change %s must be completed with the previous VCM binary", m.Tag)
		}
	}
	if changes := e.ConfigurationDrift(m); len(changes) > 0 {
		return fmt.Errorf("%s; inspect and explicitly adopt configuration with vcm recover %s --adopt-config", strings.Join(changes, "; "), workspaceSelector(m))
	}
	return nil
}

type ConfigurationAdoption struct {
	Change       string   `json:"change"`
	DryRun       bool     `json:"dry_run"`
	Changes      []string `json:"changes"`
	RetryCommand string   `json:"retry_command,omitempty"`
}

func (e *Engine) AdoptConfiguration(selector, context string) (ConfigurationAdoption, error) {
	result := ConfigurationAdoption{DryRun: e.DryRun}
	action := func() error {
		m, err := e.Select(selector, context)
		if err != nil {
			return err
		}
		result.Change = workspaceSelector(m)
		result.Changes = e.ConfigurationDrift(m)
		if m.Version < 4 && m.State != "ready" {
			return fmt.Errorf("legacy interrupted Change must finish with previous VCM binary before adoption")
		}
		if len(m.Missing) > 0 {
			return fmt.Errorf("restore missing selected repositories before adoption")
		}
		names := selectedNames(m)
		for _, r := range e.Config.Children {
			if names[r.Name] {
				for _, dep := range r.DependsOn {
					if !names[dep] {
						return fmt.Errorf("repository %s depends on unselected repository %s", r.Name, dep)
					}
				}
			}
		}
		next := e.recordConfiguration(m)
		if m.State != "ready" && len(m.Recorded) == len(next) {
			for i, old := range m.Recorded {
				if old.Repository.Name != next[i].Repository.Name {
					return fmt.Errorf("cannot change repository execution order during interrupted operation")
				}
			}
		}

		invalid := map[string]bool{}
		boundaryRepo, boundaryHook := -1, 0
		for recordIndex, old := range m.Recorded {
			var current *RecordedRepository
			for i := range next {
				if next[i].Repository.Name == old.Repository.Name {
					current = &next[i]
				}
			}
			if current == nil {
				return fmt.Errorf("cannot remove selected repository %s", old.Repository.Name)
			}
			a, b := old.Repository, current.Repository
			if a.Name != b.Name || a.Path != b.Path || a.URL != b.URL || a.Trunk != b.Trunk {
				return fmt.Errorf("cannot rebind owned repository %s; restore its recorded configuration", a.Name)
			}
			if m.State != "ready" && !reflect.DeepEqual(a.DependsOn, b.DependsOn) {
				return fmt.Errorf("cannot change dependency order during interrupted operation")
			}
			for phase := range hookPhases {
				if reflect.DeepEqual(old.Hooks[phase], current.Hooks[phase]) && reflect.DeepEqual(phaseRunners(old.Hooks[phase], old.Runners), phaseRunners(current.Hooks[phase], current.Runners)) {
					continue
				}
				first := 0
				for first < len(old.Hooks[phase]) && first < len(current.Hooks[phase]) && reflect.DeepEqual(old.Hooks[phase][first], current.Hooks[phase][first]) && reflect.DeepEqual(phaseRunners(old.Hooks[phase][first:first+1], old.Runners), phaseRunners(current.Hooks[phase][first:first+1], current.Runners)) {
					first++
				}
				if phase == HookMergeBefore && (boundaryRepo < 0 || recordIndex < boundaryRepo) {
					boundaryRepo, boundaryHook = recordIndex, first
				}
				historical := (phase == HookCreateBefore || phase == HookCreateAfter) && m.State != "creating" && m.State != "expanding"
				for key, outcome := range m.Hooks {
					if !strings.HasPrefix(key, a.Name+"/"+phase+"/") {
						continue
					}
					prefix := false
					for _, h := range old.Hooks[phase][:first] {
						if key == a.Name+"/"+phase+"/"+h.ID {
							prefix = true
						}
					}
					if prefix {
						continue
					}
					if outcome.Status == "running" {
						return fmt.Errorf("hook %s is running; acknowledge external effects with --retry-hook before adopting", key)
					}
					if historical {
						continue
					}
					if outcome.Status == "complete" {
						for _, r := range m.Repositories {
							if r.Merged || r.Removed || r.Intent == "merge" || r.Intent == "remove" {
								return fmt.Errorf("hook %s has downstream effects; restore recorded configuration", key)
							}
						}
						if phase == HookCreateBefore {
							for _, r := range m.Repositories {
								if r.Repository.Name == a.Name && r.Owned {
									return fmt.Errorf("hook %s already created resources; restore recorded configuration", key)
								}
							}
						}
					}
					invalid[key] = true
				}
			}
		}
		// Re-run only the changed gate and subsequent gates, preserving completed prefixes.
		if boundaryRepo >= 0 {
			for i, record := range m.Recorded {
				if i < boundaryRepo {
					continue
				}
				for j, h := range record.Hooks[HookMergeBefore] {
					if i == boundaryRepo && j < boundaryHook {
						continue
					}
					key := record.Repository.Name + "/" + HookMergeBefore + "/" + h.ID
					outcome, ok := m.Hooks[key]
					if !ok {
						continue
					}
					if outcome.Status == "running" {
						return fmt.Errorf("hook %s must be acknowledged before adopting", key)
					}
					if outcome.Status == "complete" {
						for _, r := range m.Repositories {
							if r.Merged || r.Removed || r.Intent == IntentMerge || r.Intent == IntentRemove {
								return fmt.Errorf("hook %s has downstream effects; restore recorded configuration", key)
							}
						}
					}
					invalid[key] = true
				}
			}
		}
		resetForRefresh := false
		resetForSource := false
		if m.State == StateMerging {
			for i := range m.Repositories {
				r := &m.Repositories[i]
				target, err := localBaseline(r)
				if err != nil {
					return err
				}
				if target != r.Base {
					resetForRefresh = true
				}
				if r.Owned && !r.Removed {
					if err := e.owned(m, r); err != nil {
						return err
					}
					source, err := head(r.Path)
					if err != nil {
						return err
					}
					if source != r.Source {
						if err := clean(r.Path); err != nil {
							return err
						}
						resetForSource = true
					}
				}

			}
			if resetForRefresh || resetForSource {
				for _, r := range m.Repositories {
					if r.Merged || r.Removed || r.Intent == IntentMerge || r.Intent == IntentRemove {
						return fmt.Errorf("cannot rebase configuration recovery after integration effects")
					}
				}
				if resetForRefresh {
					result.Changes = append(result.Changes, "canonical baselines advanced; refresh then retry merge")
					result.RetryCommand = "vcm refresh " + workspaceSelector(m) + " --workspace " + quoteArgument(m.Origin)
				} else {
					result.Changes = append(result.Changes, "source revisions changed; retry merge with new verification generation")
					result.RetryCommand = "vcm merge " + workspaceSelector(m) + " --workspace " + quoteArgument(m.Origin)
				}
				for key, outcome := range m.Hooks {
					if outcome.Status == "running" {
						return fmt.Errorf("hook %s must be acknowledged before recovery", key)
					}
				}
			}
		}
		if e.DryRun {
			return nil
		}
		if resetForRefresh || resetForSource {
			if m.HookHistory == nil {
				m.HookHistory = map[string][]HookState{}
			}
			for key, outcome := range m.Hooks {
				if strings.Contains(key, "/"+HookMergeBefore+"/") {
					if outcome.Status == "running" {
						return fmt.Errorf("hook %s must be acknowledged before recovery", key)
					}
					invalid[key] = true
				}
			}
			m.State = StateReady
			m.Generation++
		}
		if len(invalid) > 0 && !resetForRefresh && !resetForSource {
			m.Generation++
		}
		if len(invalid) > 0 && m.HookHistory == nil {
			m.HookHistory = map[string][]HookState{}
		}
		for key := range invalid {
			if outcome, ok := m.Hooks[key]; ok {
				m.HookHistory[key] = append(m.HookHistory[key], outcome)
			}
			delete(m.Hooks, key)
		}
		m.Version = 4
		m.Recorded = next
		return e.store.save(m)
	}
	if e.DryRun {
		err := action()
		return result, err
	}
	err := e.Mutate(action)
	return result, err
}

func phaseRunners(hooks []Hook, runners Runners) Runners {
	result := Runners{}
	for _, h := range hooks {
		if h.Shell != "" {
			result.Shell = runners.Shell
		}
		if h.Python != "" {
			result.Python = runners.Python
		}
	}
	return result
}
