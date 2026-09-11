package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/d1ys3nk0/vcm/internal/vcm"
)

type validateResult struct {
	Valid     bool   `json:"valid"`
	Workspace string `json:"workspace"`
}

type commandResult struct {
	Command   string `json:"command"`
	Complete  bool   `json:"complete"`
	Workspace string `json:"workspace"`
}

type createRepositoryResult struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Base string `json:"base"`
}

type createResult struct {
	Tag          string                   `json:"tag"`
	Slug         string                   `json:"slug"`
	Workspace    string                   `json:"workspace"`
	State        string                   `json:"state"`
	Repositories []createRepositoryResult `json:"repositories"`
}

type listResult struct {
	Tag             string    `json:"tag"`
	Slug            string    `json:"slug"`
	Workspace       string    `json:"workspace"`
	State           string    `json:"state"`
	CreatedAt       time.Time `json:"created_at"`
	RepositoryCount int       `json:"repository_count"`
	MergedCount     int       `json:"merged_count"`
	RemovedCount    int       `json:"removed_count"`
	current         bool
}

type statusRepositoryResult struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	Status        string `json:"status"`
	Head          string `json:"head,omitempty"`
	Target        string `json:"target,omitempty"`
	Intent        string `json:"intent,omitempty"`
	Error         string `json:"error,omitempty"`
	TargetChanged bool   `json:"target_changed,omitempty"`
}

