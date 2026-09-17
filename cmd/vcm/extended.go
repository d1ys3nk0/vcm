package main

import (
	"encoding/json"
	"fmt"
	"github.com/d1ys3nk0/vcm/internal/vcm"
	"io"
	"os"
	"strings"
)

func runExtended(e *vcm.Engine, command, arg, workspace, cwd string, o options, output func(any) error) (bool, error) {
	switch command {
	case "add", "diff", "publish", "export", "restore", "cleanup":
	default:
		return false, nil
	}
	var m *vcm.Manifest
	var snapshot vcm.Snapshot
	var err error
	if command == "restore" {
		file, err := os.Open(arg)
		if err != nil {
			return true, err
		}
		defer file.Close()
		decoder := json.NewDecoder(file)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&snapshot); err != nil {
			return true, err
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return true, fmt.Errorf("snapshot must contain exactly one JSON object")
		}
	} else {
		m, err = e.Select(arg, workspace)
		if err != nil {
			return true, err
		}
	}
	if command == "diff" {
		report, err := e.Diff(m, vcm.DiffOptions{Only: o.only, Committed: o.committed, Patch: o.patch})
		if outErr := output(report); outErr != nil {
			return true, outErr
		}
		if err != nil {
			return true, incompleteResultError{command: command}
		}
		return true, nil
	}
	if command == "export" {
		snapshot, err := e.Export(m)
		if err != nil {
			return true, err
		}
		return true, renderJSON(os.Stdout, snapshot)
	}
	if o.dry {
		var plan vcm.OperationPlan
		switch command {
		case "add":
			plan = e.AddPlan(m, o.only)
		case "publish":
			plan = e.PublishPlan(m)
		case "restore":
			plan = e.RestorePlan(&snapshot, o.name, o.fetch)
		case "cleanup":
			plan = e.CleanupPlan(m)
		}
		if err := output(newDryRunResult(plan)); err != nil {
			return true, err
		}
		if len(plan.Blockers) > 0 {
			return true, incompleteResultError{command: command}
		}
		return true, nil
	}
	err = e.Mutate(func() error {
		if command != "restore" {
			m, err = e.Select(arg, workspace)
			if err != nil {
				return err
			}
		}
		switch command {
		case "add":
			return e.Add(m, o.only)
		case "publish":
			return e.Publish(m)
		case "restore":
			m, err = e.Restore(&snapshot, o.name, o.fetch)
			return err
		case "cleanup":
			return e.Cleanup(m)
		}
		return nil
	})
	if command == "cleanup" && !o.noCD && !o.json && m != nil && withinPath(cwd, m.Workspace) {
		if _, statErr := os.Stat(cwd); os.IsNotExist(statErr) {
			if navErr := navigate(e.Root); err == nil {
				err = navErr
			}
		}
	}
	if err != nil {
		failure := operationFailure(command, m, err).(*operationError)
		if command == "add" {
			only := o.only
			if m != nil && len(m.Expansion) > 0 {
				only = strings.Join(m.Expansion, ",")
			}
			failure.NextAction += " --only " + shellQuote(only)
		}
		if command == "restore" {
			failure.NextAction = "Inspect and repair the reported failure, then rerun: vcm --workspace " + shellQuote(e.Root) + " restore " + shellQuote(arg) + " --name " + shellQuote(o.name)
			if o.fetch {
				failure.NextAction += " --fetch"
			}
		}
		return true, failure
	}
	if command == "add" || command == "restore" {
		return true, output(newCreateResult(m))
	}
	if command == "publish" {
		return true, output(newPublicationResult(m))
	}
	return true, output(commandResult{Command: command, Complete: true, Workspace: e.Root})
}

type publicationRepositoryResult struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
}
type publicationResult struct {
	Change       string                        `json:"change"`
	Repositories []publicationRepositoryResult `json:"repositories"`
}

func newPublicationResult(m *vcm.Manifest) publicationResult {
	result := publicationResult{Change: m.Tag, Repositories: []publicationRepositoryResult{}}
	for i := 1; i <= len(m.Repositories); i++ {
		r := m.Repositories[i%len(m.Repositories)]
		result.Repositories = append(result.Repositories, publicationRepositoryResult{Name: r.Repository.Name, Ref: "refs/heads/" + m.Tag, SHA: m.Published[r.Repository.Name]})
	}
	return result
}
