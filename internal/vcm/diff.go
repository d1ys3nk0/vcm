package vcm

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type DiffOptions struct {
	Committed bool
	Patch     bool
	Only      string
}

type DiffFile struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary"`
}

type DiffCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

type DiffRepository struct {
	Name      string       `json:"name"`
	Path      string       `json:"path"`
	Available bool         `json:"available"`
	Base      string       `json:"base"`
	Source    string       `json:"source"`
	Files     []DiffFile   `json:"files"`
	Commits   []DiffCommit `json:"commits"`
	Untracked []string     `json:"untracked"`
	Patch     string       `json:"patch,omitempty"`
	Error     string       `json:"error,omitempty"`
}

type DiffReport struct {
	Change       string           `json:"change"`
	Committed    bool             `json:"committed"`
	Repositories []DiffRepository `json:"repositories"`
}

// Diff compares recorded baselines to committed or current tracked contents.
// Untracked files are reported by name only. Git inspection takes no optional locks.
func (e *Engine) Diff(m *Manifest, opts DiffOptions) (DiffReport, error) {
	report := DiffReport{Committed: opts.Committed, Repositories: []DiffRepository{}}
	if m == nil {
		return report, fmt.Errorf("diff requires a Change")
	}
	report.Change = m.Tag
	selected := map[string]bool{}
	if opts.Only != "" {
		for _, name := range strings.Split(opts.Only, ",") {
			if name == "" || name != strings.TrimSpace(name) {
				return report, fmt.Errorf("--only contains a blank or padded repository name")
			}
			if selected[name] {
				return report, fmt.Errorf("--only contains duplicate repository %s", name)
			}
			found := false
			for _, r := range m.Repositories {
				if r.Repository.Name == name {
					found = true
					break
				}
			}
			for _, missing := range m.Missing {
				if missing == name {
					found = true
				}
			}
			if !found {
				return report, fmt.Errorf("repository %q is not selected in Change %s", name, m.Tag)
			}
			selected[name] = true
		}
	}
	var failures []error
	for i := range m.Repositories {
		r := &m.Repositories[i]
		if len(selected) > 0 && !selected[r.Repository.Name] {
			continue
		}
		item := DiffRepository{Name: r.Repository.Name, Path: r.Path, Base: r.Base, Files: []DiffFile{}, Commits: []DiffCommit{}, Untracked: []string{}}
		err := e.inspectDiff(m, r, opts, &item)
		if err != nil {
			item.Error = err.Error()
			failures = append(failures, fmt.Errorf("%s: %w", item.Name, err))
		}
		report.Repositories = append(report.Repositories, item)
	}
	return report, errors.Join(failures...)
}

func (e *Engine) inspectDiff(m *Manifest, r *RepoState, opts DiffOptions, item *DiffRepository) error {
	for _, name := range m.Missing {
		if r.Repository.Name == name {
			return fmt.Errorf("selected repository is absent from current configuration")
		}
	}
	if r.Removed || !r.Owned {
		return fmt.Errorf("Change worktree is unavailable")
	}
	if err := e.owned(m, r); err != nil {
		return err
	}
	source, err := head(r.Path)
	if err != nil {
		return err
	}
	item.Available, item.Source = true, source
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--find-renames", "--numstat", "-z", r.Base}
	if opts.Committed {
		args = append(args, source)
	}
	args = append(args, "--")
	output, err := gitRaw(r.Path, args...)
	if err != nil {
		return err
	}
	item.Files, err = parseDiffStats(output)
	if err != nil {
		return err
	}
	if opts.Patch {
		args = append([]string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--find-renames", "--patch"}, args[7:]...)
		item.Patch, err = gitRaw(r.Path, args...)
		if err != nil {
			return err
		}
	}
	output, err = gitRaw(r.Path, "log", "--format=%H%x00%s%x00", r.Base+".."+source, "--")
	if err != nil {
		return err
	}
	records := strings.Split(output, "\x00")
	for i := 0; i+1 < len(records); i += 2 {
		item.Commits = append(item.Commits, DiffCommit{SHA: strings.TrimLeft(records[i], "\n"), Subject: records[i+1]})
	}
	if !opts.Committed {
		output, err = gitRaw(r.Path, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return err
		}
		if output != "" {
			item.Untracked = strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
		}
	}
	return nil
}

func parseDiffStats(output string) ([]DiffFile, error) {
	files := []DiffFile{}
	records := strings.Split(output, "\x00")
	for i := 0; i < len(records); i++ {
		if records[i] == "" {
			continue
		}
		fields := strings.SplitN(records[i], "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected Git diff statistics")
		}
		file := DiffFile{Path: fields[2], Binary: fields[0] == "-" || fields[1] == "-"}
		if file.Path == "" {
			if i+2 >= len(records) {
				return nil, fmt.Errorf("incomplete Git rename statistics")
			}
			file.OldPath, file.Path = records[i+1], records[i+2]
			i += 2
		}
		if !file.Binary {
			var err error
			file.Added, err = strconv.Atoi(fields[0])
			if err != nil {
				return nil, err
			}
			file.Deleted, err = strconv.Atoi(fields[1])
			if err != nil {
				return nil, err
			}
		}
		files = append(files, file)
	}
	return files, nil
}
