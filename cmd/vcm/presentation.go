package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

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

type treeRepositoryResult struct {
	Name           string  `json:"name"`
	Path           string  `json:"path"`
	Available      bool    `json:"available"`
	Branch         *string `json:"branch,omitempty"`
	Clean          *bool   `json:"clean,omitempty"`
	TrackedChanges *int    `json:"tracked_changes,omitempty"`
	UntrackedFiles *int    `json:"untracked_files,omitempty"`
	SyncState      *string `json:"sync_state,omitempty"`
	SyncTarget     *string `json:"sync_target,omitempty"`
	Ahead          *int    `json:"ahead,omitempty"`
	Behind         *int    `json:"behind,omitempty"`
	Cached         *bool   `json:"cached,omitempty"`
	Detail         string  `json:"detail,omitempty"`
	TreeState      string  `json:"working_tree"`
}

type treeResult struct {
	Workspace    string                 `json:"workspace"`
	Context      string                 `json:"context"`
	Change       string                 `json:"change,omitempty"`
	Repositories []treeRepositoryResult `json:"repositories"`
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
	Merged         bool   `json:"merged"`
	Removed        bool   `json:"removed"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	Status         string `json:"status"`
	Head           string `json:"head,omitempty"`
	Target         string `json:"target,omitempty"`
	RecordedTarget string `json:"recorded_target,omitempty"`
	RecoveryState  string `json:"recovery_state,omitempty"`
	Intent         string `json:"intent,omitempty"`
	Error          string `json:"error,omitempty"`
	TargetChanged  bool   `json:"target_changed,omitempty"`
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
	IgnoreHookFailures    bool           `json:"ignore_hook_failures,omitempty"`
	Steps                 []vcm.PlanStep `json:"steps"`
	Blockers              []string       `json:"blockers"`
	Unverified            []string       `json:"unverified"`
	Command               string         `json:"command"`
	DryRun                bool           `json:"dry_run"`
	Force                 bool           `json:"force,omitempty"`
	DeletesIgnoredContent bool           `json:"deletes_ignored_content"`
	SkippedHookPhases     []string       `json:"skipped_hook_phases"`
	SkipHookGitHooks      bool           `json:"skip_hook_git_hooks"`
	Tag                   string         `json:"tag,omitempty"`
	Workspace             string         `json:"workspace,omitempty"`
	Resources             []string       `json:"resources"`
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
			Name: repository.Name, Path: repository.Path, Status: state, Merged: repository.Merged, Removed: repository.Removed, Head: repository.Source, Target: repository.Target,
			RecordedTarget: repository.RecordedTarget, RecoveryState: repository.RecoveryState,
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

func newTreeResult(report vcm.TreeReport) treeResult {
	result := treeResult{Workspace: report.Workspace, Context: report.Context, Change: report.Change}
	for _, repository := range report.Repositories {
		item := treeRepositoryResult{Name: repository.Name, Path: repository.Path, Available: repository.Available, Detail: repository.Detail, TreeState: repository.TreeState}
		if repository.Available {
			item.Branch = &repository.Branch
			if repository.TreeState != "error" {
				item.Clean = &repository.Clean
				item.TrackedChanges = &repository.TrackedChanges
				item.UntrackedFiles = &repository.UntrackedFiles
			}
			item.SyncState = &repository.SyncState
			item.SyncTarget = &repository.SyncTarget
			if repository.SyncState != "error" && repository.SyncState != "target missing" {
				item.Ahead = &repository.Ahead
				item.Behind = &repository.Behind
			}
			item.Cached = &repository.Cached
		}
		result.Repositories = append(result.Repositories, item)
	}
	return result
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
	return dryRunResult{Steps: plan.Steps, Blockers: plan.Blockers, Unverified: plan.Unverified, Command: plan.Command, DryRun: plan.DryRun, Force: plan.Force, IgnoreHookFailures: plan.IgnoreHookFailures, DeletesIgnoredContent: plan.DeletesIgnoredContent, SkippedHookPhases: append([]string{}, plan.SkippedHookPhases...), SkipHookGitHooks: plan.SkipHookGitHooks, Tag: plan.Tag, Workspace: plan.Workspace, Resources: append([]string{}, plan.Resources...)}
}

func renderJSON(out io.Writer, result any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func renderTable(out io.Writer, rows [][]string) error {
	widths := []int{}
	for _, row := range rows {
		for column, cell := range row {
			if column == len(row)-1 {
				continue
			}
			for len(widths) <= column {
				widths = append(widths, 0)
			}
			if width := visibleWidth(cell); width > widths[column] {
				widths[column] = width
			}
		}
	}
	for _, row := range rows {
		for column, cell := range row {
			if _, err := fmt.Fprint(out, cell); err != nil {
				return err
			}
			if column < len(row)-1 {
				if _, err := fmt.Fprint(out, strings.Repeat(" ", widths[column]-visibleWidth(cell)+2)); err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}
	return nil
}

func visibleWidth(text string) int {
	width := 0
	for len(text) > 0 {
		if strings.HasPrefix(text, "\x1b[") {
			if end := strings.IndexByte(text, 'm'); end >= 0 {
				text = text[end+1:]
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		width++
	}
	return width
}

func tableHeader(style humanStyle, labels ...string) []string {
	row := make([]string, len(labels))
	for i, label := range labels {
		row[i] = style.tablePaint(semanticCyanBold, label)
	}
	return row
}

func tableStatus(style humanStyle, status string) string {
	return style.tablePaint(statusColor(status), status)
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
	return renderHumanStyled(out, result, humanStyle{})
}

func renderHumanStyled(out io.Writer, result any, style humanStyle) error {
	switch value := result.(type) {
	case inspectionResult:
		return renderInspection(out, value, style)
	case overviewResult:
		return renderOverview(out, value, style)
	case vcm.InitResult:
		_, err := fmt.Fprintf(out, "Configuration: %s\nTrunk: %s\nConfigure children, then run vcm bootstrap.\n", value.Path, value.Trunk)
		return err
	case vcm.RecoveryResult:
		_, err := fmt.Fprintf(out, "Hook: %s\nRetry: %s\n", value.Hook, value.RetryCommand)
		return err
	case validateResult:
		_, err := fmt.Fprintf(out, "Configuration %s.\n%s %s\n", style.paint(semanticGreen, "valid"), style.paint(semanticCyanBold, "Workspace:"), value.Workspace)
		return err
	case commandResult:
		_, err := fmt.Fprintf(out, "%s %s.\n%s %s\n", commandLabel(value.Command), style.paint(semanticGreen, "complete"), style.paint(semanticCyanBold, "Workspace:"), value.Workspace)
		return err
	case vcm.AuditReport:
		repositories := [][]string{tableHeader(style, "Repository", "State", "Origin")}
		for _, repository := range value.Repositories {
			state := "clean"
			if !repository.Clean {
				state = "issues"
			}
			repositories = append(repositories, []string{repository.Name, tableStatus(style, state), repository.Origin})
		}
		if err := renderTable(out, repositories); err != nil {
			return err
		}
		if len(value.Issues) == 0 {
			_, err := fmt.Fprintf(out, "\nWorkspace %s.\n", style.paint(semanticGreen, "clean"))
			return err
		}
		if _, err := fmt.Fprintf(out, "\n%s\n", style.paint(semanticRed, "Findings:")); err != nil {
			return err
		}
		issues := [][]string{tableHeader(style, "Repository", "Kind", "Target", "Detail")}
		for _, issue := range value.Issues {
			issues = append(issues, []string{issue.Repository, issue.Kind, auditIssueTarget(issue), issue.Detail})
		}
		return renderTable(out, issues)
	case vcm.PruneReport:
		actions := [][]string{tableHeader(style, "Repository", "Action", "Target", "Status", "Detail")}
		for _, action := range value.Actions {
			actions = append(actions, []string{action.Repository, action.Action, action.Target, tableStatus(style, action.Status), action.Detail})
		}
		if err := renderTable(out, actions); err != nil {
			return err
		}
		if len(value.RemainingIssues) == 0 {
			_, err := fmt.Fprintf(out, "\nPrune %s.\n", style.paint(semanticGreen, "complete"))
			return err
		}
		if _, err := fmt.Fprintf(out, "\nRemaining %s\n", style.paint(semanticRed, "findings:")); err != nil {
			return err
		}
		issues := [][]string{tableHeader(style, "Repository", "Kind", "Target", "Detail")}
		for _, issue := range value.RemainingIssues {
			issues = append(issues, []string{issue.Repository, issue.Kind, auditIssueTarget(issue), issue.Detail})
		}
		return renderTable(out, issues)
	case createResult:
		_, err := fmt.Fprintf(out, "%s Change %s with %d repositories.\n%s %s\n", style.paint(semanticGreen, "Created"), value.Tag, len(value.Repositories), style.paint(semanticCyanBold, "Workspace:"), value.Workspace)
		return err
	case statusResult:
		if _, err := fmt.Fprintf(out, "%s %s\n%s %s\n%s %s\n%s %s\n\n", style.paint(semanticCyanBold, "Change:"), value.Tag, style.paint(semanticCyanBold, "State:"), style.paint(statusColor(value.State), value.State), style.paint(semanticCyanBold, "Created:"), value.CreatedAt.Format(time.RFC3339), style.paint(semanticCyanBold, "Workspace:"), value.Workspace); err != nil {
			return err
		}
		repositories := [][]string{tableHeader(style, "Repository", "Status", "HEAD", "Recorded target", "Current target", "Recovery", "Detail")}
		for _, repository := range value.Repositories {
			repositories = append(repositories, []string{repository.Name, tableStatus(style, repository.Status), abbreviated(repository.Head), abbreviated(repository.RecordedTarget), abbreviated(repository.Target), repository.RecoveryState, repository.Error})
		}
		if err := renderTable(out, repositories); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "\n%s\n", style.paint(semanticCyanBold, "Hooks:")); err != nil {
			return err
		}
		hooks := [][]string{tableHeader(style, "Repository", "Phase", "Hook", "Status", "Error")}
		for _, hook := range value.Hooks {
			hooks = append(hooks, []string{hook.Repository, hook.Phase, hook.ID, tableStatus(style, hook.Status), hook.Error})
		}
		if err := renderTable(out, hooks); err != nil {
			return err
		}
		if len(value.Backups) > 0 {
			fmt.Fprintf(out, "\n%s\n", style.paint(semanticCyanBold, "Backups:"))
			for _, backup := range value.Backups {
				fmt.Fprintf(out, "  %s\n", backup)
			}
			fmt.Fprintf(out, "%s %s\n", style.paint(semanticCyanBold, "Recovery directory:"), value.RecoveryDirectory)
		}
		if len(value.PendingSync) > 0 {
			fmt.Fprintf(out, "\n%s\n", style.paint(semanticYellow, "Pending synchronization:"))
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
		_, err := fmt.Fprintf(out, "%s Change %s (%d/%d repositories).\n", style.paint(semanticGreen, "Merged"), value.Tag, merged, len(value.Repositories))
		return err
	case refreshResult:
		_, err := fmt.Fprintf(out, "%s Change %s (%d repositories).\n", style.paint(semanticGreen, "Refreshed"), value.Tag, len(value.Repositories))
		return err
	case dropResult:
		removed := 0
		for _, repository := range value.Repositories {
			if repository.Removed {
				removed++
			}
		}
		_, err := fmt.Fprintf(out, "%s Change %s (%d/%d repositories removed, %d recovery backups).\n", style.paint(semanticGreen, "Dropped"), value.Tag, removed, len(value.Repositories), len(value.Backups))
		return err
	case versionResult:
		_, err := fmt.Fprintf(out, "vcm %s (%s)\n", value.Version, value.Commit)
		return err
	case dryRunResult:
		for _, step := range value.Steps {
			fmt.Fprintf(out, "[%s] %s / %s: %s (%s)\n", step.Checkpoint, step.Repository, step.Phase, step.Effect, step.Target)
		}
		for _, blocker := range value.Blockers {
			fmt.Fprintf(out, "Blocked: %s\n", blocker)
		}
		for _, unverified := range value.Unverified {
			fmt.Fprintf(out, "Unverified: %s\n", unverified)
		}
		if _, err := fmt.Fprintf(out, "%s %s\n", style.paint(semanticYellow, "Dry run:"), value.Command); err != nil {
			return err
		}
		if value.Command == "drop" {
			fmt.Fprintf(out, "%s %t\n", style.paint(semanticCyanBold, "Force:"), value.Force)
		}
		if value.Command == "merge" {
			fmt.Fprintf(out, "Ignore hook failures: %t\n", value.IgnoreHookFailures)
			fmt.Fprintf(out, "%s %t\n", style.paint(semanticCyanBold, "Delete ignored content:"), value.DeletesIgnoredContent)
			fmt.Fprintf(out, "%s %s\n", style.paint(semanticCyanBold, "Skipped hook phases:"), strings.Join(value.SkippedHookPhases, ","))
			fmt.Fprintf(out, "%s %t\n", style.paint(semanticCyanBold, "Git hooks inside lifecycle hooks suppressed:"), value.SkipHookGitHooks)
		}
		if value.Tag != "" {
			fmt.Fprintf(out, "%s %s\n", style.paint(semanticCyanBold, "Change:"), value.Tag)
		}
		if value.Workspace != "" {
			fmt.Fprintf(out, "%s %s\n", style.paint(semanticCyanBold, "Workspace:"), value.Workspace)
		}
		fmt.Fprintln(out, style.paint(semanticCyanBold, "Resources:"))
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
	writeErrorStyled(out, jsonOutput, err, humanStyle{})
}

func writeErrorStyled(out io.Writer, jsonOutput bool, err error, style humanStyle) {
	var detail *operationError
	if !errors.As(err, &detail) {
		detail = &operationError{Code: "usage", Message: err.Error()}
		var typed *vcm.Error
		if errors.As(err, &typed) {
			detail.Code, detail.Repository = typed.Code, typed.Repository
		}
	}
	if jsonOutput {
		_ = renderJSON(out, struct {
			Error *operationError `json:"error"`
		}{detail})
		return
	}
	fmt.Fprintln(out, style.paint(semanticRed, "vcm:"), detail.Message)
	for _, p := range detail.Progress {
		if p.Publication != "" {
			fmt.Fprintf(out, "%s: publication %s\n", p.Repository, p.Publication)
			continue
		}
		fmt.Fprintf(out, "%s: integration %s; cleanup %s\n", p.Repository, p.Integration, p.Cleanup)
	}
	if detail.Finalization != "" {
		fmt.Fprintln(out, "Finalization:", detail.Finalization)
	}
	if detail.NextAction != "" {
		fmt.Fprintln(out, detail.NextAction)
	}
}
