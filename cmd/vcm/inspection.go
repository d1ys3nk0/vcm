package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/d1ys3nk0/vcm/internal/vcm"
)

type pathResult struct {
	Path string `json:"path"`
}
type inspectionResult struct {
	treeResult
	Lifecycle *statusResult `json:"lifecycle,omitempty"`
	verbose   bool
}
type overviewEntry struct {
	listResult
	Dirty      *int                   `json:"dirty_repositories,omitempty"`
	BaseUpdate string                 `json:"base_update"`
	Operation  string                 `json:"operation"`
	Inspection []treeRepositoryResult `json:"repositories"`
}
type overviewResult struct {
	Changes []overviewEntry `json:"changes"`
	verbose bool
}

func inspectStatus(e *vcm.Engine, selector, context string, verbose bool) (inspectionResult, error) {
	result := inspectionResult{verbose: verbose}
	var m *vcm.Manifest
	if selector != "" {
		selected, err := e.Select(selector, context)
		if err != nil {
			return result, err
		}
		m = selected
		context = m.Workspace
	}
	report, err := e.Tree(context)
	if err != nil {
		return result, err
	}
	result.treeResult = newTreeResult(report)
	if report.Context == "change" {
		if m == nil {
			m, err = e.Select(report.Change, context)
			if err != nil {
				return result, err
			}
		}
		lifecycle := newStatusResult(m, e.Status(m))
		if m.State == "dropped" {
			lifecycle.State = operationLabel(m)
		}
		result.Lifecycle = &lifecycle
	}
	return result, nil
}
func inspectionFailed(rows []treeRepositoryResult) bool {
	for _, r := range rows {
		if r.TreeState == "error" || r.TreeState == "missing" || r.SyncState != nil && *r.SyncState == "error" {
			return true
		}
	}
	return false
}
func operationLabel(m *vcm.Manifest) string {
	switch m.State {
	case "ready":
		return "active"
	case "dropped":
		for _, r := range m.Repositories {
			if !r.Merged {
				return "discarded"
			}
		}
		return "merged"
	default:
		return m.State
	}
}
func inspectList(e *vcm.Engine, context string, all, verbose bool) (overviewResult, error) {
	result := overviewResult{Changes: []overviewEntry{}, verbose: verbose}
	manifests, err := e.All()
	if err != nil {
		return result, err
	}
	lookup := map[string]*vcm.Manifest{}
	for _, m := range manifests {
		lookup[m.Tag] = m
	}
	if absolute, err := filepath.Abs(context); err == nil {
		context = absolute
	}
	if resolved, err := filepath.EvalSymlinks(context); err == nil {
		context = resolved
	}
	for _, summary := range newListResults(manifests, context) {
		m := lookup[summary.Tag]
		if !all && m.State == "dropped" {
			continue
		}
		entry := overviewEntry{listResult: summary, Operation: operationLabel(m), BaseUpdate: "current"}
		report, err := e.Tree(m.Workspace)
		if err != nil {
			return result, err
		}
		entry.Inspection = newTreeResult(report).Repositories
		dirty := 0
		for _, r := range report.Repositories {
			if r.TreeState == "changed" {
				dirty++
			}
			if r.Behind > 0 {
				entry.BaseUpdate = "needed"
			}
		}
		if m.State == "dropped" {
			entry.BaseUpdate = "not applicable"
			entry.Dirty = nil
		} else if inspectionFailed(entry.Inspection) {
			entry.BaseUpdate = "unknown"
		} else {
			entry.Dirty = &dirty
		}
		result.Changes = append(result.Changes, entry)
	}
	return result, nil
}
func renderInspection(out io.Writer, v inspectionResult, style humanStyle) error {
	if v.Lifecycle != nil {
		fmt.Fprintf(out, "Change: %s (%s)\n", v.Lifecycle.Slug, humanLifecycle(v.Lifecycle.State))
	} else {
		fmt.Fprintln(out, "Base workspace (remote comparisons use cached refs)")
	}
	rows := [][]string{tableHeader(style, "Repository", "Branch", "Working tree", "Comparison target", "Ahead", "Behind")}
	unselected := 0
	for _, r := range v.Repositories {
		if r.TreeState == "not selected" && !v.verbose {
			unselected++
			continue
		}
		b, target, ahead, behind := "—", "—", "—", "—"
		if r.Branch != nil {
			b = *r.Branch
		}
		if r.SyncTarget != nil {
			target = *r.SyncTarget
			if r.Cached != nil && *r.Cached {
				target = "cached " + target
			} else {
				target = "local " + target
			}
		}
		if r.Ahead != nil {
			ahead = fmt.Sprint(*r.Ahead)
		}
		if r.Behind != nil {
			behind = fmt.Sprint(*r.Behind)
		}
		state := r.TreeState
		if state == "" {
			state = "unknown"
		}
		rows = append(rows, []string{r.Name, b, state, target, ahead, behind})
	}
	if err := renderTable(out, rows); err != nil {
		return err
	}
	if unselected > 0 {
		fmt.Fprintf(out, "%d repositories not selected; use --verbose for details.\n", unselected)
	}
	for _, r := range v.Repositories {
		if r.Detail != "" {
			fmt.Fprintf(out, "%s: %s\n", r.Name, r.Detail)
		}
	}
	if v.Lifecycle != nil {
		if v.Lifecycle.State != "ready" {
			completed, removed := 0, 0
			for _, r := range v.Lifecycle.Repositories {
				if r.Removed {
					removed++
				}
				if r.Merged {
					completed++
				}
			}
			fmt.Fprintf(out, "Operation: %s; integration %d/%d; cleanup %d/%d\n", humanLifecycle(v.Lifecycle.State), completed, len(v.Lifecycle.Repositories), removed, len(v.Lifecycle.Repositories))
		}
		for _, h := range v.Lifecycle.Hooks {
			if h.Status != "complete" {
				fmt.Fprintf(out, "Hook %s/%s/%s: %s %s\n", h.Repository, h.Phase, h.ID, h.Status, h.Error)
			}
		}
		if v.verbose {
			return renderHumanStyled(out, *v.Lifecycle, style)
		}
	}
	if v.verbose {
		for _, r := range v.Repositories {
			fmt.Fprintf(out, "%s: %s\n", r.Name, r.Path)
		}
	}
	return nil
}
func humanLifecycle(state string) string {
	if state == "ready" {
		return "active"
	}
	return state
}
func renderOverview(out io.Writer, v overviewResult, style humanStyle) error {
	if len(v.Changes) == 0 {
		_, err := fmt.Fprintln(out, "No Changes.")
		return err
	}
	counts := map[string]int{}
	for _, c := range v.Changes {
		counts[c.Slug]++
	}
	rows := [][]string{tableHeader(style, "", "Change", "Repos", "Dirty", "Base update", "Operation", "Age")}
	for _, c := range v.Changes {
		marker := ""
		if c.current {
			marker = "@"
		}
		name := c.Slug
		if counts[name] > 1 || v.verbose {
			name = c.Tag
		}
		dirty := "unknown"
		if c.Dirty != nil {
			dirty = fmt.Sprint(*c.Dirty)
		}
		rows = append(rows, []string{marker, name, fmt.Sprint(c.RepositoryCount), dirty, c.BaseUpdate, c.Operation, age(time.Now(), c.CreatedAt)})
	}
	return renderTable(out, rows)
}

