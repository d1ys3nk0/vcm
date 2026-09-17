package vcm

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Lifecycle string
type Intent string

const (
	StateCreating   Lifecycle = "creating"
	StateReady      Lifecycle = "ready"
	StateExpanding  Lifecycle = "expanding"
	StateRestoring  Lifecycle = "restoring"
	StateRefreshing Lifecycle = "refreshing"
	StateMerging    Lifecycle = "merging"
	StateIntegrated Lifecycle = "integrated"
	StateFinalizing Lifecycle = "merge-finalizing"
	StateDropping   Lifecycle = "dropping"
	StateDropped    Lifecycle = "dropped"
	IntentNone      Intent    = ""
	IntentCreate    Intent    = "create"
	IntentRefresh   Intent    = "refresh"
	IntentMerge     Intent    = "merge"
	IntentRemove    Intent    = "remove"
)

func validateVersionFour(p *persistedManifest) error {
	if p.Generation < 0 {
		return fmt.Errorf("invalid operation generation")
	}
	if len(p.Recorded) != len(p.Repositories) {
		return fmt.Errorf("configuration baseline must cover every selected repository")
	}
	seen := map[string]bool{}
	for i, item := range p.Recorded {
		r := item.Repository
		if _, ok := p.Repositories[r.Name]; !ok || seen[r.Name] {
			return fmt.Errorf("invalid recorded repository coverage")
		}
		if i == 0 && r.Name != "root" {
			return fmt.Errorf("recorded root must be first")
		}
		if r.Name != "root" && (r.Path == "" || (filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path || r.Path == "." || r.Path == ".." || strings.HasPrefix(r.Path, "../"))) {
			return fmt.Errorf("invalid recorded repository path")
		}
		if r.URL == "" || !validBranch(r.Trunk) {
			return fmt.Errorf("invalid recorded repository identity")
		}
		for _, dep := range r.DependsOn {
			if !seen[dep] {
				return fmt.Errorf("recorded dependency order is incomplete")
			}
		}
		want := item.Fingerprint
		item.Fingerprint = ""
		if want == "" || fingerprint(item) != want {
			return fmt.Errorf("invalid configuration fingerprint for %s", r.Name)
		}
		seen[r.Name] = true
	}
	if p.State == StateIntegrated {
		if !p.Keep {
			return fmt.Errorf("integrated state requires retained worktrees")
		}
		for _, r := range p.Repositories {
			if !r.Merged || r.Removed || !r.Owned {
				return fmt.Errorf("incomplete retained integration")
			}
		}
	}
	if p.State == StateExpanding && (len(p.Expansion) == 0 || p.Generation == 0) {
		return fmt.Errorf("expansion requires a generation and requested repositories")
	}
	requested := map[string]bool{}
	for _, name := range p.Expansion {
		if name == "root" || !seen[name] || requested[name] {
			return fmt.Errorf("invalid expansion repository")
		}
		requested[name] = true
	}
	added := map[string]bool{}
	for _, name := range p.ExpansionAdded {
		if name == "root" || !seen[name] || !requested[name] || added[name] {
			return fmt.Errorf("invalid newly added expansion repository")
		}
		added[name] = true
	}
	if p.State == StateExpanding && len(added) == 0 {
		return fmt.Errorf("expansion requires recorded new repositories")
	}
	if p.State == StateRestoring && p.RestoreSnapshot == nil {
		return fmt.Errorf("restore journal missing snapshot")
	}
	for name, sha := range p.Published {
		if !seen[name] || !objectPattern.MatchString(sha) {
			return fmt.Errorf("invalid publication checkpoint")
		}
	}
	validateOutcome := func(h HookState) error {
		if h.Generation < 0 || h.Generation > p.Generation {
			return fmt.Errorf("invalid hook generation")
		}
		if h.Definition != "" && (len(h.Definition) != 64 || !objectPattern.MatchString(h.Definition)) {
			return fmt.Errorf("invalid hook definition fingerprint")
		}
		return nil
	}
	for _, h := range p.Hooks {
		if err := validateOutcome(h); err != nil {
			return err
		}
	}
	for key, history := range p.HookHistory {
		if !validHookOutcomeKey(key, p.Repositories) {
			return fmt.Errorf("invalid historical hook key")
		}
		for _, h := range history {
			if err := validateOutcome(h); err != nil {
				return err
			}
			if h.Status != "complete" && h.Status != "failed" {
				return fmt.Errorf("invalid historical hook outcome")
			}
		}
	}
	return nil
}