type hookResult struct {
	Repository string `json:"repository"`
	Phase      string `json:"phase"`
	ID         string `json:"id"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

type pendingSyncResult struct {
	Path   string                  `json:"path"`
	Intent pendingSyncIntentResult `json:"intent"`
	Error  string                  `json:"error,omitempty"`
}

type pendingSyncIntentResult struct {
	TrunkBefore string `json:"trunk_before,omitempty"`
	Repository  string `json:"repository,omitempty"`
	Path        string `json:"path,omitempty"`
	Before      string `json:"before,omitempty"`
	Target      string `json:"target,omitempty"`
	Operation   string `json:"operation,omitempty"`
}

type statusResult struct {
	Tag               string                   `json:"tag"`
	Slug              string                   `json:"slug"`
	Workspace         string                   `json:"workspace"`
	State             string                   `json:"state"`
	CreatedAt         time.Time                `json:"created_at"`
	Repositories      []statusRepositoryResult `json:"repositories"`
	Hooks             []hookResult             `json:"hooks"`
	Backups           []string                 `json:"backups"`
	RecoveryDirectory string                   `json:"recovery_directory"`
	PendingSync       []pendingSyncResult      `json:"pending_sync"`
}

type mergeRepositoryResult struct {
	Name   string `json:"name"`
	Merged bool   `json:"merged"`
	Source string `json:"source,omitempty"`
	Target string `json:"target,omitempty"`
}

type mergeResult struct {
	Tag          string                  `json:"tag"`
	State        string                  `json:"state"`
	Repositories []mergeRepositoryResult `json:"repositories"`
	Backups      []string                `json:"backups"`
}

type refreshRepositoryResult struct {
	Name   string `json:"name"`
	Base   string `json:"base"`
	Source string `json:"source"`
}
type refreshResult struct {
	Tag          string                    `json:"tag"`
	State        string                    `json:"state"`
	Repositories []refreshRepositoryResult `json:"repositories"`
}

type dropRepositoryResult struct {
	Name    string `json:"name"`
	Removed bool   `json:"removed"`
}

type dropResult struct {
	Tag          string                 `json:"tag"`
	State        string                 `json:"state"`
	Repositories []dropRepositoryResult `json:"repositories"`
	Backups      []string               `json:"backups"`
}

type versionResult struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type dryRunResult struct {
	Command               string   `json:"command"`
	DryRun                bool     `json:"dry_run"`
	Force                 bool     `json:"force"`
	DeletesIgnoredContent bool     `json:"deletes_ignored_content"`
	SkippedHookPhases     []string `json:"skipped_hook_phases"`
	SkipGitHooks          bool     `json:"skip_git_hooks"`
	Tag                   string   `json:"tag,omitempty"`
	Workspace             string   `json:"workspace,omitempty"`
	Resources             []string `json:"resources"`
}

type errorResult struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func createdAt(tag string) time.Time {
	created, _ := time.Parse("060102150405", tag[:12])
	return created.UTC()
}

func newCreateResult(m *vcm.Manifest) createResult {
	repositories := make([]createRepositoryResult, 0, len(m.Repositories))
	for _, repository := range m.Repositories {
		repositories = append(repositories, createRepositoryResult{Name: repository.Repository.Name, Path: repository.Path, Base: repository.Base})
	}
	return createResult{Tag: m.Tag, Slug: m.Slug, Workspace: m.Workspace, State: m.State, Repositories: repositories}
}

func newListResults(manifests []*vcm.Manifest, cwd string) []listResult {
	results := make([]listResult, 0, len(manifests))
	for _, manifest := range manifests {
		merged, removed := 0, 0
		for _, repository := range manifest.Repositories {
			if repository.Merged {
				merged++
			}
			if repository.Removed {
				removed++
			}
		}
		current := cwd == manifest.Workspace || strings.HasPrefix(cwd, manifest.Workspace+string(filepath.Separator))
		results = append(results, listResult{
			Tag: manifest.Tag, Slug: manifest.Slug, Workspace: manifest.Workspace, State: manifest.State,
			CreatedAt: createdAt(manifest.Tag), RepositoryCount: len(manifest.Repositories), MergedCount: merged, RemovedCount: removed, current: current,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].CreatedAt.Equal(results[j].CreatedAt) {
			return results[i].Tag > results[j].Tag
		}
		return results[i].CreatedAt.After(results[j].CreatedAt)
	})
	return results
}

func repositoryStatus(repository vcm.RepositoryStatus) (string, string) {
	errors := []string{}
	for _, detail := range []string{repository.Error, repository.DirtyOrInterrupted, repository.OwnershipError} {
		if detail != "" {
			errors = append(errors, detail)
		}
	}
	switch {
	case repository.Removed:
		return "removed", strings.Join(errors, "; ")
	case len(errors) > 0:
		if repository.DirtyOrInterrupted != "" && repository.Error == "" && repository.OwnershipError == "" {
			return "dirty", strings.Join(errors, "; ")
		}
		return "error", strings.Join(errors, "; ")
	case repository.TargetChanged:
		return "target changed", ""
	case repository.Merged:
		return "merged", ""
	case repository.Intent != "":
		return repository.Intent, ""
	default:
		return "clean", ""
	}
}

func newStatusResult(m *vcm.Manifest, report vcm.StatusReport) statusResult {
	repositories := make([]statusRepositoryResult, 0, len(report.Repositories))
	for _, repository := range report.Repositories {
		state, detail := repositoryStatus(repository)
		repositories = append(repositories, statusRepositoryResult{
			Name: repository.Name, Path: repository.Path, Status: state, Head: repository.Source, Target: repository.Target,
			Intent: repository.Intent, Error: detail, TargetChanged: repository.TargetChanged,
		})
	}
	hooks := make([]hookResult, 0, len(m.Hooks))
	for key, hook := range m.Hooks {
		parts := strings.SplitN(key, "/", 3)
		hooks = append(hooks, hookResult{Repository: parts[0], Phase: parts[1], ID: parts[2], Status: hook.Status, Error: hook.Error})
	}
	sort.Slice(hooks, func(i, j int) bool {
		left := hooks[i].Repository + "/" + hooks[i].Phase + "/" + hooks[i].ID
		right := hooks[j].Repository + "/" + hooks[j].Phase + "/" + hooks[j].ID
		return left < right
	})
	pending := make([]pendingSyncResult, 0, len(report.PendingSync))
	for _, item := range report.PendingSync {
		pending = append(pending, pendingSyncResult{Path: item.Path, Intent: pendingSyncIntentResult{
			TrunkBefore: item.Intent.TrunkBefore,
			Repository:  item.Intent.Repository,
			Path:        item.Intent.Path,
			Before:      item.Intent.Before,
			Target:      item.Intent.Target,
			Operation:   item.Intent.Operation,
		}, Error: item.Error})
	}
	return statusResult{
		Tag: m.Tag, Slug: m.Slug, Workspace: m.Workspace, State: m.State, CreatedAt: createdAt(m.Tag), Repositories: repositories,
		Hooks: hooks, Backups: append([]string{}, m.Backups...), RecoveryDirectory: report.RecoveryDirectory, PendingSync: pending,
	}
}

func newMergeResult(m *vcm.Manifest) mergeResult {
	repositories := make([]mergeRepositoryResult, 0, len(m.Repositories))
	for _, repository := range m.Repositories {
		repositories = append(repositories, mergeRepositoryResult{Name: repository.Repository.Name, Merged: repository.Merged, Source: repository.Source, Target: repository.Target})
	}
	return mergeResult{Tag: m.Tag, State: m.State, Repositories: repositories, Backups: append([]string{}, m.Backups...)}
}

func newRefreshResult(m *vcm.Manifest) refreshResult {
	repositories := make([]refreshRepositoryResult, 0, len(m.Repositories))
	for _, repository := range m.Repositories {
		repositories = append(repositories, refreshRepositoryResult{Name: repository.Repository.Name, Base: repository.Base, Source: repository.Source})
	}
	return refreshResult{Tag: m.Tag, State: m.State, Repositories: repositories}
}

func newDropResult(m *vcm.Manifest) dropResult {
	repositories := make([]dropRepositoryResult, 0, len(m.Repositories))
	for _, repository := range m.Repositories {
		repositories = append(repositories, dropRepositoryResult{Name: repository.Repository.Name, Removed: repository.Removed})
	}
	return dropResult{Tag: m.Tag, State: m.State, Repositories: repositories, Backups: append([]string{}, m.Backups...)}
}

func newDryRunResult(plan vcm.OperationPlan) dryRunResult {
	return dryRunResult{Command: plan.Command, DryRun: plan.DryRun, Force: plan.Force, DeletesIgnoredContent: plan.DeletesIgnoredContent, SkippedHookPhases: append([]string{}, plan.SkippedHookPhases...), SkipGitHooks: plan.SkipGitHooks, Tag: plan.Tag, Workspace: plan.Workspace, Resources: append([]string{}, plan.Resources...)}
}

func renderJSON(out io.Writer, result any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func renderTable(out io.Writer, rows [][]string) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for _, row := range rows {
		if _, err := fmt.Fprintln(w, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return w.Flush()
}

func abbreviated(revision string) string {
	const length = 7
	runes := []rune(revision)
	if len(runes) <= length {
		return revision
	}
	return string(runes[:length])
}

func age(now, created time.Time) string {
	duration := now.Sub(created)
	if duration < 0 || duration < time.Minute {
		return "now"
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm", int(duration/time.Minute))
	}
	if duration < 24*time.Hour {
		return fmt.Sprintf("%dh", int(duration/time.Hour))
	}
	if duration < 30*24*time.Hour {
		return fmt.Sprintf("%dd", int(duration/(24*time.Hour)))
	}
	if duration < 365*24*time.Hour {
		return fmt.Sprintf("%dmo", int(duration/(30*24*time.Hour)))
	}
	return fmt.Sprintf("%dy", int(duration/(365*24*time.Hour)))
}

func commandLabel(command string) string {
	if command == "" {
		return ""
	}
	return strings.ToUpper(command[:1]) + command[1:]
}

func renderHuman(out io.Writer, result any) error {
	switch value := result.(type) {
	case validateResult:
		_, err := fmt.Fprintf(out, "Configuration valid.\nWorkspace: %s\n", value.Workspace)
		return err
	case commandResult:
		_, err := fmt.Fprintf(out, "%s complete.\nWorkspace: %s\n", commandLabel(value.Command), value.Workspace)
		return err
	case vcm.AuditReport:
		repositories := [][]string{{"Repository", "State", "Origin"}}
		for _, repository := range value.Repositories {
			state := "clean"
			if !repository.Clean {
				state = "issues"
			}
			repositories = append(repositories, []string{repository.Name, state, repository.Origin})
		}
		if err := renderTable(out, repositories); err != nil {
			return err
		}
		if len(value.Issues) == 0 {
			_, err := fmt.Fprintln(out, "\nWorkspace clean.")
			return err
		}
		if _, err := fmt.Fprintln(out, "\nFindings:"); err != nil {
			return err
		}
		issues := [][]string{{"Repository", "Kind", "Target", "Detail"}}
		for _, issue := range value.Issues {
			issues = append(issues, []string{issue.Repository, issue.Kind, auditIssueTarget(issue), issue.Detail})
		}
		return renderTable(out, issues)
	case vcm.PruneReport:
		actions := [][]string{{"Repository", "Action", "Target", "Status", "Detail"}}
		for _, action := range value.Actions {
			actions = append(actions, []string{action.Repository, action.Action, action.Target, action.Status, action.Detail})
		}
		if err := renderTable(out, actions); err != nil {
			return err
		}
		if len(value.RemainingIssues) == 0 {
			_, err := fmt.Fprintln(out, "\nPrune complete.")
			return err
		}
		if _, err := fmt.Fprintln(out, "\nRemaining findings:"); err != nil {
			return err
		}
		issues := [][]string{{"Repository", "Kind", "Target", "Detail"}}
		for _, issue := range value.RemainingIssues {
			issues = append(issues, []string{issue.Repository, issue.Kind, auditIssueTarget(issue), issue.Detail})
		}
		return renderTable(out, issues)
	case createResult:
		_, err := fmt.Fprintf(out, "Created Change %s with %d repositories.\nWorkspace: %s\n", value.Tag, len(value.Repositories), value.Workspace)
		return err
	case []listResult:
		if len(value) == 0 {
			_, err := fmt.Fprintln(out, "No Changes.")
			return err
		}
		rows := [][]string{{"", "Change", "State", "Repos", "Age", "Workspace"}}
		now := time.Now().UTC()
		for _, change := range value {
			marker := ""
			if change.current {
				marker = "@"
			}
			repos := fmt.Sprintf("%d/%d merged", change.MergedCount, change.RepositoryCount)
			if change.State == "dropping" || change.State == "dropped" {
				repos = fmt.Sprintf("%d/%d removed", change.RemovedCount, change.RepositoryCount)
			}
			rows = append(rows, []string{marker, change.Tag, change.State, repos, age(now, change.CreatedAt), change.Workspace})
		}
		return renderTable(out, rows)
	case statusResult:
		if _, err := fmt.Fprintf(out, "Change: %s\nState: %s\nCreated: %s\nWorkspace: %s\n\n", value.Tag, value.State, value.CreatedAt.Format(time.RFC3339), value.Workspace); err != nil {
			return err
		}
		repositories := [][]string{{"Repository", "Status", "HEAD", "Target", "Detail"}}
		for _, repository := range value.Repositories {
			repositories = append(repositories, []string{repository.Name, repository.Status, abbreviated(repository.Head), abbreviated(repository.Target), repository.Error})
		}
		if err := renderTable(out, repositories); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(out, "\nHooks:"); err != nil {
			return err
		}
		hooks := [][]string{{"Repository", "Phase", "Hook", "Status", "Error"}}
		for _, hook := range value.Hooks {
			hooks = append(hooks, []string{hook.Repository, hook.Phase, hook.ID, hook.Status, hook.Error})
		}
		if err := renderTable(out, hooks); err != nil {
			return err
		}
		if len(value.Backups) > 0 {
			fmt.Fprintln(out, "\nBackups:")
			for _, backup := range value.Backups {
				fmt.Fprintf(out, "  %s\n", backup)
			}
			fmt.Fprintf(out, "Recovery directory: %s\n", value.RecoveryDirectory)
		}
		if len(value.PendingSync) > 0 {
			fmt.Fprintln(out, "\nPending synchronization:")
			for _, pending := range value.PendingSync {
				detail := pending.Path
				if pending.Error != "" {
					detail += ": " + pending.Error
				}
				fmt.Fprintf(out, "  %s\n", detail)
			}
		}
		return nil
	case mergeResult:
		merged := 0
		for _, repository := range value.Repositories {
			if repository.Merged {
				merged++
			}
		}
		_, err := fmt.Fprintf(out, "Merged Change %s (%d/%d repositories).\n", value.Tag, merged, len(value.Repositories))
		return err
	case refreshResult:
		_, err := fmt.Fprintf(out, "Refreshed Change %s (%d repositories).\n", value.Tag, len(value.Repositories))
		return err
	case dropResult:
		removed := 0
		for _, repository := range value.Repositories {
			if repository.Removed {
				removed++
			}
		}
		_, err := fmt.Fprintf(out, "Dropped Change %s (%d/%d repositories removed, %d recovery backups).\n", value.Tag, removed, len(value.Repositories), len(value.Backups))
		return err
	case versionResult:
		_, err := fmt.Fprintf(out, "vcm %s (%s)\n", value.Version, value.Commit)
		return err
	case dryRunResult:
		if _, err := fmt.Fprintf(out, "Dry run: %s\n", value.Command); err != nil {
			return err
		}
		if value.Command == "sync" || value.Command == "merge" || value.Command == "drop" {
			fmt.Fprintf(out, "Force: %t\n", value.Force)
		}
		if value.Command == "merge" {
			fmt.Fprintf(out, "Delete ignored content: %t\n", value.DeletesIgnoredContent)
			fmt.Fprintf(out, "Skipped hook phases: %s\n", strings.Join(value.SkippedHookPhases, ","))
			fmt.Fprintf(out, "Git hooks suppressed: %t\n", value.SkipGitHooks)
		}
		if value.Tag != "" {
			fmt.Fprintf(out, "Change: %s\n", value.Tag)
		}
		if value.Workspace != "" {
			fmt.Fprintf(out, "Workspace: %s\n", value.Workspace)
		}
		fmt.Fprintln(out, "Resources:")
		for _, resource := range value.Resources {
			fmt.Fprintf(out, "  %s\n", resource)
		}
		return nil
	default:
		return fmt.Errorf("unsupported output type %T", result)
	}
}

func auditIssueTarget(issue vcm.AuditIssue) string {
	if issue.Path != "" && issue.Branch != "" {
		return issue.Path + " (" + issue.Branch + ")"
	}
	if issue.Path != "" {
		return issue.Path
	}
	return issue.Branch
}

func writeError(out io.Writer, jsonOutput bool, err error) {
	if jsonOutput {
		result := errorResult{}
		result.Error.Message = err.Error()
		_ = renderJSON(out, result)
		return
	}
	fmt.Fprintln(out, "vcm:", err)
}