type progressEntry struct {
	Repository  string `json:"repository"`
	Integration string `json:"integration,omitempty"`
	Cleanup     string `json:"cleanup,omitempty"`
	Publication string `json:"publication,omitempty"`
}
type operationError struct {
	Code         string          `json:"code"`
	Message      string          `json:"message"`
	Repository   string          `json:"repository,omitempty"`
	Change       string          `json:"change,omitempty"`
	Command      string          `json:"command,omitempty"`
	Progress     []progressEntry `json:"progress,omitempty"`
	NextAction   string          `json:"next_action,omitempty"`
	Finalization string          `json:"finalization,omitempty"`
	cause        error
}

func (e *operationError) Error() string { return e.Message }
func (e *operationError) Unwrap() error { return e.cause }
func shellQuote(s string) string        { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func operationFailure(command string, m *vcm.Manifest, err error) error {
	result := &operationError{Code: "external_command", Message: err.Error(), Command: command, cause: err}
	var typed *vcm.Error
	if errors.As(err, &typed) {
		result.Code, result.Repository = typed.Code, typed.Repository
	}
	var publication *vcm.PublicationError
	if errors.As(err, &publication) {
		for _, name := range publication.Completed {
			result.Progress = append(result.Progress, progressEntry{Repository: name, Publication: "complete"})
		}
		result.Progress = append(result.Progress, progressEntry{Repository: publication.Blocked, Publication: "blocked"})
		for _, name := range publication.Pending {
			result.Progress = append(result.Progress, progressEntry{Repository: name, Publication: "pending"})
		}
		result.NextAction = "Inspect the reported remote failure and rerun vcm push; completed publications are not rolled back."
	}
	if m != nil {
		result.Change = m.Tag
		if command == "merge" {
			result.Finalization = "pending"
			if m.State == "merge-finalizing" {
				result.Finalization = "blocked"
			}
		}

		for _, r := range m.Repositories {
			p := progressEntry{Repository: r.Repository.Name, Integration: "pending", Cleanup: "pending"}
			if r.Merged {
				p.Integration = "complete"
			}
			if r.Removed {
				p.Cleanup = "complete"
			}
			if r.Repository.Name == result.Repository {
				if r.Merged && !r.Removed {
					p.Cleanup = "blocked"
				} else if !r.Merged {
					p.Integration = "blocked"
				}
			}
			result.Progress = append(result.Progress, p)
		}
		selector := m.Tag
		if command == "create" {
			selector = m.Slug
		}
		result.NextAction = "Inspect and repair the reported failure, then rerun: vcm --workspace " + shellQuote(m.Origin) + " " + command + " " + shellQuote(selector)
		if command == "create" {
			selected := []string{}
			for _, r := range m.Repositories {
				if r.Repository.Name != "root" {
					selected = append(selected, r.Repository.Name)
				}
			}
			if len(selected) > 0 {
				result.NextAction += " --only " + strings.Join(selected, ",")
			} else if len(m.Config.Children) > 0 {
				excluded := []string{}
				for _, r := range m.Config.Children {
					excluded = append(excluded, r.Name)
				}
				result.NextAction += " --except " + strings.Join(excluded, ",")
			}
		}
	}
	return result
}
