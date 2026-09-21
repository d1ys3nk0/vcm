package vcm

import "os"

func (e *Engine) CleanupPlan(m *Manifest) OperationPlan {
	plan := OperationPlan{Command: "cleanup", DryRun: true, WorkspaceID: workspaceSelector(m), Workspace: m.Workspace, DeletesIgnoredContent: true}
	if err := e.ensureCurrentSelection(m); err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
	}
	if m.State != "integrated" && !(m.State == "merge-finalizing" && m.Keep) {
		plan.Blockers = append(plan.Blockers, "managed workspace has no retained integration awaiting cleanup")
	}
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if err := validateOrigin(r.Origin, r.Repository); err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
		}
		if _, err := localBaseline(r); err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
		}
		if err := e.checkDropTarget(r); err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
		}
		if r.Removed {
			continue
		}
		if r.Intent == "remove" {
			if _, err := os.Stat(r.Path); os.IsNotExist(err) {
				continue
			}
		}
		if err := e.owned(m, r); err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
			continue
		}
		if err := clean(r.Path); err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
		}
		source, err := head(r.Path)
		if err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
		} else if source != r.Source {
			plan.Blockers = append(plan.Blockers, "repository "+r.Repository.Name+": source changed after integration")
		}
		if err := e.cleanupSafety("merge", m, r); err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
		}
	}
	for i := len(m.Repositories) - 1; i >= 0; i-- {
		r := m.Repositories[i]
		plan.Steps = append(plan.Steps, PlanStep{Phase: "cleanup", Repository: r.Repository.Name, Target: r.Path, Effect: "remove owned worktree and branch including ignored content", Checkpoint: func() string {
			if r.Removed {
				return "complete"
			}
			return "pending"
		}()})
	}
	for i := 1; i <= len(m.Repositories); i++ {
		r := m.Repositories[i%len(m.Repositories)]
		for _, h := range r.Repository.Hooks[HookMergeAfter] {
			outcome := m.Hooks[r.Repository.Name+"/"+HookMergeAfter+"/"+h.ID]
			state := outcome.Status
			if state == "" {
				state = "pending"
			}
			if state == "running" {
				plan.Blockers = append(plan.Blockers, "hook "+r.Repository.Name+"/"+HookMergeAfter+"/"+h.ID+" interrupted; acknowledge effects before retry")
			}
			plan.Steps = append(plan.Steps, PlanStep{Phase: HookMergeAfter, Repository: r.Repository.Name, Target: r.Origin, Effect: "finalization hook " + h.ID, Checkpoint: state})
			if state != "complete" {
				plan.Unverified = append(plan.Unverified, "hook "+h.ID+" effects and result")
			}
		}
	}
	return plan
}
